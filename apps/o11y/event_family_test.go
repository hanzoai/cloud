package o11y

import (
	"net/http"
	"testing"
)

// TestEventFamilyCarriesIngestOnly pins the Sentry wire on the ONE event door:
// POST /v1/event/<project>/envelope|store maps onto its clean /v1/sentry
// runtime ingest route — never an /api/ segment, never a READ API, and never
// the door's own root.
func TestEventFamilyCarriesIngestOnly(t *testing.T) {
	cases := []struct {
		method, path string
		want         string
		ok           bool
	}{
		{http.MethodPost, "/v1/event/6ba7b810-9dad-11d1-80b4-00c04fd430c8/envelope/", "/v1/sentry/6ba7b810-9dad-11d1-80b4-00c04fd430c8/envelope/", true},
		{http.MethodPost, "/v1/event/42/store", "/v1/sentry/42/store", true},
		// Ingest is POST; reads and probes do not pass.
		{http.MethodGet, "/v1/event/42/envelope/", "", false},
		{http.MethodPost, "/v1/event/42/query_range", "", false},
		{http.MethodPost, "/v1/event/issues", "", false},
		{http.MethodPost, "/v1/event", "", false},
	}
	for _, c := range cases {
		got, ok := eventToRuntimePath(c.method, c.path)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("eventToRuntimePath(%s %s) = (%q,%v), want (%q,%v)", c.method, c.path, got, ok, c.want, c.ok)
		}
	}
}
