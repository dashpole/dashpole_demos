#!/usr/bin/env bash
# ==============================================================================
# Milestone 4: Test eBPF Context Propagation and Distributed Tracing
# ==============================================================================

set -euo pipefail

echo "Sending query through web-frontend inside the cluster..."
RESULT=$(kubectl exec -n ai-agent deploy/web-frontend -- python3 -c '
import urllib.request, json
req = urllib.request.Request(
    "http://127.0.0.1:8080/api/query",
    data=json.dumps({"prompt": "Verify return eligibility for order 1042"}).encode(),
    headers={"Content-Type": "application/json"}
)
with urllib.request.urlopen(req, timeout=30) as r:
    print(r.read().decode())
')

echo "Query Response:"
echo "${RESULT}" | jq . || echo "${RESULT}"
echo ""
echo "Traffic dispatched! Traces are now being exported via eBPF to GKE Managed OTel."
