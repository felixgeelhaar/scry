# Deploying scry with Docker

scry ships as a single static binary; the included `Dockerfile`
produces a multi-arch distroless image you can pin in compose
files, k8s manifests, or any container orchestrator.

## Build locally

```bash
docker build -t scry:dev \
  --build-arg SCRY_VERSION=$(git describe --tags --always) \
  --build-arg SCRY_COMMIT=$(git rev-parse HEAD) \
  --build-arg SCRY_DATE=$(date -u +%Y-%m-%dT%H:%M:%SZ) \
  .

docker run --rm scry:dev version
```

## Pull a release

```bash
docker pull ghcr.io/felixgeelhaar/scry:v0.2.0
```

> Image publication is part of the v0.2 release workflow. v0.1.0
> images aren't published; build locally from the v0.1.0 tag if you
> need that version.

## Single-upstream run (stdio)

stdio is the right transport when scry runs as a subprocess of an
MCP client (Claude Desktop, Cursor) — there's no use case for the
container if you're going stdio. Keep this for parity tests:

```bash
docker run --rm -i \
  -e UPSTREAM_TOKEN \
  scry:dev serve --upstream https://api.example.com/graphql \
                 --auth env://UPSTREAM_TOKEN
```

## Hosted HTTP

```bash
docker run -d \
  --name scry \
  -p 7777:7777 \
  -e UPSTREAM_TOKEN \
  -e SCRY_ADMIN_TOKEN \
  -v $PWD/servers.yml:/etc/scry/servers.yml:ro \
  -v $PWD/clients.yml:/etc/scry/clients.yml:ro \
  -v scry-audit:/var/lib/scry/audit \
  --env XDG_CONFIG_HOME=/etc \
  --env XDG_DATA_HOME=/var/lib \
  scry:dev serve \
    --transport http --listen :7777 \
    --serve-auth env://SCRY_ADMIN_TOKEN \
    --audit-dir /var/lib/scry/audit \
    --audit-max-size 52428800 \
    --audit-keep 5
```

Notes:

- `servers.yml` + `clients.yml` mount read-only — scry doesn't
  rewrite them at runtime (CLI `scry servers add` is operator-side
  on the host filesystem before scry boots).
- Audit dir lives on a named volume so rotation archives survive
  container restarts.
- `XDG_*` env vars override scry's config + data search paths to
  match the container's filesystem layout.

## Embedded TLS

For deployments where terminating TLS in scry beats running a
reverse proxy (single-binary edge, mTLS to service mesh):

```bash
docker run -d \
  --name scry \
  -p 7778:7778 \
  -e UPSTREAM_TOKEN \
  -e SCRY_ADMIN_TOKEN \
  -v $PWD/certs:/etc/scry/certs:ro \
  -v $PWD/servers.yml:/etc/scry/servers.yml:ro \
  scry:dev serve \
    --transport http --listen :7778 \
    --tls-cert /etc/scry/certs/server.pem \
    --tls-key  /etc/scry/certs/server-key.pem \
    --mtls-ca  /etc/scry/certs/clients-ca.pem \
    --serve-auth env://SCRY_ADMIN_TOKEN
```

## Diagnostics

`scry doctor` runs inside the container as a one-shot probe:

```bash
docker run --rm \
  -v $PWD/servers.yml:/etc/scry/servers.yml:ro \
  -v scry-audit:/var/lib/scry/audit \
  --env XDG_CONFIG_HOME=/etc \
  scry:dev doctor --audit-dir /var/lib/scry/audit
```

Use this as a Kubernetes readiness probe (`kubernetes.md`) or as a
post-deployment smoke step in your CI/CD.
