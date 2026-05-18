# Registry Probe

Monitors container registry availability and performance by periodically pulling a target image and recording metrics. Designed to detect registry outages, measure pull latency, and track image characteristics over time.

## Components

- **Probe** — Pulls a configured container image at a regular interval (default: 5 minutes) and records the outcome. Extracts manifest metadata including layer count and image size.

- **Metrics endpoint** — Exposes Prometheus metrics at `/metrics`:
  - Pull duration, success/failure counts, last success timestamp
  - Image size and layer count
  - Overall probe status (up/down)

- **Health check** — `/healthz` endpoint for liveness and readiness probes.
