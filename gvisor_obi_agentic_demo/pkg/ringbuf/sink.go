// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package ringbuf

import (
	"encoding/binary"
	"fmt"
	"os"
	"sync/atomic"
	"unsafe"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
	"golang.org/x/sys/unix"
)

// SinkConfig configures the Sentry RingBuffer Sink.
type SinkConfig struct {
	FilePath       string
	DataSizeBytes  uint64
	WatermarkBytes uint64
	EventFDPath    string
}

// RingBufSink represents the producer side inside gVisor Sentry.
type RingBufSink struct {
	cfg          SinkConfig
	ring         *MappedRing
	eventFD      *os.File
	droppedCount atomic.Uint64
	eventsTotal  atomic.Uint64
	bytesTotal   atomic.Uint64
	closed       atomic.Bool
}

// NewRingBufSink initializes a new RingBufSink with double-mapped memory.
func NewRingBufSink(cfg SinkConfig) (*RingBufSink, error) {
	if cfg.DataSizeBytes == 0 {
		cfg.DataSizeBytes = 4 * 1024 * 1024 // Default 4MB
	}
	if cfg.WatermarkBytes == 0 {
		cfg.WatermarkBytes = 64 * 1024 // Default 64KB
	}

	ring, err := CreateAndMapSharedRing(cfg.FilePath, cfg.DataSizeBytes, true)
	if err != nil {
		return nil, fmt.Errorf("failed to create ringbuffer memory: %w", err)
	}

	var eventFD *os.File
	if cfg.EventFDPath != "" {
		efd, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
		if err == nil {
			eventFD = os.NewFile(uintptr(efd), cfg.EventFDPath)
		}
	}

	return &RingBufSink{
		cfg:     cfg,
		ring:    ring,
		eventFD: eventFD,
	}, nil
}

// ReserveAndCommit atomically reserves space, writes header & payload, and commits.
// If the buffer is saturated, it drops the record immediately and returns false (non-blocking).
func (s *RingBufSink) ReserveAndCommit(msgType uint16, flags uint16, payload []byte) bool {
	if s.closed.Load() {
		return false
	}

	payloadLen := len(payload)
	totalLen := uint64(abi.RecordHeaderSize + abi.Align8(payloadLen))

	for {
		prod := atomic.LoadUint64(s.ring.ProducerPos)
		cons := atomic.LoadUint64(s.ring.ConsumerPos)

		// Check capacity: (prod - cons) + totalLen > DataSize
		if (prod-cons)+totalLen > s.ring.DataSize {
			s.droppedCount.Add(1)
			return false
		}

		// Attempt atomic reservation
		if atomic.CompareAndSwapUint64(s.ring.ProducerPos, prod, prod+totalLen) {
			offset := prod & s.ring.Mask

			// Write 8-byte header with BUSY bit set
			hdrPtr := (*abi.RecordHeader)(unsafe.Pointer(&s.ring.Data[offset]))
			hdrPtr.Len = uint32(payloadLen) | abi.BPF_RINGBUF_BUSY_BIT
			hdrPtr.MsgType = msgType
			hdrPtr.Flags = flags

			// Copy payload into double-mapped buffer (never splits across boundary)
			copy(s.ring.Data[offset+uint64(abi.RecordHeaderSize):offset+uint64(abi.RecordHeaderSize)+uint64(payloadLen)], payload)

			// Pad trailing bytes with zeroes
			padLen := abi.Align8(payloadLen) - payloadLen
			if padLen > 0 {
				padStart := offset + uint64(abi.RecordHeaderSize) + uint64(payloadLen)
				clearSlice := s.ring.Data[padStart : padStart+uint64(padLen)]
				for i := range clearSlice {
					clearSlice[i] = 0
				}
			}

			// Atomic commit: clear BUSY bit
			atomic.StoreUint32(&hdrPtr.Len, uint32(payloadLen))

			s.eventsTotal.Add(1)
			s.bytesTotal.Add(totalLen)

			// Notify consumer via eventfd if unconsumed bytes exceed watermark
			newProd := prod + totalLen
			if (newProd - cons) >= s.cfg.WatermarkBytes {
				s.NotifyConsumer()
			}

			return true
		}
	}
}

// NotifyConsumer sends a 64-bit value to the eventfd to wake up the consumer.
func (s *RingBufSink) NotifyConsumer() {
	if s.eventFD != nil {
		var val [8]byte
		binary.LittleEndian.PutUint64(val[:], 1)
		_, _ = unix.Write(int(s.eventFD.Fd()), val[:])
	}
}

// Stats returns the sink metrics.
func (s *RingBufSink) Stats() (events uint64, bytes uint64, dropped uint64) {
	return s.eventsTotal.Load(), s.bytesTotal.Load(), s.droppedCount.Load()
}

// EventFD returns the companion eventfd if configured.
func (s *RingBufSink) EventFD() *os.File {
	return s.eventFD
}

// Close closes the sink and backing memory.
func (s *RingBufSink) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	if s.eventFD != nil {
		_ = s.eventFD.Close()
		s.eventFD = nil
	}
	return s.ring.Close()
}
