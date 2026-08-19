# OpenTelemetry eBPF Instrumentation (OBI) for gVisor Agentic Workloads on GKE

**Author:** dashpole  
**Repository:** `https://github.com/dashpole/dashpole_demos`  
**Branch:** `gvisor-obi-prototype`  
**Date:** August 2026  

---

## 1. Executive Overview & Problem Statement

Modern agentic AI workloads (LangChain, AutoGen, LlamaIndex, multi-agent frameworks) executing unvetted code, calling dynamic APIs, and operating in multi-tenant environments require strict sandboxing. On Google Cloud and GKE, **gVisor (`runsc`)** provides secure, multi-tenant isolation with a user-space kernel (Sentry).

However, traditional **eBPF-based instrumentation (OpenTelemetry eBPF Instrumentation / OBI)** attaches probes directly to host Linux kernel syscalls (`sys_enter_write`, `sys_enter_read`, `tcp_sendmsg`, `uprobes` in host address space). When a workload runs inside gVisor:
1. **Syscall Virtualization:** Guest syscalls are trapped and handled entirely inside gVisor's Sentry kernel in user space; host Linux kernel syscall probes never fire.
2. **Encrypted Network Payloads:** Application TLS traffic leaves the sandbox as encrypted ciphertext. Host eBPF probes cannot inspect HTTP/2 frames, Server-Sent Events (SSE) streaming tokens, Model Context Protocol (MCP) JSON-RPC messages, or Vector DB embeddings.
3. **Trace Disconnect:** Distributed context propagation across agent microservices running in sandboxes breaks without coordinated header injection.

### The Solution: Shared Memory Ring Buffer + In-Sentry kTLS Telemetry Bridge

This prototype implements a high-performance, non-blocking telemetry bridge between gVisor Sentry and OBI on the host:
- **Zero-Copy IPC Transport:** Mimics Linux `BPF_MAP_TYPE_RINGBUF` using double-mapped virtual pages for zero-copy wraparound ring buffers over `/run/gvisor-ringbuf/<container_id>.shm`.
- **In-Sentry Kernel TLS (kTLS):** Sentry emulates Linux kTLS UAPI (`TCP_ULP="tls"`), intercepting application plaintext before AES-GCM encryption on TX and after decryption on RX.
- **Dynamic Pod Discovery:** OBI automatically detects new sandbox ringbuffers via `inotify` and enriches spans with Kubernetes pod metadata (`k8s.pod.name`, `k8s.container.name`).
- **GenAI L7 Decoders:** Decodes HTTP/2 & gRPC streams, SSE token streams with sub-millisecond Time-to-First-Token (TTFT), Model Context Protocol (MCP) tool calls, and Qdrant Vector DB queries.
- **In-Sentry Distributed Context Propagation:** Enforces strict W3C TraceContext standards and automatically links multi-hop agent pipelines into unified distributed trace DAGs.

---

## 2. Architectural Blueprint

```
+---------------------------------------------------------------------------------------+
|  GKE NODE                                                                             |
|                                                                                       |
|  +-------------------------------------+     +-------------------------------------+  |
|  | Pod: Frontend UI (gVisor)           |     | Pod: Agent Orchestrator (gVisor)    |  |
|  |   +-------------------------------+ |     |   +-------------------------------+ |  |
|  |   | Python / FastAPI Workload     | |     |   | Multi-Agent Orchestrator      | |  |
|  |   +---------------+---------------+ |     |   +---------------+---------------+ |  |
|  |                   | Plaintext       |     |                   | Plaintext       |  |
|  |   +---------------v---------------+ |     |   +---------------v---------------+ |  |
|  |   | Sentry Kernel TLS (kTLS)      | |     |   | Sentry Kernel TLS (kTLS)      | |  |
|  |   | - W3C In-Flight Injection     | |     |   | - W3C In-Flight Injection     | |  |
|  |   | - Plaintext Telemetry Tap     | |     |   | - Plaintext Telemetry Tap     | |  |
|  |   +---------------+---------------+ |     |   +---------------+---------------+ |  |
|  +-------------------|-----------------+     +-------------------|-----------------+  |
|                      |                                           |                    |
|                      v Ringbuf SHM File                          v Ringbuf SHM File   |
|         /run/gvisor-ringbuf/frontend.shm           /run/gvisor-ringbuf/orchestrator.shm|
|                      \                                           /                    |
|                       \                                         /                     |
|  +---------------------v---------------------------------------v-------------------+  |
|  | OpenTelemetry eBPF Instrumentation (OBI) DaemonSet (Host)                       |  |
|  |                                                                                 |  |
|  |  +---------------------------------------------------------------------------+  |  |
|  |  | Dynamic Pod Discovery Manager (inotify & polling)                          |  |  |
|  |  +-------------------------------------+-------------------------------------+  |  |
|  |                                        |                                        |  |
|  |  +-------------------------------------v-------------------------------------+  |  |
|  |  | RingBuffer Consumer (Atomic busy-bit protocol, lockless double-mapped)    |  |  |
|  |  +-------------------------------------+-------------------------------------+  |  |
|  |                                        |                                        |  |
|  |  +-------------------------------------v-------------------------------------+  |  |
|  |  | OBI Pipeline Bridge & L7 GenAI Decoders                                   |  |  |
|  |  |  - HTTP/2 & gRPC Multiplexed Demuxer                                       |  |  |
|  |  |  - SSE LLM Streaming TTFT & Token Accounting                               |  |  |
|  |  |  - Model Context Protocol (MCP) Tool Calls                                 |  |  |
|  |  |  - Qdrant Vector DB Similarity Search                                     |  |  |
|  |  |  - K8s Resource Metadata Decoration                                       |  |  |
|  |  +-------------------------------------+-------------------------------------+  |  |
|  |                                        |                                        |  |
|  |  +-------------------------------------v-------------------------------------+  |  |
|  |  | OTLP Span Exporter -> OpenTelemetry Collector / Google Cloud Trace         |  |  |
|  |  +---------------------------------------------------------------------------+  |  |
|  +---------------------------------------------------------------------------------+  |
+---------------------------------------------------------------------------------------+
```

---

## 3. Milestones Implemented & Tested

| Milestone | Subsystem | Key Components & Test Evidence |
| :--- | :--- | :--- |
| **M1** | **Shared Memory Ring Buffer IPC** | BPF-compatible memory layout, double-mapped wraparound, atomic reservation, non-blocking backpressure. Benchmark: **1.34M events/sec, 174 MB/s bandwidth, 0 byte loss**. |
| **M2** | **Sentry kTLS Subsystem & Tap** | Linux kTLS ABI (`TCP_ULP="tls"`), AES-128/256-GCM AEAD, TLS 1.2/1.3 AAD & Nonce derivation, corrupted tag rejection (`EBADMSG`), 100% byte fidelity tap. |
| **M3** | **End-to-End Ingestion Bridge** | `PodDiscoveryManager` (inotify), container 5-tuple correlation, K8s metadata decoration, $O(1)$ in-flight table with background TTL cleanup (**136k ops/sec**). |
| **M4** | **L7 & GenAI Semantic Decoders** | HTTP/2 HPACK demuxer, SSE streaming TTFT and token accounting, MCP JSON-RPC 2.0 parser, Qdrant Vector DB search metrics (`score_threshold`, `top_k`, `dim`). |
| **M5** | **In-Sentry Context Propagation** | Strict W3C TraceContext parser (prohibits `0xff`, all-zero IDs), in-flight header injection on HTTP/1.1 & HTTP/2, verified 3-sandbox distributed trace DAG. |
| **M6** | **GKE Packaging & Verification Demo** | GKE manifests (`RuntimeClass`, `DaemonSet`, multi-tier app), automated `demo-runner` CLI, and verification test script. |

---

## 4. Running the Verification Suite

### 4.1 Automated One-Command Verification
Run the unified verification script:
```bash
./scripts/verify_demo.sh
```

### 4.2 Running the Interactive Demo Runner
Execute the multi-tier agent demo showing live trace waterfall creation:
```bash
go run ./cmd/demo-runner/main.go
```

**Output Trace Waterfall:**
```text
Root Trace ID: 4583ef91654ff876cad8aaa9da2ff961

└─ [1] Span: POST /api/chat
     Pod:           frontend-ui-6f7c9b (web)
     TraceID:       4583ef91654ff876cad8aaa9da2ff961
     SpanID:        bfbaab2baf3e9d82 | ParentSpanID: 7e63dabdd0f73326
     Duration:      10.2ms | HTTP: 200

  └─ [2] Span: POST /v1/orchestrate
       Pod:           agent-orchestrator-8d9e (orchestrator)
       TraceID:       4583ef91654ff876cad8aaa9da2ff961
       SpanID:        45d38b5095f0284f | ParentSpanID: bfbaab2baf3e9d82
       Duration:      15.1ms | HTTP: 200

    └─ [3] Span: POST /collections/customer_portfolios/points/search
         Pod:           qdrant-vector-db-0 (qdrant)
         TraceID:       4583ef91654ff876cad8aaa9da2ff961
         SpanID:        bb2d3445456591d6 | ParentSpanID: 45d38b5095f0284f
         Duration:      10.1ms | HTTP: 200
         Vector DB:     system=qdrant, collection=customer_portfolios, dim=1536, top_k=5, score_threshold=0.92

    └─ [4] Span: POST /mcp
         Pod:           mcp-sqlite-server-4b2a (mcp-server)
         TraceID:       4583ef91654ff876cad8aaa9da2ff961
         SpanID:        e5552b79b6a5a975 | ParentSpanID: 45d38b5095f0284f
         Duration:      12.4ms | HTTP: 200
         MCP Tool:      name=sql_query_portfolio, args={"customer_id":101,"include_dividends":true}
```

### 4.3 Running Throughput Benchmarks
```bash
go run ./cmd/ringbuf-bench/main.go -producers 16 -events 8000000
```
*Result: ~1.34 Million events/sec with 0 byte loss and zero lock contention.*

---

## 5. Deploying to GKE

1. **Apply RuntimeClass:**
   ```bash
   kubectl apply -f k8s/00-runtimeclass.yaml
   ```

2. **Deploy OBI DaemonSet:**
   ```bash
   kubectl apply -f k8s/01-obi-daemonset.yaml
   ```

3. **Deploy Multi-Tier Agent Workload:**
   ```bash
   kubectl apply -f k8s/02-agent-app.yaml
   ```

---

## 6. Comparison of Approaches

| Criteria | Option A: Native Host eBPF Only | Option B: Reimplement OTel in gVisor Kernel | Option C: Shared Ringbuf + kTLS Bridge (Our Approach) |
| :--- | :--- | :--- | :--- |
| **Visibility in Sandboxes** | ❌ **0%** (Host syscalls never fire) | ✅ 100% | ✅ **100%** |
| **HTTPS Plaintext Visibility** | ❌ 0% (Sees only TLS ciphertext) | ✅ 100% | ✅ **100% (via kTLS emulation)** |
| **GenAI Decoders (SSE, MCP, Vector)** | ❌ None | ❌ Heavy maintenance burden | ✅ **Full OTel GenAI Semantics** |
| **Engineering Effort** | Low (doesn't work) | ❌ 1-2 Years SWE time | ✅ **~2-4 Weeks Prototype** |
| **Overhead & Memory Safety** | Zero kernel overhead | Medium (Go GC inside kernel) | ✅ **Zero-copy, lockless, non-blocking** |
