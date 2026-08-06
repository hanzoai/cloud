package o11y

import (
	"net/http"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// A reverse proxy has no Go type for "whatever the runtime answered", which is why
// this stays untyped (typed_wire_test.go's untypedByDesign) and why zipdoc has no
// doc comment to lift. The prose is declared beside the wire fact.
func init() {
	openapi.Describe("/v1/o11y/sessions", http.MethodGet,
		"List the caller org's LLM sessions",
		"Answers the caller org's LLM-observability sessions — traces grouped by session id "+
			"on the gen_ai span plane — paged by limit and offset, in the runtime's own "+
			"envelope, passed through unchanged.\n\n"+
			"An org-less caller is refused HERE, at the cloud boundary, before the request "+
			"reaches the runtime, and the org the runtime then scopes on is that SAME validated "+
			"tenant. The two cannot disagree: the tenant is minted from the principal's own "+
			"claim at ingress and a client copy never survives it.\n\n"+
			"There is deliberately no session-detail route to pair with this. The runtime serves "+
			"the list only; detail is composed client-side from this list plus the traces "+
			"filtered by session, so a caller looking for one is looking for something that was "+
			"never served rather than something that broke.")
}

// GET /v1/o11y/sessions — the flat, org-gated public path for the LLM-obs sessions
// list (traces grouped by session.id on the gen_ai span plane). The console's
// SessionsModule reads this; session DETAIL is composed client-side from this list
// + the traces list filtered by session, so there is no /sessions/:id backing route
// (the embedded runtime serves only the list) and none is registered here.
//
// Why an explicit cloud route rather than only the order-70 wildcard: this pins the
// public flat path to the runtime's internal /api/sessions route SERVER-SIDE AND
// enforces the tenant gate at the cloud boundary — an org-less caller gets a clean 403 here before the
// request reaches the runtime, and the org the runtime binds (gen_ai.hanzo.org_id
// from X-Org-Id) is the SAME validated tenant this handler refuses to proceed
// without. Registered by mountScope (order 69), so it precedes the wildcard.
//
// This used to cite query.go's composite-query pin as the precedent for the move.
// That file is GONE — it pinned POST /v1/o11y/query_range to the v3 engine, and when
// it went the module's v5 querier took the address, whose composite accepts only
// {queries:[…]}. The console still sent the v3 {queryType,panelType,builderQueries}
// envelope and every Logs page 400'd on "unknown field \"queryType\" in composite
// query". Citing a deleted pin as the discipline to follow is how the next route
// inherits the same break, so the reference is removed rather than reworded: this
// route stands on its OWN pin, three lines below, which is still here.
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
