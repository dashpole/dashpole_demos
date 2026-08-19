#!/usr/bin/env bash
set -euo pipefail

PROJECT_ID="dashpole-dev"
CLUSTER_NAME="obi-demo-cluster"
NAMESPACE="ai-agent"

echo "================================================================================"
echo "    OpenTelemetry eBPF Instrumentation (OBI) on GKE - Master Test Suite         "
echo "================================================================================"
echo "Timestamp: $(date -u)"
echo "Target Project: $PROJECT_ID | Cluster: $CLUSTER_NAME"
echo ""

# -----------------------------------------------------------------------------
echo "[1/6] Testing Milestone 1: GKE Foundation & Managed OpenTelemetry..."
# -----------------------------------------------------------------------------
READY_NODES=$(kubectl get nodes --no-headers | grep -c "Ready" || true)
if [ "$READY_NODES" -ge 3 ]; then
  echo "  ✔ GKE Cluster has $READY_NODES Ready nodes."
else
  echo "  ✖ GKE Cluster has insufficient ready nodes: $READY_NODES"
  exit 1
fi

COLLECTOR_PODS=$(kubectl get pods -n gke-managed-otel --no-headers | grep -c "Running" || true)
if [ "$COLLECTOR_PODS" -ge 1 ]; then
  echo "  ✔ GKE Managed OpenTelemetry Collector pod is Running."
else
  echo "  ✖ GKE Managed OpenTelemetry Collector pod not running!"
  exit 1
fi

# -----------------------------------------------------------------------------
echo "[2/6] Testing Milestone 2: Uninstrumented GenAI Multi-Tier Application Suite..."
# -----------------------------------------------------------------------------
# Security Audit: Strict ClusterIP
LB_SVCS=$(kubectl get svc -A -o jsonpath='{.items[?(@.spec.type=="LoadBalancer")].metadata.name}')
if [ -z "$LB_SVCS" ]; then
  echo "  ✔ Security Audit: Zero external LoadBalancer IPs. All services strictly ClusterIP."
else
  echo "  ✖ Security Audit FAILED: Found LoadBalancer services: $LB_SVCS"
  exit 1
fi

# Zero-SDK Audit
for deploy in web-frontend agent-orchestrator order-mcp-server; do
  STATUS=$(kubectl exec -n "$NAMESPACE" "deploy/$deploy" -- python3 -c "
import sys
try:
    import opentelemetry
    print('FAIL')
except ImportError:
    print('PASS')
")
  if [ "$STATUS" == "PASS" ]; then
    echo "  ✔ $deploy: 0 OpenTelemetry SDK packages."
  else
    echo "  ✖ $deploy: Contains OpenTelemetry SDK packages!"
    exit 1
  fi
done

# -----------------------------------------------------------------------------
echo "[3/6] Testing Milestone 3: OBI eBPF DaemonSet Deployment..."
# -----------------------------------------------------------------------------
DESIRED=$(kubectl get daemonset -n otel-system opentelemetry-ebpf-instrumentation -o jsonpath='{.status.desiredNumberScheduled}')
READY=$(kubectl get daemonset -n otel-system opentelemetry-ebpf-instrumentation -o jsonpath='{.status.numberReady}')
if [ "$DESIRED" -eq "$READY" ] && [ "$READY" -gt 0 ]; then
  echo "  ✔ OBI DaemonSet active on all $READY/$DESIRED nodes."
else
  echo "  ✖ OBI DaemonSet not fully ready ($READY/$DESIRED)."
  exit 1
fi

# -----------------------------------------------------------------------------
echo "[4/6] Testing Milestone 4: In-Kernel Context Propagation & Distributed Traces..."
# -----------------------------------------------------------------------------
./gke_otel_obi_demo/scripts/test-context-propagation.sh

# -----------------------------------------------------------------------------
echo "[5/6] Testing Milestone 5: MCP/VectorDB Telemetry & Cloud Monitoring RED..."
# -----------------------------------------------------------------------------
./gke_otel_obi_demo/scripts/verify-milestone5.sh

# -----------------------------------------------------------------------------
echo "[6/6] Testing Milestone 6: Fault Injection & Recovery Lifecycle..."
# -----------------------------------------------------------------------------
./gke_otel_obi_demo/scripts/inject-failure.sh latency
./gke_otel_obi_demo/scripts/recover-failure.sh

echo ""
echo "================================================================================"
echo "              ALL MILESTONE VERIFICATION CHECKS PASSED (100%)                   "
echo "================================================================================"
