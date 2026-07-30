package o11y

import (
	"net/http"
	"testing"
)

// TestEventFamilyCarriesIngestOnly pins the /v1/event/api wire: the
// SDK-expanded Sentry spelling maps onto its runtime ingest route, and nothing
// else — no READ API is reachable through the subtree, and the sibling
// analytics routes (/v1/event, /collect) are never claimed.
func TestEventFamilyCarriesIngestOnly(t *testing.T) {
	cases := []struct {
		method, path string
		want         string
		ok           bool
	}{
		// Real Sentry SDKs expand a DSN of …/v1/event/<project> with an "api" segment.
		{http.MethodPost, "/v1/event/api/42/envelope/", "/v1/o11y/api/42/envelope/", true},
		{http.MethodPost, "/v1/event/api/42/store/", "/v1/o11y/api/42/store/", true},
		// Ingest is POST; reads and probes do not pass.
		{http.MethodGet, "/v1/event/api/42/envelope/", "", false},
		{http.MethodPost, "/v1/event/api/v1/query_range", "", false},
		{http.MethodPost, "/v1/event/issues", "", false},
		{http.MethodPost, "/v1/event/collect", "", false},
		{http.MethodPost, "/v1/event", "", false},
	}
	for _, c := range cases {
		got, ok := eventToRuntimePath(c.method, c.path)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("eventToRuntimePath(%s %s) = (%q,%v), want (%q,%v)", c.method, c.path, got, ok, c.want, c.ok)
		}
	}
}
