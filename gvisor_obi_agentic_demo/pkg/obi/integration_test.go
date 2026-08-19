// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package obi

import (
	"crypto/rand"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ktls"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ringbuf"
)

func TestPipeline_EndToEnd_KTLS_To_OTelSpan(t *testing.T) {
	dir := t.TempDir()
	shmPath := filepath.Join(dir, "e2e.shm")
	containerID := "test-container-987"

	sink, err := ringbuf.NewRingBufSink(ringbuf.SinkConfig{
		FilePath:      shmPath,
		DataSizeBytes: 4 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewRingBufSink failed: %v", err)
	}
	defer sink.Close()

	reader, err := ringbuf.NewRingBufReader(ringbuf.ReaderConfig{
		FilePath:      shmPath,
		DataSizeBytes: 4 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewRingBufReader failed: %v", err)
	}
	defer reader.Close()

	// Decorator & Exporter
	decorator := NewK8sDecorator("gke-node-1")
	decorator.RegisterContainer(containerID, PodMetadata{
		PodName:       "agent-orchestrator-pod-xyz",
		Namespace:     "ai-agents",
		ContainerName: "orchestrator",
		NodeName:      "gke-node-1",
		PodUID:        "uid-999-888-777",
	})

	exporter := NewInMemorySpanExporter()
	bridge := NewPipelineBridge(decorator, exporter)
	tapSink := ktls.NewRingBufTelemetrySink(sink)

	// Sockets
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()

	clientCh := make(chan net.Conn, 1)
	go func() {
		conn, _ := ln.Accept()
		clientCh <- conn
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer clientConn.Close()

	serverConn := <-clientCh
	defer serverConn.Close()

	clientTuple := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.0.0.10"),
		SrcPort: 45678,
		DstIP:   net.ParseIP("10.0.0.20"),
		DstPort: 443,
	}
	serverTuple := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.0.0.20"),
		SrcPort: 443,
		DstIP:   net.ParseIP("10.0.0.10"),
		DstPort: 45678,
	}

	clientSock := ktls.NewTLSSocket(clientConn, clientTuple, 100, 101, tapSink)
	serverSock := ktls.NewTLSSocket(serverConn, serverTuple, 200, 201, tapSink)

	_ = clientSock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls")
	_ = serverSock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls")

	var info abi.TLS13CryptoInfoAESGCM128
	info.Info.Version = abi.TLS_1_3_VERSION
	info.Info.CipherType = abi.TLS_CIPHER_AES_GCM_128
	_, _ = rand.Read(info.Key[:])
	_, _ = rand.Read(info.IV[:])
	infoBytes := unsafe.Slice((*byte)(unsafe.Pointer(&info)), unsafe.Sizeof(info))

	_ = clientSock.SetSockOptTLS(abi.TLS_TX, infoBytes)
	_ = clientSock.SetSockOptTLS(abi.TLS_RX, infoBytes)
	_ = serverSock.SetSockOptTLS(abi.TLS_TX, infoBytes)
	_ = serverSock.SetSockOptTLS(abi.TLS_RX, infoBytes)

	reqPayload := []byte("POST /v1/chat/completions HTTP/1.1\r\nHost: gemini-api.google.com\r\ntraceparent: 00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01\r\nContent-Type: application/json\r\n\r\n{\"prompt\":\"Explain quantum computing\"}")
	respPayload := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"response\":\"Quantum computing utilizes qubits...\"}")

	// Server goroutine
	go func() {
		buf := make([]byte, 1024)
		n, _ := serverSock.Read(buf)
		if n > 0 {
			time.Sleep(10 * time.Millisecond) // Simulate processing time
			_, _ = serverSock.Write(respPayload)
		}
	}()

	// Client execute request
	_, err = clientSock.Write(reqPayload)
	if err != nil {
		t.Fatalf("client write failed: %v", err)
	}
	respBuf := make([]byte, 1024)
	_, err = clientSock.Read(respBuf)
	if err != nil {
		t.Fatalf("client read failed: %v", err)
	}

	// Drain ringbuffer into pipeline bridge
	count, err := reader.DrainAll(func(rec ringbuf.Record) error {
		return bridge.ProcessRecord(containerID, rec)
	})
	if err != nil {
		t.Fatalf("DrainAll error: %v", err)
	}
	if count < 4 {
		t.Fatalf("expected at least 4 raw records, got %d", count)
	}

	// Verify Exported Spans
	spans := exporter.Spans()
	if len(spans) == 0 {
		t.Fatalf("no spans exported!")
	}

	found := false
	for _, span := range spans {
		if span.Path == "/v1/chat/completions" && span.Method == "POST" {
			found = true
			if span.HTTPStatus != 200 {
				t.Errorf("expected HTTPStatus 200, got %d", span.HTTPStatus)
			}
			if span.Attributes["k8s.pod.name"] != "agent-orchestrator-pod-xyz" {
				t.Errorf("expected PodName agent-orchestrator-pod-xyz, got %s", span.Attributes["k8s.pod.name"])
			}
			if span.Attributes["k8s.namespace.name"] != "ai-agents" {
				t.Errorf("expected Namespace ai-agents, got %s", span.Attributes["k8s.namespace.name"])
			}
			if span.Attributes["w3c.traceparent"] != "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01" {
				t.Errorf("expected traceparent header preserved, got %s", span.Attributes["w3c.traceparent"])
			}
			if span.Duration <= 0 {
				t.Errorf("expected positive span duration, got %v", span.Duration)
			}
			t.Logf("Exported Span verified: %s", span.SpanSummary())
		}
	}
	if !found {
		t.Fatalf("expected /v1/chat/completions span not found in exported spans")
	}
}

func TestPipeline_DynamicPodDiscovery(t *testing.T) {
	watchDir := t.TempDir()
	decorator := NewK8sDecorator("node-1")
	exporter := NewInMemorySpanExporter()
	bridge := NewPipelineBridge(decorator, exporter)

	pdm := NewPodDiscoveryManager(watchDir, bridge, 20*time.Millisecond)
	if err := pdm.Start(); err != nil {
		t.Fatalf("pdm Start failed: %v", err)
	}
	defer pdm.Stop()

	// 1. Create a simulated container ringbuffer file
	containerID := "cont-alpha-123"
	shmPath := filepath.Join(watchDir, containerID+".shm")

	sink, err := ringbuf.NewRingBufSink(ringbuf.SinkConfig{
		FilePath:      shmPath,
		DataSizeBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewRingBufSink failed: %v", err)
	}
	defer sink.Close()

	// Wait for discovery
	time.Sleep(100 * time.Millisecond)

	active := pdm.ActiveContainers()
	if len(active) != 1 || active[0] != containerID {
		t.Fatalf("expected 1 active container %s, got %v", containerID, active)
	}

	// 2. Emit an event through sink
	tuple := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.1.1.1"),
		SrcPort: 1234,
		DstIP:   net.ParseIP("10.1.1.2"),
		DstPort: 8080,
	}
	reqData := ktls.SerializeTelemetryEvent(tuple, 1, 1, []byte("GET /healthz HTTP/1.1\r\nHost: localhost\r\n\r\n"))
	sink.ReserveAndCommit(abi.MsgTypeKTLSTx, abi.ChunkFlagSingle, reqData)

	respData := ktls.SerializeTelemetryEvent(tuple, 1, 1, []byte("HTTP/1.1 200 OK\r\n\r\nOK"))
	sink.ReserveAndCommit(abi.MsgTypeKTLSRx, abi.ChunkFlagSingle, respData)

	// Wait for worker to consume
	time.Sleep(100 * time.Millisecond)

	spans := exporter.Spans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span exported from discovered container, got %d", len(spans))
	}
	if spans[0].Path != "/healthz" {
		t.Errorf("expected /healthz span, got %s", spans[0].Path)
	}

	// 3. Remove container file and verify worker cleanup
	_ = sink.Close()
	_ = os.Remove(shmPath)

	time.Sleep(100 * time.Millisecond)
	activeAfter := pdm.ActiveContainers()
	if len(activeAfter) != 0 {
		t.Fatalf("expected 0 active containers after deletion, got %v", activeAfter)
	}
	t.Logf("Dynamic Pod discovery and cleanup verified successfully")
}

func TestPipeline_ConcurrentConnections_Correlation(t *testing.T) {
	decorator := NewK8sDecorator("node-1")
	exporter := NewInMemorySpanExporter()
	bridge := NewPipelineBridge(decorator, exporter)

	numConns := 20
	var wg sync.WaitGroup

	for i := 0; i < numConns; i++ {
		wg.Add(1)
		go func(connID int) {
			defer wg.Done()
			tuple := ktls.SocketTuple{
				SrcIP:   net.ParseIP("10.0.0.1"),
				SrcPort: uint16(10000 + connID),
				DstIP:   net.ParseIP("10.0.0.2"),
				DstPort: 8080,
			}
			req := []byte(fmt.Sprintf("GET /api/v1/item/%d HTTP/1.1\r\nHost: api\r\n\r\n", connID))
			resp := []byte(fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK"))

			reqEv := ktls.SerializeTelemetryEvent(tuple, uint32(connID), uint32(connID), req)
			_ = bridge.ProcessRecord("container-bench", ringbuf.Record{
				MsgType: abi.MsgTypeKTLSTx,
				Payload: reqEv,
			})

			time.Sleep(5 * time.Millisecond)

			respEv := ktls.SerializeTelemetryEvent(tuple, uint32(connID), uint32(connID), resp)
			_ = bridge.ProcessRecord("container-bench", ringbuf.Record{
				MsgType: abi.MsgTypeKTLSRx,
				Payload: respEv,
			})
		}(i)
	}

	wg.Wait()

	spans := exporter.Spans()
	if len(spans) != numConns {
		t.Fatalf("expected %d spans, got %d", numConns, len(spans))
	}

	found := make(map[string]bool)
	for _, s := range spans {
		found[s.Path] = true
	}
	for i := 0; i < numConns; i++ {
		expectedPath := fmt.Sprintf("/api/v1/item/%d", i)
		if !found[expectedPath] {
			t.Errorf("missing expected span path %s", expectedPath)
		}
	}
	t.Logf("Concurrent connection correlation: 100%% matching across %d connections", numConns)
}

func TestPipeline_HTTPErrorStatusAndDecorator(t *testing.T) {
	decorator := NewK8sDecorator("node-2")
	decorator.RegisterContainer("cont-error-test", PodMetadata{
		PodName:       "vector-db-0",
		Namespace:     "qdrant",
		ContainerName: "qdrant-server",
		NodeName:      "node-2",
		PodUID:        "uid-qdrant-123",
	})

	exporter := NewInMemorySpanExporter()
	bridge := NewPipelineBridge(decorator, exporter)

	tuple := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.2.0.1"),
		SrcPort: 50000,
		DstIP:   net.ParseIP("10.2.0.2"),
		DstPort: 6333,
	}

	req := []byte("GET /collections/missing_kb HTTP/1.1\r\nHost: qdrant:6333\r\n\r\n")
	resp := []byte("HTTP/1.1 404 Not Found\r\nContent-Type: application/json\r\n\r\n{\"status\":\"error\",\"message\":\"Collection missing_kb not found\"}")

	reqEv := ktls.SerializeTelemetryEvent(tuple, 50, 50, req)
	_ = bridge.ProcessRecord("cont-error-test", ringbuf.Record{
		MsgType: abi.MsgTypeKTLSTx,
		Payload: reqEv,
	})

	respEv := ktls.SerializeTelemetryEvent(tuple, 50, 50, resp)
	_ = bridge.ProcessRecord("cont-error-test", ringbuf.Record{
		MsgType: abi.MsgTypeKTLSRx,
		Payload: respEv,
	})

	spans := exporter.Spans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}

	span := spans[0]
	if span.HTTPStatus != 404 {
		t.Errorf("expected HTTP 404, got %d", span.HTTPStatus)
	}
	if span.Attributes["k8s.pod.name"] != "vector-db-0" {
		t.Errorf("expected pod vector-db-0, got %s", span.Attributes["k8s.pod.name"])
	}
	if span.Attributes["k8s.namespace.name"] != "qdrant" {
		t.Errorf("expected namespace qdrant, got %s", span.Attributes["k8s.namespace.name"])
	}
}

func TestPipeline_ChunkedLargePrompt_Reassembly(t *testing.T) {
	decorator := NewK8sDecorator("node-1")
	exporter := NewInMemorySpanExporter()
	bridge := NewPipelineBridge(decorator, exporter)

	tuple := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.0.0.1"),
		SrcPort: 33445,
		DstIP:   net.ParseIP("10.0.0.2"),
		DstPort: 443,
	}

	part1 := []byte("POST /v1/chat/completions HTTP/1.1\r\nHost: api\r\nContent-Type: application/json\r\n\r\n{\"prompt\":\"")
	part2 := []byte("repeated prompt segment 1234567890 ")
	part3 := []byte("\"}")

	// Feed chunked request
	ev1 := ktls.SerializeTelemetryEvent(tuple, 10, 10, part1)
	ev2 := ktls.SerializeTelemetryEvent(tuple, 10, 10, part2)
	ev3 := ktls.SerializeTelemetryEvent(tuple, 10, 10, part3)

	_ = bridge.ProcessRecord("cont-chunked", ringbuf.Record{MsgType: abi.MsgTypeKTLSTx, Flags: abi.ChunkFlagStart, Payload: ev1})
	_ = bridge.ProcessRecord("cont-chunked", ringbuf.Record{MsgType: abi.MsgTypeKTLSTx, Flags: abi.ChunkFlagCont, Payload: ev2})
	_ = bridge.ProcessRecord("cont-chunked", ringbuf.Record{MsgType: abi.MsgTypeKTLSTx, Flags: abi.ChunkFlagEnd, Payload: ev3})

	resp := []byte("HTTP/1.1 200 OK\r\n\r\n{\"response\":\"OK\"}")
	respEv := ktls.SerializeTelemetryEvent(tuple, 10, 10, resp)
	_ = bridge.ProcessRecord("cont-chunked", ringbuf.Record{MsgType: abi.MsgTypeKTLSRx, Flags: abi.ChunkFlagSingle, Payload: respEv})

	spans := exporter.Spans()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span after reassembled chunks, got %d", len(spans))
	}
	if spans[0].Path != "/v1/chat/completions" {
		t.Errorf("expected /v1/chat/completions path, got %s", spans[0].Path)
	}
	t.Logf("Multi-chunk prompt successfully reassembled and transformed into span")
}

func TestPipeline_InFlightTTL_Cleanup(t *testing.T) {
	decorator := NewK8sDecorator("node-1")
	exporter := NewInMemorySpanExporter()
	bridge := NewPipelineBridge(decorator, exporter)
	bridge.inFlightTTL = 10 * time.Millisecond

	tuple := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.0.0.1"),
		SrcPort: 55667,
		DstIP:   net.ParseIP("10.0.0.2"),
		DstPort: 80,
	}

	req := []byte("GET /abandoned HTTP/1.1\r\nHost: api\r\n\r\n")
	reqEv := ktls.SerializeTelemetryEvent(tuple, 1, 1, req)

	_ = bridge.ProcessRecord("cont-ttl", ringbuf.Record{MsgType: abi.MsgTypeKTLSTx, Flags: abi.ChunkFlagSingle, Payload: reqEv})

	if bridge.InFlightCount() != 1 {
		t.Fatalf("expected 1 in-flight item, got %d", bridge.InFlightCount())
	}

	// Sleep past TTL
	time.Sleep(25 * time.Millisecond)

	// Trigger cleanup sweep
	bridge.SweepStaleInFlight()

	// The abandoned request should have been pruned!
	if bridge.InFlightCount() != 0 {
		t.Fatalf("expected old in-flight item to be evicted by TTL, count=%d", bridge.InFlightCount())
	}
	t.Logf("InFlight TTL cleanup successfully evicted expired abandoned connection")
}

func BenchmarkPipeline_Throughput(b *testing.B) {
	decorator := NewK8sDecorator("node-bench")
	exporter := NewInMemorySpanExporter()
	bridge := NewPipelineBridge(decorator, exporter)

	tuple := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.0.0.1"),
		SrcPort: 12345,
		DstIP:   net.ParseIP("10.0.0.2"),
		DstPort: 80,
	}

	req := []byte("GET /bench HTTP/1.1\r\nHost: test\r\n\r\n")
	resp := []byte("HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\nOK")

	reqEv := ktls.SerializeTelemetryEvent(tuple, 1, 1, req)
	respEv := ktls.SerializeTelemetryEvent(tuple, 1, 1, resp)

	recTx := ringbuf.Record{MsgType: abi.MsgTypeKTLSTx, Payload: reqEv}
	recRx := ringbuf.Record{MsgType: abi.MsgTypeKTLSRx, Payload: respEv}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = bridge.ProcessRecord("container-bench", recTx)
		_ = bridge.ProcessRecord("container-bench", recRx)
	}
}
