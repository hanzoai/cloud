package cek

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"golang.org/x/crypto/hkdf"
)

type principalKind string

const (
	principalGlobal principalKind = "global"
	dekLen                        = 32
	wrapVersion                   = 1
)

func principalAAD(t principalKind, principalID string) []byte {
	return lengthPrefixedInfo(t, principalID)
}

func lengthPrefixedInfo(t principalKind, principalID string) []byte {
	tb := []byte(t)
	id := []byte(principalID)
	out := make([]byte, 0, binary.MaxVarintLen64*2+len(tb)+len(id))
	var tmp [binary.MaxVarintLen64]byte
	n := binary.PutUvarint(tmp[:], uint64(len(tb)))
	out = append(out, tmp[:n]...)
	out = append(out, tb...)
	n = binary.PutUvarint(tmp[:], uint64(len(id)))
	out = append(out, tmp[:n]...)
	out = append(out, id...)
	return out
}

func deriveKey(masterKey []byte, t principalKind, principalID string) ([]byte, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("cek: master key must be 32 bytes, got %d", len(masterKey))
	}
	if principalID == "" {
		return nil, fmt.Errorf("cek: principal ID cannot be empty")
	}
	r := hkdf.New(sha256.New, masterKey, nil, lengthPrefixedInfo(t, principalID))
	kek := make([]byte, 32)
	if _, err := io.ReadFull(r, kek); err != nil {
		return nil, fmt.Errorf("cek: hkdf: %w", err)
	}
	return kek, nil
}

func newDEK() ([]byte, error) {
	dek := make([]byte, dekLen)
	if _, err := rand.Read(dek); err != nil {
		return nil, fmt.Errorf("cek: generate DEK: %w", err)
	}
	return dek, nil
}

func wrapDEK(kek, dek, aad []byte) ([]byte, error) {
	if len(kek) != 32 {
		return nil, fmt.Errorf("cek: KEK must be 32 bytes, got %d", len(kek))
	}
	if len(dek) != dekLen {
		return nil, fmt.Errorf("cek: DEK must be %d bytes, got %d", dekLen, len(dek))
	}
	gcm, err := newGCM(kek)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("cek: generate nonce: %w", err)
	}
	out := make([]byte, 0, 1+len(nonce)+len(dek)+gcm.Overhead())
	out = append(out, wrapVersion)
	out = append(out, nonce...)
	out = gcm.Seal(out, nonce, dek, wrapAAD(aad))
	return out, nil
}

func unwrapDEK(kek, blob, aad []byte) ([]byte, error) {
	if len(kek) != 32 {
		return nil, fmt.Errorf("cek: KEK must be 32 bytes, got %d", len(kek))
	}
	gcm, err := newGCM(kek)
	if err != nil {
		return nil, err
	}
	ns := gcm.NonceSize()
	if len(blob) < 1+ns+gcm.Overhead() {
		return nil, fmt.Errorf("cek: wrapped DEK too short (%d bytes)", len(blob))
	}
	if blob[0] != wrapVersion {
		return nil, fmt.Errorf("cek: unsupported wrapped-DEK version %d", blob[0])
	}
	nonce := blob[1 : 1+ns]
	ct := blob[1+ns:]
	dek, err := gcm.Open(nil, nonce, ct, wrapAAD(aad))
	if err != nil {
		return nil, fmt.Errorf("cek: unwrap DEK (wrong key, wrong principal, or corrupt blob): %w", err)
	}
	if len(dek) != dekLen {
		return nil, fmt.Errorf("cek: unwrapped DEK has wrong length %d", len(dek))
	}
	return dek, nil
}

func wrapAAD(aad []byte) []byte {
	out := make([]byte, 0, 1+len(aad))
	out = append(out, wrapVersion)
	out = append(out, aad...)
	return out
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("cek: aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("cek: gcm: %w", err)
	}
	return gcm, nil
}
