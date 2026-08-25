package o11y

import (
	"net/http"
	"net/http/httptest"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

// The relay answers with whatever the runtime wrote, which is why this stays
// untyped (typed_wire_test.go's untypedByDesign) and why zipdoc has no doc
// comment to lift. The prose is declared beside the wire fact.
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

// sessionsRoute is where the runtime answers the LLM-obs sessions list.
//
// THE /llm/ SEGMENT IS THE MEANING. On this runtime the bare word `sessions` is
// the SIGN-IN family — the module registers DELETE /v1/o11y/sessions to end one —
// and the conversations projected off gen_ai spans live under /v1/o11y/llm/,
// alongside the traces, observations and users read the same way. One word, two
// nouns, told apart by the segment.
const sessionsRoute = "/v1/o11y/llm/sessions"

// GET /v1/o11y/sessions — the flat, org-pinned public path for the LLM-obs
// sessions list (traces grouped by session.id on the gen_ai span plane). The
// console's SessionsModule reads this; session DETAIL is composed client-side
// from this list plus the traces filtered by session, so there is no
// /sessions/:id backing route (the runtime serves the list only) and none is
// registered here.
//
// Two things happen and nothing else. An org-less caller is refused at the cloud
// boundary, before the request reaches the runtime — and the org the runtime then
// binds (gen_ai.hanzo.org_id from X-Org-Id) is that SAME validated tenant. Then
// the call is handed to the runtime handler at /v1/o11y/llm/sessions and its
// status, headers and bytes come back untouched. A flat alias has to be
// indistinguishable from the address it aliases, which is what makes verbatim the
// only correct answer here — and why there is no Go shape to declare.
//
// It delegates in process rather than adapting an http.Handler. The runtime
// registers every route at its full public path, so the address above is the one
// it serves and no rewrite stands between them. The request is built
// server-shaped, which is what RequestURI needs: a CLIENT request leaves that
// field empty by contract and the in-process backing forwards it verbatim.
func sessionsHandler(c *zip.Ctx) error {
	if _, ok := principal.Org(c); !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	h := runtimeHandler
	if h == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "o11y runtime not initialized")
	}

	target := sessionsRoute
	if q := c.Fiber().Request().URI().QueryString(); len(q) > 0 {
		target += "?" + string(q) // ?limit=&offset= ride through unchanged
	}
	req, err := http.NewRequestWithContext(c.Context(), http.MethodGet, target, http.NoBody)
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	// A handler is SERVED, not dialled, so it is entitled to read the field a
	// server would have populated. http.NewRequest deliberately leaves RequestURI
	// empty — it builds a client request, and net/http fills the request-target
	// from URL at write time — but nothing writes this to a socket.
	req.RequestURI = target
	req.Host = c.Host()
	req.RemoteAddr = c.Fiber().RequestCtx().RemoteAddr().String()
	c.Fiber().Request().Header.VisitAll(func(name, value []byte) {
		req.Header.Add(string(name), string(value))
	})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Every header, because every one is the runtime's answer. There is no
	// sniffing to reproduce here: both backings end in fasthttp, which writes its
	// own Content-Type when a handler sets none, so the recorder never sees the
	// absent header net/http would have named.
	res := c.Fiber().Response()
	for name, values := range rec.Header() {
		if name == "Content-Length" {
			continue // fasthttp writes it from the body it is about to send
		}
		for _, v := range values {
			res.Header.Add(name, v)
		}
	}
	return c.Bytes(rec.Code, rec.Body.Bytes())
}
