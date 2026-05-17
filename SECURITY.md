# Security policy

## Reporting a vulnerability

**Do not file public GitHub issues for security-sensitive reports.**

Email [security@felixgeelhaar.dev](mailto:security@felixgeelhaar.dev)
with:

- A description of the issue + impact.
- Reproduction steps or a proof of concept.
- scry version (output of `scry version`).
- Your platform + deployment shape (single-upstream stdio, hosted HTTP, mTLS, …).

You will get an acknowledgement within 72 hours. Coordinated disclosure
target is 90 days from triage; we can adjust on either end with you if
the impact warrants it.

## Supported versions

| Version | Supported |
|---|---|
| Latest minor | ✅ |
| Anything older | ❌ — upgrade |

scry is pre-1.0; security fixes ship on the latest minor only.

## What scry treats as in-scope

- Credential leakage in logs, traces, metrics, or audit chain output.
- Authz bypass: a read-only or scoped client invoking a tool / server
  it should not see (incl. `tools/list` leakage).
- Token-reference resolver flaws (`env://`, `file://`, `op://`).
- Audit-chain tampering that VerifyChain does not detect.
- Schema-index poisoning via crafted introspection or SDL input.
- TLS / mTLS misconfiguration paths that downgrade silently.

## What scry treats as out-of-scope

- Denial of service from a legitimately authorised client (handled by
  per-session budgets + cost ceiling).
- Vulnerabilities in upstream GraphQL servers — scry forwards what it
  is told to.
- Issues that require a malicious operator with filesystem access to
  the host running scry (out of threat model).

## Security model summary

- `servers.yml`, `clients.yml`: 0600, refuse to load looser perms.
- Audit dir: 0700 directory, 0600 files.
- Tokens never appear in logs (`obs.RedactTokenRef` is the choke
  point). References (`env://VAR`) keep scheme + target but never
  the resolved value.
- mTLS available via `--mtls-ca`; Bearer via `--serve-auth`.
- See `docs/auth-design.md` for the full credential model.

## Coordinated dependency vulnerabilities

scry uses [nox](https://github.com/nox-hq/nox) as its primary
vulnerability gate. CI runs nox on every PR + nightly; criticals
block merges unless waived in `security/vex.json` (OpenVEX).
