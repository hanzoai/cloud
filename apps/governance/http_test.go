// SPDX-License-Identifier: Apache-2.0 OR MIT

package governance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

func mountTestHTTP(t *testing.T, eng *Engine, exporterID string, exporterKey []byte) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	deps := Deps{
		Log: luxlog.New("test"),
		Org: func(ctx context.Context) (string, bool) {
			c := zip.CallerOf(ctx)
			if c.User == "" || c.Org == "" {
				return "", false
			}
			return c.Org, true
		},
		Acting: func(ctx context.Context) (string, error) {
			c := zip.CallerOf(ctx)
			if c.User == "" {
				return "", zip.ErrUnauthorized("a validated principal is required")
			}
			if c.Org == "" {
				return "", zip.ErrForbidden("X-Org-Id required")
			}
			return c.Org, nil
		},
		Engine:      eng,
		ExporterID:  exporterID,
		ExporterKey: exporterKey,
	}
	if err := Mount(app, deps); err != nil {
		t.Fatalf("Mount: %v", err)
	}
	return app
}

func doHTTP(t *testing.T, app *zip.App, method, path, user, org string, body any) (int, []byte) {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	req := httptest.NewRequest(method, path, r)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if user != "" {
		req.Header.Set("X-User-Id", user)
	}
	if org != "" {
		req.Header.Set("X-Org-Id", org)
	}
	resp, err := app.Test(req)
	if err != nil {
		t.Fatalf("app.Test %s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, out
}

func TestHTTPAuthenticatedCallerBindingAndFullLifecycle(t *testing.T) {
	eng, clk, actors := setupDAO(t)
	app := mountTestHTTP(t, eng, "alice", actors["alice"].priv)

	// 1. Health endpoint reports no_admin_override=true.
	code, body := doHTTP(t, app, http.MethodGet, "/v1/governance/health", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("health status = %d (%s)", code, body)
	}
	var health HealthOut
	if err := json.Unmarshal(body, &health); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	if !health.NoAdminOverride || health.ProtocolID != "hanzo-open-gov-1" {
		t.Fatalf("unexpected health: %+v", health)
	}

	// Prepare Alice's validly signed proposal.
	now := clk.Now()
	pReq := ProposalRequest{
		ProposalID:  "http-prop-1",
		ProposerID:  "alice",
		Org:         actors["alice"].member.Org,
		Kind:        ProposalParameterUpdate,
		Title:       "HTTP fee update",
		Description: "Lower fee to 150 bps via HTTP surface",
		Payload: ProposalPayload{
			ParameterUpdate: &ParameterUpdatePayload{Key: "max_inference_fee_bps", Value: "150"},
		},
		CreatedAt: now,
		Nonce:     "http-alice-p1",
	}
	pSig, err := SignProposal(eng.ProtocolID(), pReq, actors["alice"].priv)
	if err != nil {
		t.Fatalf("SignProposal: %v", err)
	}

	// 2. Unauthenticated submission MUST fail with 401.
	payloadBody := map[string]any{
		"proposal_id": pReq.ProposalID,
		"proposer_id": "alice", // untrusted JSON field must be ignored!
		"org":         actors["alice"].member.Org,
		"kind":        pReq.Kind,
		"title":       pReq.Title,
		"description": pReq.Description,
		"payload":     pReq.Payload,
		"created_at":  pReq.CreatedAt,
		"nonce":       pReq.Nonce,
		"signature":   pSig,
	}
	if code, _ := doHTTP(t, app, http.MethodPost, "/v1/governance/proposals", "", "", payloadBody); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated submit status = %d, want 401", code)
	}

	// 3. Authenticated Bob attempting to submit Alice's signed proposal (spoofing caller ID in JSON)
	// MUST fail with 403 because server binds ProposerID strictly from authenticated X-User-Id ("bob").
	if code, body := doHTTP(t, app, http.MethodPost, "/v1/governance/proposals", "bob", actors["bob"].member.Org, payloadBody); code != http.StatusForbidden {
		t.Fatalf("spoofed caller submit status = %d (%s), want 403", code, body)
	}

	// 4. Query-string hijack (?proposal_id=hijacked) MUST be ignored due to url:"-".
	code, body = doHTTP(t, app, http.MethodPost, "/v1/governance/proposals?proposal_id=hijacked", "alice", actors["alice"].member.Org, payloadBody)
	if code != http.StatusCreated {
		t.Fatalf("alice submit status = %d (%s), want 201", code, body)
	}
	var created Proposal
	if err := json.Unmarshal(body, &created); err != nil {
		t.Fatalf("decode proposal: %v", err)
	}
	if created.ID != "http-prop-1" {
		t.Fatalf("query string hijacked proposal_id: got %q, want http-prop-1", created.ID)
	}

	// 5. Advance into voting window; Alice and Bob vote over HTTP.
	clk.Advance(15 * time.Minute)
	for _, voter := range []testActor{actors["alice"], actors["bob"]} {
		vReq := VoteRequest{
			ProposalID: "http-prop-1",
			VoterID:    voter.member.ID,
			Org:        voter.member.Org,
			Support:    VoteFor,
			CastAt:     clk.Now(),
			Nonce:      "http-v-" + voter.member.ID,
		}
		vSig, err := SignVote(eng.ProtocolID(), vReq, voter.priv)
		if err != nil {
			t.Fatalf("SignVote: %v", err)
		}
		vBody := map[string]any{
			"support":   vReq.Support,
			"cast_at":   vReq.CastAt,
			"nonce":     vReq.Nonce,
			"signature": vSig,
		}
		if code, body := doHTTP(t, app, http.MethodPost, "/v1/governance/proposals/http-prop-1/votes", voter.member.ID, voter.member.Org, vBody); code != http.StatusCreated {
			t.Fatalf("vote %s status = %d (%s), want 201", voter.member.ID, code, body)
		}
	}

	// 6. Close voting, queue, reject premature execute (409), then execute after timelock (200).
	clk.Advance(2*time.Hour + time.Second)
	qReq := QueueRequest{
		ProposalID: "http-prop-1",
		CallerID:   "carol",
		Org:        actors["carol"].member.Org,
		QueuedAt:   clk.Now(),
		Nonce:      "http-q-1",
	}
	qSig, _ := SignQueue(eng.ProtocolID(), qReq, actors["carol"].priv)
	if code, body := doHTTP(t, app, http.MethodPost, "/v1/governance/proposals/http-prop-1/queue", "carol", actors["carol"].member.Org, map[string]any{
		"queued_at": qReq.QueuedAt,
		"nonce":     qReq.Nonce,
		"signature": qSig,
	}); code != http.StatusOK {
		t.Fatalf("queue status = %d (%s), want 200", code, body)
	}

	// Premature execute -> 409 Conflict.
	eEarly := ExecuteRequest{
		ProposalID: "http-prop-1",
		ExecutorID: "carol",
		Org:        actors["carol"].member.Org,
		ExecutedAt: clk.Now(),
		Nonce:      "http-e-early",
	}
	eEarlySig, _ := SignExecute(eng.ProtocolID(), eEarly, actors["carol"].priv)
	if code, body := doHTTP(t, app, http.MethodPost, "/v1/governance/proposals/http-prop-1/execute", "carol", actors["carol"].member.Org, map[string]any{
		"executed_at": eEarly.ExecutedAt,
		"nonce":       eEarly.Nonce,
		"signature":   eEarlySig,
	}); code != http.StatusConflict {
		t.Fatalf("premature execute status = %d (%s), want 409", code, body)
	}

	// Advance past timelock and execute -> 200 OK.
	clk.Advance(time.Hour + time.Second)
	eReq := ExecuteRequest{
		ProposalID: "http-prop-1",
		ExecutorID: "carol",
		Org:        actors["carol"].member.Org,
		ExecutedAt: clk.Now(),
		Nonce:      "http-e-1",
	}
	eSig, _ := SignExecute(eng.ProtocolID(), eReq, actors["carol"].priv)
	if code, body := doHTTP(t, app, http.MethodPost, "/v1/governance/proposals/http-prop-1/execute", "carol", actors["carol"].member.Org, map[string]any{
		"executed_at": eReq.ExecutedAt,
		"nonce":       eReq.Nonce,
		"signature":   eSig,
	}); code != http.StatusOK {
		t.Fatalf("execute status = %d (%s), want 200", code, body)
	}

	// 7. Export sovereign bundle via HTTP and verify via POST /v1/governance/fork/verify.
	code, exportBody := doHTTP(t, app, http.MethodGet, "/v1/governance/export", "", "", nil)
	if code != http.StatusOK {
		t.Fatalf("export status = %d (%s), want 200", code, exportBody)
	}
	var exported SovereignBundle
	if err := json.Unmarshal(exportBody, &exported); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	code, verifyBody := doHTTP(t, app, http.MethodPost, "/v1/governance/fork/verify", "", "", VerifyBundleIn{Bundle: exported})
	if code != http.StatusOK {
		t.Fatalf("verify valid bundle status = %d (%s), want 200", code, verifyBody)
	}

	// Tampered bundle must fail verification (400).
	exported.State.Treasury.BalanceMicroUSD += 42
	if code, _ := doHTTP(t, app, http.MethodPost, "/v1/governance/fork/verify", "", "", VerifyBundleIn{Bundle: exported}); code != http.StatusBadRequest {
		t.Fatalf("verify tampered bundle status = %d, want 400", code)
	}

	// 8. Export fails closed (503) when exporter key is not configured.
	appNoExport := mountTestHTTP(t, eng, "", nil)
	if code, _ := doHTTP(t, appNoExport, http.MethodGet, "/v1/governance/export", "", "", nil); code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured export status = %d, want 503", code)
	}

	// 9. Overflowing DurationMs on POST /v1/governance/emergency must be rejected (403).
	if code, body := doHTTP(t, app, http.MethodPost, "/v1/governance/emergency", "guardian", actors["guardian"].member.Org, map[string]any{
		"action_id":        "http-em-overflow",
		"kind":             EmergencyPauseSubsystem,
		"target_subsystem": "treasury_disbursements",
		"duration_ms":      int64(9223372036854775807),
		"reason":           "overflow test",
		"issued_at":        clk.Now(),
		"nonce":            "http-em-ovf",
		"signature":        "00",
	}); code != http.StatusForbidden {
		t.Fatalf("overflow duration_ms status = %d (%s), want 403", code, body)
	}
}

func TestHTTPOpenAPIEveryRouteDescribed(t *testing.T) {
	eng, _, actors := setupDAO(t)
	app := mountTestHTTP(t, eng, "alice", actors["alice"].priv)

	raw, err := json.Marshal(app.OpenAPISpec())
	if err != nil {
		t.Fatalf("marshal openapi: %v", err)
	}
	var doc struct {
		Paths map[string]map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal openapi: %v", err)
	}

	count := 0
	for p, methods := range doc.Paths {
		if !strings.HasPrefix(p, "/v1/governance") {
			continue
		}
		for m, op := range methods {
			count++
			if d, _ := op["description"].(string); strings.TrimSpace(d) == "" {
				t.Errorf("%s %s missing zipdoc OpenAPI/MCP description", strings.ToUpper(m), p)
			}
		}
	}
	if count != 20 {
		t.Fatalf("expected 20 typed /v1/governance routes in OpenAPI spec, got %d", count)
	}
}
