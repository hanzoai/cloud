package admin

// The ACCESS + WAITLIST cockpit (/v1/admin/waitlist*) — the SuperAdmin surface to SEE
// the waitlist (position / points / leaderboard) and control WHO gets access by
// granting points to move a user up toward the capacity cutoff. It is a server-authed
// passthrough to the Hanzo waitlist engine (the Base waitlist plugin — WAITLIST_URL +
// WAITLIST_AWARD_SECRET from KMS, the SAME engine + secret
// clients/automations/connector_waitlist.go bridges), so there is ONE waitlist system,
// not two: the cockpit reads its list and issues a grant AGAINST it.
//
// SECURITY. Both routes are SuperAdmin only (mounted behind s.guard). A grant is a
// privileged mutation, so it is written to cloud's tamper-evident audit trail
// (action "admin.waitlist.grant", resource "waitlist"). The engine secret is never a
// client claim — it is injected from KMS into the process env by the deployment.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/audit"
	"github.com/hanzoai/cloud/clients/admin/core"
	"github.com/zap-proto/zip"
)

const (
	waitlistURLEnv    = "WAITLIST_URL"
	waitlistSecretEnv = "WAITLIST_AWARD_SECRET"
)

var waitlistHTTP = &http.Client{Timeout: 15 * time.Second}

func waitlistConfig() (base, secret string, ok bool) {
	base = strings.TrimRight(strings.TrimSpace(os.Getenv(waitlistURLEnv)), "/")
	secret = strings.TrimSpace(os.Getenv(waitlistSecretEnv))
	return base, secret, base != "" && secret != ""
}

// waitlistProxy issues a server-authed request to the waitlist engine and returns its
// raw JSON body + status. Bounded read; Bearer secret; never forwards a client header.
func waitlistProxy(s *cloud.Service[core.State], ctx context.Context, method, target, secret string, body []byte) (json.RawMessage, int, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rdr)
	if err != nil {
		return nil, 0, fmt.Errorf("waitlist request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	resp, err := waitlistHTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("could not reach the waitlist engine: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("waitlist read: %w", err)
	}
	return json.RawMessage(raw), resp.StatusCode, nil
}

// waitlist answers GET /v1/admin/waitlist — the leaderboard/list for one waitlist,
// proxied from the engine (GET /v1/waitlist/list?waitlist=&page=&pageSize=). Honest
// "not configured" empty when the engine is absent on this deployment (never fabricated).
func waitlist(s *cloud.Service[core.State], c *zip.Ctx) error {
	base, secret, configured := waitlistConfig()
	if !configured {
		return c.JSON(200, map[string]any{"status": "ok", "msg": "the waitlist engine is not configured on this deployment", "data": map[string]any{}, "data2": 0})
	}
	q := url.Values{}
	for _, k := range []string{"waitlist", "page", "pageSize"} {
		if v := strings.TrimSpace(c.Query(k)); v != "" {
			q.Set(k, v)
		}
	}
	target := base + "/v1/waitlist/list"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	raw, code, err := waitlistProxy(s, c.Context(), http.MethodGet, target, secret, nil)
	if err != nil {
		return core.Fail(c, err.Error())
	}
	if code/100 != 2 {
		return core.Fail(c, fmt.Sprintf("waitlist engine returned http %d", code))
	}
	return core.OK(c, raw) // data = the engine list payload verbatim (the console normalizes)
}

// waitlistBoostRequest is the POST /v1/admin/waitlist/boost body.
type waitlistBoostRequest struct {
	Waitlist string `json:"waitlist"`
	Email    string `json:"email"`
	RefCode  string `json:"refCode"`
	Points   int    `json:"points"`
	Reason   string `json:"reason"`
}

// waitlistBoost answers POST /v1/admin/waitlist/boost — grant a user waitlist points to
// move them up toward the access cutoff. It funnels through the engine's verified grant
// seam (POST /v1/waitlist/award, source="grant" — the one path that honors an explicit
// points amount) and audits the grant to cloud's tamper-evident trail.
func waitlistBoost(s *cloud.Service[core.State], c *zip.Ctx) error {
	base, secret, configured := waitlistConfig()
	if !configured {
		return core.Fail(c, "the waitlist engine is not configured on this deployment")
	}
	var body waitlistBoostRequest
	if err := c.Bind(&body); err != nil {
		return core.Fail(c, "invalid request body")
	}
	body.Waitlist = strings.TrimSpace(body.Waitlist)
	body.Email = strings.TrimSpace(body.Email)
	body.RefCode = strings.TrimSpace(body.RefCode)
	if body.Waitlist == "" || (body.Email == "" && body.RefCode == "") {
		return core.Fail(c, "waitlist and (email or refCode) are required")
	}
	if body.Points <= 0 {
		return core.Fail(c, "points must be a positive number")
	}

	payload := map[string]any{"waitlist": body.Waitlist, "source": "grant", "points": body.Points}
	if body.Email != "" {
		payload["email"] = body.Email
	}
	if body.RefCode != "" {
		payload["refCode"] = body.RefCode
	}
	enc, _ := json.Marshal(payload)
	raw, code, err := waitlistProxy(s, c.Context(), http.MethodPost, base+"/v1/waitlist/award", secret, enc)

	target := body.Email
	if target == "" {
		target = body.RefCode
	}
	result := "success"
	if err != nil || code/100 != 2 {
		result = "error"
	}
	core.EmitAudit(s, c, "admin.waitlist.grant", "waitlist", body.Waitlist,
		map[string]any{"target": target},
		map[string]any{"points": body.Points, "reason": body.Reason, "source": "grant"},
		audit.Outcome{Result: result, Status: code})

	if err != nil {
		return core.Fail(c, err.Error())
	}
	if code/100 != 2 {
		return core.Fail(c, fmt.Sprintf("waitlist grant failed (http %d): %s", code, strings.TrimSpace(string(raw))))
	}
	return core.OK(c, raw)
}
