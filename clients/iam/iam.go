// Package iamsvc folds Hanzo IAM into the unified hanzoai/cloud binary as an
// in-process subsystem (HIP-0106) — the LAST binary-consolidation piece:
// "one Go binary (hanzoai/cloud) embeds IAM + KMS + o11y".
//
// WRAP, DON'T REWRITE. IAM is a Beego app (~150 routes registered in
// hanzoai/iam/routers.InitAPI over controllers.ApiController/RootController).
// iamserver.InitEmbed() runs the ENTIRE IAM identity runtime — config, SQLite
// store, KMS signing keys, controllers, authz filters, background sync/monitor
// loops — as an in-process embed: it is standalone iamd's bootstrap MINUS the
// standalone-daemon side effects that would crash or endanger this shared
// process (no StopOldInstance `lsof`/SIGKILL, no LDAP/RADIUS listeners, no
// export/os.Exit), binds NO HTTP listener, and RETURNS AN ERROR instead of
// panicking. (The standalone `hanzo iam` / iamd path still uses iamserver.Init +
// web.Run, byte-for-byte unchanged.) After a successful InitEmbed the full IAM
// http.Handler is web.BeeApp.Handlers; this subsystem mounts THAT verbatim on
// cloud's shared zip.App at every path prefix IAM owns. No auth logic is
// reimplemented — the same controllers answer, so hanzo.id's OAuth/OIDC semantics
// (authorize clientId org-resolution, JWT audiences, SuperAdmin owner=="admin",
// argon2id password hashing) are preserved byte-for-byte.
//
// The one hook web.Run() performs that the embed bootstrap omits is Beego
// session-manager registration; this subsystem fires it explicitly (initSessions),
// mirroring the sanctioned iam.Embed path, else every session-touching request
// (login, authorize) nil-derefs in the router.
//
// FAIL-CLOSED, NOT FAIL-LOUD. A broken/misconfigured IAM does NOT crash the
// consolidated binary: InitEmbed's error (or a session/handler failure) degrades
// THIS subsystem to a 503 fail-closed on every IAM prefix (mountFailClosed) while
// every co-resident subsystem (KMS, o11y, …) stays up — the blast-radius
// isolation the whole consolidation exists for, mirroring the KMS
// "no master key → health-only" pattern.
//
// Mounted in-process (whole Beego handler, full request path preserved):
//
//	/v1/iam/*      API + OAuth (/v1/iam/oauth/{authorize,token,userinfo,introspect,
//	               revoke,...}) + OIDC (/v1/iam/.well-known/{openid-configuration,
//	               jwks,...}) + login/logout/signup + userinfo + me/* + cap/* +
//	               cert/saml/tokens + the full admin surface
//	/.well-known/* legacy root OIDC discovery + JWKS (relying-party compatibility)
//	/login/oauth/* browser authorize surface (the /v1/iam/oauth/authorize 302 target)
//	/_/iam/*       login UI SPA assets
//	/cas/*         CAS 1.0/2.0/3.0 ticket validation
//	/scim/*        SCIM 2.0 user/group provisioning
//
// STAGING (security-critical): activation is the standard enable-list gate — the
// operator adds "iam" to the cloud deployment's --enable only AFTER IAM's config
// (Beego app.conf + env + KMS signing keys) is present in the cloud runtime and
// the fold is verified (login/authorize/token/jwks + the operator SSO chain). Until
// then hanzo.id is served by the standalone iam pod via ingress. A cloud pod that
// enables "iam" MUST run at replicas=1 (Config.Validate enforces this): IAM's
// session store is Beego's process-local "memory" provider, so a horizontally
// scaled app tier would mint a login/authorize session on one replica and lose it
// on the next. If a broken config slips through, the subsystem serves 503
// fail-closed (above) rather than crashing cloud.
package iam

import (
	"net/http"
	"os"
	"path/filepath"

	"github.com/hanzoai/beego/v2/server/web"
	"github.com/hanzoai/beego/v2/server/web/session"
	"github.com/hanzoai/iam/iamserver"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
)

// iamPrefixes is every root path prefix the IAM Beego handler owns. The whole
// handler is mounted at each via zip.App.Mount (which registers prefix+"/*" and
// preserves the full request path). The handler answers only paths IAM
// registered; an unknown path under a prefix 404s from Beego exactly as
// standalone IAM does, so a broad mount cannot leak another subsystem's surface.
var iamPrefixes = []string{
	"/v1/iam",      // API + oauth + .well-known + me + cap + saml + tokens + admin
	"/.well-known", // legacy root OIDC discovery + JWKS (RP compatibility)
	"/login/oauth", // browser authorize surface (the /v1/iam/oauth/authorize 302 target)
	"/_/iam",       // login UI SPA assets
	"/cas",         // CAS ticket validation
	"/scim",        // SCIM 2.0
}

// Mount boots the in-process IAM Beego server and attaches its http.Handler to
// cloud's shared zip.App. Called once by cloud.MountAll when "iam" is enabled.
//
// Beego keeps process-global singletons (web.BeeApp, GlobalSessions, logger/flag
// registration), so the bootstrap is inherently once-per-process; MountAll calls
// each subsystem's Mount exactly once, which satisfies that.
func Mount(app *zip.App, deps cloud.Deps) error {
	log := deps.Logger.New("subsystem", "iam")

	// IAM persists its SQLite store under <DataDir>/iam, matching the sanctioned
	// iam.Embed default. Set before InitEmbed reads config — Beego's conf resolves
	// env ahead of app.conf, so this wins. Setenv only fails on a malformed key,
	// which is impossible here, so the error is ignored (mirrors iam.Embed).
	dataDir := filepath.Join(deps.DataDir, "iam")
	if deps.DataDir != "" {
		_ = os.Setenv("IAM_DATA_DIR", dataDir)

		// SHARED-GLOBAL ISOLATION (the reason "iam" was staged): both this Beego
		// identity fork AND the sibling `ai` casibase/casdoor fork resolve their
		// SQLite handle from the SAME process-global keys — env `dataSourceName`
		// (checked first by both forks' conf.GetConfigString) and, failing that, the
		// one beego web.AppConfig. A deployment sets `dataSourceName` for `ai`; with
		// IAM enabled, IAM's bootstrap would resolve that SAME value and xorm-open —
		// then auto-migrate its casdoor tables INTO — ai's database file. That is the
		// documented co-residence crash ("ai: bootstrap: unable to open database file
		// (14)") that pinned every post-embed release. IAM's conf already honors an
		// IAM-scoped override (conf.GetConfigDataSourceName → IAM_DATABASE_URL wins
		// over the shared `dataSourceName`), so pinning it here to IAM's OWN sqlite
		// file under DataDir gives the two forks independent stores, order-independent
		// and with NO fork edit. driverName ("sqlite") is shared harmlessly — both
		// forks want the one Hanzo sqlite driver. An operator-set IAM_DATABASE_URL
		// (explicit external DSN) is respected — only the default is filled in.
		isolateDatabase(dataDir)
	}

	// cloud owns process shutdown, not Beego's graceful runner.
	web.BConfig.Listen.Graceful = false

	// EMBED-MODE bootstrap (see package doc): the full IAM runtime minus the
	// standalone-daemon side effects, returning an error instead of panicking. A
	// broken IAM therefore degrades THIS subsystem to fail-closed health-only while
	// every co-resident subsystem stays up — the fold's blast-radius isolation.
	if err := iamserver.InitEmbed(); err != nil {
		log.Error("iam bootstrap failed — serving fail-closed 503 (cloud stays up; standalone iam pod unaffected)", "err", err)
		mountFailClosed(app)
		return nil
	}

	// web.Run() normally registers the Beego session manager; the embed path skips
	// web.Run, so fire that one hook here. Without it every session-touching
	// request (login, authorize) nil-derefs in the router.
	if err := initSessions(); err != nil {
		log.Error("iam session init failed — serving fail-closed 503", "err", err)
		mountFailClosed(app)
		return nil
	}

	handler := web.BeeApp.Handlers // *web.ControllerRegister implements http.Handler
	if handler == nil {
		log.Error("iam produced a nil Beego handler — serving fail-closed 503")
		mountFailClosed(app)
		return nil
	}
	mountHandler(app, handler)

	log.Info("iam embedded in-process (Beego handler mounted)", "data_dir", dataDir, "prefixes", iamPrefixes)
	return nil
}

// mountFailClosed serves an honest JSON 503 on every IAM prefix when the embed
// cannot boot, so /v1/iam/* answers "iam unavailable" instead of falling through
// to the console SPA catch-all (which would return HTML 200 for an auth path).
// cloud and every other subsystem stay up — the fold's blast-radius isolation.
// During staged rollout hanzo.id is still served by the standalone iam pod via
// ingress, so clients never see this path until cutover.
func mountFailClosed(app *zip.App) {
	failed := zip.AdaptNetHTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"error":"iam unavailable","code":503}`))
	}))
	for _, p := range iamPrefixes {
		app.All(p+"/*", failed)
	}
}

// mountHandler attaches the IAM http.Handler at every prefix IAM owns. Split from
// Mount so the routing plumbing — zip.App.Mount dispatching prefix/* to the
// handler with the ORIGINAL request path preserved (Beego routes on the full
// path) — is unit-testable without booting the full Beego runtime.
func mountHandler(app *zip.App, handler http.Handler) {
	for _, p := range iamPrefixes {
		app.Mount(p, handler)
	}
}

// initSessions mirrors the Beego session-manager registration that web.Run()
// performs via its unexported initBeforeHTTPRun hook (which the embed path
// bypasses). Config is read straight from web.BConfig.WebConfig.Session, which
// iamserver.Init() has already populated (memory provider, cookie
// "iam_session_id", lax SameSite), so there is no second source of session truth
// — this only wires the manager IAM already configured. Idempotent.
func initSessions() error {
	if web.GlobalSessions != nil {
		return nil
	}
	s := web.BConfig.WebConfig.Session
	mgr, err := session.NewManager(s.SessionProvider, &session.ManagerConfig{
		CookieName:              s.SessionName,
		EnableSetCookie:         s.SessionAutoSetCookie,
		Gclifetime:              s.SessionGCMaxLifetime,
		// Secure is PINNED true, not derived from Listen.EnableHTTPS. The binary
		// listens plain :8000 behind the TLS-terminating ingress, so EnableHTTPS is
		// false and the derived value would ship a non-Secure session cookie — and
		// the embed console's identity bridge (middleware_identity.sessionAccessToken)
		// turns that opaque sid into a money bearer (hk- mint, balance/top-up). A
		// non-Secure cookie is capturable off any plaintext leg and replayable, so it
		// MUST be Secure. The deployed edge is always HTTPS; a plain-HTTP local embed
		// is not a supported prod topology. (RED H2.)
		Secure:                  true,
		CookieLifeTime:          s.SessionCookieLifeTime,
		ProviderConfig:          filepath.ToSlash(s.SessionProviderConfig),
		DisableHTTPOnly:         s.SessionDisableHTTPOnly,
		Domain:                  s.SessionDomain,
		EnableSidInHTTPHeader:   s.SessionEnableSidInHTTPHeader,
		SessionNameInHTTPHeader: s.SessionNameInHTTPHeader,
		EnableSidInURLQuery:     s.SessionEnableSidInURLQuery,
		CookieSameSite:          s.SessionCookieSameSite,
		SessionIDPrefix:         s.SessionIDPrefix,
	})
	if err != nil {
		return err
	}
	web.GlobalSessions = mgr
	go mgr.GC()
	return nil
}

// isolateDatabase pins IAM's SQLite handle to its OWN file under dataDir via the
// IAM-scoped IAM_DATABASE_URL override, so the embedded IAM never resolves the
// shared `dataSourceName` (which a deployment sets for the sibling `ai` fork) and
// the two casdoor-derived forks get independent stores. See the call site for the
// full rationale (this is the co-residence unblock). An operator-set
// IAM_DATABASE_URL is respected; only the default is filled in. Idempotent.
func isolateDatabase(dataDir string) {
	if _, ok := os.LookupEnv("IAM_DATABASE_URL"); ok {
		return // operator override always respected
	}
	// Only isolate when a SIBLING subsystem (ai) has claimed the shared
	// `dataSourceName` env — that is the co-residence collision this guards. When
	// IAM is the SOLE sqlite consumer (identity role, CLOUD_ENABLE=iam), leave the
	// DSN to IAM's own resolution so the SQLCipher enveloped path
	// (orgIsolation=sqlite) keys and ENCRYPTS the store. Forcing a plaintext
	// `file:` DSN here would make the CGO+SQLCipher identity build open its global
	// DB UNENCRYPTED — the catastrophic plaintext-at-rest the encryption gate
	// exists to prevent. No sibling → no override.
	if _, shared := os.LookupEnv("dataSourceName"); !shared {
		return
	}
	_ = os.Setenv("IAM_DATABASE_URL", defaultIAMDatabaseURL(dataDir))
}

// defaultIAMDatabaseURL is IAM's default embedded SQLite DSN: its own iam.db under
// the IAM data dir, WAL + a busy-timeout so a co-resident reader never trips
// SQLITE_BUSY. Pure (no env/IO) so the isolation contract is unit-testable.
func defaultIAMDatabaseURL(dataDir string) string {
	return "file:" + filepath.ToSlash(filepath.Join(dataDir, "iam.db")) + "?cache=shared&_busy_timeout=5000&_journal_mode=WAL"
}

// init registers IAM with cloud's subsystem registry at order 50 — IAM is the
// identity authority and most subsystems depend on deps.IAM at request time, so
// it mounts before them (the HIP-0106 iam=50 slot).
func init() {
	cloud.Register("iam", 50, cloud.Typed(Mount))
}
