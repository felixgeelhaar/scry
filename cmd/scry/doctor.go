package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/felixgeelhaar/scry/internal/auth"
	"github.com/felixgeelhaar/scry/internal/doctor"
)

// runDoctor is the `scry doctor` subcommand entry point. Probes
// every layer of local configuration + reachable runtime
// dependencies and exits 0 when healthy, non-zero with one error
// per failed check.
func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	auditDir := fs.String("audit-dir", "", "audit directory to probe (defaults to matching --audit-dir from serve)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	serversPath, err := auth.DefaultPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "scry doctor: resolve servers.yml path: %v\n", err)
		return 2
	}
	clientsPath, err := auth.DefaultClientsPath()
	if err != nil {
		fmt.Fprintf(os.Stderr, "scry doctor: resolve clients.yml path: %v\n", err)
		return 2
	}

	d := doctor.Default(doctor.Config{
		ServersPath: serversPath,
		ClientsPath: clientsPath,
		AuditDir:    *auditDir,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return d.Run(ctx)
}
