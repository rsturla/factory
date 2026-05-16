#!/bin/bash
# Standalone worker integration tests for Testing Farm.
# Tests the pull model: worker claims items via HTTP API, processes
# them locally, and reports completion back via the API.
set -euo pipefail

RECEIVER_IMAGE="${FACTORY_RECEIVER_IMAGE:-quay.io/hummingbird/factory-receiver:latest}"
DISPATCHER_IMAGE="${FACTORY_DISPATCHER_IMAGE:-quay.io/hummingbird/factory-dispatcher:latest}"
ADMIN_IMAGE="${FACTORY_ADMIN_IMAGE:-quay.io/hummingbird/factory-admin:latest}"
STANDALONE_WORKER_IMAGE="${STANDALONE_WORKER_IMAGE:-quay.io/hummingbird/standalone-worker:latest}"

COMPOSE_FILE="tests/container/docker-compose.standalone.yaml"
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

assert_gte() {
    local name="$1"
    local min="$2"
    local actual="$3"
    if [ "$actual" -ge "$min" ] 2>/dev/null; then
        echo "  PASS: $name ($actual >= $min)"
        PASS=$((PASS + 1))
    else
        echo "  FAIL: $name (expected >= $min, got $actual)"
        FAIL=$((FAIL + 1))
    fi
}

get_count() {
    local status="$1"
    curl -sf http://localhost:18080/admin/queues/echo | \
        python3 -c "import sys,json; print(json.load(sys.stdin).get('counts',{}).get('$status',0))" 2>/dev/null || echo 0
}

export RECEIVER_IMAGE DISPATCHER_IMAGE ADMIN_IMAGE STANDALONE_WORKER_IMAGE

echo "=== Starting standalone worker stack ==="
echo "  receiver:   $RECEIVER_IMAGE"
echo "  dispatcher: $DISPATCHER_IMAGE (scale-only)"
echo "  admin:      $ADMIN_IMAGE"
echo "  worker:     $STANDALONE_WORKER_IMAGE"
docker compose -f "$COMPOSE_FILE" up -d
sleep 5

# --- Services running ---
echo "=== Test: Services running ==="
assert "receiver healthz" "200" "$(curl -s -o /dev/null -w '%{http_code}' http://localhost:8081/healthz)"
assert "admin healthz" "200" "$(curl -s -o /dev/null -w '%{http_code}' http://localhost:18080/healthz)"

# Check worker is running.
worker_running=$(docker compose -f "$COMPOSE_FILE" ps --format json | python3 -c "
import sys, json
for line in sys.stdin:
    c = json.loads(line)
    if 'echo-worker' in c.get('Name',''):
        print(c.get('State',''))
        break
" 2>/dev/null || echo "unknown")
assert "standalone worker running" "running" "$worker_running"

# --- Dispatcher is in scale-only mode (not claiming) ---
echo "=== Test: Dispatcher in scale-only mode ==="
dispatcher_mode=$(docker compose -f "$COMPOSE_FILE" logs echo-dispatcher 2>/dev/null | grep -o "mode=scale-only" | head -1)
assert "dispatcher mode" "mode=scale-only" "$dispatcher_mode"

# --- Worker claims and processes items ---
echo "=== Test: Worker claims and processes items ==="
curl -sf -o /dev/null -X POST http://localhost:8081/enqueue -d '{"key":"standalone-1","priority":50}'
curl -sf -o /dev/null -X POST http://localhost:8081/enqueue -d '{"key":"standalone-2","priority":10}'
curl -sf -o /dev/null -X POST http://localhost:8081/enqueue -d '{"key":"standalone-3","priority":90}'
echo "  Enqueued 3 items, waiting for worker..."
sleep 10

assert_gte "items processed by standalone worker" "3" "$(get_count succeeded)"

# --- Verify worker processed them (not dispatcher) ---
echo "=== Test: Worker identity ==="
worker_id=$(curl -sf http://localhost:18080/admin/queues/echo/items/standalone-1 | \
    python3 -c "import sys,json; print(json.load(sys.stdin)['item'].get('worker_id',''))" 2>/dev/null || echo "")
assert "processed by standalone worker" "standalone-test-worker" "$worker_id"

# --- Item history shows full lifecycle ---
echo "=== Test: Item history via API ==="
history_count=$(curl -sf http://localhost:18080/admin/queues/echo/items/standalone-1 | \
    python3 -c "import sys,json; print(len(json.load(sys.stdin).get('history',[])))" 2>/dev/null || echo 0)
assert_gte "item has history entries" "2" "$history_count"

# --- Batch processing ---
echo "=== Test: Batch of 10 items ==="
for i in $(seq 1 10); do
    curl -sf -o /dev/null -X POST http://localhost:8081/enqueue \
        -d "{\"key\":\"batch-standalone-$i\",\"priority\":0}"
done
sleep 15
assert_gte "batch processed" "13" "$(get_count succeeded)"

# --- Worker logs show claiming ---
echo "=== Test: Worker logs ==="
worker_claimed=$(docker compose -f "$COMPOSE_FILE" logs echo-worker 2>/dev/null | grep -c "claimed items" || echo 0)
assert_gte "worker claimed items" "1" "$worker_claimed"

worker_completed=$(docker compose -f "$COMPOSE_FILE" logs echo-worker 2>/dev/null | grep -c "completed" || echo 0)
assert_gte "worker completed items" "3" "$worker_completed"

echo
echo "=== Results ==="
echo "PASS: $PASS"
echo "FAIL: $FAIL"

if [ "$FAIL" -gt 0 ]; then
    echo
    echo "=== Service logs ==="
    docker compose -f "$COMPOSE_FILE" logs --tail 30
    exit 1
fi
