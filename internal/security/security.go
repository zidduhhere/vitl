// Package security adds a lightweight, pre-shared-key based confidentiality
// and authentication layer on top of the otherwise-plaintext VitalLink UDP
// protocol. This is deliberately not a full TLS/DTLS handshake — the
// bandwidth budget the field link is judged on doesn't have room for
// handshake overhead, and both ends of the link are provisioned out of
// band (the same PSK is baked into the server and every field device at
// deploy time), so no runtime key exchange is needed.
package security

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
)

// KeySize is the AES-256 key size in bytes.
const KeySize = 32

// ErrSealedTooShort is returned by Open when the input is smaller than the
// nonce, meaning it can't possibly be a validly-sealed packet.
var ErrSealedTooShort = errors.New("security: sealed packet too short")

// DeriveKey turns an arbitrary pre-shared passphrase into a fixed-size
// AES-256 key. Using SHA-256 here (rather than requiring operators to
// supply exactly 32 random bytes) keeps deployment simple: any string works.
func DeriveKey(psk string) [KeySize]byte {
	return sha256.Sum256([]byte(psk))
}

// Seal encrypts and authenticates plaintext with AES-256-GCM, returning
// nonce||ciphertext||tag as a single buffer ready to put on the wire.
func Seal(key [KeySize]byte, plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Open reverses Seal, verifying the GCM tag and returning the original
// plaintext. A corrupted or tampered packet — or one sealed under a
// different key — returns an error, which callers should treat the same
// way they treat a checksum failure: drop it, no ACK.
func Open(key [KeySize]byte, sealed []byte) ([]byte, error) {
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(sealed) < gcm.NonceSize() {
		return nil, ErrSealedTooShort
	}
	nonce, ciphertext := sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ciphertext, nil)
}

// SealingWriter wraps an io.Writer (typically a UDP socket) so every Write
// call seals its payload with Seal before handing it off — lets existing
// send paths (transport.PrioritySender, transport.MediaSender) gain
// encryption by construction rather than by remembering to call Seal at
// every call site.
type SealingWriter struct {
	W   io.Writer
	Key [KeySize]byte
}

// Write reports len(plaintext) on success, matching the io.Writer contract
// from the caller's point of view even though more bytes go out on the
// wire (nonce+tag overhead).
func (s SealingWriter) Write(plaintext []byte) (int, error) {
	sealed, err := Seal(s.Key, plaintext)
	if err != nil {
		return 0, err
	}
	if _, err := s.W.Write(sealed); err != nil {
		return 0, err
	}
	return len(plaintext), nil
}

// DeriveWorkerToken computes the per-worker credential a field device must
// present in SESSION_INIT to prove it's a registered device rather than
// just "anyone who knows a patient_id". It's an HMAC-SHA256 of the
// workerID under the shared key, truncated to a uint32 — real production
// would issue distinct per-device secrets with revocation; for this PSK
// deployment model, binding the token to workerID is enough to stop a
// device that doesn't know the key from opening sessions.
func DeriveWorkerToken(key [KeySize]byte, workerID uint32) uint32 {
	var idBytes [4]byte
	binary.BigEndian.PutUint32(idBytes[:], workerID)
	mac := hmac.New(sha256.New, key[:])
	mac.Write(idBytes[:])
	sum := mac.Sum(nil)
	return binary.BigEndian.Uint32(sum[:4])
}
