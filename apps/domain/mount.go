package domain

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/domain/namecom"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Mount wires Hanzo Domains onto the unified cloud binary as /v1/domain/*:
//
//	GET  /v1/domain/health                      registrar reachability (no auth)
//	GET  /v1/domain/search?q=&tld=              keyword search + alternate TLDs (priced)
//	GET  /v1/domain/availability?domain=a,b     exact-name availability + pricing
//	GET  /v1/domain/domains                     the org's registered domains
//	POST /v1/domain/register {domain,years,contacts?}   buy (billed)
//	POST /v1/domain/renew    {domain,years}             renew (billed)
//	POST /v1/domain/transfer {domain,authCode,years}    transfer-in (billed)
//
// Every mutating route is org-scoped: a validated principal's org owns the purchase
// and is the ledger the charge lands on. The registrar's wholesale credentials come
// from the platform secret store (KMS) via the operator-injected env NAMECOM_USER /
// NAMECOM_TOKEN — never hard-coded, exactly as clients/sites reads CF_API_TOKEN.
func Mount(app cloud.Router, deps cloud.Deps) error {
	return cloud.Mount(app, deps, "domain", buildState, routes)
}

// state is the subsystem's data: the orchestrator plus the raw registrar (for the
// health probe's Hello) and a logger.
type state struct {
	svc *Service
	reg Registrar
	log luxlog.Logger
}

func buildState(b cloud.Base) (state, error) {
	cfg := configFromEnv()
	reg := namecom.New(
		strings.TrimSpace(os.Getenv("NAMECOM_USER")),
		strings.TrimSpace(os.Getenv("NAMECOM_TOKEN")),
		cfg.Env, nil,
	)
	biller := &meterBiller{rm: b.Bill}
	zones := &hanzodnsZones{
		base: strings.TrimRight(strings.TrimSpace(os.Getenv("HANZO_DNS_URL")), "/"),
		ns:   cfg.Nameservers,
		http: &http.Client{Timeout: 10 * time.Second},
		log:  b.Log,
	}
	svc := NewService(reg, biller, zones, NewMemStore(), cfg)
	b.Log.Info("hanzo domains ready",
		"registrar", "name.com",
		"env", cfg.Env,
		"configured", reg.Configured(),
		"nameservers", strings.Join(cfg.Nameservers, ","),
		"markup", cfg.Markup.Multiplier,
	)
	return state{svc: svc, reg: reg, log: b.Log}, nil
}

// ops binds the domain state to the typed handlers. A zip TypedHandler is
// func(context.Context, *In) (*Out, error) — no parameter for the service — so the
// service arrives as a RECEIVER and every op is a method value.
type ops struct{ s *cloud.Service[state] }

func routes(app cloud.Router, s *cloud.Service[state]) {
	o := ops{s: s}
	z := cloud.ZipApp(app)
	g := app.Group("/v1/domain")
	// The bridge FIRST — fiber runs middleware in registration order, so one
	// installed after these leaves would never run, and every op below resolves its
	// tenant through the request it parks. Terminal rides with it because these
	// handlers mount AFTER commerce, whose /v1 error filter flattens a propagated
	// error to 500: without it the real 4xx/402/409 statuses are lost.
	g.Use(cloud.Bridge(), terminal())

	// health stays an untyped handler: its 503 answer carries a BODY (status,
	// configured, reachable, error) that callers read, and a typed op has exactly
	// one response object — the success one — so typing it would replace that body
	// with zip's fixed error shape. Documented in prose, not in the registry.
	g.Get("/health", cloud.Handle(s, health))

	zip.Get(z, "/v1/domain/search", o.search)
	zip.Get(z, "/v1/domain/availability", o.availability)
	zip.Get(z, "/v1/domain/domains", o.list)
	zip.Post(z, "/v1/domain/register", o.register)
	zip.Post(z, "/v1/domain/renew", o.renew)
	zip.Post(z, "/v1/domain/transfer", o.transfer)
}

// ── config ───────────────────────────────────────────────────────────────────────

func configFromEnv() Config {
	return Config{
		Markup: Markup{
			Multiplier:     floatEnv("DOMAIN_MARKUP", 1.15),
			MinMarginCents: intEnv("DOMAIN_MIN_MARGIN_CENTS", 300),
		},
		Nameservers: nsEnv("HANZO_NAMESERVERS", []string{"ns1.hanzo.ai", "ns2.hanzo.ai"}),
		// The registrar env is EXPLICIT and fail-safe: only "prod" hits the live,
		// billable registrar; anything else (incl. unset) is the sandbox.
		Env: envOr("NAMECOM_ENV", "test"),
	}
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func floatEnv(key string, def float64) float64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func intEnv(key string, def int64) int64 {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func nsEnv(key string, def []string) []string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return def
}

// ── billing adapter (ResourceMeter → Biller) ──────────────────────────────────────

// meterBiller adapts cloud's ResourceMeter to the Biller interface: Gate is the
// pre-charge balance authorize, Meter is the debit capture. This is the exact
// deposit→charge seam every metered subsystem uses.
type meterBiller struct{ rm *cloud.ResourceMeter }

func (m *meterBiller) Authorize(ctx context.Context, org string, cents int64) error {
	// ("", false): no project sub-scope on a domain purchase — org- and
	// service-scoped caps apply; a domain buy is not project-attributed.
	err := m.rm.Gate(ctx, org, "", false, "domain.register", cents)
	if errors.Is(err, metering.ErrInsufficientBalance) {
		return ErrInsufficientFunds
	}
	return err
}

func (m *meterBiller) Capture(org string, cents int64, ref string) {
	m.rm.Meter(org, "", "domain.register", cents, ref, "")
}

// ── DNS adapter (hanzoai/dns) ──────────────────────────────────────────────────────

// hanzodnsZones ensures an authoritative zone exists in hanzoai/dns for a purchased
// domain and reports the Hanzo nameservers to point it at. When HANZO_DNS_URL is
// unset it is a no-op that still returns the nameservers, so a registration always
// points at Hanzo's NS even before the zone control plane is wired in an environment.
type hanzodnsZones struct {
	base string
	ns   []string
	http *http.Client
	log  luxlog.Logger
}

func (z *hanzodnsZones) EnsureZone(ctx context.Context, org, domainName string) ([]string, error) {
	if z.base == "" {
		return z.ns, nil
	}
	payload, _ := json.Marshal(map[string]any{"zone": domainName, "orgId": org})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, z.base+"/v1/dns/zones", bytes.NewReader(payload))
	if err != nil {
		return z.ns, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	resp, err := z.http.Do(req)
	if err != nil {
		z.log.Warn("hanzodns ensure-zone failed (registering against Hanzo NS anyway)", "domain", domainName, "err", err)
		return z.ns, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusConflict {
		z.log.Warn("hanzodns ensure-zone non-2xx (continuing)", "domain", domainName, "status", resp.StatusCode)
		return z.ns, errors.New("hanzodns: status " + strconv.Itoa(resp.StatusCode))
	}
	return z.ns, nil
}

// ── handlers ───────────────────────────────────────────────────────────────────────

func org(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// statusErr maps a core sentinel / registrar error to a zip HTTP status.
func statusErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, ErrNotConfigured):
		return zip.Errorf(http.StatusServiceUnavailable, "domain registration is not configured on this deployment")
	case errors.Is(err, ErrInsufficientFunds):
		return zip.Errorf(http.StatusPaymentRequired, "insufficient balance — add credits to buy this domain")
	case errors.Is(err, ErrUnavailable):
		return zip.ErrConflict("that domain is not available to register")
	case errors.Is(err, ErrAlreadyOwned):
		return zip.ErrConflict("your org already owns that domain")
	case errors.Is(err, ErrNotOwned):
		return zip.ErrNotFound("your org does not own that domain")
	}
	var apiErr *namecom.APIError
	if errors.As(err, &apiErr) {
		// Surface the registrar's own message; a 4xx from the registrar is a client
		// problem, a 5xx a bad-gateway.
		status := http.StatusBadGateway
		if apiErr.Status >= 400 && apiErr.Status < 500 {
			status = apiErr.Status
		}
		return zip.Errorf(status, "registrar: %s", apiErr.Message)
	}
	return zip.Errorf(http.StatusInternalServerError, "%v", err)
}

// health probes registrar reachability. Public (like every subsystem health) and
// honest: it reports whether credentials are present and, if so, whether name.com
// accepts them (the current go-live blocker surfaces here as ok:false + the reason).
// terminal is cloud.Terminal as MIDDLEWARE. Terminal wraps ONE handler, but the
// typed ops are registered on the App rather than wrapped one by one, so the guard
// has to sit in the chain instead: it lets the rest of the chain run and flattens
// whatever error comes back, exactly as the per-handler form did.
func terminal() zip.Handler {
	return func(c *zip.Ctx) error {
		return cloud.Terminal(func(*zip.Ctx) error { return c.Continue() })(c)
	}
}

func health(s *cloud.Service[state], c *zip.Ctx) error {
	res := map[string]any{"service": "domain", "registrar": "name.com", "env": s.State.svc.Env()}
	if !s.State.reg.Configured() {
		res["status"], res["configured"] = "degraded", false
		res["error"] = "registrar credentials not set (NAMECOM_USER/NAMECOM_TOKEN)"
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	res["configured"] = true
	ctx, cancel := context.WithTimeout(c.Context(), 8*time.Second)
	defer cancel()
	if _, err := s.State.reg.Hello(ctx); err != nil {
		res["status"], res["reachable"] = "degraded", false
		res["error"] = err.Error()
		return c.JSON(http.StatusServiceUnavailable, res)
	}
	res["status"], res["reachable"] = "ok", true
	return c.JSON(http.StatusOK, res)
}

// searchQuery asks the registrar for names built from a keyword.
type searchQuery struct {
	// Q is the keyword to build candidate names from. Required.
	Q string `json:"q"`
	// TLD optionally restricts the search to these TLDs, comma-separated.
	TLD string `json:"tld"`
}

// offerList is the priced-result envelope search and availability share.
type offerList struct {
	// Results are the priced availability answers, one per candidate name.
	Results []Offer `json:"results"`
}

// search asks the registrar for available names built from a keyword. Each
// candidate is priced at the customer rate, nothing is reserved or charged, and
// TLD narrows the candidates to a comma-separated list.
//
// Example: {"q": "hanzo", "tld": "ai,com"}
func (o ops) search(ctx context.Context, in *searchQuery) (*offerList, error) {
	if _, err := o.begin(ctx); err != nil {
		return nil, err
	}
	q := strings.TrimSpace(in.Q)
	if q == "" {
		return nil, zip.ErrBadRequest("q (keyword) is required")
	}
	var tlds []string
	if raw := strings.TrimSpace(in.TLD); raw != "" {
		for _, t := range strings.Split(raw, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tlds = append(tlds, t)
			}
		}
	}
	quotes, err := o.s.State.svc.Search(ctx, q, tlds...)
	if err != nil {
		return nil, statusErr(err)
	}
	return &offerList{Results: quotes}, nil
}

// availabilityQuery checks exact names rather than searching for candidates.
type availabilityQuery struct {
	// Domain is the exact name to check, comma-separated for several. Required.
	Domain string `json:"domain"`
}

// availability checks exact domain names and prices each at the customer rate.
// Comma-separate the domain value to check several at once; nothing is reserved
// or charged.
//
// Example: {"domain": "hanzo.ai,hanzo.dev"}
func (o ops) availability(ctx context.Context, in *availabilityQuery) (*offerList, error) {
	if _, err := o.begin(ctx); err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(in.Domain)
	if raw == "" {
		return nil, zip.ErrBadRequest("domain is required (comma-separate for multiple)")
	}
	var names []string
	for _, n := range strings.Split(raw, ",") {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			names = append(names, n)
		}
	}
	quotes, err := o.s.State.svc.Availability(ctx, names...)
	if err != nil {
		return nil, statusErr(err)
	}
	return &offerList{Results: quotes}, nil
}

// recordList is the owned-domain envelope.
type recordList struct {
	// Domains are the domains this org owns, with what each cost and when it expires.
	Domains []Ownership `json:"domains"`
}

// list lists the domains the caller's org owns. Each carries the price paid and the
// expiry on record, and org is the bound isolation boundary, so another tenant's
// domains are never returned.
//
// Response: {"domains": [{"org": "acme", "domain": "hanzo.ai", "registeredAt": 1780000000, "expiresAt": "2027-07-29T00:00:00Z", "priceCents": 8050, "costCents": 7000, "nameservers": ["ns1.hanzo.ai", "ns2.hanzo.ai"]}]}
func (o ops) list(ctx context.Context, _ *struct{}) (*recordList, error) {
	org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	recs, err := o.s.State.svc.ListByOrg(org)
	if err != nil {
		return nil, statusErr(err)
	}
	if recs == nil {
		recs = []Ownership{}
	}
	return &recordList{Domains: recs}, nil
}

type purchaseReq struct {
	// Domain is the exact name to buy. Required.
	Domain string `json:"domain"`
	// Years is the registration term; 0 means one year.
	Years int `json:"years"`
	// Contacts are the WHOIS contacts to register under; omitted uses the
	// reseller account's defaults.
	Contacts *namecom.Contacts `json:"contacts,omitempty"`
}

// register buys a domain for the caller's org. The customer is charged only after
// the registrar confirms, the zone is provisioned and the name points at Hanzo
// nameservers, so a registrar failure leaves the balance untouched.
//
// Example: {"domain": "hanzo.ai", "years": 1}
func (o ops) register(ctx context.Context, in *purchaseReq) (*RegisterResult, error) {
	org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Domain) == "" {
		return nil, zip.ErrBadRequest("domain is required")
	}
	res, err := o.s.State.svc.Register(ctx, org, in.Domain, in.Years, in.Contacts)
	if err != nil {
		return nil, statusErr(err)
	}
	return res, nil
}

type renewReq struct {
	// Domain is the name to extend; the caller's org must already own it. Required.
	Domain string `json:"domain"`
	// Years is how many years to add; 0 means one year.
	Years int `json:"years"`
}

// renew extends a domain the caller's org already owns. The renewal is re-quoted
// at the current price before charging, and the stored expiry advances only after
// the registrar confirms.
//
// Example: {"domain": "hanzo.ai", "years": 2}
func (o ops) renew(ctx context.Context, in *renewReq) (*RenewResult, error) {
	org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Domain) == "" {
		return nil, zip.ErrBadRequest("domain is required")
	}
	res, err := o.s.State.svc.Renew(ctx, org, in.Domain, in.Years)
	if err != nil {
		return nil, statusErr(err)
	}
	return res, nil
}

type transferReq struct {
	// Domain is the name to transfer in. Required.
	Domain string `json:"domain"`
	// AuthCode is the EPP authorization code from the losing registrar. Required.
	AuthCode string `json:"authCode"`
	// Years is the term to add on transfer; 0 means one year.
	Years int `json:"years"`
}

// transfer brings a domain registered elsewhere into the caller's org. It needs
// the EPP authorization code from the losing registrar, and it prices and records
// ownership exactly as a purchase does.
//
// Example: {"domain": "hanzo.ai", "authCode": "aG9sZGVy", "years": 1}
func (o ops) transfer(ctx context.Context, in *transferReq) (*RegisterResult, error) {
	org, err := o.begin(ctx)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(in.Domain) == "" || strings.TrimSpace(in.AuthCode) == "" {
		return nil, zip.ErrBadRequest("domain and authCode are required")
	}
	res, err := o.s.State.svc.Transfer(ctx, org, in.Domain, in.AuthCode, in.Years)
	if err != nil {
		return nil, statusErr(err)
	}
	return res, nil
}

// begin resolves the caller's org. The org NEVER comes from an In field — an In
// field is caller-supplied, so a tenant key read from one is a cross-tenant read
// the caller asserted for itself. It comes from the request cloud.Bridge parked;
// off the HTTP path there is none, so the op refuses.
func (o ops) begin(ctx context.Context) (string, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	v, ok := org(c)
	if !ok {
		return "", zip.ErrForbidden("a validated principal is required")
	}
	return v, nil
}
