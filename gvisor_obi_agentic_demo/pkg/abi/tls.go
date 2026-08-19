// Copyright 2026 The OpenTelemetry Authors / Google LLC
// SPDX-License-Identifier: Apache-2.0

package abi

const (
	// Socket Levels
	SOL_TCP = 6
	SOL_TLS = 282

	// Socket Options for SOL_TCP
	TCP_ULP = 31

	// Socket Options for SOL_TLS
	TLS_TX             = 1
	TLS_RX             = 2
	TLS_TX_ZEROCOPY_RO = 3

	// TLS Protocol Versions
	TLS_1_2_VERSION_MAJOR = 0x3
	TLS_1_2_VERSION_MINOR = 0x3
	TLS_1_2_VERSION       = (TLS_1_2_VERSION_MAJOR << 8) | TLS_1_2_VERSION_MINOR // 0x0303

	TLS_1_3_VERSION_MAJOR = 0x3
	TLS_1_3_VERSION_MINOR = 0x4
	TLS_1_3_VERSION       = (TLS_1_3_VERSION_MAJOR << 8) | TLS_1_3_VERSION_MINOR // 0x0304

	// Supported Cipher Suites (Linux UAPI)
	TLS_CIPHER_AES_GCM_128       = 51
	TLS_CIPHER_AES_GCM_256       = 52
	TLS_CIPHER_AES_CCM_128       = 53
	TLS_CIPHER_CHACHA20_POLY1305 = 54

	// TLS Record Content Types
	TLS_RECORD_TYPE_CHANGE_CIPHER_SPEC = 20
	TLS_RECORD_TYPE_ALERT              = 21
	TLS_RECORD_TYPE_HANDSHAKE          = 22
	TLS_RECORD_TYPE_DATA               = 23 // Application Data

	// GCM Tag and Header Constants
	TLS_RECORD_HEADER_SIZE = 5
	TLS_GCM_TAG_SIZE       = 16
	TLS_MAX_PAYLOAD_SIZE   = 16384 // 16KB max TLS record
)

// TLSCryptoInfo is the base header matching Linux UAPI struct tls_crypto_info.
type TLSCryptoInfo struct {
	Version    uint16
	CipherType uint16
}

// TLS12CryptoInfoAESGCM128 matches Linux UAPI struct tls12_crypto_info_aes_gcm_128.
type TLS12CryptoInfoAESGCM128 struct {
	Info   TLSCryptoInfo
	IV     [8]byte
	Key    [16]byte
	Salt   [4]byte
	RecSeq [8]byte
}

// TLS12CryptoInfoAESGCM256 matches Linux UAPI struct tls12_crypto_info_aes_gcm_256.
type TLS12CryptoInfoAESGCM256 struct {
	Info   TLSCryptoInfo
	IV     [8]byte
	Key    [32]byte
	Salt   [4]byte
	RecSeq [8]byte
}

// TLS13CryptoInfoAESGCM128 matches Linux UAPI struct tls13_crypto_info_aes_gcm_128.
type TLS13CryptoInfoAESGCM128 struct {
	Info   TLSCryptoInfo
	IV     [12]byte
	Key    [16]byte
	Salt   [0]byte
	RecSeq [8]byte
}

// TLS13CryptoInfoAESGCM256 matches Linux UAPI struct tls13_crypto_info_aes_gcm_256.
type TLS13CryptoInfoAESGCM256 struct {
	Info   TLSCryptoInfo
	IV     [12]byte
	Key    [32]byte
	Salt   [0]byte
	RecSeq [8]byte
}
