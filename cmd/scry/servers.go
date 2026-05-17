package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/felixgeelhaar/scry/internal/auth"
)

func runServers(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: scry servers list|add|remove ...")
		os.Exit(2)
	}
	switch args[0] {
	case "list":
		serversList(args[1:])
	case "add":
		serversAdd(args[1:])
	case "remove":
		serversRemove(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "scry: unknown servers command %q\n", args[0])
		os.Exit(2)
	}
}

// serversList prints one row per configured server: name, upstream
// URL, auth-status traffic light. Tokens are never printed.
func serversList(_ []string) {
	path, err := auth.DefaultPath()
	if err != nil {
		die(err)
	}
	s, err := auth.Load(path)
	if err != nil {
		die(err)
	}
	if len(s.Servers) == 0 {
		fmt.Println("no servers configured — add one with `scry servers add <name> --upstream <url>`")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tUPSTREAM\tTYPE\tSTATUS\tEXPIRES")
	for _, e := range s.StatusAll(time.Now()) {
		expiry := e.ExpiresAt
		if expiry == "" {
			expiry = "—"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", e.Name, e.Upstream, e.Type, e.Status, expiry)
	}
	_ = w.Flush()
}

// serversAdd registers a server entry. Auth is left empty — operator
// runs `scry auth login` next to set the token.
func serversAdd(args []string) {
	fs := flag.NewFlagSet("servers add", flag.ContinueOnError)
	upstream := fs.String("upstream", "", "upstream GraphQL URL (required)")
	if err := fs.Parse(args); err != nil {
		os.Exit(2)
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: scry servers add <name> --upstream <url>")
		os.Exit(2)
	}
	name := fs.Arg(0)
	if *upstream == "" {
		die(fmt.Errorf("--upstream is required"))
	}
	path, err := auth.DefaultPath()
	if err != nil {
		die(err)
	}
	s, err := auth.Load(path)
	if err != nil {
		die(err)
	}
	isNew := s.Upsert(name, auth.Server{Upstream: *upstream})
	if err := s.Validate(); err != nil {
		die(err)
	}
	if err := auth.Save(s, path); err != nil {
		die(err)
	}
	verb := "updated"
	if isNew {
		verb = "added"
	}
	fmt.Printf("%s %q → %s\nNext: scry auth login %s --token <T>\n", verb, name, *upstream, name)
}

func serversRemove(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: scry servers remove <name>")
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
	if !s.Remove(name) {
		die(fmt.Errorf("server %q not found", name))
	}
	if err := auth.Save(s, path); err != nil {
		die(err)
	}
	fmt.Printf("removed %q\n", name)
}

func die(err error) {
	fmt.Fprintf(os.Stderr, "scry: %v\n", err)
	os.Exit(1)
}
