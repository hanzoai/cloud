package o11y

import (
	"net/http"
	"testing"

	module "github.com/hanzoai/o11y"
)

// TestEventFamilyCarriesIngestOnly pins what the ONE event door admits.
//
// There is nothing to translate: the runtime opens its ingest door at the same
// /v1/event a caller knocks on, so the relay passes the path through and this is
// the gate that decides whether it may. The cases below are the wire's shape —
// POST, an envelope or store suffix — and everything else on the same root is
// refused, including /v1/event itself, which is the PRODUCT event door beside
// this one and is not reachable through the error plane's exemption.
func TestEventFamilyCarriesIngestOnly(t *testing.T) {
	cases := []struct {
		method, path string
		admit        bool
	}{
		{http.MethodPost, "/v1/event/6ba7b810-9dad-11d1-80b4-00c04fd430c8/envelope/", true},
		{http.MethodPost, "/v1/event/42/store", true},
		// Ingest is POST; reads and probes do not pass.
		{http.MethodGet, "/v1/event/42/envelope/", false},
		{http.MethodPost, "/v1/event/42/query_range", false},
		{http.MethodPost, "/v1/event/issues", false},
		{http.MethodPost, "/v1/event", false},
		// The FACE is not a door. Its reads carry a session, not a DSN key, so
		// no spelling of them is admitted here.
		{http.MethodPost, "/v1/o11y/sentinel/42/envelope/", false},
		{http.MethodPost, "/v1/o11y/sentinel/discover", false},
		{http.MethodGet, "/v1/o11y/sentinel/issues", false},
	}
	for _, c := range cases {
		if got := module.IngestWire(c.method, c.path); got != c.admit {
			t.Errorf("IngestWire(%s %s) = %v, want %v", c.method, c.path, got, c.admit)
		}
	}
}
