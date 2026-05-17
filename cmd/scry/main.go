// Command scry runs the scry MCP server and manages its credential
// store. Connects to a single upstream GraphQL endpoint, introspects
// its schema, and exposes five tools an AI agent can call:
// schema_search, schema_get, query_validate, query_cost, query_execute.
//
// Usage:
//
//	scry serve   --upstream <url> [--auth <token>]
//	scry servers list
//	scry servers add    <name> --upstream <url>
//	scry servers remove <name>
//	scry auth login   <server> --token <T>
//	scry auth logout  <server>
//	scry auth status  [<server>]
//
// stdio is the only transport in v0 — designed to be wired as an MCP
// server from Claude Code / Cursor / any MCP client.
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

func main() {
	// Init log before anything else so even flag errors carry
	// structured context. Format + level come from env so the same
	// binary is at home in CI (JSON) and dev (console via
	// SCRY_LOG=console).
	obs.Init("", os.Stderr)

	// Init OTel tracer based on OTEL_TRACES_EXPORTER. Off by
	// default (no-op provider); operators opt in via env so the
	// happy path stays zero-dependency. Shutdown defers so the
	// last batch of spans flushes on SIGTERM.
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
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		runServe(os.Args[2:])
	case "servers":
		runServers(os.Args[2:])
	case "auth":
		runAuth(os.Args[2:])
	case "version", "-v", "--version":
		fmt.Printf("scry %s\ncommit %s\nbuilt %s\n", version.Version, version.Commit, version.Date)
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "scry: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
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
  scry version
`)
}

func runServe(args []string) {
	cfg, err := server.ParseFlags(args)
	if err != nil {
		obs.L.Error().Str("event", "boot.flags").Err(err).Msg("invalid serve flags")
		os.Exit(2)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := server.Run(ctx, cfg); err != nil {
		// Distinguish a clean shutdown (ctx canceled by signal)
		// from a real fault. The first is expected on Ctrl-C;
		// the second is what operators want to alert on.
		if ctx.Err() != nil {
			obs.L.Info().Str("event", "shutdown").Msg("scry exited cleanly on signal")
			return
		}
		obs.L.Error().Str("event", "serve.fatal").Err(err).Msg("scry serve failed")
		os.Exit(1)
	}
}
