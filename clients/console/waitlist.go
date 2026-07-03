// waitlist.go ports console2's app/waitlist/route.ts — the "Join waitlist" CTA on
// coming-soon products — into the unified binary at POST /v1/console/waitlist
// (task #41). It is NOT a backend that owns the list: it is the session-gated,
// email-binding seam in front of the Hanzo Base waitlist plugin (the hanzoai/
// waitlist pattern, a per-product SQLite-backed list at POST /v1/waitlist/join).
//
// WHY IT IS A HANDLER (not a vanishing proxy). It does real work the SPA must not:
// it BINDS the recorded email to the VALIDATED session (the gateway-verified
// X-User-Email) so a signed-in user cannot enroll a third party (victim@othercorp)
// or forge "org X wants ERP" — the client-supplied email is only a fallback for an
// account whose token carries no email. The backend URL is server-only env
// (WAITLIST_URL, sourced from KMS by the deployment, never NEXT_PUBLIC); when it is
// unset the route is honestly 501 ("not open on this deployment yet"), never a
// fabricated confirmation.
package console

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// waitlistMaxBody bounds the backend response read — a small JSON verdict
// ({ok,rank,total,…}), never a blob.
const waitlistMaxBody = 1 << 20

// emailRe is a minimal sanity check; the waitlist backend is authoritative
// (disposable-domain rejection, etc.). Mirrors route.ts looksLikeEmail.
var emailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

func looksLikeEmail(s string) bool { return emailRe.MatchString(s) }

// waitlistJoin forwards a validated user's waitlist join to the Base waitlist
// plugin, binding the email to the session. Mirrors POST app/waitlist/route.ts.
func (s *svc) waitlistJoin(c *zip.Ctx) error {
	// Require a validated principal (a signed-in user) — this is not an open relay
	// to the waitlist backend. An org is NOT required (a zero-org user may join).
	if !principal.Validated(c) || strings.TrimSpace(c.User()) == "" {
		return zip.ErrForbidden("sign in to join the waitlist")
	}

	base := strings.TrimRight(strings.TrimSpace(os.Getenv("WAITLIST_URL")), "/")
	if base == "" {
		return zip.Errorf(http.StatusNotImplemented, "the waitlist is not open on this deployment yet")
	}

	var body struct {
		Waitlist string `json:"waitlist"`
		Email    string `json:"email"`
	}
	if len(c.Body()) > 0 {
		if err := c.Bind(&body); err != nil {
			return zip.ErrBadRequest("invalid request body")
		}
	}
	waitlist := strings.TrimSpace(body.Waitlist)
	// BIND the email to the SESSION: prefer the gateway-verified account email
	// (X-User-Email, minted from the JWT — never a client claim) so a signed-in
	// user can't enroll someone else. The client value is a fallback ONLY when the
	// token carries no email. (RED review, mirrors route.ts.)
	email := strings.TrimSpace(c.UserEmail())
	if email == "" {
		email = strings.TrimSpace(body.Email)
	}
	if waitlist == "" {
		return zip.ErrBadRequest("missing waitlist")
	}
	if !looksLikeEmail(email) {
		return zip.ErrBadRequest("enter a valid email")
	}

	// Do NOT forward a client X-Forwarded-For (forgeable — it would defeat the
	// backend's per-IP limit); per-user abuse is already bounded by the session gate
	// above. The backend sees this process's connection IP. (RED review.)
	payload, _ := json.Marshal(map[string]string{"waitlist": waitlist, "email": email})
	ctx, cancel := context.WithTimeout(c.Context(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/waitlist/join", bytes.NewReader(payload))
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "could not reach the waitlist: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "could not reach the waitlist: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, waitlistMaxBody))
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "could not read the waitlist response: %v", err)
	}
	// Pass the backend's verdict through verbatim (status + JSON body): the module
	// renders rank/total/alreadyJoined on success and the backend's message on error.
	c.SetHeader("Content-Type", "application/json")
	return c.Bytes(resp.StatusCode, raw)
}

// httpClient is the shared outbound client for the console subsystem's plain-HTTP
// seams (waitlist backend, EVM JSON-RPC, commerce billing). Small JSON envelopes,
// bounded reads; 15s is generous for an in-cluster / same-region hop.
var httpClient = &http.Client{Timeout: 15 * time.Second}
