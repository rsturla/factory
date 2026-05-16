#!/bin/bash
set -euo pipefail

COMPOSE_FILE="${COMPOSE_FILE:-tests/container/docker-compose.stress.yaml}"
CONTAINER_WAIT_TIMEOUT="${CONTAINER_WAIT_TIMEOUT:-60}"
TEST_ITEM_COUNT="${TEST_ITEM_COUNT:-10000}"
EXPECTED_MIN_THROUGHPUT="${EXPECTED_MIN_THROUGHPUT:-100}" # items/sec

echo "=== Factory Workqueue Stress Test ==="
echo "Items: $TEST_ITEM_COUNT"
echo "Expected throughput: >$EXPECTED_MIN_THROUGHPUT items/sec"

cleanup() {
    echo "=== Cleanup ==="
    docker compose -f "$COMPOSE_FILE" down -v --remove-orphans 2>/dev/null || true
}
trap cleanup EXIT

echo "=== Starting services ==="
docker compose -f "$COMPOSE_FILE" up -d

echo "=== Waiting for services ==="
for i in $(seq 1 "$CONTAINER_WAIT_TIMEOUT"); do
    if docker compose -f "$COMPOSE_FILE" exec -T echo-receiver wget -q -O- http://localhost:8081/healthz >/dev/null 2>&1; then
        echo "Receiver ready"
        break
    fi
    if [ "$i" -eq "$CONTAINER_WAIT_TIMEOUT" ]; then
        echo "ERROR: Receiver failed to become healthy"
        docker compose -f "$COMPOSE_FILE" logs
        exit 1
    fi
    sleep 1
done

echo "=== Enqueuing $TEST_ITEM_COUNT items ==="
start_time=$(date +%s)
for i in $(seq 1 "$TEST_ITEM_COUNT"); do
    curl -sf -X POST http://localhost:8081/enqueue \
        -H "Content-Type: application/json" \
        -d "{\"key\": \"stress-$i\"}" >/dev/null

    # Progress indicator
    if [ $((i % 1000)) -eq 0 ]; then
        echo "Enqueued $i items..."
    fi
done
enqueue_end=$(date +%s)
enqueue_duration=$((enqueue_end - start_time))
echo "Enqueued $TEST_ITEM_COUNT items in ${enqueue_duration}s"

echo "=== Waiting for processing ==="
processed=0
for i in $(seq 1 300); do  # 5 minute timeout
    metrics=$(curl -sf http://localhost:18080/metrics || echo "")
    processed=$(echo "$metrics" | grep '^factory_items_completed_total' | awk '{print $2}' || echo "0")

    if [ "$processed" -ge "$TEST_ITEM_COUNT" ]; then
        echo "All items processed!"
        break
    fi

    if [ $((i % 10)) -eq 0 ]; then
        echo "Processed: $processed / $TEST_ITEM_COUNT"
    fi

    sleep 1
done

end_time=$(date +%s)
total_duration=$((end_time - start_time))

if [ "$processed" -lt "$TEST_ITEM_COUNT" ]; then
    echo "ERROR: Only processed $processed / $TEST_ITEM_COUNT items in ${total_duration}s"
    docker compose -f "$COMPOSE_FILE" logs
    exit 1
fi

# Calculate throughput
throughput=$((TEST_ITEM_COUNT / total_duration))
echo "=== Results ==="
echo "Total duration: ${total_duration}s"
echo "Throughput: $throughput items/sec"

if [ "$throughput" -lt "$EXPECTED_MIN_THROUGHPUT" ]; then
    echo "WARNING: Throughput below expected minimum ($EXPECTED_MIN_THROUGHPUT items/sec)"
    echo "This may indicate performance degradation"
    # Don't fail - just warn. Hardware variance in Testing Farm is high.
fi

echo "=== Stress test passed ==="
