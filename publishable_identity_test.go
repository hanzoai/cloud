package cloud

import "testing"

// A publishable key must never authenticate. It ships in browser bundles, so if
// it minted a principal every visitor would hold a reading credential for the
// org that owns it.
func TestPublishableKeyIsNotAPrincipal(t *testing.T) {
	if !IsPublishableKey("pk-abc123") {
		t.Fatal("pk- must be recognised as publishable")
	}
	for _, tok := range []string{"sk-abc123", "hk-abc123", "eyJhbGciOi.x.y", ""} {
		if IsPublishableKey(tok) {
			t.Fatalf("%q must NOT be publishable", tok)
		}
	}
	// It stays an API key so OrgForKey can still attribute an ingest beacon.
	if !isAPIKey("pk-abc123") {
		t.Fatal("pk- must remain in APIKeyPrefixes so OrgForKey resolves it")
	}
}
