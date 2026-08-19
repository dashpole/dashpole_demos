# Implementation Plan: gVisor & OBI Prototype for Agentic/GenAI Observability

**Author:** AI Systems Engineering & Observability SIG  
**Target Platform:** gVisor (`runsc`/`sentry`) on Google Kubernetes Engine (GKE)  
**Ingestion Subsystem:** OpenTelemetry eBPF Instrumentation (OBI) (`otel/ebpf-instrument`)  
**Target Workloads:** Multi-tier Polyglot GenAI Agentic Pipelines (FastAPI, Qdrant Vector DB, MCP Servers, Vertex AI / Gemini / OpenAI)

---

## 1. Executive Summary & Architectural Blueprint

### 1.1 The Core Challenge
In modern AI Agent architectures on Kubernetes, workloads are composed of distributed, polyglot microservices: Python orchestrators, native compiled vector databases (e.g., Qdrant in Rust), and Model Context Protocol (MCP) tool execution engines. When deployed in secure sandboxed runtimes like **gVisor** (`runsc`), traditional host-level eBPF probes (`kprobe`, `tracepoint`, `tc`, `uprobe`) are ineffective:
1. **Syscall Virtualization:** gVisor Sentry intercepts application system calls in user space; host kernel syscall tracepoints never fire for container actions.
2. **User-Space Networking Stack:** Netstack handles TCP/IP inside Sentry; host network interfaces only see encapsulated/tunneled wire traffic.
3. **Payload Encryption:** Outbound traffic to LLM endpoints (e.g., Vertex AI Gemini) and internal microservices is encrypted via TLS within the application container before reaching host networking.

### 1.2 The Solution Architecture
This prototype establishes a high-performance, zero-code observability bridge between **gVisor** and **OBI**:
1. **Kernel TLS (kTLS) in Sentry:** Sentry implements the Linux kTLS ABI (`SOL_TCP`/`TCP_ULP="tls"`, `SOL_TLS`/`TLS_TX`/`TLS_RX`), offloading TLS symmetric encryption/decryption into Sentry. Sentry intercepts the plaintext payload **before encryption on TX** and **after decryption on RX**.
2. **Lockless Shared-Memory Ring Buffer:** A zero-copy circular ring buffer modeled on the Linux `BPF_MAP_TYPE_RINGBUF` binary layout connects gVisor Sentry (producer) and the OBI host daemon (consumer).
3. **OBI Ingestion & Agentic Decoding Engine:** OBI consumes raw ring buffer records, reuses its existing L7/GenAI stream decoders (HTTP/1.1, HTTP/2 HPACK, SSE token streaming, MCP JSON-RPC, Vector DB inspection), and exports standards-compliant OpenTelemetry traces and RED metrics to the GKE-managed OTel Collector.
4. **Kernel-Level Context Propagation:** Sentry injects W3C `traceparent` headers into outbound HTTP/HTTPS streams during TLS record framing, maintaining end-to-end distributed trace stitching across sandbox hops.

```
┌──────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│                                             GKE WORKER NODE                                              │
│                                                                                                          │
│  ┌────────────────────────────────────────────────────────────────────────────────────────────────────┐  │
│  │ GVISOR SANDBOX (runsc / sentry)                                                                    │  │
│  │                                                                                                    │  │
│  │   ┌──────────────────────────────────────────────────────────────────────────────────────────────┐  │  │
│  │   │ Sandbox Application Container (FastAPI Orchestrator / MCP Server / Qdrant)                    │  │  │
│  │   │ - ZERO OpenTelemetry SDKs / Zero code modifications                                          │  │  │
│  │   │ - Uses standard OpenSSL / BoringSSL / Python ssl with kTLS enabled (TCP_ULP = "tls")          │  │  │
│  │   │ - write(fd, plaintext) ───────┐                   ┌─────── read(fd, plaintext)               │  │  │
│  │   └───────────────────────────────┼───────────────────┼──────────────────────────────────────────┘  │  │
│  │                                   │                   │                                             │  │
│  │   ┌───────────────────────────────┼───────────────────┼──────────────────────────────────────────┐  │  │
│  │   │ SENTRY KERNEL SPACE           ▼                   ▲                                          │  │  │
│  │   │                                                                                              │  │  │
│  │   │  ┌─────────────────────────────────────────────────────────────────────────────────────────┐ │  │  │
│  │   │  │ kTLS Engine (Netstack / Sentry ULP Layer)                                               │ │  │  │
│  │   │  │   1. Capture Plaintext on TX ───► [ Tracepoint Hook ] ───► In-Flight W3C Injection      │ │  │  │
│  │   │  │   2. AES-128/256-GCM Encryption ──► Framing TLS Records (Type 23)                       │ │  │  │
│  │   │  │   3. Netstack TCP Transmission to Host Network                                          │ │  │  │
│  │   │  │   4. Netstack TCP Reception ──► AES-128/256-GCM Decryption                               │ │  │  │
│  │   │  │   5. Capture Plaintext on RX ───► [ Tracepoint Hook ] ───► Return to App                │ │  │  │
│  │   │  └─────────────────────────────────┬───────────────────────────────────────────────────────┘ │  │  │
│  │   │                                    │ (Non-blocking emit)                                     │  │  │
│  │   │                                    ▼                                                         │  │  │
│  │   │  ┌─────────────────────────────────────────────────────────────────────────────────────────┐ │  │  │
│  │   │  │ seccheck RingBuf Sink (pkg/sentry/seccheck/sinks/ringbuf)                                │ │  │  │
│  │   │  │   - Lockless atomic reservations (producer_pos)                                         │ │  │  │
│  │   │  │   - 8-Byte Aligned Record Header (len | BPF_RINGBUF_BUSY_BIT)                           │ │  │  │
│  │   │  │   - EventFD threshold notification                                                      │ │  │  │
│  │   │  └─────────────────────────────────┬───────────────────────────────────────────────────────┘ │  │  │
│  │   └────────────────────────────────────┼─────────────────────────────────────────────────────────┘  │  │
│  └────────────────────────────────────────┼────────────────────────────────────────────────────────────┘  │
│                                           │                                                               │
│       ════════════════════════════════════╪═══════════════════════════════════════════════════════        │
│       IPC BOUNDARY: Shared-Memory mmap Ring Buffer (`memfd_create` + `eventfd`)                           │
│       - Page 0 (4KB): consumer_pos (RW: OBI, RO: Sentry)                                                  │
│       - Page 1 (4KB): producer_pos (RW: Sentry, RO: OBI)                                                  │
│       - Pages 2..N: Data Ring (Double Virtual Mmap for Zero-Copy Wraparound)                              │
│       ════════════════════════════════════╪═══════════════════════════════════════════════════════        │
│                                           │                                                               │
│  ┌────────────────────────────────────────┼────────────────────────────────────────────────────────────┐  │
│  │ HOST OBSERVALITY LAYER (DaemonSet)     ▼                                                            │  │
│  │                                                                                                    │  │
│  │   ┌──────────────────────────────────────────────────────────────────────────────────────────────┐  │  │
│  │   │ OBI Daemon (otel/ebpf-instrument:v0.11.0-gvisor)                                             │  │  │
│  │   │                                                                                              │  │  │
│  │   │   ┌────────────────────────────────────────────────────────────────────────────────────────┐ │  │  │
│  │   │   │ gVisor RingBuf Ingestion Source (implements ebpfcommon.ringBufReader)                  │ │  │  │
│  │   │   │   - Discovers Pod ringbufs via CRI inotify watcher                                     │ │  │  │
│  │   │   │   - Polls eventfd with epoll_wait + timeout batching                                   │ │  │  │
│  │   │   │   - Zero-copy record decoding & atomic consumer_pos updates                            │ │  │  │
│  │   │   └───────────────────────────────┬────────────────────────────────────────────────────────┘ │  │  │
│  │   │                                   │ Raw Trace Records                                        │  │  │
│  │   │                                   ▼                                                          │  │  │
│  │   │   ┌────────────────────────────────────────────────────────────────────────────────────────┐ │  │  │
│  │   │   │ OBI Stream & Protocol Decoders                                                         │ │  │  │
│  │   │   │   - HTTP/1.1 & HTTP/2 Framer + HPACK Decompression                                     │ │  │  │
│  │   │   │   - SSE (Server-Sent Events) Stream Tracker (TTFT & Token Velocity)                    │ │  │  │
│  │   │   │   - Model Context Protocol (MCP) JSON-RPC 2.0 Parser (tools/call)                     │ │  │  │
│  │   │   │   - Vector DB Inspector (Qdrant /collections/points/search)                            │ │  │  │
│  │   │   │   - Kubernetes Metadata Decorator (Pod, Namespace, Container UID)                      │ │  │  │
│  │   │   └───────────────────────────────┬────────────────────────────────────────────────────────┘ │  │  │
│  │   │                                   │ OTel Spans & RED Metrics                                 │  │  │
│  │   └───────────────────────────────────┼──────────────────────────────────────────────────────────┘  │  │
│  └───────────────────────────────────────┼─────────────────────────────────────────────────────────────┘  │
│                                          │ OTLP / gRPC (Port 4317)                                        │
│                                          ▼                                                                │
│  ┌─────────────────────────────────────────────────────────────────────────────────────────────────────┐  │
│  │ GKE-Managed OpenTelemetry Collector (`gke-managed-otel`)                                            │  │
│  │   - Export to Google Cloud Trace (Waterfall Spans with MCP & GenAI Semantic Attributes)             │  │
│  │   - Export to Google Cloud Monitoring (Google Managed Service for Prometheus RED Metrics)           │  │
│  └─────────────────────────────────────────────────────────────────────────────────────────────────────┘  │
└──────────────────────────────────────────────────────────────────────────────────────────────────────────┘
```

---

## 2. Technical Design & Subsystem Specifications

### 2.1 Subsystem A: Sentry Kernel TLS (kTLS) Engine
**Goal:** Enable applications inside gVisor to use standard Linux kTLS syscall semantics, executing cryptographic operations in Sentry and exposing plaintext payload tap points.

#### 1. ABI Constants & Structures (`pkg/abi/linux/`)
Add the following definitions to [`pkg/abi/linux/socket.go`](file:///google/src/cloud/dashpole/obi_gvisor_agentic_research/google3/third_party/gvisor/pkg/abi/linux/socket.go) and create [`pkg/abi/linux/tls.go`](file:///google/src/cloud/dashpole/obi_gvisor_agentic_research/google3/third_party/gvisor/pkg/abi/linux/tls.go):

```go
package linux

const (
    SOL_TLS = 282

    // TLS Socket Options
    TLS_TX             = 1
    TLS_RX             = 2
    TLS_TX_ZEROCOPY_RO = 3

    // TLS Versions
    TLS_1_2_VERSION_MAJOR = 0x3
    TLS_1_2_VERSION_MINOR = 0x3
    TLS_1_2_VERSION       = (TLS_1_2_VERSION_MAJOR << 8) | TLS_1_2_VERSION_MINOR // 0x0303

    TLS_1_3_VERSION_MAJOR = 0x3
    TLS_1_3_VERSION_MINOR = 0x4
    TLS_1_3_VERSION       = (TLS_1_3_VERSION_MAJOR << 8) | TLS_1_3_VERSION_MINOR // 0x0304

    // Supported Cipher Suites
    TLS_CIPHER_AES_GCM_128        = 51
    TLS_CIPHER_AES_GCM_256        = 52
    TLS_CIPHER_AES_CCM_128        = 53
    TLS_CIPHER_CHACHA20_POLY1305  = 54

    // CMSG Control Record Types
    TLS_SET_RECORD_TYPE = 1
    TLS_GET_RECORD_TYPE = 2
)

// Crypto Info Structs matching Linux UAPI (struct tls_crypto_info)
type TLSCryptoInfo struct {
    Version    uint16
    CipherType uint16
}

type TLS12CryptoInfoAESGCM128 struct {
    Info   TLSCryptoInfo
    IV     [8]byte
    Key    [16]byte
    Salt   [4]byte
    RecSeq [8]byte
}

type TLS12CryptoInfoAESGCM256 struct {
    Info   TLSCryptoInfo
    IV     [8]byte
    Key    [32]byte
    Salt   [4]byte
    RecSeq [8]byte
}

type TLS13CryptoInfoAESGCM128 struct {
    Info   TLSCryptoInfo
    IV     [12]byte
    Key    [16]byte
    Salt   [0]byte
    RecSeq [8]byte
}

type TLS13CryptoInfoAESGCM256 struct {
    Info   TLSCryptoInfo
    IV     [12]byte
    Key    [32]byte
    Salt   [0]byte
    RecSeq [8]byte
}
```

#### 2. Socket Layer State Machine (`pkg/sentry/socket/netstack/`)
Extend [`pkg/sentry/socket/netstack/netstack.go`](file:///google/src/cloud/dashpole/obi_gvisor_agentic_research/google3/third_party/gvisor/pkg/sentry/socket/netstack/netstack.go):
* **`TCP_ULP` Handling:** In `setSockOptTCP`, intercept `linux.TCP_ULP`. If `optVal == "tls"`, verify TCP connection state is `ESTABLISHED`. Transition socket state from standard TCP to `TLSEndpoint` wrapper.
* **`SOL_TLS` Handling:** Implement `setSockOptTLS(t *kernel.Task, ep commonEndpoint, name int, optVal []byte) *syserr.Error`:
  * `TLS_TX`: Unpack crypto struct, initialize transmit AEAD cipher state (`crypto/cipher.AEAD` via `aes.NewCipher` + `cipher.NewGCMWithNonceSize`), set initial sequence number.
  * `TLS_RX`: Unpack crypto struct, initialize receive AEAD cipher state, set initial receive sequence number.

#### 3. Cryptographic Data Path & Interception Hooks
* **Transmit (`Write` / `SendMsg`):**
  1. Plaintext buffer passed from application.
  2. **Interception Hook:** Trigger `seccheck.PointKTLSTx` with plaintext slice, connection tuple, PID/TID, timestamp.
  3. Format TLS record: Construct 5-byte header (`ContentType=23`, `Version=0x0303`, `Length`).
  4. Derive 12-byte nonce (Salt XOR Sequence Number).
  5. Compute Additional Authenticated Data (AAD):
     * *TLS 1.2:* `seq_num (8B) || type (1B) || version (2B) || length (2B)` (13 bytes).
     * *TLS 1.3:* `type (1B) || version (2B) || length (2B)` (5 bytes); real content type (0x17) appended to plaintext before encryption.
  6. Encrypt with `aead.Seal()` and forward resulting ciphertext frame to underlying TCP endpoint.
* **Receive (`Read` / `RecvMsg`):**
  1. Read raw TLS record frame from Netstack TCP receive buffer.
  2. Parse header, verify length, extract ciphertext and 16-byte GCM authentication tag.
  3. Decrypt with `aead.Open()`. If tag validation fails, return `syserr.ErrBadMessage` (`EBADMSG`).
  4. **Interception Hook:** Trigger `seccheck.PointKTLSRx` with decrypted plaintext buffer.
  5. Copy decrypted payload into user buffer. If record was a control message (e.g., Alert 21), format `CMSG` with `TLS_GET_RECORD_TYPE`.

---

### 2.2 Subsystem B: Shared-Memory Circular Ring Buffer (`seccheck` RingBuf Sink)
**Goal:** Provide a high-throughput, lockless, non-blocking IPC transport between gVisor Sentry and OBI with zero memory copies during record emission.

#### 1. Binary Memory Layout Specification
The memory region is allocated via `memfd_create` on the host, structured in 3 contiguous sections matching `BPF_MAP_TYPE_RINGBUF`:

```
Offset 0x0000 ┌─────────────────────────────────────────────────────────┐
              │ Page 0 (4KB): CONSUMER PAGE                             │
              │   0x0000 - 0x0007: uint64 consumer_pos (atomic)         │
              │   0x0008 - 0x0FFF: Cacheline padding / reserved         │
Offset 0x1000 ├─────────────────────────────────────────────────────────┤
              │ Page 1 (4KB): PRODUCER PAGE                             │
              │   0x1000 - 0x1007: uint64 producer_pos (atomic)         │
              │   0x1008 - 0x1FFF: Cacheline padding / reserved         │
Offset 0x2000 ├─────────────────────────────────────────────────────────┤
              │ Pages 2..N+1 (Size S = 2^N bytes, e.g., 4MB / 16MB):     │
              │ DATA RING BUFFER (Mapped twice in virtual address space)│
              │                                                         │
              │ [Record 0 Header (8B)][Payload (Aligned 8B)]            │
              │ [Record 1 Header (8B)][Payload (Aligned 8B)]            │
              │ ...                                                     │
Offset 0x2000 └─────────────────────────────────────────────────────────┘
  + Size S
```

#### 2. Record Header Format & Atomic Commit Protocol
Each sample in the data buffer starts with an 8-byte header:
* `Len` (uint32, 4 bytes):
  * Bit 31 (`0x80000000`): `BPF_RINGBUF_BUSY_BIT` — Set during write reservation; cleared atomically on commit.
  * Bit 30 (`0x40000000`): `BPF_RINGBUF_DISCARD_BIT` — Set if producer abandons the record.
  * Bits 0..29 (`0x3FFFFFFF`): Actual payload length (max 1GB).
* `PgOff` (uint32, 4 bytes): Reserved / Subsystem identifier.

**Producer Reservation Protocol in Sentry (`pkg/sentry/seccheck/sinks/ringbuf`):**
```go
func (r *RingBufSink) ReserveAndCommit(msgType uint16, payload []byte) bool {
    totalLen := 8 + align8(len(payload))
    prod := atomic.LoadUint64(r.prodPos)
    cons := atomic.LoadUint64(r.consPos)

    if (prod - cons) + uint64(totalLen) > r.ringSize {
        // Backpressure: Ring buffer saturated. Drop event, never block application.
        r.droppedCount.Add(1)
        return false
    }

    // Lockless reservation
    newProd := atomic.AddUint64(r.prodPos, uint64(totalLen))
    offset := (newProd - uint64(totalLen)) & r.mask

    // 1. Write header with BUSY_BIT set
    hdrPtr := (*ringbufHeader)(unsafe.Pointer(&r.data[offset]))
    hdrPtr.Len = uint32(len(payload)) | BPF_RINGBUF_BUSY_BIT
    hdrPtr.MsgType = msgType

    // 2. Copy payload into double-mapped buffer (handles wraparound natively)
    copy(r.data[offset+8:offset+8+uint64(len(payload))], payload)

    // 3. Commit: Memory barrier + atomic clear of BUSY_BIT
    atomic.StoreUint32(&hdrPtr.Len, uint32(len(payload)))

    // 4. Notify consumer via eventfd if crossing watermark
    if (newProd - cons) >= r.watermarkBytes {
        r.notifyEventFD()
    }
    return true
}
```

#### 3. Host Coordination & Lifecycle
* **Container Startup:** When `runsc create` executes, the containerd shim / OCI hook allocates the `memfd` and `eventfd` pairs under `/run/gvisor-ringbuf/<container_id>.sock`.
* **FD Passing:** FDs are passed into Sentry via `InitConfig.TraceSession.Sinks` with sink name `"ringbuf"`.
* **Teardown:** On container exit, Sentry flushes final records, writes EOF marker, and closes FDs; OBI unmaps the memory space.

---

### 2.3 Subsystem C: Telemetry Event Schema & Wire Format
To maximize throughput, telemetry records emitted into the ring buffer use a compact, flat binary header followed by Protobuf-encoded L7 payloads.

#### 1. Wire Record Format
```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|          HeaderSize           |          MessageType          |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                         DroppedCount                          |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       ContextData (Varint)                    |
|       - Timestamp (ns), PID, TID, TGID, ContainerID           |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                       SocketTuple (Varint)                    |
|       - SrcIP, SrcPort, DstIP, DstPort, Protocol              |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
| Payload Length (uint32)       | Plaintext Stream Payload...   |
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

#### 2. Message Types (`pb.MessageType`)
* `MESSAGE_SENTRY_KTLS_TX` (`101`): Outbound plaintext data pre-encryption.
* `MESSAGE_SENTRY_KTLS_RX` (`102`): Inbound plaintext data post-decryption.
* `MESSAGE_SENTRY_SOCKET_CONNECT` (`103`): Socket lifecycle connect event.
* `MESSAGE_SENTRY_SOCKET_CLOSE` (`104`): Connection termination.
* `MESSAGE_SENTRY_HTTP2_FRAME` (`105`): Extracted HTTP/2 multiplexed frame chunk.

---

### 2.4 Subsystem D: OBI Ingestion & Agentic Decoding Pipeline
**Goal:** Ingest gVisor ring buffer records within the `otel/ebpf-instrument` daemon, route them into existing stream decoders, and output enriched OpenTelemetry signals.

#### 1. OBI gVisor Reader Source (`pkg/ebpf/gvisor/`)
Implement `gvisorRingBufReader` conforming to OBI's [`ringBufReader`](file:///usr/local/google/home/dashpole/go/src/go.opentelemetry.io/opentelemetry-ebpf-instrumentation/pkg/ebpf/common/ringbuf.go#L28-L34) interface:

```go
type GVisorRingBufReader struct {
    memfd      *os.File
    eventfd    *os.File
    ring       *ringReader
    prod       []byte
    cons       []byte
    epollFd    int
    containerID string
}

func NewGVisorReader(shmPath string, eventFDPath string) (*GVisorRingBufReader, error) {
    // 1. Mmap Consumer Page (Page 0) - PROT_READ | PROT_WRITE
    // 2. Mmap Producer + Double Data Pages (Page 1..2N) - PROT_READ
    // 3. Register eventfd with epoll
    // 4. Return initialized reader
}
```

Integrate with OBI's `ForwardRingbuf` in [`pkg/ebpf/common/ringbuf.go`](file:///usr/local/google/home/dashpole/go/src/go.opentelemetry.io/opentelemetry-ebpf-instrumentation/pkg/ebpf/common/ringbuf.go#L114-L130):
* Reads raw records using `ReadInto(&records[i])`.
* Feeds `parserLoop` running concurrently with the reader loop.

#### 2. L7 & Agentic Protocol Parsers
OBI routes gVisor records into dedicated protocol engines:
1. **HTTP/2 & HPACK Engine ([`pkg/ebpf/common/http2grpc_transform.go`](file:///usr/local/google/home/dashpole/go/src/go.opentelemetry.io/opentelemetry-ebpf-instrumentation/pkg/ebpf/common/http2grpc_transform.go)):**
   * Decodes HTTP/2 frames (HEADERS, DATA, SETTINGS).
   * Maintains persistent `bhpack.Decoder` table state keyed by `(container_id, conn_id)`.
   * Demultiplexes concurrent Stream IDs to generate discrete child spans.
2. **Server-Sent Events (SSE) Stream Tracker:**
   * Detects `Content-Type: text/event-stream`.
   * Parses streaming delta chunks (e.g., `data: {"choices":[{"delta":{"content":"..."}}]}`).
   * Emits live streaming telemetry metrics:
     * `gen_ai.client.time_to_first_token` (TTFT = $T_{\text{first\_chunk}} - T_{\text{request\_sent}}$).
     * `gen_ai.client.token_generation_rate` ($\text{tokens} / \text{sec}$).
     * Total token counts (`gen_ai.usage.output_tokens`).
3. **Model Context Protocol (MCP) Decoder ([`pkg/export/otel/tracesgen/tracesgen.go`](file:///usr/local/google/home/dashpole/go/src/go.opentelemetry.io/opentelemetry-ebpf-instrumentation/pkg/export/otel/tracesgen/tracesgen.go#L418-L445)):**
   * Inspects JSON-RPC 2.0 payloads for `tools/list` and `tools/call`.
   * Injects semantic attributes: `mcp.method.name`, `gen_ai.tool.name`, `gen_ai.tool.arguments`, `mcp.session.id`.
4. **Vector Database Inspection (Qdrant):**
   * Inspects `/collections/{collection}/points/search` requests in native Rust binaries.
   * Extracts search vector dimensionality, `top_k`, distance threshold, and execution latency.
5. **Kubernetes Metadata Decoration ([`pkg/transform/k8s.go`](file:///usr/local/google/home/dashpole/go/src/go.opentelemetry.io/opentelemetry-ebpf-instrumentation/pkg/transform/k8s.go)):**
   * Correlates Sentry's `ContainerID` with local K8s Pod metadata: `k8s.pod.name`, `k8s.namespace.name`, `k8s.node.name`.

---

### 2.5 Subsystem E: In-Sentry Distributed Context Propagation
**Goal:** Dynamically propagate W3C `traceparent` (`00-{trace_id}-{span_id}-01`) across polyglot microservices without application SDK code.

#### 1. Outbound Request Context Injection (kTLS TX Path)
* When an outbound HTTP/1.1 or HTTP/2 request is intercepted in Sentry on `PointKTLSTx`:
  * Sentry inspects the first chunk of data. If an HTTP request line (`POST /... HTTP/1.1` or HTTP/2 HEADERS frame) is detected:
    * If no `traceparent` header is present, Sentry generates a new 128-bit `trace_id` and 64-bit `span_id` (or inherits from active task context).
    * Sentry mutates the header buffer before framing, inserting `traceparent: 00-{trace_id}-{span_id}-01\r\n`.
    * Encrypts and transmits the modified TLS record.

#### 2. Inbound Request Context Extraction (kTLS RX Path)
* On `PointKTLSRx`, Sentry / OBI extracts the incoming `traceparent` header.
* Sets `span.ParentSpanID = {span_id}` and `span.TraceID = {trace_id}`, perfectly linking downstream server spans to upstream client spans in Google Cloud Trace.

---

## 3. Phased Implementation Milestones

```
┌────────────────────────────────────────────────────────────────────────────────────────┐
│                              PROTOTYPE DELIVERY TIMELINE                               │
└────────────────────────────────────────────────────────────────────────────────────────┘
  Week 1-2      Week 3-4      Week 5-6      Week 7-8      Week 9-10     Week 11-12
 [Milestone 1] [Milestone 2] [Milestone 3] [Milestone 4] [Milestone 5] [Milestone 6]
  RingBuf IPC    Sentry kTLS   E2E Ingestion  L7/GenAI      Context       GKE Hardening
  Foundation     & Plaintext   Pipeline       Decoders      Propagation   & Live Demo
```

### Milestone 1: Shared-Memory Ring Buffer & IPC Foundation (gVisor $\leftrightarrow$ OBI)
* **Scope & Deliverables:**
  1. Define binary ring buffer memory structure matching Linux BPF ringbuf specification.
  2. Implement `pkg/sentry/seccheck/sinks/ringbuf` in gVisor with atomic reservation, busy-bit commit protocol, and `eventfd` notification.
  3. Implement `pkg/ebpf/gvisor/reader.go` in OBI with `mmap` dual-mapping and `epoll_wait` integration.
  4. Develop a standalone C++/Go synthetic benchmark harness (`cmd/ringbuf-bench`) testing high-concurrency event production and consumption across process boundaries.

### Milestone 2: Sentry Kernel TLS (kTLS) Subsystem & Plaintext Interception
* **Scope & Deliverables:**
  1. Add Linux kTLS ABI definitions to `pkg/abi/linux/socket.go` and `pkg/abi/linux/tls.go`.
  2. Implement `TCP_ULP` ("tls") and `SOL_TLS` (`TLS_TX`, `TLS_RX`) setsockopt handlers in `pkg/sentry/socket/netstack/netstack.go`.
  3. Build AES-128-GCM and AES-256-GCM record encryption/decryption pipeline with Go `crypto/cipher`.
  4. Instrument Sentry kTLS data path with `seccheck` tracepoints (`PointKTLSTx`, `PointKTLSRx`) to capture plaintext buffers.
  5. Support TLS 1.2 and TLS 1.3 record framing and sequence number tracking.

### Milestone 3: End-to-End Ingestion Pipeline Integration
* **Scope & Deliverables:**
  1. Implement binary serialization format in Sentry for socket and kTLS events.
  2. Implement dynamic Pod discovery in OBI via inotify watcher on `/run/gvisor-ringbuf/`.
  3. Implement `ReadGVisorTraceIntoSpan` in OBI ([`pkg/ebpf/common/common.go`](file:///usr/local/google/home/dashpole/go/src/go.opentelemetry.io/opentelemetry-ebpf-instrumentation/pkg/ebpf/common/common.go#L471)), converting raw ring buffer records into `request.Span` structs.
  4. Connect OBI's gVisor reader into `ebpfcommon.ForwardRingbuf` and verify baseline HTTP/1.1 span emission to OTel collector.

### Milestone 4: L7 Protocol Decoders & Agentic Telemetry Engine
* **Scope & Deliverables:**
  1. Integrate HTTP/2 framer and HPACK dynamic decompression ([`http2grpc_transform.go`](file:///usr/local/google/home/dashpole/go/src/go.opentelemetry.io/opentelemetry-ebpf-instrumentation/pkg/ebpf/common/http2grpc_transform.go)) with kTLS stream chunks.
  2. Implement Server-Sent Events (SSE) streaming token parser, calculating TTFT and token generation velocity.
  3. Implement Model Context Protocol (MCP) JSON-RPC parser for `tools/list` and `tools/call`.
  4. Implement Qdrant Vector Search REST/gRPC parser for `/collections/{name}/points/search`.
  5. Generate semantic span attributes and Prometheus RED metrics for Agentic operations.

### Milestone 5: In-Sentry Distributed Context Propagation
* **Scope & Deliverables:**
  1. Implement in-flight W3C `traceparent` header injection in Sentry during outbound kTLS TX framing.
  2. Implement inbound `traceparent` extraction on kTLS RX, setting parent span IDs across multi-tier sandbox hops.
  3. Validate end-to-end trace waterfall across 3 sandboxed services (`web-frontend` $\rightarrow$ `agent-orchestrator` $\rightarrow$ `order-mcp-server`).

### Milestone 6: GKE Integration, Production Hardening & Live Demo Validation
* **Scope & Deliverables:**
  1. Build Containerd CRI integration with `runsc` runtime handler (`runtimeClass: gvisor`).
  2. Package OBI DaemonSet with GKE Managed OpenTelemetry Collector and Workload Identity IAM.
  3. Deploy the live Agentic demo application stack on GKE (`dashpole-dev` cluster).
  4. Execute fault injection tests (downstream MCP latency, vector search errors) and verify immediate detection in Cloud Trace and Cloud Monitoring.
  5. Final security and memory safety audit (zero key leakage, unprivileged container boundaries).

---

## 4. Rigorous Testing Gates per Milestone

Each milestone requires 100% passage of its dedicated Testing Gate before proceeding to downstream development.

```
┌──────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│                                       MILESTONE TESTING GATES                                            │
├───────────────────┬─────────────────────────────────────────────────┬────────────────────────────────────┤
│ Gate              │ Verification Criteria                           │ Benchmark / Pass Threshold         │
├───────────────────┼─────────────────────────────────────────────────┼────────────────────────────────────┤
│ Gate 1: RingBuf   │ Concurrency, throughput, drop handling, leak    │ > 5,000,000 events/sec             │
│                   │ detection in shared memory ring buffer          │ Zero lost events under non-sat     │
├───────────────────┼─────────────────────────────────────────────────┼────────────────────────────────────┤
│ Gate 2: kTLS      │ Linux kTLS UAPI compliance, OpenSSL/BoringSSL   │ 100% Linux selftests pass          │
│                   │ handshake & transfer interop, zero data corrupt │ Zero crypto failures on 100GB      │
├───────────────────┼─────────────────────────────────────────────────┼────────────────────────────────────┤
│ Gate 3: Ingestion │ End-to-end sandbox-to-collector pipeline, span  │ 100% valid OTLP trace spans        │
│                   │ generation, K8s metadata attribution            │ < 5% CPU overhead in Sentry        │
├───────────────────┼─────────────────────────────────────────────────┼────────────────────────────────────┤
│ Gate 4: Decoders  │ HPACK demuxing, SSE TTFT calculation, MCP tool  │ 100% tool_call attribute accuracy  │
│                   │ call extraction, Vector search telemetry        │ Exact token counts match ground tr │
├───────────────────┼─────────────────────────────────────────────────┼────────────────────────────────────┤
│ Gate 5: Context   │ Distributed trace stitching across multi-tier   │ Single unified trace_id across     │
│                   │ polyglot sandboxes (Python, Rust, C++)          │ 100% of end-to-end requests        │
├───────────────────┼─────────────────────────────────────────────────┼────────────────────────────────────┤
│ Gate 6: GKE E2E   │ Production GKE validation, Cloud Trace waterfall│ Full 15-min run-of-show automated  │
│                   │ RED metrics, fault injection pinpointing        │ Zero security audit violations     │
└───────────────────┴─────────────────────────────────────────────────┴────────────────────────────────────┘
```

---

### Testing Gate 1: Ring Buffer IPC Subsystem
* **Test Suite:** `pkg/sentry/seccheck/sinks/ringbuf/ringbuf_test.go` & `pkg/ebpf/gvisor/reader_test.go`
* **Test Cases:**
  1. `TestRingBuf_AtomicOrdering`: 64 producer goroutines writing concurrently to a single ring buffer; consumer verifies strict monotonic sequence per thread.
  2. `TestRingBuf_WraparoundIntegrity`: Write 10GB of random data through a 4MB ring buffer; verify 0 byte corruption across virtual memory wraparound boundaries.
  3. `TestRingBuf_BackpressureNonBlocking`: Saturate ring buffer by halting consumer; verify producer drops events gracefully, increments `droppedCount`, and never blocks Sentry task execution.
  4. `TestRingBuf_EventFDWakeup`: Verify consumer wakes up from `epoll_wait` within 100 microseconds of producer crossing watermark.
  5. `TestRingBuf_LeakDetector`: Run under Go race detector and AddressSanitizer; verify clean shutdown and unmap with zero FD or memory leaks.
* **Pass Threshold:** Minimum throughput of **5,000,000 events/sec** on 8 vCPUs with zero memory leaks.

---

### Testing Gate 2: Sentry kTLS Cryptographic Engine
* **Test Suite:** `pkg/sentry/socket/netstack/ktls_test.go` & Linux kernel selftests (`tools/testing/selftests/net/tls.c`)
* **Test Cases:**
  1. `TestKTLS_LinuxUAPICompat`: Port Linux kernel kTLS test suite (`tls.c`) to run inside `runsc`; verify `setsockopt` return codes, errno mapping, and unsupported option handling.
  2. `TestKTLS_AES128GCM_Interop`: Interop test between a standard OpenSSL 3.0 / BoringSSL client and a Python `http.server` running inside Sentry with kTLS enabled.
  3. `TestKTLS_AES256GCM_Interop`: High-entropy payload transfer verifying 256-bit key negotiation and record authentication.
  4. `TestKTLS_TLS13_KeyUpdate`: Trigger TLS 1.3 `KeyUpdate` handshake messages; verify Sentry transitions cipher keys seamlessly without dropping connections.
  5. `TestKTLS_PlaintextInterceptionAccuracy`: Verify that byte-for-byte plaintext captured in `PointKTLSTx`/`PointKTLSRx` matches the raw application string before encryption.
  6. `TestKTLS_CorruptedFrameRejection`: Inject byte mutations into ciphertext in Netstack; verify decryption fails with `EBADMSG` and no plaintext is exposed.
* **Pass Threshold:** 100% pass rate across 50+ Linux kTLS selftests; zero data corruption over a **100 GB continuous transfer**.

---

### Testing Gate 3: Pipeline Ingestion & Span Lifecycle
* **Test Suite:** `test/integration/gvisor_obi_test.go`
* **Test Cases:**
  1. `TestPipeline_HTTP11Basic`: Execute HTTP/1.1 GET and POST requests inside a gVisor container; verify OBI consumes records, parses HTTP status/method, and generates `request.Span`.
  2. `TestPipeline_K8sEnrichment`: Verify spans include correct `k8s.pod.name`, `k8s.container.name`, and `k8s.namespace.name` derived from Sentry container ID.
  3. `TestPipeline_BatchTimeout`: Verify OBI flushes batches within `batch_timeout: 100ms` even under low-throughput conditions.
  4. `TestPipeline_SentryOverhead`: Measure HTTP benchmark (wrk / autoconn) with and without seccheck ringbuf sink enabled.
* **Pass Threshold:** Sentry throughput degradation must be **< 5% CPU overhead** at 50,000 requests/sec. Spans successfully received by mock OTel collector.

---

### Testing Gate 4: Agentic & GenAI Semantic Protocol Decoders
* **Test Suite:** `pkg/ebpf/common/genai_decoders_test.go`
* **Test Cases:**
  1. `TestDecoder_HTTP2HPACK`: Send multiplexed HTTP/2 streams across 10 concurrent streams; verify OBI maintains separate dynamic HPACK tables and outputs distinct spans per Stream ID.
  2. `TestDecoder_SSETokenStreaming`: Stream a mock LLM response with 50 chunks over SSE; verify OBI measures TTFT within 1ms accuracy and calculates correct total output token count.
  3. `TestDecoder_MCPToolCall`: Execute JSON-RPC `tools/call` for `get_order_status` with arguments `{"order_id": "ORD-123"}`; verify span contains `mcp.method.name="tools/call"`, `gen_ai.tool.name="get_order_status"`, and `gen_ai.tool.call.arguments`.
  4. `TestDecoder_QdrantVectorSearch`: Execute vector similarity search against `/collections/kb/points/search`; verify span extracts vector dimensions, `top_k`, and collection name.
* **Pass Threshold:** 100% attribute extraction accuracy across all test fixtures. Zero parser panics on malformed JSON or truncated SSE frames.

---

### Testing Gate 5: Distributed Trace Context Propagation
* **Test Suite:** `test/integration/context_propagation_test.go`
* **Test Cases:**
  1. `TestPropagation_SingleHop`: Client container calls Server container; verify server span receives client's span ID as `ParentSpanID` and identical `TraceID`.
  2. `TestPropagation_ThreeTierWaterfall`: Deploy `web-frontend` $\rightarrow$ `agent-orchestrator` $\rightarrow$ `order-mcp-server` inside gVisor sandboxes; execute end-to-end query.
  3. `TestPropagation_MixedProtocols`: Verify context propagation works seamlessly across HTTP/1.1 $\rightarrow$ HTTP/2 $\rightarrow$ HTTPS kTLS transitions.
* **Pass Threshold:** **100% trace cohesion** across 10,000 synthetic multi-tier requests in Google Cloud Trace waterfall view.

---

### Testing Gate 6: Production GKE Validation & Chaos Hardening
* **Test Suite:** `dashpole_demos/gke_otel_obi_demo/scripts/verify-all.sh` on live GKE cluster (`dashpole-dev`)
* **Test Cases:**
  1. `TestGKE_FullRunOfShow`: Execute the automated 15-minute presentation script (`./scripts/run-demo.sh`).
  2. `TestGKE_FaultInjection`: Inject 2.5s artificial latency into `order-mcp-server`; verify Cloud Trace immediately pinpoints the bottleneck without code changes.
  3. `TestGKE_REDMetrics`: Verify Prometheus metrics (`http_requests_total`, `request_duration_seconds`, `gen_ai_tokens_total`) are ingested into Google Cloud Monitoring via `PodMonitoring`.
  4. `TestGKE_SecurityAudit`: Run vulnerability and privilege scan; verify OBI runs without host filesystem write permissions and gVisor sandboxes remain strictly unprivileged.
* **Pass Threshold:** Successful green run of master test runner [`scripts/verify-all.sh`](file:///usr/local/google/home/dashpole/dashpole_demos/gke_otel_obi_demo/scripts/verify-all.sh) on GKE; zero errors in Cloud Logging.

---

## 5. Implementation Task Breakdown & Work Estimates

```
┌────────────────────────────────────────────────────────────────────────────────────────────────────────┐
│ TASK BREAKDOWN & COMPONENT OWNERSHIP                                                                   │
├────────────────────────────┬──────────────────────────────────────────┬──────────────┬─────────────────┤
│ Module                     │ Task Description                         │ Owner        │ Est. Duration   │
├────────────────────────────┼──────────────────────────────────────────┼──────────────┼─────────────────┤
│ gVisor ABI & Socket        │ Add SOL_TLS, TCP_ULP, crypto constants   │ gVisor Eng   │ 3 days          │
│ Sentry kTLS Engine         │ AES-GCM cipher pipeline & record framing │ gVisor Eng   │ 7 days          │
│ Sentry Plaintext Hooks     │ Seccheck points on TX and RX data paths  │ gVisor Eng   │ 4 days          │
│ Sentry RingBuf Sink        │ Shared-memory BPF ringbuf sink & eventfd │ gVisor Eng   │ 6 days          │
│ OBI gVisor Reader Source   │ Memory mapping & epoll ingestion loop    │ OBI SIG      │ 5 days          │
│ OBI Pipeline Integration   │ Bridge ringbuf records to request.Span   │ OBI SIG      │ 4 days          │
│ L7 HTTP/2 & SSE Decoders   │ Demuxing, HPACK, SSE TTFT token tracking │ OBI SIG      │ 6 days          │
│ MCP & Vector DB Decoders   │ JSON-RPC tools/call & Qdrant parsing     │ OBI SIG      │ 5 days          │
│ In-Sentry W3C Propagation  │ In-flight traceparent injection/extract  │ gVisor/OBI   │ 6 days          │
│ GKE & Containerd Packaging │ CRI runtimeClass & DaemonSet configs     │ SRE / OBI    │ 4 days          │
│ E2E Verification & Demo    │ Run-of-show scripts & Cloud Trace audit  │ All          │ 5 days          │
└────────────────────────────┴──────────────────────────────────────────┴──────────────┴─────────────────┘
```

---

## 6. Risk Analysis & Mitigation Strategies

| Risk | Severity | Impact | Mitigation Strategy |
| :--- | :--- | :--- | :--- |
| **Ring Buffer Saturation under Burst Traffic** | High | Telemetry data loss / dropped spans | Implement dynamic adaptive sizing (default 16MB per container) and lockless atomic drop counters. Sentry never blocks; OBI monitors `dropped_count` metrics. |
| **kTLS Cryptographic Performance Overhead** | Medium | Increased CPU usage in Sentry | Leverage Go assembly-optimized crypto (`crypto/cipher` AES-NI / ARMv8 Crypto Extensions). Pre-allocate AEAD cipher instances per connection to avoid GC pressure. |
| **HTTP/2 HPACK Desynchronization** | High | Inability to decode subsequent HTTP/2 headers if a frame is dropped | Isolate HPACK decoder tables per connection. If frame loss occurs on a stream, flag connection state and fall back to heuristic path extraction until next full header table sync. |
| **Multi-Container Pod IPC Isolation** | Medium | Security boundary violation if ringbufs share memory across pods | Allocate dedicated `memfd` file descriptors per Sandbox/Pod. Restrict OBI access to host-level runtime paths using strict Kubernetes node permissions. |
| **TLS 1.3 Zero-RTT & Early Data** | Low | Plaintext ordering ambiguity during handshake | Disallow 0-RTT in Sentry kTLS initial configuration; require full 1-RTT handshake before activating `TLS_TX`/`TLS_RX`. |

---

## 7. Deployment Configuration Artifacts

### 7.1 OBI ConfigMap for gVisor Ingestion (`03-obi/02-obi-configmap.yaml`)
```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: obi-config
  namespace: otel-system
data:
  ebpf-instrument-config.yaml: |
    log_level: debug
    gvisor:
      enabled: true
      ringbuf_dir: "/run/gvisor-ringbuf"
      watermark_kb: 64
      batch_timeout: 100ms
    otel_traces_export:
      endpoint: "http://opentelemetry-collector.gke-managed-otel.svc.cluster.local:4317"
      insecure: true
      protocol: grpc
      sampler:
        name: always_on
    ebpf:
      context_propagation: all
      track_request_headers: true
      payload_extraction:
        http:
          jsonrpc:
            enabled: true
          genai:
            mcp:
              enabled: true
            retrieval:
              enabled: true
            gemini:
              enabled: true
    routes:
      unmatched: heuristic
    attributes:
      kubernetes:
        enable: true
```

### 7.2 Containerd / gVisor Seccheck Pod Init (`/etc/gvisor/pod_init.json`)
```json
{
  "trace_session": {
    "name": "OBI-Ingestion",
    "points": [
      { "name": "container/start" },
      { "name": "sentry/clone" },
      { "name": "sentry/task_exit" },
      {
        "name": "sentry/ktls_tx",
        "optional_fields": ["payload", "socket_tuple"],
        "context_fields": ["time", "container_id", "thread_id", "process_name"]
      },
      {
        "name": "sentry/ktls_rx",
        "optional_fields": ["payload", "socket_tuple"],
        "context_fields": ["time", "container_id", "thread_id", "process_name"]
      }
    ],
    "sinks": [
      {
        "name": "ringbuf",
        "config": {
          "buffer_size_mb": 16,
          "watermark_kb": 64
        },
        "ignore_setup_error": false
      }
    ]
  }
}
```
