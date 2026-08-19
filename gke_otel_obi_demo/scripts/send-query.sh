#!/usr/bin/env bash
# Helper script to send test queries to the AI Agent via port-forward or in-cluster proxy
set -euo pipefail

PROMPT="${1:-Check my order #1042 status and find our return policy for opened items.}"
ENDPOINT="${ENDPOINT:-http://localhost:8080/api/query}"

echo "Submitting query: \"${PROMPT}\""
echo "Target Endpoint:  ${ENDPOINT}"
echo "--------------------------------------------------------------------------------"

curl -s -X POST "${ENDPOINT}" \
  -H "Content-Type: application/json" \
  -d "{\"prompt\": \"${PROMPT}\"}" | jq . || cat
echo ""
