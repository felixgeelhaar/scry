// Package doctor implements `scry doctor` — a one-shot diagnostic
// that probes the local configuration and runtime surface so
// operators can self-service smoke-test a fresh install before
// filing an issue.
//
// Design: each Check has a stable name, a humane Run() method, and
// returns a Result with status + message. Doctor.Run() collects them
// all, renders to stdout, and exits non-zero when at least one
// failed. Checks are independent and short-circuit only their own
// probe, so a missing servers.yml doesn't suppress the audit-dir
// check or the OTel reachability probe.
package doctor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/felixgeelhaar/scry/internal/auth"
)

// Status is the per-check verdict. Three values keep output scanable
// at a glance — pass / warn / fail.
type Status string

const (
	StatusPass Status = "pass"
	StatusWarn Status = "warn"
	StatusFail Status = "fail"
)

// Result is the typed output of one Check.
type Result struct {
	Name    string
	Status  Status
	Message string
	// Detail is optional extra context shown indented under the
	// one-line verdict. Right for paths, env values, or
	// suggested next commands.
	Detail string
}

// Check is one diagnostic probe.
type Check interface {
	Name() string
	Run(ctx context.Context) Result
}

// Doctor orchestrates a fixed set of checks against a Config.
type Doctor struct {
	Checks []Check
	Out    io.Writer
}

// Config captures the bits doctor needs to probe. Mirrors
// server.Config's relevant fields so callers can hand it across
// from main.
type Config struct {
	ServersPath string
	ClientsPath string
	AuditDir    string
}

// Default constructs a Doctor with the standard probe set. Callers
// can replace .Checks before .Run() for test injection.
func Default(cfg Config) *Doctor {
	return &Doctor{
		Out: os.Stdout,
		Checks: []Check{
			ServersFileCheck{Path: cfg.ServersPath},
			ClientsFileCheck{Path: cfg.ClientsPath},
			AuditDirCheck{Path: cfg.AuditDir},
			OTelExporterCheck{Endpoint: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")},
			UpstreamsReachableCheck{ServersPath: cfg.ServersPath},
		},
	}
}

// Run executes every Check, prints a formatted report, returns the
// count of fail results. Callers wire the count to exit code: 0 ==
// healthy.
func (d *Doctor) Run(ctx context.Context) int {
	fmt.Fprintln(d.Out, "scry doctor — probing configuration + runtime")
	fmt.Fprintln(d.Out, strings.Repeat("-", 60))

	results := make([]Result, 0, len(d.Checks))
	for _, c := range d.Checks {
		results = append(results, c.Run(ctx))
	}

	fails := 0
	for _, r := range results {
		fmt.Fprintf(d.Out, "  %-5s  %-22s  %s\n", strings.ToUpper(string(r.Status)), r.Name, r.Message)
		if r.Detail != "" {
			for _, line := range strings.Split(r.Detail, "\n") {
				fmt.Fprintf(d.Out, "         %s\n", line)
			}
		}
		if r.Status == StatusFail {
			fails++
		}
	}
	fmt.Fprintln(d.Out, strings.Repeat("-", 60))
	if fails == 0 {
		fmt.Fprintln(d.Out, "verdict: healthy")
	} else {
		fmt.Fprintf(d.Out, "verdict: %d failing check(s) — fix the FAIL lines above\n", fails)
	}
	return fails
}

// ServersFileCheck verifies servers.yml is loadable + 0600.
type ServersFileCheck struct{ Path string }

func (c ServersFileCheck) Name() string { return "servers.yml" }
func (c ServersFileCheck) Run(_ context.Context) Result {
	if c.Path == "" {
		return Result{Name: c.Name(), Status: StatusWarn, Message: "no path resolved (XDG_CONFIG_HOME unset?)"}
	}
	info, err := os.Stat(c.Path)
	if os.IsNotExist(err) {
		return Result{Name: c.Name(), Status: StatusWarn, Message: "absent — single-upstream mode only",
			Detail: "expected at " + c.Path}
	}
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Message: err.Error()}
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return Result{Name: c.Name(), Status: StatusFail,
			Message: fmt.Sprintf("insecure perms %o", info.Mode().Perm()),
			Detail:  "chmod 600 " + c.Path}
	}
	s, err := auth.Load(c.Path)
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Message: "parse: " + err.Error()}
	}
	if err := s.Validate(); err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Message: "validate: " + err.Error()}
	}
	return Result{Name: c.Name(), Status: StatusPass,
		Message: fmt.Sprintf("%d server(s) registered", len(s.Servers))}
}

// ClientsFileCheck mirrors ServersFileCheck for the optional
// clients.yml.
type ClientsFileCheck struct{ Path string }

func (c ClientsFileCheck) Name() string { return "clients.yml" }
func (c ClientsFileCheck) Run(_ context.Context) Result {
	if c.Path == "" {
		return Result{Name: c.Name(), Status: StatusWarn, Message: "no path resolved"}
	}
	info, err := os.Stat(c.Path)
	if os.IsNotExist(err) {
		return Result{Name: c.Name(), Status: StatusPass, Message: "absent (admin-only auth)"}
	}
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Message: err.Error()}
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return Result{Name: c.Name(), Status: StatusFail,
			Message: fmt.Sprintf("insecure perms %o", info.Mode().Perm()),
			Detail:  "chmod 600 " + c.Path}
	}
	cs, err := auth.LoadClients(c.Path)
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Message: "parse: " + err.Error()}
	}
	if err := cs.Validate(); err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Message: "validate: " + err.Error()}
	}
	names := cs.Names()
	sort.Strings(names)
	return Result{Name: c.Name(), Status: StatusPass,
		Message: fmt.Sprintf("%d client(s): %s", len(names), strings.Join(names, ", "))}
}

// AuditDirCheck verifies the audit directory exists with 0700 perms
// + child files (when present) are 0600.
type AuditDirCheck struct{ Path string }

func (c AuditDirCheck) Name() string { return "audit dir" }
func (c AuditDirCheck) Run(_ context.Context) Result {
	if c.Path == "" {
		return Result{Name: c.Name(), Status: StatusPass, Message: "disabled (in-memory audit only)"}
	}
	info, err := os.Stat(c.Path)
	if os.IsNotExist(err) {
		return Result{Name: c.Name(), Status: StatusWarn,
			Message: "absent — will be created on first audit write",
			Detail:  "expected at " + c.Path}
	}
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Message: err.Error()}
	}
	if !info.IsDir() {
		return Result{Name: c.Name(), Status: StatusFail,
			Message: "not a directory: " + c.Path}
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return Result{Name: c.Name(), Status: StatusFail,
			Message: fmt.Sprintf("dir perms %o (want 0700)", info.Mode().Perm()),
			Detail:  "chmod 700 " + c.Path}
	}
	// Spot-check a couple of files for 0600 perms.
	entries, err := os.ReadDir(c.Path)
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Message: err.Error()}
	}
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		fi, err := ent.Info()
		if err != nil {
			continue
		}
		if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
			return Result{Name: c.Name(), Status: StatusFail,
				Message: fmt.Sprintf("file %s has perms %o (want 0600)", ent.Name(), fi.Mode().Perm()),
				Detail:  "chmod 600 " + c.Path + "/" + ent.Name()}
		}
	}
	return Result{Name: c.Name(), Status: StatusPass,
		Message: fmt.Sprintf("ok (%d file(s))", len(entries))}
}

// OTelExporterCheck probes OTEL_EXPORTER_OTLP_ENDPOINT reachability.
// Pass when unset (tracing disabled) or when a TCP dial to the
// endpoint succeeds.
type OTelExporterCheck struct{ Endpoint string }

func (c OTelExporterCheck) Name() string { return "OTel exporter" }
func (c OTelExporterCheck) Run(ctx context.Context) Result {
	if c.Endpoint == "" {
		return Result{Name: c.Name(), Status: StatusPass, Message: "disabled (OTEL_EXPORTER_OTLP_ENDPOINT unset)"}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probeCtx, "GET", c.Endpoint, nil)
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail, Message: err.Error()}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Result{Name: c.Name(), Status: StatusFail,
			Message: "unreachable: " + err.Error(),
			Detail:  "endpoint: " + c.Endpoint}
	}
	defer func() { _ = resp.Body.Close() }()
	return Result{Name: c.Name(), Status: StatusPass,
		Message: fmt.Sprintf("reachable (HTTP %d)", resp.StatusCode)}
}

// UpstreamsReachableCheck probes each configured upstream's URL with
// a HEAD request. Doesn't validate the GraphQL schema — just that
// TCP/TLS works. Failure mode is informational; many upstreams 405
// HEAD and that still counts as reachable.
type UpstreamsReachableCheck struct{ ServersPath string }

func (c UpstreamsReachableCheck) Name() string { return "upstreams" }
func (c UpstreamsReachableCheck) Run(ctx context.Context) Result {
	s, err := auth.Load(c.ServersPath)
	if err != nil {
		return Result{Name: c.Name(), Status: StatusWarn, Message: "servers.yml unloadable; skipping"}
	}
	if len(s.Servers) == 0 {
		return Result{Name: c.Name(), Status: StatusPass, Message: "no servers configured to probe"}
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	var fails []string
	var details []string
	for name, srv := range s.Servers {
		req, err := http.NewRequestWithContext(probeCtx, "HEAD", srv.Upstream, nil)
		if err != nil {
			fails = append(fails, name)
			details = append(details, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fails = append(fails, name)
			details = append(details, fmt.Sprintf("%s: %v", name, err))
			continue
		}
		_ = resp.Body.Close()
		details = append(details, fmt.Sprintf("%s: HTTP %d", name, resp.StatusCode))
	}
	if len(fails) > 0 {
		return Result{Name: c.Name(), Status: StatusFail,
			Message: fmt.Sprintf("%d/%d upstream(s) unreachable: %s", len(fails), len(s.Servers), strings.Join(fails, ", ")),
			Detail:  strings.Join(details, "\n")}
	}
	return Result{Name: c.Name(), Status: StatusPass,
		Message: fmt.Sprintf("%d/%d reachable", len(s.Servers), len(s.Servers)),
		Detail:  strings.Join(details, "\n")}
}
