// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package obi

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ringbuf"
)

// ActiveSandboxWorker manages the ingestion loop for a single sandbox ringbuffer.
type ActiveSandboxWorker struct {
	ContainerID string
	ShmPath     string
	Reader      *ringbuf.RingBufReader
	Cancel      context.CancelFunc
	Done        chan struct{}
}

// PodDiscoveryManager watches the ring buffer directory and manages per-sandbox readers.
type PodDiscoveryManager struct {
	mu           sync.Mutex
	watchDir     string
	bridge       *PipelineBridge
	workers      map[string]*ActiveSandboxWorker
	pollInterval time.Duration
	stopCh       chan struct{}
	wg           sync.WaitGroup
	inotifyFd    int
	watchWd      int
}

// NewPodDiscoveryManager creates a new discovery manager.
func NewPodDiscoveryManager(watchDir string, bridge *PipelineBridge, pollInterval time.Duration) *PodDiscoveryManager {
	if pollInterval == 0 {
		pollInterval = 50 * time.Millisecond
	}
	return &PodDiscoveryManager{
		watchDir:     watchDir,
		bridge:       bridge,
		workers:      make(map[string]*ActiveSandboxWorker),
		pollInterval: pollInterval,
		stopCh:       make(chan struct{}),
		inotifyFd:    -1,
		watchWd:      -1,
	}
}

// Start begins the discovery and ingestion loop with inotify and polling fallback.
func (pdm *PodDiscoveryManager) Start() error {
	if err := os.MkdirAll(pdm.watchDir, 0755); err != nil {
		return fmt.Errorf("failed to create watch directory %s: %w", pdm.watchDir, err)
	}

	// Try initializing inotify
	if ifd, err := unix.InotifyInit1(unix.IN_NONBLOCK | unix.IN_CLOEXEC); err == nil {
		pdm.inotifyFd = ifd
		if wd, err := unix.InotifyAddWatch(ifd, pdm.watchDir, unix.IN_CREATE|unix.IN_DELETE|unix.IN_MOVED_TO); err == nil {
			pdm.watchWd = wd
			pdm.wg.Add(1)
			go pdm.inotifyLoop()
		}
	}

	pdm.wg.Add(1)
	go pdm.scanLoop()
	return nil
}

func (pdm *PodDiscoveryManager) inotifyLoop() {
	defer pdm.wg.Done()
	buf := make([]byte, 4096)

	for {
		select {
		case <-pdm.stopCh:
			return
		default:
			n, err := unix.Read(pdm.inotifyFd, buf)
			if err != nil {
				if err == unix.EAGAIN || err == unix.EWOULDBLOCK {
					time.Sleep(10 * time.Millisecond)
					continue
				}
				return
			}
			if n > 0 {
				var offset uint32
				for offset < uint32(n) {
					event := (*unix.InotifyEvent)(unsafe.Pointer(&buf[offset]))
					if event.Len > 0 {
						pdm.scan()
					}
					offset += unix.SizeofInotifyEvent + event.Len
				}
			}
		}
	}
}

func (pdm *PodDiscoveryManager) scanLoop() {
	defer pdm.wg.Done()
	ticker := time.NewTicker(pdm.pollInterval)
	defer ticker.Stop()

	// Initial scan
	pdm.scan()

	for {
		select {
		case <-pdm.stopCh:
			return
		case <-ticker.C:
			pdm.scan()
		}
	}
}

func (pdm *PodDiscoveryManager) scan() {
	entries, err := os.ReadDir(pdm.watchDir)
	if err != nil {
		return
	}

	activeFiles := make(map[string]bool)

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".shm") {
			continue
		}

		containerID := strings.TrimSuffix(entry.Name(), ".shm")
		shmPath := filepath.Join(pdm.watchDir, entry.Name())
		activeFiles[containerID] = true

		pdm.mu.Lock()
		if _, exists := pdm.workers[containerID]; !exists {
			pdm.startWorker(containerID, shmPath)
		}
		pdm.mu.Unlock()
	}

	// Clean up removed containers with synchronized unmap protection
	pdm.mu.Lock()
	var removedWorkers []*ActiveSandboxWorker
	for containerID, worker := range pdm.workers {
		if !activeFiles[containerID] {
			removedWorkers = append(removedWorkers, worker)
			delete(pdm.workers, containerID)
		}
	}
	pdm.mu.Unlock()

	for _, worker := range removedWorkers {
		worker.Cancel()
		<-worker.Done // Wait for background read loop to safely exit before unmapping memory
		_ = worker.Reader.Close()
	}
}

func (pdm *PodDiscoveryManager) startWorker(containerID, shmPath string) {
	stat, err := os.Stat(shmPath)
	if err != nil {
		return
	}
	dataSize := int(stat.Size() - abi.ControlHeaderSize)
	if dataSize <= 0 {
		dataSize = 4 * 1024 * 1024
	}

	reader, err := ringbuf.NewRingBufReader(ringbuf.ReaderConfig{
		FilePath:      shmPath,
		DataSizeBytes: uint64(dataSize),
		BatchTimeout:  50 * time.Millisecond,
	})
	if err != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	worker := &ActiveSandboxWorker{
		ContainerID: containerID,
		ShmPath:     shmPath,
		Reader:      reader,
		Cancel:      cancel,
		Done:        done,
	}
	pdm.workers[containerID] = worker

	pdm.wg.Add(1)
	go func() {
		defer pdm.wg.Done()
		defer close(done)
		var rec ringbuf.Record
		for {
			select {
			case <-ctx.Done():
				return
			default:
				drained := 0
				for {
					ok, err := reader.ReadRecord(&rec)
					if err != nil || !ok {
						break
					}
					_ = pdm.bridge.ProcessRecord(containerID, rec)
					drained++
				}
				if drained == 0 {
					time.Sleep(2 * time.Millisecond)
				}
			}
		}
	}()
}

// ActiveContainers returns currently active container IDs.
func (pdm *PodDiscoveryManager) ActiveContainers() []string {
	pdm.mu.Lock()
	defer pdm.mu.Unlock()
	var list []string
	for id := range pdm.workers {
		list = append(list, id)
	}
	return list
}

// Stop shuts down all workers cleanly and waits for readers to exit.
func (pdm *PodDiscoveryManager) Stop() {
	close(pdm.stopCh)
	if pdm.inotifyFd >= 0 {
		if pdm.watchWd >= 0 {
			_, _ = unix.InotifyRmWatch(pdm.inotifyFd, uint32(pdm.watchWd))
		}
		_ = unix.Close(pdm.inotifyFd)
	}

	pdm.mu.Lock()
	workers := make([]*ActiveSandboxWorker, 0, len(pdm.workers))
	for _, worker := range pdm.workers {
		workers = append(workers, worker)
	}
	pdm.workers = make(map[string]*ActiveSandboxWorker)
	pdm.mu.Unlock()

	for _, worker := range workers {
		worker.Cancel()
		<-worker.Done
		_ = worker.Reader.Close()
	}

	pdm.wg.Wait()
}
