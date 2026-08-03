package org

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"fmt"

	"github.com/hanzoai/cek"
	"github.com/hanzoai/namespace"
)

// Cipher seals one database's snapshot at rest, so the object in SeaweedFS is
// ciphertext and a leaked file is useless without the KMS master key.
//
// The key is cek's, and only cek's: cek.DeriveKey(master, ns, subsystem) — the
// same derivation, from the same master, that keyed the local file this
// snapshot is a copy of. A file and its snapshot are two renderings of one key,
// and the estate has one place that turns a master into a key. This package
// used to hold a second HKDF of its own, which meant the local file and the
// object it ships to were keyed by two different functions that no test
// compared; keeping them equal was a promise in a comment rather than a
// property of the code.
//
// Binding the subsystem is what the org slug alone could not do. Every database
// an org owned used to seal under a single key, so its settings snapshot and
// its ledger snapshot were interchangeable to anyone holding either one. cek
// binds the subsystem into the derivation, so they no longer are.
//
// AES-256-GCM gives confidentiality and integrity. The AAD is the same
// namespace/subsystem binding the key was derived under, so a blob lifted into
// another database's slot fails the tag instead of decrypting to garbage.
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
	nonceLabel = "hanzo/org-db/nonce/v1"
)

// NewCipher builds a Cipher from the KMS master key. master must be exactly 32
// bytes — the same master SetMaster installs, so the snapshot and the file it
// came from derive from one secret.
func NewCipher(master []byte) (*Cipher, error) {
	if len(master) != keyLen {
		return nil, fmt.Errorf("org: cipher master key must be %d bytes, got %d", keyLen, len(master))
	}
	cp := make([]byte, keyLen)
	copy(cp, master)
	return &Cipher{master: cp}, nil
}

// Seal encrypts a snapshot of ns's subsystem database, returning
// nonce||ciphertext(+tag).
func (c *Cipher) Seal(ns namespace.Namespace, subsystem string, plaintext []byte) ([]byte, error) {
	gcm, key, aad, err := c.gcm(ns, subsystem)
	if err != nil {
		return nil, err
	}
	nonce := deriveNonce(key, plaintext)
	// Prefix the nonce; AAD binds the ciphertext to its database.
	out := make([]byte, nonceLen, nonceLen+len(plaintext)+gcm.Overhead())
	copy(out, nonce)
	return gcm.Seal(out, nonce, plaintext, aad), nil
}

// Open decrypts a blob sealed for the same namespace and subsystem. It fails
// (auth error) if the blob was tampered with, or was sealed for another
// database.
func (c *Cipher) Open(ns namespace.Namespace, subsystem string, sealed []byte) ([]byte, error) {
	if len(sealed) < nonceLen {
		return nil, fmt.Errorf("org: sealed blob too short")
	}
	gcm, _, aad, err := c.gcm(ns, subsystem)
	if err != nil {
		return nil, err
	}
	nonce, ct := sealed[:nonceLen], sealed[nonceLen:]
	pt, err := gcm.Open(nil, nonce, ct, aad)
	if err != nil {
		return nil, fmt.Errorf("org: open %s/%s: %w", ns, subsystem, err)
	}
	return pt, nil
}

// gcm derives this database's key through cek and returns an AES-256-GCM AEAD
// over it, alongside the binding this snapshot is authenticated under.
func (c *Cipher) gcm(ns namespace.Namespace, subsystem string) (cipher.AEAD, []byte, []byte, error) {
	key, err := cek.DeriveKey(c.master, ns, subsystem)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("org: derive snapshot key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("org: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("org: gcm: %w", err)
	}
	// The same (namespace, subsystem) pair the key was derived under, so the
	// blob authenticates against the database it belongs to.
	return gcm, key, []byte(ns.String() + "/" + subsystem), nil
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
