# Deploying scry on Kubernetes

scry's container image ships as a static-binary distroless build —
right for the typical k8s deployment pattern: stateless replicas
behind a Service, secrets injected via env, audit chains persisted
on a PersistentVolume.

The manifests in `deploy/k8s/` are a starting point. Adapt
namespace, image tag, and resource limits to your cluster.

## Layout

```
deploy/k8s/
├── 00-namespace.yaml
├── 10-configmap.yaml      # servers.yml + clients.yml (non-secret)
├── 20-secret.yaml         # tokens — keep this out of git
├── 30-pvc.yaml            # audit chain volume
├── 40-deployment.yaml     # the workload itself
├── 50-service.yaml        # ClusterIP, exposes :7777
└── 60-hpa.yaml            # optional autoscale on CPU
```

## Apply

```bash
kubectl apply -f deploy/k8s/00-namespace.yaml
kubectl apply -f deploy/k8s/

kubectl -n scry get pods -w
kubectl -n scry logs deploy/scry -f
```

## Readiness probe = `scry doctor`

The Deployment uses `scry doctor` as both startup + readiness
probes — the same diagnostic operators run locally. Failed probes
bubble up as not-ready pods so traffic re-routes during a config
drift.

## Multi-region / HA

Each replica owns an independent audit chain (per-pod PVC). For
centralised audit:

1. Mount a ReadWriteMany PVC instead of the per-pod default.
2. Set `--audit-keep` high enough to survive multi-pod writes.
3. Plan for the chain-tampering surface: shared writers mean any
   replica can append; consider an external append-only sink
   instead (e.g. ship records to a SIEM via the OTel logs
   bridge — queued as v0.3 work).

## TLS

Two patterns:

1. **TLS at the Service** — Use cert-manager + a TLS-terminating
   ingress (Envoy, ingress-nginx). scry binds plain HTTP inside
   the cluster. Simplest for most deployments.

2. **Embedded TLS** — Mount cert + key as a Secret, pass
   `--tls-cert` / `--tls-key`. Right for mTLS service-mesh
   identity. See `deploy/k8s/40-deployment.yaml`'s commented
   block.

## Scaling

`60-hpa.yaml` scales on CPU. For agent-traffic-aware scaling, add
a custom metric from scry's OTel exporter
(`scry.query_execute.count`) via the Prometheus adapter or
KEDA-prometheus-scaler.

Per-replica state to remember:

- Audit chains are per-pod with the default PVC; rotation runs
  independently. `gate_chain` returns the chain visible to the
  pod the agent connected to.
- Hot reload of servers.yml requires every replica to see the
  same ConfigMap. k8s propagates ConfigMap changes within ~1
  minute; scry's fsnotify watcher picks up the inotify event
  inside that window.

## Limits

The Deployment template starts at 50m CPU / 64Mi memory. Scale up
for high-throughput hosted scenarios — the SQLite FTS5 index is
the hot path and benefits from cache headroom.
