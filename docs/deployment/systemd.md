# Deploying scry with systemd

scry runs as a long-lived process; systemd is the right
production supervisor on a single Linux host. The included
`deploy/systemd/scry.service` ships a hardened unit you can copy
into `/etc/systemd/system/` and adapt.

## Layout

```
/usr/local/bin/scry                 # binary (from goreleaser tarball or `go install`)
/etc/scry/servers.yml               # mode 0600, owner=scry
/etc/scry/clients.yml               # mode 0600, owner=scry (optional)
/etc/scry/scry.env                  # mode 0600, owner=scry — env vars (tokens)
/var/lib/scry/audit/                # mode 0700, owner=scry — JSONL chain
/etc/scry/certs/                    # mode 0600, owner=scry (optional TLS)
```

## Install

```bash
# 1. Dedicated unprivileged user
sudo useradd --system --home /var/lib/scry --shell /usr/sbin/nologin scry

# 2. Directories with locked-down perms
sudo install -d -m 0700 -o scry -g scry /etc/scry /var/lib/scry/audit

# 3. Binary
sudo install -m 0755 dist/scry-linux-amd64 /usr/local/bin/scry

# 4. Config files (operator-authored)
sudo install -m 0600 -o scry -g scry servers.yml /etc/scry/
sudo install -m 0600 -o scry -g scry clients.yml /etc/scry/
sudo install -m 0600 -o scry -g scry scry.env    /etc/scry/

# 5. Unit
sudo install -m 0644 deploy/systemd/scry.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now scry
```

## Diagnostics

```bash
sudo -u scry XDG_CONFIG_HOME=/etc XDG_DATA_HOME=/var/lib \
  /usr/local/bin/scry doctor --audit-dir /var/lib/scry/audit
```

Run on every deploy + after a config change. Exit code 0 = healthy;
non-zero lists the failing checks.

## Hardening notes

The shipped unit:

- Runs as the dedicated `scry` user (`DynamicUser` is also possible
  if you don't need a persistent UID for the audit volume).
- `PrivateTmp=yes` — process gets its own `/tmp`.
- `ProtectSystem=strict` — root + `/usr` mounted read-only.
- `ProtectHome=yes` — `/home`, `/root` invisible.
- `NoNewPrivileges=yes` — setuid binaries can't escalate.
- `ReadWritePaths=/var/lib/scry/audit` — the only writable
  surface scry needs.
- `CapabilityBoundingSet=` empty — drops every Linux capability.
- `SystemCallFilter=@system-service` — seccomp baseline.

If you terminate TLS at the edge (recommended), drop `--tls-cert`
+ remove `/etc/scry/certs` from `ReadOnlyPaths`. If you bind to
port < 1024 you'll need `AmbientCapabilities=CAP_NET_BIND_SERVICE`
+ matching `CapabilityBoundingSet`.

## Reading logs

scry emits JSON to stderr by default; systemd captures it via the
journal:

```bash
sudo journalctl -u scry -f
sudo journalctl -u scry --since "1 hour ago" --output cat | jq .
```

Set `SCRY_LOG=console` in `/etc/scry/scry.env` for human-readable
output during incident response.

## Hot reload

scry watches `servers.yml` via fsnotify. Editing the file in place
takes effect within ~500ms — no `systemctl reload` needed.
`scry auth login` does the right thing on a running unit.
