package doctor

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/felixgeelhaar/scry/internal/auth"
)

func TestServersFileCheckPasses(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.yml")
	s := &auth.Servers{Servers: map[string]auth.Server{
		"a": {Upstream: "https://example.com/graphql"},
	}}
	if err := auth.Save(s, path); err != nil {
		t.Fatalf("save: %v", err)
	}
	r := ServersFileCheck{Path: path}.Run(context.Background())
	if r.Status != StatusPass {
		t.Errorf("status = %s, want pass — %s", r.Status, r.Message)
	}
}

func TestServersFileCheckWarnsOnMissing(t *testing.T) {
	r := ServersFileCheck{Path: filepath.Join(t.TempDir(), "nope.yml")}.Run(context.Background())
	if r.Status != StatusWarn {
		t.Errorf("status = %s, want warn", r.Status)
	}
}

func TestServersFileCheckFailsOnInsecurePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm check skipped on windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.yml")
	if err := os.WriteFile(path, []byte("version: 1\nservers: {}\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	r := ServersFileCheck{Path: path}.Run(context.Background())
	if r.Status != StatusFail {
		t.Errorf("status = %s, want fail (perms)", r.Status)
	}
}

func TestAuditDirCheckDisabledPassesWhenNoPath(t *testing.T) {
	r := AuditDirCheck{Path: ""}.Run(context.Background())
	if r.Status != StatusPass {
		t.Errorf("status = %s, want pass for unset audit dir", r.Status)
	}
}

func TestAuditDirCheckFailsOnLoosePerms(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("perm check skipped on windows")
	}
	dir := filepath.Join(t.TempDir(), "audit")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	r := AuditDirCheck{Path: dir}.Run(context.Background())
	if r.Status != StatusFail {
		t.Errorf("status = %s, want fail (0755 dir)", r.Status)
	}
}

func TestOTelExporterCheckPassesWhenUnset(t *testing.T) {
	r := OTelExporterCheck{Endpoint: ""}.Run(context.Background())
	if r.Status != StatusPass {
		t.Errorf("status = %s, want pass", r.Status)
	}
}

func TestOTelExporterCheckProbesEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(204)
	}))
	defer srv.Close()
	r := OTelExporterCheck{Endpoint: srv.URL}.Run(context.Background())
	if r.Status != StatusPass {
		t.Errorf("status = %s, want pass — %s", r.Status, r.Message)
	}
}

func TestDoctorRunReturnsFailCount(t *testing.T) {
	d := &Doctor{
		Out: &bytes.Buffer{},
		Checks: []Check{
			fakeCheck{name: "ok", st: StatusPass},
			fakeCheck{name: "warn", st: StatusWarn},
			fakeCheck{name: "fail1", st: StatusFail},
			fakeCheck{name: "fail2", st: StatusFail},
		},
	}
	if got := d.Run(context.Background()); got != 2 {
		t.Errorf("fail count = %d, want 2", got)
	}
}

func TestDoctorRunRendersAllChecks(t *testing.T) {
	var buf bytes.Buffer
	d := &Doctor{
		Out: &buf,
		Checks: []Check{
			fakeCheck{name: "alpha", st: StatusPass, msg: "looks fine"},
			fakeCheck{name: "beta", st: StatusFail, msg: "broken thing", detail: "fix me"},
		},
	}
	_ = d.Run(context.Background())
	out := buf.String()
	for _, want := range []string{"alpha", "beta", "looks fine", "broken thing", "fix me", "verdict"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in output:\n%s", want, out)
		}
	}
}

type fakeCheck struct {
	name   string
	st     Status
	msg    string
	detail string
}

func (f fakeCheck) Name() string { return f.name }
func (f fakeCheck) Run(_ context.Context) Result {
	return Result{Name: f.name, Status: f.st, Message: f.msg, Detail: f.detail}
}
