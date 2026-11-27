package wallet

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A custody upstream's refusal is relayed to the caller verbatim by
// custodyHTTPError's default arm (502 "custody: %v"), so whatever the Safe
// service or an MPC node writes into a 4xx body reaches a signed-in customer. A
// service that refuses a call routinely quotes the credential it refused.
//
// Both clients are therefore scrubbed at the PRODUCER — the one place the
// upstream body enters an error — so every consumer is covered, including
// custodyHTTPError and anything added after it. These tests drive the real client
// against a real server, because what is being checked is that this path reaches
// the scrubber.
const custodyKey = "sk-live-QwErTyUiOpAsDfGhJkLzXcVbNm0123456789ab"

func refusingUpstream(t *testing.T, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// assertScrubbed holds the property for one client's error: the message survives,
// the credential does not, and the redaction marker proves the scrub ran rather
// than the upstream simply having said nothing.
func assertScrubbed(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("the upstream refused, so an error must be returned; this test proves nothing without one")
	}
	got := err.Error()
	if !strings.Contains(got, "invalid credential") {
		t.Fatalf("the refusal reached the caller stripped of its own text (%q); the scrub must remove the credential, not the message", got)
	}
	if strings.Contains(got, custodyKey) {
		t.Fatalf("a custody upstream's credential is relayed to the caller: %q", got)
	}
	if !strings.Contains(got, "[REDACTED-TOKEN]") {
		t.Fatalf("the credential was neither relayed nor redacted, so the scrub did not run: %q", got)
	}
}

func TestSafeClientDoesNotRelayAnUpstreamCredential(t *testing.T) {
	srv := refusingUpstream(t, `{"detail":"invalid credential: `+custodyKey+`"}`)
	c := newSafeClient(srv.URL, []byte("test-secret"))
	assertScrubbed(t, c.do(context.Background(), http.MethodGet, "/v1/safes", "acme", nil, nil))
}

func TestMPCClientDoesNotRelayAnUpstreamCredential(t *testing.T) {
	srv := refusingUpstream(t, `{"detail":"invalid credential: `+custodyKey+`"}`)
	m := newMPCClient([]string{srv.URL}, []byte("test-key"))
	assertScrubbed(t, m.do(context.Background(), http.MethodGet, "/v1/keys", nil, nil))
}
