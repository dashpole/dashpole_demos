// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package decoders

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"

	"sync"

	"golang.org/x/net/http2/hpack"
)

var (
	ErrInvalidHTTP2Frame = errors.New("http2: invalid frame header")
)

// HTTP/2 Frame Types
const (
	FrameData         = 0x0
	FrameHeaders      = 0x1
	FramePriority     = 0x2
	FrameRSTStream    = 0x3
	FrameSettings     = 0x4
	FramePushPromise  = 0x5
	FramePing         = 0x6
	FrameGoAway       = 0x7
	FrameWindowUpdate = 0x8
	FrameContinuation = 0x9
)

// HTTP/2 Flags
const (
	FlagEndStream  = 0x1
	FlagEndHeaders = 0x4
)

// HTTP2Frame represents a parsed HTTP/2 wire frame.
type HTTP2Frame struct {
	Length   uint32
	Type     uint8
	Flags    uint8
	StreamID uint32
	Payload  []byte
}

// HTTP2StreamState maintains per-stream decoded headers and data.
type HTTP2StreamState struct {
	StreamID   uint32
	Method     string
	Path       string
	Host       string
	Status     int
	GRPCStatus int
	Headers    map[string]string
	Data       bytes.Buffer
	EndStream  bool
}

// HTTP2Decoder parses multiplexed HTTP/2 streams and decompresses HPACK headers.
type HTTP2Decoder struct {
	mu           sync.Mutex
	hpackDecoder *hpack.Decoder
	streams      map[uint32]*HTTP2StreamState
}

// NewHTTP2Decoder creates a new HTTP/2 decoder instance.
func NewHTTP2Decoder() *HTTP2Decoder {
	dec := &HTTP2Decoder{
		streams: make(map[uint32]*HTTP2StreamState),
	}
	dec.hpackDecoder = hpack.NewDecoder(4096, func(f hpack.HeaderField) {
		// Callback for decoded headers handled during frame parsing
	})
	return dec
}

// ActiveStreamCount returns the count of currently tracked in-flight streams.
func (d *HTTP2Decoder) ActiveStreamCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.streams)
}

// ParseFrames extracts individual HTTP/2 frames from a raw TCP slice.
// If client connection preface "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n" is present, it is skipped.
func ParseFrames(data []byte) ([]HTTP2Frame, error) {
	const magicPreface = "PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n"
	if strings.HasPrefix(string(data), magicPreface) {
		data = data[len(magicPreface):]
	}

	var frames []HTTP2Frame
	reader := bytes.NewReader(data)

	for reader.Len() >= 9 {
		var hdr [9]byte
		if _, err := io.ReadFull(reader, hdr[:]); err != nil {
			break
		}

		length := uint32(hdr[0])<<16 | uint32(hdr[1])<<8 | uint32(hdr[2])
		frameType := hdr[3]
		flags := hdr[4]
		streamID := binary.BigEndian.Uint32(hdr[5:9]) & 0x7FFFFFFF // Mask reserved bit

		if int(length) > reader.Len() {
			// Incomplete trailing frame
			break
		}

		payload := make([]byte, length)
		if length > 0 {
			if _, err := io.ReadFull(reader, payload); err != nil {
				return nil, err
			}
		}

		frames = append(frames, HTTP2Frame{
			Length:   length,
			Type:     frameType,
			Flags:    flags,
			StreamID: streamID,
			Payload:  payload,
		})
	}

	return frames, nil
}

// DecodeFrames feeds frames into the stream demuxer and returns any streams completed by END_STREAM.
func (d *HTTP2Decoder) DecodeFrames(frames []HTTP2Frame) ([]*HTTP2StreamState, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	var completed []*HTTP2StreamState

	for _, f := range frames {
		if f.StreamID == 0 {
			// Control frames (SETTINGS, PING, WINDOW_UPDATE on stream 0)
			continue
		}

		stream, exists := d.streams[f.StreamID]
		if !exists {
			stream = &HTTP2StreamState{
				StreamID: f.StreamID,
				Headers:  make(map[string]string),
			}
			d.streams[f.StreamID] = stream
		}

		switch f.Type {
		case FrameHeaders, FrameContinuation:
			headerBlock := f.Payload
			// If HEADERS has padding or priority, strip them
			if f.Type == FrameHeaders && len(headerBlock) > 0 {
				padLength := 0
				if f.Flags&0x8 != 0 { // PADDED
					padLength = int(headerBlock[0])
					headerBlock = headerBlock[1:]
				}
				if f.Flags&0x20 != 0 { // PRIORITY
					if len(headerBlock) >= 5 {
						headerBlock = headerBlock[5:]
					}
				}
				if padLength > 0 && len(headerBlock) >= padLength {
					headerBlock = headerBlock[:len(headerBlock)-padLength]
				}
			}

			// Decompress HPACK headers
			hfList, err := d.hpackDecoder.DecodeFull(headerBlock)
			if err == nil {
				for _, hf := range hfList {
					stream.Headers[hf.Name] = hf.Value
					switch hf.Name {
					case ":method":
						stream.Method = hf.Value
					case ":path":
						stream.Path = hf.Value
					case ":authority":
						stream.Host = hf.Value
					case "host":
						if stream.Host == "" {
							stream.Host = hf.Value
						}
					case ":status":
						if s, err := fmt.Sscanf(hf.Value, "%d", &stream.Status); err != nil || s == 0 {
							stream.Status = 200
						}
					case "grpc-status":
						_, _ = fmt.Sscanf(hf.Value, "%d", &stream.GRPCStatus)
					}
				}
			}

			if f.Flags&FlagEndStream != 0 {
				stream.EndStream = true
				completed = append(completed, stream)
				delete(d.streams, f.StreamID)
			}

		case FrameData:
			dataPayload := f.Payload
			if f.Flags&0x8 != 0 && len(dataPayload) > 0 { // PADDED
				padLength := int(dataPayload[0])
				if len(dataPayload) >= 1+padLength {
					dataPayload = dataPayload[1 : len(dataPayload)-padLength]
				}
			}
			stream.Data.Write(dataPayload)

			if f.Flags&FlagEndStream != 0 {
				stream.EndStream = true
				completed = append(completed, stream)
				delete(d.streams, f.StreamID)
			}

		case FrameRSTStream:
			stream.EndStream = true
			completed = append(completed, stream)
			delete(d.streams, f.StreamID)
		}
	}

	return completed, nil
}
