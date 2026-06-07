package obs

import (
	"bytes"
	"strings"
	"testing"

	"go.klarlabs.de/bolt"
)

func TestRedactTokenRefHidesLiterals(t *testing.T) {
	cases := map[string]string{
		"":                       "[unset]",
		"shpat_abcdef1234567890": "[redacted len=22]",
		"x":                      "[redacted len=1]",
	}
	for in, want := range cases {
		if got := RedactTokenRef(in); got != want {
			t.Errorf("RedactTokenRef(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactTokenRefKeepsSchemeAndTarget(t *testing.T) {
	cases := map[string]string{
		"env://SCRY_TOKEN":            "env://SCRY_TOKEN",
		"file:///run/secrets/scry":    "file:///run/secrets/scry",
		"op://Personal/shopify/token": "op://Personal/shopify/token",
	}
	for in, want := range cases {
		if got := RedactTokenRef(in); got != want {
			t.Errorf("RedactTokenRef(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestBuildJSONHandler is a sanity check that a built logger writes
// structured output to the supplied writer. Catches a regression
// where format dispatch falls through to a no-op handler.
func TestBuildJSONHandler(t *testing.T) {
	var buf bytes.Buffer
	l := build("json", &buf)
	l.Info().Str("k", "v").Msg("hello")
	out := buf.String()
	if !strings.Contains(out, `"k":"v"`) || !strings.Contains(out, `hello`) {
		t.Errorf("JSON handler output missing expected fields: %q", out)
	}
}

func TestBuildConsoleHandler(t *testing.T) {
	var buf bytes.Buffer
	l := build("console", &buf)
	l.Info().Str("k", "v").Msg("hello")
	if !strings.Contains(buf.String(), "hello") {
		t.Errorf("console handler output missing message: %q", buf.String())
	}
}

func TestBuildHonoursLogLevel(t *testing.T) {
	t.Setenv("SCRY_LOG_LEVEL", "warn")
	var buf bytes.Buffer
	l := build("json", &buf)
	l.Info().Msg("should-be-suppressed")
	l.Warn().Msg("should-appear")
	out := buf.String()
	if strings.Contains(out, "should-be-suppressed") {
		t.Errorf("info-level message leaked through warn filter: %q", out)
	}
	if !strings.Contains(out, "should-appear") {
		t.Errorf("warn-level message missing: %q", out)
	}
}

func TestResetMetersForTestRebuildsInstruments(t *testing.T) {
	// Touching meters once populates them against whatever the
	// current provider is.
	_ = Metrics()
	// Reset clears the cache so a subsequent call picks up a
	// freshly-swapped provider — important for tests that swap
	// providers between cases.
	ResetMetersForTest()
	m := Metrics()
	if m.ExecuteCount == nil {
		t.Errorf("expected ExecuteCount to be rebuilt after reset, got nil")
	}
}

func TestSetForTestRoundtrip(t *testing.T) {
	prev := L
	var buf bytes.Buffer
	restore := SetForTest(bolt.New(bolt.NewJSONHandler(&buf)))
	if L == prev {
		t.Fatalf("SetForTest did not swap logger")
	}
	restore()
	if L != prev {
		t.Fatalf("restore did not put back the previous logger")
	}
}
