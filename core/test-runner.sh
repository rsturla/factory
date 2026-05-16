#!/bin/bash
set -e

export DATABASE_URL="postgres://factory:factory@localhost/factory?sslmode=disable"

echo "=== Starting Factory Services ==="

# Start mock queue receivers (just accept enqueue, no processing)
echo "Starting mock queue receivers..."

# Mock sf-pipeline receiver
(while true; do echo -e "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK" | nc -l 8081; done) &
PID_PIPELINE_RCV=$!

# Mock sf-stage receiver
(while true; do echo -e "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK" | nc -l 8082; done) &
PID_STAGE_RCV=$!

# Mock sf-output receiver
(while true; do echo -e "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK" | nc -l 8083; done) &
PID_OUTPUT_RCV=$!

sleep 1

# Start factory-api
echo "Starting factory-api..."
PIPELINE_BASE_PATH="examples/hello-agent/.factory" \
ENQUEUE_ENDPOINT="http://localhost:8081" \
PORT="8080" \
./bin/factory-api &
PID_API=$!

sleep 2

# Cleanup function
cleanup() {
    echo ""
    echo "=== Cleaning up ==="
    kill $PID_API $PID_PIPELINE_RCV $PID_STAGE_RCV $PID_OUTPUT_RCV 2>/dev/null || true
    wait 2>/dev/null || true
}

trap cleanup EXIT INT TERM

echo ""
echo "=== Services Running ==="
echo "API: http://localhost:8080"
echo "Database: postgres://localhost:5432/factory"
echo ""
echo "Press Ctrl+C to stop"
echo ""

# Keep script running
wait $PID_API
