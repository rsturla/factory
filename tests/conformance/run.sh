#!/bin/bash
# PostgreSQL conformance tests for Testing Farm.
# DynamoDB conformance runs in GitHub Actions CI.
set -euo pipefail

COMPOSE_FILE="tests/conformance/docker-compose.yaml"

cleanup() {
    echo "=== Tearing down ==="
    docker compose -f "$COMPOSE_FILE" down -v 2>/dev/null || true
}
trap cleanup EXIT

echo "=== Starting PostgreSQL ==="
docker compose -f "$COMPOSE_FILE" up -d postgres
sleep 5

echo "Waiting for PostgreSQL..."
for i in $(seq 1 30); do
    if docker compose -f "$COMPOSE_FILE" exec -T postgres pg_isready -U factory > /dev/null 2>&1; then
        break
    fi
    sleep 1
done

echo
echo "=== PostgreSQL Conformance (31 tests + 6 migration tests) ==="
DATABASE_URL="postgres://factory:factory@localhost:5432/factory?sslmode=disable" \
    go test -v -count=1 -timeout 60s ./internal/store/postgres/
