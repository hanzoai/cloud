package team

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/team/token"
)

// svc is the mounted team subsystem: the two stores + the wired config. Held so
// Shutdown can release the DB handles and the roster hub.
type svc struct {
	accounts *accountStore
	trans    *transServer
}

// mounted is the active service so Shutdown can release resources. Idempotent.
var mounted *svc

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
	acct.register(app)

	// The transactor data-plane WebSocket. The :token segment is a JWT (a single
	// path segment — no slashes), decoded + VERIFIED before the upgrade.
	app.Get("/v1/team/transactor/:token", trans.serveWS)

	bridge := &botsBridge{trans: trans, accounts: accounts}
	bridge.register(app)

	mounted = &svc{accounts: accounts, trans: trans}
	log.Info("team mounted", "brand", deps.Brand, "iam", cfg.iamEndpoint)
	return nil
}

func init() {
	cloud.RegisterWithShutdown("team", 138, func(app any, deps cloud.Deps) error {
		a, ok := app.(*zip.App)
		if !ok {
			return fmt.Errorf("team.Mount: app is %T, want *zip.App", app)
		}
		return Mount(a, deps)
	}, func(context.Context) error { return Shutdown() })
}

// Shutdown releases the team stores (account DB + every cached per-workspace docs
// handle). Idempotent — safe to call when nothing is mounted.
func Shutdown() error {
	if mounted == nil {
		return nil
	}
	var firstErr error
	if mounted.trans != nil && mounted.trans.store != nil {
		if err := mounted.trans.store.Close(); err != nil {
			firstErr = err
		}
	}
	if mounted.accounts != nil {
		if err := mounted.accounts.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	mounted = nil
	live = nil
	return firstErr
}

// loadConfig resolves the team config from deps + KMS-synced env. Secrets
// (IAM_CLIENT_SECRET, SERVER_SECRET) come from env values the operator syncs from
// KMS — never plaintext in code, never a git-committed value.
func loadConfig(deps cloud.Deps) config {
	return config{
		iamEndpoint:     firstNonEmpty(deps.IAMIssuer, os.Getenv("IAM_ENDPOINT"), "https://hanzo.id"),
		iamClientID:     os.Getenv("IAM_CLIENT_ID"),
		iamClientSecret: os.Getenv("IAM_CLIENT_SECRET"),
		iamOrg:          firstNonEmpty(os.Getenv("IAM_ORG"), deps.Brand, "hanzo"),
		serverSecret:    env("SERVER_SECRET", token.DefaultSecret),
		frontURL:        strings.TrimRight(os.Getenv("FRONT_URL"), "/"),
		transactor:      strings.TrimRight(os.Getenv("TRANSACTOR_URL"), "/"),
		provider:        "openid",
	}
}

// env returns the value of key, or fallback when unset. The ONE env helper for the
// package (used by the docs store, the config loader).
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
