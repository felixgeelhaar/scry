package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/felixgeelhaar/scry/internal/auth"
)

func runAuth(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: scry auth login|logout|status ...")
		os.Exit(2)
	}
	switch args[0] {
	case "login":
		authLogin(args[1:])
	case "logout":
		authLogout(args[1:])
	case "status":
		authStatus(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "scry: unknown auth command %q\n", args[0])
		os.Exit(2)
	}
}

// authLogin writes a bearer token for an existing server (or creates
// a new one when --upstream is also given). v0 is non-interactive
// only: pass --token explicitly so this composes cleanly with CI and
// password managers.
func authLogin(args []string) {
	fs := flag.NewFlagSet("auth login", flag.ContinueOnError)
	token := fs.String("token", "", "bearer token (required)")
	upstream := fs.String("upstream", "", "upstream GraphQL URL — required when registering a new server")
	expires := fs.Duration("expires", 0, "token TTL (e.g. 1h, 24h, 720h); leaves expires_at empty if zero")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: scry auth login <server> --token <T> [--upstream <url>] [--expires <duration>]")
		os.Exit(2)
	}
	name := fs.Arg(0)
	if *token == "" {
		die(fmt.Errorf("--token is required (v0 supports non-interactive bearer login only)"))
	}

	path, err := auth.DefaultPath()
	if err != nil {
		die(err)
	}
	s, err := auth.Load(path)
	if err != nil {
		die(err)
	}

	srv, exists := s.Servers[name]
	if !exists {
		if *upstream == "" {
			die(fmt.Errorf("server %q not registered — re-run with --upstream <url> to create it", name))
		}
		srv = auth.Server{Upstream: *upstream}
	} else if *upstream != "" {
		srv.Upstream = *upstream
	}
	srv.Auth = auth.Auth{Type: "bearer", Token: *token}
	if *expires > 0 {
		srv.Auth.ExpiresAt = time.Now().UTC().Add(*expires)
	}
	s.Upsert(name, srv)
	if err := s.Validate(); err != nil {
		die(err)
	}
	if err := auth.Save(s, path); err != nil {
		die(err)
	}
	fmt.Printf("logged in to %q (%s)\n", name, srv.Upstream)
	if !srv.Auth.ExpiresAt.IsZero() {
		fmt.Printf("expires at %s\n", srv.Auth.ExpiresAt.UTC().Format(time.RFC3339))
	}
}

// authLogout clears the auth block but keeps the server entry so the
// next `scry auth login` doesn't need to re-specify the upstream.
func authLogout(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: scry auth logout <server>")
		os.Exit(2)
	}
	name := args[0]
	path, err := auth.DefaultPath()
	if err != nil {
		die(err)
	}
	s, err := auth.Load(path)
	if err != nil {
		die(err)
	}
	srv, ok := s.Servers[name]
	if !ok {
		die(fmt.Errorf("server %q not found", name))
	}
	srv.Auth = auth.Auth{}
	s.Upsert(name, srv)
	if err := auth.Save(s, path); err != nil {
		die(err)
	}
	fmt.Printf("logged out of %q\n", name)
}

// authStatus prints the auth traffic light for one or all servers.
// Output is tabulated for the terminal; tokens never appear.
func authStatus(args []string) {
	path, err := auth.DefaultPath()
	if err != nil {
		die(err)
	}
	s, err := auth.Load(path)
	if err != nil {
		die(err)
	}
	entries := s.StatusAll(time.Now())
	if len(args) > 0 {
		want := args[0]
		filtered := entries[:0]
		for _, e := range entries {
			if e.Name == want {
				filtered = append(filtered, e)
			}
		}
		entries = filtered
		if len(entries) == 0 {
			die(fmt.Errorf("server %q not found", want))
		}
	}
	if len(entries) == 0 {
		fmt.Println("no servers configured")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATUS\tEXPIRES IN\tUPSTREAM")
	for _, e := range entries {
		expiresIn := "—"
		if e.ExpiresInSeconds > 0 {
			expiresIn = (time.Duration(e.ExpiresInSeconds) * time.Second).Round(time.Second).String()
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Name, e.Status, expiresIn, e.Upstream)
	}
	_ = w.Flush()
}
