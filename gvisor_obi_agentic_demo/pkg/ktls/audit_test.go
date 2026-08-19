// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package ktls

import (
	"bytes"
	"crypto/rand"
	"io"
	"net"
	"testing"
	"time"
	"unsafe"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
)

// 1. Test Audit: TLS 1.2 with AES-256-GCM
func TestAudit_TLS12_AES256GCM_Framing(t *testing.T) {
	key := make([]byte, 32)
	iv := make([]byte, 8)
	salt := make([]byte, 4)
	_, _ = rand.Read(key)
	_, _ = rand.Read(iv)
	_, _ = rand.Read(salt)

	tx, err := NewCryptoContext(abi.TLS_1_2_VERSION, abi.TLS_CIPHER_AES_GCM_256, key, iv, salt, 0)
	if err != nil {
		t.Fatalf("failed to create TX crypto: %v", err)
	}
	rx, err := NewCryptoContext(abi.TLS_1_2_VERSION, abi.TLS_CIPHER_AES_GCM_256, key, iv, salt, 0)
	if err != nil {
		t.Fatalf("failed to create RX crypto: %v", err)
	}

	payload := []byte("TLS 1.2 AES-256-GCM audit verification payload")
	frame, err := tx.SealRecord(abi.TLS_RECORD_TYPE_DATA, payload)
	if err != nil {
		t.Fatalf("SealRecord failed: %v", err)
	}

	cType, decrypted, err := rx.OpenRecord(frame)
	if err != nil {
		t.Fatalf("OpenRecord failed: %v", err)
	}
	if cType != abi.TLS_RECORD_TYPE_DATA {
		t.Errorf("expected ContentType %d, got %d", abi.TLS_RECORD_TYPE_DATA, cType)
	}
	if !bytes.Equal(decrypted, payload) {
		t.Errorf("decrypted payload mismatch: expected %q, got %q", string(payload), string(decrypted))
	}
}

// 2. Test Audit: TLS 1.3 with AES-128-GCM
func TestAudit_TLS13_AES128GCM_Framing(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 12)
	_, _ = rand.Read(key)
	_, _ = rand.Read(iv)

	tx, err := NewCryptoContext(abi.TLS_1_3_VERSION, abi.TLS_CIPHER_AES_GCM_128, key, iv, nil, 0)
	if err != nil {
		t.Fatalf("failed to create TX crypto: %v", err)
	}
	rx, err := NewCryptoContext(abi.TLS_1_3_VERSION, abi.TLS_CIPHER_AES_GCM_128, key, iv, nil, 0)
	if err != nil {
		t.Fatalf("failed to create RX crypto: %v", err)
	}

	payload := []byte("TLS 1.3 AES-128-GCM audit verification payload")
	frame, err := tx.SealRecord(abi.TLS_RECORD_TYPE_HANDSHAKE, payload)
	if err != nil {
		t.Fatalf("SealRecord failed: %v", err)
	}

	cType, decrypted, err := rx.OpenRecord(frame)
	if err != nil {
		t.Fatalf("OpenRecord failed: %v", err)
	}
	if cType != abi.TLS_RECORD_TYPE_HANDSHAKE {
		t.Errorf("expected ContentType %d, got %d", abi.TLS_RECORD_TYPE_HANDSHAKE, cType)
	}
	if !bytes.Equal(decrypted, payload) {
		t.Errorf("decrypted payload mismatch: expected %q, got %q", string(payload), string(decrypted))
	}
}

// 3. Test Audit: TLS 1.3 Corrupted Tag / Ciphertext Rejection
func TestAudit_TLS13_CorruptedTag_Rejection(t *testing.T) {
	key := make([]byte, 32)
	iv := make([]byte, 12)
	_, _ = rand.Read(key)
	_, _ = rand.Read(iv)

	tx, _ := NewCryptoContext(abi.TLS_1_3_VERSION, abi.TLS_CIPHER_AES_GCM_256, key, iv, nil, 0)
	rx, _ := NewCryptoContext(abi.TLS_1_3_VERSION, abi.TLS_CIPHER_AES_GCM_256, key, iv, nil, 0)

	payload := []byte("confidential telemetry")
	frame, _ := tx.SealRecord(abi.TLS_RECORD_TYPE_DATA, payload)

	// Tamper tag (last byte)
	frame[len(frame)-1] ^= 0x01

	_, _, err := rx.OpenRecord(frame)
	if err != ErrBadMessage {
		t.Fatalf("expected ErrBadMessage on corrupted tag, got %v", err)
	}
}

// 4. Test Audit: TLS 1.2 Header Tamper Detection (AAD mismatch)
func TestAudit_TLS12_HeaderTamper_Rejection(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 8)
	salt := make([]byte, 4)
	_, _ = rand.Read(key)
	_, _ = rand.Read(iv)
	_, _ = rand.Read(salt)

	tx, _ := NewCryptoContext(abi.TLS_1_2_VERSION, abi.TLS_CIPHER_AES_GCM_128, key, iv, salt, 0)
	rx, _ := NewCryptoContext(abi.TLS_1_2_VERSION, abi.TLS_CIPHER_AES_GCM_128, key, iv, salt, 0)

	payload := []byte("critical transaction")
	frame, _ := tx.SealRecord(abi.TLS_RECORD_TYPE_DATA, payload)

	// Tamper content type in header (byte 0)
	frame[0] = abi.TLS_RECORD_TYPE_ALERT

	_, _, err := rx.OpenRecord(frame)
	if err != ErrBadMessage {
		t.Fatalf("expected ErrBadMessage on tampered header, got %v", err)
	}
}

// 5. Test Audit: TLS 1.3 Outer Header Tamper Detection (AAD mismatch)
func TestAudit_TLS13_HeaderTamper_Rejection(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 12)
	_, _ = rand.Read(key)
	_, _ = rand.Read(iv)

	tx, _ := NewCryptoContext(abi.TLS_1_3_VERSION, abi.TLS_CIPHER_AES_GCM_128, key, iv, nil, 0)
	rx, _ := NewCryptoContext(abi.TLS_1_3_VERSION, abi.TLS_CIPHER_AES_GCM_128, key, iv, nil, 0)

	payload := []byte("tls 1.3 packet")
	frame, _ := tx.SealRecord(abi.TLS_RECORD_TYPE_DATA, payload)

	// Tamper legacy record version in outer header (bytes 1..2)
	frame[2] ^= 0x01

	_, _, err := rx.OpenRecord(frame)
	if err != ErrBadMessage {
		t.Fatalf("expected ErrBadMessage on tampered TLS 1.3 outer header, got %v", err)
	}
}

// 6. Test Audit: Replay Attack and Out-of-Order Frame Rejection
func TestAudit_ReplayAttack_SeqDesync(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 8)
	salt := make([]byte, 4)
	_, _ = rand.Read(key)
	_, _ = rand.Read(iv)
	_, _ = rand.Read(salt)

	tx, _ := NewCryptoContext(abi.TLS_1_2_VERSION, abi.TLS_CIPHER_AES_GCM_128, key, iv, salt, 0)
	rx, _ := NewCryptoContext(abi.TLS_1_2_VERSION, abi.TLS_CIPHER_AES_GCM_128, key, iv, salt, 0)

	frame0, _ := tx.SealRecord(abi.TLS_RECORD_TYPE_DATA, []byte("message 0"))
	frame1, _ := tx.SealRecord(abi.TLS_RECORD_TYPE_DATA, []byte("message 1"))

	// Successfully receive frame 0
	_, _, err := rx.OpenRecord(frame0)
	if err != nil {
		t.Fatalf("frame0 failed: %v", err)
	}

	// Replay frame 0 when expecting frame 1
	_, _, err = rx.OpenRecord(frame0)
	if err != ErrBadMessage {
		t.Fatalf("expected ErrBadMessage on replayed frame, got %v", err)
	}

	// Now rx SeqNum is at 2, so frame 1 (sealed with SeqNum 1) must also fail
	_, _, err = rx.OpenRecord(frame1)
	if err != ErrBadMessage {
		t.Fatalf("expected ErrBadMessage on desynced sequence, got %v", err)
	}
}

// 7. Test Audit: Leftover Buffering Byte-by-Byte Read
func TestAudit_LeftoverBuffering_ByteByByte(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	clientSock := NewTLSSocket(clientConn, SocketTuple{}, 1, 1, nil)
	serverSock := NewTLSSocket(serverConn, SocketTuple{}, 2, 2, nil)

	_ = clientSock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls")
	_ = serverSock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls")

	var info abi.TLS13CryptoInfoAESGCM128
	info.Info.Version = abi.TLS_1_3_VERSION
	info.Info.CipherType = abi.TLS_CIPHER_AES_GCM_128
	_, _ = rand.Read(info.Key[:])
	_, _ = rand.Read(info.IV[:])
	infoBytes := unsafe.Slice((*byte)(unsafe.Pointer(&info)), unsafe.Sizeof(info))

	_ = clientSock.SetSockOptTLS(abi.TLS_TX, infoBytes)
	_ = serverSock.SetSockOptTLS(abi.TLS_RX, infoBytes)

	message := []byte("The quick brown fox jumps over the lazy dog. 1234567890!@#$%^&*()")

	go func() {
		_, _ = clientSock.Write(message)
	}()

	received := make([]byte, len(message))
	for i := 0; i < len(message); i++ {
		oneByte := make([]byte, 1)
		n, err := serverSock.Read(oneByte)
		if err != nil {
			t.Fatalf("read byte %d failed: %v", i, err)
		}
		if n != 1 {
			t.Fatalf("read byte %d returned n=%d", i, n)
		}
		received[i] = oneByte[0]
	}

	if !bytes.Equal(received, message) {
		t.Errorf("byte-by-byte mismatch: expected %q, got %q", string(message), string(received))
	}
}

// 8. Test Audit: Multi-record Chunking for Large Write (>16KB)
func TestAudit_MultiRecord_Chunking_LargeWrite(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	clientSock := NewTLSSocket(clientConn, SocketTuple{}, 1, 1, nil)
	serverSock := NewTLSSocket(serverConn, SocketTuple{}, 2, 2, nil)

	_ = clientSock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls")
	_ = serverSock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls")

	var info abi.TLS12CryptoInfoAESGCM128
	info.Info.Version = abi.TLS_1_2_VERSION
	info.Info.CipherType = abi.TLS_CIPHER_AES_GCM_128
	_, _ = rand.Read(info.Key[:])
	_, _ = rand.Read(info.IV[:])
	_, _ = rand.Read(info.Salt[:])
	infoBytes := unsafe.Slice((*byte)(unsafe.Pointer(&info)), unsafe.Sizeof(info))

	_ = clientSock.SetSockOptTLS(abi.TLS_TX, infoBytes)
	_ = serverSock.SetSockOptTLS(abi.TLS_RX, infoBytes)

	largePayload := make([]byte, 40000) // ~40KB -> 3 records (16KB + 16KB + 7216B)
	for i := range largePayload {
		largePayload[i] = byte(i % 251)
	}

	go func() {
		n, err := clientSock.Write(largePayload)
		if err != nil {
			t.Errorf("large write failed: %v", err)
		}
		if n != len(largePayload) {
			t.Errorf("expected %d bytes written, got %d", len(largePayload), n)
		}
	}()

	recvBuf := make([]byte, len(largePayload))
	if _, err := io.ReadFull(serverSock, recvBuf); err != nil {
		t.Fatalf("ReadFull failed: %v", err)
	}

	if !bytes.Equal(recvBuf, largePayload) {
		t.Errorf("large payload received content mismatch")
	}
}

// 9. Test Audit: IPv6 Socket Tuple Telemetry Serialization
func TestAudit_IPv6_TelemetryTap(t *testing.T) {
	tuple := SocketTuple{
		SrcIP:   net.ParseIP("2001:db8::1"),
		SrcPort: 8080,
		DstIP:   net.ParseIP("2001:db8::2"),
		DstPort: 9090,
	}
	pid := uint32(1234)
	tid := uint32(5678)
	payload := []byte("IPv6 telemetry payload")

	serialized := SerializeTelemetryEvent(tuple, pid, tid, payload)
	deserializedTuple, dPid, dTid, tsNS, dPayload, ok := DeserializeTelemetryEvent(serialized)
	if !ok {
		t.Fatalf("deserialization failed")
	}
	if tsNS == 0 {
		t.Errorf("timestamp was 0")
	}
	if dPid != pid || dTid != tid {
		t.Errorf("PID/TID mismatch: expected %d/%d, got %d/%d", pid, tid, dPid, dTid)
	}
	if !deserializedTuple.SrcIP.Equal(tuple.SrcIP) {
		t.Errorf("SrcIP mismatch: expected %v, got %v", tuple.SrcIP, deserializedTuple.SrcIP)
	}
	if !deserializedTuple.DstIP.Equal(tuple.DstIP) {
		t.Errorf("DstIP mismatch: expected %v, got %v", tuple.DstIP, deserializedTuple.DstIP)
	}
	if deserializedTuple.SrcPort != tuple.SrcPort || deserializedTuple.DstPort != tuple.DstPort {
		t.Errorf("Port mismatch: expected %d->%d, got %d->%d", tuple.SrcPort, tuple.DstPort, deserializedTuple.SrcPort, deserializedTuple.DstPort)
	}
	if !bytes.Equal(dPayload, payload) {
		t.Errorf("payload mismatch: expected %q, got %q", string(payload), string(dPayload))
	}
}

// 10. Test Audit: Invalid SockOpt error handling
func TestAudit_InvalidSockOpts(t *testing.T) {
	clientConn, _ := net.Pipe()
	defer clientConn.Close()

	sock := NewTLSSocket(clientConn, SocketTuple{}, 1, 1, nil)

	// Invalid Level
	if err := sock.SetSockOptTCP(123, abi.TCP_ULP, "tls"); err == nil {
		t.Errorf("expected error on invalid level")
	}
	// Invalid ULP name
	if err := sock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "invalid"); err == nil {
		t.Errorf("expected error on invalid ULP name")
	}
	// Valid ULP
	if err := sock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls"); err != nil {
		t.Errorf("SetSockOptTCP tls failed: %v", err)
	}
	// Duplicate ULP attachment
	if err := sock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls"); err != ErrAlreadyULP {
		t.Errorf("expected ErrAlreadyULP on duplicate attach, got %v", err)
	}
	// Buffer too small for TLS
	if err := sock.SetSockOptTLS(abi.TLS_TX, []byte{1, 2}); err == nil {
		t.Errorf("expected error on too small buffer")
	}
}

// 11. Test Audit: Concurrent Full-Duplex Read and Write (Testing Lock Contention / Deadlock)
func TestAudit_FullDuplex_ConcurrentReadWrite(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()

	clientCh := make(chan net.Conn, 1)
	go func() {
		conn, _ := ln.Accept()
		clientCh <- conn
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	defer clientConn.Close()

	serverConn := <-clientCh
	defer serverConn.Close()

	clientSock := NewTLSSocket(clientConn, SocketTuple{}, 1, 1, nil)
	serverSock := NewTLSSocket(serverConn, SocketTuple{}, 2, 2, nil)

	_ = clientSock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls")
	_ = serverSock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls")

	var info abi.TLS13CryptoInfoAESGCM128
	info.Info.Version = abi.TLS_1_3_VERSION
	info.Info.CipherType = abi.TLS_CIPHER_AES_GCM_128
	_, _ = rand.Read(info.Key[:])
	_, _ = rand.Read(info.IV[:])
	infoBytes := unsafe.Slice((*byte)(unsafe.Pointer(&info)), unsafe.Sizeof(info))

	// Client TX -> Server RX
	_ = clientSock.SetSockOptTLS(abi.TLS_TX, infoBytes)
	_ = serverSock.SetSockOptTLS(abi.TLS_RX, infoBytes)

	// Server TX -> Client RX
	_ = serverSock.SetSockOptTLS(abi.TLS_TX, infoBytes)
	_ = clientSock.SetSockOptTLS(abi.TLS_RX, infoBytes)

	// Start client reader FIRST (which blocks waiting for server data)
	clientReadDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 100)
		_, err := clientSock.Read(buf)
		clientReadDone <- err
	}()

	// Give the client read goroutine time to enter Read() and block on socket
	time.Sleep(50 * time.Millisecond)

	// Now client writes while client Read() is actively blocked!
	// If a single mutex is held by Read(), Write() will deadlock!
	writeDone := make(chan error, 1)
	go func() {
		_, err := clientSock.Write([]byte("ping"))
		writeDone <- err
	}()

	select {
	case err := <-writeDone:
		if err != nil {
			t.Fatalf("client Write failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("DEADLOCK DETECTED: client Write() blocked because client Read() holds socket lock!")
	}

	// Server reads the "ping"
	serverBuf := make([]byte, 100)
	n, err := serverSock.Read(serverBuf)
	if err != nil || string(serverBuf[:n]) != "ping" {
		t.Fatalf("server read ping failed: %v", err)
	}

	// Server replies with "pong" to unblock client Read
	_, _ = serverSock.Write([]byte("pong"))

	select {
	case err := <-clientReadDone:
		if err != nil {
			t.Fatalf("client Read failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("client Read did not complete")
	}
}

// 12. Test Audit: Concurrent Close while Read is Blocked (Testing Deadlock on Close with TCP socket)
func TestAudit_ConcurrentCloseWhileReadBlocked(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen failed: %v", err)
	}
	defer ln.Close()

	connCh := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err == nil {
			connCh <- c
		}
	}()

	clientConn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("Dial failed: %v", err)
	}
	serverConn := <-connCh
	defer serverConn.Close()

	clientSock := NewTLSSocket(clientConn, SocketTuple{}, 1, 1, nil)
	_ = clientSock.SetSockOptTCP(abi.SOL_TCP, abi.TCP_ULP, "tls")

	var info abi.TLS13CryptoInfoAESGCM128
	info.Info.Version = abi.TLS_1_3_VERSION
	info.Info.CipherType = abi.TLS_CIPHER_AES_GCM_128
	_, _ = rand.Read(info.Key[:])
	_, _ = rand.Read(info.IV[:])
	infoBytes := unsafe.Slice((*byte)(unsafe.Pointer(&info)), unsafe.Sizeof(info))
	_ = clientSock.SetSockOptTLS(abi.TLS_RX, infoBytes)

	readErrCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 100)
		_, err := clientSock.Read(buf)
		readErrCh <- err
	}()

	// Wait for Read() to block on network I/O
	time.Sleep(50 * time.Millisecond)

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- clientSock.Close()
	}()

	select {
	case err := <-closeDone:
		t.Logf("Close returned: %v", err)
		if err != nil {
			t.Fatalf("Close failed: %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("DEADLOCK DETECTED: Close() blocked because Read() holds socket lock!")
	}

	select {
	case err := <-readErrCh:
		t.Logf("Read returned: %v", err)
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("Read() failed to unblock after socket Close()")
	}

}

