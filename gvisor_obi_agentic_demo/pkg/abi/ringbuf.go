// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package abi

import "unsafe"

const (
	// PageSize is the standard OS memory page size.
	PageSize = 4096

	// ControlPages is the number of control pages preceding the data ring buffer.
	// Page 0: Consumer Page (consumer_pos uint64)
	// Page 1: Producer Page (producer_pos uint64)
	ControlPages = 2

	// ControlHeaderSize is the byte size of control pages.
	ControlHeaderSize = ControlPages * PageSize

	// BPF Ring Buffer Header Flags (matching Linux kernel / bpf.h)
	BPF_RINGBUF_BUSY_BIT    = uint32(1 << 31) // 0x80000000: Write in progress
	BPF_RINGBUF_DISCARD_BIT = uint32(1 << 30) // 0x40000000: Sample discarded
	BPF_RINGBUF_LEN_MASK    = uint32(0x3FFFFFFF) // Max record length 1GB

	// RecordHeaderSize is the size of the 8-byte record header.
	RecordHeaderSize = 8
)

// RecordHeader is the 8-byte header preceding every record in the data ring buffer.
type RecordHeader struct {
	Len     uint32 // Bits 0..29: length; Bit 30: discard; Bit 31: busy
	MsgType uint16 // Custom telemetry message type
	Flags   uint16 // Reserved / Chunk flags
}

// Telemetry Message Types
const (
	MsgTypeUnknown        uint16 = 0
	MsgTypeKTLSTx         uint16 = 101 // Outbound plaintext data pre-encryption
	MsgTypeKTLSRx         uint16 = 102 // Inbound plaintext data post-decryption
	MsgTypeSocketConnect  uint16 = 103 // Socket connect event
	MsgTypeSocketClose    uint16 = 104 // Socket close event
	MsgTypeHTTP2Frame     uint16 = 105 // HTTP/2 raw multiplexed frame chunk
	MsgTypeSyntheticBench uint16 = 999 // Benchmark payload
)

// Chunk Flags for Large Payload Multi-Chunk Reassembly
const (
	ChunkFlagSingle uint16 = 0x0000 // Single independent record
	ChunkFlagStart  uint16 = 0x0001 // First chunk of multi-chunk stream
	ChunkFlagCont   uint16 = 0x0002 // Continuation chunk
	ChunkFlagEnd    uint16 = 0x0004 // Final chunk of multi-chunk stream
)

// Align8 rounds up a length to the nearest 8-byte boundary.
func Align8(n int) int {
	return (n + 7) &^ 7
}

// Align8Uint64 rounds up a uint64 to the nearest 8-byte boundary.
func Align8Uint64(n uint64) uint64 {
	return (n + 7) &^ 7
}

// RecordHeaderSizeOf returns the byte size of RecordHeader.
const RecordHeaderSizeOf = int(unsafe.Sizeof(RecordHeader{}))
