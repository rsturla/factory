# Authentication & Authorization

Factory separates authentication (who are you?) from authorization (can you do this?).

## Architecture

```
User → OAuth Proxy → Factory service
       (authn)        (authz)
       sets headers   reads headers, evaluates policy
```

**Authentication** is external. An OAuth proxy (OpenShift OAuth Proxy, Envoy, etc.) sits in front of the factory services and handles login, token validation, and session management. It sets HTTP headers that identify the caller:

- `X-Forwarded-User` — the authenticated username
- `X-Forwarded-Groups` — comma-separated group memberships

**Authorization** is internal. The factory reads these headers and delegates the allow/deny decision to a pluggable policy backend.

## Authorization backends

Set `AUTHZ_BACKEND` to choose:

| Backend | Value | Config | How it works |
|---------|-------|--------|-------------|
| Noop | `noop` | None | Allow everything. Default for development and deployments where auth is handled entirely by NetworkPolicy. |
| Cedar | `cedar` | `AUTHZ_CEDAR_POLICY_PATH` | Evaluate Cedar policies in-process. No external server. Policies loaded from a file or directory on startup. |
| OPA | `opa` | `AUTHZ_OPA_ENDPOINT` | Call an external OPA server via REST API. OPA runs as a sidecar or standalone service. |

## Actions

Every API endpoint is mapped to an action. The authorization backend receives the user, their groups, the action, and the target queue:

| Action | Endpoint | Description |
|--------|----------|-------------|
| `enqueue` | `POST /enqueue` | Enqueue a work item |
| `queues:read` | `GET /admin/queues` | List queues |
| `items:read` | `GET /admin/queues/{name}/items` | List or get items |
| `items:retry` | `POST /admin/queues/{name}/items/{key}/retry` | Retry a failed item |
| `items:cancel` | `POST /admin/queues/{name}/items/{key}/cancel` | Cancel an item |
| `deadletter:purge` | `DELETE /admin/queues/{name}/dead-letters` | Purge dead letters |
| `workers:read` | `GET /admin/workers` | List workers |
| `events:stream` | `GET /admin/queues/{name}/events` | Stream events |
| `claim` | `POST /wq/claim` | Claim items from a queue (standalone workers) |
| `complete` | `POST /wq/complete`, `POST /wq/fail`, `POST /wq/heartbeat` | Complete, fail, or heartbeat a claimed item |
| `requeue` | `POST /wq/requeue`, `POST /wq/requeue-undo` | Requeue an item or undo a requeue |
| `deadletter` | `POST /wq/deadletter` | Dead-letter an item |
| `transition` | `POST /wq/transition` | Transition an item between states |
| `queue:admin` | `POST /wq/ensure-queue`, `POST /wq/repair`, `POST /wq/record-history`, `POST /wq/set-paused` | Queue management operations |

Actions scoped to a queue (items, retry, cancel, purge, events) include the queue name in the authorization check. This enables per-queue permissions — a team can have write access to their queue but read-only access to others.

## Cedar

Cedar policies are evaluated in-process using the [cedar-go](https://github.com/cedar-policy/cedar-go) SDK. No sidecar or external service needed.

### Setup

```bash
AUTHZ_BACKEND=cedar
AUTHZ_CEDAR_POLICY_PATH=/etc/factory/policies  # file or directory
```

If a directory, all `.cedar` files are loaded. One file per concern is recommended.

### PARC mapping

Cedar uses Principal, Action, Resource, Context. Factory maps to:

| Cedar | Factory |
|-------|---------|
| `Factory::User::"alice"` | `X-Forwarded-User: alice` |
| `Factory::Action::"items:retry"` | The API action |
| `Factory::Queue::"rpm-update"` | The target queue |
| `context.groups` | `X-Forwarded-Groups` as a set |

### Example policies

**SRE full access** (`sre.cedar`):
```cedar
permit(
    principal,
    action,
    resource
) when {
    context.groups.contains("sre-team")
};
```

**Read-only for everyone** (`read-only.cedar`):
```cedar
permit(
    principal,
    action in [
        Factory::Action::"queues:read",
        Factory::Action::"items:read",
        Factory::Action::"workers:read",
        Factory::Action::"events:stream"
    ],
    resource
);
```

**Team-scoped write** (`rpm-team.cedar`):
```cedar
permit(
    principal,
    action in [
        Factory::Action::"enqueue",
        Factory::Action::"items:retry"
    ],
    resource == Factory::Queue::"rpm-update"
) when {
    context.groups.contains("rpm-team")
};
```

### Production deployment

Mount policies from a ConfigMap:

```yaml
volumes:
  - name: policies
    configMap:
      name: factory-cedar-policies
containers:
  - name: admin
    env:
      - name: AUTHZ_BACKEND
        value: cedar
      - name: AUTHZ_CEDAR_POLICY_PATH
        value: /policies
    volumeMounts:
      - name: policies
        mountPath: /policies
```

Policy changes require a pod restart (or a file-watching mechanism, not currently implemented). Update the ConfigMap and do a rolling restart.

## OPA

OPA policies are evaluated by an external [Open Policy Agent](https://www.openpolicyagent.org/) server. The factory sends the authorization request as JSON and OPA returns allow/deny.

### Setup

```bash
AUTHZ_BACKEND=opa
AUTHZ_OPA_ENDPOINT=http://localhost:8181
```

### Request format

The factory sends:

```json
{
  "input": {
    "user": "alice",
    "groups": ["sre-team", "on-call"],
    "action": "items:retry",
    "queue": "rpm-update"
  }
}
```

OPA must return:

```json
{"result": {"allow": true}}
```

### Example Rego policy

**SRE full access** (`sre.rego`):
```rego
package factory.authz

allow if {
    input.groups[_] == "sre-team"
}
```

**Read-only for everyone** (`read_only.rego`):
```rego
package factory.authz

allow if {
    input.action in {"queues:read", "items:read", "workers:read", "events:stream"}
}
```

**Default deny** (`default.rego`):
```rego
package factory.authz

default allow = false
```

All `.rego` files in the same package are merged by OPA automatically.

### Production deployment

Run OPA as a sidecar:

```yaml
containers:
  - name: admin
    env:
      - name: AUTHZ_BACKEND
        value: opa
      - name: AUTHZ_OPA_ENDPOINT
        value: http://localhost:8181

  - name: opa
    image: openpolicyagent/opa:latest
    args: ["run", "--server", "--addr", ":8181", "/policies"]
    volumeMounts:
      - name: policies
        mountPath: /policies

volumes:
  - name: policies
    configMap:
      name: factory-opa-policies
```

OPA supports hot-reloading — policy changes via ConfigMap update take effect without restarting OPA.

## Cedar vs OPA

| | Cedar | OPA |
|---|---|---|
| Evaluation | In-process (microseconds) | HTTP call (milliseconds) |
| Deployment | No extra service | Sidecar or standalone |
| Policy language | Cedar | Rego |
| Hot reload | Restart required | Automatic |
| Audit logging | Application-level | Built-in decision log |
| Ecosystem | AWS-originated, growing | CNCF graduated, widely adopted |

Choose Cedar if you want simplicity (no sidecar, no network call). Choose OPA if you want hot-reloading, built-in audit logs, or your organization already uses OPA.

## Testing authorization

Use the docker-compose files with spoofed headers:

```bash
# Cedar
cd deploy && docker compose -f docker-compose.cedar.yaml up --build -d

# OPA
cd deploy && docker compose -f docker-compose.opa.yaml up --build -d

# Allowed (SRE):
curl -H "X-Forwarded-User: alice" -H "X-Forwarded-Groups: sre-team" \
  http://localhost:18080/admin/queues

# Denied (random user, write):
curl -X POST -H "X-Forwarded-User: eve" -H "X-Forwarded-Groups: other" \
  http://localhost:8081/enqueue -d '{"key":"test","priority":0}'
# → 403 Forbidden

# Denied (unauthenticated):
curl http://localhost:18080/admin/queues
# → 403 Forbidden
```
