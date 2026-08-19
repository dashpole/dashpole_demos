# OpenTelemetry eBPF Instrumentation (OBI) on GKE

This directory contains the manifests, scripts, and verification suite for running **OpenTelemetry eBPF Instrumentation (OBI)** on **Google Kubernetes Engine (GKE)** with **GKE Managed OpenTelemetry**.

## Architecture Overview

```
                          [User / Browser]
                                 │ (kubectl port-forward)
                                 ▼
                     ┌───────────────────────┐
                     │     web-frontend      │ (Port 8080 - ClusterIP)
                     └───────────┬───────────┘
                                 │ Kernel eBPF Context Injection (W3C traceparent)
                                 ▼
                     ┌───────────────────────┐
                     │   agent-orchestrator  │ (Port 8000 - Python asyncio / FastAPI)
                     └───┬───────────┬───────┘
                         │           │
       Vector Search (Cosine)        │ JSON-RPC (tools/call)
                         │           │
                         ▼           ▼
        ┌──────────────────┐       ┌──────────────────┐
        │knowledge-vectordb│       │ order-mcp-server │ (Port 9000 - Model Context Protocol)
        │ (Qdrant :6333)   │       └──────────────────┘
        └──────────────────┘                 │
                                             │ HTTPS / SSE
                                             ▼
                                  ┌──────────────────────┐
                                  │   Vertex AI Gemini   │ (Gemini 2.0 Flash)
                                  └──────────────────────┘

  ════════════════════════ Node Observability Layer ════════════════════════
    [OBI DaemonSet (otel/ebpf-instrument)] ────► [GKE Managed OTel Collector]
        (Socket / Kprobe / TC)                     (gke-managed-otel :4317/:4318)
                                                               │
                                                               ▼
                                                  [Google Cloud Observability]
                                                  - Cloud Trace Explorer
                                                  - Cloud Monitoring RED Metrics
```

## Key Capabilities Highlighted
1. **Zero-Code Instrumentation**: Application containers contain zero OpenTelemetry SDK dependencies or manual spans.
2. **In-Kernel Context Propagation (`tpinjector`)**: W3C `traceparent` headers are injected into network/socket stream buffers at the kernel level across process boundaries, stitching multi-tier services into a single trace waterfall.
3. **Model Context Protocol (MCP) Observability**: Automatic extraction and decoding of JSON-RPC `tools/list` and `tools/call` spans.
4. **Vector Database RAG Inspection**: Automatic extraction of similarity search operations and `top_k` query parameters.
5. **GKE Managed OpenTelemetry**: Seamless out-of-the-box collector pipeline managed by GKE with Workload Identity exporting to Cloud Trace and Cloud Monitoring.

## Directory Structure
* `01-foundation/`: Cluster provisioning script and synthetic OTLP smoke tests.
* `02-workloads/`: Uninstrumented multi-tier GenAI application suite manifests.
* `03-obi/`: OBI RBAC, ConfigMap (Config v2), and DaemonSet manifests.
* `scripts/`: Port-forwarding helpers, pre-warm queries, and verification scripts.

## Security Note
All Kubernetes services in this demo are configured as `type: ClusterIP`. No public/external LoadBalancer IPs are created. Access to the web interface is established using:
```bash
kubectl port-forward -n ai-agent svc/web-frontend 8080:8080
```
