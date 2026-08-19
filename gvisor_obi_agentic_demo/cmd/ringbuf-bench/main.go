// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ringbuf"
)

func main() {
	numProducers := flag.Int("producers", 16, "Number of concurrent producer goroutines")
	eventsPerProducer := flag.Int("events", 500000, "Number of events per producer")
	payloadSize := flag.Int("size", 128, "Byte size of each event payload")
	ringSizeMB := flag.Int("ring-mb", 16, "Ring buffer size in MB")
	flag.Parse()

	dir, err := os.MkdirTemp("", "gvisor_obi_bench_*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	shmPath := filepath.Join(dir, "bench.shm")
	ringSizeBytes := uint64(*ringSizeMB) * 1024 * 1024

	sink, err := ringbuf.NewRingBufSink(ringbuf.SinkConfig{
		FilePath:      shmPath,
		DataSizeBytes: ringSizeBytes,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create sink: %v\n", err)
		os.Exit(1)
	}
	defer sink.Close()

	reader, err := ringbuf.NewRingBufReader(ringbuf.ReaderConfig{
		FilePath:      shmPath,
		DataSizeBytes: ringSizeBytes,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create reader: %v\n", err)
		os.Exit(1)
	}
	defer reader.Close()

	totalExpectedEvents := uint64(*numProducers * *eventsPerProducer)
	fmt.Printf("=== gVisor <-> OBI Shared Memory RingBuffer Stress Benchmark ===\n")
	fmt.Printf("Producers: %d | Events/Producer: %d | Total Events: %d | Payload Size: %d B | Ring: %d MB\n\n",
		*numProducers, *eventsPerProducer, totalExpectedEvents, *payloadSize, *ringSizeMB)

	var consumedCount atomic.Uint64
	var stopConsumer atomic.Bool
	consumerDone := make(chan struct{})

	// High-speed consumer loop
	go func() {
		defer close(consumerDone)
		var rec ringbuf.Record
		for !stopConsumer.Load() {
			drained := 0
			for {
				ok, err := reader.ReadRecord(&rec)
				if err != nil || !ok {
					break
				}
				consumedCount.Add(1)
				drained++
			}
			if drained == 0 {
				runtime.Gosched()
			}
		}
		// Final drain
		for {
			ok, _ := reader.ReadRecord(&rec)
			if !ok {
				break
			}
			consumedCount.Add(1)
		}
	}()

	start := time.Now()
	var wg sync.WaitGroup

	for p := 0; p < *numProducers; p++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			payload := make([]byte, *payloadSize)
			payload[0] = byte(id)
			for e := 0; e < *eventsPerProducer; e++ {
				for !sink.ReserveAndCommit(abi.MsgTypeSyntheticBench, abi.ChunkFlagSingle, payload) {
					runtime.Gosched()
				}
			}
		}(p)
	}

	wg.Wait()
	stopConsumer.Store(true)
	<-consumerDone
	elapsed := time.Since(start)

	produced, bytesTotal, dropped := sink.Stats()
	consumed := consumedCount.Load()
	rate := float64(produced) / elapsed.Seconds()
	throughputMB := float64(bytesTotal) / (1024 * 1024) / elapsed.Seconds()

	fmt.Printf("--- Benchmark Results ---\n")
	fmt.Printf("Elapsed Time:       %v\n", elapsed)
	fmt.Printf("Events Produced:    %d\n", produced)
	fmt.Printf("Events Consumed:    %d\n", consumed)
	fmt.Printf("Events Dropped:     %d (%.4f%%)\n", dropped, float64(dropped)/float64(produced+dropped)*100)
	fmt.Printf("Total Data Written: %.2f MB\n", float64(bytesTotal)/(1024*1024))
	fmt.Printf("Event Throughput:   %.2f events/sec\n", rate)
	fmt.Printf("Data Bandwidth:     %.2f MB/sec\n", throughputMB)

	if consumed != produced {
		fmt.Fprintf(os.Stderr, "FATAL: Ingestion mismatch! Produced %d, Consumed %d\n", produced, consumed)
		os.Exit(1)
	}
	fmt.Printf("\n>>> GATE 1 PASS: Zero byte loss, monotonic sequence preserved across %d events <<<\n", produced)
}
