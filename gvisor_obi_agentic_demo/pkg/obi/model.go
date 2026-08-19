// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package obi

import (
	"net"
	"time"
)

// SpanKind matches OTel SpanKind.
type SpanKind int

const (
	SpanKindInternal SpanKind = 1
	SpanKindServer   SpanKind = 2
	SpanKindClient   SpanKind = 3
	SpanKindProducer SpanKind = 4
	SpanKindConsumer SpanKind = 5
)

// TraceSpan represents an in-flight or completed OpenTelemetry span.
type TraceSpan struct {
	TraceID      [16]byte
	SpanID       [8]byte
	ParentSpanID [8]byte
	Name         string
	Kind         SpanKind
	StartTime    time.Time
	EndTime      time.Time
	Duration     time.Duration
	StatusCode   int
	StatusMsg    string

	// Connection 5-tuple
	SrcIP   net.IP
	SrcPort uint16
	DstIP   net.IP
	DstPort uint16

	// Process & Container
	PID         uint32
	TID         uint32
	ContainerID string

	// HTTP & RPC Metadata
	Method     string
	Path       string
	Host       string
	Scheme     string
	HTTPStatus int
	ReqBytes   int
	RespBytes  int

	// GenAI & Agentic Metadata
	ModelName      string
	PromptTokens   int
	OutputTokens   int
	TotalTokens    int
	TimeToFirstTok time.Duration
	ToolName       string
	ToolArgs       string
	ToolSessionID  string

	// Dynamic Attributes
	Attributes map[string]string
}

// NewTraceSpan creates a new TraceSpan with initialized attribute map.
func NewTraceSpan(name string, kind SpanKind) *TraceSpan {
	return &TraceSpan{
		Name:       name,
		Kind:       kind,
		Attributes: make(map[string]string),
	}
}
