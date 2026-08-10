package o11y

import (
	"net/http"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// mountSigninOutcome answers GET /v1/o11y/login — the address the module sends a
// browser to when a sign-in callback fails.
//
// The module builds that redirect as ExternalPath()+"/login" with no host
// (implsession/handler.go: getRedirectURLFromErr), so it can only ever be
// SAME-ORIGIN. Its assumption is that the console it serves lives beside its API.
// Here it does not: /v1/o11y/* is the API and the console is a separate host, so
// every failed o11y sign-in landed on a 404 — the reach gate found it, and the
// gate was right.
//
// This does NOT redirect onward to that console, deliberately. The redirect target
// would have to name a brand's hostname, and a hostname baked into a shared
// surface is how one brand's identity ends up in front of another's customer —
// the same defect that had every white-label pay page rendering a Hanzo login.
// The console knows where it is; this door does not, and answering as the API it
// belongs to needs no such claim.
//
// So it says what happened, in the caller's own terms, carrying the reason the
// module already put in the query. 401, because the sign-in did not complete —
// which is the fact, where 404 was a statement about routing that was never true.
func mountSigninOutcome(a cloud.Router) {
	a.Get("/v1/o11y/login", signinOutcome)
}

func signinOutcome(c *zip.Ctx) error {
	body := map[string]any{
		"status": http.StatusUnauthorized,
		"code":   or(c.Query("code"), "signin_incomplete"),
		"error":  or(c.Query("message"), "the observability sign-in did not complete"),
	}
	// Present only on the module's failure redirect; absent when someone opens this
	// address directly, and an empty key would read as "there were no errors".
	if e := c.Query("errors"); e != "" {
		body["errors"] = e
	}
	return c.JSON(http.StatusUnauthorized, body)
}
