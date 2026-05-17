package auth

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveTokenLiteralPassthrough(t *testing.T) {
	tok, err := ResolveToken("shpat_literal")
	if err != nil {
		t.Fatalf("literal: %v", err)
	}
	if tok != "shpat_literal" {
		t.Errorf("literal mangled: %q", tok)
	}
}

func TestResolveTokenEnvScheme(t *testing.T) {
	t.Setenv("SCRY_TEST_TOKEN", "value-from-env")
	tok, err := ResolveToken("env://SCRY_TEST_TOKEN")
	if err != nil {
		t.Fatalf("env: %v", err)
	}
	if tok != "value-from-env" {
		t.Errorf("env ref returned %q", tok)
	}

	if _, err := ResolveToken("env://SCRY_TEST_MISSING"); err == nil {
		t.Errorf("expected error for missing env var")
	}
	if _, err := ResolveToken("env://"); err == nil {
		t.Errorf("expected error for empty var name")
	}
}

func TestResolveTokenFileScheme(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("secret-from-file\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	tok, err := ResolveToken("file://" + path)
	if err != nil {
		t.Fatalf("file: %v", err)
	}
	if tok != "secret-from-file" {
		t.Errorf("file ref returned %q (trailing newline not trimmed?)", tok)
	}

	if runtime.GOOS != "windows" {
		loose := filepath.Join(dir, "loose")
		if err := os.WriteFile(loose, []byte("x"), 0o644); err != nil {
			t.Fatalf("write loose: %v", err)
		}
		if _, err := ResolveToken("file://" + loose); err == nil || !strings.Contains(err.Error(), "insecure perms") {
			t.Errorf("expected insecure-perms error, got %v", err)
		}
	}

	if _, err := ResolveToken("file://" + filepath.Join(dir, "doesnotexist")); err == nil {
		t.Errorf("expected error for missing file")
	}
}

func TestResolveTokenUnknownScheme(t *testing.T) {
	if _, err := ResolveToken("vault://kv/foo"); err == nil || !strings.Contains(err.Error(), "unknown token ref scheme") {
		t.Errorf("expected unknown-scheme error, got %v", err)
	}
}
