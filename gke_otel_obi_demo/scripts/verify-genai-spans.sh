#!/usr/bin/env bash
# ==============================================================================
# verify-genai-spans.sh
# Formally verifies OpenTelemetry GenAI Semantic Convention attributes in Cloud Trace
# ==============================================================================

set -euo pipefail

PROJECT_ID="${GCP_PROJECT:-dashpole-dev}"
NAMESPACE="ai-agent"

echo "================================================================================"
echo "    GenAI Semantic Convention Attribute Verification in Google Cloud Trace     "
echo "================================================================================"
echo "Project: $PROJECT_ID | Namespace: $NAMESPACE"
echo "Timestamp: $(date -u)"
echo ""

# 1. Dispatch query through web-frontend inside the cluster
echo "▶ [1/3] Dispatching end-to-end customer query through web-frontend..."
QUERY_OUTPUT=$(kubectl exec -n "$NAMESPACE" deploy/web-frontend -- python3 -c '
import urllib.request, json
req_data = json.dumps({"prompt": "What is the return policy for electronics, and can you check order #1042?"}).encode("utf-8")
req = urllib.request.Request("http://127.0.0.1:8080/api/query", data=req_data, headers={"Content-Type": "application/json"})
with urllib.request.urlopen(req, timeout=30) as resp:
    print(resp.read().decode("utf-8"))
')

TRACE_ID=$(echo "$QUERY_OUTPUT" | python3 -c 'import sys, json; print(json.load(sys.stdin).get("trace_id", ""))')

if [ -z "$TRACE_ID" ]; then
  echo "✖ Failed to obtain Trace ID from web-frontend response!"
  echo "Response: $QUERY_OUTPUT"
  exit 1
fi

echo "  ✔ Query succeeded!"
echo "  ✔ Captured Trace ID: $TRACE_ID"
echo "  ✔ Cloud Trace URL: https://console.cloud.google.com/traces/viewer?project=${PROJECT_ID}&tid=${TRACE_ID}"
echo ""

# 2. Wait for OBI eBPF and Managed Collector export to Google Cloud Trace
echo "▶ [2/3] Waiting for trace export to Google Cloud Trace backend..."
sleep 6

# 3. Query Cloud Trace API and verify semantic convention attributes
echo "▶ [3/3] Fetching trace details from Google Cloud Trace API and asserting attributes..."
python3 - <<EOF
import urllib.request
import json
import subprocess
import sys
import time

project_id = "${PROJECT_ID}"
trace_id = "${TRACE_ID}"
token = subprocess.check_output(["gcloud", "auth", "application-default", "print-access-token"]).decode().strip()

url = f"https://cloudtrace.googleapis.com/v1/projects/{project_id}/traces/{trace_id}"
req = urllib.request.Request(url, headers={"Authorization": f"Bearer {token}"})

trace_data = None
spans = []

# Poll until all distributed spans (client + server hops >= 18) arrive in Cloud Trace
for attempt in range(12):
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            trace_data = json.loads(r.read().decode())
            spans = trace_data.get("spans", [])
            has_gemini = any("generate_content" in s.get("name", "") for s in spans)
            has_mcp = any("execute_tool" in s.get("name", "") for s in spans)
            has_retrieval = any("retrieval" in s.get("name", "") for s in spans)
            if has_gemini and has_mcp and has_retrieval and len(spans) >= 18:
                break
    except Exception:
        pass
    time.sleep(3)

if not spans:
    print(f"✖ Could not fetch trace {trace_id} from Cloud Trace API!")
    sys.exit(1)

print(f"  ✔ Retrieved distributed trace ({len(spans)} total spans in DAG)\n")

# Assertions Tracker
checks = {
    "model_span_name": False,
    "model_operation": False,
    "model_provider": False,
    "model_request_model": False,
    "model_response_model": False,
    "model_usage_input": False,
    "model_usage_output": False,
    "model_finish_reasons": False,
    "mcp_execute_tool_span": False,
    "mcp_tool_name": False,
    "mcp_method_name": False,
    "mcp_session_id": False,
    "mcp_tools_list_span": False,
    "retrieval_span": False,
    "retrieval_operation": False,
    "retrieval_top_k": False,
}

print(f"{'SPAN NAME':<40} | {'SPAN KIND':<12} | {'KEY GENAI / MCP ATTRIBUTES'}")
print("-" * 110)

for s in spans:
    name = s.get("name", "")
    kind = s.get("kind", "INTERNAL")
    labels = s.get("labels", {})

    gen_ai_attrs = []
    for k, v in sorted(labels.items()):
        if any(p in k for p in ["gen_ai", "mcp", "rpc", "jsonrpc"]):
            gen_ai_attrs.append(f"{k}={v}")

    attr_summary = ", ".join(gen_ai_attrs[:3])
    if len(gen_ai_attrs) > 3:
        attr_summary += f" (+{len(gen_ai_attrs)-3} more)"
    print(f"{name:<40} | {kind:<12} | {attr_summary}")

    # Check Model Span
    if "generate_content" in name:
        checks["model_span_name"] = True
    if labels.get("gen_ai.operation.name") == "generate_content":
        checks["model_operation"] = True
    if "gemini" in labels.get("gen_ai.provider.name", ""):
        checks["model_provider"] = True
    if "gemini" in labels.get("gen_ai.request.model", ""):
        checks["model_request_model"] = True
    if "gemini" in labels.get("gen_ai.response.model", ""):
        checks["model_response_model"] = True
    if "gen_ai.usage.input_tokens" in labels:
        checks["model_usage_input"] = True
    if "gen_ai.usage.output_tokens" in labels:
        checks["model_usage_output"] = True
    if "gen_ai.response.finish_reasons" in labels:
        checks["model_finish_reasons"] = True

    # Check MCP Tool Execution
    if "execute_tool" in name or "get_order_status" in name:
        checks["mcp_execute_tool_span"] = True
    if labels.get("gen_ai.tool.name") == "get_order_status":
        checks["mcp_tool_name"] = True
    if labels.get("mcp.method.name") == "tools/call":
        checks["mcp_method_name"] = True
    if "mcp-session" in labels.get("mcp.session.id", ""):
        checks["mcp_session_id"] = True

    # Check MCP Tools List
    if "tools/list" in name or "tools_list" in name:
        checks["mcp_tools_list_span"] = True

    # Check Vector DB Retrieval
    if "retrieval" in name:
        checks["retrieval_span"] = True
    if labels.get("gen_ai.operation.name") == "retrieval":
        checks["retrieval_operation"] = True
    if "gen_ai.retrieval.top_k" in labels:
        checks["retrieval_top_k"] = True

print("-" * 110)
print("\n▶ Formal Semantic Convention Assertion Summary:")

all_passed = True
for check_name, passed in checks.items():
    icon = "✔" if passed else "✖"
    status_str = "PASSED" if passed else "FAILED"
    print(f"  {icon} {check_name:<28}: {status_str}")
    if not passed:
        all_passed = False

if all_passed:
    print("\n🎉 ALL OpenTelemetry GenAI Semantic Convention assertions PASSED successfully!")
    sys.exit(0)
else:
    print("\n✖ Some OpenTelemetry GenAI assertions FAILED.")
    sys.exit(1)
EOF

echo ""
echo "================================================================================"
echo "    GenAI Trace Attribute Verification COMPLETE                                 "
echo "================================================================================"
