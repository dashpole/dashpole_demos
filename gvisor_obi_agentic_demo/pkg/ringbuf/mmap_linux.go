// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

//go:build linux

package ringbuf

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
	"golang.org/x/sys/unix"
)

// MappedRing represents a double-mapped shared memory ring buffer.
type MappedRing struct {
	BaseAddr      uintptr
	TotalMmapSize uintptr
	DataSize      uint64
	Mask          uint64
	File          *os.File

	// Pointers into mapped memory
	ConsumerPos *uint64 // Offset 0x0000 (Page 0)
	ProducerPos *uint64 // Offset 0x1000 (Page 1)
	Data        []byte  // Data ring slice (length = 2 * DataSize)
}

// CreateAndMapSharedRing creates the underlying shared file and maps it with double virtual mapping.
func CreateAndMapSharedRing(path string, dataSize uint64, writable bool) (*MappedRing, error) {
	if dataSize == 0 || (dataSize&(dataSize-1)) != 0 {
		return nil, fmt.Errorf("dataSize must be a power of 2, got %d", dataSize)
	}

	flags := os.O_RDWR
	if writable {
		flags |= os.O_CREATE
	}
	file, err := os.OpenFile(path, flags, 0660)
	if err != nil {
		return nil, fmt.Errorf("failed to open ringbuffer file %s: %w", path, err)
	}

	physicalSize := int64(abi.ControlHeaderSize + dataSize)
	if writable {
		if err := file.Truncate(physicalSize); err != nil {
			file.Close()
			return nil, fmt.Errorf("failed to truncate ringbuffer file to %d bytes: %w", physicalSize, err)
		}
	} else {
		fi, err := file.Stat()
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("failed to stat ringbuffer file: %w", err)
		}
		if fi.Size() < physicalSize {
			file.Close()
			return nil, fmt.Errorf("ringbuffer file size %d smaller than expected %d", fi.Size(), physicalSize)
		}
	}

	totalVirtualSize := uintptr(abi.ControlHeaderSize + 2*dataSize)

	// Step 1: Reserve continuous virtual address space
	r1, _, errno := syscall.Syscall6(
		syscall.SYS_MMAP,
		0,
		totalVirtualSize,
		uintptr(syscall.PROT_NONE),
		uintptr(syscall.MAP_ANONYMOUS|syscall.MAP_PRIVATE),
		^uintptr(0),
		0,
	)
	if errno != 0 {
		file.Close()
		return nil, fmt.Errorf("failed to reserve virtual address space of size %d: %v", totalVirtualSize, errno)
	}
	reserveAddr := r1
	basePtr := *(*unsafe.Pointer)(unsafe.Pointer(&reserveAddr))

	prot := uintptr(unix.PROT_READ | unix.PROT_WRITE)

	// Step 2: Map Control Pages (Page 0 Consumer + Page 1 Producer)
	_, _, errno = unix.Syscall6(
		unix.SYS_MMAP,
		reserveAddr,
		uintptr(abi.ControlHeaderSize),
		prot,
		uintptr(unix.MAP_SHARED|unix.MAP_FIXED),
		file.Fd(),
		0,
	)
	if errno != 0 {
		_, _, _ = unix.Syscall(unix.SYS_MUNMAP, reserveAddr, totalVirtualSize, 0)
		file.Close()
		return nil, fmt.Errorf("failed to map control pages: %v", errno)
	}

	// Step 3: Map First Copy of Data Ring
	data1Addr := reserveAddr + uintptr(abi.ControlHeaderSize)
	_, _, errno = unix.Syscall6(
		unix.SYS_MMAP,
		data1Addr,
		uintptr(dataSize),
		prot,
		uintptr(unix.MAP_SHARED|unix.MAP_FIXED),
		file.Fd(),
		uintptr(abi.ControlHeaderSize),
	)
	if errno != 0 {
		_, _, _ = unix.Syscall(unix.SYS_MUNMAP, reserveAddr, totalVirtualSize, 0)
		file.Close()
		return nil, fmt.Errorf("failed to map first data ring copy: %v", errno)
	}

	// Step 4: Map Second Copy of Data Ring immediately adjacent
	data2Addr := data1Addr + uintptr(dataSize)
	_, _, errno = unix.Syscall6(
		unix.SYS_MMAP,
		data2Addr,
		uintptr(dataSize),
		prot,
		uintptr(unix.MAP_SHARED|unix.MAP_FIXED),
		file.Fd(),
		uintptr(abi.ControlHeaderSize),
	)
	if errno != 0 {
		_, _, _ = unix.Syscall(unix.SYS_MUNMAP, reserveAddr, totalVirtualSize, 0)
		file.Close()
		return nil, fmt.Errorf("failed to map second data ring copy: %v", errno)
	}

	data1Ptr := unsafe.Add(basePtr, abi.ControlHeaderSize)

	// Construct Go slice over the double-mapped data ring
	dataSlice := unsafe.Slice((*byte)(data1Ptr), int(2*dataSize))

	consumerPtr := (*uint64)(basePtr)
	producerPtr := (*uint64)(unsafe.Add(basePtr, abi.PageSize))

	return &MappedRing{
		BaseAddr:      reserveAddr,
		TotalMmapSize: totalVirtualSize,
		DataSize:      dataSize,
		Mask:          dataSize - 1,
		File:          file,
		ConsumerPos:   consumerPtr,
		ProducerPos:   producerPtr,
		Data:          dataSlice,
	}, nil
}

// Close unmaps the memory and closes the backing file.
func (m *MappedRing) Close() error {
	if m.BaseAddr != 0 {
		_, _, _ = unix.Syscall(unix.SYS_MUNMAP, m.BaseAddr, m.TotalMmapSize, 0)
		m.BaseAddr = 0
	}
	if m.File != nil {
		err := m.File.Close()
		m.File = nil
		return err
	}
	return nil
}
