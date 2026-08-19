#!/usr/bin/env python3
"""
Synthetic trace sender for verifying GKE Managed OpenTelemetry ingestion and Cloud Trace export.
Sends a single OTLP trace payload over HTTP/JSON or gRPC to the in-cluster OpenTelemetry collector.
"""

import json
import os
import sys
import time
import urllib.request
import random

def generate_id(length_bytes):
    return "".join(random.choices("0123456789abcdef", k=length_bytes * 2))

def send_trace(endpoint="http://opentelemetry-collector.gke-managed-otel.svc.cluster.local:4318/v1/traces"):
    trace_id = generate_id(16)
    span_id = generate_id(8)
    now_nano = int(time.time() * 1e9)
    end_nano = now_nano + int(50 * 1e6) # 50ms duration

    payload = {
        "resourceSpans": [
            {
                "resource": {
                    "attributes": [
                        {"key": "service.name", "value": {"stringValue": "gke-managed-otel-smoke-test"}},
                        {"key": "gcp.project_id", "value": {"stringValue": os.environ.get("PROJECT_ID", "dashpole-dev")}},
                        {"key": "test.type", "value": {"stringValue": "milestone-1-verification"}}
                    ]
                },
                "scopeSpans": [
                    {
                        "scope": {"name": "milestone1.verifier", "version": "1.0.0"},
                        "spans": [
                            {
                                "traceId": trace_id,
                                "spanId": span_id,
                                "name": "Milestone-1-Managed-OTel-SmokeTest",
                                "kind": 1, # SPAN_KIND_INTERNAL
                                "startTimeUnixNano": str(now_nano),
                                "endTimeUnixNano": str(end_nano),
                                "attributes": [
                                    {"key": "test.status", "value": {"stringValue": "PASSED"}},
                                    {"key": "demo.component", "value": {"stringValue": "gke-managed-otel"}}
                                ],
                                "status": {"code": 1} # STATUS_CODE_OK
                            }
                        ]
                    }
                ]
            }
        ]
    }

    req = urllib.request.Request(
        endpoint,
        data=json.dumps(payload).encode("utf-8"),
        headers={"Content-Type": "application/json"}
    )

    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            print(f"Smoke test span successfully sent to {endpoint} (HTTP {resp.status})")
            print(f"Trace ID: {trace_id}")
            print(f"Span ID:  {span_id}")
            return trace_id
    except Exception as e:
        print(f"Failed to send span to {endpoint}: {e}", file=sys.stderr)
        sys.exit(1)

if __name__ == "__main__":
    ep = sys.argv[1] if len(sys.argv) > 1 else "http://opentelemetry-collector.gke-managed-otel.svc.cluster.local:4318/v1/traces"
    send_trace(ep)
