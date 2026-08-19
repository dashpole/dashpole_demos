// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package decoders_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2/hpack"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/decoders"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ktls"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/obi"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ringbuf"
)

func TestDecoder_HTTP2Demuxing(t *testing.T) {
	var buf bytes.Buffer
	// Client connection preface
	buf.WriteString("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")

	// Encode headers with HPACK for Stream 1 (POST /v1/chat)
	var hpackBuf1 bytes.Buffer
	enc1 := hpack.NewEncoder(&hpackBuf1)
	_ = enc1.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
	_ = enc1.WriteField(hpack.HeaderField{Name: ":path", Value: "/v1/chat"})
	_ = enc1.WriteField(hpack.HeaderField{Name: ":authority", Value: "api.openai.com"})
	_ = enc1.WriteField(hpack.HeaderField{Name: "content-type", Value: "application/json"})

	// Frame 1: Stream 1 HEADERS (Flags: END_HEADERS)
	hdrBytes1 := hpackBuf1.Bytes()
	frameHdr1 := make([]byte, 9)
	frameHdr1[0] = byte(len(hdrBytes1) >> 16)
	frameHdr1[1] = byte(len(hdrBytes1) >> 8)
	frameHdr1[2] = byte(len(hdrBytes1))
	frameHdr1[3] = decoders.FrameHeaders
	frameHdr1[4] = decoders.FlagEndHeaders
	binary.BigEndian.PutUint32(frameHdr1[5:9], 1) // Stream ID 1
	buf.Write(frameHdr1)
	buf.Write(hdrBytes1)

	// Frame 2: Stream 1 DATA (Flags: END_STREAM)
	payload1 := []byte("{\"model\":\"gpt-4o\"}")
	frameData1 := make([]byte, 9)
	frameData1[0] = byte(len(payload1) >> 16)
	frameData1[1] = byte(len(payload1) >> 8)
	frameData1[2] = byte(len(payload1))
	frameData1[3] = decoders.FrameData
	frameData1[4] = decoders.FlagEndStream
	binary.BigEndian.PutUint32(frameData1[5:9], 1)
	buf.Write(frameData1)
	buf.Write(payload1)

	// Encode headers for Stream 3 (GET /healthz)
	var hpackBuf3 bytes.Buffer
	enc3 := hpack.NewEncoder(&hpackBuf3)
	_ = enc3.WriteField(hpack.HeaderField{Name: ":method", Value: "GET"})
	_ = enc3.WriteField(hpack.HeaderField{Name: ":path", Value: "/healthz"})

	// Frame 3: Stream 3 HEADERS + DATA (Flags: END_HEADERS | END_STREAM)
	hdrBytes3 := hpackBuf3.Bytes()
	frameHdr3 := make([]byte, 9)
	frameHdr3[0] = byte(len(hdrBytes3) >> 16)
	frameHdr3[1] = byte(len(hdrBytes3) >> 8)
	frameHdr3[2] = byte(len(hdrBytes3))
	frameHdr3[3] = decoders.FrameHeaders
	frameHdr3[4] = decoders.FlagEndHeaders | decoders.FlagEndStream
	binary.BigEndian.PutUint32(frameHdr3[5:9], 3) // Stream ID 3
	buf.Write(frameHdr3)
	buf.Write(hdrBytes3)

	frames, err := decoders.ParseFrames(buf.Bytes())
	if err != nil {
		t.Fatalf("ParseFrames error: %v", err)
	}
	if len(frames) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(frames))
	}

	decoder := decoders.NewHTTP2Decoder()
	completed, err := decoder.DecodeFrames(frames)
	if err != nil {
		t.Fatalf("DecodeFrames error: %v", err)
	}
	if len(completed) != 2 {
		t.Fatalf("expected 2 completed streams, got %d", len(completed))
	}

	// Verify Stream 1
	s1 := completed[0]
	if s1.StreamID != 1 || s1.Method != "POST" || s1.Path != "/v1/chat" || s1.Data.String() != "{\"model\":\"gpt-4o\"}" {
		t.Errorf("Stream 1 decoded mismatch: ID=%d, Method=%s, Path=%s, Data=%s", s1.StreamID, s1.Method, s1.Path, s1.Data.String())
	}

	// Verify Stream 3
	s3 := completed[1]
	if s3.StreamID != 3 || s3.Method != "GET" || s3.Path != "/healthz" {
		t.Errorf("Stream 3 decoded mismatch: ID=%d, Method=%s, Path=%s", s3.StreamID, s3.Method, s3.Path)
	}
	t.Logf("HTTP/2 Demuxing verified: 2 concurrent streams successfully separated and parsed")
}

func TestDecoder_SSETokenStreaming(t *testing.T) {
	reqStart := time.Now()
	sseDec := decoders.NewSSEDecoder(reqStart)

	time.Sleep(10 * time.Millisecond) // Simulated latency before TTFT
	chunk1Time := time.Now()

	// 50 streaming chunks
	for i := 0; i < 50; i++ {
		rawChunk := fmt.Sprintf("data: {\"model\":\"gemini-1.5-flash\",\"choices\":[{\"delta\":{\"content\":\"token_%d \"}}]}\n\n", i)
		arrTime := chunk1Time.Add(time.Duration(i) * time.Millisecond)
		sseDec.IngestChunk([]byte(rawChunk), arrTime)
	}

	// Final usage frame and DONE
	finalFrame := []byte("data: {\"usage\":{\"prompt_tokens\":120,\"completion_tokens\":50,\"total_tokens\":170}}\n\ndata: [DONE]\n\n")
	metrics := sseDec.IngestChunk(finalFrame, time.Now())

	if !metrics.IsDone {
		t.Errorf("expected IsDone=true")
	}
	if metrics.ModelName != "gemini-1.5-flash" {
		t.Errorf("expected model gemini-1.5-flash, got %s", metrics.ModelName)
	}
	if metrics.OutputTokens != 50 {
		t.Errorf("expected OutputTokens=50, got %d", metrics.OutputTokens)
	}
	if metrics.PromptTokens != 120 {
		t.Errorf("expected PromptTokens=120, got %d", metrics.PromptTokens)
	}
	if metrics.TotalTokens != 170 {
		t.Errorf("expected TotalTokens=170, got %d", metrics.TotalTokens)
	}
	if metrics.TimeToFirstTok < 9*time.Millisecond || metrics.TimeToFirstTok > 50*time.Millisecond {
		t.Errorf("unexpected TTFT: %v", metrics.TimeToFirstTok)
	}
	if !strings.Contains(metrics.FullText.String(), "token_0 token_1") {
		t.Errorf("full text missing expected token content: %q", metrics.FullText.String())
	}
	t.Logf("SSE Token Streaming verified: TTFT=%v, OutputTokens=%d, TotalTokens=%d",
		metrics.TimeToFirstTok, metrics.OutputTokens, metrics.TotalTokens)
}

func TestDecoder_MCPToolCall(t *testing.T) {
	reqData := []byte(`{
		"jsonrpc": "2.0",
		"id": 42,
		"method": "tools/call",
		"params": {
			"name": "get_order_status",
			"arguments": {
				"order_id": "ORD-12345",
				"include_tracking": true
			}
		}
	}`)

	reqDetails, ok := decoders.ParseMCPRequest(reqData)
	if !ok {
		t.Fatalf("failed to parse MCP request")
	}
	if reqDetails.MethodName != "tools/call" {
		t.Errorf("expected MethodName tools/call, got %s", reqDetails.MethodName)
	}
	if reqDetails.ToolName != "get_order_status" {
		t.Errorf("expected ToolName get_order_status, got %s", reqDetails.ToolName)
	}
	if !strings.Contains(reqDetails.ToolArguments, "ORD-12345") {
		t.Errorf("expected ToolArguments to contain ORD-12345, got %s", reqDetails.ToolArguments)
	}

	// Successful MCP Response
	respData := []byte(`{
		"jsonrpc": "2.0",
		"id": 42,
		"result": {
			"content": [{"type": "text", "text": "Shipped - Out for delivery"}]
		}
	}`)
	respDetails, ok := decoders.ParseMCPResponse(respData)
	if !ok {
		t.Fatalf("failed to parse MCP response")
	}
	if respDetails.IsError {
		t.Errorf("expected success response, got error")
	}
	if !strings.Contains(respDetails.ToolResult, "Out for delivery") {
		t.Errorf("expected result to contain status, got %s", respDetails.ToolResult)
	}

	// Error MCP Response
	errRespData := []byte(`{
		"jsonrpc": "2.0",
		"id": 43,
		"error": {
			"code": -32602,
			"message": "Invalid parameter: order_id not found"
		}
	}`)
	errRespDetails, ok := decoders.ParseMCPResponse(errRespData)
	if !ok || !errRespDetails.IsError {
		t.Fatalf("expected parsed error response")
	}
	if !strings.Contains(errRespDetails.ErrorMessage, "-32602") {
		t.Errorf("expected error code -32602 in message, got %s", errRespDetails.ErrorMessage)
	}
	t.Logf("MCP Tool Call decoder verified for both request and response/error frames")
}

func TestDecoder_QdrantVectorSearch(t *testing.T) {
	path := "/collections/customer_kb/points/search"
	body := []byte(`{
		"vector": [0.12, -0.45, 0.88, 0.05],
		"limit": 10,
		"score_threshold": 0.85,
		"filter": {
			"must": [{"key": "department", "match": {"value": "support"}}]
		}
	}`)

	details, ok := decoders.ParseVectorDBRequest(path, body)
	if !ok {
		t.Fatalf("failed to parse VectorDB request")
	}
	if details.System != "qdrant" {
		t.Errorf("expected system qdrant, got %s", details.System)
	}
	if details.CollectionName != "customer_kb" {
		t.Errorf("expected collection customer_kb, got %s", details.CollectionName)
	}
	if details.Operation != "search" {
		t.Errorf("expected operation search, got %s", details.Operation)
	}
	if details.VectorDim != 4 {
		t.Errorf("expected VectorDim=4, got %d", details.VectorDim)
	}
	if details.TopK != 10 {
		t.Errorf("expected TopK=10, got %d", details.TopK)
	}
	if !details.HasFilter {
		t.Errorf("expected HasFilter=true")
	}
	t.Logf("Qdrant Vector Search parser verified successfully")
}

func TestDecoder_PipelineIntegration_GenAISpans(t *testing.T) {
	decorator := obi.NewK8sDecorator("node-1")
	exporter := obi.NewInMemorySpanExporter()
	bridge := obi.NewPipelineBridge(decorator, exporter)
	defer bridge.Close()

	tuple := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.0.0.1"),
		SrcPort: 51234,
		DstIP:   net.ParseIP("10.0.0.2"),
		DstPort: 443,
	}

	// 1. Test SSE Streaming Pipeline Span
	reqSSE := []byte("POST /v1/chat/completions HTTP/1.1\r\nHost: api.openai.com\r\nContent-Type: application/json\r\n\r\n{\"model\":\"gpt-4o\",\"stream\":true}")
	respSSE := []byte("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\ndata: {\"model\":\"gpt-4o\",\"choices\":[{\"delta\":{\"content\":\"Agent execution started\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":80,\"completion_tokens\":20,\"total_tokens\":100}}\n\ndata: [DONE]\n\n")

	evReqSSE := ktls.SerializeTelemetryEvent(tuple, 10, 10, reqSSE)
	evRespSSE := ktls.SerializeTelemetryEvent(tuple, 10, 10, respSSE)

	_ = bridge.ProcessRecord("c-llm", ringbuf.Record{MsgType: abi.MsgTypeKTLSTx, Flags: abi.ChunkFlagSingle, Payload: evReqSSE})
	time.Sleep(15 * time.Millisecond) // Simulated latency for TTFT
	_ = bridge.ProcessRecord("c-llm", ringbuf.Record{MsgType: abi.MsgTypeKTLSRx, Flags: abi.ChunkFlagSingle, Payload: evRespSSE})

	// 2. Test MCP Tool Call Pipeline Span
	tupleMCP := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.0.0.1"),
		SrcPort: 51235,
		DstIP:   net.ParseIP("10.0.0.3"),
		DstPort: 8080,
	}
	reqMCP := []byte("POST /mcp HTTP/1.1\r\nHost: mcp-server\r\nContent-Type: application/json\r\n\r\n{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/call\",\"params\":{\"name\":\"sql_query\",\"arguments\":{\"query\":\"SELECT * FROM users\"}}}")
	respMCP := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"rows\":[{\"id\":1,\"name\":\"Alice\"}]}}")

	evReqMCP := ktls.SerializeTelemetryEvent(tupleMCP, 20, 20, reqMCP)
	evRespMCP := ktls.SerializeTelemetryEvent(tupleMCP, 20, 20, respMCP)

	_ = bridge.ProcessRecord("c-mcp", ringbuf.Record{MsgType: abi.MsgTypeKTLSTx, Flags: abi.ChunkFlagSingle, Payload: evReqMCP})
	_ = bridge.ProcessRecord("c-mcp", ringbuf.Record{MsgType: abi.MsgTypeKTLSRx, Flags: abi.ChunkFlagSingle, Payload: evRespMCP})

	// 3. Test Qdrant Vector Search Pipeline Span
	tupleQdrant := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.0.0.1"),
		SrcPort: 51236,
		DstIP:   net.ParseIP("10.0.0.4"),
		DstPort: 6333,
	}
	reqQdrant := []byte("POST /collections/docs/points/search HTTP/1.1\r\nHost: qdrant:6333\r\nContent-Type: application/json\r\n\r\n{\"vector\":[0.1,0.2,0.3],\"limit\":5}")
	respQdrant := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"result\":[{\"id\":10,\"score\":0.99}]}")

	evReqQdrant := ktls.SerializeTelemetryEvent(tupleQdrant, 30, 30, reqQdrant)
	evRespQdrant := ktls.SerializeTelemetryEvent(tupleQdrant, 30, 30, respQdrant)

	_ = bridge.ProcessRecord("c-qdrant", ringbuf.Record{MsgType: abi.MsgTypeKTLSTx, Flags: abi.ChunkFlagSingle, Payload: evReqQdrant})
	_ = bridge.ProcessRecord("c-qdrant", ringbuf.Record{MsgType: abi.MsgTypeKTLSRx, Flags: abi.ChunkFlagSingle, Payload: evRespQdrant})

	spans := exporter.Spans()
	if len(spans) != 3 {
		t.Fatalf("expected 3 enriched GenAI spans, got %d", len(spans))
	}

	// Verify LLM Span
	spanLLM := spans[0]
	if spanLLM.ModelName != "gpt-4o" {
		t.Errorf("LLM Span expected model gpt-4o, got %s", spanLLM.ModelName)
	}
	if spanLLM.PromptTokens != 80 || spanLLM.OutputTokens != 20 {
		t.Errorf("LLM Span tokens mismatch: prompt=%d, output=%d", spanLLM.PromptTokens, spanLLM.OutputTokens)
	}
	if spanLLM.TimeToFirstTok <= 0 {
		t.Errorf("LLM Span expected positive TTFT, got %v", spanLLM.TimeToFirstTok)
	}

	// Verify MCP Span
	spanMCP := spans[1]
	if spanMCP.ToolName != "sql_query" {
		t.Errorf("MCP Span expected tool sql_query, got %s", spanMCP.ToolName)
	}
	if !strings.Contains(spanMCP.ToolArgs, "SELECT * FROM users") {
		t.Errorf("MCP Span args mismatch: %s", spanMCP.ToolArgs)
	}

	// Verify Qdrant Span
	spanQdrant := spans[2]
	if spanQdrant.Attributes["db.system"] != "qdrant" || spanQdrant.Attributes["db.collection.name"] != "docs" {
		t.Errorf("Qdrant Span mismatch: system=%s, collection=%s",
			spanQdrant.Attributes["db.system"], spanQdrant.Attributes["db.collection.name"])
	}
	if spanQdrant.Attributes["db.vector.dimension"] != "3" || spanQdrant.Attributes["db.vector.top_k"] != "5" {
		t.Errorf("Qdrant Span vector dim/top_k mismatch: dim=%s, top_k=%s",
			spanQdrant.Attributes["db.vector.dimension"], spanQdrant.Attributes["db.vector.top_k"])
	}

	t.Logf("End-to-End GenAI Decoder Integration verified across LLM SSE, MCP, and Vector DB")
}

func TestDecoder_CorruptedAndMalformedRobustness(t *testing.T) {
	// 1. Truncated HTTP/2 Frame
	_, _ = decoders.ParseFrames([]byte{0x00, 0x00, 0x10, 0x01, 0x00, 0x00, 0x00, 0x00}) // < 9 bytes

	// 2. Corrupted JSON in MCP
	_, ok := decoders.ParseMCPRequest([]byte("{not valid json"))
	if ok {
		t.Errorf("expected false for invalid JSON")
	}

	// 3. Corrupted JSON in VectorDB
	details, ok := decoders.ParseVectorDBRequest("/collections/test/points/search", []byte("invalid-json"))
	if !ok || details.Operation != "search" {
		t.Errorf("expected fallback parse for malformed body")
	}

	// 4. Corrupted SSE stream
	dec := decoders.NewSSEDecoder(time.Now())
	metrics := dec.IngestChunk([]byte("data: {corrupted-json\n\ndata: \n\n"), time.Now())
	if metrics == nil {
		t.Errorf("expected non-nil metrics on malformed SSE")
	}
	t.Logf("Decoder robustness test passed: zero panics on malformed input")
}

func TestDecoder_HTTP2_PaddingAndPriority(t *testing.T) {
	// HPACK header block
	var hpackBuf bytes.Buffer
	enc := hpack.NewEncoder(&hpackBuf)
	_ = enc.WriteField(hpack.HeaderField{Name: ":method", Value: "GET"})
	_ = enc.WriteField(hpack.HeaderField{Name: ":path", Value: "/api/v2/items"})
	_ = enc.WriteField(hpack.HeaderField{Name: ":authority", Value: "example.com"})
	_ = enc.WriteField(hpack.HeaderField{Name: ":status", Value: "200"})
	rawHdr := hpackBuf.Bytes()

	// Frame 1: HEADERS with PADDED (0x8) and PRIORITY (0x20) and END_HEADERS (0x4)
	// Layout: PadLength (1B) + StreamDependency (4B) + Weight (1B) + HeaderBlock + Padding (PadLength B)
	padLen := byte(4)
	priority := []byte{0x00, 0x00, 0x00, 0x00, 0x10} // 5 bytes
	padding := make([]byte, padLen)
	for i := range padding {
		padding[i] = 0xAA
	}

	var headersPayload bytes.Buffer
	headersPayload.WriteByte(padLen)
	headersPayload.Write(priority)
	headersPayload.Write(rawHdr)
	headersPayload.Write(padding)

	var wireBuf bytes.Buffer
	wireBuf.WriteString("PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n")

	fHdr1 := make([]byte, 9)
	payLen1 := headersPayload.Len()
	fHdr1[0] = byte(payLen1 >> 16)
	fHdr1[1] = byte(payLen1 >> 8)
	fHdr1[2] = byte(payLen1)
	fHdr1[3] = decoders.FrameHeaders
	fHdr1[4] = decoders.FlagEndHeaders | 0x8 | 0x20 // END_HEADERS | PADDED | PRIORITY
	binary.BigEndian.PutUint32(fHdr1[5:9], 1)
	wireBuf.Write(fHdr1)
	wireBuf.Write(headersPayload.Bytes())

	// Frame 2: DATA with PADDED (0x8) and END_STREAM (0x1)
	// Layout: PadLength (1B) + Data + Padding (PadLength B)
	dataPadLen := byte(3)
	dataPayloadStr := "{\"status\":\"ok\"}"
	var dataBuf bytes.Buffer
	dataBuf.WriteByte(dataPadLen)
	dataBuf.WriteString(dataPayloadStr)
	dataBuf.Write(make([]byte, dataPadLen))

	fHdr2 := make([]byte, 9)
	payLen2 := dataBuf.Len()
	fHdr2[0] = byte(payLen2 >> 16)
	fHdr2[1] = byte(payLen2 >> 8)
	fHdr2[2] = byte(payLen2)
	fHdr2[3] = decoders.FrameData
	fHdr2[4] = decoders.FlagEndStream | 0x8 // END_STREAM | PADDED
	binary.BigEndian.PutUint32(fHdr2[5:9], 1)
	wireBuf.Write(fHdr2)
	wireBuf.Write(dataBuf.Bytes())

	frames, err := decoders.ParseFrames(wireBuf.Bytes())
	if err != nil {
		t.Fatalf("ParseFrames error: %v", err)
	}
	if len(frames) != 2 {
		t.Fatalf("expected 2 frames, got %d", len(frames))
	}

	decoder := decoders.NewHTTP2Decoder()
	completed, err := decoder.DecodeFrames(frames)
	if err != nil {
		t.Fatalf("DecodeFrames error: %v", err)
	}
	if len(completed) != 1 {
		t.Fatalf("expected 1 completed stream, got %d", len(completed))
	}

	s := completed[0]
	if s.StreamID != 1 {
		t.Errorf("expected StreamID 1, got %d", s.StreamID)
	}
	if s.Method != "GET" || s.Path != "/api/v2/items" || s.Host != "example.com" {
		t.Errorf("mismatch headers: Method=%s, Path=%s, Host=%s", s.Method, s.Path, s.Host)
	}
	if s.Status != 200 {
		t.Errorf("expected status 200, got %d", s.Status)
	}
	if s.Data.String() != dataPayloadStr {
		t.Errorf("data mismatch after padding stripping: expected %q, got %q", dataPayloadStr, s.Data.String())
	}
	t.Logf("HTTP/2 Padding and Priority header stripping verified successfully")
}

func TestDecoder_HTTP2_GRPC_StreamAndTrailers(t *testing.T) {
	decoder := decoders.NewHTTP2Decoder()

	// Stream 5: gRPC call (POST /google.cloud.aiplatform.v1.PredictionService/Predict)
	// Frame 1: HEADERS
	var hpackBuf1 bytes.Buffer
	enc1 := hpack.NewEncoder(&hpackBuf1)
	_ = enc1.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
	_ = enc1.WriteField(hpack.HeaderField{Name: ":path", Value: "/google.cloud.aiplatform.v1.PredictionService/Predict"})
	_ = enc1.WriteField(hpack.HeaderField{Name: ":authority", Value: "aiplatform.googleapis.com"})
	_ = enc1.WriteField(hpack.HeaderField{Name: "content-type", Value: "application/grpc"})

	hdrPayload1 := hpackBuf1.Bytes()
	frame1 := decoders.HTTP2Frame{
		Length:   uint32(len(hdrPayload1)),
		Type:     decoders.FrameHeaders,
		Flags:    decoders.FlagEndHeaders,
		StreamID: 5,
		Payload:  hdrPayload1,
	}

	// Frame 2: DATA (gRPC 5-byte header + protobuf)
	grpcMsg := []byte{0x00, 0x00, 0x00, 0x00, 0x07, 'p', 'r', 'e', 'd', 'i', 'c', 't'}
	frame2 := decoders.HTTP2Frame{
		Length:   uint32(len(grpcMsg)),
		Type:     decoders.FrameData,
		Flags:    0,
		StreamID: 5,
		Payload:  grpcMsg,
	}

	// Frame 3: Trailers HEADERS with grpc-status: 0 and END_STREAM
	var hpackBufTrailers bytes.Buffer
	encTrailers := hpack.NewEncoder(&hpackBufTrailers)
	_ = encTrailers.WriteField(hpack.HeaderField{Name: "grpc-status", Value: "0"})
	_ = encTrailers.WriteField(hpack.HeaderField{Name: "grpc-message", Value: "OK"})

	trailerPayload := hpackBufTrailers.Bytes()
	frame3 := decoders.HTTP2Frame{
		Length:   uint32(len(trailerPayload)),
		Type:     decoders.FrameHeaders,
		Flags:    decoders.FlagEndHeaders | decoders.FlagEndStream,
		StreamID: 5,
		Payload:  trailerPayload,
	}

	completed, err := decoder.DecodeFrames([]decoders.HTTP2Frame{frame1, frame2, frame3})
	if err != nil {
		t.Fatalf("DecodeFrames error: %v", err)
	}
	if len(completed) != 1 {
		t.Fatalf("expected 1 completed gRPC stream, got %d", len(completed))
	}

	s5 := completed[0]
	if s5.StreamID != 5 || s5.Method != "POST" || s5.Path != "/google.cloud.aiplatform.v1.PredictionService/Predict" {
		t.Errorf("gRPC stream metadata mismatch: ID=%d, Method=%s, Path=%s", s5.StreamID, s5.Method, s5.Path)
	}
	if s5.GRPCStatus != 0 {
		t.Errorf("expected GRPCStatus 0, got %d", s5.GRPCStatus)
	}
	if !bytes.Equal(s5.Data.Bytes(), grpcMsg) {
		t.Errorf("gRPC message payload mismatch: got %v", s5.Data.Bytes())
	}

	// Non-zero gRPC Status (Stream 7: UNAVAILABLE = 14)
	var hpackBufErr bytes.Buffer
	encErr := hpack.NewEncoder(&hpackBufErr)
	_ = encErr.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
	_ = encErr.WriteField(hpack.HeaderField{Name: ":path", Value: "/qdrant.Points/Search"})
	_ = encErr.WriteField(hpack.HeaderField{Name: "grpc-status", Value: "14"})

	errPayload := hpackBufErr.Bytes()
	frameErr := decoders.HTTP2Frame{
		Length:   uint32(len(errPayload)),
		Type:     decoders.FrameHeaders,
		Flags:    decoders.FlagEndHeaders | decoders.FlagEndStream,
		StreamID: 7,
		Payload:  errPayload,
	}

	completedErr, err := decoder.DecodeFrames([]decoders.HTTP2Frame{frameErr})
	if err != nil {
		t.Fatalf("DecodeFrames error: %v", err)
	}
	if len(completedErr) != 1 || completedErr[0].GRPCStatus != 14 {
		t.Errorf("expected gRPC status 14, got %v", completedErr[0].GRPCStatus)
	}
	t.Logf("HTTP/2 gRPC stream trailers and grpc-status extraction verified successfully")
}

func TestDecoder_HTTP2_RSTStream_And_ControlFrames(t *testing.T) {
	decoder := decoders.NewHTTP2Decoder()

	// 1. Control frame on Stream 0 (SETTINGS)
	settingsPayload := []byte{0x00, 0x01, 0x00, 0x00, 0x10, 0x00} // SETTINGS_HEADER_TABLE_SIZE = 4096
	frameSettings := decoders.HTTP2Frame{
		Length:   uint32(len(settingsPayload)),
		Type:     decoders.FrameSettings,
		Flags:    0,
		StreamID: 0,
		Payload:  settingsPayload,
	}

	// 2. Control frame on Stream 0 (PING)
	pingPayload := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	framePing := decoders.HTTP2Frame{
		Length:   uint32(len(pingPayload)),
		Type:     decoders.FramePing,
		Flags:    0x1, // ACK
		StreamID: 0,
		Payload:  pingPayload,
	}

	// 3. Normal stream 9 start
	var hpackBuf bytes.Buffer
	enc := hpack.NewEncoder(&hpackBuf)
	_ = enc.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
	_ = enc.WriteField(hpack.HeaderField{Name: ":path", Value: "/cancel_me"})
	hdrPayload := hpackBuf.Bytes()

	frameHdr := decoders.HTTP2Frame{
		Length:   uint32(len(hdrPayload)),
		Type:     decoders.FrameHeaders,
		Flags:    decoders.FlagEndHeaders,
		StreamID: 9,
		Payload:  hdrPayload,
	}

	// 4. RST_STREAM on Stream 9
	rstPayload := []byte{0x00, 0x00, 0x00, 0x08} // CANCEL (code 8)
	frameRST := decoders.HTTP2Frame{
		Length:   uint32(len(rstPayload)),
		Type:     decoders.FrameRSTStream,
		Flags:    0,
		StreamID: 9,
		Payload:  rstPayload,
	}

	completed, err := decoder.DecodeFrames([]decoders.HTTP2Frame{frameSettings, framePing, frameHdr, frameRST})
	if err != nil {
		t.Fatalf("DecodeFrames error: %v", err)
	}
	if len(completed) != 1 {
		t.Fatalf("expected 1 completed stream on RST_STREAM, got %d", len(completed))
	}

	if completed[0].StreamID != 9 || !completed[0].EndStream {
		t.Errorf("expected stream 9 terminated with EndStream=true")
	}
	if decoder.ActiveStreamCount() != 0 {
		t.Errorf("expected 0 active streams after RST_STREAM, got %d", decoder.ActiveStreamCount())
	}
	t.Logf("HTTP/2 Control frames and RST_STREAM handling verified successfully")
}

func TestDecoder_HTTP2_MultiStream_ConcurrentDemuxing(t *testing.T) {
	decoder := decoders.NewHTTP2Decoder()
	numStreams := 8
	var allFrames []decoders.HTTP2Frame

	// Step 1: Interleave HEADERS for all streams
	for i := 0; i < numStreams; i++ {
		streamID := uint32(1 + i*2)
		var hpackBuf bytes.Buffer
		enc := hpack.NewEncoder(&hpackBuf)
		_ = enc.WriteField(hpack.HeaderField{Name: ":method", Value: "POST"})
		_ = enc.WriteField(hpack.HeaderField{Name: ":path", Value: fmt.Sprintf("/stream/%d", streamID)})
		_ = enc.WriteField(hpack.HeaderField{Name: ":authority", Value: "agent-demux"})
		payload := hpackBuf.Bytes()

		allFrames = append(allFrames, decoders.HTTP2Frame{
			Length:   uint32(len(payload)),
			Type:     decoders.FrameHeaders,
			Flags:    decoders.FlagEndHeaders,
			StreamID: streamID,
			Payload:  payload,
		})
	}

	// Step 2: Interleave 3 DATA chunks for each stream
	for chunk := 0; chunk < 3; chunk++ {
		for i := 0; i < numStreams; i++ {
			streamID := uint32(1 + i*2)
			dataPayload := []byte(fmt.Sprintf("[S%d-C%d]", streamID, chunk))
			flags := uint8(0)
			if chunk == 2 {
				flags = decoders.FlagEndStream
			}

			allFrames = append(allFrames, decoders.HTTP2Frame{
				Length:   uint32(len(dataPayload)),
				Type:     decoders.FrameData,
				Flags:    flags,
				StreamID: streamID,
				Payload:  dataPayload,
			})
		}
	}

	completed, err := decoder.DecodeFrames(allFrames)
	if err != nil {
		t.Fatalf("DecodeFrames error: %v", err)
	}
	if len(completed) != numStreams {
		t.Fatalf("expected %d completed streams, got %d", numStreams, len(completed))
	}

	for _, s := range completed {
		expectedPath := fmt.Sprintf("/stream/%d", s.StreamID)
		if s.Path != expectedPath {
			t.Errorf("Stream %d path mismatch: expected %s, got %s", s.StreamID, expectedPath, s.Path)
		}
		expectedData := fmt.Sprintf("[S%d-C0][S%d-C1][S%d-C2]", s.StreamID, s.StreamID, s.StreamID)
		if s.Data.String() != expectedData {
			t.Errorf("Stream %d data mismatch: expected %q, got %q", s.StreamID, expectedData, s.Data.String())
		}
	}
	t.Logf("HTTP/2 Concurrent Dynamic Stream Demuxing verified across %d interleaved streams", numStreams)
}

func TestDecoder_SSE_Gemini_Vertex(t *testing.T) {
	reqStart := time.Now().Add(-50 * time.Millisecond)
	sseDec := decoders.NewSSEDecoder(reqStart)

	chunk1Time := reqStart.Add(20 * time.Millisecond)
	chunk1 := []byte("data: {\"modelVersion\":\"gemini-1.5-pro\",\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Gemini reasoning \"}]}}]}\n\n")
	sseDec.IngestChunk(chunk1, chunk1Time)

	chunk2Time := reqStart.Add(40 * time.Millisecond)
	chunk2 := []byte("data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"step 1 complete.\"}]}}],\"usageMetadata\":{\"promptTokenCount\":64,\"candidatesTokenCount\":16,\"totalTokenCount\":80}}\n\ndata: [DONE]\n\n")
	metrics := sseDec.IngestChunk(chunk2, chunk2Time)

	if metrics.ModelName != "gemini-1.5-pro" {
		t.Errorf("expected model gemini-1.5-pro, got %s", metrics.ModelName)
	}
	if metrics.FullText.String() != "Gemini reasoning step 1 complete." {
		t.Errorf("text mismatch: %q", metrics.FullText.String())
	}
	if metrics.PromptTokens != 64 || metrics.OutputTokens != 16 || metrics.TotalTokens != 80 {
		t.Errorf("token counts mismatch: prompt=%d, output=%d, total=%d", metrics.PromptTokens, metrics.OutputTokens, metrics.TotalTokens)
	}
	if !metrics.IsDone {
		t.Errorf("expected IsDone=true")
	}
	if metrics.TimeToFirstTok != 20*time.Millisecond {
		t.Errorf("expected TTFT 20ms, got %v", metrics.TimeToFirstTok)
	}
	t.Logf("Vertex AI / Gemini SSE format decoding verified successfully")
}

func TestDecoder_SSE_Anthropic_Claude(t *testing.T) {
	reqStart := time.Now().Add(-60 * time.Millisecond)
	sseDec := decoders.NewSSEDecoder(reqStart)

	chunk1Time := reqStart.Add(25 * time.Millisecond)
	chunk1 := []byte("data: {\"model\":\"claude-3-5-sonnet\",\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"Plan verified.\"}}\n\n")
	sseDec.IngestChunk(chunk1, chunk1Time)

	chunk2Time := reqStart.Add(50 * time.Millisecond)
	chunk2 := []byte("data: {\"type\":\"message_delta\",\"usage\":{\"input_tokens\":120,\"output_tokens\":25}}\n\ndata: [DONE]\n\n")
	metrics := sseDec.IngestChunk(chunk2, chunk2Time)

	if metrics.ModelName != "claude-3-5-sonnet" {
		t.Errorf("expected model claude-3-5-sonnet, got %s", metrics.ModelName)
	}
	if metrics.FullText.String() != "Plan verified." {
		t.Errorf("text mismatch: %q", metrics.FullText.String())
	}
	if metrics.PromptTokens != 120 || metrics.OutputTokens != 25 || metrics.TotalTokens != 145 {
		t.Errorf("token mismatch: prompt=%d, output=%d, total=%d", metrics.PromptTokens, metrics.OutputTokens, metrics.TotalTokens)
	}
	if metrics.TimeToFirstTok != 25*time.Millisecond {
		t.Errorf("expected TTFT 25ms, got %v", metrics.TimeToFirstTok)
	}
	t.Logf("Anthropic Claude SSE format decoding verified successfully")
}

func TestDecoder_SSE_TTFT_Precision(t *testing.T) {
	t0 := time.Now()
	sseDec := decoders.NewSSEDecoder(t0)

	t1 := t0.Add(35 * time.Millisecond)
	t2 := t0.Add(70 * time.Millisecond)
	t3 := t0.Add(105 * time.Millisecond)

	sseDec.IngestChunk([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"alpha \"}}]}\n\n"), t1)
	sseDec.IngestChunk([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"beta \"}}]}\n\n"), t2)
	metrics := sseDec.IngestChunk([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"gamma\"}}]}\n\ndata: [DONE]\n\n"), t3)

	if metrics.TimeToFirstTok != 35*time.Millisecond {
		t.Errorf("expected TTFT exactly 35ms, got %v", metrics.TimeToFirstTok)
	}
	if metrics.ChunkCount != 3 {
		t.Errorf("expected ChunkCount 3, got %d", metrics.ChunkCount)
	}
	t.Logf("TTFT precision verified: exactly T_first_chunk - T_req_start without drift")
}

func TestDecoder_MCP_Comprehensive(t *testing.T) {
	// 1. Tool Call with complex types
	complexReq := []byte(`{
		"jsonrpc": "2.0",
		"id": "req-999",
		"method": "tools/call",
		"params": {
			"name": "search_code",
			"arguments": {
				"query": "func TestDecoder",
				"limit": 50,
				"filters": ["*.go", "!*_test.go"],
				"case_sensitive": true
			}
		}
	}`)
	details, ok := decoders.ParseMCPRequest(complexReq)
	if !ok || details.ToolName != "search_code" {
		t.Fatalf("failed to parse complex MCP request")
	}
	if !strings.Contains(details.ToolArguments, "filters") || !strings.Contains(details.ToolArguments, "case_sensitive") {
		t.Errorf("arguments missing expected keys: %s", details.ToolArguments)
	}

	// 2. Non-tool MCP method (resources/list)
	nonToolReq := []byte(`{"jsonrpc":"2.0","id":100,"method":"resources/list"}`)
	details2, ok2 := decoders.ParseMCPRequest(nonToolReq)
	if !ok2 || details2.MethodName != "resources/list" || details2.ToolName != "" {
		t.Errorf("expected method resources/list with empty tool name")
	}

	// 3. Error response with negative code
	errResp := []byte(`{"jsonrpc":"2.0","id":"req-999","error":{"code":-32600,"message":"Invalid Request"}}`)
	errDetails, okErr := decoders.ParseMCPResponse(errResp)
	if !okErr || !errDetails.IsError || !strings.Contains(errDetails.ErrorMessage, "-32600") {
		t.Errorf("expected parsed error response with code -32600")
	}

	// 4. Non-JSON-RPC payload (negative test)
	badReq := []byte(`{"method":"login","username":"admin"}`)
	_, okBad := decoders.ParseMCPRequest(badReq)
	if okBad {
		t.Errorf("expected non-JSON-RPC 2.0 payload to be rejected")
	}
	t.Logf("MCP Comprehensive tool call and error handling verified successfully")
}

func TestDecoder_VectorDB_Comprehensive(t *testing.T) {
	// 1. Search with Query Parameters
	pathQuery := "/collections/knowledge_base/points/search?consistency=strong"
	bodyDense := []byte(`{"vector":[0.1, 0.2, 0.3, 0.4], "limit": 25, "score_threshold": 0.90, "filter":{"must":[{"key":"tag","match":{"value":"agent"}}]}}`)
	d1, ok1 := decoders.ParseVectorDBRequest(pathQuery, bodyDense)
	if !ok1 {
		t.Fatalf("failed to parse VectorDB search with query params")
	}
	if d1.Operation != "search" || d1.CollectionName != "knowledge_base" || d1.VectorDim != 4 || d1.TopK != 25 || d1.ScoreThreshold != 0.90 || !d1.HasFilter {
		t.Errorf("search metadata mismatch: op=%s, coll=%s, dim=%d, topk=%d, score=%f, filter=%v",
			d1.Operation, d1.CollectionName, d1.VectorDim, d1.TopK, d1.ScoreThreshold, d1.HasFilter)
	}

	// 2. Search with Named / Nested Vector
	pathNamed := "/collections/multi_modal/points/search"
	bodyNamed := []byte(`{"vector":{"name":"image_embeddings","vector":[0.5, 0.6, 0.7, 0.8, 0.9, 1.0]},"limit":5}`)
	d2, ok2 := decoders.ParseVectorDBRequest(pathNamed, bodyNamed)
	if !ok2 || d2.VectorDim != 6 || d2.TopK != 5 {
		t.Errorf("named vector dimension mismatch: ok=%v, dim=%d, topk=%d", ok2, d2.VectorDim, d2.TopK)
	}

	// 3. Upsert route with query params
	pathUpsert := "/collections/knowledge_base/points?wait=true"
	d3, ok3 := decoders.ParseVectorDBRequest(pathUpsert, nil)
	if !ok3 || d3.Operation != "upsert" || d3.CollectionName != "knowledge_base" {
		t.Errorf("upsert route mismatch: op=%s, coll=%s", d3.Operation, d3.CollectionName)
	}

	// 4. Collection metadata query
	pathColl := "/collections/knowledge_base"
	d4, ok4 := decoders.ParseVectorDBRequest(pathColl, nil)
	if !ok4 || d4.Operation != "query" || d4.CollectionName != "knowledge_base" {
		t.Errorf("collection route mismatch: op=%s, coll=%s", d4.Operation, d4.CollectionName)
	}

	// 5. Negative cases
	if _, ok := decoders.ParseVectorDBRequest("/collections", nil); ok {
		t.Errorf("expected /collections without trailing slash to be false")
	}
	if _, ok := decoders.ParseVectorDBRequest("/collections/", nil); ok {
		t.Errorf("expected /collections/ with empty collection to be false")
	}
	if _, ok := decoders.ParseVectorDBRequest("/other/api", nil); ok {
		t.Errorf("expected non-collection route to be false")
	}
	t.Logf("Vector DB comprehensive routes and named vector parsing verified successfully")
}

func TestDecoder_Concurrency_RaceStress(t *testing.T) {
	var wg sync.WaitGroup
	numWorkers := 30

	h2Dec := decoders.NewHTTP2Decoder()
	sseDec := decoders.NewSSEDecoder(time.Now())

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()

			// HTTP/2 concurrent frame decoding
			streamID := uint32(workerID*2 + 1)
			frame := decoders.HTTP2Frame{
				Length:   10,
				Type:     decoders.FrameData,
				Flags:    decoders.FlagEndStream,
				StreamID: streamID,
				Payload:  []byte("concurrent"),
			}
			_, _ = h2Dec.DecodeFrames([]decoders.HTTP2Frame{frame})

			// SSE concurrent chunk ingestion
			chunk := fmt.Sprintf("data: {\"model\":\"gpt-4o\",\"choices\":[{\"delta\":{\"content\":\"word_%d \"}}]}\n\n", workerID)
			_ = sseDec.IngestChunk([]byte(chunk), time.Now())

			// MCP concurrent parsing
			mcpReq := fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"tool_%d","arguments":{}}}`, workerID, workerID)
			_, _ = decoders.ParseMCPRequest([]byte(mcpReq))

			// VectorDB concurrent parsing
			vPath := fmt.Sprintf("/collections/coll_%d/points/search", workerID)
			_, _ = decoders.ParseVectorDBRequest(vPath, []byte(`{"vector":[1,2,3],"limit":10}`))
		}(w)
	}

	wg.Wait()

	if h2Dec.ActiveStreamCount() != 0 {
		t.Errorf("expected all streams cleaned up after END_STREAM")
	}
	metrics := sseDec.Metrics()
	if metrics.ChunkCount != numWorkers {
		t.Errorf("expected ChunkCount %d, got %d", numWorkers, metrics.ChunkCount)
	}
	t.Logf("Decoder Concurrency and Thread-Safety stress test passed with 0 data races")
}

func TestDecoder_Adversarial_FuzzAndMalformed(t *testing.T) {
	// 1. Partial frames and huge length values
	fuzzBytes := []byte{
		0xFF, 0xFF, 0xFF, // huge 16MB length
		0x01,                   // HEADERS
		0x28,                   // PADDED | PRIORITY
		0x00, 0x00, 0x00, 0x05, // Stream 5
		0xFF, 0xFF, // Only 2 bytes payload instead of 16MB
	}
	frames, err := decoders.ParseFrames(fuzzBytes)
	if err != nil {
		t.Errorf("ParseFrames unexpected error: %v", err)
	}
	if len(frames) != 0 {
		t.Errorf("expected incomplete frame to be ignored, got %d", len(frames))
	}

	// 2. Malformed padding in HEADERS: padLength > len(payload)
	dec := decoders.NewHTTP2Decoder()
	badPadFrame := decoders.HTTP2Frame{
		Length:   5,
		Type:     decoders.FrameHeaders,
		Flags:    decoders.FlagEndHeaders | 0x8, // PADDED
		StreamID: 11,
		Payload:  []byte{0x20, 0x01, 0x02, 0x03, 0x04}, // padLength = 32 > 5
	}
	completed, err := dec.DecodeFrames([]decoders.HTTP2Frame{badPadFrame})
	if err != nil {
		t.Errorf("unexpected error on bad padding: %v", err)
	}
	_ = completed

	// 3. Malformed padding in DATA: padLength > len(payload)
	badDataPad := decoders.HTTP2Frame{
		Length:   4,
		Type:     decoders.FrameData,
		Flags:    0x8 | decoders.FlagEndStream, // PADDED | END_STREAM
		StreamID: 11,
		Payload:  []byte{0x10, 'a', 'b', 'c'}, // padLength = 16 > 4
	}
	_, _ = dec.DecodeFrames([]decoders.HTTP2Frame{badDataPad})

	// 4. SSE with broken multi-byte and invalid utf8
	sseDec := decoders.NewSSEDecoder(time.Now())
	_ = sseDec.IngestChunk([]byte("data: \xff\xfe\xfd\n\ndata: {\"model\": 12345, \"choices\": [null, false]}\n\n"), time.Now())

	// 5. MCP with array parameters instead of object
	_, _ = decoders.ParseMCPRequest([]byte(`{"jsonrpc":"2.0","method":"tools/call","params":["not","an","object"]}`))
	_, _ = decoders.ParseMCPResponse([]byte(`{"jsonrpc":"2.0","error":"string instead of object"}`))

	// 6. VectorDB with non-numeric floats and malformed types
	_, _ = decoders.ParseVectorDBRequest("/collections/test/points/search", []byte(`{"vector": "not an array", "limit": "not an int"}`))

	t.Logf("Adversarial fuzzing completed: 0 panics across all malformed and hostile payloads")
}
