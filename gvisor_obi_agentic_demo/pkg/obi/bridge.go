// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package obi

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/decoders"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ktls"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/propagation"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ringbuf"
)

// ConnKey identifies a bidirectional TCP connection within a container sandbox.
type ConnKey struct {
	ContainerID string
	SrcIP       string
	SrcPort     uint16
	DstIP       string
	DstPort     uint16
}

func (k ConnKey) String() string {
	return fmt.Sprintf("%s:%s:%d->%s:%d", k.ContainerID, k.SrcIP, k.SrcPort, k.DstIP, k.DstPort)
}

// InFlightTxRx tracks a request/response transaction.
type InFlightTxRx struct {
	Span      *TraceSpan
	CreatedAt time.Time
}

// SpanExporter interface for exporting completed spans.
type SpanExporter interface {
	ExportSpan(span *TraceSpan) error
}

// PipelineBridge converts gVisor ring buffer events into OpenTelemetry spans.
type PipelineBridge struct {
	mu           sync.Mutex
	inFlight     map[ConnKey]*InFlightTxRx
	decorator    *K8sDecorator
	exporter     SpanExporter
	reassembler  *ChunkReassembler
	extractor    *propagation.ContextExtractor
	injector     *propagation.ContextInjector
	spansEmitted atomic.Uint64
	inFlightTTL  time.Duration
	stopCh       chan struct{}
	cleanupWg    sync.WaitGroup
}

// NewPipelineBridge creates a new PipelineBridge instance with background TTL cleanup.
func NewPipelineBridge(decorator *K8sDecorator, exporter SpanExporter) *PipelineBridge {
	pb := &PipelineBridge{
		inFlight:    make(map[ConnKey]*InFlightTxRx),
		decorator:   decorator,
		exporter:    exporter,
		reassembler: NewChunkReassembler(),
		extractor:   propagation.NewContextExtractor(),
		injector:    propagation.NewContextInjector(),
		inFlightTTL: 30 * time.Second,
		stopCh:       make(chan struct{}),
	}
	pb.cleanupWg.Add(1)
	go pb.backgroundCleaner()
	return pb
}

// Close gracefully stops the background cleanup worker.
func (pb *PipelineBridge) Close() {
	close(pb.stopCh)
	pb.cleanupWg.Wait()
}

// backgroundCleaner removes stale in-flight transactions periodically off the hot path.
func (pb *PipelineBridge) backgroundCleaner() {
	defer pb.cleanupWg.Done()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-pb.stopCh:
			return
		case <-ticker.C:
			pb.SweepStaleInFlight()
		}
	}
}

// SweepStaleInFlight removes transactions that exceeded the inFlightTTL.
func (pb *PipelineBridge) SweepStaleInFlight() {
	pb.mu.Lock()
	defer pb.mu.Unlock()

	now := time.Now()
	for k, inF := range pb.inFlight {
		if now.Sub(inF.CreatedAt) > pb.inFlightTTL {
			delete(pb.inFlight, k)
		}
	}
}

// ComputeStreamID calculates a collision-free 64-bit stream identifier.
func ComputeStreamID(containerID string, tuple ktls.SocketTuple, pid uint32) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(containerID))
	_, _ = h.Write(tuple.SrcIP)
	_ = binary.Write(h, binary.BigEndian, tuple.SrcPort)
	_, _ = h.Write(tuple.DstIP)
	_ = binary.Write(h, binary.BigEndian, tuple.DstPort)
	_ = binary.Write(h, binary.BigEndian, pid)
	return h.Sum64()
}

// ProcessRecord parses a single ring buffer record in O(1) time.
func (pb *PipelineBridge) ProcessRecord(containerID string, rec ringbuf.Record) error {
	tuple, pid, tid, tsNS, payload, ok := ktls.DeserializeTelemetryEvent(rec.Payload)
	if !ok {
		return fmt.Errorf("failed to deserialize telemetry event")
	}

	// Handle multi-chunk reassembly if chunk flags are set
	if rec.Flags != abi.ChunkFlagSingle {
		streamID := ComputeStreamID(containerID, tuple, pid)
		buf, done, err := pb.reassembler.IngestChunk(streamID, rec.Flags, payload)
		if err != nil {
			return err
		}
		if !done || buf == nil {
			return nil
		}
		payload = buf.Bytes()
	}

	eventTime := time.Unix(0, int64(tsNS))
	if tsNS == 0 {
		eventTime = time.Now()
	}

	key := ConnKey{
		ContainerID: containerID,
		SrcIP:       tuple.SrcIP.String(),
		SrcPort:     tuple.SrcPort,
		DstIP:       tuple.DstIP.String(),
		DstPort:     tuple.DstPort,
	}

	revKey := ConnKey{
		ContainerID: containerID,
		SrcIP:       tuple.DstIP.String(),
		SrcPort:     tuple.DstPort,
		DstIP:       tuple.SrcIP.String(),
		DstPort:     tuple.SrcPort,
	}

	pb.mu.Lock()
	defer pb.mu.Unlock()

	now := time.Now()

	switch rec.MsgType {
	case abi.MsgTypeKTLSTx:
		// Outbound write: Request or Response
		if isHTTPRequest(payload) {
			// Outbound client request
			span := NewTraceSpan(formatHTTPName(payload), SpanKindClient)
			span.StartTime = eventTime
			span.SrcIP = tuple.SrcIP
			span.SrcPort = tuple.SrcPort
			span.DstIP = tuple.DstIP
			span.DstPort = tuple.DstPort
			span.PID = pid
			span.TID = tid
			span.ContainerID = containerID
			span.ReqBytes = len(payload)

			_, _ = rand.Read(span.TraceID[:])
			_, _ = rand.Read(span.SpanID[:])

			pb.parseHTTPDetails(key, payload, span)

			pb.inFlight[key] = &InFlightTxRx{
				Span:      span,
				CreatedAt: now,
			}
		} else if isHTTPResponse(payload) {
			// Outbound server response completing an incoming server request (SpanKindServer)
			matchKey, inFlight := pb.findMatchingSpan(key, revKey, SpanKindServer)
			if inFlight != nil {
				span := inFlight.Span
				span.EndTime = eventTime
				span.Duration = span.EndTime.Sub(span.StartTime)
				span.RespBytes = len(payload)
				pb.parseHTTPResponseDetails(payload, span)

				delete(pb.inFlight, matchKey)

				if pb.decorator != nil {
					pb.decorator.DecorateSpan(span)
				}
				if pb.exporter != nil {
					_ = pb.exporter.ExportSpan(span)
				}
				pb.spansEmitted.Add(1)
			}
		}

	case abi.MsgTypeKTLSRx:
		// Inbound read: Request or Response
		if isHTTPRequest(payload) {
			// Inbound server request received
			span := NewTraceSpan(formatHTTPName(payload), SpanKindServer)
			span.StartTime = eventTime
			span.SrcIP = tuple.SrcIP
			span.SrcPort = tuple.SrcPort
			span.DstIP = tuple.DstIP
			span.DstPort = tuple.DstPort
			span.PID = pid
			span.TID = tid
			span.ContainerID = containerID
			span.ReqBytes = len(payload)

			_, _ = rand.Read(span.TraceID[:])
			_, _ = rand.Read(span.SpanID[:])

			pb.parseHTTPDetails(key, payload, span)

			pb.inFlight[key] = &InFlightTxRx{
				Span:      span,
				CreatedAt: now,
			}
		} else if isHTTPResponse(payload) {
			// Inbound client response received completing an outbound client request (SpanKindClient)
			matchKey, inFlight := pb.findMatchingSpan(key, revKey, SpanKindClient)
			if inFlight != nil {
				span := inFlight.Span
				span.EndTime = eventTime
				span.Duration = span.EndTime.Sub(span.StartTime)
				span.RespBytes = len(payload)
				pb.parseHTTPResponseDetails(payload, span)

				delete(pb.inFlight, matchKey)

				if pb.decorator != nil {
					pb.decorator.DecorateSpan(span)
				}
				if pb.exporter != nil {
					_ = pb.exporter.ExportSpan(span)
				}
				pb.spansEmitted.Add(1)
			}
		}
	}

	return nil
}

// findMatchingSpan searches for the corresponding in-flight transaction with preferred SpanKind.
func (pb *PipelineBridge) findMatchingSpan(key, revKey ConnKey, preferredKind SpanKind) (ConnKey, *InFlightTxRx) {
	// First check direct key
	if inF, exists := pb.inFlight[key]; exists && inF.Span.Kind == preferredKind {
		return key, inF
	}
	// Next check reverse key
	if inF, exists := pb.inFlight[revKey]; exists && inF.Span.Kind == preferredKind {
		return revKey, inF
	}
	// Fallback check direct key (any kind)
	if inF, exists := pb.inFlight[key]; exists {
		return key, inF
	}
	// Fallback check reverse key (any kind)
	if inF, exists := pb.inFlight[revKey]; exists {
		return revKey, inF
	}
	return key, nil
}

// SpansEmitted returns total spans processed and exported.
func (pb *PipelineBridge) SpansEmitted() uint64 {
	return pb.spansEmitted.Load()
}

// InFlightCount returns current in-flight connection tracking count.
func (pb *PipelineBridge) InFlightCount() int {
	pb.mu.Lock()
	defer pb.mu.Unlock()
	return len(pb.inFlight)
}

func isHTTPRequest(data []byte) bool {
	if len(data) < 4 {
		return false
	}
	methods := []string{"GET ", "POST ", "PUT ", "DELETE ", "HEAD ", "OPTIONS ", "PATCH "}
	for _, m := range methods {
		if bytes.HasPrefix(data, []byte(m)) {
			return true
		}
	}
	// Check for HTTP/2 preface
	if strings.HasPrefix(string(data), "PRI * HTTP/2.0") {
		return true
	}
	return false
}

func isHTTPResponse(data []byte) bool {
	return len(data) >= 8 && (strings.HasPrefix(string(data[:8]), "HTTP/1.1") || strings.HasPrefix(string(data[:8]), "HTTP/1.0"))
}

func formatHTTPName(data []byte) string {
	lineEnd := bytes.IndexByte(data, '\n')
	if lineEnd == -1 {
		lineEnd = min(len(data), 32)
	}
	firstLine := strings.TrimSpace(string(data[:lineEnd]))
	parts := strings.Split(firstLine, " ")
	if len(parts) >= 2 {
		return parts[0] + " " + parts[1]
	}
	return "HTTP Request"
}

func (pb *PipelineBridge) parseHTTPDetails(key ConnKey, data []byte, span *TraceSpan) {
	// Extract traceparent via ContextExtractor
	connKeyStr := key.String()
	if tc, ok := pb.extractor.ExtractHTTP1(data); ok {
		span.TraceID = tc.TraceID
		span.ParentSpanID = tc.ParentSpanID
		span.Attributes["w3c.traceparent"] = tc.FormatTraceparent()
	} else if tc, ok := pb.extractor.ExtractHTTP2(connKeyStr, data); ok {
		span.TraceID = tc.TraceID
		span.ParentSpanID = tc.ParentSpanID
		span.Attributes["w3c.traceparent"] = tc.FormatTraceparent()
	}

	reader := bufio.NewReader(bytes.NewReader(data))
	req, err := http.ReadRequest(reader)
	var bodyBytes []byte
	if err == nil {
		span.Method = req.Method
		span.Path = req.URL.Path
		span.Host = req.Host
		span.Attributes["http.request.method"] = req.Method
		span.Attributes["url.path"] = req.URL.Path
		span.Attributes["server.address"] = req.Host

		if req.Body != nil {
			bodyBytes, _ = io.ReadAll(req.Body)
		}
		if len(bodyBytes) == 0 {
			if idx := bytes.Index(data, []byte("\r\n\r\n")); idx != -1 {
				bodyBytes = data[idx+4:]
			}
		}
	} else {
		// Fallback simple parsing
		lines := strings.Split(string(data), "\r\n")
		parts := strings.Split(lines[0], " ")
		if len(parts) >= 2 {
			span.Method = parts[0]
			span.Path = parts[1]
			span.Attributes["http.request.method"] = parts[0]
			span.Attributes["url.path"] = parts[1]
		}
		// Try to find body separator
		if idx := bytes.Index(data, []byte("\r\n\r\n")); idx != -1 {
			bodyBytes = data[idx+4:]
		}
	}

	// 1. Vector DB semantic extraction
	if vDetails, ok := decoders.ParseVectorDBRequest(span.Path, bodyBytes); ok {
		span.Attributes["db.system"] = vDetails.System
		span.Attributes["db.collection.name"] = vDetails.CollectionName
		span.Attributes["db.operation"] = vDetails.Operation
		if vDetails.VectorDim > 0 {
			span.Attributes["db.vector.dimension"] = strconv.Itoa(vDetails.VectorDim)
		}
		if vDetails.TopK > 0 {
			span.Attributes["db.vector.top_k"] = strconv.Itoa(vDetails.TopK)
		}
		if vDetails.ScoreThreshold > 0 {
			span.Attributes["db.vector.score_threshold"] = fmt.Sprintf("%.2f", vDetails.ScoreThreshold)
		}
		if vDetails.HasFilter {
			span.Attributes["db.vector.has_filter"] = "true"
		}
	}

	// 2. Model Context Protocol (MCP) JSON-RPC extraction
	if len(bodyBytes) > 0 {
		if mcpDetails, ok := decoders.ParseMCPRequest(bodyBytes); ok {
			span.Attributes["mcp.method.name"] = mcpDetails.MethodName
			if mcpDetails.ToolName != "" {
				span.ToolName = mcpDetails.ToolName
				span.Attributes["gen_ai.tool.name"] = mcpDetails.ToolName
			}
			if mcpDetails.ToolArguments != "" {
				span.ToolArgs = mcpDetails.ToolArguments
				span.Attributes["gen_ai.tool.call.arguments"] = mcpDetails.ToolArguments
			}
		}
	}
}

func (pb *PipelineBridge) parseHTTPResponseDetails(data []byte, span *TraceSpan) {
	reader := bufio.NewReader(bytes.NewReader(data))
	resp, err := http.ReadResponse(reader, nil)
	var bodyBytes []byte
	if err == nil {
		span.HTTPStatus = resp.StatusCode
		span.StatusCode = resp.StatusCode
		span.Attributes["http.response.status_code"] = strconv.Itoa(resp.StatusCode)
		if resp.StatusCode >= 400 {
			span.StatusMsg = resp.Status
		}
		if resp.Body != nil {
			bodyBytes, _ = io.ReadAll(resp.Body)
		}
		if len(bodyBytes) == 0 {
			if idx := bytes.Index(data, []byte("\r\n\r\n")); idx != -1 {
				bodyBytes = data[idx+4:]
			}
		}
	} else {
		// Fallback
		parts := strings.Split(strings.Split(string(data), "\r\n")[0], " ")
		if len(parts) >= 2 {
			if code, err := strconv.Atoi(parts[1]); err == nil {
				span.HTTPStatus = code
				span.StatusCode = code
				span.Attributes["http.response.status_code"] = parts[1]
			}
		}
		if idx := bytes.Index(data, []byte("\r\n\r\n")); idx != -1 {
			bodyBytes = data[idx+4:]
		}
	}

	// 1. Server-Sent Events (SSE) LLM streaming extraction
	if strings.Contains(string(data), "data:") {
		sseDec := decoders.NewSSEDecoder(span.StartTime)
		metrics := sseDec.IngestChunk(data, span.EndTime)
		if metrics.ModelName != "" {
			span.ModelName = metrics.ModelName
			span.Attributes["gen_ai.response.model"] = metrics.ModelName
		}
		if metrics.PromptTokens > 0 {
			span.PromptTokens = metrics.PromptTokens
			span.Attributes["gen_ai.usage.input_tokens"] = strconv.Itoa(metrics.PromptTokens)
		}
		if metrics.OutputTokens > 0 {
			span.OutputTokens = metrics.OutputTokens
			span.Attributes["gen_ai.usage.output_tokens"] = strconv.Itoa(metrics.OutputTokens)
		}
		if metrics.TotalTokens > 0 {
			span.TotalTokens = metrics.TotalTokens
		}
		if metrics.TimeToFirstTok > 0 {
			span.TimeToFirstTok = metrics.TimeToFirstTok
			span.Attributes["gen_ai.time_to_first_token_ms"] = fmt.Sprintf("%.2f", float64(metrics.TimeToFirstTok.Microseconds())/1000.0)
		}
	}

	// 2. MCP JSON-RPC Response extraction
	if len(bodyBytes) > 0 {
		if mcpResp, ok := decoders.ParseMCPResponse(bodyBytes); ok {
			if mcpResp.IsError {
				span.Attributes["gen_ai.tool.call.error"] = mcpResp.ErrorMessage
			} else if mcpResp.ToolResult != "" {
				span.Attributes["gen_ai.tool.call.result"] = mcpResp.ToolResult
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
