// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package obi

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"path/filepath"
	"testing"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ringbuf"
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

func TestRingBuf_LargeBufReassembly(t *testing.T) {
	dir := t.TempDir()
	shmPath := filepath.Join(dir, "largebuf.shm")

	sink, err := ringbuf.NewRingBufSink(ringbuf.SinkConfig{
		FilePath:      shmPath,
		DataSizeBytes: 4 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewRingBufSink failed: %v", err)
	}
	defer sink.Close()

	reader, err := ringbuf.NewRingBufReader(ringbuf.ReaderConfig{
		FilePath:      shmPath,
		DataSizeBytes: 4 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewRingBufReader failed: %v", err)
	}
	defer reader.Close()

	reassembler := NewChunkReassembler()

	// Simulate a 128KB GenAI prompt chunked into 16KB records
	totalPromptSize := 128 * 1024
	prompt := make([]byte, totalPromptSize)
	_, _ = rand.Read(prompt)

	chunkSize := 16 * 1024
	streamID := uint64(42)

	// Write chunks to ring buffer
	numChunks := totalPromptSize / chunkSize
	for i := 0; i < numChunks; i++ {
		start := i * chunkSize
		end := start + chunkSize
		chunkData := prompt[start:end]

		var flags uint16
		if i == 0 {
			flags = abi.ChunkFlagStart
		} else if i == numChunks-1 {
			flags = abi.ChunkFlagEnd
		} else {
			flags = abi.ChunkFlagCont
		}

		wirePayload := make([]byte, 8+len(chunkData))
		binary.LittleEndian.PutUint64(wirePayload[0:8], streamID)
		copy(wirePayload[8:], chunkData)

		sink.ReserveAndCommit(abi.MsgTypeKTLSTx, flags, wirePayload)
	}

	// Consume and reassemble
	var assembledBuf *LargeBuffer
	var completed bool

	for {
		var rec ringbuf.Record
		ok, err := reader.ReadRecord(&rec)
		if err != nil {
			t.Fatalf("ReadRecord failed: %v", err)
		}
		if !ok {
			break
		}

		sID := binary.LittleEndian.Uint64(rec.Payload[0:8])
		chunkPayload := rec.Payload[8:]

		buf, done, err := reassembler.IngestChunk(sID, rec.Flags, chunkPayload)
		if err != nil {
			t.Fatalf("IngestChunk error: %v", err)
		}
		if done {
			assembledBuf = buf
			completed = true
			break
		}
	}

	if !completed || assembledBuf == nil {
		t.Fatalf("LargeBuffer reassembly failed to complete")
	}

	if assembledBuf.Len() != totalPromptSize {
		t.Fatalf("Reassembled length mismatch: expected %d, got %d", totalPromptSize, assembledBuf.Len())
	}

	if !bytes.Equal(assembledBuf.Bytes(), prompt) {
		t.Fatalf("Reassembled prompt content mismatch with original 128KB payload")
	}
	t.Logf("LargeBuffer successfully reassembled %d-byte prompt across %d chunks", totalPromptSize, numChunks)
}
