// Package gateway is live control of the policy your API applies to every
// incoming request: CORS, rate limits, cache TTL and allowed methods, changed
// without a redeploy.
//
// THE GATEWAY IS PLUMBING, AND THIS IS ITS ONE PRODUCT ENDPOINT. The gateway is the
// trust boundary — validate the IAM JWT, strip client-supplied identity, re-mint
// X-Org-Id — and it is not a network hop: it is compiled INTO the cloud binary as
// gateway.Use, and hanzoai/gateway's own routes.go states the law ("ONE routing
// source of truth = cloud's mount table, not a second map here"). Plumbing earns no
// prefix. What earns this one is the thing a customer actually calls: the runtime
// config plane for that policy, at /v1/gateway/config, and nothing else. It serves
// GET/PUT over the SAME edge.Store the EdgeCORS/EdgeRateLimit middleware and
// ScopeRateLimit read live, so an operator retunes the CORS allowlist, the pre-auth
// per-IP flood cap, or a tenant's authenticated rate ceiling with NO redeploy,
// replacing config that used to be baked into an image.
//
// TWO IAM-gated scopes (mirrors apps/pricing/enablement.go: global state is
// SuperAdmin-only, self-service is scoped to the validated tenant):
//
//   - PLATFORM policy (CORS origins, per-IP cap + window) — pre-auth edge knobs
//     with no tenant at evaluation time. Writable ONLY by a SuperAdmin
//     (c.IsAdmin() ⟺ owner == admin org). A PUT carrying any platform field is
//     routed to the platform row explicitly (PutPlatform), so it lands correctly
//     even when the SuperAdmin is org-switched to another tenant.
//   - PER-ORG policy (OrgRPM, the authenticated ceiling; CacheTTLSec + CachePaths,
//     the edge-cache TTL; Methods, the accepted-method allowlist) — a tenant's own
//     row of self-service edge config. An org admin writes its own (org from
//     principal.Org, never a raw header); a SuperAdmin may target any tenant with
//     ?org=<slug>.
//   - MODE, the abuse gate's posture, lives on a tenant's row but is NOT
//     self-service: a control's subject may not switch the control off, so writing
//     it requires SuperAdmin whichever row it lands on. It is the one field on this
//     surface whose scope (which org it applies to) and whose authority (who may
//     set it) are different questions.
//
// The store is owned by BuildDeps (deps.GatewayPolicy) and shared; this subsystem
// does not open or close it (serve.go closes it once at shutdown), so there is one
// store, one source of truth. Per-PROJECT rate scoping is NOT duplicated here — it
// remains ScopeRateLimit's commerce-configured domain (per (org,project,service)).
package gateway

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/apps/principal"
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
}

// ops binds the mounted Service so each op can be a method value — the only bound
// form cmd/zipdoc can lift prose from. It carries STATE and no logic.
type ops struct{ s *cloud.Service[state] }

// Mount wires /v1/gateway/config onto app over the shared policy store. The store
// is owned by deps (not a Base dep), so this constructs the Service value directly
// via cloud.NewBase rather than cloud.Use.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("gateway.Use:  nil app")
	}
	if deps.GatewayPolicy == nil {
		return fmt.Errorf("gateway.Use:  nil deps.GatewayPolicy")
	}
	s := &cloud.Service[state]{
		Base:  cloud.NewBase(deps, "gateway"),
		State: state{store: deps.GatewayPolicy, traffic: deps.Traffic},
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
	g := app.Group("/v1/gateway")
	o := ops{s: s}
	zip.Get(g, "/config", o.read)
	zip.Put(g, "/config", o.write)
	zip.Get(g, "/traffic", o.traffic,
		zip.WithOperationID("gatewayTraffic"),
		zip.WithSummary("Report who is calling this org's API right now"),
		zip.WithTags("gateway"))
}

// noArgs is the input of an op that takes none: no body, no query, no path param.
type noArgs struct{}

// caller is the ONE identity client both config ops ask. It returns the caller's
// VALIDATED org (principal.OrgFrom — never an input field, which is caller-supplied)
// together with the request, because this surface gates on two facts the org alone
// does not carry: SuperAdmin-ness (X-User-IsAdmin) and the ?org=<slug> a SuperAdmin
// targets another tenant with. It fails closed off the HTTP path, where there is no
// principal and no attested admin.
func caller(ctx context.Context) (*zip.Ctx, string, error) {
	org, ok := principal.OrgFrom(ctx)
	if !ok {
		return nil, "", zip.ErrForbidden("a validated principal is required")
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return nil, "", zip.ErrForbidden("a validated principal is required")
	}
	return c, org, nil
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
	if !c.IsAdmin() {
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
func (o ops) read(ctx context.Context, _ *noArgs) (*edge.Policy, error) {
	c, org, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	p := o.s.State.store.Effective(target(c, org))
	return &p, nil
}

// Write updates one policy scope and returns the policy in force after the write.
// A body carrying any PLATFORM field (cors_origins, per_ip_rpm, window_sec) is a
// platform write and requires SuperAdmin; otherwise it is a per-org write (org_rpm,
// cache_ttl_sec, cache_paths, methods) scoped to the caller's own org — or, for a
// SuperAdmin, the tenant named by ?org=<slug>. A body that sets nothing is a 400.
// The abuse gate's mode is an OPERATOR field: setting it requires SuperAdmin,
// whichever organization it lands on. updated_at and updated_by are
// server-stamped; a client-supplied value is ignored.
//
// Example: {"org_rpm": 120, "cache_ttl_sec": 30, "methods": ["GET", "POST"]}
func (o ops) write(ctx context.Context, in *edge.Policy) (*edge.Policy, error) {
	c, org, err := caller(ctx)
	if err != nil {
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
	if in.Mode != "" && !c.IsAdmin() {
		return nil, zip.ErrForbidden("mode is set by the platform, not by the organization it governs")
	}

	platformWrite := len(in.CORSOrigins) > 0 || in.PerIPRPM > 0 || in.WindowSec > 0
	if platformWrite {
		if !c.IsAdmin() {
			return nil, zip.ErrForbidden("platform policy (cors/per-IP) requires SuperAdmin")
		}
		if err := in.Validate(); err != nil {
			return nil, zip.ErrBadRequest(err.Error())
		}
		saved, err := o.s.State.store.PutPlatform(ctx, *in)
		if err != nil {
			o.s.Log.Warn("gateway platform policy write failed", "err", err)
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
		CacheTTLSec: in.CacheTTLSec,
		CachePaths:  in.CachePaths,
		Methods:     in.Methods,
		Mode:        in.Mode,
		UpdatedBy:   in.UpdatedBy,
	}
	if orgCfg.OrgRPM <= 0 && orgCfg.CacheTTLSec <= 0 && len(orgCfg.CachePaths) == 0 && len(orgCfg.Methods) == 0 && orgCfg.Mode == "" {
		return nil, zip.ErrBadRequest("nothing to set: provide org_rpm/cache_ttl_sec/cache_paths/methods/mode, or cors_origins/per_ip_rpm/window_sec (SuperAdmin)")
	}
	if err := orgCfg.Validate(); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
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
	tgt := target(c, org)
	saved, err := o.s.State.store.Put(ctx, tgt, orgCfg)
	if err != nil {
		o.s.Log.Warn("gateway org policy write failed", "org", tgt, "err", err)
		return nil, zip.Errorf(503, "policy store unavailable")
	}
	o.s.Log.Info("gateway org policy updated", "org", tgt, "by", orgCfg.UpdatedBy,
		"org_rpm", saved.OrgRPM, "cache_ttl_sec", saved.CacheTTLSec, "methods", len(saved.Methods),
		"mode", saved.Mode)
	eff := o.s.State.store.Effective(tgt)
	return &eff, nil
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
func (o ops) traffic(ctx context.Context, _ *noArgs) (*edge.TrafficView, error) {
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
