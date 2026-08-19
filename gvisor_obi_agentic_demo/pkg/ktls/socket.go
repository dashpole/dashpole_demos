// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package ktls

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
)

var (
	ErrNotTLSULP      = errors.New("ktls: socket is not in TLS ULP mode")
	ErrCryptoNotSet   = errors.New("ktls: crypto info has not been configured")
	ErrAlreadyULP     = errors.New("ktls: ULP already attached")
	ErrConnectionDown = errors.New("ktls: connection closed")
)

// TLSSocket wraps an underlying TCP connection with full-duplex Sentry kTLS emulation.
type TLSSocket struct {
	txMu       sync.Mutex
	rxMu       sync.Mutex
	conn       net.Conn
	tuple      SocketTuple
	pid        uint32
	tid        uint32
	ulpEnabled atomic.Bool
	txCrypto   *CryptoContext
	rxCrypto   *CryptoContext
	tapSink    TelemetrySink
	rxBuffer   []byte // Buffer for decrypted bytes not yet read by application
	closed     atomic.Bool
}

// NewTLSSocket wraps a standard TCP connection with kTLS capabilities.
func NewTLSSocket(conn net.Conn, tuple SocketTuple, pid, tid uint32, tapSink TelemetrySink) *TLSSocket {
	return &TLSSocket{
		conn:     conn,
		tuple:    tuple,
		pid:      pid,
		tid:      tid,
		tapSink:  tapSink,
		rxBuffer: make([]byte, 0, abi.TLS_MAX_PAYLOAD_SIZE),
	}
}

// SetSockOptTCP handles SOL_TCP options like TCP_ULP.
func (s *TLSSocket) SetSockOptTCP(level int, name int, val string) error {
	if level != abi.SOL_TCP {
		return fmt.Errorf("invalid level %d", level)
	}

	if name == abi.TCP_ULP {
		if val != "tls" {
			return fmt.Errorf("unsupported ULP %q, only \"tls\" supported", val)
		}
		if s.ulpEnabled.Swap(true) {
			return ErrAlreadyULP
		}
		return nil
	}

	return fmt.Errorf("unsupported TCP option %d", name)
}

// SetSockOptTLS handles SOL_TLS options like TLS_TX and TLS_RX.
func (s *TLSSocket) SetSockOptTLS(name int, optVal []byte) error {
	if !s.ulpEnabled.Load() {
		return ErrNotTLSULP
	}

	if len(optVal) < int(unsafe.Sizeof(abi.TLSCryptoInfo{})) {
		return fmt.Errorf("crypto info buffer too small: %d", len(optVal))
	}

	version := binary.LittleEndian.Uint16(optVal[0:2])
	cipherType := binary.LittleEndian.Uint16(optVal[2:4])

	var key, iv, salt []byte
	var startSeq uint64

	switch cipherType {
	case abi.TLS_CIPHER_AES_GCM_128:
		if version == abi.TLS_1_2_VERSION {
			if len(optVal) < int(unsafe.Sizeof(abi.TLS12CryptoInfoAESGCM128{})) {
				return fmt.Errorf("buffer too small for TLS 1.2 AES-GCM-128")
			}
			info := (*abi.TLS12CryptoInfoAESGCM128)(unsafe.Pointer(&optVal[0]))
			iv = info.IV[:]
			key = info.Key[:]
			salt = info.Salt[:]
			startSeq = binary.BigEndian.Uint64(info.RecSeq[:])
		} else if version == abi.TLS_1_3_VERSION {
			if len(optVal) < int(unsafe.Sizeof(abi.TLS13CryptoInfoAESGCM128{})) {
				return fmt.Errorf("buffer too small for TLS 1.3 AES-GCM-128")
			}
			info := (*abi.TLS13CryptoInfoAESGCM128)(unsafe.Pointer(&optVal[0]))
			iv = info.IV[:]
			key = info.Key[:]
			startSeq = binary.BigEndian.Uint64(info.RecSeq[:])
		} else {
			return ErrUnsupportedProto
		}

	case abi.TLS_CIPHER_AES_GCM_256:
		if version == abi.TLS_1_2_VERSION {
			if len(optVal) < int(unsafe.Sizeof(abi.TLS12CryptoInfoAESGCM256{})) {
				return fmt.Errorf("buffer too small for TLS 1.2 AES-GCM-256")
			}
			info := (*abi.TLS12CryptoInfoAESGCM256)(unsafe.Pointer(&optVal[0]))
			iv = info.IV[:]
			key = info.Key[:]
			salt = info.Salt[:]
			startSeq = binary.BigEndian.Uint64(info.RecSeq[:])
		} else if version == abi.TLS_1_3_VERSION {
			if len(optVal) < int(unsafe.Sizeof(abi.TLS13CryptoInfoAESGCM256{})) {
				return fmt.Errorf("buffer too small for TLS 1.3 AES-GCM-256")
			}
			info := (*abi.TLS13CryptoInfoAESGCM256)(unsafe.Pointer(&optVal[0]))
			iv = info.IV[:]
			key = info.Key[:]
			startSeq = binary.BigEndian.Uint64(info.RecSeq[:])
		} else {
			return ErrUnsupportedProto
		}

	default:
		return ErrUnsupportedProto
	}

	ctx, err := NewCryptoContext(version, cipherType, key, iv, salt, startSeq)
	if err != nil {
		return err
	}

	if name == abi.TLS_TX {
		s.txMu.Lock()
		s.txCrypto = ctx
		s.txMu.Unlock()
	} else if name == abi.TLS_RX {
		s.rxMu.Lock()
		s.rxCrypto = ctx
		s.rxMu.Unlock()
	} else {
		return fmt.Errorf("unknown TLS sockopt %d", name)
	}

	return nil
}

// Write encrypts and transmits application plaintext, tapping the stream for telemetry.
// Thread-safe for concurrent writes with concurrent reads.
func (s *TLSSocket) Write(p []byte) (int, error) {
	s.txMu.Lock()
	defer s.txMu.Unlock()

	if s.closed.Load() {
		return 0, ErrConnectionDown
	}

	// If kTLS TX is not configured, write raw plaintext to underlying socket
	if !s.ulpEnabled.Load() || s.txCrypto == nil {
		return s.conn.Write(p)
	}

	// Plaintext Tap Point (TX)
	if s.tapSink != nil && len(p) > 0 {
		s.tapSink.EmitKTLSTx(s.tuple, s.pid, s.tid, p)
	}

	// Chunk plaintext into TLS records up to 16KB
	totalWritten := 0
	for totalWritten < len(p) {
		chunkSize := len(p) - totalWritten
		if chunkSize > abi.TLS_MAX_PAYLOAD_SIZE {
			chunkSize = abi.TLS_MAX_PAYLOAD_SIZE
		}
		chunk := p[totalWritten : totalWritten+chunkSize]

		recordFrame, err := s.txCrypto.SealRecord(abi.TLS_RECORD_TYPE_DATA, chunk)
		if err != nil {
			return totalWritten, fmt.Errorf("failed to seal TLS record: %w", err)
		}

		if _, err := s.conn.Write(recordFrame); err != nil {
			return totalWritten, err
		}
		totalWritten += chunkSize
	}

	return totalWritten, nil
}

// Read reads from the socket, decrypting incoming TLS records and tapping plaintext.
// Thread-safe for concurrent reads with concurrent writes.
func (s *TLSSocket) Read(p []byte) (int, error) {
	s.rxMu.Lock()
	defer s.rxMu.Unlock()

	if s.closed.Load() {
		return 0, ErrConnectionDown
	}

	// If kTLS RX is not configured, read raw from underlying socket
	if !s.ulpEnabled.Load() || s.rxCrypto == nil {
		return s.conn.Read(p)
	}

	// Drain any previously decrypted buffered data first
	if len(s.rxBuffer) > 0 {
		n := copy(p, s.rxBuffer)
		s.rxBuffer = s.rxBuffer[n:]
		return n, nil
	}

	// Read 5-byte TLS record header
	header := make([]byte, abi.TLS_RECORD_HEADER_SIZE)
	if _, err := io.ReadFull(s.conn, header); err != nil {
		return 0, err
	}

	length := binary.BigEndian.Uint16(header[3:5])
	recordFrame := make([]byte, abi.TLS_RECORD_HEADER_SIZE+int(length))
	copy(recordFrame[0:5], header)

	if _, err := io.ReadFull(s.conn, recordFrame[5:]); err != nil {
		return 0, err
	}

	// Decrypt record
	_, decrypted, err := s.rxCrypto.OpenRecord(recordFrame)
	if err != nil {
		return 0, err
	}

	// Plaintext Tap Point (RX)
	if s.tapSink != nil && len(decrypted) > 0 {
		s.tapSink.EmitKTLSRx(s.tuple, s.pid, s.tid, decrypted)
	}

	// Copy into user buffer
	n := copy(p, decrypted)
	if n < len(decrypted) {
		s.rxBuffer = append(s.rxBuffer[:0], decrypted[n:]...)
	}

	return n, nil
}

// Close closes the socket and underlying connection.
func (s *TLSSocket) Close() error {
	if s.closed.Swap(true) {
		return nil
	}
	return s.conn.Close()
}
