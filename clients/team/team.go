package team

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/team/token"
)

// guardFn wraps a route handler so it fails closed (503) in degraded mode. It is
// applied per-route by Mount so the enforcement lives in ONE place and the health
// probe is never wrapped.
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
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("team.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("team.Mount: nil deps.Logger")
	}
	log := deps.Logger.New("subsystem", "team")
	if deps.DataDir == "" {
		return fmt.Errorf("team.Mount: empty DataDir")
	}
	root := filepath.Join(deps.DataDir, "team")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("team.Mount: data dir: %w", err)
	}

	accounts, err := openAccountStore(filepath.Join(root, "account.db"))
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

	trans := &transServer{
		store:    newStore(filepath.Join(root, "workspaces")),
		hier:     buildHierarchy(modelJSON),
		hub:      newHub(),
		secret:   cfg.serverSecret,
		accounts: accounts,
		bots:     agentsBotLister, // the ONE in-process seam to the agents registry
	}
	// Publish the singleton so the in-process projection path (Apply / ingest) and
	// the /v1/team/bots/sync handler can write into the per-workspace store.
	live = trans

	acct := &api{accounts: accounts, trans: trans, cfg: cfg, log: log}
	acct.register(app, guard)

	// The transactor data-plane WebSocket. The :token segment is a JWT (a single
	// path segment — no slashes), decoded + VERIFIED before the upgrade.
	app.Get("/v1/team/transactor/:token", guard(trans.serveWS))

	bridge := &botsBridge{trans: trans, accounts: accounts}
	bridge.register(app, guard)

	// Files plane: the workspace blob store the Team front's UPLOAD_URL/FILES_URL
	// hit, backed by cloud's canonical VFS seam (deps.VFS) and org-scoped by the
	// verified session token — the SAME isolation invariant as the docs store.
	fsvc := &filesSvc{vfs: deps.VFS, accounts: accounts, secret: cfg.serverSecret}
	fsvc.register(app, guard)

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
		iamOrg:          firstNonEmpty(os.Getenv("IAM_ORG"), "hanzo"),
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
// the public DefaultSecret literal AND the TEAM_DEV_INSECURE=1 escape hatch is not
// set — so no production path ever signs/verifies a team token with a known key
// (which would let a forged {extra.org, workspace} token read+write ANY tenant's
// docs). With the dev escape hatch it runs functional on the default secret,
// loudly. It mutates cfg.serverSecret to the concrete signing key for the dev case.
func resolveSecret(cfg *config, log luxlog.Logger) (degraded bool) {
	if cfg.serverSecret != "" && cfg.serverSecret != token.DefaultSecret {
		return false // a real, non-default secret — functional and secure
	}
	if os.Getenv("TEAM_DEV_INSECURE") == "1" {
		cfg.serverSecret = token.DefaultSecret
		log.Warn("team: running on the DEFAULT HS256 signing secret (TEAM_DEV_INSECURE=1) — NEVER in production")
		return false
	}
	log.Error("team: SERVER_SECRET is unset or the public default — subsystem DEGRADED (health-only); every /v1/team route returns 503 until a KMS-synced SERVER_SECRET is provided")
	return true
}
