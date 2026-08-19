// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package obi

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
)

func TestLargeBuffer_MultiChunk(t *testing.T) {
	lb := NewLargeBuffer()
	chunk1 := []byte("chunk1-")
	chunk2 := []byte("chunk2-")
	chunk3 := []byte("chunk3")

	lb.AppendChunk(chunk1)
	lb.AppendChunk(chunk2)
	lb.AppendChunk(chunk3)

	expected := "chunk1-chunk2-chunk3"
	if lb.Len() != len(expected) {
		t.Fatalf("expected length %d, got %d", len(expected), lb.Len())
	}
	if string(lb.Bytes()) != expected {
		t.Fatalf("expected %q, got %q", expected, string(lb.Bytes()))
	}

	reader := lb.Reader()
	data, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("ReadAll error: %v", err)
	}
	if string(data) != expected {
		t.Fatalf("Reader output mismatch: expected %q, got %q", expected, string(data))
	}
}

func TestChunkReassembler_Lifecycle(t *testing.T) {
	cr := NewChunkReassembler()
	streamID := uint64(1001)

	fullData := make([]byte, 64*1024) // 64KB
	_, _ = rand.Read(fullData)

	chunkSize := 16 * 1024
	numChunks := len(fullData) / chunkSize

	for i := 0; i < numChunks; i++ {
		start := i * chunkSize
		end := start + chunkSize
		chunk := fullData[start:end]

		var flags uint16
		if i == 0 {
			flags = abi.ChunkFlagStart
		} else if i == numChunks-1 {
			flags = abi.ChunkFlagEnd
		} else {
			flags = abi.ChunkFlagCont
		}

		buf, done, err := cr.IngestChunk(streamID, flags, chunk)
		if err != nil {
			t.Fatalf("IngestChunk error on chunk %d: %v", i, err)
		}
		if i == numChunks-1 {
			if !done || buf == nil {
				t.Fatalf("expected stream completion on chunk %d", i)
			}
			if !bytes.Equal(buf.Bytes(), fullData) {
				t.Fatalf("reassembled data does not match original data")
			}
		} else {
			if done || buf != nil {
				t.Fatalf("premature completion on chunk %d", i)
			}
		}
	}
}
