# scry — raw Kubernetes manifests

For operators who don't use Helm. Mirrors `charts/scry`'s defaults so
`kubectl diff -f deploy/k8s/` against a `helm template` output is
small + auditable.

## Apply

```bash
# 1. Create a namespace
kubectl apply -f deploy/k8s/namespace.yaml

# 2. Fill in your servers.yml inside the Secret first, then apply.
kubectl apply -f deploy/k8s/secret.yaml

# 3. The rest:
kubectl apply -f deploy/k8s/deployment.yaml -f deploy/k8s/service.yaml
```

For Ingress / persistence / PodMonitor — copy the corresponding
sample below and apply.

## Customising

- **Image tag:** edit `deployment.yaml` → `spec.template.spec.containers[0].image`. Default: `ghcr.io/felixgeelhaar/scry:v0.7.0`.
- **Replicas:** `spec.replicas` in `deployment.yaml`. Default 1.
- **Args / flags:** `spec.template.spec.containers[0].args`. Edit to add `--cache-ttl`, `--cost-ceiling`, etc.
- **Env-sourced tokens:** add `envFrom: - secretRef: name: scry-upstream-tokens` under the container so `env://VAR` refs in servers.yml resolve.

For more complex deployments, the Helm chart at `charts/scry/`
exposes the same surface declaratively.
