package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/felixgeelhaar/scry/internal/pq"
)

// runPQ dispatches `scry pq {add|list|remove} ...`. Each subcommand
// opens the per-server pq store directly — same path the running
// daemon uses, so hot-add takes effect on the next `query_execute`
// without restarting scry.
func runPQ(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: scry pq add|list|remove ...")
		return 2
	}
	switch args[0] {
	case "add":
		return pqAdd(args[1:])
	case "list":
		return pqList(args[1:])
	case "remove", "rm":
		return pqRemove(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "scry pq: unknown subcommand %q\n", args[0])
		return 2
	}
}

func pqStorePath(server string) (string, error) {
	dir := os.Getenv("XDG_DATA_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(dir, "scry", safeName(server)+".pq.db"), nil
}

// safeName matches runtime.safeIndexName so the CLI hits the same
// SQLite file the daemon owns. Duplicated here to avoid a runtime
// import in the CLI (circular).
func safeName(name string) string {
	out := make([]byte, len(name))
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_':
			out[i] = c
		default:
			out[i] = '_'
		}
	}
	return string(out)
}

func openPQStore(server string) (*pq.Store, error) {
	path, err := pqStorePath(server)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	return pq.OpenStore(path)
}

// pqAdd registers a query under a friendly name. Reads the query
// body from --file or stdin (when --file is "-").
func pqAdd(args []string) int {
	fs := flag.NewFlagSet("pq add", flag.ContinueOnError)
	file := fs.String("file", "", "path to the .graphql file (use - for stdin)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 2 {
		fmt.Fprintln(os.Stderr, "usage: scry pq add <server> <name> --file <path>")
		return 2
	}
	server, name := fs.Arg(0), fs.Arg(1)
	if *file == "" {
		fmt.Fprintln(os.Stderr, "scry pq add: --file is required (use - for stdin)")
		return 2
	}
	var body []byte
	var err error
	if *file == "-" {
		body, err = readAllStdin()
	} else {
		body, err = os.ReadFile(*file)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "scry pq add: read query: %v\n", err)
		return 1
	}
	s, err := openPQStore(server)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scry pq add: %v\n", err)
		return 1
	}
	defer func() { _ = s.Close() }()
	entry, err := s.Put(context.Background(), name, string(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "scry pq add: %v\n", err)
		return 1
	}
	fmt.Printf("registered %q on %q\nhash: %s\n", entry.Name, server, entry.Hash)
	return 0
}

func pqList(args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: scry pq list <server>")
		return 2
	}
	server := args[0]
	s, err := openPQStore(server)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scry pq list: %v\n", err)
		return 1
	}
	defer func() { _ = s.Close() }()
	entries, err := s.List(context.Background())
	if err != nil {
		fmt.Fprintf(os.Stderr, "scry pq list: %v\n", err)
		return 1
	}
	if len(entries) == 0 {
		fmt.Printf("no persisted queries on %q — register with `scry pq add %s <name> --file query.graphql`\n", server, server)
		return 0
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tHASH\tBYTES")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\t%d\n", e.Name, e.Hash[:12]+"...", len(e.Query))
	}
	_ = w.Flush()
	return 0
}

func pqRemove(args []string) int {
	if len(args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: scry pq remove <server> <hash|name>")
		return 2
	}
	server, target := args[0], args[1]
	s, err := openPQStore(server)
	if err != nil {
		fmt.Fprintf(os.Stderr, "scry pq remove: %v\n", err)
		return 1
	}
	defer func() { _ = s.Close() }()
	if err := s.Delete(context.Background(), target); err != nil {
		if errors.Is(err, pq.ErrNotFound) {
			fmt.Fprintf(os.Stderr, "scry pq remove: no entry %q on %q\n", target, server)
			return 1
		}
		fmt.Fprintf(os.Stderr, "scry pq remove: %v\n", err)
		return 1
	}
	fmt.Printf("removed %q from %q\n", target, server)
	return 0
}

// readAllStdin reads the rest of os.Stdin into memory. Capped at
// 1 MiB so an accidental `pq add … --file -` against a large file
// can't OOM the CLI.
func readAllStdin() ([]byte, error) {
	const cap = 1 << 20
	buf := make([]byte, cap)
	n, err := os.Stdin.Read(buf)
	if err != nil && err.Error() != "EOF" {
		return nil, err
	}
	return buf[:n], nil
}
