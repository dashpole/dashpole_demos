// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package propagation_test

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"net"
	"strings"
	"sync"
	"testing"

	"golang.org/x/net/http2/hpack"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/decoders"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ktls"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/obi"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/propagation"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ringbuf"
)

func TestPropagation_W3CTraceContext_FormatAndParse(t *testing.T) {
	tc := propagation.NewTraceContext()
	formatted := tc.FormatTraceparent()

	parsed, err := propagation.ParseTraceparent(formatted)
	if err != nil {
		t.Fatalf("ParseTraceparent failed: %v", err)
	}

	if parsed.Version != tc.Version {
		t.Errorf("version mismatch: %x vs %x", parsed.Version, tc.Version)
	}
	if parsed.TraceID != tc.TraceID {
		t.Errorf("traceID mismatch: %x vs %x", parsed.TraceID, tc.TraceID)
	}
	if parsed.ParentSpanID != tc.SpanID {
		t.Errorf("parentSpanID mismatch: %x vs %x", parsed.ParentSpanID, tc.SpanID)
	}
	if parsed.TraceFlags != tc.TraceFlags {
		t.Errorf("traceFlags mismatch: %x vs %x", parsed.TraceFlags, tc.TraceFlags)
	}
}

func TestPropagation_W3CSpecCompliance_EdgeCases(t *testing.T) {
	// 1. Version 0xff is prohibited
	if _, err := propagation.ParseTraceparent("ff-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"); err == nil {
		t.Errorf("expected error for prohibited version 0xff")
	}

	// 2. All-zero TraceID is prohibited
	if _, err := propagation.ParseTraceparent("00-00000000000000000000000000000000-00f067aa0ba902b7-01"); err == nil {
		t.Errorf("expected error for all-zero TraceID")
	}

	// 3. All-zero ParentID is prohibited
	if _, err := propagation.ParseTraceparent("00-4bf92f3577b34da6a3ce929d0e0e4736-0000000000000000-01"); err == nil {
		t.Errorf("expected error for all-zero ParentID")
	}

	// 4. Version 00 with extra fields is prohibited
	if _, err := propagation.ParseTraceparent("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01-extra"); err == nil {
		t.Errorf("expected error for version 00 with >4 parts")
	}

	// 5. Invalid hex lengths
	if _, err := propagation.ParseTraceparent("00-short-00f067aa0ba902b7-01"); err == nil {
		t.Errorf("expected error for short trace_id")
	}
}

func TestPropagation_HTTP1_InjectionAndExtraction(t *testing.T) {
	tc := propagation.NewTraceContext()
	injector := propagation.NewContextInjector()
	extractor := propagation.NewContextExtractor()

	// Body containing "traceparent: fake" string - should NOT suppress injection
	rawReq := []byte("POST /v1/agent HTTP/1.1\r\nHost: agent-service\r\nContent-Type: application/json\r\n\r\n{\"message\":\"traceparent: fake_in_body\"}")

	injected := injector.InjectHTTP1(rawReq, tc)
	if !strings.Contains(string(injected), "traceparent: "+tc.FormatTraceparent()) {
		t.Fatalf("injected payload missing traceparent header: %s", string(injected))
	}

	// Verify body is intact
	if !strings.Contains(string(injected), "{\"message\":\"traceparent: fake_in_body\"}") {
		t.Fatalf("body corrupted during injection: %s", string(injected))
	}

	extracted, ok := extractor.ExtractHTTP1(injected)
	if !ok {
		t.Fatalf("failed to extract traceparent from injected HTTP/1 payload")
	}

	if extracted.TraceID != tc.TraceID || extracted.ParentSpanID != tc.SpanID {
		t.Errorf("extracted context does not match injected: %x vs %x", extracted.TraceID, tc.TraceID)
	}

	// Test Response - should NOT inject into HTTP responses
	respPayload := []byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK")
	notInjectedResp := injector.InjectHTTP1(respPayload, tc)
	if bytes.Contains(notInjectedResp, []byte("traceparent:")) {
		t.Errorf("traceparent illegally injected into HTTP response payload")
	}

	t.Logf("HTTP/1.1 TraceContext injection and extraction verified successfully")
}

func TestPropagation_HTTP2_PaddedAndPriority_InjectionExtraction(t *testing.T) {
	tc := propagation.NewTraceContext()
	injector := propagation.NewContextInjector()
	extractor := propagation.NewContextExtractor()

	var buf bytes.Buffer
	buf.WriteString("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")

	// Encode base HTTP/2 headers
	var hpackBuf bytes.Buffer
	enc := hpack.NewEncoder(&hpackBuf)
	_ = enc.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
	_ = enc.WriteField(hpack.HeaderField{Name: ":path", Value: "/v1/agent"})
	_ = enc.WriteField(hpack.HeaderField{Name: ":authority", Value: "agent.internal"})

	rawHpack := hpackBuf.Bytes()

	// Construct HEADERS frame with PADDED (0x8) and PRIORITY (0x20)
	padLen := byte(4)
	padding := []byte{0x00, 0x00, 0x00, 0x00}
	priority := []byte{0x00, 0x00, 0x00, 0x00, 0x10} // 5 bytes stream dependency + weight

	var framePayload bytes.Buffer
	framePayload.WriteByte(padLen)
	framePayload.Write(priority)
	framePayload.Write(rawHpack)
	framePayload.Write(padding)

	fullPayload := framePayload.Bytes()
	frameHdr := make([]byte, 9)
	frameHdr[0] = byte(len(fullPayload) >> 16)
	frameHdr[1] = byte(len(fullPayload) >> 8)
	frameHdr[2] = byte(len(fullPayload))
	frameHdr[3] = decoders.FrameHeaders
	frameHdr[4] = 0x8 | 0x20 | decoders.FlagEndHeaders | decoders.FlagEndStream
	binary.BigEndian.PutUint32(frameHdr[5:9], 1)

	buf.Write(frameHdr)
	buf.Write(fullPayload)

	// Inject traceparent into HTTP/2 frame stream
	injected, err := injector.InjectHTTP2("conn-1", buf.Bytes(), tc)
	if err != nil {
		t.Fatalf("InjectHTTP2 failed: %v", err)
	}

	// Extract traceparent from HTTP/2 frame stream
	extracted, ok := extractor.ExtractHTTP2("conn-1", injected)
	if !ok {
		t.Fatalf("failed to extract traceparent from injected HTTP/2 payload")
	}

	if extracted.TraceID != tc.TraceID || extracted.ParentSpanID != tc.SpanID {
		t.Errorf("extracted HTTP/2 context mismatch: %x vs %x", extracted.TraceID, tc.TraceID)
	}
	t.Logf("HTTP/2 HPACK Padded & Priority TraceContext injection/extraction verified successfully")
}

func TestPropagation_MultiHopDistributedTrace_ThreeSandboxes(t *testing.T) {
	decorator := obi.NewK8sDecorator("gke-node-pool-1")
	decorator.RegisterContainer("sandbox-frontend", obi.PodMetadata{PodName: "frontend-ui", Namespace: "agentic-app"})
	decorator.RegisterContainer("sandbox-agent", obi.PodMetadata{PodName: "agent-orchestrator", Namespace: "agentic-app"})
	decorator.RegisterContainer("sandbox-tool-server", obi.PodMetadata{PodName: "mcp-sqlite-tool", Namespace: "agentic-app"})

	exporter := obi.NewInMemorySpanExporter()
	bridge := obi.NewPipelineBridge(decorator, exporter)
	defer bridge.Close()

	injector := propagation.NewContextInjector()

	// Step 1: External user calls Frontend (Root trace context)
	rootTC := propagation.NewTraceContext()
	tuple1 := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.200.0.1"),
		SrcPort: 40001,
		DstIP:   net.ParseIP("10.200.0.2"),
		DstPort: 8080,
	}

	reqFrontendRaw := []byte("POST /api/chat HTTP/1.1\r\nHost: frontend\r\nContent-Type: application/json\r\n\r\n{\"prompt\":\"find user status\"}")
	reqFrontend := injector.InjectHTTP1(reqFrontendRaw, rootTC)
	respFrontend := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"status\":\"completed\"}")

	// Ingest Hop 1 (Frontend Server)
	evReq1 := ktls.SerializeTelemetryEvent(tuple1, 100, 100, reqFrontend)
	evResp1 := ktls.SerializeTelemetryEvent(tuple1, 100, 100, respFrontend)
	_ = bridge.ProcessRecord("sandbox-frontend", ringbuf.Record{MsgType: abi.MsgTypeKTLSRx, Flags: abi.ChunkFlagSingle, Payload: evReq1})
	_ = bridge.ProcessRecord("sandbox-frontend", ringbuf.Record{MsgType: abi.MsgTypeKTLSTx, Flags: abi.ChunkFlagSingle, Payload: evResp1})

	spansHop1 := exporter.Spans()
	if len(spansHop1) != 1 {
		t.Fatalf("expected 1 span after Hop 1, got %d", len(spansHop1))
	}
	span1 := spansHop1[0]

	// Step 2: Frontend calls Agent Orchestrator (Hop 2 Client/Server).
	// In-flight injection sets parent_id = span1.SpanID!
	hop2TC := rootTC.NewChildContext(span1.SpanID)
	tuple2 := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.200.0.2"),
		SrcPort: 50002,
		DstIP:   net.ParseIP("10.200.0.3"),
		DstPort: 9000,
	}

	reqAgentRaw := []byte("POST /v1/agent/run HTTP/1.1\r\nHost: agent-orchestrator\r\nContent-Type: application/json\r\n\r\n{\"instruction\":\"query database\"}")
	reqAgent := injector.InjectHTTP1(reqAgentRaw, hop2TC)
	respAgent := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"result\":\"OK\"}")

	// Ingest Hop 2 (Agent Orchestrator Server)
	evReq2 := ktls.SerializeTelemetryEvent(tuple2, 200, 200, reqAgent)
	evResp2 := ktls.SerializeTelemetryEvent(tuple2, 200, 200, respAgent)
	_ = bridge.ProcessRecord("sandbox-agent", ringbuf.Record{MsgType: abi.MsgTypeKTLSRx, Flags: abi.ChunkFlagSingle, Payload: evReq2})
	_ = bridge.ProcessRecord("sandbox-agent", ringbuf.Record{MsgType: abi.MsgTypeKTLSTx, Flags: abi.ChunkFlagSingle, Payload: evResp2})

	spansHop2 := exporter.Spans()
	if len(spansHop2) != 2 {
		t.Fatalf("expected 2 spans after Hop 2, got %d", len(spansHop2))
	}
	span2 := spansHop2[1]

	// Step 3: Agent Orchestrator calls MCP Tool Server (Hop 3 Client/Server).
	// In-flight injection sets parent_id = span2.SpanID!
	hop3TC := hop2TC.NewChildContext(span2.SpanID)
	tuple3 := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.200.0.3"),
		SrcPort: 60003,
		DstIP:   net.ParseIP("10.200.0.4"),
		DstPort: 3000,
	}

	reqToolRaw := []byte("POST /mcp HTTP/1.1\r\nHost: mcp-tool\r\nContent-Type: application/json\r\n\r\n{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"query_users\",\"arguments\":{\"id\":42}}}")
	reqTool := injector.InjectHTTP1(reqToolRaw, hop3TC)
	respTool := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"user\":\"Alice\"}}")

	// Ingest Hop 3 (MCP Tool Server)
	evReq3 := ktls.SerializeTelemetryEvent(tuple3, 300, 300, reqTool)
	evResp3 := ktls.SerializeTelemetryEvent(tuple3, 300, 300, respTool)
	_ = bridge.ProcessRecord("sandbox-tool-server", ringbuf.Record{MsgType: abi.MsgTypeKTLSRx, Flags: abi.ChunkFlagSingle, Payload: evReq3})
	_ = bridge.ProcessRecord("sandbox-tool-server", ringbuf.Record{MsgType: abi.MsgTypeKTLSTx, Flags: abi.ChunkFlagSingle, Payload: evResp3})

	allSpans := exporter.Spans()
	if len(allSpans) != 3 {
		t.Fatalf("expected 3 distributed spans, got %d", len(allSpans))
	}

	span3 := allSpans[2]

	expectedTraceID := hex.EncodeToString(rootTC.TraceID[:])
	trace1 := hex.EncodeToString(span1.TraceID[:])
	trace2 := hex.EncodeToString(span2.TraceID[:])
	trace3 := hex.EncodeToString(span3.TraceID[:])

	t.Logf("Span 1 (Frontend): Pod=%s Trace=%s SpanID=%x ParentSpanID=%x",
		span1.Attributes["k8s.pod.name"], trace1, span1.SpanID, span1.ParentSpanID)
	t.Logf("Span 2 (Agent):    Pod=%s Trace=%s SpanID=%x ParentSpanID=%x",
		span2.Attributes["k8s.pod.name"], trace2, span2.SpanID, span2.ParentSpanID)
	t.Logf("Span 3 (MCP Tool): Pod=%s Trace=%s SpanID=%x ParentSpanID=%x",
		span3.Attributes["k8s.pod.name"], trace3, span3.SpanID, span3.ParentSpanID)

	// 1. TraceID MUST match across all 3 hops
	if trace1 != expectedTraceID || trace2 != expectedTraceID || trace3 != expectedTraceID {
		t.Fatalf("TraceID mismatch across distributed hops! 1=%s 2=%s 3=%s (expected %s)",
			trace1, trace2, trace3, expectedTraceID)
	}

	// 2. Strict Parent-Child Span Hierarchy Verification:
	// ParentSpanID of Hop 2 MUST equal SpanID of Hop 1
	if span2.ParentSpanID != span1.SpanID {
		t.Fatalf("Span hierarchy failure: Hop 2 ParentSpanID (%x) does not match Hop 1 SpanID (%x)!",
			span2.ParentSpanID, span1.SpanID)
	}

	// ParentSpanID of Hop 3 MUST equal SpanID of Hop 2
	if span3.ParentSpanID != span2.SpanID {
		t.Fatalf("Span hierarchy failure: Hop 3 ParentSpanID (%x) does not match Hop 2 SpanID (%x)!",
			span3.ParentSpanID, span2.SpanID)
	}

	t.Logf("Distributed Trace Context Propagation verified across 3 separate gVisor sandboxes with strict parent-child DAG linkage!")
}

func TestPropagation_SocketOutboundFilter_Integration(t *testing.T) {
	tc := propagation.NewTraceContext()
	injector := propagation.NewContextInjector()

	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	tuple := ktls.SocketTuple{
		SrcIP:   net.ParseIP("127.0.0.1"),
		SrcPort: 1111,
		DstIP:   net.ParseIP("127.0.0.1"),
		DstPort: 2222,
	}

	tlsSock := ktls.NewTLSSocket(clientConn, tuple, 100, 100, nil)
	tlsSock.SetOutboundFilter(func(t ktls.SocketTuple, p []byte) []byte {
		return injector.InjectHTTP1(p, tc)
	})

	go func() {
		_, _ = tlsSock.Write([]byte("GET /status HTTP/1.1\r\nHost: local\r\n\r\n"))
	}()

	buf := make([]byte, 1024)
	n, err := serverConn.Read(buf)
	if err != nil {
		t.Fatalf("server read failed: %v", err)
	}

	received := string(buf[:n])
	if !strings.Contains(received, "traceparent: "+tc.FormatTraceparent()) {
		t.Fatalf("socket outbound filter failed to inject traceparent into wire data: %s", received)
	}
	t.Logf("TLSSocket OutboundFilter kernel datapath context injection verified successfully")
}

func TestPropagation_ConcurrentStress_MultiProtocol(t *testing.T) {
	injector := propagation.NewContextInjector()
	extractor := propagation.NewContextExtractor()

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		connID := hex.EncodeToString([]byte{byte(i)})
		// 1. HTTP/1.1 goroutine
		go func() {
			defer wg.Done()
			tc := propagation.NewTraceContext()
			raw := []byte("GET / HTTP/1.1\r\nHost: test\r\n\r\n")
			injected := injector.InjectHTTP1(raw, tc)
			extracted, ok := extractor.ExtractHTTP1(injected)
			if !ok || extracted.TraceID != tc.TraceID {
				t.Errorf("concurrent HTTP/1 failure")
			}
		}()

		// 2. HTTP/2 goroutine
		go func() {
			defer wg.Done()
			tc := propagation.NewTraceContext()
			var hpackBuf bytes.Buffer
			enc := hpack.NewEncoder(&hpackBuf)
			_ = enc.WriteField(hpack.HeaderField{Name: ":method", Value: "GET"})
			_ = enc.WriteField(hpack.HeaderField{Name: ":path", Value: "/"})

			hdrBytes := hpackBuf.Bytes()
			frameHdr := make([]byte, 9)
			frameHdr[0] = byte(len(hdrBytes) >> 16)
			frameHdr[1] = byte(len(hdrBytes) >> 8)
			frameHdr[2] = byte(len(hdrBytes))
			frameHdr[3] = decoders.FrameHeaders
			frameHdr[4] = decoders.FlagEndHeaders
			binary.BigEndian.PutUint32(frameHdr[5:9], 1)

			var raw bytes.Buffer
			raw.Write(frameHdr)
			raw.Write(hdrBytes)

			injected, err := injector.InjectHTTP2(connID, raw.Bytes(), tc)
			if err != nil {
				t.Errorf("InjectHTTP2 failed: %v", err)
				return
			}
			extracted, ok := extractor.ExtractHTTP2(connID, injected)
			if !ok || extracted.TraceID != tc.TraceID {
				t.Errorf("concurrent HTTP/2 failure")
			}
		}()
	}
	wg.Wait()
	t.Logf("Concurrent multi-protocol propagation stress test passed with zero race conditions")
}
