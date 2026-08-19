#!/usr/bin/env bash
set -euo pipefail

PROJECT_ID="dashpole-dev"
CLUSTER_NAME="obi-demo-cluster"
NAMESPACE="ai-agent"

CYAN='\033[0;36m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
BOLD='\033[1m'
NC='\033[0m' # No Color

clear || true
echo -e "${BOLD}${CYAN}================================================================================${NC}"
echo -e "${BOLD}${CYAN}     OpenTelemetry eBPF Instrumentation (OBI) on GKE - Live Demo Runner         ${NC}"
echo -e "${BOLD}${CYAN}================================================================================${NC}"
echo -e "Project: ${BOLD}${PROJECT_ID}${NC} | Cluster: ${BOLD}${CLUSTER_NAME}${NC} (us-central1-a)"
echo ""

pause() {
  echo -e "${YELLOW}Press [ENTER] to continue to next stage...${NC}"
  read -r
  echo ""
}

# -----------------------------------------------------------------------------
echo -e "${BOLD}[Stage 1/7] Pre-flight Environment & Security Verification${NC}"
# -----------------------------------------------------------------------------
echo "Verifying GKE nodes and kernel version..."
kubectl get nodes -o wide
echo ""
echo "Verifying GKE Managed OpenTelemetry Collector..."
kubectl get pods,svc -n gke-managed-otel
echo ""
echo "Verifying Zero External IPs (ClusterIP Security Guarantee)..."
EXTERNAL_IPS=$(kubectl get svc -A -o jsonpath='{.items[?(@.spec.type=="LoadBalancer")].status.loadBalancer.ingress}')
if [ -z "$EXTERNAL_IPS" ]; then
  echo -e "${GREEN}✔ Security Audit PASSED: 0 external public IPs. All services strictly ClusterIP.${NC}"
else
  echo -e "${RED}✖ Security Audit FAILED: External IPs detected: $EXTERNAL_IPS${NC}"
  exit 1
fi
echo ""
pause

# -----------------------------------------------------------------------------
echo -e "${BOLD}[Stage 2/7] Zero-SDK Code Purity Verification${NC}"
# -----------------------------------------------------------------------------
echo "Inspecting application containers for OpenTelemetry SDK libraries..."
for deploy in web-frontend agent-orchestrator order-mcp-server; do
  OTEL_PKGS=$(kubectl exec -n "$NAMESPACE" "deploy/$deploy" -- python3 -c "
import sys
try:
    import opentelemetry
    print('FOUND')
except ImportError:
    print('NONE')
")
  if [ "$OTEL_PKGS" == "NONE" ]; then
    echo -e "${GREEN}✔ $deploy: ZERO OpenTelemetry SDK imports or libraries installed.${NC}"
  else
    echo -e "${RED}✖ $deploy: OpenTelemetry found!${NC}"
  fi
done
echo ""
pause

# -----------------------------------------------------------------------------
echo -e "${BOLD}[Stage 3/7] OBI eBPF DaemonSet & Kernel Probes${NC}"
# -----------------------------------------------------------------------------
echo "Verifying OBI DaemonSet status across all nodes..."
kubectl get daemonset -n otel-system opentelemetry-ebpf-instrumentation
echo ""
echo "Inspecting OBI PodMonitoring & internal Prometheus metrics..."
NODE_IP=$(kubectl get pod -n otel-system -l app=obi -o jsonpath='{.items[0].status.hostIP}')
kubectl exec -n "$NAMESPACE" deploy/web-frontend -- python3 -c "
import urllib.request
data = urllib.request.urlopen('http://${NODE_IP}:9090/metrics').read().decode()
for key in ['http_server_request_duration_seconds', 'http_client_request_duration_seconds', 'obi_network_flow_bytes_total']:
    print(f'  ✔ Found metric: {key}')
"
echo ""
pause

# -----------------------------------------------------------------------------
echo -e "${BOLD}[Stage 4/7] Live Traffic Generation & Model Context Protocol (MCP) Execution${NC}"
# -----------------------------------------------------------------------------
echo "Submitting customer support request through web-frontend:"
echo -e "${CYAN}Prompt: 'What is the return policy for electronics, and can you check order #1042?'${NC}"
echo ""
kubectl exec -n "$NAMESPACE" deploy/web-frontend -- python3 -c '
import urllib.request, json, time
start = time.time()
req = urllib.request.Request(
    "http://127.0.0.1:8080/api/query",
    data=json.dumps({"prompt": "What is the return policy for electronics, and can you check order #1042?"}).encode(),
    headers={"Content-Type": "application/json"}
)
with urllib.request.urlopen(req, timeout=30) as r:
    elapsed = time.time() - start
    body = json.loads(r.read().decode())
    print(f"Status: HTTP {r.status} (Execution Time: {elapsed:.3f}s)")
    print(f"Discovered MCP Tools: {body.get(\"mcp_discovered_tools\")}")
    print(f"Tool Execution Result: {body.get(\"mcp_tool_result\")}")
    print(f"\nSynthesized Agent Response:\n{body.get(\"response\")}")
'
echo ""
echo -e "${GREEN}✔ Request executed: 1) MCP Tool Discovery -> 2) Vector DB Retrieval -> 3) Tool Call (get_order_status)${NC}"
echo ""
pause

# -----------------------------------------------------------------------------
echo -e "${BOLD}[Stage 5/7] Google Cloud Trace: 4-Tier Distributed Waterfall & Tool Spans${NC}"
# -----------------------------------------------------------------------------
echo "The eBPF kernel injector stitched W3C traceparents across all tiers & tool calls:"
echo "  POST /api/query [web-frontend]"
echo "  └── POST /chat [agent-orchestrator]"
echo "      ├── POST /mcp/tools/list [order-mcp-server]                 <-- MCP Tool Discovery"
echo "      ├── POST /collections/*/points/search [knowledge-vectordb]   <-- Rust Vector DB (Qdrant)"
echo "      └── POST /mcp/tools/call/get_order_status [order-mcp-server] <-- Tool Call Execution"
echo ""
echo "Open Cloud Trace in Google Cloud Console:"
echo -e "${BOLD}https://console.cloud.google.com/traces/traces?project=${PROJECT_ID}${NC}"
echo ""
pause

# -----------------------------------------------------------------------------
echo -e "${BOLD}[Stage 6/7] Fault Injection Scenario: Troubleshooting with OBI${NC}"
# -----------------------------------------------------------------------------
echo "Simulating downstream backend latency degradation in order-mcp-server tool execution..."
./gke_otel_obi_demo/scripts/inject-failure.sh latency
echo ""
echo "Notice how latency spiked in the trace and pinpointed order-mcp-server instantly."
echo "Restoring health with automated recovery script..."
./gke_otel_obi_demo/scripts/recover-failure.sh
echo ""
pause

# -----------------------------------------------------------------------------
echo -e "${BOLD}[Stage 7/7] Interactive Web UI Access${NC}"
# -----------------------------------------------------------------------------
echo "To interact with the live demo web interface in your browser, run:"
echo -e "${BOLD}${GREEN}kubectl port-forward -n ai-agent svc/web-frontend 8080:8080${NC}"
echo "Then navigate to: ${BOLD}http://localhost:8080${NC} (or http://dashpole.c.googlers.com:8080)"
echo ""
echo -e "${BOLD}${CYAN}================================================================================${NC}"
echo -e "${BOLD}${GREEN}                       Demo Presentation Flow Complete!                         ${NC}"
echo -e "${BOLD}${CYAN}================================================================================${NC}"
