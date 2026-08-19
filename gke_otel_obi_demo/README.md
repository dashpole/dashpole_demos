# OpenTelemetry eBPF Instrumentation (OBI) on GKE

[![OpenTelemetry eBPF](https://img.shields.io/badge/OpenTelemetry-eBPF%20(OBI)%20v0.11.0-blue.svg)](https://github.com/open-telemetry/opentelemetry-ebpf-instrumentation)
[![GKE](https://img.shields.io/badge/GKE-Managed%20OpenTelemetry-4285F4.svg)](https://cloud.google.com/kubernetes-engine/docs/concepts/opentelemetry)
[![Zero-SDK](https://img.shields.io/badge/App%20Code-Zero%20SDKs-brightgreen.svg)](#zero-code-purity)

This repository contains the complete live demo and verification suite showcasing **OpenTelemetry eBPF Instrumentation (OBI)** running on **Google Kubernetes Engine (GKE)** with the **GKE-managed OpenTelemetry Collector**.

This demo highlights the major advancements made by the **OBI SIG** over the last 6 months:
* **Kernel-Level Context Propagation (`tpinjector`)**: Dynamic, in-flight W3C `traceparent` injection at the Linux socket layer, providing end-to-end distributed trace stitching across uninstrumented polyglot services without code changes.
* **Model Context Protocol (MCP) Observability**: Automatic extraction and decoding of JSON-RPC `tools/list` and `tools/call` executions.
* **Vector Database (Qdrant) Inspection**: Zero-code tracing of similarity search operations, vector sizes, and query paths in native compiled binaries (Rust).
* **GKE-Managed OpenTelemetry**: Turnkey telemetry ingestion using GKE's managed collector with Workload Identity exporting traces to **Google Cloud Trace** and RED metrics to **Google Cloud Monitoring** via Google Managed Service for Prometheus.

---

## Architecture

```
                          [User / Browser]
                                 │
                                 │ kubectl port-forward
                                 ▼
                     ┌───────────────────────┐
                     │     web-frontend      │ (Port 8080 - FastAPI / HTML UI)
                     └───────────┬───────────┘
                                 │ Kernel eBPF Context Injection (W3C traceparent)
                                 ▼
                     ┌───────────────────────┐
                     │   agent-orchestrator  │ (Port 8000 - FastAPI / RAG Orchestrator)
                     └───┬───────────┬───────┘
                         │           │
       Vector Search :6333        │ JSON-RPC :9000 (tools/call)
                         │           │
                         ▼           ▼
        ┌──────────────────┐       ┌──────────────────┐
        │knowledge-vectordb│       │ order-mcp-server │ (Port 9000 - MCP Server)
        │ (Qdrant - Rust)  │       └──────────────────┘
        └──────────────────┘                 │
                                             │ HTTPS / Vertex AI
                                             ▼
                                  ┌──────────────────────┐
                                  │   Vertex AI Gemini   │ (Gemini 1.5 / 2.0 Flash)
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

---

## 15-Minute Run-of-Show Presentation Script

### Act 1: The Observability Challenge with GenAI Microservices (Minutes 0:00 – 3:00)
* **Talking Points:**
  * Modern AI Agent architectures are distributed, polyglot microservice pipelines: frontend proxies, async Python orchestrators, compiled Vector Databases (Rust/C++), and Model Context Protocol (MCP) tool servers.
  * Adding manual OpenTelemetry SDK instrumentation across all these components is invasive, costly, requires redeployments, and is impossible for pre-compiled third-party binaries (like Qdrant or closed-source MCP servers).
* **Action:**
  * Run the zero-SDK code audit to prove that none of the application containers contain OpenTelemetry libraries:
    ```bash
    ./scripts/run-demo.sh
    ```

### Act 2: Deploying OBI with GKE Managed OpenTelemetry (Minutes 3:00 – 6:00)
* **Talking Points:**
  * GKE provides built-in Managed OpenTelemetry (`--managed-otel-scope=COLLECTION_AND_INSTRUMENTATION_COMPONENTS`), offering a managed, secure collector with Workload Identity IAM.
  * Deploying OBI (`otel/ebpf-instrument:v0.11.0`) as a DaemonSet instruments the entire node cluster instantly without touching application pods.
* **Action:**
  * Inspect the DaemonSet and collector in `gke-managed-otel`:
    ```bash
    kubectl get daemonset -n otel-system opentelemetry-ebpf-instrumentation
    kubectl get pods,svc -n gke-managed-otel
    ```

### Act 3: In-Kernel Context Propagation & Distributed Tracing (Minutes 6:00 – 9:00)
* **Talking Points:**
  * Historically, eBPF could only see single-hop spans without distributed context unless apps manually passed headers.
  * The OBI SIG has introduced in-kernel context injection (`tpinjector` socket probes): OBI intercepts TCP socket streams in the Linux kernel and injects W3C `traceparent` headers dynamically into outbound HTTP requests.
  * When downstream services receive the request, OBI extracts the `traceparent`, establishing a unified, multi-tier distributed trace across Python, Rust, and JSON-RPC servers!
* **Action:**
  * Send a customer query and view the unified trace waterfall in Google Cloud Trace:
    ```bash
    ./scripts/send-query.sh
    ```
  * Open **Google Cloud Trace**: [Cloud Trace Console](https://console.cloud.google.com/traces/traces?project=dashpole-dev). Show the single Trace ID connecting `web-frontend` $\rightarrow$ `agent-orchestrator` $\rightarrow$ `knowledge-vectordb` $\rightarrow$ `order-mcp-server`.

### Act 4: MCP & Vector DB Semantic Telemetry (Minutes 9:00 – 12:00)
* **Talking Points:**
  * OBI automatically extracts domain-specific semantic attributes:
    * **Model Context Protocol (MCP)**: Captures `tools/call`, tool name (`get_order_status`), order IDs, and JSON-RPC payload sizes.
    * **Vector Databases**: Traces similarity searches against `/collections/kb/points/search` in native Rust.
  * Traces include full Kubernetes metadata (`k8s.pod.name`, `k8s.node.name`, `k8s.deployment.name`).

### Act 5: Live Troubleshooting & Cloud Monitoring RED Metrics (Minutes 12:00 – 15:00)
* **Talking Points:**
  * OBI generates golden RED metrics (Rate, Errors, Duration) and network flow bytes directly in kernel space.
  * Using Google Managed Service for Prometheus (`PodMonitoring`), metrics are ingested into Google Cloud Monitoring without running separate scrapers.
* **Action:**
  * Inject downstream latency into the MCP server:
    ```bash
    ./scripts/inject-failure.sh latency
    ```
  * Show the 2.5-second bottleneck immediately pinpointed in Cloud Trace without modifying application code!
  * Recover the system:
    ```bash
    ./scripts/recover-failure.sh
    ```

---

## Directory Structure

```
gke_otel_obi_demo/
├── 01-foundation/
│   ├── 01-provision-cluster.sh           # Script for GKE cluster setup
│   └── 02-test-managed-otel.yaml         # Synthetic OTLP smoke test job
├── 02-workloads/
│   ├── 00-namespace.yaml                 # ai-agent namespace definition
│   ├── 01-rbac-workload-identity.yaml    # Workload Identity IAM binding
│   ├── 02-qdrant-vectordb.yaml           # Qdrant Vector DB Deployment & ClusterIP
│   ├── 03-order-mcp-server.yaml          # MCP JSON-RPC Server Deployment & ClusterIP
│   ├── 04-agent-orchestrator.yaml        # FastAPI Orchestrator Deployment & ClusterIP
│   └── 05-web-frontend.yaml              # HTML/JS Web Frontend & Query Proxy
├── 03-obi/
│   ├── 01-obi-rbac.yaml                  # ServiceAccount & ClusterRole for OBI
│   ├── 02-obi-configmap.yaml             # OBI Config v2 with eBPF context propagation
│   ├── 03-obi-daemonset.yaml             # otel/ebpf-instrument DaemonSet
│   └── 04-obi-podmonitoring.yaml         # Google Managed Prometheus PodMonitoring
├── scripts/
│   ├── port-forward-frontend.sh          # Port-forward web UI to localhost:8080
│   ├── send-query.sh                     # Submit queries to the AI stack
│   ├── prewarm-demo.sh                   # Pre-populate DB and warm connections
│   ├── inject-failure.sh                 # Fault injection helper (latency / error)
│   ├── recover-failure.sh                # Restore healthy baseline
│   ├── test-context-propagation.sh       # Verify in-kernel W3C trace stitching
│   ├── verify-milestone5.sh              # Verify RED metrics & telemetry
│   ├── verify-all.sh                     # Master regression test harness
│   └── run-demo.sh                       # Interactive 15-minute presentation runner
└── README.md                             # Comprehensive demo guide
```

---

## Quickstart Guide

### 1. Prerequisites
* Google Cloud Project with Billing enabled (`dashpole-dev`).
* `gcloud` CLI authenticated with Kubernetes Engine Admin permissions.
* `kubectl` installed.

### 2. Deploy the Stack
```bash
# 1. Apply multi-tier workloads
kubectl apply -f 02-workloads/

# 2. Deploy OBI DaemonSet and Monitoring
kubectl apply -f 03-obi/

# 3. Pre-warm databases and cache
./scripts/prewarm-demo.sh
```

### 3. Run Master Verification
```bash
./scripts/verify-all.sh
```

### 4. Interactive Live Demo
```bash
./scripts/run-demo.sh
```

### 5. Access the Web Frontend
For security compliance, all services are strictly configured as `type: ClusterIP`. Access the web frontend via:
```bash
./scripts/port-forward-frontend.sh
```
Open `http://localhost:8080` in your web browser.

---

## Security Audit

* **External IP Enforcement**: Zero `LoadBalancer` services or public IPs are provisioned in this cluster.
* **Workload Identity**: In-cluster ServiceAccounts are mapped to Google Cloud IAM roles (`roles/aiplatform.user`) without static credential files.
* **eBPF Capabilities**: OBI is deployed with isolated BPF privileges (`SYS_PTRACE`, `SYS_ADMIN`, `BPF`) strictly scoped to the `otel-system` namespace.
