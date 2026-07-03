package framework

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Password fields — hash on write, redact on read.
//
// A DocField of fieldtype Password NEVER stores or returns a plaintext value.
// This engine hashes it one-way with argon2id on every write and redacts it on
// every read, so the stored data and any list/detail response carry a hash a
// caller can neither read back nor reverse. This is the fail-secure, law-abiding
// choice ("always HASH, never plaintext"): a Password field is a write-only
// credential you can SET and VERIFY (via verifyPassword, available to hooks), not
// retrieve. A DocType that needs a RETRIEVABLE secret must use KMS (kms.hanzo.ai),
// not a Password field — secret storage is KMS's job, not the document store's.
//
// argon2id parameters follow the OWASP baseline (m=19 MiB, t=2, p=1). The encoded
// form is the standard PHC string "$argon2id$v=19$m=...,t=...,p=...$salt$hash", so
// it is self-describing and the prefix identifies an already-hashed value
// (idempotent re-writes never double-hash).
const (
	argonMemoryKiB = 19 * 1024 // 19 MiB
	argonTime      = 2
	argonThreads   = 1
	argonKeyLen    = 32
	argonSaltLen   = 16
	argonPrefix    = "$argon2id$"
	// redactedMarker is returned for a Password field that IS set. It is a fixed
	// sentinel — never the hash, never the plaintext — so a client can tell "a
	// secret is set" without learning anything crackable. An unset field is "".
	redactedMarker = "__set__"
)

// hashPassword returns the argon2id PHC-encoded hash of plaintext.
func hashPassword(plaintext string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password salt: %w", err)
	}
	key := argon2.IDKey([]byte(plaintext), salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	b64 := base64.RawStdEncoding.EncodeToString
	return fmt.Sprintf("%sv=%d$m=%d,t=%d,p=%d$%s$%s",
		argonPrefix, argon2.Version, argonMemoryKiB, argonTime, argonThreads,
		b64(salt), b64(key)), nil
}

// isHashed reports whether s is already an argon2id PHC hash produced here, so a
// re-save of an unchanged, already-redacted document never re-hashes a hash.
func isHashed(s string) bool { return strings.HasPrefix(s, argonPrefix) }

// verifyPassword reports whether candidate matches the argon2id-encoded hash. It
// is constant-time in the digest comparison. Exposed for hooks that implement a
// credential check on a Password field; not wired to any route in v1.
func verifyPassword(encoded, candidate string) bool {
	// $argon2id$v=19$m=..,t=..,p=..$salt$hash
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var mem, tm, th int
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &tm, &th); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(candidate), salt, uint32(tm), uint32(mem), uint8(th), uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
