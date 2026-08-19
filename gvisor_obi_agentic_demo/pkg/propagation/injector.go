// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package propagation

import (
	"bytes"
	"encoding/binary"
	"strings"
	"sync"

	"golang.org/x/net/http2/hpack"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/decoders"
)

const (
	MaxHTTP2FrameSize = 16384
)

// ContextInjector handles in-flight injection of W3C trace context into HTTP/1.1 and HTTP/2 payloads.
type ContextInjector struct {
	mu       sync.Mutex
	decoders map[string]*hpack.Decoder
}

// NewContextInjector creates a new context injector.
func NewContextInjector() *ContextInjector {
	return &ContextInjector{
		decoders: make(map[string]*hpack.Decoder),
	}
}

func isHTTP1Request(payload []byte) bool {
	if len(payload) < 4 {
		return false
	}
	methods := []string{"GET ", "POST ", "PUT ", "DELETE ", "HEAD ", "OPTIONS ", "PATCH "}
	for _, m := range methods {
		if bytes.HasPrefix(payload, []byte(m)) {
			return true
		}
	}
	return false
}

// InjectHTTP1 injects a traceparent header into an HTTP/1.1 request plaintext payload.
func (ci *ContextInjector) InjectHTTP1(payload []byte, tc TraceContext) []byte {
	if !isHTTP1Request(payload) {
		return payload
	}

	// Locate header/body boundary
	headerEnd := bytes.Index(payload, []byte("\r\n\r\n"))
	var headerBytes, bodyBytes []byte
	if headerEnd != -1 {
		headerBytes = payload[:headerEnd+4]
		bodyBytes = payload[headerEnd+4:]
	} else {
		headerBytes = payload
	}

	// Check if headers already contain traceparent
	lowerHdr := bytes.ToLower(headerBytes)
	if bytes.Contains(lowerHdr, []byte("\r\ntraceparent:")) || bytes.HasPrefix(lowerHdr, []byte("traceparent:")) {
		return payload
	}

	firstLineEnd := bytes.Index(headerBytes, []byte("\r\n"))
	if firstLineEnd == -1 {
		return payload
	}

	headerLine := []byte("traceparent: " + tc.FormatTraceparent() + "\r\n")

	var res bytes.Buffer
	res.Grow(len(payload) + len(headerLine))
	res.Write(headerBytes[:firstLineEnd+2])
	res.Write(headerLine)
	res.Write(headerBytes[firstLineEnd+2:])
	if len(bodyBytes) > 0 {
		res.Write(bodyBytes)
	}

	return res.Bytes()
}

// InjectHTTP2 injects a traceparent header field into HTTP/2 HEADERS frames.
func (ci *ContextInjector) InjectHTTP2(connKey string, data []byte, tc TraceContext) ([]byte, error) {
	ci.mu.Lock()
	defer ci.mu.Unlock()

	frames, err := decoders.ParseFrames(data)
	if err != nil || len(frames) == 0 {
		return data, err
	}

	dec, exists := ci.decoders[connKey]
	if !exists {
		dec = hpack.NewDecoder(4096, nil)
		ci.decoders[connKey] = dec
	}

	var out bytes.Buffer
	const magicPreface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"
	if strings.HasPrefix(string(data), magicPreface) {
		out.WriteString(magicPreface)
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

			// Decompress headers
			hfList, err := dec.DecodeFull(headerBlock)
			if err == nil {
				// Append traceparent header
				hasTP := false
				for _, h := range hfList {
					if strings.ToLower(h.Name) == "traceparent" {
						hasTP = true
						break
					}
				}
				if !hasTP {
					hfList = append(hfList, hpack.HeaderField{
						Name:  "traceparent",
						Value: tc.FormatTraceparent(),
					})
				}

				// Re-encode HPACK using literal without indexing to avoid client/server dynamic table corruption
				var newHpack bytes.Buffer
				enc := hpack.NewEncoder(&newHpack)
				for _, h := range hfList {
					_ = enc.WriteField(h)
				}
				newPayload := newHpack.Bytes()

				// Strip PADDED and PRIORITY flags on reinjected clean header
				cleanFlags := f.Flags &^ 0x8 &^ 0x20

				// Check frame size limit
				if len(newPayload) <= MaxHTTP2FrameSize {
					hdr := make([]byte, 9)
					hdr[0] = byte(len(newPayload) >> 16)
					hdr[1] = byte(len(newPayload) >> 8)
					hdr[2] = byte(len(newPayload))
					hdr[3] = f.Type
					hdr[4] = cleanFlags
					binary.BigEndian.PutUint32(hdr[5:9], f.StreamID)

					out.Write(hdr)
					out.Write(newPayload)
					continue
				}
			}
		}

		// Write unchanged frame
		hdr := make([]byte, 9)
		hdr[0] = byte(len(f.Payload) >> 16)
		hdr[1] = byte(len(f.Payload) >> 8)
		hdr[2] = byte(len(f.Payload))
		hdr[3] = f.Type
		hdr[4] = f.Flags
		binary.BigEndian.PutUint32(hdr[5:9], f.StreamID)

		out.Write(hdr)
		out.Write(f.Payload)
	}

	return out.Bytes(), nil
}
