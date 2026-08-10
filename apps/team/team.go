package team

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/plan"
)

// teamPrefix is THE path every team route hangs under — one constant, so the
// group each file builds for its own routes cannot drift from the group Mount
// builds for the bridge. A constant (not a variable) because cmd/zipdoc resolves
// a typed op's prefix from the CONSTANT VALUE of the Group argument.
const teamPrefix = "/v1/team"

// guardFn wraps a route handler so it fails closed (503) in degraded mode. It is
// applied per-route by Mount so the enforcement lives in ONE place and the health
// probe is never wrapped. A TYPED op is not a zip.Handler and cannot be wrapped
// by one, so it carries the same refusal itself — see typed.go.
type guardFn = func(zip.Handler) zip.Handler

// state is team's own data: the account store + the transactor. Held via the
// mounted cloud.Service so Shutdown can release the DB handles and the roster
// hub. Shared deps (logger, KMS, billing, brand) live in the embedded cloud.Base.
type state struct {
	accounts *accountStore
	trans    *transServer
}

// mounted is the active service so Shutdown can release resources. Idempotent.
var mounted *cloud.Service[state]

// Mount wires the /v1/team/* surface onto app per HIP-0106. It opens the two
// SQLite stores under {DataDir}/team, wires the account API, the transactor
// WebSocket and the bots read routes, and publishes the transactor singleton so
// the in-process projection path can write into the workspace store.
//
// The uniform /v1/team/health liveness route is provided by the compose root
// (serve.go registers GET /v1/<name>/health for every enabled subsystem BEFORE
// MountAll, HIP-0106) — the SAME contract clients/tracker, clients/crm and
// clients/agents rely on. Mount does NOT re-register it (a second identical route
// is dead — Fiber matches the first-registered — and violates one-way).
func Mount(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("team.Mount: nil app")
	}
	log := luxlog.Default().New("subsystem", "team")
	if deps.DataDir == "" {
		return fmt.Errorf("team.Mount: empty DataDir")
	}
	root := filepath.Join(deps.DataDir, "team")

	accounts, err := openAccountStore(root)
	if err != nil {
		return fmt.Errorf("team.Mount: open account store: %w", err)
	}

	cfg := loadConfig(deps)

	// Fail-closed HS256 posture (Red CRITICAL). When SERVER_SECRET is unset/default
	// (and no dev escape hatch), the subsystem serves HEALTH-ONLY: every /v1/team
	// route is guarded to 503 and NO token is ever decoded/accepted, so a forged
	// token cannot be used — while Mount still SUCCEEDS so the cloud binary and all
	// other subsystems stay up (mirrors clients/kms; erroring here would fail the
	// whole MountAll). /v1/team/health (serve.go's uniform contract) is unaffected.
	degraded := resolveSecret(&cfg, log)

	// guard wraps a handler so it 503s in degraded mode. Applied per-route (NEVER to
	// /v1/team/health, which serve.go owns at the root) so the liveness probe is
	// never degraded — a group/prefix middleware would risk catching health.
	guard := func(h zip.Handler) zip.Handler {
		if !degraded {
			return h
		}
		return func(c *zip.Ctx) error {
			return zip.Errorf(http.StatusServiceUnavailable, "team: signing secret not configured")
		}
	}

	// The identity seam: ONE answer to "who is calling, and what may they touch",
	// shared by every team surface. The IAM validator is the SAME RS256/JWKS trust
	// anchor the identity boundary and the OAuth callback use; the HS256 secret is
	// the fallback arm; the account store is the membership authority the IAM lane
	// authorizes a named workspace against.
	ident := &identity{
		verify:   cloud.NewTokenValidator(cfg.iamEndpoint).Validate,
		secret:   cfg.serverSecret,
		accounts: accounts,
		audience: sessionAudience(cfg),
	}

	trans := &transServer{
		store:     newStore(filepath.Join(root, "workspaces")),
		hier:      buildHierarchy(modelJSON),
		hub:       newHub(),
		ident:     ident,
		accounts:  accounts,
		bots:      agentsBotLister, // the ONE in-process seam to the agents registry
		log:       log,
		startedAt: time.Now().UnixMilli(), // freshness floor: messages older than boot are never answered
		degraded:  degraded,
	}
	// Chunter agent responder: OFF by default (one-way safe default). Only when
	// TEAM_AGENTS_ENABLED=1 do we wire the LLM seam + the concurrency cap, so an
	// un-configured OR misconfigured binary is provably inert — nil runAgent makes
	// maybeAgentReply return at the top and NO outbound model call can ever fire.
	// (This is the containment the writer-crash post-mortem demands: a new binary
	// must be safe by default and only answer when an operator opts in.)
	if os.Getenv("TEAM_AGENTS_ENABLED") == "1" {
		trans.runAgent = agentReplyRunner
		trans.sem = make(chan struct{}, teamAgentsMaxConcurrency())
		log.Info("team: Chunter agent responder ENABLED", "maxConcurrency", cap(trans.sem))
	} else {
		log.Info("team: Chunter agent responder OFF (set TEAM_AGENTS_ENABLED=1 to enable)")
	}
	// Publish the singleton so the in-process projection path (Apply / ingest) and
	// the /v1/team/bots/sync handler can write into the per-workspace store.
	live = trans

	acct := &api{
		accounts: accounts, trans: trans, cfg: cfg, log: log,
		// The IAM boundary: the SAME RS256/JWKS validator the identity middleware
		// uses, so the OAuth callback's `owner` claim is VERIFIED, never trusted raw.
		ident: ident,
		// The entitlement seams: commerce answers "does the org's plan license
		// 'team'"; plan answers the plan's entitlement block (team.guests cap).
		commerce: deps.Commerce,
		planEnt:  plan.Entitlements,
		degraded: degraded,
	}
	// Bridge FIRST, before any leaf AND before the group below: a typed op receives
	// only a context, so the validated org reaches it by being parked there — never
	// as an In field, which is caller-supplied and would be a cross-tenant read the
	// caller asserted for itself. fiber runs middleware in registration order, so
	// one installed after its leaves never runs — which is why this precedes tg
	// rather than following it. Serve installs the same middleware for the whole
	// binary; this one is what makes team's typed ops resolve their tenant under a
	// BARE Mount too (the app's own tests, and any embedder that mounts without
	// Serve). Nesting is harmless — the inner one is what the handler sees.
	//
	// One /v1/team group; every route below is a child of it. Each file that
	// declares TYPED ops builds its own group from the same teamPrefix constant —
	// a fiber group IS its prefix, so those are the same routing subtree, and it
	// is what lets cmd/zipdoc resolve each op's path from the file it is declared
	// in (see bots.go).
	tg := app.Group(teamPrefix)
	// A typed op receives only a context, so the validated org reaches it by
	// being parked there — never as an In field, which is caller-supplied and
	// would be a cross-tenant read the caller asserted for itself. cloud.Bridge
	// does the parking, and the composer owns that install, once at its root;
	// this group is a bare path prefix.
	acct.register(app, guard)

	// The front's workspace switcher polls this statistics endpoint on the
	// transactor base (LoginEndpoint ws→http + /api/v1/statistics). Static route —
	// never captured by the :token segment below.
	//
	// Canon is the api.* host + /v1/ with no nested /api/vN. The clean path is the
	// canonical one new callers use; the /api/v1/ path stays as a forward-compatible
	// alias for the current transactor front until it repoints (no outage, no rename
	// in place). Both resolve to the SAME handler — and both are TYPED ops, so each
	// is its own registry entry and its own operation in the document.
	zip.Get(tg, "/transactor/statistics", trans.statistics)
	zip.Get(tg, "/transactor/api/v1/statistics", trans.statistics)

	// The transactor data-plane WebSocket. The :token segment is a JWT (a single
	// path segment — no slashes), decoded + VERIFIED before the upgrade. UNTYPED,
	// and it cannot be otherwise: the response is a protocol upgrade, not a value.
	tg.Get("/transactor/:token", guard(trans.serveWS))

	bridge := &botsBridge{trans: trans, accounts: accounts, degraded: degraded}
	bridge.register(app)

	// Files plane: the workspace blob store the Team front's UPLOAD_URL/FILES_URL
	// hit, backed by cloud's canonical VFS seam (deps.VFS) and org-scoped by the
	// verified session token — the SAME isolation invariant as the docs store.
	files := &filesService{vfs: deps.VFS, ident: ident, degraded: degraded}
	files.register(app, guard)

	// Billing plane: the go:embed'd usage/wallet page (/billing/ui/*) + the
	// plan/seats read (/billing/plan) — session-gated, org-scoped through the
	// SAME commerce/plan seams the login gate (entitle.go) uses.
	billing := &billingService{accounts: accounts, commerce: deps.Commerce, planEnt: plan.Entitlements, ident: ident, degraded: degraded}
	billing.register(app, guard)

	// Collaborator planes, both app-level under /collaborator: the markup
	// snapshot RPC (collab.go, POST /collaborator/rpc/:documentId) and the live
	// hocuspocus Y.js WebSocket (collabws.go, GET /collaborator) — one service,
	// one tenancy gate, one VFS seam.
	collab := &collabService{vfs: deps.VFS, accounts: accounts, ident: ident, hub: newCollabHub(deps.VFS), degraded: degraded}
	collab.register(app, guard)

	// The membership read a PEER process needs: meet decides a room join and does
	// not own these rows, so the answer travels rather than being read off a signed
	// claim (member_rpc.go).
	exposeMember(accounts)

	mounted = &cloud.Service[state]{Base: cloud.NewBase(deps, "team"), State: state{accounts: accounts, trans: trans}}
	log.Info("team mounted", "brand", deps.Brand, "iam", cfg.iamEndpoint, "client", cfg.iamClientID, "degraded", degraded)
	return nil
}

// Shutdown releases the team stores (account DB + every cached per-workspace docs
// handle). Idempotent — safe to call when nothing is mounted.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	var firstErr error
	if mounted.State.trans != nil && mounted.State.trans.store != nil {
		if err := mounted.State.trans.store.Close(); err != nil {
			firstErr = err
		}
	}
	if mounted.State.accounts != nil {
		if err := mounted.State.accounts.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	mounted = nil
	live = nil
	return firstErr
}

// loadConfig resolves the team config from deps + KMS-synced env. Secrets
// (TEAM_IAM_CLIENT_SECRET, SERVER_SECRET) come from env values the operator syncs
// from KMS — never plaintext in code, never a git-committed value.
//
// The IAM OAuth client is TEAM-NAMESPACED (TEAM_IAM_CLIENT_ID / _SECRET, default
// client id "hanzo-team"), NOT the generic IAM_CLIENT_ID: in the unified cloud
// binary IAM_CLIENT_ID=hanzo-cloud is cloud's OWN confidential client, whose
// registered redirect URIs are console/cloud — not hanzo.team. team's account flow
// needs the hanzo-team app (whose redirect URIs include the team callback), so
// reading the generic id would break Dave's login at cutover.
//
// serverSecret is read RAW here (no default); Mount decides degrade-vs-dev so a
// missing/default HS256 secret fails closed rather than silently signing tokens
// with a public literal.
func loadConfig(deps cloud.Deps) config {
	return config{
		iamEndpoint:     firstNonEmpty(deps.IAMIssuer, os.Getenv("IAM_ENDPOINT"), "https://hanzo.id"),
		iamClientID:     firstNonEmpty(os.Getenv("TEAM_IAM_CLIENT_ID"), "hanzo-team"),
		iamClientSecret: os.Getenv("TEAM_IAM_CLIENT_SECRET"),
		serverSecret:    os.Getenv("SERVER_SECRET"),
		frontURL:        strings.TrimRight(os.Getenv("FRONT_URL"), "/"),
		transactor:      strings.TrimRight(os.Getenv("TRANSACTOR_URL"), "/"),
		provider:        "openid",
		// Public origin for the OAuth redirect_uri behind the gateway (so cloud can
		// sit behind the gateway uniformly like api.hanzo.ai and still emit the
		// correct public callback). TEAM_PUBLIC_URL preferred; PUBLIC_ORIGIN is the
		// shared fallback name. Unset → derive from the request Host (no regression).
		publicURL: strings.TrimRight(firstNonEmpty(os.Getenv("TEAM_PUBLIC_URL"), os.Getenv("PUBLIC_ORIGIN")), "/"),
	}
}

// teamAgentsMaxConcurrency resolves the responder's global in-flight turn cap from
// TEAM_AGENTS_MAX_CONCURRENCY (default defaultMaxConcurrent). It is clamped to
// [1,64] so a typo can never uncap the fan-out (0/negative → default) or make it
// absurd. The cap is the hard ceiling on concurrent outbound model calls the
// responder can have in flight process-wide.
func teamAgentsMaxConcurrency() int {
	n := defaultMaxConcurrent
	if v := os.Getenv("TEAM_AGENTS_MAX_CONCURRENCY"); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			n = parsed
		}
	}
	if n < 1 {
		n = 1
	}
	if n > 64 {
		n = 64
	}
	return n
}

// env returns the value of key, or fallback when unset. The ONE env helper for the
// package (used by the docs store).
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// resolveSecret decides the HS256 signing posture from the RAW SERVER_SECRET env.
// It returns degraded=true (fail-closed, health-only) when the secret is unset or
// the upstream public "secret" literal — so no path EVER signs/verifies a team
// token with a known key (which would let a forged {extra.org, workspace} token
// read+write ANY tenant's docs). There is no escape hatch: dev sets a real
// SERVER_SECRET like every other deployment (one way).
func resolveSecret(cfg *config, log luxlog.Logger) (degraded bool) {
	if cfg.serverSecret != "" && cfg.serverSecret != "secret" {
		return false // a real, non-default secret — functional and secure
	}
	log.Error("team: SERVER_SECRET is unset or the public default — subsystem DEGRADED (health-only); every /v1/team route returns 503 until a KMS-synced SERVER_SECRET is provided")
	return true
}
