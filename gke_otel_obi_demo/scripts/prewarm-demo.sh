#!/usr/bin/env bash
# Pre-warms the demo cluster, DNS, and Cloud Trace indexing pipelines before the live presentation
set -euo pipefail

ENDPOINT="${1:-http://localhost:8080/api/query}"

echo "Starting pre-warming routine for GKE OBI demo..."

QUERIES=(
  "Check my order #1042 status and find our return policy for opened items."
  "What is the standard refund policy for electronics?"
  "Can I track order 1042 shipped via FedEx?"
)

for i in "${!QUERIES[@]}"; do
  echo "Warm-up query $((i+1))/${#QUERIES[@]}: \"${QUERIES[$i]}\""
  curl -s -o /dev/null -w "HTTP Status: %{http_code} (took %{time_total}s)\n" \
    -X POST "${ENDPOINT}" \
    -H "Content-Type: application/json" \
    -d "{\"prompt\": \"${QUERIES[$i]}\"}"
  sleep 2
done

echo "Pre-warming completed! Caches and trace indexing queues are warm."
