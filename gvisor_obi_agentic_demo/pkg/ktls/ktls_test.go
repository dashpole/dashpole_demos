// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package ktls

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"net"
	"path/filepath"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/ringbuf"
)

func TestKTLS_AES128GCM_TLS12_Framing(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 8)
	salt := make([]byte, 4)
	_, _ = rand.Read(key)
	_, _ = rand.Read(iv)
	_, _ = rand.Read(salt)

	tx, err := NewCryptoContext(abi.TLS_1_2_VERSION, abi.TLS_CIPHER_AES_GCM_128, key, iv, salt, 1)
	if err != nil {
		t.Fatalf("failed to create TX crypto: %v", err)
	}

	rx, err := NewCryptoContext(abi.TLS_1_2_VERSION, abi.TLS_CIPHER_AES_GCM_128, key, iv, salt, 1)
	if err != nil {
		t.Fatalf("failed to create RX crypto: %v", err)
	}

	plaintext := []byte("POST /v1/chat/completions HTTP/1.1\r\nContent-Type: application/json\r\n\r\n{\"model\":\"gemini-1.5-pro\"}")
	recordFrame, err := tx.SealRecord(abi.TLS_RECORD_TYPE_DATA, plaintext)
	if err != nil {
		t.Fatalf("SealRecord error: %v", err)
	}

	cType, decrypted, err := rx.OpenRecord(recordFrame)
	if err != nil {
		t.Fatalf("OpenRecord error: %v", err)
	}
	if cType != abi.TLS_RECORD_TYPE_DATA {
		t.Errorf("expected ContentType %d, got %d", abi.TLS_RECORD_TYPE_DATA, cType)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("decrypted plaintext mismatch: expected %q, got %q", string(plaintext), string(decrypted))
	}
}

func TestKTLS_AES256GCM_TLS13_Framing(t *testing.T) {
	key := make([]byte, 32)
	iv := make([]byte, 12)
	_, _ = rand.Read(key)
	_, _ = rand.Read(iv)

	tx, err := NewCryptoContext(abi.TLS_1_3_VERSION, abi.TLS_CIPHER_AES_GCM_256, key, iv, nil, 0)
	if err != nil {
		t.Fatalf("failed to create TX crypto: %v", err)
	}

	rx, err := NewCryptoContext(abi.TLS_1_3_VERSION, abi.TLS_CIPHER_AES_GCM_256, key, iv, nil, 0)
	if err != nil {
		t.Fatalf("failed to create RX crypto: %v", err)
	}

	plaintext := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"Hello agent!\"}}]}\n\n")
	recordFrame, err := tx.SealRecord(abi.TLS_RECORD_TYPE_DATA, plaintext)
	if err != nil {
		t.Fatalf("SealRecord error: %v", err)
	}

	cType, decrypted, err := rx.OpenRecord(recordFrame)
	if err != nil {
		t.Fatalf("OpenRecord error: %v", err)
	}
	if cType != abi.TLS_RECORD_TYPE_DATA {
		t.Errorf("expected ContentType %d, got %d", abi.TLS_RECORD_TYPE_DATA, cType)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("decrypted plaintext mismatch: expected %q, got %q", string(plaintext), string(decrypted))
	}
}

func TestKTLS_CorruptedFrameRejection(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 8)
	salt := make([]byte, 4)
	_, _ = rand.Read(key)
	_, _ = rand.Read(iv)
	_, _ = rand.Read(salt)

	tx, _ := NewCryptoContext(abi.TLS_1_2_VERSION, abi.TLS_CIPHER_AES_GCM_128, key, iv, salt, 0)
	rx, _ := NewCryptoContext(abi.TLS_1_2_VERSION, abi.TLS_CIPHER_AES_GCM_128, key, iv, salt, 0)

	plaintext := []byte("secret agent data")
	recordFrame, _ := tx.SealRecord(abi.TLS_RECORD_TYPE_DATA, plaintext)

	// Corrupt a byte in the ciphertext payload
	recordFrame[len(recordFrame)-5] ^= 0xFF

	_, _, err := rx.OpenRecord(recordFrame)
	if err != ErrBadMessage {
		t.Fatalf("expected ErrBadMessage on corrupted ciphertext, got %v", err)
	}
}

func TestKTLS_SocketEmulation_Bidirectional(t *testing.T) {
	dir := t.TempDir()
	shmPath := filepath.Join(dir, "ktls_tap.shm")

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

	tapSink := NewRingBufTelemetrySink(sink)

	// Setup net.Pipe to simulate in-kernel socket pair
	clientConn, serverConn := net.Pipe()

	clientTuple := SocketTuple{
		SrcIP:   net.ParseIP("10.0.0.1"),
		SrcPort: 54321,
		DstIP:   net.ParseIP("10.0.0.2"),
		DstPort: 443,
	}
	serverTuple := SocketTuple{
		SrcIP:   net.ParseIP("10.0.0.2"),
		SrcPort: 443,
		DstIP:   net.ParseIP("10.0.0.1"),
		DstPort: 54321,
	}

	clientSock := NewTLSSocket(clientConn, clientTuple, 100, 101, tapSink)
	serverSock := NewTLSSocket(serverConn, serverTuple, 200, 201, tapSink)

	// Attach ULP "tls"
	if err := clientSock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls"); err != nil {
		t.Fatalf("client SetSockOptTCP failed: %v", err)
	}
	if err := serverSock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls"); err != nil {
		t.Fatalf("server SetSockOptTCP failed: %v", err)
	}

	// Prepare TLS 1.3 AES-GCM-128 Crypto Info
	var clientTXInfo abi.TLS13CryptoInfoAESGCM128
	clientTXInfo.Info.Version = abi.TLS_1_3_VERSION
	clientTXInfo.Info.CipherType = abi.TLS_CIPHER_AES_GCM_128
	_, _ = rand.Read(clientTXInfo.Key[:])
	_, _ = rand.Read(clientTXInfo.IV[:])

	var serverTXInfo abi.TLS13CryptoInfoAESGCM128
	serverTXInfo.Info.Version = abi.TLS_1_3_VERSION
	serverTXInfo.Info.CipherType = abi.TLS_CIPHER_AES_GCM_128
	_, _ = rand.Read(serverTXInfo.Key[:])
	_, _ = rand.Read(serverTXInfo.IV[:])

	// Client TX = Server RX
	clientTXBytes := unsafe.Slice((*byte)(unsafe.Pointer(&clientTXInfo)), unsafe.Sizeof(clientTXInfo))
	// Server TX = Client RX
	serverTXBytes := unsafe.Slice((*byte)(unsafe.Pointer(&serverTXInfo)), unsafe.Sizeof(serverTXInfo))

	// Configure client
	if err := clientSock.SetSockOptTLS(abi.TLS_TX, clientTXBytes); err != nil {
		t.Fatalf("client SetSockOptTLS TX failed: %v", err)
	}
	if err := clientSock.SetSockOptTLS(abi.TLS_RX, serverTXBytes); err != nil {
		t.Fatalf("client SetSockOptTLS RX failed: %v", err)
	}

	// Configure server
	if err := serverSock.SetSockOptTLS(abi.TLS_RX, clientTXBytes); err != nil {
		t.Fatalf("server SetSockOptTLS RX failed: %v", err)
	}
	if err := serverSock.SetSockOptTLS(abi.TLS_TX, serverTXBytes); err != nil {
		t.Fatalf("server SetSockOptTLS TX failed: %v", err)
	}

	requestPayload := []byte("GET /mcp/tools/list HTTP/1.1\r\nHost: agent-service\r\n\r\n")
	responsePayload := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n{\"tools\":[\"qdrant_search\",\"execute_python\"]}")

	errCh := make(chan error, 2)

	// Server goroutine: Read request, write response
	go func() {
		buf := make([]byte, 1024)
		n, err := serverSock.Read(buf)
		if err != nil {
			errCh <- err
			return
		}
		if !bytes.Equal(buf[:n], requestPayload) {
			t.Errorf("server received payload mismatch: expected %q, got %q", string(requestPayload), string(buf[:n]))
		}

		if _, err := serverSock.Write(responsePayload); err != nil {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	// Client goroutine: Write request, read response
	go func() {
		if _, err := clientSock.Write(requestPayload); err != nil {
			errCh <- err
			return
		}
		buf := make([]byte, 1024)
		n, err := clientSock.Read(buf)
		if err != nil {
			errCh <- err
			return
		}
		if !bytes.Equal(buf[:n], responsePayload) {
			t.Errorf("client received payload mismatch: expected %q, got %q", string(responsePayload), string(buf[:n]))
		}
		errCh <- nil
	}()

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("exchange error: %v", err)
		}
	}

	// Verify Telemetry Tap records in RingBuffer:
	// We expect:
	// 1. Client TX tap (requestPayload)
	// 2. Server RX tap (requestPayload)
	// 3. Server TX tap (responsePayload)
	// 4. Client RX tap (responsePayload)
	var capturedEvents []ringbuf.Record
	_, err = reader.DrainAll(func(rec ringbuf.Record) error {
		capturedEvents = append(capturedEvents, ringbuf.Record{
			MsgType: rec.MsgType,
			Flags:   rec.Flags,
			Payload: append([]byte(nil), rec.Payload...),
		})
		return nil
	})
	if err != nil {
		t.Fatalf("DrainAll error: %v", err)
	}

	if len(capturedEvents) != 4 {
		t.Fatalf("expected 4 telemetry events in ringbuffer, got %d", len(capturedEvents))
	}

	// Event 1: Client TX
	if capturedEvents[0].MsgType != abi.MsgTypeKTLSTx {
		t.Errorf("Event 0 MsgType expected %d (TX), got %d", abi.MsgTypeKTLSTx, capturedEvents[0].MsgType)
	}
	_, pid0, _, _, p0, ok0 := DeserializeTelemetryEvent(capturedEvents[0].Payload)
	if !ok0 || pid0 != 100 || !bytes.Equal(p0, requestPayload) {
		t.Errorf("Event 0 deserialization error: ok=%v, pid=%d, payload=%q", ok0, pid0, string(p0))
	}

	// Event 2: Server RX
	if capturedEvents[1].MsgType != abi.MsgTypeKTLSRx {
		t.Errorf("Event 1 MsgType expected %d (RX), got %d", abi.MsgTypeKTLSRx, capturedEvents[1].MsgType)
	}
	_, pid1, _, _, p1, ok1 := DeserializeTelemetryEvent(capturedEvents[1].Payload)
	if !ok1 || pid1 != 200 || !bytes.Equal(p1, requestPayload) {
		t.Errorf("Event 1 deserialization error: ok=%v, pid=%d, payload=%q", ok1, pid1, string(p1))
	}

	// Event 3: Server TX
	if capturedEvents[2].MsgType != abi.MsgTypeKTLSTx {
		t.Errorf("Event 2 MsgType expected %d (TX), got %d", abi.MsgTypeKTLSTx, capturedEvents[2].MsgType)
	}
	_, pid2, _, _, p2, ok2 := DeserializeTelemetryEvent(capturedEvents[2].Payload)
	if !ok2 || pid2 != 200 || !bytes.Equal(p2, responsePayload) {
		t.Errorf("Event 2 deserialization error: ok=%v, pid=%d, payload=%q", ok2, pid2, string(p2))
	}

	// Event 4: Client RX
	if capturedEvents[3].MsgType != abi.MsgTypeKTLSRx {
		t.Errorf("Event 3 MsgType expected %d (RX), got %d", abi.MsgTypeKTLSRx, capturedEvents[3].MsgType)
	}
	_, pid3, _, _, p3, ok3 := DeserializeTelemetryEvent(capturedEvents[3].Payload)
	if !ok3 || pid3 != 100 || !bytes.Equal(p3, responsePayload) {
		t.Errorf("Event 3 deserialization error: ok=%v, pid=%d, payload=%q", ok3, pid3, string(p3))
	}

	t.Logf("kTLS Bidirectional Interception: 100%% byte accuracy across all 4 TX/RX tap points")
}

func TestKTLS_FullDuplexConcurrency(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	clientTuple := SocketTuple{SrcPort: 1000, DstPort: 2000}
	serverTuple := SocketTuple{SrcPort: 2000, DstPort: 1000}

	clientSock := NewTLSSocket(clientConn, clientTuple, 1, 1, nil)
	serverSock := NewTLSSocket(serverConn, serverTuple, 2, 2, nil)

	_ = clientSock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls")
	_ = serverSock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls")

	key := make([]byte, 16)
	iv := make([]byte, 12)
	_, _ = rand.Read(key)
	_, _ = rand.Read(iv)

	var info abi.TLS13CryptoInfoAESGCM128
	info.Info.Version = abi.TLS_1_3_VERSION
	info.Info.CipherType = abi.TLS_CIPHER_AES_GCM_128
	copy(info.Key[:], key)
	copy(info.IV[:], iv)

	infoBytes := unsafe.Slice((*byte)(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	_ = clientSock.SetSockOptTLS(abi.TLS_TX, infoBytes)
	_ = clientSock.SetSockOptTLS(abi.TLS_RX, infoBytes)
	_ = serverSock.SetSockOptTLS(abi.TLS_TX, infoBytes)
	_ = serverSock.SetSockOptTLS(abi.TLS_RX, infoBytes)

	// Run concurrent read and write loops in both directions
	var wg sync.WaitGroup
	count := 200
	payload := []byte("concurrency-test-payload-12345")

	// Client writes to Server
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < count; i++ {
			if _, err := clientSock.Write(payload); err != nil {
				t.Errorf("client write failed: %v", err)
				return
			}
		}
	}()

	// Server reads from Client
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, len(payload))
		for i := 0; i < count; i++ {
			if _, err := serverSock.Read(buf); err != nil {
				t.Errorf("server read failed: %v", err)
				return
			}
		}
	}()

	// Server writes to Client simultaneously
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < count; i++ {
			if _, err := serverSock.Write(payload); err != nil {
				t.Errorf("server write failed: %v", err)
				return
			}
		}
	}()

	// Client reads from Server simultaneously
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, len(payload))
		for i := 0; i < count; i++ {
			if _, err := clientSock.Read(buf); err != nil {
				t.Errorf("client read failed: %v", err)
				return
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		t.Logf("Full-duplex concurrent Read and Write successfully exchanged %d messages without deadlocks", count*2)
	case <-time.After(5 * time.Second):
		t.Fatalf("Full-duplex concurrent Read/Write deadlocked!")
	}
}

func TestKTLS_ContinuousTransferStress(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 8)
	salt := make([]byte, 4)
	_, _ = rand.Read(key)
	_, _ = rand.Read(iv)
	_, _ = rand.Read(salt)

	tx, _ := NewCryptoContext(abi.TLS_1_2_VERSION, abi.TLS_CIPHER_AES_GCM_128, key, iv, salt, 0)
	rx, _ := NewCryptoContext(abi.TLS_1_2_VERSION, abi.TLS_CIPHER_AES_GCM_128, key, iv, salt, 0)

	totalRecords := 5000
	payloadSize := 8192 // 8KB per record -> ~40MB continuous test

	for i := 0; i < totalRecords; i++ {
		plaintext := make([]byte, payloadSize)
		binary.LittleEndian.PutUint64(plaintext[0:8], uint64(i))
		binary.LittleEndian.PutUint64(plaintext[8:16], uint64(^i))

		frame, err := tx.SealRecord(abi.TLS_RECORD_TYPE_DATA, plaintext)
		if err != nil {
			t.Fatalf("SealRecord error on record %d: %v", i, err)
		}

		cType, decrypted, err := rx.OpenRecord(frame)
		if err != nil {
			t.Fatalf("OpenRecord error on record %d: %v", i, err)
		}
		if cType != abi.TLS_RECORD_TYPE_DATA {
			t.Fatalf("wrong content type on record %d: %d", i, cType)
		}
		seq := binary.LittleEndian.Uint64(decrypted[0:8])
		inv := binary.LittleEndian.Uint64(decrypted[8:16])
		if seq != uint64(i) || inv != uint64(^i) {
			t.Fatalf("continuous transfer corruption on record %d: seq=%d, inv=%d", i, seq, inv)
		}
	}
	t.Logf("Continuous transfer: successfully sealed & opened %d records (%d MB) with 0 failures",
		totalRecords, (totalRecords*payloadSize)/(1024*1024))
}
