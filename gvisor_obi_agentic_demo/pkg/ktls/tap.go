// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package ktls

import (
	"encoding/binary"
	"net"
	"time"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ringbuf"
)

// SocketTuple describes the 5-tuple of a monitored connection.
type SocketTuple struct {
	SrcIP   net.IP
	SrcPort uint16
	DstIP   net.IP
	DstPort uint16
}

// TelemetryHeader is the fixed binary header for intercepted kTLS streams.
// Wire Format:
// [TimestampNS (8B)][PID (4B)][TID (4B)][SrcPort (2B)][DstPort (2B)][IPVersion (1B)][Flags (1B)][SrcIP (16B)][DstIP (16B)][PayloadLength (4B)][Payload...]
const TelemetryHeaderSize = 8 + 4 + 4 + 2 + 2 + 1 + 1 + 16 + 16 + 4 // 58 bytes

// SerializeTelemetryEvent packs connection metadata and plaintext payload into a wire record.
func SerializeTelemetryEvent(tuple SocketTuple, pid, tid uint32, payload []byte) []byte {
	buf := make([]byte, TelemetryHeaderSize+len(payload))

	// Timestamp
	binary.LittleEndian.PutUint64(buf[0:8], uint64(time.Now().UnixNano()))
	binary.LittleEndian.PutUint32(buf[8:12], pid)
	binary.LittleEndian.PutUint32(buf[12:16], tid)
	binary.BigEndian.PutUint16(buf[16:18], tuple.SrcPort)
	binary.BigEndian.PutUint16(buf[18:20], tuple.DstPort)

	isIPv6 := len(tuple.SrcIP) == net.IPv6len && tuple.SrcIP.To4() == nil
	if isIPv6 {
		buf[20] = 6
	} else {
		buf[20] = 4
	}
	buf[21] = 0 // Flags / reserved

	// Copy 16-byte IP representations (IPv4-mapped if IPv4)
	src16 := tuple.SrcIP.To16()
	if src16 != nil {
		copy(buf[22:38], src16)
	}
	dst16 := tuple.DstIP.To16()
	if dst16 != nil {
		copy(buf[38:54], dst16)
	}

	binary.LittleEndian.PutUint32(buf[54:58], uint32(len(payload)))
	copy(buf[58:], payload)

	return buf
}

// DeserializeTelemetryEvent extracts connection metadata and payload from a raw wire slice.
func DeserializeTelemetryEvent(data []byte) (tuple SocketTuple, pid, tid uint32, tsNS uint64, payload []byte, ok bool) {
	if len(data) < TelemetryHeaderSize {
		return tuple, 0, 0, 0, nil, false
	}

	tsNS = binary.LittleEndian.Uint64(data[0:8])
	pid = binary.LittleEndian.Uint32(data[8:12])
	tid = binary.LittleEndian.Uint32(data[12:16])
	tuple.SrcPort = binary.BigEndian.Uint16(data[16:18])
	tuple.DstPort = binary.BigEndian.Uint16(data[18:20])

	ipVer := data[20]
	if ipVer == 4 {
		tuple.SrcIP = net.IP(data[22:38]).To4()
		tuple.DstIP = net.IP(data[38:54]).To4()
	} else {
		tuple.SrcIP = net.IP(data[22:38])
		tuple.DstIP = net.IP(data[38:54])
	}

	payloadLen := binary.LittleEndian.Uint32(data[54:58])
	if len(data) < TelemetryHeaderSize+int(payloadLen) {
		return tuple, 0, 0, 0, nil, false
	}

	payload = data[58 : 58+int(payloadLen)]
	return tuple, pid, tid, tsNS, payload, true
}

// TelemetrySink is an abstraction for intercepting plaintext streams.
type TelemetrySink interface {
	EmitKTLSTx(tuple SocketTuple, pid, tid uint32, payload []byte) bool
	EmitKTLSRx(tuple SocketTuple, pid, tid uint32, payload []byte) bool
}

// RingBufTelemetrySink adapts RingBufSink into TelemetrySink.
type RingBufTelemetrySink struct {
	sink *ringbuf.RingBufSink
}

// NewRingBufTelemetrySink creates a new adapter.
func NewRingBufTelemetrySink(sink *ringbuf.RingBufSink) *RingBufTelemetrySink {
	return &RingBufTelemetrySink{sink: sink}
}

func (s *RingBufTelemetrySink) EmitKTLSTx(tuple SocketTuple, pid, tid uint32, payload []byte) bool {
	if s.sink == nil {
		return false
	}
	record := SerializeTelemetryEvent(tuple, pid, tid, payload)
	return s.sink.ReserveAndCommit(abi.MsgTypeKTLSTx, abi.ChunkFlagSingle, record)
}

func (s *RingBufTelemetrySink) EmitKTLSRx(tuple SocketTuple, pid, tid uint32, payload []byte) bool {
	if s.sink == nil {
		return false
	}
	record := SerializeTelemetryEvent(tuple, pid, tid, payload)
	return s.sink.ReserveAndCommit(abi.MsgTypeKTLSRx, abi.ChunkFlagSingle, record)
}
