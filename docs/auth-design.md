# Auth design

scry talks to N GraphQL upstreams. Each has its own auth shape
(bearer token, OAuth device-code, PKCE, API key in a header).
Operators don't want to hand-edit YAML to manage tokens; agents
need to recover when a token expires mid-task.

This doc locks the auth flow before code.

## Config layout

Single file at `$XDG_CONFIG_HOME/scry/servers.yml` (default
`~/.config/scry/servers.yml`):

```yaml
servers:
  shopify:
    upstream: https://api.shopify.com/admin/api/2024-01/graphql.json
    auth:
      type: bearer
      token: shpat_xxx
      expires_at: 2026-06-17T10:00:00Z
      refresh_at: 2026-06-16T22:00:00Z  # optional; renew earlier
  github:
    upstream: https://api.github.com/graphql
    auth:
      type: bearer
      token: github_pat_xxx
  linear:
    upstream: https://api.linear.app/graphql
    auth:
      type: oauth
      access_token: lin_oat_...
      refresh_token: lin_ort_...
      expires_at: 2026-06-17T...
      client_id: scry-linear-client
```

`secrets:` are kept in the same file with `0600` perms — same model
as `kubectl config`. Operators who don't want plaintext tokens on
disk **today** can use a token reference instead of a literal:

```yaml
auth:
  type: bearer
  token: env://SHOPIFY_TOKEN              # environment variable
  # token: file:///run/secrets/shopify    # external file (must be 0600)
  # token: op://Personal/shopify/token    # 1Password CLI
```

Resolution happens per request, so a rotated env var or file secret
is picked up without restarting scry. See
`internal/auth/resolve.go` for the implementation. OS keychain
integration (`keychain://service.account`) is queued for v0.2.

## CLI surface

```
scry servers list                       # table of configured servers + auth status
scry servers add <name> --upstream URL  # registers a server; auth follows
scry servers remove <name>              # drops server + tokens

scry auth login <server>                # interactive flow; persists token
scry auth login <server> --token "$TOKEN"  # non-interactive; CI-friendly
scry auth logout <server>               # clears tokens; server entry stays
scry auth refresh <server>              # forces a re-auth flow
scry auth status                        # one row per server: valid | expiring | expired
```

`scry auth login` dispatches per `auth.type`:

| Type | Flow |
|---|---|
| `bearer` | Prompt for token; verify with a no-op introspection query; write. |
| `oauth-device` | Run device-code flow, print user_code + verification_url, poll, write. |
| `oauth-pkce` | Open browser, redirect callback, exchange code, write. |
| `api-key-header` | Prompt for value; verify; write. |

Per-server provider profiles ship as YAML in `internal/auth/profiles/`
so adding Shopify or Linear is a config diff, not code.

## MCP surface

Two agent-callable tools alongside the five core tools:

| Tool | Purpose |
|---|---|
| `auth_status` | Returns `[{server, type, valid, expires_in_seconds}]`. Agent calls before kicking a long task. |
| `auth_login(server)` | Returns either `{ok: true}` for token-already-valid, OR `{verification_url, user_code, expires_in}` for device-code flows. Agent prints the URL to the operator and polls until completion. |

The flow when a query expires mid-task:

```
agent → query_execute(shopify, ...)
scry  → 401 from upstream
scry  → returns { error: "auth_expired", server: "shopify",
                  hint: "call auth_login('shopify')" }
agent → auth_login("shopify")
scry  → returns { verification_url: "https://shopify.com/...",
                  user_code: "ABCD-1234",
                  expires_in: 600 }
agent → relays URL+code to operator via its host's UI
       (Claude Code prints it; Cursor renders the panel)
operator visits URL, authorises
scry  → polls token endpoint, writes token to servers.yml
agent → retries query_execute, succeeds
```

The operator never opens YAML. The agent never sees a long-lived
secret directly — it only sees the device-code URL + the eventual
success signal.

## Hot reload

scry watches `servers.yml` via `fsnotify`. On change:

1. Re-parse the file (validated against the YAML schema).
2. Per server: if `auth.token` changed, swap the bearer in the
   fortify-wrapped client without restart.
3. If a new server appeared, introspect + index it; register its
   namespaced tools.
4. If a server was removed, unregister its tools.

Hot reload means `scry auth login shopify` from one terminal is
picked up by the running `scry serve` in another without a SIGHUP.

## Token storage choices NOT in v0

- OS keychain (macOS Keychain, libsecret) — defer to v0.2.
- Encrypted YAML (age, sops) — defer. Operators who care can wrap
  the file in their existing secrets pipeline.
- Multi-user installs — scry is local-first, one user per host. No
  user namespacing.

## Refresh policy

- `refresh_at` (optional) tells scry to renew the token *before*
  expiry — e.g. 1h before for tokens with hourly rotation.
- Background refresher runs once a minute; if `refresh_at < now`
  and the server has OAuth refresh tokens, run the refresh flow
  silently.
- Tokens with no refresh path (PATs, bearer paste) only get
  re-prompted via `auth_login` or `auth_refresh`.

## Security boundary

- `servers.yml` is `0600`. scry refuses to start if it finds the
  file world-readable.
- Tokens never appear in logs (bolt's structured logger has a
  redact rule for known token-shaped fields).
- `auth_status` tool returns `valid | expiring | expired` enums;
  it never returns the token itself.
- `query_execute` failures surface upstream status codes but
  scrub response bodies that look like auth errors (they often
  echo the token back).
