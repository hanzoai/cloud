package integrations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// signed reports whether one of sigs is an HMAC-SHA256 of body under secret.
// It is the one verifier every provider's delivery goes through — GitHub sends
// "sha256=<hex>" in X-Hub-Signature-256, Linear a bare hex in Linear-Signature,
// the forge a bare hex under X-Git-Signature and its two older spellings — so a
// receiver hands over the headers it reads and nothing else about the wire.
//
// Constant-time on the digest, and closed on every failure: an empty secret, an
// empty or malformed signature, or a digest over other bytes all answer false
// and never panic. The receiver decides what a false means (401) and never
// decodes a body this has not vouched for.
func signed(secret string, body []byte, sigs ...string) bool {
	if secret == "" {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := mac.Sum(nil)
	for _, sig := range sigs {
		got, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(sig), "sha256="))
		if err != nil || len(got) == 0 {
			continue
		}
		if hmac.Equal(got, want) {
			return true
		}
	}
	return false
}
