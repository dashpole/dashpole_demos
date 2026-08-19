// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package obi

import (
	"encoding/hex"
	"fmt"
	"sync"
)

// InMemorySpanExporter stores completed spans in memory for verification and testing.
type InMemorySpanExporter struct {
	mu    sync.Mutex
	spans []*TraceSpan
}

// NewInMemorySpanExporter creates a new in-memory collector exporter.
func NewInMemorySpanExporter() *InMemorySpanExporter {
	return &InMemorySpanExporter{
		spans: make([]*TraceSpan, 0),
	}
}

// ExportSpan stores the completed span.
func (exp *InMemorySpanExporter) ExportSpan(span *TraceSpan) error {
	exp.mu.Lock()
	defer exp.mu.Unlock()
	// Deep copy span
	copied := *span
	copied.Attributes = make(map[string]string, len(span.Attributes))
	for k, v := range span.Attributes {
		copied.Attributes[k] = v
	}
	exp.spans = append(exp.spans, &copied)
	return nil
}

// Spans returns a copy of all exported spans.
func (exp *InMemorySpanExporter) Spans() []*TraceSpan {
	exp.mu.Lock()
	defer exp.mu.Unlock()
	res := make([]*TraceSpan, len(exp.spans))
	copy(res, exp.spans)
	return res
}

// Clear resets the exporter storage.
func (exp *InMemorySpanExporter) Clear() {
	exp.mu.Lock()
	defer exp.mu.Unlock()
	exp.spans = exp.spans[:0]
}

// SpanSummary prints a human-readable summary of a span.
func (s *TraceSpan) SpanSummary() string {
	traceHex := hex.EncodeToString(s.TraceID[:])
	spanHex := hex.EncodeToString(s.SpanID[:])
	return fmt.Sprintf("[Span: %s | %s] Trace=%s Span=%s Status=%d Duration=%v Pod=%s",
		s.Name, s.Method, traceHex, spanHex, s.HTTPStatus, s.Duration, s.Attributes["k8s.pod.name"])
}
