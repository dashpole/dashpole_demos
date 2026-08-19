// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package ringbuf

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/obi"
)

func TestRingBuf_Basic(t *testing.T) {
	dir := t.TempDir()
	shmPath := filepath.Join(dir, "test.shm")

	sink, err := NewRingBufSink(SinkConfig{
		FilePath:      shmPath,
		DataSizeBytes: 1024 * 1024, // 1MB
	})
	if err != nil {
		t.Fatalf("NewRingBufSink failed: %v", err)
	}
	defer sink.Close()

	reader, err := NewRingBufReader(ReaderConfig{
		FilePath:      shmPath,
		DataSizeBytes: 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewRingBufReader failed: %v", err)
	}
	defer reader.Close()

	payload := []byte("hello-gvisor-obi-world")
	ok := sink.ReserveAndCommit(abi.MsgTypeSyntheticBench, abi.ChunkFlagSingle, payload)
	if !ok {
		t.Fatalf("ReserveAndCommit returned false")
	}

	var rec Record
	ok, err = reader.ReadRecord(&rec)
	if err != nil {
		t.Fatalf("ReadRecord error: %v", err)
	}
	if !ok {
		t.Fatalf("ReadRecord returned false, expected record")
	}
	if rec.MsgType != abi.MsgTypeSyntheticBench {
		t.Errorf("expected MsgType %d, got %d", abi.MsgTypeSyntheticBench, rec.MsgType)
	}
	if !bytes.Equal(rec.Payload, payload) {
		t.Errorf("payload mismatch: expected %q, got %q", string(payload), string(rec.Payload))
	}
}

func TestRingBuf_WraparoundIntegrity(t *testing.T) {
	dir := t.TempDir()
	shmPath := filepath.Join(dir, "wrap.shm")
	ringSize := uint64(64 * 1024) // Small 64KB ring to force many wraparounds

	sink, err := NewRingBufSink(SinkConfig{
		FilePath:      shmPath,
		DataSizeBytes: ringSize,
	})
	if err != nil {
		t.Fatalf("NewRingBufSink failed: %v", err)
	}
	defer sink.Close()

	reader, err := NewRingBufReader(ReaderConfig{
		FilePath:      shmPath,
		DataSizeBytes: ringSize,
	})
	if err != nil {
		t.Fatalf("NewRingBufReader failed: %v", err)
	}
	defer reader.Close()

	totalRecords := 100000
	payloadSize := 256

	// Write and read interleaved to exercise continuous wraparounds
	for i := 0; i < totalRecords; i++ {
		payload := make([]byte, payloadSize)
		binary.LittleEndian.PutUint64(payload[0:8], uint64(i))
		binary.LittleEndian.PutUint64(payload[8:16], uint64(^i))

		if !sink.ReserveAndCommit(abi.MsgTypeSyntheticBench, abi.ChunkFlagSingle, payload) {
			t.Fatalf("failed to write record %d", i)
		}

		var rec Record
		ok, err := reader.ReadRecord(&rec)
		if err != nil || !ok {
			t.Fatalf("failed to read record %d: ok=%v, err=%v", i, ok, err)
		}
		if len(rec.Payload) != payloadSize {
			t.Fatalf("record %d length mismatch: expected %d, got %d", i, payloadSize, len(rec.Payload))
		}
		seq := binary.LittleEndian.Uint64(rec.Payload[0:8])
		inv := binary.LittleEndian.Uint64(rec.Payload[8:16])
		if seq != uint64(i) || inv != uint64(^i) {
			t.Fatalf("record %d corruption at wraparound: seq=%d, inv=%d", i, seq, inv)
		}
	}
}

func TestRingBuf_ConcurrentProducers(t *testing.T) {
	dir := t.TempDir()
	shmPath := filepath.Join(dir, "concurrent.shm")
	ringSize := uint64(16 * 1024 * 1024) // 16MB

	sink, err := NewRingBufSink(SinkConfig{
		FilePath:      shmPath,
		DataSizeBytes: ringSize,
	})
	if err != nil {
		t.Fatalf("NewRingBufSink failed: %v", err)
	}
	defer sink.Close()

	reader, err := NewRingBufReader(ReaderConfig{
		FilePath:      shmPath,
		DataSizeBytes: ringSize,
	})
	if err != nil {
		t.Fatalf("NewRingBufReader failed: %v", err)
	}
	defer reader.Close()

	numProducers := 64
	recordsPerProducer := 10000
	var wg sync.WaitGroup

	consumed := make(map[uint32]uint64)
	var consumeMu sync.Mutex
	stopConsumer := make(chan struct{})
	consumerDone := make(chan struct{})

	// Background consumer
	go func() {
		defer close(consumerDone)
		var rec Record
		for {
			select {
			case <-stopConsumer:
				// Drain remaining
				for {
					ok, _ := reader.ReadRecord(&rec)
					if !ok {
						return
					}
					if len(rec.Payload) >= 16 {
						prodID := binary.LittleEndian.Uint32(rec.Payload[0:4])
						seq := binary.LittleEndian.Uint64(rec.Payload[8:16])
						consumeMu.Lock()
						lastSeq := consumed[prodID]
						if seq != lastSeq+1 && (lastSeq != 0 || seq != 1) {
							t.Errorf("Producer %d out-of-order seq: last=%d, current=%d", prodID, lastSeq, seq)
						}
						consumed[prodID] = seq
						consumeMu.Unlock()
					}
				}
			default:
				ok, _ := reader.ReadRecord(&rec)
				if ok && len(rec.Payload) >= 16 {
					prodID := binary.LittleEndian.Uint32(rec.Payload[0:4])
					seq := binary.LittleEndian.Uint64(rec.Payload[8:16])
					consumeMu.Lock()
					lastSeq := consumed[prodID]
					if seq != lastSeq+1 && (lastSeq != 0 || seq != 1) {
						t.Errorf("Producer %d out-of-order seq: last=%d, current=%d", prodID, lastSeq, seq)
					}
					consumed[prodID] = seq
					consumeMu.Unlock()
				} else {
					time.Sleep(10 * time.Microsecond)
				}
			}
		}
	}()

	// Launch concurrent producers
	start := time.Now()
	for p := 0; p < numProducers; p++ {
		wg.Add(1)
		go func(prodID uint32) {
			defer wg.Done()
			payload := make([]byte, 64)
			binary.LittleEndian.PutUint32(payload[0:4], prodID)
			for s := 1; s <= recordsPerProducer; s++ {
				binary.LittleEndian.PutUint64(payload[8:16], uint64(s))
				for !sink.ReserveAndCommit(abi.MsgTypeSyntheticBench, abi.ChunkFlagSingle, payload) {
					time.Sleep(100 * time.Nanosecond) // Backpressure retry
				}
			}
		}(uint32(p + 1))
	}

	wg.Wait()
	close(stopConsumer)
	<-consumerDone

	elapsed := time.Since(start)
	totalEvents := numProducers * recordsPerProducer
	rate := float64(totalEvents) / elapsed.Seconds()
	t.Logf("Concurrent throughput: %d events in %v (%.2f events/sec)", totalEvents, elapsed, rate)

	consumeMu.Lock()
	defer consumeMu.Unlock()
	for p := 1; p <= numProducers; p++ {
		if consumed[uint32(p)] != uint64(recordsPerProducer) {
			t.Errorf("Producer %d: expected %d records, consumed %d", p, recordsPerProducer, consumed[uint32(p)])
		}
	}
}

func TestRingBuf_BackpressureNonBlocking(t *testing.T) {
	dir := t.TempDir()
	shmPath := filepath.Join(dir, "saturate.shm")
	ringSize := uint64(16 * 1024) // Small 16KB ring

	sink, err := NewRingBufSink(SinkConfig{
		FilePath:      shmPath,
		DataSizeBytes: ringSize,
	})
	if err != nil {
		t.Fatalf("NewRingBufSink failed: %v", err)
	}
	defer sink.Close()

	payload := make([]byte, 1024) // 1KB records
	written := 0
	dropped := 0

	// Write without reading until buffer saturates
	for i := 0; i < 50; i++ {
		start := time.Now()
		ok := sink.ReserveAndCommit(abi.MsgTypeSyntheticBench, abi.ChunkFlagSingle, payload)
		duration := time.Since(start)

		if duration > 5*time.Millisecond {
			t.Errorf("ReserveAndCommit blocked for %v (expected non-blocking <1ms)", duration)
		}

		if ok {
			written++
		} else {
			dropped++
		}
	}

	if dropped == 0 {
		t.Fatalf("Expected dropped records when saturating 16KB buffer with 50KB data")
	}

	_, _, statsDropped := sink.Stats()
	if statsDropped != uint64(dropped) {
		t.Errorf("Stats dropped mismatch: expected %d, got %d", dropped, statsDropped)
	}
	t.Logf("Non-blocking saturation test: %d written, %d dropped gracefully", written, dropped)
}

func TestRingBuf_EventFDWakeup(t *testing.T) {
	dir := t.TempDir()
	shmPath := filepath.Join(dir, "eventfd.shm")
	efdPath := filepath.Join(dir, "eventfd.sock")

	sink, err := NewRingBufSink(SinkConfig{
		FilePath:       shmPath,
		DataSizeBytes:  1024 * 1024,
		WatermarkBytes: 1024, // 1KB watermark
		EventFDPath:    efdPath,
	})
	if err != nil {
		t.Fatalf("NewRingBufSink failed: %v", err)
	}
	defer sink.Close()

	reader, err := NewRingBufReader(ReaderConfig{
		FilePath:      shmPath,
		DataSizeBytes: 1024 * 1024,
		EventFD:       sink.EventFD(),
	})
	if err != nil {
		t.Fatalf("NewRingBufReader failed: %v", err)
	}
	defer reader.Close()

	// Launch consumer waiting on eventfd
	wokenUp := make(chan time.Duration, 1)
	go func() {
		t0 := time.Now()
		hasEvent, err := reader.WaitForEvents(500 * time.Millisecond)
		latency := time.Since(t0)
		if err != nil || !hasEvent {
			wokenUp <- -1
			return
		}
		wokenUp <- latency
	}()

	// Short delay then write over 1KB to trigger eventfd
	time.Sleep(50 * time.Millisecond)
	payload := make([]byte, 2048)
	sink.ReserveAndCommit(abi.MsgTypeSyntheticBench, abi.ChunkFlagSingle, payload)

	select {
	case latency := <-wokenUp:
		if latency < 0 {
			t.Fatalf("WaitForEvents failed or timed out")
		}
		t.Logf("EventFD notification latency: %v", latency)
	case <-time.After(1 * time.Second):
		t.Fatalf("Consumer was not woken up by eventfd within 1s")
	}
}

func TestRingBuf_LargeBufReassembly(t *testing.T) {
	dir := t.TempDir()
	shmPath := filepath.Join(dir, "largebuf.shm")

	sink, err := NewRingBufSink(SinkConfig{
		FilePath:      shmPath,
		DataSizeBytes: 4 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewRingBufSink failed: %v", err)
	}
	defer sink.Close()

	reader, err := NewRingBufReader(ReaderConfig{
		FilePath:      shmPath,
		DataSizeBytes: 4 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("NewRingBufReader failed: %v", err)
	}
	defer reader.Close()

	reassembler := obi.NewChunkReassembler()

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

		// Prepend 8-byte stream ID to payload
		wirePayload := make([]byte, 8+len(chunkData))
		binary.LittleEndian.PutUint64(wirePayload[0:8], streamID)
		copy(wirePayload[8:], chunkData)

		sink.ReserveAndCommit(abi.MsgTypeKTLSTx, flags, wirePayload)
	}

	// Consume and reassemble
	var assembledBuf *obi.LargeBuffer
	var completed bool

	for {
		var rec Record
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

func BenchmarkRingBuf_Throughput(b *testing.B) {
	dir := b.TempDir()
	shmPath := filepath.Join(dir, "bench.shm")
	ringSize := uint64(32 * 1024 * 1024) // 32MB

	sink, err := NewRingBufSink(SinkConfig{
		FilePath:      shmPath,
		DataSizeBytes: ringSize,
	})
	if err != nil {
		b.Fatalf("NewRingBufSink failed: %v", err)
	}
	defer sink.Close()

	reader, err := NewRingBufReader(ReaderConfig{
		FilePath:      shmPath,
		DataSizeBytes: ringSize,
	})
	if err != nil {
		b.Fatalf("NewRingBufReader failed: %v", err)
	}
	defer reader.Close()

	payload := make([]byte, 128)
	var stop atomic.Bool

	// Consumer loop
	go func() {
		var rec Record
		for !stop.Load() {
			for {
				ok, _ := reader.ReadRecord(&rec)
				if !ok {
					break
				}
			}
			time.Sleep(10 * time.Microsecond)
		}
	}()

	b.ResetTimer()
	b.SetParallelism(8)
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			for !sink.ReserveAndCommit(abi.MsgTypeSyntheticBench, abi.ChunkFlagSingle, payload) {
				runtime.Gosched()
			}
		}
	})
	b.StopTimer()
	stop.Store(true)

	events, bytesTotal, dropped := sink.Stats()
	b.ReportMetric(float64(events)/b.Elapsed().Seconds(), "events/sec")
	b.ReportMetric(float64(bytesTotal)/(1024*1024)/b.Elapsed().Seconds(), "MB/sec")
	b.Logf("Benchmark stats: %d events, %.2f MB, %d dropped", events, float64(bytesTotal)/(1024*1024), dropped)
}
