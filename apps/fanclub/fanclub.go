// SPDX-License-Identifier: Apache-2.0 OR MIT

package fanclub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

const (
	// CollectionMembers stores per-org FanClub member records in Cloud Base.
	CollectionMembers = "fc_members"
	// CollectionProposals stores per-org human-review proposals in Cloud Base.
	CollectionProposals = "fc_proposals"
	// CollectionAudit stores per-org provenance entries in Cloud Base.
	CollectionAudit = "fc_audit"
	// SimulationTaskKind is the Cloud Tasks kind enqueued on proposal approval.
	SimulationTaskKind = "fanclub_action_simulation"
)

// BaseRecord mirrors one record in Cloud Base without importing the host.
type BaseRecord struct {
	Collection string
	ID         string
	Doc        json.RawMessage
	Created    time.Time
	Updated    time.Time
}

// BaseCollectionCount reports one collection's record count in Cloud Base.
type BaseCollectionCount struct {
	// Name is the Cloud Base collection name.
	Name string `json:"name"`
	// Records is the number of stored records for the caller's org.
	Records int64 `json:"records"`
}

// BaseStore is the narrow interface apps/fanclub uses to read and write
// fc_members, fc_proposals, and fc_audit in Cloud Base. Never an independent DB.
type BaseStore interface {
	Collections(ctx context.Context, org string) ([]BaseCollectionCount, error)
	List(ctx context.Context, org, collection string, limit, offset int) ([]BaseRecord, error)
	Get(ctx context.Context, org, collection, id string) (*BaseRecord, error)
	Create(ctx context.Context, org, collection string, doc json.RawMessage) (*BaseRecord, error)
	Update(ctx context.Context, org, collection, id string, doc json.RawMessage) (*BaseRecord, error)
}

// VerifiedIdentity is the IAM-validated identity returned after token verification.
type VerifiedIdentity struct {
	// Org is the validated IAM owner / organization slug.
	Org string `json:"org"`
	// User is the canonical IAM user subject.
	User string `json:"user"`
	// Username is the IAM username.
	Username string `json:"username,omitempty"`
	// Email is the validated email claim when present.
	Email string `json:"email,omitempty"`
	// Issuer is the validated OIDC issuer URL.
	Issuer string `json:"issuer"`
	// IsOrgAdmin reports IAM's isAdmin flag (admin of one's own org).
	IsOrgAdmin bool `json:"isOrgAdmin"`
	// IsSuperAdmin reports platform SuperAdmin (owner == reserved admin org).
	IsSuperAdmin bool `json:"isSuperAdmin"`
}

// ChatRequest is one inference request routed through Cloud's AIClient.
type ChatRequest struct {
	Model      string
	Prompt     string
	Org        string
	BillingOrg string
	Project    string
}

// ChatResponse is one real inference completion returned by Cloud's AIClient.
type ChatResponse struct {
	Content          string
	Model            string
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

// Deps is everything apps/fanclub asks of its host. Zero imports of
// github.com/hanzoai/cloud so both OSS and private Cloud mount the same source.
type Deps struct {
	Log                 luxlog.Logger
	IAMIssuer           string
	AdminOrg            string
	ClientID            string
	RedirectURIs        []string
	DefaultModel        string
	InferenceConfigured bool
	InferenceTransport  string
	ValidateToken       func(raw string) (VerifiedIdentity, error)
	HTTPClient          *http.Client
	Store               BaseStore
	EnqueueTask         func(ctx context.Context, org, kind string, payload json.RawMessage) (string, error)
	Chat                func(ctx context.Context, req ChatRequest) (ChatResponse, error)
	Now                 func() time.Time
}

type service struct {
	deps  Deps
	http  *http.Client
	now   func() time.Time
	mu    sync.Mutex
	orgMu map[string]*sync.Mutex
}

// Mount registers every FanClub typed operation under /v1/fanclub. All routes
// are typed zip ops so they project simultaneously into REST, /v1/openapi.json,
// /mcp JSON-RPC 2.0, ZAP, and CLI under Cloud's IAM identity and audit boundary.
func Mount(app *zip.App, deps Deps) error {
	if app == nil {
		return errors.New("fanclub: nil zip.App")
	}
	if deps.Store == nil {
		return errors.New("fanclub: nil BaseStore")
	}
	hc := deps.HTTPClient
	if hc == nil {
		hc = &http.Client{
			Timeout: 15 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	nowFn := deps.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	deps.IAMIssuer = strings.TrimRight(strings.TrimSpace(deps.IAMIssuer), "/")
	if deps.AdminOrg == "" {
		deps.AdminOrg = "admin"
	}
	if deps.ClientID == "" {
		deps.ClientID = "hanzo-fanclub"
	}

	s := &service{
		deps:  deps,
		http:  hc,
		now:   nowFn,
		orgMu: map[string]*sync.Mutex{},
	}

	g := app.Group("/v1/fanclub")
	g.Get("/config", s.config,
		zip.WithOperationID("fanclub_config"),
		zip.WithSummary("Public IAM OIDC and capability configuration for FanClub"),
		zip.WithTags("fanclub"))
	g.Post("/oauth/exchange", s.oauthExchange,
		zip.WithOperationID("fanclub_oauth_exchange"),
		zip.WithSummary("Exchange a Hanzo IAM PKCE authorization code for a verified access token"),
		zip.WithTags("fanclub"))
	g.Get("/me", s.me,
		zip.WithOperationID("fanclub_me"),
		zip.WithSummary("Return the IAM-validated organization and principal identity"),
		zip.WithTags("fanclub"))
	g.Get("/inference/status", s.inferenceStatus,
		zip.WithOperationID("fanclub_inference_status"),
		zip.WithSummary("Report inference configuration status without claiming live readiness"),
		zip.WithTags("fanclub"))
	g.Post("/chat", s.chat,
		zip.WithOperationID("fanclub_chat"),
		zip.WithSummary("Run authenticated Hanzo AI inference over aggregate workspace metrics only"),
		zip.WithTags("fanclub"))
	g.Get("/summary", s.summary,
		zip.WithOperationID("fanclub_summary"),
		zip.WithSummary("Read-only IAM-scoped summary of workspace metrics, journeys, and Cloud Base counts"),
		zip.WithTags("fanclub"))
	g.Post("/seed", s.seed,
		zip.WithOperationID("fanclub_seed"),
		zip.WithSummary("Idempotently seed opt-in fictional synthetic records into Cloud Base"),
		zip.WithTags("fanclub"),
		zip.WithStatus(201))
	g.Get("/members", s.members,
		zip.WithOperationID("fanclub_members"),
		zip.WithSummary("List IAM org member records from Cloud Base with deterministic rules scores"),
		zip.WithTags("fanclub"))
	g.Get("/proposals", s.proposals,
		zip.WithOperationID("fanclub_proposals"),
		zip.WithSummary("List human-review proposals stored in Cloud Base"),
		zip.WithTags("fanclub"))
	g.Post("/proposals/plan", s.planProposals,
		zip.WithOperationID("fanclub_proposals_plan"),
		zip.WithSummary("Generate simulation-only human-review proposals for a decision journey"),
		zip.WithTags("fanclub"),
		zip.WithStatus(201))
	g.Post("/proposals/:id/decision", s.decideProposal,
		zip.WithOperationID("fanclub_proposal_decision"),
		zip.WithSummary("Approve (simulation) or reject a proposal with org-admin role enforcement"),
		zip.WithTags("fanclub"))
	g.Get("/audit", s.auditList,
		zip.WithOperationID("fanclub_audit"),
		zip.WithSummary("List workspace provenance and approval receipts from Cloud Base"),
		zip.WithTags("fanclub"))
	g.Get("/evaluation", s.evaluation,
		zip.WithOperationID("fanclub_evaluation"),
		zip.WithSummary("Compute deterministic rules baseline holdout metrics over Cloud Base records"),
		zip.WithTags("fanclub"))
	g.Get("/stack", s.stackStatus,
		zip.WithOperationID("fanclub_stack"),
		zip.WithSummary("Report honest readiness of Hanzo Cloud primitives backing FanClub"),
		zip.WithTags("fanclub"))

	if deps.Log != nil {
		deps.Log.Info("fanclub mounted",
			"iam_issuer", deps.IAMIssuer,
			"inference_configured", deps.InferenceConfigured,
			"inference_transport", deps.InferenceTransport)
	}
	return nil
}

func errUnavailable(msg string) *zip.HTTPError {
	return zip.Errorf(http.StatusServiceUnavailable, "%s", msg)
}

func (s *service) lockOrg(org string) func() {
	s.mu.Lock()
	m, ok := s.orgMu[org]
	if !ok {
		m = &sync.Mutex{}
		s.orgMu[org] = m
	}
	s.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// requireCaller enforces that ctx carries a validated IAM principal (set ONLY
// by Cloud's SanitizeIdentity middleware) with a non-empty org. Unauthenticated
// calls fail 401; org-less calls fail 403. Never reads a client-chosen tenant.
func requireCaller(ctx context.Context) (zip.Caller, error) {
	c := zip.CallerOf(ctx)
	if strings.TrimSpace(c.User) == "" {
		return zip.Caller{}, zip.ErrUnauthorized("authenticated Hanzo IAM principal required")
	}
	if strings.TrimSpace(c.Org) == "" {
		return zip.Caller{}, zip.ErrForbidden("validated IAM organization required")
	}
	return c, nil
}

func actorLabel(c zip.Caller) string {
	if strings.TrimSpace(c.Name) != "" {
		return c.Org + "/" + c.Name
	}
	return c.Org + "/" + c.User
}

// NoIn is the empty input for parameterless read operations.
type NoIn struct{}

// Disclosures explicitly labels synthetic data, rules baselines, simulation
// boundaries, and unqualified model-training gates.
type Disclosures struct {
	// SyntheticData states that seeded fixture records are fictional.
	SyntheticData string `json:"syntheticData"`
	// RulesBaseline states that member risk scores are deterministic rules, not trained AI.
	RulesBaseline string `json:"rulesBaseline"`
	// Approvals states that approving a proposal executes only a durable simulation task.
	Approvals string `json:"approvals"`
	// TrainToServe states that customer-owned model training is not yet qualified.
	TrainToServe string `json:"trainToServe"`
}

// ConfigOut is the public IAM OIDC and capability discovery payload.
type ConfigOut struct {
	// IAMIssuer is the configured Hanzo IAM issuer URL.
	IAMIssuer string `json:"iamIssuer"`
	// AuthorizationEndpoint is the IAM OIDC authorization endpoint for PKCE sign-in.
	AuthorizationEndpoint string `json:"authorizationEndpoint"`
	// TokenEndpoint is the IAM OIDC token endpoint used by oauth/exchange.
	TokenEndpoint string `json:"tokenEndpoint"`
	// UserinfoEndpoint is the IAM OIDC userinfo endpoint.
	UserinfoEndpoint string `json:"userinfoEndpoint"`
	// JWKSURI is the IAM JSON Web Key Set URL Cloud verifies tokens against.
	JWKSURI string `json:"jwksUri"`
	// ClientID is the registered public OIDC client ID for PKCE sign-in.
	ClientID string `json:"clientId"`
	// AllowedRedirectURIs lists the exact redirect URIs accepted by oauth/exchange.
	AllowedRedirectURIs []string `json:"allowedRedirectUris"`
	// Collections names the Cloud Base collections FanClub uses exclusively.
	Collections []string `json:"collections"`
	// InferenceConfigured reports whether Cloud's AI client has an endpoint configured.
	InferenceConfigured bool `json:"inferenceConfigured"`
	// InferenceTransport names the configured AI transport ("http", "zap", or "").
	InferenceTransport string `json:"inferenceTransport"`
	// DefaultModel is Cloud's configured default model name.
	DefaultModel string `json:"defaultModel"`
	// Profiles lists the available fictional industry profiles for opt-in synthetic seeding.
	Profiles []Profile `json:"profiles"`
	// Disclosures states the product and qualification boundaries honestly.
	Disclosures Disclosures `json:"disclosures"`
}

func defaultDisclosures() Disclosures {
	return Disclosures{
		SyntheticData: "Opt-in seeded fixtures are reproducible fictional data stored in your IAM org's Cloud Base collections; never real customer data.",
		RulesBaseline: "Member risk scoring and Model Lab metrics are computed by an explicit deterministic rules baseline, not a trained ML model.",
		Approvals:     "Approvals require org-admin authorization and enqueue only a simulation-only Cloud Task with external_side_effects=false.",
		TrainToServe:  "Custom LoRA/PEFT training, holdout qualification, and independent Engine serving remain separate qualification gates.",
	}
}

// config returns operator-configured public IAM OIDC metadata and honest
// capability disclosures so the embedded FanClub UI can initiate PKCE sign-in.
func (s *service) config(_ context.Context, _ *NoIn) (*ConfigOut, error) {
	iss := s.deps.IAMIssuer
	redirects := append([]string{}, s.deps.RedirectURIs...)
	return &ConfigOut{
		IAMIssuer:             iss,
		AuthorizationEndpoint: iss + "/v1/iam/oauth/authorize",
		TokenEndpoint:         iss + "/v1/iam/oauth/token",
		UserinfoEndpoint:      iss + "/v1/iam/oauth/userinfo",
		JWKSURI:               iss + "/v1/iam/.well-known/jwks",
		ClientID:              s.deps.ClientID,
		AllowedRedirectURIs:   redirects,
		Collections:           []string{CollectionMembers, CollectionProposals, CollectionAudit},
		InferenceConfigured:   s.deps.InferenceConfigured,
		InferenceTransport:    s.deps.InferenceTransport,
		DefaultModel:          s.deps.DefaultModel,
		Profiles:              append([]Profile{}, syntheticProfiles...),
		Disclosures:           defaultDisclosures(),
	}, nil
}

// OAuthExchangeIn carries the OIDC Authorization Code + PKCE parameters.
type OAuthExchangeIn struct {
	// Code is the single-use authorization code returned by Hanzo IAM.
	Code string `json:"code"`
	// CodeVerifier is the RFC 7636 PKCE verifier matching the code_challenge.
	CodeVerifier string `json:"codeVerifier"`
	// RedirectURI must match an operator-registered redirect URI verbatim.
	RedirectURI string `json:"redirectUri"`
}

// OAuthExchangeOut returns the IAM-verified access token and principal claims.
type OAuthExchangeOut struct {
	// AccessToken is the IAM-signed JWT verified by Cloud's TokenValidator.
	AccessToken string `json:"accessToken"`
	// TokenType is typically "Bearer".
	TokenType string `json:"tokenType"`
	// ExpiresIn is the remaining lifetime in seconds reported by IAM.
	ExpiresIn int `json:"expiresIn"`
	// Identity holds the cryptographically verified IAM claims.
	Identity VerifiedIdentity `json:"identity"`
}

// oauthExchange exchanges an IAM PKCE authorization code for an access token,
// enforcing an exact registered redirect URI allowlist and validating the
// resulting JWT against Cloud's IAM JWKS validator before returning it.
func (s *service) oauthExchange(ctx context.Context, in *OAuthExchangeIn) (*OAuthExchangeOut, error) {
	if s.deps.IAMIssuer == "" {
		return nil, errUnavailable("IAM issuer is not configured")
	}
	code := strings.TrimSpace(in.Code)
	verifier := strings.TrimSpace(in.CodeVerifier)
	redirectURI := strings.TrimSpace(in.RedirectURI)
	if code == "" {
		return nil, zip.ErrBadRequest("code is required")
	}
	if len(verifier) < 43 || len(verifier) > 128 {
		return nil, zip.ErrBadRequest("codeVerifier must be 43..128 characters per RFC 7636")
	}
	if !s.redirectAllowed(redirectURI) {
		return nil, zip.ErrBadRequest("redirectUri does not exactly match any registered redirect URI")
	}

	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {s.deps.ClientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.deps.IAMIssuer+"/v1/iam/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, zip.ErrInternal("failed to construct IAM token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	res, err := s.http.Do(req)
	if err != nil {
		return nil, errUnavailable("Hanzo IAM token endpoint unavailable")
	}
	defer func() { _ = res.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, errUnavailable("failed reading Hanzo IAM token response")
	}
	var tokResp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	_ = json.Unmarshal(raw, &tokResp)
	if res.StatusCode < 200 || res.StatusCode >= 300 || tokResp.AccessToken == "" {
		msg := strings.TrimSpace(tokResp.ErrorDesc)
		if msg == "" {
			msg = strings.TrimSpace(tokResp.Error)
		}
		if msg == "" {
			msg = fmt.Sprintf("IAM token exchange failed (HTTP %d)", res.StatusCode)
		}
		return nil, zip.ErrUnauthorized(msg)
	}
	if s.deps.ValidateToken == nil {
		return nil, errUnavailable("IAM token validator is not configured")
	}
	ident, err := s.deps.ValidateToken(tokResp.AccessToken)
	if err != nil {
		return nil, zip.ErrUnauthorized("IAM access token failed verification: " + err.Error())
	}
	if strings.TrimSpace(ident.Org) == "" {
		return nil, zip.ErrForbidden("verified IAM token carries no organization owner")
	}
	tokenType := tokResp.TokenType
	if tokenType == "" {
		tokenType = "Bearer"
	}
	return &OAuthExchangeOut{
		AccessToken: tokResp.AccessToken,
		TokenType:   tokenType,
		ExpiresIn:   tokResp.ExpiresIn,
		Identity:    ident,
	}, nil
}

func (s *service) redirectAllowed(u string) bool {
	if u == "" {
		return false
	}
	for _, allowed := range s.deps.RedirectURIs {
		if u == allowed {
			return true
		}
	}
	return false
}

// MeOut describes the IAM-validated organization and principal identity.
type MeOut struct {
	// Org is the validated effective organization from Cloud's identity boundary.
	Org string `json:"org"`
	// Owner is the principal's validated home IAM organization.
	Owner string `json:"owner"`
	// User is the canonical IAM user ID (sub).
	User string `json:"user"`
	// Username is the IAM username.
	Username string `json:"username,omitempty"`
	// Email is the validated email address when present.
	Email string `json:"email,omitempty"`
	// IsOrgAdmin reports whether the principal administers its own org.
	IsOrgAdmin bool `json:"isOrgAdmin"`
	// IsSuperAdmin reports platform SuperAdmin authority (owner == admin org).
	IsSuperAdmin bool `json:"isSuperAdmin"`
	// CanApprove reports whether the principal is authorized to decide proposals.
	CanApprove bool `json:"canApprove"`
	// Issuer is the configured IAM issuer URL.
	Issuer string `json:"issuer"`
}

// me returns the IAM-validated organization, user identity, and approval
// authorization derived exclusively from Cloud's SanitizeIdentity boundary.
func (s *service) me(ctx context.Context, _ *NoIn) (*MeOut, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	owner := c.Owner
	if owner == "" {
		owner = c.Org
	}
	return &MeOut{
		Org:          c.Org,
		Owner:        owner,
		User:         c.User,
		Username:     c.Name,
		Email:        c.Email,
		IsOrgAdmin:   c.OrgAdmin,
		IsSuperAdmin: c.Admin,
		CanApprove:   c.OrgAdmin || c.Admin,
		Issuer:       s.deps.IAMIssuer,
	}, nil
}

// InferenceStatusOut honestly describes inference configuration without making
// an unverified live readiness claim.
type InferenceStatusOut struct {
	// Configured is true when Cloud's AIClient has an HTTP or ZAP endpoint configured.
	Configured bool `json:"configured"`
	// Transport is "http", "zap", "in-process", or "" when unconfigured.
	Transport string `json:"transport"`
	// Model is Cloud's configured default model identifier.
	Model string `json:"model"`
	// ReadinessClaim is always false: configuration does not prove live model availability.
	ReadinessClaim bool `json:"readinessClaim"`
	// FailClosed is always true: chat returns 503 on outage rather than faking AI.
	FailClosed bool `json:"failClosed"`
	// Note explains the current configuration posture.
	Note string `json:"note"`
}

// inferenceStatus reports whether Cloud's AIClient is configured. It is a
// configuration statement only, never a readiness claim.
func (s *service) inferenceStatus(_ context.Context, _ *NoIn) (*InferenceStatusOut, error) {
	note := "Inference is not configured (set CLOUD_AI_BASE_URL or CLOUD_AI_ZAP_ADDR). Chat fails closed with 503 and never falls back to synthetic rules or external providers."
	if s.deps.InferenceConfigured {
		note = fmt.Sprintf("Cloud AI client configured via %s transport (model %q). Readiness is verified only on real inference calls; outages fail closed with 503.", s.deps.InferenceTransport, s.deps.DefaultModel)
	}
	return &InferenceStatusOut{
		Configured:     s.deps.InferenceConfigured,
		Transport:      s.deps.InferenceTransport,
		Model:          s.deps.DefaultModel,
		ReadinessClaim: false,
		FailClosed:     true,
		Note:           note,
	}, nil
}

// ChatIn submits a natural-language analytics question grounded strictly on
// aggregate workspace metrics (no individual member records).
type ChatIn struct {
	// Question is the operator's question (1..400 characters).
	Question string `json:"question"`
	// Model optionally overrides Cloud's default model for this request.
	Model string `json:"model,omitempty"`
}

// ChatUsage records token counts reported by the upstream model.
type ChatUsage struct {
	// PromptTokens is the input token count reported by the model.
	PromptTokens int `json:"promptTokens"`
	// CompletionTokens is the generated token count reported by the model.
	CompletionTokens int `json:"completionTokens"`
	// TotalTokens is the total token count reported by the model.
	TotalTokens int `json:"totalTokens"`
}

// ChatOut returns a verified inference answer from Hanzo Cloud's AIClient.
type ChatOut struct {
	// Answer is the real model completion text.
	Answer string `json:"answer"`
	// Provider identifies the canonical Cloud AI path ("hanzo-cloud-ai").
	Provider string `json:"provider"`
	// Transport names the active Cloud AI transport ("http", "zap", "in-process").
	Transport string `json:"transport"`
	// Model is the model identifier that answered.
	Model string `json:"model"`
	// GroundedOn confirms only aggregate workspace counts were sent ("aggregates-only").
	GroundedOn string `json:"groundedOn"`
	// Usage reports token usage from the model response.
	Usage ChatUsage `json:"usage"`
	// Disclaimer states grounding scope honestly.
	Disclaimer string `json:"disclaimer"`
}

// chat routes an authenticated analytics question to Cloud's AIClient using
// ONLY aggregate org metrics from Cloud Base (zero PII). Fails closed with 503
// when unconfigured or on model outage — never substitutes mock answers.
func (s *service) chat(ctx context.Context, in *ChatIn) (*ChatOut, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	q := strings.TrimSpace(in.Question)
	if q == "" || len(q) > 400 {
		return nil, zip.ErrBadRequest("question must be 1..400 characters")
	}
	if !s.deps.InferenceConfigured || s.deps.Chat == nil {
		return nil, errUnavailable("inference is not configured; set CLOUD_AI_BASE_URL or CLOUD_AI_ZAP_ADDR (fail-closed, no synthetic fallback)")
	}

	members, err := s.loadMembers(ctx, c.Org)
	if err != nil {
		return nil, err
	}
	proposals, err := s.loadProposals(ctx, c.Org, members)
	if err != nil {
		return nil, err
	}
	metrics := computeMetrics(c.Org, members, proposals)
	summaryJSON, _ := json.Marshal(map[string]any{
		"org":         c.Org,
		"members":     metrics.Members,
		"atRisk":      metrics.AtRisk,
		"consented":   metrics.Consented,
		"valueAtRisk": metrics.ValueAtRisk,
		"engagement":  metrics.Engagement,
		"pending":     metrics.Pending,
		"approved":    metrics.Approved,
	})

	model := strings.TrimSpace(in.Model)
	if model == "" {
		model = s.deps.DefaultModel
	}
	billingOrg := c.Owner
	if billingOrg == "" {
		billingOrg = c.Org
	}

	prompt := fmt.Sprintf(
		"You are a private organization analytics assistant inside Hanzo Cloud. "+
			"Answer ONLY from the aggregate workspace counts below. Never invent individual person facts, statistical significance, or causal uplift. Keep under 100 words.\n"+
			"Aggregate metrics: %s\nQuestion: %s",
		string(summaryJSON), q,
	)

	resp, err := s.deps.Chat(ctx, ChatRequest{
		Model:      model,
		Prompt:     prompt,
		Org:        c.Org,
		BillingOrg: billingOrg,
		Project:    c.Project,
	})
	if err != nil {
		_ = s.recordAudit(ctx, c.Org, actorLabel(c), "assistant.unavailable", "", map[string]any{
			"model":     model,
			"transport": s.deps.InferenceTransport,
			"error":     err.Error(),
		})
		return nil, errUnavailable("Hanzo AI inference unavailable: " + err.Error())
	}
	answer := strings.TrimSpace(resp.Content)
	if answer == "" {
		return nil, errUnavailable("Hanzo AI inference returned empty answer")
	}
	usedModel := resp.Model
	if usedModel == "" {
		usedModel = model
	}

	qTrunc := q
	if len(qTrunc) > 120 {
		qTrunc = qTrunc[:120]
	}
	_ = s.recordAudit(ctx, c.Org, actorLabel(c), "assistant.queried", "", map[string]any{
		"provider":  "hanzo-cloud-ai",
		"transport": s.deps.InferenceTransport,
		"model":     usedModel,
		"question":  qTrunc,
		"data":      "aggregates-only",
		"tokens":    resp.TotalTokens,
	})

	return &ChatOut{
		Answer:     answer,
		Provider:   "hanzo-cloud-ai",
		Transport:  s.deps.InferenceTransport,
		Model:      usedModel,
		GroundedOn: "aggregates-only",
		Usage: ChatUsage{
			PromptTokens:     resp.PromptTokens,
			CompletionTokens: resp.CompletionTokens,
			TotalTokens:      resp.TotalTokens,
		},
		Disclaimer: "Real Hanzo Cloud AI completion grounded only on aggregate org counts; no individual member records were sent.",
	}, nil
}

// SummaryOut is the IAM-scoped read-only workspace summary projected to both
// REST and MCP (/mcp tools/call fanclub_summary).
type SummaryOut struct {
	// Org is the validated IAM organization.
	Org string `json:"org"`
	// Actor is the validated IAM principal label.
	Actor string `json:"actor"`
	// Metrics summarizes member, risk, and approval counts.
	Metrics Metrics `json:"metrics"`
	// Journeys lists the available human-reviewed workflows.
	Journeys []Journey `json:"journeys"`
	// Evaluation summarizes the deterministic rules baseline holdout accuracy.
	Evaluation Evaluation `json:"evaluation"`
	// Collections lists the caller org's Cloud Base collections and record counts.
	Collections []BaseCollectionCount `json:"collections"`
	// SyntheticDataPresent is true when any record in fc_members carries synthetic=true.
	SyntheticDataPresent bool `json:"syntheticDataPresent"`
	// Disclosures states synthetic, rules, approval, and training boundaries.
	Disclosures Disclosures `json:"disclosures"`
}

// summary returns an IAM-scoped read-only workspace summary from Cloud Base.
// Registered as a typed op so authorized MCP clients can invoke fanclub_summary
// through /mcp with identical org isolation.
func (s *service) summary(ctx context.Context, _ *NoIn) (*SummaryOut, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	members, err := s.loadMembers(ctx, c.Org)
	if err != nil {
		return nil, err
	}
	proposals, err := s.loadProposals(ctx, c.Org, members)
	if err != nil {
		return nil, err
	}
	cols, err := s.deps.Store.Collections(ctx, c.Org)
	if err != nil {
		return nil, err
	}
	synth := false
	for _, m := range members {
		if m.Synthetic {
			synth = true
			break
		}
	}
	return &SummaryOut{
		Org:                  c.Org,
		Actor:                actorLabel(c),
		Metrics:              computeMetrics(c.Org, members, proposals),
		Journeys:             append([]Journey{}, defaultJourneys...),
		Evaluation:           computeEvaluation(members),
		Collections:          cols,
		SyntheticDataPresent: synth,
		Disclosures:          defaultDisclosures(),
	}, nil
}

// SeedIn selects which fictional industry profile to seed into Cloud Base.
type SeedIn struct {
	// Profile is "northstar" (default), "solstice", or "harbor".
	Profile string `json:"profile"`
}

// SeedOut reports how many synthetic fixture records were written to Cloud Base.
type SeedOut struct {
	// Org is the validated IAM organization seeded.
	Org string `json:"org"`
	// Profile is the fictional profile that was selected.
	Profile Profile `json:"profile"`
	// Seeded is the number of newly created synthetic fc_members records.
	Seeded int `json:"seeded"`
	// Existing is the count of pre-existing fc_members records (non-zero when already seeded).
	Existing int `json:"existing"`
	// Synthetic is always true to label these records explicitly as fictional fixtures.
	Synthetic bool `json:"synthetic"`
}

// seed idempotently populates 90 reproducible synthetic member records in Cloud
// Base fc_members for the caller's validated IAM org.
func (s *service) seed(ctx context.Context, in *SeedIn) (*SeedOut, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	prof, ok := findProfile(in.Profile)
	if !ok {
		return nil, zip.ErrBadRequest("unknown synthetic profile; choose northstar, solstice, or harbor")
	}
	unlock := s.lockOrg(c.Org)
	defer unlock()

	existing, err := s.deps.Store.List(ctx, c.Org, CollectionMembers, 1, 0)
	if err != nil {
		return nil, err
	}
	if len(existing) > 0 {
		all, err := s.listAll(ctx, c.Org, CollectionMembers)
		if err != nil {
			return nil, err
		}
		return &SeedOut{
			Org:       c.Org,
			Profile:   prof,
			Seeded:    0,
			Existing:  len(all),
			Synthetic: true,
		}, nil
	}

	fixtures := GenerateSyntheticMembers(prof, s.now())
	for _, doc := range fixtures {
		raw, err := json.Marshal(doc)
		if err != nil {
			return nil, zip.ErrInternal("encode fixture failed")
		}
		if _, err := s.deps.Store.Create(ctx, c.Org, CollectionMembers, raw); err != nil {
			return nil, err
		}
	}
	if err := s.recordAudit(ctx, c.Org, actorLabel(c), "workspace.seeded", CollectionMembers, map[string]any{
		"profile":   prof.ID,
		"count":     len(fixtures),
		"synthetic": true,
		"source":    "fictional deterministic fixtures",
	}); err != nil {
		return nil, err
	}
	return &SeedOut{
		Org:       c.Org,
		Profile:   prof,
		Seeded:    len(fixtures),
		Existing:  0,
		Synthetic: true,
	}, nil
}

// MembersIn filters the caller org's member listing.
type MembersIn struct {
	// Q searches name, tier, or segment (case-insensitive, max 80 chars).
	Q string `json:"q"`
	// Filter narrows to "all" (default), "high" (elevated risk), or "consented".
	Filter string `json:"filter"`
}

// MembersOut returns member records from Cloud Base enriched with rules scores.
type MembersOut struct {
	// Org is the validated IAM organization.
	Org string `json:"org"`
	// Total is the total count of members in fc_members before filtering.
	Total int `json:"total"`
	// Members holds the matching enriched records.
	Members []MemberView `json:"members"`
	// Baseline names the deterministic risk scoring baseline.
	Baseline string `json:"baseline"`
}

// members reads fc_members from Cloud Base for the caller's validated IAM org.
func (s *service) members(ctx context.Context, in *MembersIn) (*MembersOut, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	all, err := s.loadMembers(ctx, c.Org)
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(in.Q))
	if len(q) > 80 {
		q = q[:80]
	}
	filter := strings.ToLower(strings.TrimSpace(in.Filter))
	matched := make([]MemberView, 0, len(all))
	for _, m := range all {
		if q != "" && !strings.Contains(strings.ToLower(m.Name+" "+m.Tier+" "+m.Segment+" "+m.MemberID), q) {
			continue
		}
		if filter == "high" && m.Score.Priority != "High" {
			continue
		}
		if filter == "consented" && !m.Consent {
			continue
		}
		matched = append(matched, m)
	}
	return &MembersOut{
		Org:      c.Org,
		Total:    len(all),
		Members:  matched,
		Baseline: "deterministic-rules-v1",
	}, nil
}

// ProposalsOut lists human-review proposals in Cloud Base fc_proposals.
type ProposalsOut struct {
	// Org is the validated IAM organization.
	Org string `json:"org"`
	// Proposals lists proposals newest first.
	Proposals []ProposalView `json:"proposals"`
}

// proposals lists all proposals stored in Cloud Base fc_proposals for the
// caller's validated IAM org.
func (s *service) proposals(ctx context.Context, _ *NoIn) (*ProposalsOut, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	members, err := s.loadMembers(ctx, c.Org)
	if err != nil {
		return nil, err
	}
	props, err := s.loadProposals(ctx, c.Org, members)
	if err != nil {
		return nil, err
	}
	return &ProposalsOut{Org: c.Org, Proposals: props}, nil
}

// PlanProposalsIn generates simulation proposals for a named journey.
type PlanProposalsIn struct {
	// Journey is "retention", "return", or "growth".
	Journey string `json:"journey"`
	// Limit caps how many new proposals are created (1..15, default 6).
	Limit int `json:"limit"`
}

// PlanProposalsOut summarizes generated human-review proposals.
type PlanProposalsOut struct {
	// Created is the count of new Pending proposals written to Cloud Base.
	Created int `json:"created"`
	// Eligible is the count of consented Active members matching the journey rules.
	Eligible int `json:"eligible"`
	// Mode is always "simulation-only".
	Mode string `json:"mode"`
}

// planProposals creates Pending simulation-only proposals in Cloud Base
// fc_proposals for eligible consented Active members in the caller's IAM org.
func (s *service) planProposals(ctx context.Context, in *PlanProposalsIn) (*PlanProposalsOut, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	journey, ok := findJourney(strings.TrimSpace(in.Journey))
	if !ok {
		return nil, zip.ErrBadRequest("unknown journey; choose retention, return, or growth")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 6
	}
	limit = clampInt(limit, 1, 15)

	unlock := s.lockOrg(c.Org)
	defer unlock()

	members, err := s.loadMembers(ctx, c.Org)
	if err != nil {
		return nil, err
	}
	existingProps, err := s.loadProposals(ctx, c.Org, members)
	if err != nil {
		return nil, err
	}
	pendingSet := map[string]bool{}
	for _, p := range existingProps {
		if p.Status == "Pending" {
			pendingSet[p.MemberID+"|"+p.Journey] = true
		}
	}

	sort.Slice(members, func(i, j int) bool { return members[i].MemberID < members[j].MemberID })

	eligible := 0
	created := 0
	nowISO := s.now().UTC().Format(time.RFC3339)
	actor := actorLabel(c)

	for _, m := range members {
		if !m.Consent || m.Status != "Active" {
			continue
		}
		matches := false
		action := ""
		switch journey.ID {
		case "retention":
			matches = m.Score.Risk >= 65
			action = "Review renewal check-in"
		case "return":
			matches = m.Visits90d <= 2
			action = "Suggest second experience"
		case "growth":
			matches = m.Engagement > 72 && m.Tier == "Core"
			action = "Review appropriate upgrade"
		}
		if !matches {
			continue
		}
		eligible++
		if created >= limit {
			continue
		}
		if pendingSet[m.MemberID+"|"+journey.ID] {
			continue
		}
		doc := ProposalDoc{
			MemberID:            m.MemberID,
			MemberName:          m.Name,
			Journey:             journey.ID,
			Action:              action,
			Reason:              strings.Join(m.Score.Reason, " · "),
			Status:              "Pending",
			Mode:                "simulation-only",
			ExternalSideEffects: false,
			CreatedBy:           actor,
			CreatedAt:           nowISO,
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return nil, zip.ErrInternal("encode proposal failed")
		}
		if _, err := s.deps.Store.Create(ctx, c.Org, CollectionProposals, raw); err != nil {
			return nil, err
		}
		pendingSet[m.MemberID+"|"+journey.ID] = true
		created++
	}

	if err := s.recordAudit(ctx, c.Org, actor, "journey.planned", journey.ID, map[string]any{
		"created":  created,
		"eligible": eligible,
		"mode":     "simulation-only",
	}); err != nil {
		return nil, err
	}

	return &PlanProposalsOut{
		Created:  created,
		Eligible: eligible,
		Mode:     "simulation-only",
	}, nil
}

// DecisionIn records a human approval or rejection on a pending proposal.
type DecisionIn struct {
	// ID is the Cloud Base record ID of the proposal in fc_proposals.
	ID string `json:"id"`
	// Action is "approve" or "reject".
	Action string `json:"action"`
}

// DecisionOut confirms the atomic proposal transition and simulation receipt.
type DecisionOut struct {
	// ID is the Cloud Base record ID of the decided proposal.
	ID string `json:"id"`
	// Status is "Approved (simulation)" or "Rejected".
	Status string `json:"status"`
	// Mode is always "simulation-only".
	Mode string `json:"mode"`
	// ExternalSideEffects is always false (no messages, billing, or CRM mutations).
	ExternalSideEffects bool `json:"externalSideEffects"`
	// TaskID is the Cloud Tasks durable simulation task ID minted on approval.
	TaskID string `json:"taskId,omitempty"`
	// DecidedBy is the IAM-validated admin who recorded the decision.
	DecidedBy string `json:"decidedBy"`
	// DecidedAt is the RFC3339 decision timestamp.
	DecidedAt string `json:"decidedAt"`
}

// decideProposal enforces org-admin / SuperAdmin role authorization, tenant
// ownership, consent, and atomic idempotency before updating fc_proposals in
// Cloud Base, enqueueing a durable simulation task in Cloud Tasks, and writing
// a provenance entry to fc_audit.
func (s *service) decideProposal(ctx context.Context, in *DecisionIn) (*DecisionOut, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	if !c.OrgAdmin && !c.Admin {
		return nil, zip.ErrForbidden("org admin role required to approve or reject proposals")
	}
	action := strings.ToLower(strings.TrimSpace(in.Action))
	if action != "approve" && action != "reject" {
		return nil, zip.ErrBadRequest("action must be approve or reject")
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, zip.ErrBadRequest("proposal id is required")
	}

	unlock := s.lockOrg(c.Org)
	defer unlock()

	rec, err := s.deps.Store.Get(ctx, c.Org, CollectionProposals, id)
	if err != nil {
		return nil, err
	}
	var prop ProposalDoc
	if err := json.Unmarshal(rec.Doc, &prop); err != nil {
		return nil, zip.ErrInternal("corrupt proposal document")
	}
	if prop.Status != "Pending" {
		return nil, zip.ErrConflict("proposal already resolved")
	}

	if action == "approve" {
		members, err := s.loadMembers(ctx, c.Org)
		if err != nil {
			return nil, err
		}
		var found *MemberView
		for i := range members {
			if members[i].MemberID == prop.MemberID {
				found = &members[i]
				break
			}
		}
		if found == nil || !found.Consent {
			return nil, zip.ErrForbidden("member marketing consent is required before approval")
		}
	}

	nowISO := s.now().UTC().Format(time.RFC3339)
	actor := actorLabel(c)
	var taskID string
	status := "Rejected"
	event := "action.rejected"

	if action == "approve" {
		status = "Approved (simulation)"
		event = "action.simulated"
		if s.deps.EnqueueTask != nil {
			payload, _ := json.Marshal(map[string]any{
				"proposal_id":           id,
				"member_id":             prop.MemberID,
				"journey":               prop.Journey,
				"action":                prop.Action,
				"approved_by":           actor,
				"mode":                  "simulation-only",
				"external_side_effects": false,
			})
			tid, err := s.deps.EnqueueTask(ctx, c.Org, SimulationTaskKind, payload)
			if err != nil {
				return nil, errUnavailable("failed to enqueue durable simulation task: " + err.Error())
			}
			taskID = tid
		}
	}

	prop.Status = status
	prop.DecidedBy = actor
	prop.DecidedAt = nowISO
	prop.TaskID = taskID
	prop.Mode = "simulation-only"
	prop.ExternalSideEffects = false

	raw, err := json.Marshal(prop)
	if err != nil {
		return nil, zip.ErrInternal("encode proposal failed")
	}
	if _, err := s.deps.Store.Update(ctx, c.Org, CollectionProposals, id, raw); err != nil {
		return nil, err
	}

	if err := s.recordAudit(ctx, c.Org, actor, event, id, map[string]any{
		"member_id":             prop.MemberID,
		"journey":               prop.Journey,
		"task_id":               taskID,
		"mode":                  "simulation-only",
		"external_side_effects": false,
	}); err != nil {
		return nil, err
	}

	return &DecisionOut{
		ID:                  id,
		Status:              status,
		Mode:                "simulation-only",
		ExternalSideEffects: false,
		TaskID:              taskID,
		DecidedBy:           actor,
		DecidedAt:           nowISO,
	}, nil
}

// AuditOut lists workspace audit entries from Cloud Base fc_audit.
type AuditOut struct {
	// Org is the validated IAM organization.
	Org string `json:"org"`
	// Events holds audit receipts newest first (up to 100 entries).
	Events []AuditView `json:"events"`
}

// auditList returns workspace provenance entries from Cloud Base fc_audit for
// the caller's validated IAM org.
func (s *service) auditList(ctx context.Context, _ *NoIn) (*AuditOut, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	recs, err := s.listAll(ctx, c.Org, CollectionAudit)
	if err != nil {
		return nil, err
	}
	events := make([]AuditView, 0, len(recs))
	for _, r := range recs {
		var doc AuditDoc
		if json.Unmarshal(r.Doc, &doc) != nil {
			continue
		}
		if doc.CreatedAt == "" {
			doc.CreatedAt = r.Created.UTC().Format(time.RFC3339)
		}
		events = append(events, AuditView{ID: r.ID, AuditDoc: doc})
	}
	sort.Slice(events, func(i, j int) bool { return events[i].CreatedAt > events[j].CreatedAt })
	if len(events) > 100 {
		events = events[:100]
	}
	return &AuditOut{Org: c.Org, Events: events}, nil
}

// EvaluationOut wraps the deterministic rules baseline evaluation.
type EvaluationOut struct {
	// Org is the validated IAM organization.
	Org string `json:"org"`
	// Evaluation holds the actually computed holdout metrics.
	Evaluation Evaluation `json:"evaluation"`
}

// evaluation computes holdout precision, recall, and accuracy of the
// deterministic rules baseline over the caller org's Cloud Base fc_members.
func (s *service) evaluation(ctx context.Context, _ *NoIn) (*EvaluationOut, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	members, err := s.loadMembers(ctx, c.Org)
	if err != nil {
		return nil, err
	}
	return &EvaluationOut{
		Org:        c.Org,
		Evaluation: computeEvaluation(members),
	}, nil
}

// ComponentStatus honestly reports one platform subsystem's current state.
type ComponentStatus struct {
	// Name identifies the Hanzo Cloud / OS capability.
	Name string `json:"name"`
	// Status is "Active", "Configured", "Unconfigured", or "Unqualified gate".
	Status string `json:"status"`
	// Detail explains what is live vs. gated.
	Detail string `json:"detail"`
}

// StackOut reports honest readiness for every Hanzo Cloud primitive backing FanClub.
type StackOut struct {
	// Org is the validated IAM organization.
	Org string `json:"org"`
	// Mode identifies the canonical Cloud-native staged subsystem mode.
	Mode string `json:"mode"`
	// Components lists each Hanzo primitive and its honest status.
	Components []ComponentStatus `json:"components"`
}

// stackStatus reports the live configuration posture of Hanzo Cloud primitives
// backing the caller's FanClub workspace.
func (s *service) stackStatus(ctx context.Context, _ *NoIn) (*StackOut, error) {
	c, err := requireCaller(ctx)
	if err != nil {
		return nil, err
	}
	infStatus := "Unconfigured"
	infDetail := "Set CLOUD_AI_BASE_URL (e.g. https://api.hanzo.ai or local Hanzo Engine) or CLOUD_AI_ZAP_ADDR; chat fails closed (503) when unconfigured."
	if s.deps.InferenceConfigured {
		infStatus = "Configured"
		infDetail = fmt.Sprintf("Cloud AIClient wired via %s transport (default model %q); configuration status only, outages fail closed (503).", s.deps.InferenceTransport, s.deps.DefaultModel)
	}
	taskStatus := "Active"
	taskDetail := "Org-admin approvals enqueue durable simulation-only tasks (fanclub_action_simulation, external_side_effects=false) in Cloud Tasks."
	if s.deps.EnqueueTask == nil {
		taskStatus = "Unconfigured"
		taskDetail = "Cloud Tasks queue is not mounted; approvals record simulation receipts in Cloud Base only."
	}

	return &StackOut{
		Org:  c.Org,
		Mode: "hanzo-cloud-native",
		Components: []ComponentStatus{
			{
				Name:   "Hanzo IAM",
				Status: "Active",
				Detail: fmt.Sprintf("JWT signature, issuer (%s), expiry, org isolation, and PKCE exchange enforced by Cloud SanitizeIdentity.", s.deps.IAMIssuer),
			},
			{
				Name:   "Hanzo Cloud Base",
				Status: "Active",
				Detail: "Durable records stored exclusively in encrypted Cloud Base collections fc_members, fc_proposals, and fc_audit scoped to IAM org.",
			},
			{
				Name:   "Hanzo Cloud Audit",
				Status: "Active",
				Detail: "Tamper-evident append-only Cloud audit trail covers all mutating REST and MCP calls alongside workspace receipts in fc_audit.",
			},
			{
				Name:   "Hanzo Cloud Tasks",
				Status: taskStatus,
				Detail: taskDetail,
			},
			{
				Name:   "Hanzo MCP (/mcp)",
				Status: "Active",
				Detail: "Typed zip operations (fanclub_summary, base_list, etc.) served over /mcp JSON-RPC 2.0 under the same IAM principal boundary; not yet a sandbox.",
			},
			{
				Name:   "Hanzo AI / Engine",
				Status: infStatus,
				Detail: infDetail,
			},
			{
				Name:   "Custom ML Train-to-Serve",
				Status: "Unqualified gate",
				Detail: "Licensed local LoRA/PEFT training, holdout evaluation, model custody, and independent Engine serving require separate qualification.",
			},
		},
	}, nil
}

func (s *service) listAll(ctx context.Context, org, collection string) ([]BaseRecord, error) {
	const pageSize = 500
	var out []BaseRecord
	for offset := 0; ; offset += pageSize {
		page, err := s.deps.Store.List(ctx, org, collection, pageSize, offset)
		if err != nil {
			return nil, err
		}
		out = append(out, page...)
		if len(page) < pageSize {
			break
		}
	}
	return out, nil
}

func (s *service) loadMembers(ctx context.Context, org string) ([]MemberView, error) {
	recs, err := s.listAll(ctx, org, CollectionMembers)
	if err != nil {
		return nil, err
	}
	out := make([]MemberView, 0, len(recs))
	for _, r := range recs {
		var doc MemberDoc
		if json.Unmarshal(r.Doc, &doc) != nil {
			continue
		}
		out = append(out, MemberView{
			RecordID:  r.ID,
			MemberDoc: doc,
			Score:     ScoreMember(doc),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *service) loadProposals(ctx context.Context, org string, members []MemberView) ([]ProposalView, error) {
	byMember := make(map[string]MemberView, len(members))
	for _, m := range members {
		byMember[m.MemberID] = m
	}
	recs, err := s.listAll(ctx, org, CollectionProposals)
	if err != nil {
		return nil, err
	}
	out := make([]ProposalView, 0, len(recs))
	for _, r := range recs {
		var doc ProposalDoc
		if json.Unmarshal(r.Doc, &doc) != nil {
			continue
		}
		pv := ProposalView{ID: r.ID, ProposalDoc: doc}
		if m, ok := byMember[doc.MemberID]; ok {
			pv.MemberName = m.Name
			pv.Consent = m.Consent
		}
		out = append(out, pv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out, nil
}

func (s *service) recordAudit(ctx context.Context, org, actor, event, subject string, details map[string]any) error {
	doc := AuditDoc{
		Actor:     actor,
		Event:     event,
		Subject:   subject,
		Details:   details,
		CreatedAt: s.now().UTC().Format(time.RFC3339),
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return zip.ErrInternal("encode audit entry failed")
	}
	_, err = s.deps.Store.Create(ctx, org, CollectionAudit, raw)
	return err
}
