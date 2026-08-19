#!/usr/bin/env bash
set -euo pipefail

MODE="${1:-latency}"

echo "=========================================================="
echo " OBI Live Demo: Injecting Simulated Failure (${MODE})"
echo "=========================================================="

if [ "$MODE" == "latency" ]; then
  echo "Injecting 2.5s artificial latency into order-mcp-server..."
  kubectl set env deployment/order-mcp-server -n ai-agent INJECT_LATENCY=2.5 INJECT_ERROR-
elif [ "$MODE" == "error" ]; then
  echo "Injecting HTTP 500 error into order-mcp-server..."
  kubectl set env deployment/order-mcp-server -n ai-agent INJECT_ERROR=true INJECT_LATENCY-
else
  echo "Usage: $0 [latency|error]"
  exit 1
fi

kubectl rollout status deployment/order-mcp-server -n ai-agent --timeout=60s
# Allow short grace period for endpoint routing to switch cleanly
sleep 4

echo "----------------------------------------------------------"
echo "Sending test query during fault injection..."
kubectl exec -n ai-agent deploy/web-frontend -- python3 -c "
import urllib.request, json, time
start = time.time()
req = urllib.request.Request(
    'http://127.0.0.1:8080/api/query',
    data=json.dumps({'prompt': 'Can you check order 1042 during fault injection?'}).encode(),
    headers={'Content-Type': 'application/json'}
)
try:
    with urllib.request.urlopen(req, timeout=30) as r:
        elapsed = time.time() - start
        body = json.loads(r.read().decode())
        print(f'Query returned HTTP {r.status} in {elapsed:.2f}s (Status: {body.get(\"status\")})')
        print(f'Summary: {body.get(\"response\")[:80]}...')
except Exception as e:
    elapsed = time.time() - start
    print(f'Query failed with {e} in {elapsed:.2f}s')
"

echo "=========================================================="
echo " Failure active! Check Google Cloud Trace & Cloud Monitoring."
echo " Run ./scripts/recover-failure.sh to restore normal health."
echo "=========================================================="
