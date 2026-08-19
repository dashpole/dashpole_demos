// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package propagation

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var (
	ErrInvalidTraceparent = errors.New("propagation: invalid W3C traceparent header")
)

// TraceContext represents a parsed W3C trace context.
type TraceContext struct {
	Version      byte
	TraceID      [16]byte
	SpanID       [8]byte // Span ID transmitted in the header (wire parent-id)
	ParentSpanID [8]byte // Received parent span ID
	TraceFlags   byte
}

// NewTraceContext generates a fresh root trace context.
func NewTraceContext() TraceContext {
	var tc TraceContext
	tc.Version = 0x00
	for {
		_, _ = rand.Read(tc.TraceID[:])
		if !bytes.Equal(tc.TraceID[:], make([]byte, 16)) {
			break
		}
	}
	for {
		_, _ = rand.Read(tc.SpanID[:])
		if !bytes.Equal(tc.SpanID[:], make([]byte, 8)) {
			break
		}
	}
	tc.ParentSpanID = tc.SpanID
	tc.TraceFlags = 0x01 // Sampled
	return tc
}

// NewChildContext creates a child context inheriting TraceID with parentSpanID as the outbound SpanID.
func (tc TraceContext) NewChildContext(parentSpanID [8]byte) TraceContext {
	return TraceContext{
		Version:      tc.Version,
		TraceID:      tc.TraceID,
		SpanID:       parentSpanID,
		ParentSpanID: parentSpanID,
		TraceFlags:   tc.TraceFlags,
	}
}

// FormatTraceparent formats the context for outbound transmission: "00-traceid-spanid-01".
func (tc TraceContext) FormatTraceparent() string {
	return fmt.Sprintf("%02x-%s-%s-%02x",
		tc.Version,
		hex.EncodeToString(tc.TraceID[:]),
		hex.EncodeToString(tc.SpanID[:]),
		tc.TraceFlags,
	)
}

// ParseTraceparent decodes a W3C traceparent string conforming strictly to W3C Trace Context spec.
func ParseTraceparent(val string) (TraceContext, error) {
	val = strings.TrimSpace(val)
	parts := strings.Split(val, "-")
	if len(parts) < 4 {
		return TraceContext{}, fmt.Errorf("%w: expected at least 4 hyphen-separated parts, got %d", ErrInvalidTraceparent, len(parts))
	}

	if len(parts[0]) != 2 || len(parts[1]) != 32 || len(parts[2]) != 16 || len(parts[3]) != 2 {
		return TraceContext{}, fmt.Errorf("%w: invalid field lengths", ErrInvalidTraceparent)
	}

	// 1. Version validation
	vBytes, err := hex.DecodeString(parts[0])
	if err != nil {
		return TraceContext{}, fmt.Errorf("%w: invalid version hex", ErrInvalidTraceparent)
	}
	if vBytes[0] == 0xff {
		return TraceContext{}, fmt.Errorf("%w: version ff is prohibited by W3C spec", ErrInvalidTraceparent)
	}
	if vBytes[0] == 0x00 && len(parts) > 4 {
		return TraceContext{}, fmt.Errorf("%w: version 00 cannot contain more than 4 parts", ErrInvalidTraceparent)
	}

	// 2. TraceID validation
	tBytes, err := hex.DecodeString(parts[1])
	if err != nil {
		return TraceContext{}, fmt.Errorf("%w: invalid trace_id hex", ErrInvalidTraceparent)
	}
	if bytes.Equal(tBytes, make([]byte, 16)) {
		return TraceContext{}, fmt.Errorf("%w: all-zero trace_id is prohibited", ErrInvalidTraceparent)
	}

	// 3. ParentID validation
	pBytes, err := hex.DecodeString(parts[2])
	if err != nil {
		return TraceContext{}, fmt.Errorf("%w: invalid parent_id hex", ErrInvalidTraceparent)
	}
	if bytes.Equal(pBytes, make([]byte, 8)) {
		return TraceContext{}, fmt.Errorf("%w: all-zero parent_id is prohibited", ErrInvalidTraceparent)
	}

	// 4. Flags validation
	fBytes, err := hex.DecodeString(parts[3])
	if err != nil {
		return TraceContext{}, fmt.Errorf("%w: invalid flags hex", ErrInvalidTraceparent)
	}

	var tc TraceContext
	tc.Version = vBytes[0]
	copy(tc.TraceID[:], tBytes)
	copy(tc.ParentSpanID[:], pBytes)
	copy(tc.SpanID[:], pBytes)
	tc.TraceFlags = fBytes[0]

	return tc, nil
}
