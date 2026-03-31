#!/bin/bash
# Store conformance tests — runs the 31-test suite against PostgreSQL
# and DynamoDB+S3 using Docker containers.
set -euo pipefail

COMPOSE_FILE="tests/conformance/docker-compose.yaml"
PASS=0
FAIL=0

cleanup() {
    echo "=== Tearing down ==="
    docker compose -f "$COMPOSE_FILE" down -v 2>/dev/null || true
}
trap cleanup EXIT

echo "=== Starting test infrastructure ==="
docker compose -f "$COMPOSE_FILE" up -d
sleep 5

# Wait for services.
echo "Waiting for PostgreSQL..."
for i in $(seq 1 30); do
    if docker compose -f "$COMPOSE_FILE" exec -T postgres pg_isready -U factory > /dev/null 2>&1; then
        break
    fi
    sleep 1
done

echo "Waiting for DynamoDB Local..."
for i in $(seq 1 30); do
    if curl -sf http://localhost:8000/ > /dev/null 2>&1; then
        break
    fi
    sleep 1
done

echo "Waiting for rustfs bucket..."
for i in $(seq 1 30); do
    if AWS_ACCESS_KEY_ID=rustfsadmin AWS_SECRET_ACCESS_KEY=rustfsadmin \
       aws --endpoint-url http://localhost:9000 s3 ls s3://factory-test > /dev/null 2>&1; then
        break
    fi
    sleep 1
done

# --- PostgreSQL conformance ---
echo
echo "=== PostgreSQL Conformance (31 tests + 6 migration tests) ==="
if DATABASE_URL="postgres://factory:factory@localhost:5432/factory?sslmode=disable" \
   go test -v -count=1 -timeout 60s ./internal/store/postgres/ 2>&1; then
    echo "  RESULT: PASS"
    PASS=$((PASS + 1))
else
    echo "  RESULT: FAIL"
    FAIL=$((FAIL + 1))
fi

# --- DynamoDB conformance ---
echo
echo "=== DynamoDB+S3 Conformance (31 tests) ==="
if AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_REGION=us-east-1 \
   DDB_TEST_ENDPOINT=http://localhost:8000 \
   S3_TEST_ENDPOINT=http://localhost:9000 \
   S3_TEST_BUCKET=factory-test \
   go test -v -count=1 -timeout 120s ./internal/store/dynamodb/ 2>&1; then
    echo "  RESULT: PASS"
    PASS=$((PASS + 1))
else
    echo "  RESULT: FAIL"
    FAIL=$((FAIL + 1))
fi

echo
echo "=== Results ==="
echo "PASS: $PASS"
echo "FAIL: $FAIL"

if [ "$FAIL" -gt 0 ]; then
    exit 1
fi
