// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package ringbuf

import (
	"fmt"
	"os"
	"runtime"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
	"golang.org/x/sys/unix"
)

// Record represents a single telemetry event read from the ring buffer.
type Record struct {
	MsgType uint16
	Flags   uint16
	Payload []byte
}

// ReaderConfig configures the OBI RingBuffer Reader.
type ReaderConfig struct {
	FilePath      string
	DataSizeBytes uint64
	BatchTimeout  time.Duration
	EventFD       *os.File
}

// RingBufReader represents the consumer side inside OBI.
type RingBufReader struct {
	cfg        ReaderConfig
	ring       *MappedRing
	epollFd    int
	eventFdNum int
	closed     atomic.Bool
	readCount  atomic.Uint64
}

// NewRingBufReader initializes a new RingBufReader for the given ringbuffer file.
func NewRingBufReader(cfg ReaderConfig) (*RingBufReader, error) {
	if cfg.DataSizeBytes == 0 {
		cfg.DataSizeBytes = 4 * 1024 * 1024 // Default 4MB
	}
	if cfg.BatchTimeout == 0 {
		cfg.BatchTimeout = 50 * time.Millisecond
	}

	ring, err := CreateAndMapSharedRing(cfg.FilePath, cfg.DataSizeBytes, false)
	if err != nil {
		return nil, fmt.Errorf("failed to map ringbuffer memory for reading: %w", err)
	}

	epollFd := -1
	eventFdNum := -1
	if cfg.EventFD != nil {
		epollFd, err = unix.EpollCreate1(unix.EPOLL_CLOEXEC)
		if err == nil {
			eventFdNum = int(cfg.EventFD.Fd())
			event := unix.EpollEvent{
				Events: unix.EPOLLIN | unix.EPOLLET,
				Fd:     int32(eventFdNum),
			}
			_ = unix.EpollCtl(epollFd, unix.EPOLL_CTL_ADD, eventFdNum, &event)
		}
	}

	return &RingBufReader{
		cfg:        cfg,
		ring:       ring,
		epollFd:    epollFd,
		eventFdNum: eventFdNum,
	}, nil
}

// ConsumeRecord processes the next available record with the provided callback.
// The consumer position is advanced ONLY after handler returns, guaranteeing
// zero-copy memory safety against concurrent producer overwrites.
func (r *RingBufReader) ConsumeRecord(handler func(Record) error) (bool, error) {
	if r.closed.Load() {
		return false, fmt.Errorf("reader closed")
	}

	cons := atomic.LoadUint64(r.ring.ConsumerPos)
	prod := atomic.LoadUint64(r.ring.ProducerPos)

	if cons >= prod {
		return false, nil // Empty
	}

	offset := cons & r.ring.Mask
	hdrPtr := (*abi.RecordHeader)(unsafe.Pointer(&r.ring.Data[offset]))

	rawLen := atomic.LoadUint32(&hdrPtr.Len)

	// If BUSY_BIT is set or rawLen is 0 (producer still reserving/writing), spin briefly
	if rawLen == 0 || (rawLen&abi.BPF_RINGBUF_BUSY_BIT != 0) {
		for i := 0; i < 500; i++ {
			runtime.Gosched()
			rawLen = atomic.LoadUint32(&hdrPtr.Len)
			if rawLen != 0 && (rawLen&abi.BPF_RINGBUF_BUSY_BIT == 0) {
				break
			}
		}
		if rawLen == 0 || (rawLen&abi.BPF_RINGBUF_BUSY_BIT != 0) {
			// Producer has not finished committing; DO NOT advance consumer position
			return false, nil
		}
	}

	payloadLen := int(rawLen & abi.BPF_RINGBUF_LEN_MASK)
	totalLen := uint64(abi.RecordHeaderSize + abi.Align8(payloadLen))

	// Check if discarded
	if rawLen&abi.BPF_RINGBUF_DISCARD_BIT != 0 {
		atomic.StoreUint32(&hdrPtr.Len, 0) // Clear header for next wraparound
		atomic.StoreUint64(r.ring.ConsumerPos, cons+totalLen)
		return r.ConsumeRecord(handler) // Skip discarded and get next
	}

	rec := Record{
		MsgType: hdrPtr.MsgType,
		Flags:   hdrPtr.Flags,
		// Zero-copy slice view into double-mapped ring
		Payload: r.ring.Data[offset+uint64(abi.RecordHeaderSize) : offset+uint64(abi.RecordHeaderSize)+uint64(payloadLen)],
	}

	if err := handler(rec); err != nil {
		return false, err
	}

	// Clear header Len so future wraparounds don't see stale committed length
	atomic.StoreUint32(&hdrPtr.Len, 0)

	// Advance consumer position only after handler has safely read the record
	atomic.StoreUint64(r.ring.ConsumerPos, cons+totalLen)
	r.readCount.Add(1)

	return true, nil
}

// ReadRecord copies the next record into rec and advances the consumer position.
func (r *RingBufReader) ReadRecord(rec *Record) (bool, error) {
	return r.ConsumeRecord(func(src Record) error {
		rec.MsgType = src.MsgType
		rec.Flags = src.Flags
		if cap(rec.Payload) >= len(src.Payload) {
			rec.Payload = rec.Payload[:len(src.Payload)]
		} else {
			rec.Payload = make([]byte, len(src.Payload))
		}
		copy(rec.Payload, src.Payload)
		return nil
	})
}

// DrainAll drains all currently available records without blocking.
func (r *RingBufReader) DrainAll(handler func(Record) error) (int, error) {
	count := 0
	for {
		ok, err := r.ConsumeRecord(handler)
		if err != nil {
			return count, err
		}
		if !ok {
			break
		}
		count++
	}
	return count, nil
}

// WaitForEvents waits for new events on the eventfd or times out.
func (r *RingBufReader) WaitForEvents(timeout time.Duration) (bool, error) {
	if r.epollFd < 0 {
		time.Sleep(timeout)
		return true, nil
	}

	var events [1]unix.EpollEvent
	timeoutMs := int(timeout.Milliseconds())
	if timeoutMs <= 0 {
		timeoutMs = 1
	}

	n, err := unix.EpollWait(r.epollFd, events[:], timeoutMs)
	if err != nil {
		if err == unix.EINTR {
			return false, nil
		}
		return false, err
	}
	if n > 0 {
		// Drain eventfd counter
		var buf [8]byte
		_, _ = unix.Read(r.eventFdNum, buf[:])
		return true, nil
	}
	return false, nil
}

// ReadCount returns total records consumed.
func (r *RingBufReader) ReadCount() uint64 {
	return r.readCount.Load()
}

// Close closes the reader and unmaps memory.
func (r *RingBufReader) Close() error {
	if r.closed.Swap(true) {
		return nil
	}
	if r.epollFd >= 0 {
		_ = unix.Close(r.epollFd)
		r.epollFd = -1
	}
	return r.ring.Close()
}
