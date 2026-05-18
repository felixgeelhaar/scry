package auth

import (
	"errors"
	"strings"
	"testing"

	"github.com/99designs/keyring"
)

func TestResolveTokenKeychainRoundtrip(t *testing.T) {
	called := 0
	restore := SetKeychainOpenerForTest(func(cfg keyring.Config) (keyring.Keyring, error) {
		called++
		if cfg.ServiceName != "scry" {
			t.Errorf("service name = %q, want scry", cfg.ServiceName)
		}
		return keyring.NewArrayKeyring([]keyring.Item{
			{Key: "shopify", Data: []byte("shpat_secret_value\n")},
		}), nil
	})
	defer restore()

	tok, err := ResolveToken("keychain://scry/shopify")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if tok != "shpat_secret_value" {
		t.Errorf("token = %q, want shpat_secret_value (trailing whitespace not trimmed?)", tok)
	}
	if called != 1 {
		t.Errorf("keychainOpener called %d times, want 1", called)
	}
}

func TestResolveTokenKeychainNotFound(t *testing.T) {
	restore := SetKeychainOpenerForTest(func(_ keyring.Config) (keyring.Keyring, error) {
		return keyring.NewArrayKeyring(nil), nil
	})
	defer restore()

	_, err := ResolveToken("keychain://scry/missing")
	if err == nil {
		t.Fatalf("expected error for missing entry")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should hint at missing entry, got %v", err)
	}
}

func TestResolveTokenKeychainHeadlessFallback(t *testing.T) {
	restore := SetKeychainOpenerForTest(func(_ keyring.Config) (keyring.Keyring, error) {
		return nil, keyring.ErrNoAvailImpl
	})
	defer restore()

	_, err := ResolveToken("keychain://scry/shopify")
	if err == nil {
		t.Fatalf("expected error on headless")
	}
	if !strings.Contains(err.Error(), "file://") || !strings.Contains(err.Error(), "env://") {
		t.Errorf("headless error should point operator at file:// / env:// fallback, got %v", err)
	}
}

func TestResolveTokenKeychainRejectsBadShape(t *testing.T) {
	cases := []string{
		"keychain://",
		"keychain://no-key",
		"keychain:///just-a-key",
	}
	for _, in := range cases {
		t.Run(in, func(t *testing.T) {
			if _, err := ResolveToken(in); err == nil {
				t.Errorf("expected error for %q", in)
			}
		})
	}
}

func TestResolveTokenKeychainPropagatesOtherErrors(t *testing.T) {
	sentinel := errors.New("simulated dbus failure")
	restore := SetKeychainOpenerForTest(func(_ keyring.Config) (keyring.Keyring, error) {
		return nil, sentinel
	})
	defer restore()

	_, err := ResolveToken("keychain://scry/x")
	if err == nil || !strings.Contains(err.Error(), "simulated") {
		t.Errorf("expected sentinel error to bubble up, got %v", err)
	}
}
