package o11y

import (
	"net/http"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// GET /v1/o11y/sessions — the flat, org-gated public path for the LLM-obs sessions
// list (traces grouped by session.id on the gen_ai span plane). The console's
// SessionsModule reads this; session DETAIL is composed client-side from this list
// + the traces list filtered by session, so there is no /sessions/:id backing route
// (the embedded runtime serves only the list) and none is registered here.
//
// Why an explicit cloud route rather than only the order-70 wildcard: this pins the
// public flat path to the runtime's internal /api/sessions route SERVER-SIDE (the
// same discipline query.go uses for the composite query) AND enforces the tenant
// gate at the cloud boundary — an org-less caller gets a clean 403 here before the
// request reaches the runtime, and the org the runtime binds (gen_ai.hanzo.org_id
// from X-Org-Id) is the SAME validated tenant this handler refuses to proceed
// without. Registered by mountScope (order 69), so it precedes the wildcard.
//
// The list query (?limit=&offset=) rides through unchanged; the runtime returns the
// llmobstypes.GettableSessions {items,offset,limit} under the {status,data} envelope
// the console's O11yApi.sessions already unwraps.
func sessionsHandler(c *zip.Ctx) error {
	if _, ok := principal.Org(c); !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	h := runtimeHandler
	if h == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "o11y runtime not initialized")
	}
	return zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Resolve the flat public path to the runtime's version-less /api/sessions
		// route INTERNALLY (the embedded runtime's StripPrefix leaves an /api-rooted
		// path untouched, landing it on the llmobs Sessions handler).
		r.URL.Path = "/api/sessions"
		r.URL.RawPath = ""
		h.ServeHTTP(w, r)
	}))(c)
}
