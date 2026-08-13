package domain

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
	"github.com/hanzoai/cloud/internal/environ"
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

// routes registers the domain surface. Every op is TYPED: the input and the answer
// are Go types, so the schema, the prose, the MCP tool, the CLI command and every
// generated SDK method are projections of the handler itself. zipdoc lifts the doc
// comments into zipdoc_gen.go, which is the only way prose reaches the published
// registry — Go drops comments at compile time.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc
func routes(app cloud.Router, s *cloud.Service[state]) {
	g := app.Group("/v1/domain")
	// These ops mount AFTER commerce, whose /v1 error filter flattens a PROPAGATED
	// error to 500. cloud.Terminal writes an HTTPError in band instead, so a 402,
	// a 409 and the registrar's own 4xx reach the caller as themselves. Used here
	// as group middleware — Terminal over c.Continue is the same function doing
	// the same job one level out, which is what lets the leaves be typed ops.
	g.Use(zip.H(cloud.Terminal(func(c *zip.Ctx) error { return c.Continue() })))
	o := ops{s: s}

	zip.Get(g, "/health", o.health, zip.WithStatus(http.StatusOK, http.StatusServiceUnavailable))
	zip.Get(g, "/search", o.search)
	zip.Get(g, "/availability", o.availability)
	zip.Get(g, "/domains", o.list)
	zip.Post(g, "/register", o.register)
	zip.Post(g, "/renew", o.renew)
	zip.Post(g, "/transfer", o.transfer)
}

// ops binds the service to the typed ops. A TypedHandler takes no service
// parameter, so the service arrives as a RECEIVER and every op is a method value
// — also the only bound form cmd/zipdoc can lift prose from.
type ops struct{ s *cloud.Service[state] }

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
		Env: environ.Or("NAMECOM_ENV", "test"),
	}
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

// Capture records the debit for a registration/renewal/transfer.
//
// IT NAMES NO ACT, and the ledger mints the entry's own key. The three purchase sites used
// to name it after the DOMAIN — "domain:register:<name>", "domain:renew:<name>",
// "domain:transfer:<name>" — and that string became the idempotency key. But a ref names
// an ACT and a domain is not one: it is a thing that can be bought again. So the second
// renewal of a name replayed into the first renewal's entry and moved no money — a free
// year, every year, invisible to the spend cap because nothing was ever posted.
//
// A minted key is safe here because there is nothing to be exactly-once ABOUT: this is
// fire-and-forget and is never re-driven, and it runs only once the registrar has already
// confirmed — the two paths that must not charge (a refused Authorize, a registrar error)
// both return before reaching it.
func (m *meterBiller) Capture(org string, cents int64) {
	m.rm.MeterUsage(org, "domain.register", metering.Usage{
		Model:       "domain.register",
		AmountCents: cents,
	})
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
	if apiErr, ok := errors.AsType[*namecom.APIError](err); ok {
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

// noIn is the input of an op that takes nothing: no body, no path parameter, no
// query.
type noIn struct{}

// reachability is what this deployment can actually do about domains right now.
type reachability struct {
	// Service names the subsystem answering.
	Service string `json:"service"`
	// Registrar names the wholesale registrar behind it.
	Registrar string `json:"registrar"`
	// Env is the registrar environment. It is the fact that decides whether money
	// moves: only "prod" reaches the live, billable registrar — anything else,
	// including unset, is the sandbox.
	Env string `json:"env"`
	// Status is "ok" when a live call succeeded, else "degraded".
	Status string `json:"status"`
	// Configured is whether the wholesale credentials are present at all.
	Configured bool `json:"configured"`
	// Reachable is whether the registrar accepted those credentials on a live call
	// made while the caller waited.
	Reachable bool `json:"reachable"`
	// Error is the blocker, so an operator reads it instead of guessing at it.
	Error string `json:"error,omitempty"`
}

// StatusCode is 200 when this deployment can sell domains and 503 when it cannot.
// The body is the answer either way — the reason IS the payload, so it rides the
// refusal rather than being replaced by an error envelope.
func (r *reachability) StatusCode() int {
	if r.Status == "ok" {
		return http.StatusOK
	}
	return http.StatusServiceUnavailable
}

// health reports registrar reachability honestly: ok only when the wholesale
// credentials are present AND name.com accepted them on a live call made while you
// waited.
//
// Missing credentials or an unreachable registrar is 503 carrying configured,
// reachable and the reason, so an operator reads the blocker instead of guessing at
// it. It takes no principal, like every subsystem health probe.
func (o ops) health(ctx context.Context, _ *noIn) (*reachability, error) {
	s := o.s
	res := &reachability{Service: "domain", Registrar: "name.com", Env: s.State.svc.Env(), Status: "degraded"}
	if !s.State.reg.Configured() {
		res.Error = "registrar credentials not set (NAMECOM_USER/NAMECOM_TOKEN)"
		return res, nil
	}
	res.Configured = true
	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if _, err := s.State.reg.Hello(probeCtx); err != nil {
		res.Error = err.Error()
		return res, nil
	}
	res.Status, res.Reachable = "ok", true
	return res, nil
}

// quoteList is a set of priced availability results.
type quoteList struct {
	// Results is one quote per name, priced RETAIL — this deployment's markup is
	// already applied and the wholesale cost is never on the wire.
	Results []Offer `json:"results"`
}

// searchQuery asks for buyable names built from a keyword.
type searchQuery struct {
	// Q is the keyword to build names from. It is required.
	Q string `json:"q" validate:"required"`
	// TLD narrows the search to a comma-separated set of top-level domains.
	TLD string `json:"tld"`
}

// search finds names built from the keyword q, plus the registrar's alternate-TLD
// suggestions, and answers a quote for each: the name, whether it is purchasable,
// whether it is premium, the first-term and renewal price in cents, and the TLD.
//
// Prices are RETAIL — this deployment's markup is already applied and the wholesale
// cost is never on the wire.
//
// It requires a validated principal; 403 without one. Nothing is charged and
// nothing is held — a quote is not a reservation, and the price is re-quoted at
// purchase, so a name quoted here can be gone or dearer by the time you buy it. A
// deployment with no registrar credentials answers 503.
func (o ops) search(ctx context.Context, in *searchQuery) (*quoteList, error) {
	if _, err := principal.RequireOrg(ctx); err != nil {
		return nil, err
	}
	q := strings.TrimSpace(in.Q)
	if q == "" {
		return nil, zip.ErrBadRequest("q (keyword) is required")
	}
	var tlds []string
	for t := range strings.SplitSeq(strings.TrimSpace(in.TLD), ",") {
		if t = strings.TrimSpace(t); t != "" {
			tlds = append(tlds, t)
		}
	}
	quotes, err := o.s.State.svc.Search(ctx, q, tlds...)
	if err != nil {
		return nil, statusErr(err)
	}
	return &quoteList{Results: quotes}, nil
}

// availabilityQuery asks about names the caller already has in mind.
type availabilityQuery struct {
	// Domain is one name, or several comma-separated, to check in one call. Names
	// are lowercased. It is required.
	Domain string `json:"domain" validate:"required"`
}

// availability checks exact names rather than searching for them, and answers the
// same quote shape search does — purchasable, premium, first-term and renewal price
// in cents.
//
// It requires a validated principal; 403 without one. Nothing is charged and
// nothing is held. A deployment with no registrar credentials answers 503.
func (o ops) availability(ctx context.Context, in *availabilityQuery) (*quoteList, error) {
	if _, err := principal.RequireOrg(ctx); err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(in.Domain)
	if raw == "" {
		return nil, zip.ErrBadRequest("domain is required (comma-separate for multiple)")
	}
	var names []string
	for n := range strings.SplitSeq(raw, ",") {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			names = append(names, n)
		}
	}
	quotes, err := o.s.State.svc.Availability(ctx, names...)
	if err != nil {
		return nil, statusErr(err)
	}
	return &quoteList{Results: quotes}, nil
}

// holdings is what an org bought through this surface.
type holdings struct {
	// Domains is the caller org's domains, newest registration first.
	Domains []Holding `json:"domains"`
}

// list is the domains your org has bought here, newest registration first, each
// carrying the name, when it was registered, when it expires, what the org paid,
// the registrar order id and the nameservers it points at.
//
// Scoped to the validated principal's org — 403 without one, and there is no
// parameter that reaches another org's holdings.
//
// This is the deployment's OWN ownership record, not a query to the registrar: it
// lists what was bought THROUGH this surface, so a domain the org holds elsewhere
// is not here. The default store is in-process, so a deployment that has not
// swapped in a durable store answers from what this process registered.
func (o ops) list(ctx context.Context, _ *noIn) (*holdings, error) {
	org, err := principal.RequireOrg(ctx)
	if err != nil {
		return nil, err
	}
	recs, err := o.s.State.svc.ListByOrg(org)
	if err != nil {
		return nil, statusErr(err)
	}
	if recs == nil {
		recs = []Holding{}
	}
	return &holdings{Domains: recs}, nil
}

// order buys a domain for the caller's org.
type order struct {
	// Domain is the name to buy. It is required.
	Domain string `json:"domain" validate:"required"`
	// Years is the term to buy, defaulting to 1.
	Years int `json:"years"`
	// Contacts is the WHOIS contact set. Omit it and the registrar uses the
	// reseller account's default contacts.
	Contacts *namecom.Contacts `json:"contacts,omitempty"`
}

// register buys a domain for your org and answers the ownership record together
// with the quote it was bought at.
//
// The order of operations is the product guarantee: quote, refuse anything
// unpurchasable or unpriced, AUTHORIZE the org's prepaid balance, provision the
// authoritative zone in Hanzo DNS, register at the registrar already pointing at
// Hanzo's nameservers, and only then CAPTURE the charge and record ownership. A
// registrar failure therefore leaves the balance untouched — the org is never
// billed for a domain it did not get.
//
// It requires a validated principal; that principal's org owns the domain and is
// the ledger the charge lands on. Re-buying a name the org already holds is 409,
// not a second purchase.
//
// Refusals are distinct on purpose: 402 when the prepaid balance cannot cover the
// quoted price, 409 when the name is not available, 503 when the deployment has no
// registrar credentials, and the registrar's own message with its own 4xx — or 502
// for its 5xx — when it rejects the purchase. Zone provisioning is best-effort: if
// the zone service is down the domain is still registered against Hanzo's
// nameservers and the zone reconciles afterwards, rather than the purchase failing.
func (o ops) register(ctx context.Context, in *order) (*RegisterResult, error) {
	org, err := principal.RequireOrg(ctx)
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

// renewReq extends a domain the caller's org already owns.
type renewReq struct {
	// Domain is the name to extend. It is required, and the caller's org must hold it.
	Domain string `json:"domain" validate:"required"`
	// Years is how much longer to hold it, defaulting to 1.
	Years int `json:"years"`
}

// renew extends a domain your org already owns and answers the updated record with
// its new expiry alongside what was paid.
//
// Ownership is the gate: a name the caller's org does not hold is 404, so a renewal
// can never reach another tenant's domain.
//
// The price is re-quoted at the CURRENT renewal rate rather than the one paid at
// purchase. If the registrar returns no renewal price the org's original price is
// charged instead, so a renewal is never accidentally free. The balance is
// authorized before the registrar is called and captured after it confirms — 402
// when the prepaid balance cannot cover it, 503 when the deployment has no
// registrar credentials. Requires a validated principal.
func (o ops) renew(ctx context.Context, in *renewReq) (*RenewResult, error) {
	org, err := principal.RequireOrg(ctx)
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

// transferReq moves a domain the caller owns elsewhere onto their org here.
type transferReq struct {
	// Domain is the name to move in. It is required.
	Domain string `json:"domain" validate:"required"`
	// AuthCode is the transfer authorization the losing registrar issued. It is
	// required.
	AuthCode string `json:"authCode" validate:"required"`
	// Years is the term to buy on transfer, defaulting to 1.
	Years int `json:"years"`
}

// transfer moves a domain you own at another registrar onto your org here, using
// its authCode, and answers the same record-plus-quote a purchase does.
//
// It is priced and charged exactly like a registration: authorize the org's prepaid
// balance, ask the registrar for the transfer, capture only after the registrar
// accepts. A name the registrar will not price is 409, an insufficient balance is
// 402, and a deployment with no registrar credentials is 503.
//
// It requires a validated principal; the ownership record is written under that org
// as soon as the registrar ACCEPTS the request, which is not the same instant the
// transfer completes at the losing registrar. Unlike a registration this does not
// provision a zone, so the record carries this deployment's configured nameservers.
func (o ops) transfer(ctx context.Context, in *transferReq) (*RegisterResult, error) {
	org, err := principal.RequireOrg(ctx)
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
