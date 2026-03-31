# Kubernetes Deployment

## Prerequisites

- Kubernetes cluster
- [CNPG operator](https://cloudnative-pg.io/) installed
- Prometheus Operator installed (for ServiceMonitor)
- Container images pushed to `quay.io/hummingbird/`

## Deploy

### 1. Shared infrastructure

```bash
kubectl apply -f base/namespace.yaml
kubectl apply -f base/postgres.yaml
kubectl -n factory wait --for=condition=Ready cluster/factory-db --timeout=300s
kubectl apply -f base/admin.yaml
kubectl apply -f base/monitoring.yaml
kubectl apply -f base/networkpolicy.yaml
```

### 2. Reconciler stack (per queue)

```bash
kubectl apply -f reconcilers/echo/
```

This creates:
- `factory-echo-receiver` (2 replicas)
- `factory-echo-dispatcher` (1 replica, singleton)
- `factory-echo-reconciler` (2-100 replicas, HPA)
- `factory-echo` ServiceAccount

### 3. Verify

```bash
kubectl -n factory get pods
kubectl -n factory port-forward svc/factory-echo-receiver 8081
curl -X POST http://localhost:8081/enqueue -d '{"key":"test","priority":0}'

kubectl -n factory port-forward svc/factory-admin 8080
curl http://localhost:8080/admin/queues
```

## Adding a new reconciler

1. Create `reconcilers/<name>/` with `serviceaccount.yaml`, `receiver.yaml`, `dispatcher.yaml`, `reconciler.yaml`
2. Copy from `reconcilers/echo/` and change queue name, image, concurrency settings, HPA limits
3. `kubectl apply -f reconcilers/<name>/`

## Layout

```
kubernetes/
├── base/
│   ├── namespace.yaml        Namespace
│   ├── postgres.yaml         CNPG PostgreSQL cluster (3 instances)
│   ├── admin.yaml            Admin API Deployment + Service
│   ├── monitoring.yaml       ServiceMonitor for Prometheus
│   └── networkpolicy.yaml    Restrict traffic to factory namespace
└── reconcilers/
    └── echo/
        ├── serviceaccount.yaml
        ├── receiver.yaml       Deployment + Service
        ├── dispatcher.yaml     Deployment + Service
        └── reconciler.yaml     Deployment + Service + HPA
```

## Database credentials

CNPG creates a secret `factory-db-app` with connection details:

```yaml
env:
  - name: DATABASE_URL
    valueFrom:
      secretKeyRef:
        name: factory-db-app
        key: uri
```

## Scaling

- **Receiver**: increase `replicas` or add HPA on CPU
- **Dispatcher**: always 1 per queue
- **Reconciler**: auto-scaled by HPA on `factory_queue_depth{status="pending"}`
- **PostgreSQL**: scale via CNPG (instances, resources)

See [SCALING.md](../../docs/SCALING.md) for details.
