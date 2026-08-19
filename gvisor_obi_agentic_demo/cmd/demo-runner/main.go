// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ktls"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/obi"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/propagation"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ringbuf"
)

func main() {
	fmt.Println("================================================================================")
	fmt.Println("   gVisor Sentry <-> OpenTelemetry eBPF Instrumentation (OBI) Agentic Demo")
	fmt.Println("================================================================================")

	tempDir, err := os.MkdirTemp("", "gvisor-obi-demo-*")
	if err != nil {
		log.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	// Step 1: Initialize K8s Decorator and In-Memory Exporter
	decorator := obi.NewK8sDecorator("gke-sandbox-pool-01")
	decorator.RegisterContainer("sandbox-frontend", obi.PodMetadata{PodName: "frontend-ui-6f7c9b", Namespace: "agentic-app", ContainerName: "web"})
	decorator.RegisterContainer("sandbox-orchestrator", obi.PodMetadata{PodName: "agent-orchestrator-8d9e", Namespace: "agentic-app", ContainerName: "orchestrator"})
	decorator.RegisterContainer("sandbox-qdrant", obi.PodMetadata{PodName: "qdrant-vector-db-0", Namespace: "agentic-app", ContainerName: "qdrant"})
	decorator.RegisterContainer("sandbox-mcp", obi.PodMetadata{PodName: "mcp-sqlite-server-4b2a", Namespace: "agentic-app", ContainerName: "mcp-server"})

	exporter := obi.NewInMemorySpanExporter()
	bridge := obi.NewPipelineBridge(decorator, exporter)
	defer bridge.Close()

	// Step 2: Initialize Pod Discovery Manager
	discovery := obi.NewPodDiscoveryManager(tempDir, bridge, 20*time.Millisecond)
	if err := discovery.Start(); err != nil {
		log.Fatalf("Failed to start PodDiscoveryManager: %v", err)
	}
	defer discovery.Stop()

	// Step 3: Create Sentry Ring Buffers for all 4 sandboxes
	createSandboxRingBuf := func(cid string) (*ringbuf.RingBufSink, *ktls.RingBufTelemetrySink) {
		shmPath := filepath.Join(tempDir, fmt.Sprintf("%s.shm", cid))
		sink, err := ringbuf.NewRingBufSink(ringbuf.SinkConfig{
			FilePath:      shmPath,
			DataSizeBytes: 4 * 1024 * 1024,
		})
		if err != nil {
			log.Fatalf("Failed to create sink for %s: %v", cid, err)
		}
		return sink, ktls.NewRingBufTelemetrySink(sink)
	}

	sinkFront, tapFront := createSandboxRingBuf("sandbox-frontend")
	defer sinkFront.Close()
	sinkOrch, tapOrch := createSandboxRingBuf("sandbox-orchestrator")
	defer sinkOrch.Close()
	sinkQdrant, tapQdrant := createSandboxRingBuf("sandbox-qdrant")
	defer sinkQdrant.Close()
	sinkMCP, tapMCP := createSandboxRingBuf("sandbox-mcp")
	defer sinkMCP.Close()

	// Wait for discovery to register active readers
	time.Sleep(100 * time.Millisecond)
	fmt.Printf("[+] Discovered %d sandboxed workloads under %s\n", len(discovery.ActiveContainers()), tempDir)

	injector := propagation.NewContextInjector()

	// --- HOP 1: User Request to Frontend UI ---
	fmt.Println("\n[1/4] User sends prompt: 'Analyze portfolio performance and update customer vector profile'")
	rootTC := propagation.NewTraceContext()
	tuple1 := ktls.SocketTuple{
		SrcIP:   net.ParseIP("192.168.1.50"),
		SrcPort: 58120,
		DstIP:   net.ParseIP("10.128.0.10"),
		DstPort: 80,
	}
	req1Raw := []byte("POST /api/chat HTTP/1.1\r\nHost: agent.demo.google\r\nContent-Type: application/json\r\n\r\n{\"prompt\":\"Analyze portfolio performance\"}")
	req1 := injector.InjectHTTP1(req1Raw, rootTC)
	resp1 := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"status\":\"Agent workflow completed successfully\"}")

	tapFront.EmitKTLSRx(tuple1, 101, 101, req1)
	time.Sleep(10 * time.Millisecond)
	tapFront.EmitKTLSTx(tuple1, 101, 101, resp1)

	// Fetch Span 1 to obtain SpanID for child context propagation
	time.Sleep(50 * time.Millisecond)
	spans := exporter.Spans()
	if len(spans) < 1 {
		log.Fatalf("Hop 1 failed to emit span")
	}
	span1 := spans[0]

	// --- HOP 2: Frontend calls Agent Orchestrator ---
	fmt.Println("[2/4] Frontend delegates to Agent Orchestrator (In-Sentry W3C context propagation)")
	hop2TC := rootTC.NewChildContext(span1.SpanID)
	tuple2 := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.128.0.10"),
		SrcPort: 48920,
		DstIP:   net.ParseIP("10.128.0.11"),
		DstPort: 9000,
	}
	req2Raw := []byte("POST /v1/orchestrate HTTP/1.1\r\nHost: agent-orchestrator\r\nContent-Type: application/json\r\n\r\n{\"task\":\"portfolio_analysis\"}")
	req2 := injector.InjectHTTP1(req2Raw, hop2TC)
	resp2 := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"plan\":\"1. Query Vector DB, 2. Run MCP Tool, 3. Stream LLM summary\"}")

	tapOrch.EmitKTLSRx(tuple2, 202, 202, req2)
	time.Sleep(15 * time.Millisecond)
	tapOrch.EmitKTLSTx(tuple2, 202, 202, resp2)

	time.Sleep(50 * time.Millisecond)
	spans = exporter.Spans()
	if len(spans) < 2 {
		log.Fatalf("Hop 2 failed to emit span")
	}
	span2 := spans[1]

	// --- HOP 3: Agent Orchestrator queries Qdrant Vector DB ---
	fmt.Println("[3/4] Agent Orchestrator executes Qdrant vector similarity search (1536-dim embedding)")
	hop3TC_Qdrant := hop2TC.NewChildContext(span2.SpanID)
	tuple3 := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.128.0.11"),
		SrcPort: 42100,
		DstIP:   net.ParseIP("10.128.0.12"),
		DstPort: 6333,
	}
	req3Raw := []byte("POST /collections/customer_portfolios/points/search HTTP/1.1\r\nHost: qdrant:6333\r\nContent-Type: application/json\r\n\r\n{\"vector\":[0.05,-0.12,0.88,0.34],\"limit\":5,\"score_threshold\":0.92}")
	req3 := injector.InjectHTTP1(req3Raw, hop3TC_Qdrant)
	resp3 := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"result\":[{\"id\":101,\"score\":0.96,\"payload\":{\"risk\":\"moderate\"}}]}")

	tapQdrant.EmitKTLSRx(tuple3, 303, 303, req3)
	time.Sleep(10 * time.Millisecond)
	tapQdrant.EmitKTLSTx(tuple3, 303, 303, resp3)

	// --- HOP 4: Agent Orchestrator executes MCP Tool Call ---
	fmt.Println("[4/4] Agent Orchestrator invokes Model Context Protocol (MCP) SQLite Tool server")
	hop3TC_MCP := hop2TC.NewChildContext(span2.SpanID)
	tuple4 := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.128.0.11"),
		SrcPort: 42102,
		DstIP:   net.ParseIP("10.128.0.13"),
		DstPort: 8080,
	}
	req4Raw := []byte("POST /mcp HTTP/1.1\r\nHost: mcp-server:8080\r\nContent-Type: application/json\r\n\r\n{\"jsonrpc\":\"2.0\",\"id\":1001,\"method\":\"tools/call\",\"params\":{\"name\":\"sql_query_portfolio\",\"arguments\":{\"customer_id\":101,\"include_dividends\":true}}}")
	req4 := injector.InjectHTTP1(req4Raw, hop3TC_MCP)
	resp4 := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"jsonrpc\":\"2.0\",\"id\":1001,\"result\":{\"portfolio_value\":158420.50,\"currency\":\"USD\"}}")

	tapMCP.EmitKTLSRx(tuple4, 404, 404, req4)
	time.Sleep(12 * time.Millisecond)
	tapMCP.EmitKTLSTx(tuple4, 404, 404, resp4)

	// Wait for pipeline ingestion
	time.Sleep(150 * time.Millisecond)
	allSpans := exporter.Spans()

	fmt.Println("\n================================================================================")
	fmt.Printf("   VERIFICATION RESULTS: Captured %d Spans Across Distributed Sandboxes\n", len(allSpans))
	fmt.Println("================================================================================")

	expectedTrace := hex.EncodeToString(rootTC.TraceID[:])
	fmt.Printf("Root Trace ID: %s\n\n", expectedTrace)

	for idx, s := range allSpans {
		traceHex := hex.EncodeToString(s.TraceID[:])
		spanHex := hex.EncodeToString(s.SpanID[:])
		parentHex := hex.EncodeToString(s.ParentSpanID[:])

		indent := strings.Repeat("  ", idx)
		fmt.Printf("%s└─ [%d] Span: %s\n", indent, idx+1, s.Name)
		fmt.Printf("%s   Pod:           %s (%s)\n", indent, s.Attributes["k8s.pod.name"], s.Attributes["k8s.container.name"])
		fmt.Printf("%s   TraceID:       %s\n", indent, traceHex)
		fmt.Printf("%s   SpanID:        %s | ParentSpanID: %s\n", indent, spanHex, parentHex)
		fmt.Printf("%s   Duration:      %v | HTTP: %d\n", indent, s.Duration, s.HTTPStatus)
		if s.Attributes["db.system"] != "" {
			fmt.Printf("%s   Vector DB:     system=%s, collection=%s, dim=%s, top_k=%s, score_threshold=%s\n",
				indent, s.Attributes["db.system"], s.Attributes["db.collection.name"],
				s.Attributes["db.vector.dimension"], s.Attributes["db.vector.top_k"], s.Attributes["db.vector.score_threshold"])
		}
		if s.ToolName != "" {
			fmt.Printf("%s   MCP Tool:      name=%s, args=%s\n", indent, s.ToolName, s.ToolArgs)
		}
		fmt.Println()
	}

	// Automated Validation Assertions
	if len(allSpans) != 4 {
		log.Fatalf("FAILED: Expected 4 spans, got %d", len(allSpans))
	}
	for i, s := range allSpans {
		if hex.EncodeToString(s.TraceID[:]) != expectedTrace {
			log.Fatalf("FAILED: Span %d TraceID mismatch", i+1)
		}
	}
	if allSpans[1].ParentSpanID != allSpans[0].SpanID {
		log.Fatalf("FAILED: Span 2 ParentSpanID does not link to Span 1 SpanID")
	}
	if allSpans[2].ParentSpanID != allSpans[1].SpanID {
		log.Fatalf("FAILED: Span 3 (Qdrant) ParentSpanID does not link to Span 2 SpanID")
	}
	if allSpans[3].ParentSpanID != allSpans[1].SpanID {
		log.Fatalf("FAILED: Span 4 (MCP) ParentSpanID does not link to Span 2 SpanID")
	}

	fmt.Println("================================================================================")
	fmt.Println("   STATUS: 100% PASSED! Multi-tier agentic trace verified across all sandboxes.")
	fmt.Println("================================================================================")
}
