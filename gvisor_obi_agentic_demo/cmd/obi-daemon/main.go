// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/obi"
)

type stdoutSpanExporter struct{}

func (s *stdoutSpanExporter) ExportSpan(span *obi.TraceSpan) error {
	log.Printf("[OTEL SPAN EXPORT] %s (Req: %d B, Resp: %d B, Dur: %v) | Attributes: %v",
		span.SpanSummary(), span.ReqBytes, span.RespBytes, span.Duration, span.Attributes)
	return nil
}

func main() {
	watchDir := flag.String("ringbuf-dir", "/run/gvisor-ringbuf", "Directory containing gVisor ringbuffer .shm files")
	nodeName := flag.String("node-name", "gke-node-1", "Kubernetes Node Name for decoration")
	pollInterval := flag.Duration("poll-interval", 50*time.Millisecond, "Discovery polling interval")
	flag.Parse()

	log.Printf("Starting OpenTelemetry eBPF Instrumentation (OBI) for gVisor...")
	log.Printf("Monitoring ring buffer directory: %s", *watchDir)

	decorator := obi.NewK8sDecorator(*nodeName)
	exporter := &stdoutSpanExporter{}
	bridge := obi.NewPipelineBridge(decorator, exporter)
	defer bridge.Close()

	discovery := obi.NewPodDiscoveryManager(*watchDir, bridge, *pollInterval)
	if err := discovery.Start(); err != nil {
		log.Fatalf("Failed to start PodDiscoveryManager: %v", err)
	}
	defer discovery.Stop()

	log.Printf("OBI Daemon successfully running. Press Ctrl+C to terminate.")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Printf("Shutting down OBI Daemon...")
			return
		case <-ticker.C:
			active := discovery.ActiveContainers()
			log.Printf("OBI Heartbeat: Active sandboxes: %d, Spans emitted: %d, In-flight connections: %d",
				len(active), bridge.SpansEmitted(), bridge.InFlightCount())
		}
	}
}
