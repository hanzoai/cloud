package event

import "testing"

// TestOneWireThreeSpellingsOneMeaning is the claim /v1/event's own endpoint makes and
// the one an SDK generated from its document relies on: the endpoint publishes
// three shapes, and whatever the batch can express the bare object and the bare
// array express too.
//
// It is asserted per FIELD rather than on the whole struct, because the failure it
// guards is silent: an unknown key decodes into nothing at all, so a caller sends
// a session id and a url, reads a 200 receipt saying one event was accepted, and
// finds neither in the warehouse. A test that only counted events would pass.
func TestOneWireThreeSpellingsOneMeaning(t *testing.T) {
	const bare = `{"event":"click","distinctId":"u1","sessionId":"s1","url":"/x",
	               "path":"/x","referrer":"https://r","product":"app","anonymousId":"a1",
	               "timestamp":"2026-01-02T03:04:05Z"}`

	for _, tc := range []struct{ name, body string }{
		{"bare object", bare},
		{"bare array", "[" + bare + "]"},
		{"batch", `{"batch":[` + bare + `]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evs, err := decodeEvent([]byte(tc.body))
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if len(evs) != 1 {
				t.Fatalf("decoded %d events, want 1", len(evs))
			}
			got := evs[0]
			for _, f := range []struct{ name, got, want string }{
				{"event", got.Event, "click"},
				{"distinctId", got.DistinctID, "u1"},
				{"sessionId", got.SessionID, "s1"},
				{"url", got.URL, "/x"},
				{"path", got.Path, "/x"},
				{"referrer", got.Referrer, "https://r"},
				{"product", got.Product, "app"},
				{"anonymousId", got.AnonymousID, "a1"},
				{"timestamp", got.Timestamp, "2026-01-02T03:04:05Z"},
			} {
				if f.got != f.want {
					t.Errorf("%s = %q, want %q — this spelling drops it while another keeps it",
						f.name, f.got, f.want)
				}
			}
		})
	}
}
