package org

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
)

// Cipher seals each org's SQLite snapshot at rest with a distinct 256-bit key,
// so the object in SeaweedFS is ciphertext and orgs are cryptographically
// isolated — a leaked file is useless without the KMS master key.
//
// Envelope model: the KMS master (KMSMasterKeyRef, 32 bytes) never leaves the
// process. A per-org key is DERIVED from it via HKDF(master, label, orgID); it is
// held only in-memory, never stored, never on the wire. Rotating the master
// re-keys every org. AES-256-GCM provides confidentiality + integrity; the orgID
// is bound in as AAD so a sealed blob cannot be replayed under a different org.
//
// The GCM nonce is DERIVED from (key, plaintext) — HMAC(key,"nonce"||plaintext),
// so identical plaintext seals to identical ciphertext (content-addressable,
// keeps the Replicator's version-skip working) while distinct plaintexts get
// distinct nonces (GCM safety: the same (key,nonce) never covers two different
// messages).
type Cipher struct {
	master []byte // 32-byte KMS master; copied on construction, never mutated
}

const (
	keyLen     = 32 // AES-256
	nonceLen   = 12 // GCM standard nonce
	hkdfLabel  = "hanzo/org-db/v1"
	nonceLabel = "hanzo/org-db/nonce/v1"
)

// NewCipher builds a Cipher from the KMS master key. master must be exactly 32
// bytes (derive it from KMSMasterKeyRef, e.g. SHA-256 of the raw secret).
func NewCipher(master []byte) (*Cipher, error) {
	if len(master) != keyLen {
		return nil, fmt.Errorf("org: cipher master key must be %d bytes, got %d", keyLen, len(master))
	}
	cp := make([]byte, keyLen)
	copy(cp, master)
	return &Cipher{master: cp}, nil
}

// Seal encrypts plaintext for orgID, returning nonce||ciphertext(+tag).
func (c *Cipher) Seal(orgID string, plaintext []byte) ([]byte, error) {
	gcm, key, err := c.gcm(orgID)
	if err != nil {
		return nil, err
	}
	nonce := deriveNonce(key, plaintext)
	// Prefix the nonce; AAD binds the ciphertext to the org.
	out := make([]byte, nonceLen, nonceLen+len(plaintext)+gcm.Overhead())
	copy(out, nonce)
	return gcm.Seal(out, nonce, plaintext, []byte(orgID)), nil
}

// Open decrypts a sealed blob for orgID. It fails (auth error) if the blob was
// tampered with, or was sealed for a different org.
func (c *Cipher) Open(orgID string, sealed []byte) ([]byte, error) {
	if len(sealed) < nonceLen {
		return nil, fmt.Errorf("org: sealed blob too short")
	}
	gcm, _, err := c.gcm(orgID)
	if err != nil {
		return nil, err
	}
	nonce, ct := sealed[:nonceLen], sealed[nonceLen:]
	pt, err := gcm.Open(nil, nonce, ct, []byte(orgID))
	if err != nil {
		return nil, fmt.Errorf("org: open %s: %w", orgID, err)
	}
	return pt, nil
}

// gcm derives the per-org key and returns an AES-256-GCM AEAD over it.
func (c *Cipher) gcm(orgID string) (cipher.AEAD, []byte, error) {
	key := deriveKey(c.master, orgID)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, fmt.Errorf("org: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, fmt.Errorf("org: gcm: %w", err)
	}
	return gcm, key, nil
}

// deriveKey is HKDF(master; salt=label; info=orgID) → 32 bytes. Extract:
// PRK = HMAC(salt, master). Expand: OKM = HMAC(PRK, info||0x01)[:32] — one block
// of SHA-256 is exactly 32 bytes, so a single expand round suffices.
func deriveKey(master []byte, orgID string) []byte {
	prk := hmacSum([]byte(hkdfLabel), master)
	okm := hmacSum(prk, append([]byte(orgID), 0x01))
	return okm[:keyLen]
}

// deriveNonce = HMAC(key, nonceLabel||plaintext)[:12] — deterministic per
// (key, plaintext) so identical content seals identically; unique across
// distinct plaintexts so GCM's (key,nonce) uniqueness holds.
func deriveNonce(key, plaintext []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(nonceLabel))
	m.Write(plaintext)
	return m.Sum(nil)[:nonceLen]
}

func hmacSum(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

// ConstantTimeEqual compares two secrets without leaking timing — exported for
// callers verifying wrapped-key material.
func ConstantTimeEqual(a, b []byte) bool { return subtle.ConstantTimeCompare(a, b) == 1 }
