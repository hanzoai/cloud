package analytics

import (
	"testing"
	"time"
)

// A stack/message carrying an email, a bearer token, an sk- key, and a
// ?access_token= query secret must be redacted before it reaches ANY column — the
// message, and every stack frame parsed out of the stack text. A frame's `file` is a
// URL and carries query secrets as readily as a message does, which is why it is
// scrubbed rather than trusted for being a path.
func TestErrorFactRedactsSecretsAndPII(t *testing.T) {
	e := CaptureEvent{
		Type: "error",
		Error: &Exception{
			Type:    "Error",
			Message: "login failed for alice@example.com with sk-live-abcdef0123456789",
			Stack:   "at fetch (https://api.x.com/v1?access_token=tok_abc123def456:12:3)\n  at auth (https://api.x.com/a.js:4:5)",
		},
	}
	f, ok := normalize("acme", time.Now(), e)
	if !ok || f.fault == nil {
		t.Fatalf("want an error fact, got ok=%v fault=%v", ok, f.fault)
	}
	seen := []string{f.fault.message}
	for _, fr := range f.fault.frames {
		seen = append(seen, fr.file, fr.function)
	}
	for _, s := range seen {
		if containsAny(s, "alice@example.com", "sk-live-abcdef0123456789", "tok_abc123def456") {
			t.Fatalf("secret/PII survived scrub: %q", s)
		}
	}
	// the caller's struct must NOT be mutated (copy semantics)
	if e.Error.Message == f.fault.message {
		t.Fatal("normalize mutated the caller's Exception")
	}
	// scrubValue must also handle *Exception directly (defense in depth)
	sv, _ := scrubValue(&Exception{Message: "x@y.com Bearer sk-abcdef0123456789"}).(*Exception)
	if sv == nil || containsAny(sv.Message, "x@y.com", "sk-abcdef0123456789") {
		t.Fatalf("scrubValue(*Exception) did not redact: %+v", sv)
	}
}

func containsAny(hay string, needles ...string) bool {
	for _, n := range needles {
		if len(n) > 0 && indexOf(hay, n) >= 0 {
			return true
		}
	}
	return false
}
func indexOf(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
