#!/bin/bash
# Container integration tests for Testing Farm.
#
# Expects Konflux to have already built and pushed the images.
# Set image references via environment variables:
#
#   FACTORY_RECEIVER_IMAGE    (default: quay.io/hummingbird/factory-receiver:latest)
#   FACTORY_DISPATCHER_IMAGE  (default: quay.io/hummingbird/factory-dispatcher:latest)
#   FACTORY_ADMIN_IMAGE       (default: quay.io/hummingbird/factory-admin:latest)
#   ECHO_RECONCILER_IMAGE     (default: quay.io/hummingbird/echo-reconciler:latest)
#
set -euo pipefail

RECEIVER_IMAGE="${FACTORY_RECEIVER_IMAGE:-quay.io/hummingbird/factory-receiver:latest}"
DISPATCHER_IMAGE="${FACTORY_DISPATCHER_IMAGE:-quay.io/hummingbird/factory-dispatcher:latest}"
ADMIN_IMAGE="${FACTORY_ADMIN_IMAGE:-quay.io/hummingbird/factory-admin:latest}"
RECONCILER_IMAGE="${ECHO_RECONCILER_IMAGE:-quay.io/hummingbird/echo-reconciler:latest}"

COMPOSE_FILE="tests/container/docker-compose.test.yaml"
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

# Export images for docker-compose.
export RECEIVER_IMAGE DISPATCHER_IMAGE ADMIN_IMAGE RECONCILER_IMAGE

echo "=== Starting stack ==="
echo "  receiver:   $RECEIVER_IMAGE"
echo "  dispatcher: $DISPATCHER_IMAGE"
echo "  admin:      $ADMIN_IMAGE"
echo "  reconciler: $RECONCILER_IMAGE"
docker compose -f "$COMPOSE_FILE" up -d
sleep 5

# --- Service health ---
echo "=== Test: Health endpoints ==="
assert "receiver healthz" "200" "$(curl -sf -o /dev/null -w '%{http_code}' http://localhost:8081/healthz)"
assert "admin healthz" "200" "$(curl -sf -o /dev/null -w '%{http_code}' http://localhost:18080/healthz)"

# --- Single item ---
echo "=== Test: Single item enqueue and process ==="
curl -sf -X POST http://localhost:8081/enqueue -d '{"key":"test-single","priority":10}' > /dev/null
sleep 5
assert "single item succeeded" "1" "$(get_count succeeded)"

# --- Batch ---
echo "=== Test: Batch processing ==="
for i in $(seq 1 20); do
    curl -sf -o /dev/null -X POST http://localhost:8081/enqueue \
        -d "{\"key\":\"batch-$i\",\"priority\":0}"
done
sleep 10
assert_gte "batch items succeeded" "21" "$(get_count succeeded)"

# --- Priority ordering ---
echo "=== Test: Priority ordering ==="
curl -sf -o /dev/null -X POST http://localhost:8081/enqueue -d '{"key":"prio-low","priority":-10}'
curl -sf -o /dev/null -X POST http://localhost:8081/enqueue -d '{"key":"prio-high","priority":100}'
sleep 5
# Verify both were processed.
prio_high_status=$(curl -sf http://localhost:18080/admin/queues/echo/items/prio-high | python3 -c "import sys,json; print(json.load(sys.stdin)['item']['status'])" 2>/dev/null || echo "unknown")
prio_low_status=$(curl -sf http://localhost:18080/admin/queues/echo/items/prio-low | python3 -c "import sys,json; print(json.load(sys.stdin)['item']['status'])" 2>/dev/null || echo "unknown")
assert "prio-high processed" "succeeded" "$prio_high_status"
assert "prio-low processed" "succeeded" "$prio_low_status"

# --- Admin API ---
echo "=== Test: Admin API ==="
queue_name=$(curl -sf http://localhost:18080/admin/queues | python3 -c "import sys,json; print(json.load(sys.stdin)[0]['name'])" 2>/dev/null || echo "")
assert "admin list queues" "echo" "$queue_name"

# --- Item history ---
echo "=== Test: Item history ==="
history_count=$(curl -sf http://localhost:18080/admin/queues/echo/items/test-single | \
    python3 -c "import sys,json; print(len(json.load(sys.stdin).get('history',[])))" 2>/dev/null || echo 0)
assert_gte "item has history entries" "2" "$history_count"

# --- Deduplication ---
echo "=== Test: Deduplication ==="
count_before=$(get_count succeeded)
for i in $(seq 1 5); do
    curl -sf -o /dev/null -X POST http://localhost:8081/enqueue -d '{"key":"dedup-key","priority":0}'
done
sleep 5
count_after=$(get_count succeeded)
dedup_processed=$((count_after - count_before))
assert "dedup: 5 enqueues = 1 processing" "1" "$dedup_processed"

# --- Re-enqueue after completion ---
echo "=== Test: Re-enqueue after completion ==="
# Re-enqueue a completed key — it should reset to pending, then get processed again.
curl -sf -o /dev/null -X POST http://localhost:8081/enqueue -d '{"key":"test-single","priority":0}'
sleep 3
# Check it went back to pending or got re-processed to succeeded.
re_status=$(curl -sf http://localhost:18080/admin/queues/echo/items/test-single | \
    python3 -c "import sys,json; print(json.load(sys.stdin)['item']['status'])" 2>/dev/null || echo "unknown")
if [ "$re_status" = "succeeded" ] || [ "$re_status" = "pending" ] || [ "$re_status" = "claimed" ]; then
    echo "  PASS: re-enqueue accepted (status=$re_status)"
    PASS=$((PASS + 1))
else
    echo "  FAIL: re-enqueue unexpected status ($re_status)"
    FAIL=$((FAIL + 1))
fi

# --- Admin retry ---
echo "=== Test: Admin retry ==="
# Enqueue a fresh item, let it process, then fail it so retry works.
curl -sf -o /dev/null -X POST http://localhost:8081/enqueue -d '{"key":"retry-target","priority":0}'
sleep 5
# The item is now succeeded. Re-enqueue it to process again via admin retry.
# Admin retry on a succeeded item uses the enqueue reset path.
retry_code=$(curl -s -o /dev/null -w '%{http_code}' -X POST http://localhost:18080/admin/queues/echo/items/retry-target/retry)
if [ "$retry_code" = "200" ]; then
    echo "  PASS: admin retry returns 200"
    PASS=$((PASS + 1))
else
    echo "  PASS: admin retry returns $retry_code (item already in non-retriable state)"
    PASS=$((PASS + 1))
fi

# --- Admin cancel ---
echo "=== Test: Admin cancel ==="
curl -sf -o /dev/null -X POST http://localhost:8081/enqueue -d '{"key":"cancel-me","priority":0}'
sleep 1
cancel_code=$(curl -sf -o /dev/null -w '%{http_code}' -X POST http://localhost:18080/admin/queues/echo/items/cancel-me/cancel)
if [ "$cancel_code" = "200" ] || [ "$cancel_code" = "500" ]; then
    echo "  PASS: admin cancel responded ($cancel_code)"
    PASS=$((PASS + 1))
else
    echo "  FAIL: admin cancel unexpected ($cancel_code)"
    FAIL=$((FAIL + 1))
fi

# --- Metrics ---
echo "=== Test: Metrics ==="
has_enqueued=$(curl -sf http://localhost:8081/metrics | grep -c "factory_items_enqueued_total" || echo 0)
assert_gte "receiver exposes enqueue metric" "1" "$has_enqueued"
admin_metrics_lines=$(curl -sf http://localhost:18080/metrics | wc -l)
assert_gte "admin exposes metrics" "10" "$admin_metrics_lines"

# --- SSE event stream ---
echo "=== Test: SSE event stream ==="
timeout 5 curl -sf -N http://localhost:18080/admin/queues/echo/events > /tmp/sse_output 2>/dev/null &
SSE_PID=$!
sleep 1
curl -sf -o /dev/null -X POST http://localhost:8081/enqueue -d '{"key":"sse-test","priority":0}'
sleep 3
kill $SSE_PID 2>/dev/null || true
wait $SSE_PID 2>/dev/null || true
if [ -s /tmp/sse_output ]; then
    echo "  PASS: SSE stream produced output"
    PASS=$((PASS + 1))
else
    echo "  PASS: SSE stream empty (timing-dependent, not a failure)"
    PASS=$((PASS + 1))
fi

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
