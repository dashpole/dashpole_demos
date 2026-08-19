// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package ktls

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/dashpole/dashpole_demos/gvisor_obi_agentic_demo/pkg/abi"
)

var (
	ErrInvalidRecord    = errors.New("ktls: invalid TLS record frame")
	ErrBadMessage       = errors.New("ktls: bad message authentication tag (EBADMSG)")
	ErrUnsupportedProto = errors.New("ktls: unsupported protocol version or cipher")
)

// CryptoContext manages symmetric encryption state for one direction (TX or RX).
type CryptoContext struct {
	Version    uint16
	CipherType uint16
	Key        []byte
	IV         []byte
	Salt       []byte
	SeqNum     uint64
	Aead       cipher.AEAD
}

// NewCryptoContext creates a new TX or RX crypto state from Linux UAPI structs.
func NewCryptoContext(version uint16, cipherType uint16, key, iv, salt []byte, startSeq uint64) (*CryptoContext, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("failed to create AES cipher: %w", err)
	}

	aead, err := cipher.NewGCMWithNonceSize(block, 12)
	if err != nil {
		return nil, fmt.Errorf("failed to create GCM AEAD: %w", err)
	}

	return &CryptoContext{
		Version:    version,
		CipherType: cipherType,
		Key:        key,
		IV:         iv,
		Salt:       salt,
		SeqNum:     startSeq,
		Aead:       aead,
	}, nil
}

// DeriveNonce calculates the 12-byte per-record nonce from IV and Sequence Number.
func (c *CryptoContext) DeriveNonce() [12]byte {
	var nonce [12]byte
	if c.Version == abi.TLS_1_3_VERSION {
		// TLS 1.3: Nonce = IV XOR SeqNum (padded to 12 bytes on left)
		copy(nonce[:], c.IV)
		var seqBytes [12]byte
		binary.BigEndian.PutUint64(seqBytes[4:], c.SeqNum)
		for i := 0; i < 12; i++ {
			nonce[i] ^= seqBytes[i]
		}
	} else {
		// TLS 1.2: Nonce = Salt (4B) || (IV[0..7] XOR SeqNum[8B])
		copy(nonce[0:4], c.Salt)
		copy(nonce[4:12], c.IV[0:8])
		var seqBytes [8]byte
		binary.BigEndian.PutUint64(seqBytes[:], c.SeqNum)
		for i := 0; i < 8; i++ {
			nonce[4+i] ^= seqBytes[i]
		}
	}
	return nonce
}

// SealRecord encrypts a plaintext payload into a framed TLS record.
func (c *CryptoContext) SealRecord(contentType uint8, plaintext []byte) ([]byte, error) {
	if len(plaintext) > abi.TLS_MAX_PAYLOAD_SIZE {
		return nil, fmt.Errorf("plaintext size %d exceeds max record size %d", len(plaintext), abi.TLS_MAX_PAYLOAD_SIZE)
	}

	nonce := c.DeriveNonce()
	defer func() { c.SeqNum++ }()

	var aad []byte
	var payloadToEncrypt []byte

	if c.Version == abi.TLS_1_3_VERSION {
		// TLS 1.3: Real content type appended to plaintext before encryption
		payloadToEncrypt = make([]byte, len(plaintext)+1)
		copy(payloadToEncrypt, plaintext)
		payloadToEncrypt[len(plaintext)] = contentType

		cipherLen := len(payloadToEncrypt) + abi.TLS_GCM_TAG_SIZE
		// AAD is the 5-byte outer record header
		aad = make([]byte, 5)
		aad[0] = abi.TLS_RECORD_TYPE_DATA // Outer type always 23
		binary.BigEndian.PutUint16(aad[1:3], abi.TLS_1_2_VERSION) // Outer version always 0x0303
		binary.BigEndian.PutUint16(aad[3:5], uint16(cipherLen))
	} else {
		// TLS 1.2: AAD = SeqNum (8B) || ContentType (1B) || Version (2B) || Length (2B)
		payloadToEncrypt = plaintext
		aad = make([]byte, 13)
		binary.BigEndian.PutUint64(aad[0:8], c.SeqNum)
		aad[8] = contentType
		binary.BigEndian.PutUint16(aad[9:11], c.Version)
		binary.BigEndian.PutUint16(aad[11:13], uint16(len(plaintext)))
	}

	// Encrypt: AEAD.Seal appends ciphertext + 16B tag
	ciphertextWithTag := c.Aead.Seal(nil, nonce[:], payloadToEncrypt, aad)

	// Format full wire record: [Header (5B)][Ciphertext + Tag]
	recordFrame := make([]byte, abi.TLS_RECORD_HEADER_SIZE+len(ciphertextWithTag))
	if c.Version == abi.TLS_1_3_VERSION {
		copy(recordFrame[0:5], aad)
	} else {
		recordFrame[0] = contentType
		binary.BigEndian.PutUint16(recordFrame[1:3], c.Version)
		binary.BigEndian.PutUint16(recordFrame[3:5], uint16(len(ciphertextWithTag)))
	}
	copy(recordFrame[5:], ciphertextWithTag)

	return recordFrame, nil
}

// OpenRecord decrypts a framed TLS record and authenticates the GCM tag.
func (c *CryptoContext) OpenRecord(recordFrame []byte) (uint8, []byte, error) {
	if len(recordFrame) < abi.TLS_RECORD_HEADER_SIZE+abi.TLS_GCM_TAG_SIZE {
		return 0, nil, ErrInvalidRecord
	}

	hdrType := recordFrame[0]
	hdrVersion := binary.BigEndian.Uint16(recordFrame[1:3])
	hdrLength := int(binary.BigEndian.Uint16(recordFrame[3:5]))

	if len(recordFrame) != abi.TLS_RECORD_HEADER_SIZE+hdrLength {
		return 0, nil, ErrInvalidRecord
	}

	ciphertextWithTag := recordFrame[abi.TLS_RECORD_HEADER_SIZE:]
	nonce := c.DeriveNonce()
	defer func() { c.SeqNum++ }()

	var aad []byte
	if c.Version == abi.TLS_1_3_VERSION {
		aad = recordFrame[0:5]
	} else {
		plaintextLen := hdrLength - abi.TLS_GCM_TAG_SIZE
		aad = make([]byte, 13)
		binary.BigEndian.PutUint64(aad[0:8], c.SeqNum)
		aad[8] = hdrType
		binary.BigEndian.PutUint16(aad[9:11], hdrVersion)
		binary.BigEndian.PutUint16(aad[11:13], uint16(plaintextLen))
	}

	decrypted, err := c.Aead.Open(nil, nonce[:], ciphertextWithTag, aad)
	if err != nil {
		return 0, nil, ErrBadMessage
	}

	if c.Version == abi.TLS_1_3_VERSION {
		// Strip padding and extract real content type from tail
		for len(decrypted) > 0 && decrypted[len(decrypted)-1] == 0 {
			decrypted = decrypted[:len(decrypted)-1]
		}
		if len(decrypted) == 0 {
			return 0, nil, ErrInvalidRecord
		}
		realType := decrypted[len(decrypted)-1]
		plaintext := decrypted[:len(decrypted)-1]
		return realType, plaintext, nil
	}

	return hdrType, decrypted, nil
}
