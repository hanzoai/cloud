package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/internal/host"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// zipdoc lifts the doc comment off each typed op and its In/Out fields into
// zipdoc_gen.go, which is the ONLY way that prose reaches the published document
// and the MCP tool list — Go drops comments at compile time. Run by
// `make -C apps/gateway openapi` and by the Dockerfile before every build.
//
//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// state is gateway's own data; shared deps live in the embedded cloud.Base.
type state struct {
	store   *edge.Store
	traffic *edge.Traffic
	// trail is the audit trail a SuperAdmin's change to a cap — an org's org_cap or
	// the platform's org_rpm — is recorded on before it is made (audited).
	trail *audit.Recorder
}

// ops binds the mounted Service so each op can be a method value — the only bound
// form cmd/zipdoc can lift prose from. It carries STATE and no logic.
type ops struct{ s *cloud.Service[state] }

// Mount wires /v1/gateway/config onto app over the shared policy store. The store
// is the host's, one per process (host.EdgePolicy), not a Base dep, so this constructs the Service value directly
// via cloud.NewBase rather than cloud.Use.
func Use(app cloud.Router, deps cloud.Deps) error {
	return mount(app, deps, host.EdgePolicy(cloud.LoadConfig()), host.Traffic())
}

// mount is Use over a policy store and a traffic sensor already in hand.
func mount(app cloud.Router, deps cloud.Deps, store *edge.Store, traffic *edge.Traffic) error {
	if app == nil {
		return errors.New("gateway: nil app")
	}
	if store == nil {
		return fmt.Errorf("gateway.Use:  nil policy store")
	}
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "gateway"),
		State: state{store: store, traffic: traffic, trail: deps.Audit},
	}

	routes(app, s)

	s.Log.Info("gateway config plane mounted", "prefix", "/v1/gateway")
	return nil
}

// routes registers the gateway config plane as TYPED ops — one registry entry per
// route, from which the REST route, the OpenAPI operation, the MCP tool, the CLI
// command and every generated SDK method follow.
func routes(app cloud.Router, s *cloud.Service[state]) {
	// The composer owns cloud.Bridge: the fused host installs it once at its root
	// and the plugin constructor does the same for a plugin program, so no
	// subsystem installs it.

	// Declared on the GROUP: the op's path is the prefix composed with the leaf,
	// which is the identity every projection keys on, and cmd/zipdoc resolves the
	// prefix the same way, so the prose below reaches the document and the tool list.
	o := ops{s: s}
	g := app.Group("/v1/gateway")
	g.Get("/config", o.read)
	g.Put("/config", o.write)
	g.Get("/traffic", o.traffic,
		zip.WithOperationID("gatewayTraffic"),
		zip.WithSummary("Report who is calling this org's API right now"),
		zip.WithTags("gateway"))
}

// caller is the ONE identity client both config ops ask. It returns the caller's
// VALIDATED org (principal.OrgFrom — never an input field, which is caller-supplied)
// together with the request, because this surface gates on two facts the org alone
// does not carry: SuperAdmin-ness (X-User-IsAdmin) and the ?org=<slug> a SuperAdmin
// targets another tenant with. It fails closed off the HTTP path, where there is no
// principal and no attested admin.
func caller(ctx context.Context) (*zip.Ctx, string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, "", err
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, "", zip.ErrForbidden("a validated principal is required")
	}
	return c, org, nil
}

// writer refuses a write to edge policy by anyone but a SIGNED-IN org admin of the
// org the request acts in, or a SuperAdmin, on a change its holder sent on purpose.
//
// EVERY FIELD HERE GOVERNS THE WHOLE ORG, and that is what made a validated org
// alone the wrong bar. org_rpm only ever tightens, which sounds safe and is the
// attack: any member, or any key, that may set it to 1 throttles every caller the
// org has. So it takes the authority billing's spend caps take (apps/billing
// orgAdmin) — the same two bits, minted by the identity boundary, never a client
// header — and the same anti-forgery question every change a browser session can
// make is asked (cloud.Intended): a cookie rides any request a page is made to
// send, and a sibling page must not set an org's ceiling with it.
//
// NO API KEY WRITES HERE, and that is the boundary's answer rather than this
// function's: a key resolves with no signed membership set, so authz reads it as a
// machine and mints neither admin bit for it, whoever holds it and whatever it may
// reach. Signing in is how an org admin changes the org's policy.
func writer(c *zip.Ctx) error {
	if err := cloud.Intended(c); err != nil {
		return err
	}
	if !principal.Super(c) && !principal.IsOrgAdmin(c) {
		return zip.ErrForbidden("only a signed-in org admin or SuperAdmin changes the gateway policy")
	}
	return nil
}

// target resolves the scope a call acts on: the caller's own validated org, or —
// for a SuperAdmin only — the scope named by ?org=. The override is read off the
// URL rather than modelled as an input field, because zip binds an input field from
// the BODY too and this route has never accepted an org there.
//
// A PRESENT but EMPTY ?org= names the scope that has no tenant — the anonymous
// lane, where every caller the identity boundary could not validate is counted.
// That lane had no spelling at all, so its traffic and its saturation were
// readable by nobody, SuperAdmin included: the only way to ask for a scope was to
// name it, and this one has no name. The empty value IS its name — it cannot
// collide with any tenant, and it needs no second query parameter to mean what
// "org" already means.
func target(c *zip.Ctx, org string) string {
	if !principal.Super(c) {
		return org
	}
	if q := c.Query("org"); q != "" {
		return q
	}
	if c.Fiber().Request().URI().QueryArgs().Has("org") {
		return anonymousLane
	}
	return org
}

// anonymousLane is the scope with no tenant, in both the policy store and the
// sensor: the empty org. Named here so the op that serves it and the store that
// resolves its mode say the same thing.
const anonymousLane = ""

// Read returns the EFFECTIVE edge policy the caller is subject to: the platform CORS
// allowlist and pre-auth per-IP flood cap in force, plus the caller's own authenticated
// rate ceiling, edge-cache TTLs and accepted-method allowlist. A SuperAdmin may inspect
// a specific tenant's effective policy with ?org=<slug>.
func (o ops) read(ctx context.Context, _ *cloud.Unit) (*edge.Policy, error) {
	c, org, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	p := o.s.State.store.Effective(target(c, org))
	return &p, nil
}

// Write updates one policy scope and returns the policy in force after the write.
// Writing requires a signed-in admin of the organization, or a SuperAdmin; a
// member and every API key are refused 403, and so is a browser session's change
// that its own page did not send.
// A body carrying any PLATFORM field (cors_origins, per_ip_rpm, window_sec) is a
// platform write and requires SuperAdmin; otherwise it is a per-org write (org_rpm,
// cache_ttl_sec, cache_paths, methods) scoped to the caller's own org — or, for a
// SuperAdmin, the tenant named by ?org=<slug>. A body that sets nothing is a 400.
// The abuse gate's mode and an organization's rate cap (org_cap) are OPERATOR
// fields: setting either requires SuperAdmin, whichever organization it lands on,
// and an org_cap change is recorded on the audit trail with the cap before and
// after (gateway.org_cap), or refused (503) when the trail cannot take it. The
// platform's org_rpm is every organization's default cap, so moving it is recorded
// the same way (gateway.default_cap). An
// organization's own org_rpm may lower its ceiling but not raise it past its cap. updated_at and updated_by are server-stamped; a client-supplied value is
// ignored.
//
// Example: {"org_rpm": 120, "cache_ttl_sec": 30, "methods": ["GET", "POST"]}
func (o ops) write(ctx context.Context, in *edge.Policy) (*edge.Policy, error) {
	c, org, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := writer(c); err != nil {
		return nil, err
	}
	in.UpdatedBy = c.User() // server-stamped; a client-supplied value is ignored.

	in.Normalize()

	// MODE IS NOT SELF-SERVICE. Every other field on this route is a tenant's own
	// preference about its own traffic: how fast it may call, what it caches, which
	// methods it accepts. Mode is not a preference — it is whether the platform's
	// abuse control ENFORCES against this tenant, and the tenant is the subject of
	// that control. Leaving it in the self-service branch meant an org admin, or
	// anyone holding an org-admin credential, could PUT {"mode":"shadow"} and turn
	// the defense off for exactly the account it was defending against; a stolen
	// key's first useful call is the one that disarms the thing watching it.
	//
	// Checked BEFORE the scope split, so it holds for the platform row and for a
	// tenant row alike, and stated as its own refusal so the answer names the actual
	// rule rather than "not found" or "nothing to set".
	if in.Mode != "" && !principal.Super(c) {
		return nil, zip.ErrForbidden("mode is set by the platform, not by the organization it governs")
	}
	// THE CAP IS NOT SELF-SERVICE EITHER, for the same reason: it is the most an
	// organization's ceiling may be, and every free signup administers an org of its
	// own, so a cap its admin could raise would be no cap.
	if in.OrgCap != 0 && !principal.Super(c) {
		return nil, zip.ErrForbidden("org_cap is set by the platform, not by the organization it governs")
	}

	platformWrite := len(in.CORSOrigins) > 0 || in.PerIPRPM > 0 || in.WindowSec > 0
	if platformWrite {
		if !principal.Super(c) {
			return nil, zip.ErrForbidden("platform policy (cors/per-IP) requires SuperAdmin")
		}
		if err := in.Validate(); err != nil {
			return nil, zip.ErrBadRequest(err.Error())
		}
		// A cap is an org's, set on the org's row and audited there; a platform write
		// carries none.
		in.OrgCap = 0
		var recs []*audit.Record
		if in.OrgRPM > 0 {
			r, err := o.audited(c, "gateway.default_cap", o.s.State.store.AdminOrg(), "org_rpm", o.s.State.store.Platform().OrgRPM, in.OrgRPM)
			if err != nil {
				return nil, err
			}
			recs = append(recs, r)
		}
		saved, err := o.s.State.store.PutPlatform(ctx, *in)
		if err != nil {
			o.s.Log.Warn("gateway platform policy write failed", "err", err)
			o.failed(ctx, recs)
			return nil, zip.Errorf(503, "policy store unavailable")
		}
		o.s.Log.Info("gateway platform policy updated", "by", in.UpdatedBy,
			"cors", len(saved.CORSOrigins), "per_ip_rpm", saved.PerIPRPM, "window_sec", saved.WindowSec)
		return &saved, nil
	}

	// Per-org scope: the tenant's OWN edge config — rate ceiling, cache policy,
	// method allowlist, plus the operator-only mode already gated above. Only these
	// fields are forwarded, so a tenant can never smuggle a platform knob into its
	// own row. Scoped to the caller's org (never a body-supplied org); a SuperAdmin
	// may target a specific tenant with ?org=<slug>.
	orgCfg := edge.Policy{
		OrgRPM:      in.OrgRPM,
		OrgCap:      in.OrgCap,
		CacheTTLSec: in.CacheTTLSec,
		CachePaths:  in.CachePaths,
		Methods:     in.Methods,
		Mode:        in.Mode,
		UpdatedBy:   in.UpdatedBy,
	}
	if orgCfg.OrgRPM <= 0 && orgCfg.OrgCap <= 0 && orgCfg.CacheTTLSec <= 0 && len(orgCfg.CachePaths) == 0 && len(orgCfg.Methods) == 0 && orgCfg.Mode == "" {
		return nil, zip.ErrBadRequest("nothing to set: provide org_rpm/cache_ttl_sec/cache_paths/methods, mode/org_cap (SuperAdmin), or cors_origins/per_ip_rpm/window_sec (SuperAdmin)")
	}
	if err := orgCfg.Validate(); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	tgt := target(c, org)
	// An organization lowers its own ceiling and never raises it past its cap. The
	// limiter clips a ceiling to the cap whatever is stored (ScopeRateLimit), so this
	// is the answer a tenant can act on, said at the write, not a second rule.
	if capped := o.s.State.store.Cap(tgt); !principal.Super(c) && capped > 0 && orgCfg.OrgRPM > capped {
		return nil, zip.ErrForbidden(fmt.Sprintf("org_rpm is at most %d for this organization; a higher ceiling is the platform's to grant (org_cap)", capped))
	}
	// Arming the abuse gate is refused while no scorer is installed. Live mode
	// makes a privileged grant fail CLOSED when the scorer cannot answer — which
	// is the right posture for a scorer that is momentarily down, and an outage
	// for one that was never there. Refusing here is the difference between the
	// two, and it is checked at the moment of arming rather than on every request
	// because a gate that silently disarms itself is not a gate.
	//
	// RiskScorerInstalled reports on THIS process (risk.go), and as composed today
	// this one is the gateway binary, which links gateway alone — so nothing it can
	// observe installs a scorer and the refusal below is currently unconditional.
	// That is the fail-SAFE direction, and it refuses rather than admits, but it
	// answers "is a scorer linked here" and not the question arming asks, which is
	// whether the risk plane can answer for the fleet.
	// Co-residency would not close the gap either: apps/risk exports no scorer to
	// install. Reaching the fleet answer is a cross-process ask (a plane op, as the
	// obs event endpoint does), not a wider reading of this predicate.
	if orgCfg.Mode == edge.ModeLive && !cloud.RiskScorerInstalled() {
		return nil, zip.ErrBadRequest("mode=live requires the risk scorer; none is installed in this deployment")
	}
	var recs []*audit.Record
	if orgCfg.OrgCap > 0 {
		r, err := o.audited(c, "gateway.org_cap", tgt, "org_cap", o.s.State.store.OwnCap(tgt), orgCfg.OrgCap)
		if err != nil {
			return nil, err
		}
		recs = append(recs, r)
	}
	// The admin org's row IS the platform row, so its org_rpm is every org's
	// default cap.
	if tgt == o.s.State.store.AdminOrg() && orgCfg.OrgRPM > 0 {
		r, err := o.audited(c, "gateway.default_cap", tgt, "org_rpm", o.s.State.store.Platform().OrgRPM, orgCfg.OrgRPM)
		if err != nil {
			o.failed(ctx, recs)
			return nil, err
		}
		recs = append(recs, r)
	}
	saved, err := o.s.State.store.Put(ctx, tgt, orgCfg)
	if err != nil {
		o.s.Log.Warn("gateway org policy write failed", "org", tgt, "err", err)
		o.failed(ctx, recs)
		return nil, zip.Errorf(503, "policy store unavailable")
	}
	o.s.Log.Info("gateway org policy updated", "org", tgt, "by", orgCfg.UpdatedBy,
		"org_rpm", saved.OrgRPM, "org_cap", saved.OrgCap, "cache_ttl_sec", saved.CacheTTLSec, "methods", len(saved.Methods),
		"mode", saved.Mode)
	eff := o.s.State.store.Effective(tgt)
	return &eff, nil
}

// audited records a SuperAdmin moving a ceiling-setting field on the audit trail,
// with its value before and after, BEFORE the change is made: an org's org_cap
// (gateway.org_cap), or the platform's org_rpm, which is every org's default cap
// (gateway.default_cap). A ceiling moved on a SuperAdmin session must leave a
// record, so a trail that does not take it refuses the change (503) and nothing
// moves — the rule the credit limit keeps (admin.credit.limit). It answers the
// record, so a store that then fails can be recorded as the outcome.
func (o ops) audited(c *zip.Ctx, action, org, field string, before, after int) (*audit.Record, error) {
	if o.s.State.trail == nil {
		return nil, zip.Errorf(503, "the audit trail did not take the change, so it was not made")
	}
	was, _ := json.Marshal(map[string]int{field: before})
	now, _ := json.Marshal(map[string]int{field: after})
	rec := audit.Record{
		Actor:     audit.Actor{Org: principal.Owner(c), Sub: c.User()},
		Action:    action,
		Resource:  audit.Resource{Type: "org", ID: org},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: true},
		Outcome:   audit.Outcome{Result: "success", Status: 200},
		RequestID: c.Header("X-Request-Id"),
		Method:    "PUT",
		Path:      "/v1/gateway/config",
		Before:    audit.Redact(was),
		After:     audit.Redact(now),
	}
	if _, err := o.s.State.trail.Append(c.Context(), rec); err != nil {
		return nil, zip.Errorf(503, "the audit trail did not take the change, so it was not made: %v", err)
	}
	return &rec, nil
}

// failed records that a change audited before it was made did not land.
func (o ops) failed(ctx context.Context, recs []*audit.Record) {
	for _, r := range recs {
		r.Outcome = audit.Outcome{Result: "error", Status: 503, Reason: "the policy store did not take the change"}
		_, _ = o.s.State.trail.Append(ctx, *r)
	}
}

// Traffic reports who is calling this organization's API right now: the request
// count for the last minute split by AGENCY LANE — agent, human, bot, unknown —
// and the busiest callers behind it, each with its request count, its
// authentication-failure count, how many distinct paths it touched, and any
// verdict currently held against it.
//
// The lane split is the answer to the question a generic bot filter cannot
// answer: which of this traffic is the customer's own automation and which is
// somebody working through a list. It is computed from credentials we issued, not
// from the client's self-description, so a scraper cannot move itself into the
// agent lane by editing a header.
//
// A validated caller appears as a FINGERPRINT — a one-way, per-process digest. It
// is stable enough to recognise the same caller across a minute and cannot be
// turned back into a key, so this report is safe to read, screenshot and paste.
//
// It also reports what the sensor's own ceilings are doing (strain, tracked,
// ceiling, refused) and how many screens the scorer did not answer (unscored), so
// a control that has stopped measuring or a judge that has stopped answering is a
// number here rather than a quiet day.
//
// Scoped to the caller's own validated organization. A SuperAdmin may inspect a
// specific tenant with ?org=<slug>, or the lane that has no tenant — every caller
// the identity boundary could not validate — with an empty ?org=.
func (o ops) traffic(ctx context.Context, _ *cloud.Unit) (*edge.TrafficView, error) {
	c, org, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if o.s.State.traffic == nil {
		return nil, zip.Errorf(503, "the edge traffic sensor is not running in this deployment")
	}
	tgt := target(c, org)
	v := o.s.State.traffic.View(tgt, o.s.State.store.Mode(tgt), time.Now())
	return &v, nil
}
