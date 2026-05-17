#!/bin/bash
set -euo pipefail

ENV="${1:?usage: deploy.sh <env> [--dry-run]}"
DRY_RUN="${2:-}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_DIR="${SCRIPT_DIR}/environments/${ENV}"

if [[ ! -d "$ENV_DIR" ]]; then
  echo "error: environment '${ENV}' not found at ${ENV_DIR}" >&2
  echo "available: $(ls "${SCRIPT_DIR}/environments/")" >&2
  exit 1
fi

if [[ "$DRY_RUN" == "--dry-run" ]]; then
  kubectl apply -k "$ENV_DIR" --dry-run=client
else
  kubectl apply -k "$ENV_DIR"
fi
