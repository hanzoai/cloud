package admin

// The ACCESS + WAITLIST cockpit (/v1/admin/waitlist*) — the SuperAdmin surface to SEE
// the waitlist (position / points / leaderboard) and control WHO gets access by
// granting points to move a user up toward the capacity cutoff. It is a server-authed
// passthrough to the Hanzo waitlist engine (the Base waitlist plugin — WAITLIST_URL +
// WAITLIST_AWARD_SECRET from KMS, the SAME engine + secret
// apps/auto/connector_waitlist.go bridges), so there is ONE waitlist system,
// not two: the cockpit reads its list and issues a grant AGAINST it.
//
// SECURITY. Both routes are SuperAdmin only (gated by core.Admit). A grant is a
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

	"github.com/hanzoai/cloud/apps/admin/core"
	"github.com/hanzoai/cloud/audit"
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
func waitlistProxy(ctx context.Context, method, target, secret string, body []byte) (json.RawMessage, int, error) {
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

// waitlist reads one waitlist's leaderboard from the Hanzo waitlist engine — position,
// points and referral standing per entry — proxied server-authed with the engine secret,
// never a client credential.
//
// The engine's payload is forwarded VERBATIM as data; the console normalizes it. When
// the engine is not configured on this deployment the read still succeeds, with an empty
// object and a msg saying so, so the panel shows an honest not-wired state instead of an
// error the operator would chase.
//
// Example: {"waitlist":"chat","page":"1","pageSize":"50"}
// Response: {"status":"ok","msg":"","data":{"entries":[{"email":"ada@acme.com","points":120,
// "position":7}],"total":842}}
func waitlist(ctx context.Context, in *waitlistIn) (*rawOut, error) {
	if _, err := core.Admit(ctx); err != nil {
		return nil, err
	}
	base, secret, configured := waitlistConfig()
	if !configured {
		return &rawOut{
			Status: core.OK,
			Msg:    "the waitlist engine is not configured on this deployment",
			Data:   map[string]any{},
			Total:  core.Total(0),
		}, nil
	}
	q := url.Values{}
	for k, v := range map[string]string{"waitlist": in.Waitlist, "page": in.Page, "pageSize": in.PageSize} {
		if v = strings.TrimSpace(v); v != "" {
			q.Set(k, v)
		}
	}
	target := base + "/v1/waitlist/list"
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	raw, code, err := waitlistProxy(ctx, http.MethodGet, target, secret, nil)
	if err != nil {
		return &rawOut{Status: core.Err, Msg: err.Error()}, nil
	}
	if code/100 != 2 {
		return &rawOut{Status: core.Err, Msg: fmt.Sprintf("waitlist engine returned http %d", code)}, nil
	}
	// data = the engine list payload verbatim (the console normalizes).
	return &rawOut{Status: core.OK, Data: raw}, nil
}

// waitlistIn is the GET /v1/admin/waitlist query. Each value is forwarded to the engine
// only when non-empty, so the engine applies its own defaults.
type waitlistIn struct {
	// Waitlist is the waitlist slug to read (e.g. "chat"). The engine decides what an
	// empty slug means.
	Waitlist string `json:"waitlist"`
	// Page is the 1-based page number.
	Page string `json:"page"`
	// PageSize is entries per page.
	PageSize string `json:"pageSize"`
}

// waitlistBoostRequest is the POST /v1/admin/waitlist/boost body.
type waitlistBoostRequest struct {
	// Waitlist is the waitlist slug the grant lands on. Required.
	Waitlist string `json:"waitlist"`
	// Email identifies the entry to boost. Either this or RefCode is required.
	Email string `json:"email"`
	// RefCode identifies the entry by its referral code, when the email is unknown.
	RefCode string `json:"refCode"`
	// Points is how many points to award. Must be positive — this client exists to move
	// someone UP toward the cutoff.
	Points int `json:"points"`
	// Reason is the operator's justification. Not sent to the engine; it is recorded on
	// the audit row, which is the point of asking for it.
	Reason string `json:"reason"`
}

// waitlistBoost grants a user waitlist points, moving them up toward the access cutoff.
// This is the access lever: the cutoff itself does not move, the person does.
//
// It funnels through the engine's verified grant client (POST /v1/waitlist/award with
// source="grant" — the ONE path that honours an explicit points amount) and writes a
// tamper-evident audit row either way, so a FAILED grant is recorded too. The reason
// field goes only to that row.
//
// Example: {"waitlist":"chat","email":"ada@acme.com","points":50,"reason":"design partner"}
// Response: {"status":"ok","msg":"","data":{"email":"ada@acme.com","points":170,"position":3}}
func (o ops) waitlistBoost(ctx context.Context, in *waitlistBoostRequest) (*rawOut, error) {
	c, err := core.Change(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	base, secret, configured := waitlistConfig()
	if !configured {
		return &rawOut{Status: core.Err, Msg: "the waitlist engine is not configured on this deployment"}, nil
	}
	body := *in
	body.Waitlist = strings.TrimSpace(body.Waitlist)
	body.Email = strings.TrimSpace(body.Email)
	body.RefCode = strings.TrimSpace(body.RefCode)
	if body.Waitlist == "" || (body.Email == "" && body.RefCode == "") {
		return &rawOut{Status: core.Err, Msg: "waitlist and (email or refCode) are required"}, nil
	}
	if body.Points <= 0 {
		return &rawOut{Status: core.Err, Msg: "points must be a positive number"}, nil
	}

	payload := map[string]any{"waitlist": body.Waitlist, "source": "grant", "points": body.Points}
	if body.Email != "" {
		payload["email"] = body.Email
	}
	if body.RefCode != "" {
		payload["refCode"] = body.RefCode
	}
	enc, _ := json.Marshal(payload)
	raw, code, err := waitlistProxy(ctx, http.MethodPost, base+"/v1/waitlist/award", secret, enc)

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
		return &rawOut{Status: core.Err, Msg: err.Error()}, nil
	}
	if code/100 != 2 {
		return &rawOut{Status: core.Err, Msg: fmt.Sprintf("waitlist grant failed (http %d): %s", code, strings.TrimSpace(string(raw)))}, nil
	}
	return &rawOut{Status: core.OK, Data: raw}, nil
}
