// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ktls"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/propagation"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ringbuf"
)

func main() {
	containerID := flag.String("container-id", "demo-sandbox-01", "Container Sandbox ID")
	ringbufDir := flag.String("ringbuf-dir", "/tmp/gvisor-ringbuf", "Shared memory directory")
	flag.Parse()

	_ = os.MkdirAll(*ringbufDir, 0755)
	shmPath := filepath.Join(*ringbufDir, fmt.Sprintf("%s.shm", *containerID))

	sink, err := ringbuf.NewRingBufSink(ringbuf.SinkConfig{
		FilePath:      shmPath,
		DataSizeBytes: 4 * 1024 * 1024,
	})
	if err != nil {
		log.Fatalf("Failed to initialize RingBufSink: %v", err)
	}
	defer sink.Close()

	tapSink := ktls.NewRingBufTelemetrySink(sink)
	injector := propagation.NewContextInjector()

	log.Printf("Simulating gVisor Sentry sandbox [%s] at %s", *containerID, shmPath)

	// Simulate periodic agentic traffic
	tuple := ktls.SocketTuple{
		SrcIP:   net.ParseIP("10.0.0.100"),
		SrcPort: 54321,
		DstIP:   net.ParseIP("10.0.0.200"),
		DstPort: 443,
	}

	for i := 1; i <= 5; i++ {
		rootTC := propagation.NewTraceContext()
		reqRaw := []byte(fmt.Sprintf("POST /v1/chat/completions HTTP/1.1\r\nHost: api.openai.com\r\n\r\n{\"model\":\"gpt-4o\",\"prompt\":\"step %d\"}", i))
		req := injector.InjectHTTP1(reqRaw, rootTC)
		resp := []byte(fmt.Sprintf("HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\ndata: {\"model\":\"gpt-4o\",\"choices\":[{\"delta\":{\"content\":\"Result %d\"}}]}\n\ndata: [DONE]\n\n", i))

		tapSink.EmitKTLSTx(tuple, 1000, 1000, req)
		time.Sleep(20 * time.Millisecond)
		tapSink.EmitKTLSRx(tuple, 1000, 1000, resp)
		log.Printf("Emitted telemetry transaction #%d for sandbox %s", i, *containerID)
		time.Sleep(200 * time.Millisecond)
	}

	log.Printf("Simulation complete. Exiting.")
}
