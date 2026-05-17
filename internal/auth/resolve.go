package auth

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
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
//
// Future work (v0.2): keychain://service.account (macOS Keychain,
// libsecret on Linux, DPAPI on Windows) once a portable keyring
// library is vendored.
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
	default:
		return "", fmt.Errorf("auth: unknown token ref scheme %q (supported: env, file, op)", scheme)
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
