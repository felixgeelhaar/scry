// Command scry runs the scry MCP server and manages its credential
// store. Connects to one or many GraphQL endpoints, introspects each
// schema, and exposes ten MCP tools an AI agent can call:
// schema_search, schema_get, query_validate, query_cost,
// query_execute, list_servers, auth_status, auth_login, gate_status,
// gate_chain.
//
// Usage:
//
//	scry serve   --upstream <url> [--auth <token>] [--cost-ceiling N]
//	scry servers list
//	scry servers add    <name> --upstream <url>
//	scry servers remove <name>
//	scry auth login   <server> --token <T>
//	scry auth logout  <server>
//	scry auth status  [<server>]
//	scry version
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/felixgeelhaar/scry/internal/obs"
	"github.com/felixgeelhaar/scry/internal/server"
	"github.com/felixgeelhaar/scry/internal/version"
)

// main delegates to run() so deferred cleanup (tracer/meter shutdown,
// signal-context cancel) always executes before the process exits.
// Subcommand failures bubble up as exit codes instead of os.Exit
// scattered across packages — keeps the OTel exporter from losing
// the last batch of spans on a flag-validation error.
func main() {
	os.Exit(run())
}

func run() int {
	obs.Init("", os.Stderr)

	// Init OTel tracer + meter based on OTEL_* env. Off by default
	// (no-op providers); operators opt in via env so the happy
	// path stays zero-dependency. Shutdown defers so the last
	// batch of spans + metrics flushes on graceful exit.
	ctxInit, cancelInit := context.WithTimeout(context.Background(), 5*time.Second)
	shutdownTracer, err := obs.InitTracer(ctxInit)
	if err != nil {
		obs.L.Warn().Str("event", "boot.tracer_init_failed").Err(err).Msg("tracing disabled")
	}
	shutdownMeter, err := obs.InitMeter(ctxInit)
	cancelInit()
	if err != nil {
		obs.L.Warn().Str("event", "boot.meter_init_failed").Err(err).Msg("metrics disabled")
	}
	defer func() {
		ctxShut, cancelShut := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShut()
		_ = shutdownTracer(ctxShut)
		_ = shutdownMeter(ctxShut)
	}()

	if len(os.Args) < 2 {
		usage()
		return 2
	}
	switch os.Args[1] {
	case "serve":
		return runServe(os.Args[2:])
	case "servers":
		runServers(os.Args[2:])
		return 0
	case "auth":
		runAuth(os.Args[2:])
		return 0
	case "doctor":
		return runDoctor(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Printf("scry %s\ncommit %s\nbuilt %s\n", version.Version, version.Commit, version.Date)
		return 0
	case "-h", "--help", "help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "scry: unknown command %q\n\n", os.Args[1])
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `scry — searchable GraphQL bridge for AI agents

usage:
  scry serve   --upstream <url> [--auth <token>] [--cost-ceiling N]
  scry servers list
  scry servers add    <name> --upstream <url>
  scry servers remove <name>
  scry auth login   <server> --token <T> [--upstream <url>] [--expires <duration>]
  scry auth logout  <server>
  scry auth status  [<server>]
  scry doctor [--audit-dir <path>]
  scry version
`)
}

func runServe(args []string) int {
	cfg, err := server.ParseFlags(args)
	if err != nil {
		obs.L.Error().Str("event", "boot.flags").Err(err).Msg("invalid serve flags")
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := server.Run(ctx, cfg); err != nil {
		// Distinguish a clean shutdown (ctx canceled by signal)
		// from a real fault. The first is expected on Ctrl-C;
		// the second is what operators want to alert on.
		if ctx.Err() != nil {
			obs.L.Info().Str("event", "shutdown").Msg("scry exited cleanly on signal")
			return 0
		}
		obs.L.Error().Str("event", "serve.fatal").Err(err).Msg("scry serve failed")
		return 1
	}
	return 0
}
