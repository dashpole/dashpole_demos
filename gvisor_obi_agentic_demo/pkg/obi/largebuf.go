// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package obi

import (
	"bytes"
	"fmt"
	"io"
	"sync"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
)

// LargeBuffer assembles chunked ring-buffer events into a contiguous byte stream.
// Modeled after go.opentelemetry.io/obi/pkg/internal/largebuf.
type LargeBuffer struct {
	chunks [][]byte
	total  int
}

// NewLargeBuffer returns an empty LargeBuffer.
func NewLargeBuffer() *LargeBuffer {
	return &LargeBuffer{
		chunks: make([][]byte, 0, 4),
	}
}

// NewLargeBufferFrom wraps b as a single-chunk LargeBuffer without unnecessary allocation.
func NewLargeBufferFrom(b []byte) *LargeBuffer {
	copied := make([]byte, len(b))
	copy(copied, b)
	return &LargeBuffer{
		chunks: [][]byte{copied},
		total:  len(b),
	}
}

// AppendChunk appends a byte slice, copying into Go-owned memory.
func (lb *LargeBuffer) AppendChunk(data []byte) {
	if len(data) == 0 {
		return
	}
	buf := make([]byte, len(data))
	copy(buf, data)
	lb.chunks = append(lb.chunks, buf)
	lb.total += len(data)
}

// Len returns total assembled bytes.
func (lb *LargeBuffer) Len() int {
	return lb.total
}

// Bytes returns the complete contiguous byte slice.
func (lb *LargeBuffer) Bytes() []byte {
	if len(lb.chunks) == 0 {
		return nil
	}
	if len(lb.chunks) == 1 {
		return lb.chunks[0]
	}
	res := make([]byte, lb.total)
	off := 0
	for _, c := range lb.chunks {
		copy(res[off:], c)
		off += len(c)
	}
	return res
}

// Reader returns an io.Reader over the chunks.
func (lb *LargeBuffer) Reader() io.Reader {
	if len(lb.chunks) == 0 {
		return bytes.NewReader(nil)
	}
	if len(lb.chunks) == 1 {
		return bytes.NewReader(lb.chunks[0])
	}
	readers := make([]io.Reader, len(lb.chunks))
	for i, c := range lb.chunks {
		readers[i] = bytes.NewReader(c)
	}
	return io.MultiReader(readers...)
}

// ChunkReassembler manages active in-flight multi-chunk payload streams.
type ChunkReassembler struct {
	mu      sync.Mutex
	streams map[uint64]*LargeBuffer
}

// NewChunkReassembler creates a new reassembly manager.
func NewChunkReassembler() *ChunkReassembler {
	return &ChunkReassembler{
		streams: make(map[uint64]*LargeBuffer),
	}
}

// IngestChunk processes a record chunk. Returns (buffer, isComplete, error).
func (cr *ChunkReassembler) IngestChunk(streamID uint64, flags uint16, payload []byte) (*LargeBuffer, bool, error) {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	switch flags {
	case abi.ChunkFlagSingle:
		return NewLargeBufferFrom(payload), true, nil

	case abi.ChunkFlagStart:
		buf := NewLargeBuffer()
		buf.AppendChunk(payload)
		cr.streams[streamID] = buf
		return nil, false, nil

	case abi.ChunkFlagCont:
		buf, exists := cr.streams[streamID]
		if !exists {
			return nil, false, fmt.Errorf("received continuation chunk for unknown stream ID %d", streamID)
		}
		buf.AppendChunk(payload)
		return nil, false, nil

	case abi.ChunkFlagEnd:
		buf, exists := cr.streams[streamID]
		if !exists {
			return nil, false, fmt.Errorf("received end chunk for unknown stream ID %d", streamID)
		}
		buf.AppendChunk(payload)
		delete(cr.streams, streamID)
		return buf, true, nil

	default:
		return NewLargeBufferFrom(payload), true, nil
	}
}
