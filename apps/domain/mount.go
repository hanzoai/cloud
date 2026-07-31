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
	"github.com/hanzoai/cloud/openapi"
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

func routes(app cloud.Router, s *cloud.Service[state]) {
	// Handlers mount AFTER commerce, whose /v1 error filter flattens a propagated
	// error to 500 — so wrap in Terminal to preserve real 4xx/402/409 statuses.
	h := func(fn func(*cloud.Service[state], *zip.Ctx) error) func(*zip.Ctx) error {
		return cloud.Terminal(cloud.Handle(s, fn))
	}
	g := app.Group("/v1/domain")
	g.Get("/health", cloud.Handle(s, health))
	g.Get("/search", h(searchHandler))
	g.Get("/availability", h(availabilityHandler))
	g.Get("/domains", h(listHandler))
	g.Post("/register", h(registerHandler))
	g.Post("/renew", h(renewHandler))
	g.Post("/transfer", h(transferHandler))
}

// The surface's prose, beside the route table so path and meaning are read (and
// changed) together. None of these seven is a typed op — every one of them binds
// its input by hand off the query string or the body — so there is no doc comment
// for zipdoc to lift, and without this each would publish an operationId and
// NOTHING else: an SDK method with no explanation and a CLI command with no help
// text. Declared through the same registry Register uses, so a description
// renders only while the router actually serves the route and this list can never
// invent a path.
func init() {
	openapi.Describe("/v1/domain/health", http.MethodGet,
		"Whether this deployment can actually sell domains, and why not when it cannot",
		"Reports registrar reachability honestly: `ok` only when the wholesale credentials are "+
			"present AND name.com accepted them on a live call made while you waited. Missing "+
			"credentials or an unreachable registrar is 503 carrying `configured`, `reachable` and "+
			"the reason, so an operator reads the blocker instead of guessing at it. Takes no "+
			"principal, like every subsystem health probe. The answer also names the registrar "+
			"`env`, which is the fact that decides whether money moves: only `prod` reaches the "+
			"live, billable registrar — anything else, including unset, is the sandbox.")

	openapi.Describe("/v1/domain/search", http.MethodGet,
		"Buyable names for a keyword, priced",
		"Searches the registrar for names built from the keyword `q`, plus its alternate-TLD "+
			"suggestions, and answers a quote for each: the name, whether it is purchasable, "+
			"whether it is premium, the first-term and renewal price in cents, and the TLD. Prices "+
			"are RETAIL — this deployment's markup is already applied and the wholesale cost is "+
			"never on the wire. Narrow the TLDs with a comma-separated `tld`; `q` is required and "+
			"its absence is 400.\n\n"+
			"Requires a validated principal; 403 without one. Nothing is charged and nothing is "+
			"held — a quote is not a reservation, and the price is re-quoted at purchase, so a name "+
			"quoted here can be gone or dearer by the time you buy it. A deployment with no "+
			"registrar credentials answers 503.")

	openapi.Describe("/v1/domain/availability", http.MethodGet,
		"Availability and price for names you already have in mind",
		"Checks exact names rather than searching for them, and answers the same quote shape "+
			"search does — purchasable, premium, first-term and renewal price in cents. Pass "+
			"`domain` with one name or several comma-separated to check them in one call; names "+
			"are lowercased. An empty `domain` is 400.\n\n"+
			"Requires a validated principal; 403 without one. Nothing is charged and nothing is "+
			"held. A deployment with no registrar credentials answers 503.")

	openapi.Describe("/v1/domain/domains", http.MethodGet,
		"The domains your org has bought here",
		"Lists the caller org's domains, newest registration first, each carrying the name, when "+
			"it was registered, when it expires, what the org paid, the registrar order id and the "+
			"nameservers it points at. Scoped to the validated principal's org — 403 without one, "+
			"and there is no parameter that reaches another org's holdings.\n\n"+
			"This is the deployment's OWN ownership record, not a query to the registrar: it lists "+
			"what was bought THROUGH this surface, so a domain the org holds elsewhere is not "+
			"here. The default store is in-process, so a deployment that has not swapped in a "+
			"durable store answers from what this process registered.")

	openapi.Describe("/v1/domain/register", http.MethodPost,
		"Buy a domain for your org — charged only once the registrar confirms",
		"Buys `domain` for `years` (default 1) and answers the ownership record together with the "+
			"quote it was bought at. The order of operations is the product guarantee: quote, "+
			"refuse anything unpurchasable or unpriced, AUTHORIZE the org's prepaid balance, "+
			"provision the authoritative zone in Hanzo DNS, register at the registrar already "+
			"pointing at Hanzo's nameservers, and only then CAPTURE the charge and record "+
			"ownership. A registrar failure therefore leaves the balance untouched — the org is "+
			"never billed for a domain it did not get.\n\n"+
			"Requires a validated principal; that principal's org owns the domain and is the ledger "+
			"the charge lands on. Re-buying a name the org already holds is 409, not a second "+
			"purchase. `contacts` is optional — omit it and the registrar uses the reseller "+
			"account's default WHOIS contacts.\n\n"+
			"Refusals are distinct on purpose: 402 when the prepaid balance cannot cover the "+
			"quoted price, 409 when the name is not available, 503 when the deployment has no "+
			"registrar credentials, and the registrar's own message with its own 4xx — or 502 for "+
			"its 5xx — when it rejects the purchase. Zone provisioning is best-effort: if the zone "+
			"service is down the domain is still registered against Hanzo's nameservers and the "+
			"zone reconciles afterwards, rather than the purchase failing.")

	openapi.Describe("/v1/domain/renew", http.MethodPost,
		"Extend a domain your org already owns",
		"Renews `domain` for `years` (default 1) and answers the updated record with its new "+
			"expiry alongside what was paid. Ownership is the gate: a name the caller's org does "+
			"not hold is 404, so a renewal can never reach another tenant's domain.\n\n"+
			"The price is re-quoted at the CURRENT renewal rate rather than the one paid at "+
			"purchase. If the registrar returns no renewal price the org's original price is "+
			"charged instead, so a renewal is never accidentally free. Balance is authorized before "+
			"the registrar is called and captured after it confirms — 402 when the prepaid balance "+
			"cannot cover it, 503 when the deployment has no registrar credentials. Requires a "+
			"validated principal.")

	openapi.Describe("/v1/domain/transfer", http.MethodPost,
		"Move a domain you own at another registrar onto your org here",
		"Transfers `domain` in using its `authCode` — both required, 400 otherwise — for `years` "+
			"(default 1), and answers the same record-plus-quote a purchase does. It is priced and "+
			"charged exactly like a registration: authorize the org's prepaid balance, ask the "+
			"registrar for the transfer, capture only after the registrar accepts. A name the "+
			"registrar will not price is 409, an insufficient balance is 402, and a deployment with "+
			"no registrar credentials is 503.\n\n"+
			"Requires a validated principal; the ownership record is written under that org as soon "+
			"as the registrar ACCEPTS the request, which is not the same instant the transfer "+
			"completes at the losing registrar. Unlike a registration this does not provision a "+
			"zone, so the record carries this deployment's configured nameservers.")
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

func searchHandler(s *cloud.Service[state], c *zip.Ctx) error {
	if _, ok := org(c); !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	q := strings.TrimSpace(c.Query("q"))
	if q == "" {
		return zip.ErrBadRequest("q (keyword) is required")
	}
	var tlds []string
	if raw := strings.TrimSpace(c.Query("tld")); raw != "" {
		for _, t := range strings.Split(raw, ",") {
			if t = strings.TrimSpace(t); t != "" {
				tlds = append(tlds, t)
			}
		}
	}
	quotes, err := s.State.svc.Search(c.Context(), q, tlds...)
	if err != nil {
		return statusErr(err)
	}
	return c.JSON(http.StatusOK, map[string]any{"results": quotes})
}

func availabilityHandler(s *cloud.Service[state], c *zip.Ctx) error {
	if _, ok := org(c); !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	raw := strings.TrimSpace(c.Query("domain"))
	if raw == "" {
		return zip.ErrBadRequest("domain is required (comma-separate for multiple)")
	}
	var names []string
	for _, n := range strings.Split(raw, ",") {
		if n = strings.ToLower(strings.TrimSpace(n)); n != "" {
			names = append(names, n)
		}
	}
	quotes, err := s.State.svc.Availability(c.Context(), names...)
	if err != nil {
		return statusErr(err)
	}
	return c.JSON(http.StatusOK, map[string]any{"results": quotes})
}

func listHandler(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	recs, err := s.State.svc.ListByOrg(o)
	if err != nil {
		return statusErr(err)
	}
	if recs == nil {
		recs = []Record{}
	}
	return c.JSON(http.StatusOK, map[string]any{"domains": recs})
}

type registerReq struct {
	Domain   string            `json:"domain"`
	Years    int               `json:"years"`
	Contacts *namecom.Contacts `json:"contacts,omitempty"`
}

func registerHandler(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	var body registerReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Domain) == "" {
		return zip.ErrBadRequest("domain is required")
	}
	res, err := s.State.svc.Register(c.Context(), o, body.Domain, body.Years, body.Contacts)
	if err != nil {
		return statusErr(err)
	}
	return c.JSON(http.StatusOK, res)
}

type renewReq struct {
	Domain string `json:"domain"`
	Years  int    `json:"years"`
}

func renewHandler(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	var body renewReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Domain) == "" {
		return zip.ErrBadRequest("domain is required")
	}
	res, err := s.State.svc.Renew(c.Context(), o, body.Domain, body.Years)
	if err != nil {
		return statusErr(err)
	}
	return c.JSON(http.StatusOK, res)
}

type transferReq struct {
	Domain   string `json:"domain"`
	AuthCode string `json:"authCode"`
	Years    int    `json:"years"`
}

func transferHandler(s *cloud.Service[state], c *zip.Ctx) error {
	o, ok := org(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	var body transferReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Domain) == "" || strings.TrimSpace(body.AuthCode) == "" {
		return zip.ErrBadRequest("domain and authCode are required")
	}
	res, err := s.State.svc.Transfer(c.Context(), o, body.Domain, body.AuthCode, body.Years)
	if err != nil {
		return statusErr(err)
	}
	return c.JSON(http.StatusOK, res)
}
