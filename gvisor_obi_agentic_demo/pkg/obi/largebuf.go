// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package obi

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
)

var (
	ErrStreamTooLarge = errors.New("largebuf: stream exceeds maximum allowed buffer size")
	ErrChunkOutOfOrder = errors.New("largebuf: chunk received out of sequence")
)

const (
	// DefaultMaxStreamSize limits single multi-chunk stream to 32MB to prevent memory exhaustion
	DefaultMaxStreamSize = 32 * 1024 * 1024
	// DefaultStreamTTL is the maximum inactivity duration before abandoned streams are evicted
	DefaultStreamTTL = 60 * time.Second
)

// LargeBuffer represents a multi-chunk reassembled payload.
type LargeBuffer struct {
	chunks     [][]byte
	totalLen   int
	lastActive time.Time
}

// NewLargeBuffer initializes an empty LargeBuffer.
func NewLargeBuffer() *LargeBuffer {
	return &LargeBuffer{
		chunks:     make([][]byte, 0, 8),
		totalLen:   0,
		lastActive: time.Now(),
	}
}

// AppendChunk adds a chunk slice to the buffer.
func (lb *LargeBuffer) AppendChunk(chunk []byte) {
	if len(chunk) == 0 {
		return
	}
	cp := make([]byte, len(chunk))
	copy(cp, chunk)
	lb.chunks = append(lb.chunks, cp)
	lb.totalLen += len(chunk)
	lb.lastActive = time.Now()
}

// Len returns total assembled byte length.
func (lb *LargeBuffer) Len() int {
	return lb.totalLen
}

// Bytes returns a contiguous byte slice of the assembled payload.
func (lb *LargeBuffer) Bytes() []byte {
	if len(lb.chunks) == 0 {
		return nil
	}
	if len(lb.chunks) == 1 {
		return lb.chunks[0]
	}
	res := make([]byte, lb.totalLen)
	offset := 0
	for _, c := range lb.chunks {
		copy(res[offset:], c)
		offset += len(c)
	}
	return res
}

// Reader returns an io.Reader over the chunked buffer without copying memory.
func (lb *LargeBuffer) Reader() io.Reader {
	readers := make([]io.Reader, len(lb.chunks))
	for i, c := range lb.chunks {
		readers[i] = bytes.NewReader(c)
	}
	return io.MultiReader(readers...)
}

// ChunkReassembler manages active in-flight multi-chunk payload reassemblies.
type ChunkReassembler struct {
	mu            sync.Mutex
	streams       map[uint64]*LargeBuffer
	maxStreamSize int
	streamTTL     time.Duration
}

// NewChunkReassembler creates a new reassembler.
func NewChunkReassembler() *ChunkReassembler {
	return &ChunkReassembler{
		streams:       make(map[uint64]*LargeBuffer),
		maxStreamSize: DefaultMaxStreamSize,
		streamTTL:     DefaultStreamTTL,
	}
}

// IngestChunk processes a single chunk record for a streamID.
func (cr *ChunkReassembler) IngestChunk(streamID uint64, flags uint16, chunk []byte) (*LargeBuffer, bool, error) {
	cr.mu.Lock()
	defer cr.mu.Unlock()

	// Evict stale streams periodically
	now := time.Now()
	for sID, stream := range cr.streams {
		if now.Sub(stream.lastActive) > cr.streamTTL {
			delete(cr.streams, sID)
		}
	}

	if flags == abi.ChunkFlagSingle {
		lb := NewLargeBuffer()
		lb.AppendChunk(chunk)
		return lb, true, nil
	}

	buf, exists := cr.streams[streamID]
	if !exists {
		if flags != abi.ChunkFlagStart {
			return nil, false, fmt.Errorf("%w: missing start chunk for stream %d", ErrChunkOutOfOrder, streamID)
		}
		buf = NewLargeBuffer()
		cr.streams[streamID] = buf
	}

	if buf.Len()+len(chunk) > cr.maxStreamSize {
		delete(cr.streams, streamID)
		return nil, false, fmt.Errorf("%w: stream %d size %d > max %d", ErrStreamTooLarge, streamID, buf.Len()+len(chunk), cr.maxStreamSize)
	}

	buf.AppendChunk(chunk)

	if flags&abi.ChunkFlagEnd != 0 {
		delete(cr.streams, streamID)
		return buf, true, nil
	}

	return nil, false, nil
}

// StreamCount returns the number of currently active in-flight streams.
func (cr *ChunkReassembler) StreamCount() int {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	return len(cr.streams)
}
