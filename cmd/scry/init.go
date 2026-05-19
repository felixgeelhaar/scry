package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/felixgeelhaar/scry/internal/auth"
	"github.com/felixgeelhaar/scry/internal/runtime"
)

// runInit implements `scry init`. Two modes:
//
//   - Interactive: prompts for upstream URL + token + name.
//   - Non-interactive: --upstream + (--token | --no-token) + --yes.
//
// Both write to $XDG_CONFIG_HOME/scry/servers.yml at mode 0600. Re-
// running detects existing entries — interactive mode prompts
// replace/add/cancel; non-interactive overwrites cleanly when --yes
// is passed.
//
// Returns a process exit code; main.go's run() wraps this in
// os.Exit so deferred tracer shutdown can flush.
func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	upstreamFlag := fs.String("upstream", "", "upstream GraphQL URL (skips prompt when set)")
	tokenFlag := fs.String("token", "", "auth token or token-ref (env://VAR, file://path, op://...)")
	nameFlag := fs.String("name", "default", "server name in servers.yml")
	noToken := fs.Bool("no-token", false, "skip the token prompt (auth-less upstream)")
	yes := fs.Bool("yes", false, "non-interactive: accept defaults + overwrite existing entries")
	skipProbe := fs.Bool("skip-probe", false, "skip the introspection probe (use for upstreams that disable introspection)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	stdin := bufio.NewReader(os.Stdin)
	stdout := os.Stdout

	// Resolve target servers.yml path.
	path, err := auth.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, "scry init:", err)
		return 1
	}
	store, err := auth.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "scry init: load existing servers.yml:", err)
		return 1
	}

	// Gather upstream URL.
	upstream := *upstreamFlag
	if upstream == "" {
		if *yes {
			fmt.Fprintln(os.Stderr, "scry init: --upstream is required in non-interactive mode")
			return 2
		}
		upstream, err = promptLine(stdin, stdout, "Upstream GraphQL URL: ")
		if err != nil {
			return 1
		}
	}
	if _, err := url.ParseRequestURI(upstream); err != nil {
		fmt.Fprintln(os.Stderr, "scry init: invalid upstream URL:", err)
		return 1
	}

	// Gather name.
	name := *nameFlag
	if !*yes && *nameFlag == "default" && *upstreamFlag == "" {
		// Interactive mode + default name not explicitly set →
		// offer a chance to override.
		entered, err := promptLine(stdin, stdout, fmt.Sprintf("Server name [%s]: ", name))
		if err != nil {
			return 1
		}
		if entered != "" {
			name = entered
		}
	}

	// Token.
	token := *tokenFlag
	if token == "" && !*noToken {
		if *yes {
			// Non-interactive without --token or --no-token →
			// fail loud so callers don't get a silent token-less
			// install.
			fmt.Fprintln(os.Stderr, "scry init: pass --token <ref> OR --no-token in non-interactive mode")
			return 2
		}
		token, err = promptLine(stdin, stdout, "Auth token (or env://VAR / file://path / blank for none): ")
		if err != nil {
			return 1
		}
	}

	// Idempotency: existing entry under this name → prompt or
	// overwrite. Interactive cancel exits without writing.
	if _, hasExisting := store.Servers[name]; hasExisting {
		if !*yes {
			fmt.Fprintf(stdout, "Server %q already exists. Replace? [y/N]: ", name)
			reply, err := stdin.ReadString('\n')
			if err != nil && err != io.EOF {
				return 1
			}
			reply = strings.ToLower(strings.TrimSpace(reply))
			if reply != "y" && reply != "yes" {
				fmt.Fprintln(stdout, "cancelled.")
				return 0
			}
		}
	}

	// Persist before the probe — probe failures shouldn't leave
	// the operator without a configured entry. Probe surfaces as
	// a warning, not a fatal error.
	authType := "bearer"
	if token == "" {
		authType = "none"
	}
	store.Upsert(name, auth.Server{
		Upstream: upstream,
		Auth: auth.Auth{
			Type:  authType,
			Token: token,
		},
	})
	if err := auth.Save(store, path); err != nil {
		fmt.Fprintln(os.Stderr, "scry init: save:", err)
		return 1
	}
	fmt.Fprintf(stdout, "wrote %s (mode 0600)\n", path)

	// Optional introspection probe so operators learn at init
	// time whether scry can talk to the upstream, instead of at
	// first agent call.
	if !*skipProbe {
		if probeErr := probeIntrospection(context.Background(), upstream, token); probeErr != nil {
			fmt.Fprintf(stdout, "warning: introspection probe failed: %v\n", probeErr)
			fmt.Fprintln(stdout, "         the entry is saved; fix upstream/auth before `scry serve`.")
		} else {
			fmt.Fprintln(stdout, "introspection probe succeeded.")
		}
	}
	return 0
}

// promptLine emits prompt to out, reads one line from in, trims, and
// returns it. Empty input returns the empty string — caller decides
// what to do with it.
func promptLine(in *bufio.Reader, out io.Writer, prompt string) (string, error) {
	if _, err := fmt.Fprint(out, prompt); err != nil {
		return "", err
	}
	line, err := in.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// probeIntrospection issues a one-shot introspection request against
// the upstream so the operator gets immediate feedback on
// reachability + auth. Uses the same code path scry serve does at
// boot — if this fails, serve will too.
func probeIntrospection(ctx context.Context, upstream, token string) error {
	indexDir, err := os.MkdirTemp("", "scry-init-probe-*")
	if err != nil {
		return fmt.Errorf("temp index dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(indexDir) }()
	mgr, err := runtime.New(indexDir, 1000)
	if err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	defer func() { _ = mgr.Close() }()
	return mgr.Add(ctx, runtime.AddConfig{
		Name:     "probe",
		Upstream: upstream,
		AuthRef:  token,
	})
}
