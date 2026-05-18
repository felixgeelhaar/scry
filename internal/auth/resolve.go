package auth

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/99designs/keyring"
)

// ResolveToken interprets a token string from servers.yml. Two shapes
// are supported:
//
//   - Literal: any bare string that doesn't match a known URI scheme
//     is returned verbatim. Preserves backward compatibility with the
//     simplest v0 servers.yml.
//
//   - URI ref: <scheme>://<rest> indirects through one of the
//     resolvers below. The token never lives in servers.yml — only
//     the *reference* to where it lives. Lets operators keep
//     credentials in 1Password / env vars / a separate file without
//     waiting for keychain integration.
//
// Supported schemes:
//
//	env://VAR_NAME           — value of the environment variable
//	file://path/to/secret    — file contents (whitespace trimmed)
//	op://Vault/Item/field    — shells out to the 1Password `op` CLI
//	keychain://service/key   — OS-native keyring (macOS Keychain,
//	                           libsecret on Linux, Windows credential
//	                           manager). Headless systems get a clear
//	                           error pointing back to file://.
func ResolveToken(ref string) (string, error) {
	if !strings.Contains(ref, "://") {
		return ref, nil
	}
	scheme, rest, _ := strings.Cut(ref, "://")
	switch scheme {
	case "env":
		return resolveEnv(rest)
	case "file":
		return resolveFile(rest)
	case "op":
		return resolveOp(ref)
	case "keychain":
		return resolveKeychain(rest)
	default:
		return "", fmt.Errorf("auth: unknown token ref scheme %q (supported: env, file, op, keychain)", scheme)
	}
}

func resolveEnv(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("auth: env:// ref is missing variable name")
	}
	v := os.Getenv(name)
	if v == "" {
		return "", fmt.Errorf("auth: env var %s is empty or unset", name)
	}
	return v, nil
}

// resolveFile reads a secret out of a separate file, then trims any
// trailing newline a user is likely to leave behind when piping
// `echo $TOKEN > tokenfile`. The file must be 0600-or-stricter on
// POSIX so a stray world-readable token file is caught early.
func resolveFile(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("auth: file:// ref is missing path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("auth: stat token file: %w", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("auth: token file %s has insecure perms %o — chmod 600 to fix", path, info.Mode().Perm())
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("auth: read token file: %w", err)
	}
	return strings.TrimRight(string(b), "\r\n\t "), nil
}

// keychainOpener is the injection seam for tests. Production code
// uses the package-default which opens the real OS keyring; tests
// swap in an in-memory ArrayKeyring via SetKeychainOpenerForTest so
// they don't pop OS auth dialogs.
var keychainOpener = keyring.Open

// SetKeychainOpenerForTest swaps the keychain factory. Returns the
// previous opener so tests can restore via defer.
func SetKeychainOpenerForTest(fn func(keyring.Config) (keyring.Keyring, error)) (restore func()) {
	prev := keychainOpener
	keychainOpener = fn
	return func() { keychainOpener = prev }
}

// resolveKeychain reads `service/key` (the rest of the URI after
// `keychain://`). The first path segment is the service name passed
// to the OS keyring; the remainder is the entry key within that
// service. Slashes inside the key are preserved so "scry/upstream"
// stays a single key, e.g. `keychain://scry/scry/upstream` yields
// service="scry", key="scry/upstream".
//
// Failures:
//   - ref without "/" separator: configuration error
//   - keyring.ErrNoAvailImpl: headless system without a supported
//     backend; surface a clear hint to switch to file://
//   - keyring.ErrKeyNotFound: the operator hasn't stored the secret
//     yet; suggest the platform-appropriate add command
func resolveKeychain(rest string) (string, error) {
	service, key, ok := strings.Cut(rest, "/")
	if !ok || service == "" || key == "" {
		return "", fmt.Errorf("auth: keychain:// ref must be 'service/key', got %q", rest)
	}
	kr, err := keychainOpener(keyring.Config{
		ServiceName: service,
		// Allowed backends: every native option. Drop the
		// file-based backend (it would expose a YAML-style prompt
		// dialogue at runtime, defeating the point of using a
		// keychain). Pass-store is similar — works headless but
		// needs additional ceremony; keep it explicit.
		AllowedBackends: []keyring.BackendType{
			keyring.KeychainBackend,      // macOS
			keyring.SecretServiceBackend, // Linux (libsecret)
			keyring.KWalletBackend,       // KDE
			keyring.WinCredBackend,       // Windows
		},
	})
	if errors.Is(err, keyring.ErrNoAvailImpl) {
		return "", fmt.Errorf("auth: keychain:// requires an OS keyring backend (macOS Keychain / libsecret / KWallet / Windows credential manager) — none available on this host; use file:// or env:// instead")
	}
	if err != nil {
		return "", fmt.Errorf("auth: open keychain service %q: %w", service, err)
	}
	item, err := kr.Get(key)
	if errors.Is(err, keyring.ErrKeyNotFound) {
		return "", fmt.Errorf("auth: keychain entry %q not found in service %q — store it first via your OS keyring UI (e.g. macOS Keychain Access) or `security add-generic-password -s %q -a %q -w <token>`", key, service, service, key)
	}
	if err != nil {
		return "", fmt.Errorf("auth: read keychain entry %q/%q: %w", service, key, err)
	}
	return strings.TrimRight(string(item.Data), "\r\n\t "), nil
}

// resolveOp shells out to the 1Password CLI. The `op` binary handles
// auth caching, biometric unlock, and account selection on its own
// — scry just forwards the secret reference.
//
// Returns a friendly error when `op` is not installed so operators
// know to either install it or pick a different scheme.
func resolveOp(ref string) (string, error) {
	if _, err := exec.LookPath("op"); err != nil {
		return "", fmt.Errorf("auth: op:// ref requires the 1Password CLI (`op`) on PATH: %w", err)
	}
	out, err := exec.Command("op", "read", ref).Output()
	if err != nil {
		// op writes the real error to stderr; surface its exit
		// status so operators see "vault locked" / "item missing"
		// rather than a generic failure.
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("auth: `op read` failed: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return "", fmt.Errorf("auth: `op read`: %w", err)
	}
	return strings.TrimRight(string(out), "\r\n\t "), nil
}
