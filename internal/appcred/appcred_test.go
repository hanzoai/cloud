package appcred

import "testing"

func TestCanonicalWinsAndFallbackCompletes(t *testing.T) {
	t.Setenv("X_CLIENT_ID", "cid")
	t.Setenv("X_CLIENT_SECRET", "sec")
	t.Setenv("X_API_KEY", "old")
	t.Setenv("X_API_SECRET", "older")
	id, secret, missing := Resolve("x")
	if id != "cid" || secret != "sec" || len(missing) != 0 {
		t.Fatalf("canonical pair: id=%q secret=%q missing=%v", id, secret, missing)
	}

	t.Setenv("X_CLIENT_ID", "")
	t.Setenv("X_CLIENT_SECRET", "")
	id, secret, missing = Resolve("x")
	if id != "old" || secret != "older" || len(missing) != 0 {
		t.Fatalf("fallback pair: id=%q secret=%q missing=%v", id, secret, missing)
	}
}

func TestAHalfSetDoesNotMixPairs(t *testing.T) {
	t.Setenv("FACEBOOK_CLIENT_ID", "new-id")
	t.Setenv("FACEBOOK_CLIENT_SECRET", "")
	t.Setenv("FACEBOOK_APP_ID", "")
	t.Setenv("FACEBOOK_APP_SECRET", "old-secret")
	id, secret, missing := Resolve("facebook")
	if id != "" || secret != "" {
		t.Fatalf("a client id must not be paired with another app's secret, got %q %q", id, secret)
	}
	if len(missing) != 2 || missing[0] != "FACEBOOK_CLIENT_SECRET" || missing[1] != "FACEBOOK_APP_ID" {
		t.Fatalf("missing names: %v", missing)
	}
}

func TestAnUnknownNetworkHasNoLadder(t *testing.T) {
	id, secret, missing := Resolve("bluesky")
	if id != "" || secret != "" || missing != nil {
		t.Fatalf("bluesky is not on the ladder: %q %q %v", id, secret, missing)
	}
}
