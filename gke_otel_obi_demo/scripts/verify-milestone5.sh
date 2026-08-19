#!/usr/bin/env bash
set -euo pipefail

PROJECT_ID="dashpole-dev"
CLUSTER_NAME="obi-demo-cluster"
NAMESPACE="ai-agent"

echo "=========================================================="
echo " Milestone 5: MCP, Vector DB & Cloud Monitoring RED Tests"
echo "=========================================================="

echo "[1/3] Generating traffic across multi-tier AI agent stack..."
for i in {1..5}; do
  kubectl exec -n "$NAMESPACE" deploy/web-frontend -- python3 -c '
import urllib.request, json
req = urllib.request.Request(
    "http://127.0.0.1:8080/api/query",
    data=json.dumps({"prompt": "Verification query for Milestone 5"}).encode(),
    headers={"Content-Type": "application/json"}
)
with urllib.request.urlopen(req, timeout=30) as r:
    assert r.status == 200
'
done
echo "Traffic generation completed successfully."

echo "[2/3] Verifying in-cluster Prometheus RED metrics on OBI pods..."
NODE_IP=$(kubectl get pod -n otel-system -l app=obi -o jsonpath='{.items[0].status.hostIP}')
kubectl exec -n "$NAMESPACE" deploy/web-frontend -- python3 -c "
import urllib.request, sys
data = urllib.request.urlopen('http://${NODE_IP}:9090/metrics').read().decode()
required = [
    'http_server_request_duration_seconds',
    'http_server_request_body_size_bytes',
    'http_server_response_body_size_bytes',
    'obi_network_flow_bytes_total',
    'target_info'
]
all_ok = True
for key in required:
    if key in data:
        print(f'  ✔ {key} verified')
    else:
        print(f'  ✖ {key} MISSING')
        all_ok = False
if not all_ok:
    sys.exit(1)
"

echo "[3/3] Verifying PodMonitoring resource in otel-system..."
kubectl get podmonitoring -n otel-system obi-metrics -o wide

echo "=========================================================="
echo " Milestone 5 In-Cluster Verification Complete: SUCCESS"
echo "=========================================================="
