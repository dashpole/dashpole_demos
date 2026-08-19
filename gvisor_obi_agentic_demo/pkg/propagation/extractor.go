// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package propagation

import (
	"bufio"
	"bytes"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/net/http2/hpack"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/decoders"
)

// ContextExtractor extracts W3C trace context from HTTP/1.1 and HTTP/2 payloads.
type ContextExtractor struct {
	mu       sync.Mutex
	decoders map[string]*hpack.Decoder
}

// NewContextExtractor creates a new context extractor.
func NewContextExtractor() *ContextExtractor {
	return &ContextExtractor{
		decoders: make(map[string]*hpack.Decoder),
	}
}

// ExtractHTTP1 extracts TraceContext strictly from the header section of an HTTP/1.1 payload.
func (ce *ContextExtractor) ExtractHTTP1(payload []byte) (TraceContext, bool) {
	reader := bufio.NewReader(bytes.NewReader(payload))
	req, err := http.ReadRequest(reader)
	if err == nil {
		if tp := req.Header.Get("traceparent"); tp != "" {
			if tc, err := ParseTraceparent(tp); err == nil {
				return tc, true
			}
		}
	}

	// Strictly limit fallback scanning to the header section before \r\n\r\n
	headerEnd := bytes.Index(payload, []byte("\r\n\r\n"))
	var headerBytes []byte
	if headerEnd != -1 {
		headerBytes = payload[:headerEnd]
	} else {
		// If incomplete, only check if it looks like headers without body
		if isHTTP1Request(payload) {
			headerBytes = payload
		} else {
			return TraceContext{}, false
		}
	}

	lines := strings.Split(string(headerBytes), "\r\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(trimmed), "traceparent:") {
			tp := strings.TrimSpace(trimmed[len("traceparent:"):])
			if tc, err := ParseTraceparent(tp); err == nil {
				return tc, true
			}
		}
	}

	return TraceContext{}, false
}

// ExtractHTTP2 extracts TraceContext from HTTP/2 HEADERS frames.
func (ce *ContextExtractor) ExtractHTTP2(connKey string, data []byte) (TraceContext, bool) {
	ce.mu.Lock()
	defer ce.mu.Unlock()

	frames, err := decoders.ParseFrames(data)
	if err != nil || len(frames) == 0 {
		return TraceContext{}, false
	}

	dec, exists := ce.decoders[connKey]
	if !exists {
		dec = hpack.NewDecoder(4096, nil)
		ce.decoders[connKey] = dec
	}

	for _, f := range frames {
		if f.Type == decoders.FrameHeaders {
			headerBlock := f.Payload
			padLength := 0
			if f.Flags&0x8 != 0 && len(headerBlock) > 0 { // PADDED
				padLength = int(headerBlock[0])
				headerBlock = headerBlock[1:]
			}
			if f.Flags&0x20 != 0 && len(headerBlock) >= 5 { // PRIORITY
				headerBlock = headerBlock[5:]
			}
			if padLength > 0 && len(headerBlock) >= padLength {
				headerBlock = headerBlock[:len(headerBlock)-padLength]
			}

			hfList, err := dec.DecodeFull(headerBlock)
			if err == nil {
				for _, h := range hfList {
					if strings.ToLower(h.Name) == "traceparent" {
						if tc, err := ParseTraceparent(h.Value); err == nil {
							return tc, true
						}
					}
				}
			}
		}
	}

	return TraceContext{}, false
}
