// Package obs is scry's observability surface: a process-global
// bolt logger plus a small set of redaction helpers. Kept narrow on
// purpose — scry doesn't need every observability primitive, just a
// reliable structured log and a single place to enforce "never log
// a bearer token".
//
// Bolt is the chosen logger (see docs/dep-eval.md): zero-alloc
// fluent API + JSON or console handlers + OTEL bridge available
// when we wire it. v0 ships JSON to stderr; operators run their
// existing log pipeline against the binary's stderr stream.
package obs

import (
	"io"
	"os"
	"strings"
	"sync"

	"go.klarlabs.de/bolt"
)

var (
	once sync.Once
	L    *bolt.Logger
)

// Init sets up the process-global logger. Safe to call once at
// startup. Subsequent calls are no-ops so accidentally double-wiring
// from main.go and a server.Run() helper doesn't double-print.
//
// Format selection:
//   - explicit "console" or "json" via the format arg wins.
//   - empty falls back to env SCRY_LOG (json|console). Empty there
//     too falls back to JSON (the default that machine log pipelines
//     can parse).
//
// Level selection:
//   - SCRY_LOG_LEVEL env var (trace|debug|info|warn|error). Empty
//     defaults to info. Operators bump to debug to follow refresh +
//     execute internals; tests run with warn to keep output clean.
func Init(format string, out io.Writer) {
	once.Do(func() {
		L = build(format, out)
	})
}

// SetForTest replaces the global logger. Tests call this in setup
// then restore via the returned cleanup. Bypasses the once guard
// because tests need a fresh logger per case.
func SetForTest(l *bolt.Logger) (restore func()) {
	prev := L
	L = l
	return func() { L = prev }
}

func build(format string, out io.Writer) *bolt.Logger {
	if out == nil {
		out = os.Stderr
	}
	if format == "" {
		format = os.Getenv("SCRY_LOG")
	}
	var handler bolt.Handler
	switch strings.ToLower(format) {
	case "console", "pretty", "text":
		handler = bolt.NewConsoleHandler(out)
	default:
		handler = bolt.NewJSONHandler(out)
	}
	l := bolt.New(handler)
	if lvl := strings.ToLower(os.Getenv("SCRY_LOG_LEVEL")); lvl != "" {
		l = l.SetLevel(bolt.ParseLevel(lvl))
	}
	return l
}

// RedactTokenRef takes a token reference (env://VAR, file://path,
// op://Vault/Item/field) or a literal token and returns a value
// safe to log. References keep their scheme + target so operators
// can debug "wrong env var name" without leaking the value. Literals
// collapse to "[redacted len=N]" so even token length is somewhat
// preserved without exposing the bytes.
//
// Empty input maps to "[unset]" — operators reading logs need to
// distinguish "no token configured" from "token was redacted".
func RedactTokenRef(ref string) string {
	if ref == "" {
		return "[unset]"
	}
	if i := strings.Index(ref, "://"); i >= 0 {
		scheme := ref[:i]
		rest := ref[i+3:]
		// For env:// the variable name is non-secret (it's the
		// pointer, not the value). file:// path same. op://
		// reference path same — the secret is what op resolves
		// to, not the path.
		return scheme + "://" + rest
	}
	return literalRedact(ref)
}

// literalRedact masks a literal bearer token. Keeps length only —
// matches the strictest interpretation of "never log a secret".
func literalRedact(tok string) string {
	if len(tok) == 0 {
		return "[unset]"
	}
	return formatRedacted(len(tok))
}

func formatRedacted(n int) string {
	// Stick to a fixed shape so log queries like
	//   message=~"\[redacted .*\]"
	// stay simple.
	var b strings.Builder
	b.WriteString("[redacted len=")
	// Inline itoa rather than fmt.Sprintf to keep the package
	// allocation-free on the hot path (bolt's whole appeal).
	if n == 0 {
		b.WriteByte('0')
	} else {
		var digits [20]byte
		i := len(digits)
		for n > 0 {
			i--
			digits[i] = byte('0' + n%10)
			n /= 10
		}
		b.Write(digits[i:])
	}
	b.WriteByte(']')
	return b.String()
}
