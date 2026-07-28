package analytics

import "testing"

// A stack/message carrying an email, a bearer token, an sk- key, and a
// ?access_token= query secret must be redacted at rest AND in the folded event
// that the destinations fan-out consumes (forward.go sees pre-warehouse-scrub).
func TestFoldException_RedactsSecretsAndPII(t *testing.T) {
	e := CaptureEvent{
		Type: "error",
		Error: &Exception{
			Type:    "Error",
			Message: "login failed for alice@example.com with sk-live-abcdef0123456789",
			Stack:   "at fetch (https://api.x.com/v1?access_token=tok_abc123def456 )\n  Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.payload.sig",
		},
	}
	got := foldException(e)
	ex, ok := got.Properties["$exception"].(*Exception)
	if !ok || ex == nil {
		t.Fatalf("$exception not an *Exception: %T", got.Properties["$exception"])
	}
	for _, s := range []string{ex.Message, ex.Stack} {
		if containsAny(s, "alice@example.com", "sk-live-abcdef0123456789", "tok_abc123def456", "eyJhbGciOiJIUzI1NiJ9") {
			t.Fatalf("secret/PII survived scrub: %q", s)
		}
	}
	// original struct must NOT be mutated (copy semantics)
	if e.Error.Message == ex.Message {
		t.Fatal("foldException mutated the caller's Exception")
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
