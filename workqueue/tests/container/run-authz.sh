#!/bin/bash
# Authorization integration tests for Testing Farm.
# Tests Cedar policy enforcement on pre-built container images.
set -euo pipefail

RECEIVER_IMAGE="${FACTORY_RECEIVER_IMAGE:-quay.io/hummingbird/factory-receiver:latest}"
DISPATCHER_IMAGE="${FACTORY_DISPATCHER_IMAGE:-quay.io/hummingbird/factory-dispatcher:latest}"
ADMIN_IMAGE="${FACTORY_ADMIN_IMAGE:-quay.io/hummingbird/factory-admin:latest}"
RECONCILER_IMAGE="${ECHO_RECONCILER_IMAGE:-quay.io/hummingbird/echo-reconciler:latest}"

COMPOSE_FILE="tests/container/docker-compose.authz.yaml"
PASS=0
FAIL=0

cleanup() {
    echo "=== Tearing down ==="
    docker compose -f "$COMPOSE_FILE" down -v 2>/dev/null || true
}
trap cleanup EXIT

assert() {
    local name="$1"
    local expected="$2"
    local actual="$3"
    if [ "$actual" = "$expected" ]; then
        echo "  PASS: $name"
        PASS=$((PASS + 1))
    else
        echo "  FAIL: $name (expected=$expected actual=$actual)"
        FAIL=$((FAIL + 1))
    fi
}

export RECEIVER_IMAGE DISPATCHER_IMAGE ADMIN_IMAGE RECONCILER_IMAGE

echo "=== Starting stack with Cedar authz ==="
docker compose -f "$COMPOSE_FILE" up -d
sleep 5

# --- SRE team: full access ---
echo "=== Test: SRE team allowed ==="

sre_enqueue=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    -H "X-Forwarded-User: alice" -H "X-Forwarded-Groups: sre-team" \
    http://localhost:8081/enqueue -d '{"key":"sre-item","priority":50}')
assert "SRE enqueue allowed" "200" "$sre_enqueue"

sre_read=$(curl -s -o /dev/null -w '%{http_code}' \
    -H "X-Forwarded-User: alice" -H "X-Forwarded-Groups: sre-team" \
    http://localhost:18080/admin/queues)
assert "SRE admin read allowed" "200" "$sre_read"

# --- Echo team: scoped access ---
echo "=== Test: Echo team scoped access ==="

team_enqueue=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    -H "X-Forwarded-User: bob" -H "X-Forwarded-Groups: echo-team" \
    http://localhost:8081/enqueue -d '{"key":"team-item","priority":10}')
assert "echo-team enqueue to echo queue allowed" "200" "$team_enqueue"

# --- Authenticated user: read-only ---
echo "=== Test: Authenticated user read-only ==="

user_read=$(curl -s -o /dev/null -w '%{http_code}' \
    -H "X-Forwarded-User: eve" -H "X-Forwarded-Groups: other" \
    http://localhost:18080/admin/queues)
assert "authenticated user read allowed" "200" "$user_read"

user_enqueue=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    -H "X-Forwarded-User: eve" -H "X-Forwarded-Groups: other" \
    http://localhost:8081/enqueue -d '{"key":"denied","priority":0}')
assert "authenticated user enqueue denied" "403" "$user_enqueue"

# --- Unauthenticated: denied ---
echo "=== Test: Unauthenticated denied ==="

noauth_read=$(curl -s -o /dev/null -w '%{http_code}' \
    http://localhost:18080/admin/queues)
assert "unauthenticated admin read denied" "403" "$noauth_read"

noauth_enqueue=$(curl -s -o /dev/null -w '%{http_code}' -X POST \
    http://localhost:8081/enqueue -d '{"key":"denied","priority":0}')
assert "unauthenticated enqueue denied" "403" "$noauth_enqueue"

# --- Verify authorized items were actually processed ---
echo "=== Test: Authorized items processed ==="
sleep 5
sre_status=$(curl -s \
    -H "X-Forwarded-User: alice" -H "X-Forwarded-Groups: sre-team" \
    http://localhost:18080/admin/queues/echo/items/sre-item | \
    python3 -c "import sys,json; print(json.load(sys.stdin)['item']['status'])" 2>/dev/null || echo "unknown")
assert "SRE item processed" "succeeded" "$sre_status"

echo
echo "=== Results ==="
echo "PASS: $PASS"
echo "FAIL: $FAIL"

if [ "$FAIL" -gt 0 ]; then
    echo
    echo "=== Service logs ==="
    docker compose -f "$COMPOSE_FILE" logs --tail 20
    exit 1
fi
