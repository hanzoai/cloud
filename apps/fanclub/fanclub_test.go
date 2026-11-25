// SPDX-License-Identifier: Apache-2.0 OR MIT

package fanclub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

type memBase struct {
	mu      sync.Mutex
	seq     int
	records map[string]map[string][]BaseRecord // org -> collection -> records
}

func newMemBase() *memBase {
	return &memBase{records: map[string]map[string][]BaseRecord{}}
}

func (m *memBase) Collections(_ context.Context, org string) ([]BaseCollectionCount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []BaseCollectionCount{}
	for col, list := range m.records[org] {
		if len(list) > 0 {
			out = append(out, BaseCollectionCount{Name: col, Records: int64(len(list))})
		}
	}
	return out, nil
}

func (m *memBase) List(_ context.Context, org, collection string, limit, offset int) ([]BaseRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.records[org][collection]
	if offset >= len(list) {
		return []BaseRecord{}, nil
	}
	end := offset + limit
	if end > len(list) {
		end = len(list)
	}
	out := make([]BaseRecord, end-offset)
	copy(out, list[offset:end])
	return out, nil
}

func (m *memBase) Get(_ context.Context, org, collection, id string) (*BaseRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range m.records[org][collection] {
		if r.ID == id {
			cp := r
			return &cp, nil
		}
	}
	return nil, zip.ErrNotFound("no such record")
}

func (m *memBase) Create(_ context.Context, org, collection string, doc json.RawMessage) (*BaseRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.seq++
	if m.records[org] == nil {
		m.records[org] = map[string][]BaseRecord{}
	}
	now := time.Now().UTC()
	rec := BaseRecord{
		Collection: collection,
		ID:         fmt.Sprintf("rec-%06d", m.seq),
		Doc:        append(json.RawMessage(nil), doc...),
		Created:    now,
		Updated:    now,
	}
	m.records[org][collection] = append(m.records[org][collection], rec)
	return &rec, nil
}

func (m *memBase) Update(_ context.Context, org, collection, id string, doc json.RawMessage) (*BaseRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.records[org][collection]
	for i := range list {
		if list[i].ID == id {
			list[i].Doc = append(json.RawMessage(nil), doc...)
			list[i].Updated = time.Now().UTC()
			cp := list[i]
			return &cp, nil
		}
	}
	return nil, zip.ErrNotFound("no such record")
}

func httpStatus(err error) int {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return he.Status
	}
	return 0
}

func callerCtx(org, user string, orgAdmin, superAdmin bool) context.Context {
	return zip.WithCaller(context.Background(), zip.Caller{
		Org:      org,
		Owner:    org,
		User:     user,
		Name:     user,
		Email:    user + "@" + org + ".example",
		OrgAdmin: orgAdmin,
		Admin:    superAdmin,
	})
}

func TestScoreMemberDeterministicAndConsentBounded(t *testing.T) {
	m := MemberDoc{DaysToRenewal: 11, Visits90d: 0, Engagement: 13, Status: "Active", Consent: false}
	a, b := ScoreMember(m), ScoreMember(m)
	if a.Risk != b.Risk || a.Risk > 98 || a.NextBest != "No outreach permitted" {
		t.Fatalf("ScoreMember = %+v, want deterministic bounded risk with no outreach permitted", a)
	}
}

func TestOAuthExchangeEnforcesExactRedirectAndValidatesToken(t *testing.T) {
	iamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/iam/oauth/token" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		if r.FormValue("code") != "good-code" || r.FormValue("code_verifier") == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"invalid_grant","error_description":"bad code"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"jwt-verified","token_type":"Bearer","expires_in":3600}`))
	}))
	defer iamSrv.Close()

	s := &service{
		deps: Deps{
			Log:          luxlog.New("test"),
			IAMIssuer:    iamSrv.URL,
			ClientID:     "hanzo-fanclub",
			RedirectURIs: []string{"http://127.0.0.1:8080/fanclub/index.html"},
			Store:        newMemBase(),
			ValidateToken: func(raw string) (VerifiedIdentity, error) {
				if raw != "jwt-verified" {
					return VerifiedIdentity{}, errors.New("bad signature")
				}
				return VerifiedIdentity{
					Org:        "acme",
					User:       "u-1",
					Username:   "alice",
					Issuer:     iamSrv.URL,
					IsOrgAdmin: true,
				}, nil
			},
		},
		http:  iamSrv.Client(),
		now:   time.Now,
		orgMu: map[string]*sync.Mutex{},
	}

	verifier := strings.Repeat("v", 48)
	// Mismatched redirect URI must fail closed with 400.
	_, err := s.oauthExchange(context.Background(), &OAuthExchangeIn{
		Code:         "good-code",
		CodeVerifier: verifier,
		RedirectURI:  "https://evil.example/fanclub/index.html",
	})
	if httpStatus(err) != http.StatusBadRequest {
		t.Fatalf("mismatched redirectUri status = %d (%v), want 400", httpStatus(err), err)
	}

	// Exact redirect URI + valid code succeeds and returns verified IAM identity.
	out, err := s.oauthExchange(context.Background(), &OAuthExchangeIn{
		Code:         "good-code",
		CodeVerifier: verifier,
		RedirectURI:  "http://127.0.0.1:8080/fanclub/index.html",
	})
	if err != nil {
		t.Fatalf("oauthExchange valid: %v", err)
	}
	if out.AccessToken != "jwt-verified" || out.Identity.Org != "acme" || !out.Identity.IsOrgAdmin {
		t.Fatalf("oauthExchange out = %+v", out)
	}
}

func TestTenantIsolationRoleAuthAndTasksDispatch(t *testing.T) {
	store := newMemBase()
	var enqueued []string
	s := &service{
		deps: Deps{
			Log:       luxlog.New("test"),
			IAMIssuer: "https://hanzo.id",
			Store:     store,
			EnqueueTask: func(_ context.Context, org, kind string, _ json.RawMessage) (string, error) {
				id := fmt.Sprintf("task-%s-%s-%d", org, kind, len(enqueued)+1)
				enqueued = append(enqueued, id)
				return id, nil
			},
		},
		http:  http.DefaultClient,
		now:   time.Now,
		orgMu: map[string]*sync.Mutex{},
	}

	// Unauthenticated callers are denied (401).
	if _, err := s.me(context.Background(), &NoIn{}); httpStatus(err) != http.StatusUnauthorized {
		t.Fatalf("unauthenticated me = %v, want 401", err)
	}
	if _, err := s.summary(context.Background(), &NoIn{}); httpStatus(err) != http.StatusUnauthorized {
		t.Fatalf("unauthenticated summary = %v, want 401", err)
	}

	alphaAdmin := callerCtx("org-alpha", "alice", true, false)
	alphaMember := callerCtx("org-alpha", "bob", false, false)
	betaAdmin := callerCtx("org-beta", "carol", true, false)

	// Seed org-alpha with synthetic fixtures (idempotent).
	seeded, err := s.seed(alphaAdmin, &SeedIn{Profile: "northstar"})
	if err != nil || seeded.Seeded != 90 || !seeded.Synthetic {
		t.Fatalf("seed org-alpha = %+v, err=%v", seeded, err)
	}
	again, err := s.seed(alphaAdmin, &SeedIn{Profile: "northstar"})
	if err != nil || again.Seeded != 0 || again.Existing != 90 {
		t.Fatalf("idempotent seed = %+v, err=%v", again, err)
	}

	// Cross-tenant isolation: org-beta sees zero of org-alpha's members.
	betaMem, err := s.members(betaAdmin, &MembersIn{})
	if err != nil || betaMem.Total != 0 {
		t.Fatalf("org-beta members = %+v, err=%v, want 0 (tenant isolated)", betaMem, err)
	}

	// Plan proposals in org-alpha.
	planned, err := s.planProposals(alphaAdmin, &PlanProposalsIn{Journey: "retention", Limit: 5})
	if err != nil || planned.Created == 0 || planned.Mode != "simulation-only" {
		t.Fatalf("planProposals = %+v, err=%v", planned, err)
	}
	props, err := s.proposals(alphaAdmin, &NoIn{})
	if err != nil || len(props.Proposals) == 0 {
		t.Fatalf("proposals = %+v, err=%v", props, err)
	}
	propID := props.Proposals[0].ID

	// Non-admin member of org-alpha is refused 403 on decision.
	if _, err := s.decideProposal(alphaMember, &DecisionIn{ID: propID, Action: "approve"}); httpStatus(err) != http.StatusForbidden {
		t.Fatalf("non-admin approve = %v, want 403", err)
	}

	// Admin of org-beta cannot approve org-alpha's proposal (404).
	if _, err := s.decideProposal(betaAdmin, &DecisionIn{ID: propID, Action: "approve"}); httpStatus(err) != http.StatusNotFound {
		t.Fatalf("cross-tenant approve = %v, want 404", err)
	}

	// Org-alpha admin approves -> simulation-only + durable task enqueued.
	dec, err := s.decideProposal(alphaAdmin, &DecisionIn{ID: propID, Action: "approve"})
	if err != nil {
		t.Fatalf("admin approve: %v", err)
	}
	if dec.Status != "Approved (simulation)" || dec.ExternalSideEffects || dec.TaskID == "" || len(enqueued) != 1 {
		t.Fatalf("decision = %+v, enqueued=%v", dec, enqueued)
	}

	// Second approval on same proposal fails 409 Conflict (atomic idempotency).
	if _, err := s.decideProposal(alphaAdmin, &DecisionIn{ID: propID, Action: "approve"}); httpStatus(err) != http.StatusConflict {
		t.Fatalf("duplicate approve = %v, want 409", err)
	}
}

func TestChatFailsClosedOnUnconfiguredAndOutageAndSendsAggregatesOnly(t *testing.T) {
	store := newMemBase()
	alphaAdmin := callerCtx("org-alpha", "alice", true, false)

	// 1. Unconfigured inference -> 503 fail-closed (never synthetic rules!).
	sUnconf := &service{
		deps:  Deps{Store: store, InferenceConfigured: false},
		now:   time.Now,
		orgMu: map[string]*sync.Mutex{},
	}
	if _, err := sUnconf.chat(alphaAdmin, &ChatIn{Question: "How many members are at risk?"}); httpStatus(err) != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured chat = %v, want 503", err)
	}

	// Seed records so we can verify ZERO individual member names leak into the prompt.
	_, _ = sUnconf.seed(alphaAdmin, &SeedIn{Profile: "northstar"})

	// 2. Upstream outage -> 503 fail-closed.
	sOutage := &service{
		deps: Deps{
			Store:               store,
			InferenceConfigured: true,
			InferenceTransport:  "http",
			DefaultModel:        "zen",
			Chat: func(_ context.Context, _ ChatRequest) (ChatResponse, error) {
				return ChatResponse{}, errors.New("upstream 503 overloaded")
			},
		},
		now:   time.Now,
		orgMu: map[string]*sync.Mutex{},
	}
	if _, err := sOutage.chat(alphaAdmin, &ChatIn{Question: "How many members are at risk?"}); httpStatus(err) != http.StatusServiceUnavailable {
		t.Fatalf("outage chat = %v, want 503", err)
	}

	// 3. Real model response -> succeeds and sends aggregates only (no member names).
	var seenPrompt string
	sLive := &service{
		deps: Deps{
			Store:               store,
			InferenceConfigured: true,
			InferenceTransport:  "http",
			DefaultModel:        "zen",
			Chat: func(_ context.Context, req ChatRequest) (ChatResponse, error) {
				seenPrompt = req.Prompt
				return ChatResponse{Content: "Aggregate risk summary from Hanzo model.", Model: "hanzo/zen", TotalTokens: 42}, nil
			},
		},
		now:   time.Now,
		orgMu: map[string]*sync.Mutex{},
	}
	ans, err := sLive.chat(alphaAdmin, &ChatIn{Question: "Summarize risk counts."})
	if err != nil {
		t.Fatalf("live chat: %v", err)
	}
	if ans.Provider != "hanzo-cloud-ai" || ans.Model != "hanzo/zen" || ans.GroundedOn != "aggregates-only" {
		t.Fatalf("ans = %+v", ans)
	}
	if strings.Contains(seenPrompt, "Avery") || strings.Contains(seenPrompt, "northstar-001") {
		t.Fatalf("prompt leaked individual member data: %s", seenPrompt)
	}
}
