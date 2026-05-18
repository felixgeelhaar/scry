# syntax=docker/dockerfile:1.7
#
# Multi-stage build for scry. Stage 1 compiles a static binary from
# the local source; stage 2 ships it on distroless static so the
# attack surface is just the binary itself + tini for PID-1 duties.
#
# Build:
#   docker build -t scry:dev .
#
# Run:
#   docker run --rm -i \
#     -e UPSTREAM_TOKEN \
#     -v $PWD/servers.yml:/etc/scry/servers.yml:ro \
#     scry:dev serve --transport http --listen :7777 \
#                    --auth env://UPSTREAM_TOKEN
#
# scry binds plain HTTP — terminate TLS at the edge (Envoy, nginx,
# Cloud Run) or pass --tls-cert / --tls-key + bind-mount the PEM
# files.

ARG GO_VERSION=1.26
ARG SCRY_VERSION=dev
ARG SCRY_COMMIT=unknown
ARG SCRY_DATE=unknown

FROM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
# Pull dep manifests first so go mod download caches independently
# of source changes.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG SCRY_VERSION
ARG SCRY_COMMIT
ARG SCRY_DATE
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -tags netgo \
      -ldflags "-s -w \
        -X github.com/felixgeelhaar/scry/internal/version.Version=${SCRY_VERSION} \
        -X github.com/felixgeelhaar/scry/internal/version.Commit=${SCRY_COMMIT} \
        -X github.com/felixgeelhaar/scry/internal/version.Date=${SCRY_DATE}" \
      -o /out/scry ./cmd/scry

FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/scry /usr/local/bin/scry
USER 65532:65532
# stdio is the default; operators override to http/grpc/ws and pass
# --listen at the docker run command line.
ENTRYPOINT ["/usr/local/bin/scry"]
CMD ["version"]
