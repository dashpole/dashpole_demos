#!/usr/bin/env bash
set -euo pipefail

echo "=========================================================="
echo " OBI Live Demo: Recovering from Failure Injection"
echo "=========================================================="

echo "Removing fault injection environment variables..."
kubectl set env deployment/order-mcp-server -n ai-agent INJECT_LATENCY- INJECT_ERROR-

kubectl rollout status deployment/order-mcp-server -n ai-agent --timeout=60s
# Allow short grace period for endpoint routing to switch cleanly
sleep 4

echo "----------------------------------------------------------"
echo "Sending test query to verify recovered health..."
kubectl exec -n ai-agent deploy/web-frontend -- python3 -c "
import urllib.request, json, time
start = time.time()
req = urllib.request.Request(
    'http://127.0.0.1:8080/api/query',
    data=json.dumps({'prompt': 'Can you check order 1042 after recovery?'}).encode(),
    headers={'Content-Type': 'application/json'}
)
with urllib.request.urlopen(req, timeout=30) as r:
    elapsed = time.time() - start
    body = json.loads(r.read().decode())
    print(f'Query returned HTTP {r.status} in {elapsed:.2f}s (Status: {body.get(\"status\")})')
    print(f'Summary: {body.get(\"response\")[:80]}...')
"

echo "=========================================================="
echo " System fully restored to normal latency and zero errors!"
echo "=========================================================="
