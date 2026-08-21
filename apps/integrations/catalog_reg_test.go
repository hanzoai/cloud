package integrations

import "testing"

// The catalog is DATA, so what needs proving is that every entry is well-formed
// and that it lands in the SAME registry the hand-rolled providers use — a
// second registry would be a second way to declare a connector.
//
// `register` panics on a duplicate id, so a collision with one of the existing
// hand-written providers would take the process down at init. That is the right
// failure (a duplicate slug is a programming error), and this test proves the
// catalog does not cause it.
func TestCatalogProvidersAreRegistered(t *testing.T) {
	want := []string{
		// social
		"x", "linkedin", "facebook", "instagram", "threads", "pinterest", "reddit", "tiktok",
		// video
		"youtube", "twitch",
		// productivity
		"microsoft",
	}
	for _, id := range want {
		p := registry[id]
		if p == nil {
			t.Errorf("connector %q is not registered", id)
			continue
		}
		if want := callbackPath(id); p.RedirectPath != want {
			t.Errorf("%s RedirectPath = %q, want %q", id, p.RedirectPath, want)
		}
		if len(p.Secrets) == 0 {
			t.Errorf("%s names no secret — disconnect would leave a live token sealed", id)
		}
		if p.Configured == nil || p.Authorize == nil || p.Exchange == nil {
			t.Errorf("%s is missing part of the oauth flow", id)
		}
		// Unconfigured on a test host: no env is set, so the card must report
		// itself unavailable rather than offering a connect that dead-ends.
		if p.Configured() {
			t.Errorf("%s reports configured with no env set", id)
		}
	}
}

// The engine builds the authorize URL for every declaration, so proving it once
// proves it for all of them — including that the client SECRET never reaches the
// browser, which a consent redirect would otherwise disclose.
func TestCatalogAuthorizeCarriesNoSecret(t *testing.T) {
	p := registry["linkedin"]
	if p == nil {
		t.Skip("linkedin not registered")
	}
	raw, err := p.Authorize(OAuthConfig{ClientID: "cid", ClientSecret: "sekrit"},
		"https://api.hanzo.ai/v1/integrations/linkedin/callback", "STATE-1")
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	if got := raw; len(got) == 0 {
		t.Fatal("empty authorize url")
	}
	for _, bad := range []string{"sekrit", "client_secret"} {
		if contains(raw, bad) {
			t.Fatalf("authorize URL leaks %q: %s", bad, raw)
		}
	}
}

func contains(s, sub string) bool {
	return len(sub) > 0 && len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
