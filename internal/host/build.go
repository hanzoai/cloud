package host

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/sdk"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hanzoai/cloud/sdk/object"
	"github.com/hanzoai/cloud/sdk/secret"

	"github.com/hanzoai/cloud/apps/commerce/transport"
	"github.com/hanzoai/cloud/internal/aimodel"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/iam"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/ha"
	metrics "github.com/hanzoai/o11y/metrics"
	sqlitedrv "github.com/hanzoai/sqlite"
	"github.com/hanzoai/vfs/replica"
	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/finance"
	"github.com/hanzoai/cloud/sdk/infer"
)

// BuildDeps constructs the Deps used by every subsystem's Use(app, deps).
//
// Wiring rules per HIP-0106 inter-subsystem contract:
//
//  1. If the subsystem is enabled in this process, the Client field is
//     left nil here. The subsystem's own Use() will install a typed
//     in-process Client into Deps via the SetClient helpers exposed by
//     this package. (Subsystem Mounts run after BuildDeps; they have
//     full access to construct their concrete implementation, and the
//     resulting object goes back into Deps for everyone else to call.)
//
//  2. If the subsystem is disabled but cfg has a non-empty ZAP RPC
//     endpoint for it, the Client field gets a ZAP-RPC stub targeting
//     that endpoint. Subsystem code calls deps.X.Foo(...) without
//     knowing the call goes over the wire.
//
//  3. If the subsystem is disabled AND there is no endpoint, the Client
//     field gets a "disabled" stub that fails closed with a clear
//     error. Mount-time consumers detect this with
//     errors.Is(err, infer.ErrDisabled) and log a friendly "dep X needed by Y
//     not configured" message.
//
// JSON does not appear in any of these paths. A co-resident dependency is a
// direct Go method call; a peer that is elsewhere is reached over the peer plane
// (plane.Call — ZAP bytes on the peer's own socket, addressed by name). JSON
// happens only at the gateway/ingress edge, through the zip jsonenc helper.
func buildDeps(cfg *cloud.Config) cloud.Deps {
	transport.SetIdentity(carryIdentity)
	logger := luxlog.New("cloud")

	// ONE logger, and this is where it becomes reachable without being carried.
	//
	// luxfi/log publishes a process default — Root, Default, and the bare Info/Warn/
	// Error functions — and nothing ever set it, so every package that wanted to log
	// had to be HANDED one. That is what Deps.Logger WAS: reads threading a value the
	// library was already prepared to hold. Cloud's own core did not even use that
	// field consistently; it built `luxlog.New("cloud")` again in five separate
	// places, so there were two ways to obtain a logger and neither was the library's.
	// The field is gone; this line is what replaced it.
	//
	// Installed HERE because this is where the process's logger is constructed, and
	// installed FIRST because a default set late is a default that silently did not
	// apply to whatever logged before it. Everything downstream — every subsystem
	// mount, every request — runs after this line.
	luxlog.SetDefault(logger)

	// Credentials, before anything opens a store. The first open is edge.New at the
	// bottom of this function, and an open with no master installed fails — so a key
	// installed any later is installed after the store that needed it. Boot is
	// sync.Once-guarded and Serve calls it earlier still; this call is what covers
	// every caller that builds deps directly.
	cloud.BootMaster(cloud.DataDir())
	logMaster(logger)

	logger.Info(
		"building deps",
		"brand", cfg.Brand,
		"domain", cfg.Domain,
		"iam_issuer", cfg.IAMIssuer,
		"data_dir", cfg.DataDir,
		"enabled", cfg.Enable,
	)

	deps := cloud.Deps{
		Version:   cfg.Version,
		IAMIssuer: cfg.IAMIssuer,
		Discovery: cfg.Discovery,
		MasterKey: masterKeyBytes(cfg),
	}

	// For each subsystem: enabled → leave nil (Mount fills it); not
	// enabled + endpoint → RPC client; not enabled + no endpoint →
	// disabled stub. The plain co-resident-or-RPC-or-disabled clients share
	// ONE resolver (pick); KMS/AI/VFS keep bespoke pickers because their
	// construction genuinely differs (embedded store / gateway preference /
	// S3-admin backend). O11y's disabled stub is a no-op (telemetry going
	// nowhere is normal), not fail-closed.
	// BEFORE the clients: the durable plane reads only cfg and the object store, and
	// the embedded KMS builds per-org stores that want it. Discovered late, it could
	// not reach them, and KMS's files were local-only for no reason anyone chose.
	co := buildCoord(cfg, logger)
	deps.Durable, deps.LiveMembers = buildDurability(cfg, co, logger)
	deps.Peers = peered(cfg)
	deps.KMS = pickKMSClient(cfg, deps.Durable, logger)
	useBotAuth(deps.KMS)
	deps.Commerce = pickCommerceClient(cfg, logger)
	// Metering client BEFORE the AI client: deps.AI is wrapped in the metering
	// decorator (the ONE inference gate+meter — no exempt path, no bypass, no
	// side-channel key), which needs deps.Metering. nil-safe — an unconfigured
	// commerce URL yields a !Enabled() client, so the wrap is a transparent
	// pass-through and a dev deployment is never blocked.
	deps.Metering = buildMeteringClient(cfg, logger)
	// AI (completions) and Embed (embeddings and reranks) both take the
	// deployment's own identity wherever it has one, and neither rides it to the
	// gateway: a buffered completion, an embedding and a rerank cross the plane to
	// `ai` carrying the org and project they are for (overPlane), which is
	// the only hop that names them. The gateway client stays behind for streams and
	// the model list, and for a fleet that runs no `ai`. A static key is the
	// operator's credential for a deployment with no identity, and stays on the
	// gateway; completions never ride a read-only publishable (pk-) one. Both meter
	// through the ONE commerce path.
	deps.AI = cloud.MeteredAIClient(overPlane(pickCompletionsClient(cfg, logger)), deps)
	deps.Embed = cloud.MeteredAIClient(overPlane(pickEmbedClient(cfg, logger)), deps)
	installFinance(cfg, deps, logger)

	// Payments and Vault never co-resident. Disabled stub when no
	// endpoint, otherwise RPC.

	return deps
}

// publicCatalog is what the anonymous lane may be served from a cache, and for how
// long (seconds): the model list, the price list, the published research runs,
// the starter-kit gallery, the plan catalog, commerce's catalog and the
// agent-skills catalogue read the same for every caller who presents no
// credential, whatever page asks. So an anonymous GET of
// one is answered to every origin at once (EdgeCORS) and one shared copy serves
// them all (EdgeCache; hanzo-inc/universe infra/cf-zones/cache.py SHARED).
//
// A path is the CANONICAL spelling, the one the host rewrites to before an app
// sees it: /v1/templates arrives here as /v1/template.
//
// /v1/billing/plans is not here though its answer qualifies: hanzo-inc/pay and
// @hanzo/ui's onboarding read it signed out with credentials: 'include', which a
// browser refuses to pair with `*`. It joins when they read it without.
//
// A writer that changes one of these answers clears the edge's copies of it
// (client.PurgeEdge, through the one edge client the project app holds), so a TTL
// here bounds how stale a copy is when a purge is missed, not how soon a write shows.
//
// An operator's cache_paths on the admin row replaces this map whole (edge
// merge), so a row that sets one names these paths too or stops caching them.
var publicCatalog = map[string]int{
	"/v1/models":        300,
	"/v1/pricing":       300,
	"/v1/research/runs": 300,
	"/v1/template":      300,
	// The plan catalog is the bundle this binary ships, so it changes with a
	// release; its probe is never kept.
	"/v1/plan":        3600,
	"/v1/plan/health": 0,
	// commerce's public catalog, by the brand its URL names.
	"/v1/commerce/catalog": 300,
	// the billing catalog and discovery documents an agent reads before it pays: the same for
	// every caller who presents no credential.
	"/v1/billing/catalog":       300,
	"/v1/billing/discovery":     300,
	"/.well-known/agent-skills": 300,
	// Public SVG README badges: one shared hourly copy per handle/options across origins.
	"/v1/leaderboard/badge": 3600,
	"/v1/reward/badge":      3600,
	// Public aggregate IAM counts (zero PII): shared 5-minute edge copy.
	"/v1/iam/stats": 300,
}

// staticEdgePolicy projects the static env/flag edge config into the boot-default
// policy the edge.Store layers runtime overrides on top of. A disabled
// per-IP limiter (CLOUD_EDGE_RATELIMIT=false) maps to PerIPRPM 0 (a live no-op).
func staticEdgePolicy(cfg *cloud.Config) edge.Policy {
	p := edge.Policy{
		CORSOrigins: cfg.CORSOrigins,
		WindowSec:   cfg.EdgeRateWindowSec,
		CachePaths:  publicCatalog,
	}
	if cfg.EdgeRateEnabled {
		p.PerIPRPM = cfg.EdgeRatePerIP
	}
	return p
}

// buildMeteringClient constructs the commerce metering client for BillingGate —
// the request-edge debit on every paid AI call.
//
// CO-RESIDENT (task #111): when commerce is folded in-process (Enabled("commerce")),
// the gate DEBITS the in-process commerce handler over the commerce transport's
// self-routing dispatch — a direct Go call, no socket to commerce.hanzo.svc:8001. The base is
// pinned NON-EMPTY (real CLOUD_COMMERCE_HTTP_URL, else the in-process placeholder) so
// the gate stays ENABLED even after the standalone + its env are retired — a metering
// gate that silently no-ops is a free-money hole, so it must never drop to
// "not configured" while commerce is co-resident. The transport resolves the handler
// lazily (published by commerce.Use before any request), so building the client
// here (pre-UseAll) is fine.
//
// SPLIT-DEPLOY (unchanged): without co-residency an empty CommerceHTTPURL yields a
// not-Enabled() client (allow + no-op) and a set one speaks plain HTTP to the
// standalone, exactly as before. The token is KMS-sourced; never logged.
func buildMeteringClient(cfg *cloud.Config, log luxlog.Logger) *meter.Client {
	base := cfg.CommerceHTTPURL
	var httpClient meter.Doer
	inProcess := cfg.Enabled("commerce")
	if inProcess {
		if base == "" {
			base = transport.PlaceholderBase
		}
		httpClient = transport.Client(0) // in-process dispatch; no network timeout
	}
	m, err := meter.New(meter.Config{
		BaseURL:    base,
		HTTPClient: httpClient, // nil off the co-resident path → metering builds its own
	})
	if err != nil {
		// Only an unparseable URL reaches here. The client without one asks the
		// process that owns the ledger over the plane, so the gate still weighs every
		// billable call and still refuses one it cannot weigh.
		log.Error("billing: invalid commerce URL, the gate asks the ledger over the plane", "err", err)
		m, _ = meter.New(meter.Config{})
	}
	// Every cap check that reached no verdict refused the call; say so.
	meter.OnCapError = func(err error) {
		log.Warn("spend cap unreadable, call refused", "err", err)
	}
	if m.Enabled() {
		log.Info("billing gate enabled", "commerce", boolStr(inProcess, "in-process", "http:"+base))
	} else {
		log.Info("billing gate asks the ledger over the plane (no commerce URL)")
	}
	return m
}

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}

// installFinance constructs the ONE in-process finance ledger (per-org SQLite
// double-entry prepaid wallet), publishes it for every money consumer to resolve by
// the narrow finance.Client, and installs the embedded ai router's balance-read hook
// so the PREPAID gate reads it DIRECTLY — a typed in-proc call, no HTTP, no socket.
// There is NO exempt path (hanzoai/ai >= v1.805.8): every
// principal is gated on a positive prepaid balance, fail-closed. MUST run before
// ai.Use (the ai gate reads the hook per request; the hook must be installed first)
// — which BuildDeps guarantees (deps are built before UseAll).
func installFinance(cfg *cloud.Config, deps cloud.Deps, log luxlog.Logger) {
	if !cfg.Enabled("commerce") {
		return // money layer not co-resident (split-deploy); ai falls back to HTTP.
	}
	fin := finance.New(CustomerBooks(deps))
	finance.Publish(fin)
	// Money is billed to the SUBJECT's wallet, inside the org's ledger.
	//
	// org  = which ledger (the tenant's books)
	// subject = which wallet in it (ai resolves it: a person => "org/name", an
	//           org-owned application/service key => the org's own account)
	//
	// So a personal account has a PERSONAL balance and a personal plan, and an org
	// pays for what its applications and service keys spend — which is the product:
	// sign up as yourself, then stand up an org whose users are your customers.
	//
	// Keying both hooks on the org collapsed every member onto the tenant's pool
	// wallet: every new signup lives in "hanzo", so a brand-new $0 account read
	// HANZO's balance and sailed through the gate — we enforced our own wallet.
	//
	// The invariant that must never break: the gate READ and the usage DEBIT key on
	// the SAME wallet, or spend can outrun the balance that admitted it. Both use
	// subject. The gate reads a coarse cents balance (a >0 threshold only); the DEBIT
	// (apps/ai debit, through metering) is 18-decimal-exact.
	cloud.SetBalanceReader(func(ctx context.Context, subject, namespace, currency string) (int64, error) {
		bal, err := fin.Balance(ctx, namespace, subject, currency, false)
		if err != nil {
			return 0, err
		}
		return bal.Cents(), nil
	})
	log.Info("finance ledger wired (per-subject wallet in the org ledger, 18-decimal-exact, fail-closed)", "dataDir", cfg.DataDir)
}

// pickKMSClient resolves deps.KMS. When the kms subsystem is co-resident
// (Enabled("kms")) it returns the IN-PROCESS Client backed by the embedded
// luxfi/kms SecretStore under CLOUD_DATA_DIR — no external RPC. A store-open
// failure is NOT fatal to the whole binary: it falls back to the disabled stub
// (fail-closed) and logs, so a bad data dir degrades KMS rather than crashing
// every subsystem. Absent co-residency the legacy ZAP-RPC + disabled fallbacks
// apply (out-of-process KMS, or not wired).
//
// The subsystem id is "kms" (clients/kms registers it with cloud.HealthOwner so
// the generic liveness route never shadows its real /v1/kms/health, and registers
// the client factory this gate calls); this gate keys on the same id so "enabled"
// is one concept.
func pickKMSClient(cfg *cloud.Config, dur *tenure.Durability, log luxlog.Logger) secret.Store {
	if cfg.Enabled("kms") {
		// The embedded-client constructor is registered by clients/kms in init()
		// (RegisterKMSClientFactory). cloud never imports clients/kms, so the KMS
		// library and its /v1/kms subsystem live in one package with no cloud⇄kms
		// import cycle. Absent the registration (clients/kms not linked into this
		// binary) KMS fails closed rather than pretending to host secrets.
		if cloud.KMSClientFactory() == nil {
			log.Error("deps.KMS: kms enabled but no client factory registered (clients/kms not linked); failing closed")
			return secret.Disabled()
		}
		c, err := cloud.KMSClientFactory()(cfg, dur, log)
		if err != nil {
			log.Error("deps.KMS: embedded KMS unavailable, failing closed", "err", err)
			return secret.Disabled()
		}
		return c
	}
	// The store is not in this process, which is the normal case: exactly one holds
	// it. Ask that one over the internal plane.
	//
	// There is no second way to reach it. CLOUD_KMS_ZAP_ADDR used to select
	// KMSRPCAt, a stub whose every method returned "not yet wired
	// (zapc-gen pending)" — so configuring it produced a KMS client that failed
	// every call, which is worse than none because it looks configured. The plane
	// is the transport, and it is the only one.
	log.Info("deps.KMS → the kms app over the internal plane")
	return cloud.KMSPeer{}
}

// ---- git-push-to-deploy ----

// ---- first-party service release (push→build→image→CR rollout) ----

// ---- workflow job → runner ----

// ---- git lifecycle event stream ----
//
// One event, many subscribers. push-to-deploy (OnGitPush) is the deploy
// subscriber-of-record and stays exactly as it is; this client generalizes the SAME
// inversion to N reactors (mirror-out, Slack-notify, …) so git/platform EMIT a
// lifecycle fact and never import the subscribers. It is deliberately SEPARATE
// from OnGitPush — the deploy path is single-registrant and synchronous, this
// stream is many-registrant and best-effort — so adding a reactor can never
// perturb push→deploy.

// pickCommerceClient resolves deps.Commerce, the entitlement check. In the process
// that links the commerce plugin it is the in-process client, a direct Go call into
// the embedded commerce datastore, through the factory apps/commerce registers in
// init() (apps/commerce imports apps/plan, which imports cloud, so this package
// cannot name it). Every other process asks that plugin by name over the plane
// (planeCommerce): each app is its own process, and only commerce's links
// commerce. A fleet that runs no commerce answers the plane's ErrNoPeer, an error,
// never a grant.
func pickCommerceClient(cfg *cloud.Config, log luxlog.Logger) cloud.CommerceClient {
	if cloud.CommerceClientFactory() != nil && cfg.Enabled("commerce") {
		log.Info("deps.Commerce → in-process (embedded commerce)", "brand", cfg.Brand)
		return cloud.CommerceClientFactory()(cfg, log)
	}
	return planeCommerce()
}

// publishableKey reports whether apiKey is a read-only PUBLISHABLE key (pk-). The
// Hanzo gateway 403s a publishable key on any WRITE endpoint — "Publishable keys
// can only access read-only endpoints … use a secret key (sk-)". So the COMPLETIONS
// resolver must refuse it (it would only 403 chat), while the EMBED resolver accepts
// it (embeddings ARE read-only). pk- is the IAM key family's read-only member
// (pk-/sk-; clients/admission). This ONE predicate is the split's crux:
// it kept the intermittent-403 bug — a pk- embed key riding the shared completions
// client — from ever recurring, wherever the key comes from.
func publishableKey(apiKey string) bool {
	return strings.HasPrefix(strings.TrimSpace(apiKey), "pk-")
}

// isPlaceholderKey reports whether a configured key is a stand-in rather than a
// credential: an unseeded secret leaves "placeholder-..." where a key belongs,
// and a fixture leaves "test-...". Sending either to the gateway spends the
// round trip to be told it is not a key, and the fallback that would have
// worked is never tried.
func isPlaceholderKey(apiKey string) bool {
	k := strings.TrimSpace(apiKey)
	return strings.HasPrefix(k, "placeholder") || strings.HasPrefix(k, "test-")
}

// pickCompletionsClient resolves deps.AI — the client the agents run path (and
// guide/crm/content/sitegen/code /ask, and knowledge's summaries and ToC pick)
// execute CHAT COMPLETIONS through. There is NO in-process "ai" mount that fills a
// nil deps.AI: inference is the `ai` app, so this returns a concrete client, never nil.
//
// THE DEPLOYMENT'S OWN IDENTITY BUYS COMPLETIONS WHEREVER IT HAS ONE. A completion is
// bought for the org it is for, and only the plane can say which org that is: the
// M2M client crosses it (overPlane) with the caller stated, and ai serves it
// for that org. A static key reaches the model API as its own holder, so every
// completion it carried was named at the gateway after the key's account — the
// embed key's service account, for every org's knowledge summaries — and never after
// the org whose file it was. So the static key buys nothing beside an identity,
// and drives completions, embeddings and reranks only in a deployment with no IAM
// identity, where the operator's key is the one credential there is.
//
// Completions are a WRITE endpoint: a read-only publishable (pk-) key 403s them, so
// this resolver never rides one, and a placeholder key is not a key at all. Order:
//  1. M2M HTTP gateway when a base URL AND the binary's IAM identity are present: the
//     client mints and refreshes a client-credentials token from IAM_CLIENT_ID/SECRET
//     and crosses the plane where an `ai` runs. Secret never logged.
//  2. Static SECRET-key HTTP gateway when there is no IAM identity and a
//     completions-capable (non-pk-) key is configured.
//  3. Fail-closed stub otherwise — a run records an honest error, never fakes one.
//
// There is no rung for a static agent key: CLOUD_AGENT_KEY belonged to an IAM user,
// and when the user was deleted every completion arrived as a key IAM refused.
func pickCompletionsClient(cfg *cloud.Config, log luxlog.Logger) infer.Client {
	// THE PEER IS REACHED OVER ITS OWN SOCKET, and the address is its NAME.
	//
	// `ai` is a plugin of this same binary running as its own process, and its
	// routes ride its unix socket exactly as they ride a public listener. The
	// configured alternative was the pod's OWN public address, so a completion
	// left through Cloudflare and came back — and, because that listener demands
	// a credential, the pod minted an OAuth token to authenticate to its own
	// deployment. Both were consequences of addressing a peer by URL.
	//
	// Which transport a process gets is decided by WHAT IT IS: !Enabled(ai) means
	// this process does not carry the app, which is exactly when `ai` is a
	// sibling. The process that IS `ai` never routes inference back through here.
	via, base := cloud.AIRoute(cfg)
	if base != "" && cfg.AIAuthClientID != "" && cfg.AIAuthClientSecret != "" {
		if tokenURL := aiM2MTokenURL(cfg); tokenURL != "" {
			log.Info("deps.AI (completions) → IAM M2M", "at", base, "socket", via != nil,
				"token_url", tokenURL, "client_id", cfg.AIAuthClientID, "default_model", aimodel.DefaultModel)
			return infer.HTTPM2MOn(base, tokenURL, cfg.AIAuthClientID, cfg.AIAuthClientSecret, defaultModelFor, via)
		}
	}
	key := strings.TrimSpace(cfg.AIAPIKey)
	if base != "" && key != "" && !publishableKey(key) && !isPlaceholderKey(key) {
		log.Info("deps.AI (completions) → static secret key (no IAM identity)", "at", base, "socket", via != nil, "default_model", aimodel.DefaultModel)
		return infer.HTTPOn(base, key, defaultModelFor, via)
	}
	if key != "" && publishableKey(key) {
		log.Info("deps.AI (completions) → refusing read-only publishable (pk-) key for chat", "at", base)
	}
	log.Info("deps.AI (completions) → disabled (no IAM M2M identity, no secret key, no gateway configured)")
	return infer.Disabled()
}

// pickEmbedClient resolves deps.Embed — the client code-index, knowledge and search
// run EMBEDDINGS and RERANKS through. It takes the deployment's own identity first,
// for the reason completions do (pickCompletionsClient): the M2M client crosses the
// plane carrying the org a file or a query belongs to, where a static key reached the
// gateway as its own holder — the embed key's service account, for every org's
// passages. Order:
//  1. M2M HTTP gateway when a base URL AND the binary's IAM identity are present.
//  2. Static-key HTTP gateway when there is no identity and a key is configured. A
//     read-only publishable (pk-) key may serve here, since both calls are reads.
//  3. Otherwise the completions resolution (fail-closed stub).
func pickEmbedClient(cfg *cloud.Config, log luxlog.Logger) infer.Client {
	via, base := cloud.AIRoute(cfg)
	if base != "" && cfg.AIAuthClientID != "" && cfg.AIAuthClientSecret != "" {
		if tokenURL := aiM2MTokenURL(cfg); tokenURL != "" {
			log.Info("deps.Embed → IAM M2M", "at", base, "socket", via != nil,
				"token_url", tokenURL, "client_id", cfg.AIAuthClientID, "default_model", aimodel.DefaultModel)
			return infer.HTTPM2MOn(base, tokenURL, cfg.AIAuthClientID, cfg.AIAuthClientSecret, defaultModelFor, via)
		}
	}
	if key := strings.TrimSpace(cfg.AIAPIKey); base != "" && key != "" && !isPlaceholderKey(key) {
		log.Info("deps.Embed → static key (no IAM identity)", "at", base, "socket", via != nil, "default_model", aimodel.DefaultModel)
		return infer.HTTPOn(base, key, defaultModelFor, via)
	}
	return pickCompletionsClient(cfg, log)
}

// aiM2MTokenURL resolves IAM's client_credentials endpoint the agent runner mints
// its M2M inference token at. It MUST be reachable FROM INSIDE THE CLUSTER: the
// runner runs in-cluster and the public issuer host (https://hanzo.id) is fronted
// by Cloudflare, which 403s a server-side (non-browser) loopback POST with edge
// error 1006 — so minting against the PUBLIC issuer URL fails and every
// POST /v1/agent/:ref/run 502s. Measured: an in-cluster POST to
// https://hanzo.id/v1/iam/oauth/token answers 403/1006, http://iam.hanzo.svc/... 200.
// This mirrors the KMS login-broker resolution (apps/kms/mount.go) exactly — one
// split-horizon policy, no drift. Prefer, in order: an explicit override
// (CLOUD_AI_IAM_TOKEN_URL), the in-cluster IAM service base (IAM_URL — already
// wired to http://iam.hanzo.svc for JWKS), then the public issuer as a last resort
// (single-process / no split-horizon deploys). Returns "" only when no identity is
// resolvable, which keeps the M2M branch off (caller falls through to the stub).
func aiM2MTokenURL(cfg *cloud.Config) string {
	if override := strings.TrimSpace(os.Getenv("CLOUD_AI_IAM_TOKEN_URL")); override != "" {
		return override
	}
	if base := iam.BaseURL(cfg.IAMIssuer); base != "" {
		return base + "/v1/iam/oauth/token"
	}
	return ""
}

// durableBucket holds every org's HA-SQLite snapshot (and its writer lease). One
// bucket, keys laid out orgs/<slug>[/…]/<subsystem>.db per HIP-0302 — the durable
// twin of the on-disk DataDir layout.
//
// OPERATIONAL REQUIREMENTS the fence depends on (enforce in the S3 deployment,
// not in code):
//
//   - Object versioning + a no-expiry / no-lifecycle-deletion policy on this prefix.
//     The writer lease (orgs/<slug>/.owner) is the round's system of record; if the
//     gateway silently drops or rolls back that object, a monotone round can reset and
//     un-fence a zombie writer (Red M4). A local high-water-round floor per pod is the
//     future in-process defense; the object lifecycle is the operational one.
//   - A per-pod DataDir (an emptyDir, or an RWO claim) — NEVER an RWX shared volume
//     (Red M5). Two pods on one DataDir corrupt SQLite regardless of this fence;
//     single-writer here is the durable-copy fence, and a per-pod disk is the
//     local-file guarantee the shard router already relies on (see shardrouter.go).
//     The disk is a cache of this bucket: an empty one learns what exists by listing
//     it (OrgStore.Has, Stored, and the first sweep).
const durableBucket = "org-db"

// durableProbePrefix namespaces the boot CAS-atomicity probe's throwaway objects
// (org.ProbeCAS writes one per boot) away from the orgs/ tree. A bucket lifecycle rule
// may reap ".probe/*"; the objects are tiny and never read after the probe.
const durableProbePrefix = ".probe/cas-"

// reachable separates "the store answered and said no" from "the store was not
// there". Only the second makes the probes that follow pointless, so only the
// second short-circuits them — a store that refuses one call may still enforce
// the atomicity the next one asks about.
//
// A deadline that expired IS unreachable for this purpose: something that cannot
// answer inside the boot bound is not something boot should keep waiting on.
func reachable(deadline error, err error) bool {
	if deadline != nil {
		return false
	}
	if _, ok := errors.AsType[net.Error](err); ok {
		return false
	}
	return !errors.Is(err, syscall.ECONNREFUSED) && !errors.Is(err, context.DeadlineExceeded)
}

// bootReach bounds every object-store read on the startup path. See the comment
// at its use in the durability setup for why it is short.
const bootReach = 2 * time.Second

// declared is how many writers this deployment may run: the replica count its
// Deployment declares (CLOUD_REPLICAS, the autoscaler's ceiling where there is one)
// or the pods a static CLOUD_PEERS names, whichever is more.
func declared(cfg *cloud.Config) int {
	return max(len(parsePeers(cfg.ShardPeers)), cfg.Replicas)
}

// peered reports whether this deployment may run MORE THAN ONE writer.
//
// It is read off what the deployment DECLARES and never off who is live. A pod's
// view of the live set can be wrong (a KV that refuses writes, a heartbeat that does
// not land, a boot before the set was read), and a pod that took a view of itself
// alone for proof it was alone would acknowledge every org write, run every loop
// and redeem every single-use token beside a peer doing the same. KV_URL alone declares
// nothing: a deployment of one pod with a KV is one writer, and reading the KV's
// presence as peers took Base and every org's acknowledgement away from exactly
// that deployment. The live set routes requests and elects org owners; the object
// store fences them.
//
// It is a FUNCTION and not an expression at the call site because it is a rule
// about money and a rule nothing can call is a rule nothing can test.
func peered(cfg *cloud.Config) bool {
	return declared(cfg) > 1
}

// buildCoord is this process's place in the deployment (sdk/coord): over its
// shared KV when KV_URL names one, and the process's default from here on. It never
// joins the live set: the pod's root does that (cmd/cloud), so an app process the
// host reaps when idle cannot take the pod out of the set with it. It reads the set
// and leads the loops this app runs.
//
// A KV_URL that cannot be used is not a reason to answer alone: pods that each
// believe they are alone each run every loop, so the Coord is Broken and Serve will
// not listen.
func buildCoord(cfg *cloud.Config, log luxlog.Logger) *coord.Coord {
	cc := coord.Env()
	cc.URL = cfg.KVURL
	cc.Self.ID = selfID(cfg)
	cc.Secret = masterKeyBytes(cfg)
	cc.Declared = declared(cfg)
	cc.Log = log
	c, err := coord.New(cc)
	if err != nil {
		log.Error("coord: KV_URL cannot be used", "err", err)
		c = coord.Broken(err)
	}
	c.Start(context.Background())
	coord.SetDefault(c)
	return c
}

// buildDurability constructs the deployment's HA-durability factory, or nil when the
// deployment has no object store to be durable against (dev/single-node — every
// OrgStore then stays local-only). It composes the object store's If-Match
// ConditionalStore (the SAME store deps.VFS uses), the writer membership
// over CLOUD_PEERS (the SAME set the shard router elects on, so the store-layer owner
// and the routed owner agree), and the per-org envelope Cipher rooted at the KMS
// master. Any construction failure fails SAFE to nil (local-only) rather than crash
// the boot; an encryption-capable build with no usable cipher is REFUSED — a build
// that promises encryption never ships plaintext snapshots to the object store.
// It returns the durable factory AND the live-members reader for the shard router (the
// SAME election snapshot), non-nil together only when the plane is active; both nil when
// local-only (the router then stays on the static ordinal set).
func buildDurability(cfg *cloud.Config, c *coord.Coord, log luxlog.Logger) (*tenure.Durability, func() []ha.Member) {
	// A multi-replica deployment REQUIRES the durable plane: with >1 writer, a per-org
	// store that is not hydrate-on-open + fenced is the outage this exists to fix.
	// disabledDurability logs at the severity the replica count warrants, so a
	// misconfigured prod deployment is never SILENTLY non-durable (Red L2).
	multiReplica := peered(cfg)

	// bootReach is how long ANY of this may wait on the object store.
	//
	// Both reads below are optional in the sense that matters: a store that does
	// not answer means local-only, which is a supported posture the next few
	// lines take deliberately. Neither is a reason to delay serving the API.
	//
	// The old bounds were 10s and 15s, and they were sized for "how long might a
	// healthy store take" when the question is "how long do we wait before
	// concluding it is not there". A store that is local or one hop away answers
	// in milliseconds; one that is DOWN refuses instantly and one that is HUNG
	// never answers at all, so a long bound only ever buys the hung case, and
	// pays for it on every boot. Measured against a dead store, these two plus
	// the console read cost a child 34.5 seconds before it began listening.

	// Durability is THE path — there is no operator toggle. It self-detects capability
	// at boot: no object store reachable (dev / native-Go / no S3 creds) → local-only,
	// same code path, graceful; a reachable store → PROVE its conditional-PUT atomicity
	// (org.ProbeCAS) before fencing a single byte of tenant data. A store that cannot be
	// proven atomic fails SAFE to local-only with a loud alert — never a silent-wrong
	// fence on a store that could split-brain.
	if err := object.Check(); err != nil {
		disabledDurability(log, multiReplica, fmt.Sprintf("object store unavailable: %v", err))
		return nil, nil
	}
	if object.IsLocal() {
		// A fence is a compare-and-set on a store every replica shares; local disk is
		// this pod's own volume, and is already the durable copy of itself.
		disabledDurability(log, multiReplica, "the object store is local disk (S3_ADMIN_* unset)")
		return nil, nil
	}
	store := object.Default()
	// NOTHING ON THIS PATH WAITS ON THE OBJECT STORE. The bucket ensure that stood
	// here was the same pure optimisation the VFS constructor carried — idempotent,
	// its error logged and continued past, and retried by the first ship or hydrate
	// that needs it — so all a boot copy could add was the wait.
	//
	// The atomicity probe below is NOT that, and the difference is why it moved
	// rather than went: it is the safety property, and no byte of tenant data may be
	// fenced against a store that has not answered it. It is asked by the first
	// store that wants to be durable instead of by every process at startup, which
	// is the same guarantee at a different moment — a probe nobody's data is waiting
	// on is a probe nothing needs to block a listener.
	// Prove the store enforces conditional-PUT atomically BEFORE fencing any tenant data
	// (the auto-H2 self-check that replaces the old opt-in flag). A store that cannot be
	// proven atomic fails SAFE to local-only + a loud alert — never a silent fence on a
	// store that could admit two writers for one round (split-brain). The result is
	// cached for the process life (deps.Durable is set once), so this probe runs once.
	// cond is the linearizable register the fences stand on: every lease this
	// deployment holds — an org's writer (sdk/tenure) and each loop's runner
	// (sdk/coord) — is a compare-and-set on it, and every read of one must be
	// the store's own answer. A cache in front of it would show a holder a lease it
	// has already lost and a taker a version that has already changed, and the term
	// arithmetic both fences rest on assumes neither happens. So nothing sits on this
	// path: a hydrate-read cache, if one is ever wanted, wraps the data reads, never
	// cond.
	objects := tenure.NewConditional(store, durableBucket)
	cond := proven(objects, log)
	// The same register leases the deployment's loops, so a loop changes hands under
	// the same compare-and-set an org does, and its round outlives anything the KV
	// loses.
	c.SetStore(cloud.LeaseStore{ConditionalStore: cond})

	// Membership: LIVE when the pods share a KV (a rolling upgrade's changing pod set is
	// tracked, a draining/dead pod is never elected an org's owner), else the STATIC
	// CLOUD_PEERS/self set (see membership_writers.go). A single-pod deployment with no peers is its own sole writer.
	// The 2s refresh keeps a drained pod out of every peer's election within a bound the
	// terminationGracePeriod covers, so a rolling handoff loses no request.
	self := selfID(cfg)
	peers := parsePeers(cfg.ShardPeers)
	if len(peers) == 0 {
		peers = []tenure.Member{{ID: self, Addr: self}}
	}
	src := cloud.MembershipSource(peers, c)
	members := tenure.NewMembership(self, src, 2*time.Second)
	_ = members.Start(context.Background()) // initial refresh populates Members() before first request

	cipher := durableCipher(cfg, log)
	if cipher == nil && sqlitedrv.EncryptionAvailable() {
		// The master that keys the local file must decode here too, so this is a
		// genuine misconfig, not a dev path: never ship plaintext snapshots AND never
		// silently drop durability — fail closed and log LOUDLY for the replica count.
		disabledDurability(log, multiReplica, "encryption-capable build but no durable cipher (would ship plaintext snapshots)")
		return nil, nil
	}

	log.Info("durability enabled", "bucket", durableBucket, "self", self, "peers", len(peers), "atomic_cas", true, "encrypted", cipher != nil)
	// members.Members is the live election snapshot; hand it to the shard router so it
	// routes on the SAME set the fencer elects over — the store-layer owner and the routed
	// owner never disagree.
	//
	// WithSeal hands the ship the crypto client: on the pure-Go envelope backend
	// sqlitedrv.Checkpoint re-encrypts the real path so the ship reads fresh bytes and not
	// the last-sealed ciphertext (which would be a lost acked write on takeover); on the
	// write-time backends it is a successful no-op, so it is passed unconditionally.
	//
	// WithReader hands it orgReader, the read-only handle a ship pins its file with, so the
	// store's own connection is free while the file is copied and the copy reads what the
	// fold left there.
	// WithWriters: a deployment of one writer has no one to depose a replica, so an
	// owner asks the object store nothing (Durable.Owned). WithLive: where pods share a
	// KV, an owner trusts the store's last answer only while its own heartbeat is in
	// the live set its peers read.
	opts := []tenure.DurabilityOption{
		tenure.WithSeal(sqlitedrv.Checkpoint),
		tenure.WithReader(cloud.OrgReader),
		tenure.WithWriters(declared(cfg)),
		tenure.WithLister(objects),
	}
	if c.Shared() {
		opts = append(opts, tenure.WithLive(c.Live))
	}
	return tenure.NewDurability(cond, members, cipher, opts...), members.Members
}

// disabledDurability records that the durable plane is OFF, at ERROR when the
// deployment is multi-replica (per-org stores then survive only via shard routing +
// per-pod RWO PVC; a lost/rescheduled PVC loses committed data — the operator MUST
// configure S3) and at INFO when single-replica/dev (local-only is the expected
// posture). One place, so no disabled path is silent on a deployment that needs HA.
func disabledDurability(log luxlog.Logger, multiReplica bool, why string) {
	if multiReplica {
		log.Error("DURABILITY DISABLED on a MULTI-REPLICA deployment — per-org stores survive only via shard routing + per-pod RWO PVC; a lost/rescheduled PVC loses committed data. Configure S3_ADMIN_* to enable the durable plane.", "reason", why)
		return
	}
	log.Info("durability disabled — single-replica/dev, per-org stores stay local-only", "reason", why)
}

// durableCipher builds the per-org envelope Cipher from the base64 KMS master
// (CLOUD_KMS_MASTER_KEY_REF — the SAME key cek derives from). nil when no valid
// 32-byte master is configured (pure-Go dev → plaintext durable object, matching the
// plaintext local file).
func durableCipher(cfg *cloud.Config, log luxlog.Logger) *tenure.Cipher {
	ref := strings.TrimSpace(cfg.KMSMasterKeyRef)
	if ref == "" {
		return nil
	}
	master, err := base64.StdEncoding.DecodeString(ref)
	if err != nil {
		log.Warn("durable cipher: KMS master key ref is not valid base64", "err", err)
		return nil
	}
	c, err := tenure.NewCipher(master)
	if err != nil {
		log.Warn("durable cipher: invalid KMS master key", "err", err)
		return nil
	}
	return c
}

// selfID is THIS process's stable id: the StatefulSet ordinal (CLOUD_POD_NAME /
// POD_NAME, already resolved onto cfg.ShardSelf) falling back to the OS hostname.
// ONE resolver — the durability membership elects on it and Deps.Self reports it, so
// the id an operator reads in a status IS the id the ring routes by. Two resolutions
// that drifted would name the same pod two different things at the worst moment.
func selfID(cfg *cloud.Config) string {
	return cmp.Or(strings.TrimSpace(cfg.ShardSelf), hostnameOr("cloud-0"))
}

// hostnameOr returns the OS hostname, or def when unavailable — a stable self id for
// a single-pod deployment that sets no CLOUD_POD_NAME.
func hostnameOr(def string) string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return def
}

// UseMetrics adapts hanzoai/o11y/metrics into a UseFunc. metrics declares its
// OWN narrow Deps (Logger, DataDir, Brand, Org) and does not import hanzoai/cloud,
// so Typed cannot bridge it; this builds that Deps from cloud's and calls
// metrics.Mount.
//
// The package used to be github.com/hanzoai/metrics, which is retired: its NOTICE
// named hanzoai/o11y its successor while this line still imported the archive, so
// eleven live routes and their store sat in a read-only repository. The code moved
// to the successor (o11y v1.5.67) and the import followed it. The capability did
// not move — metrics is still its own row, its own document and its own binary;
// what changed is which module ships the surface.
//
// Org is the load-bearing one, and it points the other way: metrics used to
// decide its own tenant by reading X-Org-Id, which is a header a caller sends,
// so an anonymous request could read and write any org's telemetry. The org
// rule belongs to the boundary that authenticates and lives once, in
// principal.Org; handing it down is how a module that cannot import cloud still
// applies cloud's rule instead of a weaker copy of it.
//
// It lives here rather than in package apps for exactly CtxShutdown's reason: an
// apps-local identifier in a Wire entry is unreachable from the generated
// plugin/<app>/main.go, so metrics fell back to the stub that links every subsystem.
// The move is affordable because it is nearly free — the package imports only
// zap-proto/zip and luxfi, which cloud's core already carries, so every binary
// grows by the surface itself and not by the o11y runtime beside it in that module.
// It is the ONLY one of the four mount adapters that is: commerce would add 527
// packages to the core, and zen (via hanzoai/ai/controllers) and ai import
// hanzoai/cloud — a cycle, not a weight.
//
// hanzoai/o11y/metrics registers its own addresses whole, so they are declared on
// a definition of their own and composed through the grant, which refuses any
// address outside /v1/metrics.
func UseMetrics(app cloud.Router, deps cloud.Deps) error {
	if err := metrics.Use(app.Group(""), metrics.Deps{
		Logger: luxlog.Default(), DataDir: cloud.DataDir(), Brand: cloud.Brand(),
		Org: principal.Org,
	}); err != nil {
		return err
	}
	describeMetrics()
	return nil
}

// The prose for the operations this repo PUBLISHES but does not REGISTER.
//
// Every other surface declares its own: a typed op carries prose in a doc comment
// zipdoc lifts, and an untyped route declares it with openapi.Describe beside the
// route table. Two subsystems can do neither, and both for the same reason — the
// module that owns the route cannot reach this registry:
//
//   - hanzoai/o11y/metrics registers /v1/metrics/* itself and imports ONLY
//     zap-proto/zip + luxfi, deliberately (mount.go's own argument: it depends on
//     the three things it uses). Importing hanzoai/cloud to describe itself would
//     give that up to buy prose.
//
// the licensing module is no longer such a case: since v0.1.10 it types its own ops,
// and a typed op carries its prose in the handler's doc comment, which zipdoc lifts.
//
// Called from UseMetrics, never from init: prose keyed to an address is only
// true of a document that also carries the route, and every app in this repo links
// this package while only one mounts these eleven. Registered at init, the other
// apps' documents each grew eleven descriptions naming nothing — which is the one
// thing the surface gate refuses, so no app document could be generated at all.
func describeMetrics() {
	// ── metrics: one native store, three signals ──────────────────────────────
	//
	// The tenancy rule is repeated on each operation on purpose: each description
	// is read ALONE, as one SDK method's doc or one CLI command's help.

	spec.Describe("/v1/metrics/health", http.MethodGet,
		"How many metric series this deployment holds for your org",
		"Reports the native metrics store's live state for the calling tenant: the subsystem "+
			"version, the resolved `org`, and `series` — the number of distinct series actually "+
			"held right now, read out of the store rather than a constant. It is not a dependency "+
			"probe and has nothing downstream to fail on: the store is in-process, so this answers "+
			"200 whenever the process is up.\n\n"+
			"The tenant is the gateway-minted `X-Org-Id` header, falling back to the deployment "+
			"brand and then `default`. This surface trusts the edge rather than re-deriving the org "+
			"from a validated claim of its own, so it belongs behind the gateway and nowhere else.")

	spec.Describe("/v1/metrics/batch", http.MethodPost,
		"Ingest a MetricBatch — the same payload the ZAP transport carries",
		"Writes every sample in a luxfi/metric `MetricBatch` into the calling org's store and "+
			"answers `{written}`: the number of SAMPLES stored, not families and not metrics. This "+
			"is the exact wire shape the ZAP `MsgMetricBatch` transport carries, so the HTTP endpoint "+
			"and the optional ZAP push receiver share one code path and one meaning — the transport "+
			"is an optimisation, never a different contract.\n\n"+
			"A counter or gauge lands as one sample. A histogram or summary contributes DERIVED "+
			"`<name>_sum` and `<name>_count` series, so one metric can write more than one sample "+
			"and `written` can exceed the number of metrics you sent. The batch's own "+
			"`TimestampNs` stamps every sample it carries.\n\n"+
			"The tenant is the gateway-minted `X-Org-Id` header, falling back to the deployment "+
			"brand and then `default`; each org gets its own store, WAL-durable under the "+
			"deployment's data dir. A body that does not decode is 400.")

	spec.Describe("/v1/metrics/write", http.MethodPost,
		"Append samples to your org's named, labelled series",
		"Takes `{series:[{name, labels, samples:[{t, v}]}]}`, appends every sample, creating each "+
			"series on first write, and answers `{written}` — again counting SAMPLES, so three "+
			"series of ten samples is 30.\n\n"+
			"A series is identified by its name PLUS its whole label set, so adding one label makes "+
			"a different series rather than annotating an existing one. Timestamps `t` are "+
			"NANOSECONDS since the Unix epoch; a sample sent without one is stored at 0 and is then "+
			"excluded by any query that sets a lower bound, which is the usual reason a write that "+
			"reported success does not read back. Retention is per series and bounded — past 65536 "+
			"samples the oldest are evicted.\n\n"+
			"The tenant is the gateway-minted `X-Org-Id` header, falling back to the deployment "+
			"brand and then `default`. A body that does not decode is 400; nothing else is "+
			"validated or rejected.")

	spec.Describe("/v1/metrics/query", http.MethodGet,
		"Read your org's series back over a time range",
		"Answers `{count, series}`, where `count` is the number of matching SERIES and each series "+
			"carries the samples that fall inside the window. `name` selects one series name, and "+
			"an absent or empty `name` returns every series the org holds. `match` is a "+
			"`k=v,k2=v2` label matcher applied as a SUPERSET test: a series matches when it carries "+
			"all the named labels with those values, extra labels and all.\n\n"+
			"`start` and `end` are nanoseconds since the Unix epoch, and here is the rule worth "+
			"knowing: a bound that is absent, empty or unparseable becomes 0, which this store "+
			"reads as UNBOUNDED. A malformed `start` therefore silently widens the query instead of "+
			"failing it. There is no limit parameter — the window and the matcher are the whole of "+
			"what bounds the answer.\n\n"+
			"The tenant is the gateway-minted `X-Org-Id` header, falling back to the deployment "+
			"brand and then `default`, so a query can only ever read the org the edge asserted.")

	spec.Describe("/v1/metrics/logs/health", http.MethodGet,
		"How many log records this deployment holds for your org",
		"Reports the native log store's live state for the calling tenant: the subsystem version "+
			"and `records`, the count actually held right now rather than a constant. Not a "+
			"dependency probe — the store is in-process, so this answers 200 whenever the process "+
			"is up.\n\n"+
			"The tenant is the gateway-minted `X-Org-Id` header, falling back to the deployment "+
			"brand and then `default`.")

	spec.Describe("/v1/metrics/logs/write", http.MethodPost,
		"Append structured log records for your org",
		"Takes `{records:[{t, level, body, labels}]}`, appends each one, and answers `{written}`. "+
			"Bodies are stored verbatim; `labels` are the indexed dimensions a query filters on, so "+
			"what you do not label you can only find by substring.\n\n"+
			"`t` is NANOSECONDS since the Unix epoch. A record sent without one is stored at 0 and "+
			"then falls outside any query carrying a lower bound — the usual reason a successful "+
			"write does not read back. Retention is a bounded ring, 1048576 records per org, oldest "+
			"evicted first. No record is validated or rejected, so `written` is the number of "+
			"records SENT; only a body that does not decode at all is 400.\n\n"+
			"The tenant is the gateway-minted `X-Org-Id` header, falling back to the deployment "+
			"brand and then `default`; each org's records live in its own WAL-durable store.")

	spec.Describe("/v1/metrics/logs/query", http.MethodGet,
		"Search your org's logs by label, time and substring",
		"Answers `{count, records}`, newest first. `match` is the same `k=v,k2=v2` superset label "+
			"matcher the metrics query uses; `contains` is a case-insensitive substring test "+
			"against the record body; `start` and `end` are nanosecond bounds.\n\n"+
			"A bound that is absent, empty or unparseable becomes 0, which means UNBOUNDED — a "+
			"malformed `start` widens the search rather than failing it. `limit` caps the page and "+
			"defaults to 100 when absent or non-positive, so an unfiltered read is never the whole "+
			"ring.\n\n"+
			"The tenant is the gateway-minted `X-Org-Id` header, falling back to the deployment "+
			"brand and then `default`, so a search can only reach the org the edge asserted.")

	spec.Describe("/v1/metrics/traces/health", http.MethodGet,
		"How many spans this deployment holds for your org",
		"Reports the native trace store's live state for the calling tenant: the subsystem version "+
			"and `spans`, the count actually held right now. Not a dependency probe — the store is "+
			"in-process, so this answers 200 whenever the process is up.\n\n"+
			"The tenant is the gateway-minted `X-Org-Id` header, falling back to the deployment "+
			"brand and then `default`.")

	spec.Describe("/v1/metrics/traces/write", http.MethodPost,
		"Append spans for your org",
		"Takes `{spans:[{traceId, spanId, parentId, name, startNs, endNs, attrs}]}`, appends each, "+
			"and answers `{written}` — the number of spans sent. Every span is indexed by its trace "+
			"id as it lands, which is what makes the waterfall read possible without a second "+
			"store.\n\n"+
			"Times are NANOSECONDS since the Unix epoch. Retention is a bounded ring of 1048576 "+
			"spans per org: past that the OLDEST are evicted to keep the newest 1048576, and the "+
			"trace index is rebuilt — so a long-lived trace can lose its early spans while its "+
			"later ones survive, and a waterfall read is best-effort against retention, not a "+
			"guarantee.\n\n"+
			"The tenant is the gateway-minted `X-Org-Id` header, falling back to the deployment "+
			"brand and then `default`. A body that does not decode is 400.")

	spec.Describe("/v1/metrics/traces/trace", http.MethodGet,
		"Every span of one trace — the waterfall",
		"Answers `{spans}`: every span the org holds for the trace id in `id`, in the order they "+
			"were appended, which is what a waterfall view renders. Unlike the other reads there is "+
			"no count, no time range and no limit — a trace is addressed by id or not at all.\n\n"+
			"An id with no spans answers an EMPTY list, never a 404: the store cannot tell a trace "+
			"that never existed from one whose spans retention has already dropped, so it does not "+
			"pretend to. The tenant is the gateway-minted `X-Org-Id` header, falling back to the "+
			"deployment brand and then `default`, and a trace id belonging to another org is simply "+
			"not in this org's store.")

	spec.Describe("/v1/metrics/traces/query", http.MethodGet,
		"Recent spans for your org over a time range",
		"Answers `{count, spans}`, newest first, filtered on each span's START time. `start` and "+
			"`end` are nanosecond bounds where 0 — which is what an absent, empty or unparseable "+
			"value becomes — means UNBOUNDED, so a malformed bound widens the listing instead of "+
			"failing it. `limit` defaults to 100 when absent or non-positive.\n\n"+
			"It lists SPANS, not traces: several spans of one trace each count separately and each "+
			"take a slot against `limit`. Assembling one trace is /v1/metrics/traces/trace. The tenant is "+
			"the gateway-minted `X-Org-Id` header, falling back to the deployment brand and then "+
			"`default`.")
}

// logMaster states how this process resolved its data-plane key. Nothing
// branches on it — but it is the line whose absence once let a boot announce a
// dev key it had not installed, so it reports the resolved source, or the reason
// there is none.
func logMaster(log luxlog.Logger) {
	switch {
	case cloud.MasterErr() != nil:
		log.Warn("data-plane key: NONE — store opens fail closed", "err", cloud.MasterErr())
	case cloud.MasterFrom() == cloud.MasterEnv:
		log.Info("data-plane key: from the environment", "from", cloud.MasterFrom())
	default:
		log.Warn("data-plane key: DEV (nothing configured, and the data directory was empty — dev/CI only)")
	}
}

// masterKeyBytes decodes the base64 KMS master (CLOUD_KMS_MASTER_KEY_REF) into
// the 32 raw bytes a subsystem needs to encrypt its own store. nil on absent or
// malformed — never a partial or wrong-length key, because a wrong key encrypts
// against a store no other key can open.
func masterKeyBytes(cfg *cloud.Config) []byte {
	ref := strings.TrimSpace(cfg.KMSMasterKeyRef)
	if ref == "" {
		return nil
	}
	master, err := base64.StdEncoding.DecodeString(ref)
	if err != nil || len(master) != 32 {
		return nil
	}
	return master
}

// proven is a ConditionalStore that will not answer until the store behind it has
// been shown to enforce conditional-PUT ATOMICALLY, and asks that question at the
// first use rather than at startup.
//
// The property is unchanged and it is the whole point of the type: no byte of
// tenant data is fenced against a store whose atomicity is unproven, because the
// proof runs on the first call and a store that fails it answers nothing, ever.
// What changed is WHO WAITS. Asked at boot, every process paid the probe before it
// listened, including the overwhelming majority that never open a durable store at
// all; asked here, it is paid once, by the first ship or hydrate, which is already
// an operation on that store and already waiting on it.
//
// Only a VERDICT is remembered. The probe ends one of three ways: the store is
// atomic (nil), the store is proven non-atomic (org.ErrNonAtomicStore), or the
// store did not answer — a deadline, a refused connection, a 5xx. The first two are
// facts about the store and hold for the life of the process. The third is a fact
// about that moment: it is returned to the caller and the probe is asked again, no
// sooner than probeRetryMin after the failure and doubling up to probeRetryMax, so
// an unreachable store costs one probe per interval rather than one per call, and a
// store that answers again is used again without a restart.
type provenStore struct {
	store replica.ConditionalStore
	log   luxlog.Logger
	now   func() time.Time

	mu      sync.Mutex
	settled bool          // a verdict is in: err is nil (atomic) or non-atomic, for good
	err     error         // the verdict, or the last failure to reach the store
	retry   time.Time     // earliest time an unsettled probe is asked again
	wait    time.Duration // current retry interval
}

// probeRetryMin and probeRetryMax bound the interval between probes while the
// store does not answer.
const (
	probeRetryMin = time.Second
	probeRetryMax = time.Minute
)

func proven(store replica.ConditionalStore, log luxlog.Logger) replica.ConditionalStore {
	return &provenStore{store: store, log: log, now: time.Now}
}

// ok reports whether the store may be used, probing when no verdict is in and the
// retry interval has passed. Callers are serialized on the probe: one runs at a
// time, and a caller that arrives during it reads its outcome.
//
// The deadline is the CALLER'S. A probe inherits the context of whatever needed the
// store, so a hydrate that may wait ten seconds gets ten seconds and a request that
// may wait one gets one — which is the bound the caller already chose, rather than a
// second one invented here that would have to be wrong for one of them. A probe cut
// short by that deadline leaves no verdict, so it decides nothing for the callers
// after it.
func (p *provenStore) ok(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.settled || p.now().Before(p.retry) {
		return p.err
	}
	err := tenure.ProbeCAS(ctx, p.store, durableProbePrefix)
	switch {
	case err == nil:
		if p.err != nil {
			p.log.Info("durability confirmed", "after", p.err)
		}
		p.settled, p.err = true, nil
	case errors.Is(err, tenure.ErrNonAtomicStore):
		p.settled = true
		p.err = fmt.Errorf("object-store conditional-PUT atomicity NOT confirmed — %w", err)
		p.log.Error("durability REFUSED", "why", p.err)
	default:
		p.wait = min(max(2*p.wait, probeRetryMin), probeRetryMax)
		p.retry = p.now().Add(p.wait)
		p.err = fmt.Errorf("object-store conditional-PUT atomicity unproven — the store did not answer the probe (next probe in %s): %w", p.wait, err)
		p.log.Warn("durability deferred", "why", err, "retry", p.wait)
	}
	return p.err
}

func (p *provenStore) Get(ctx context.Context, key string) ([]byte, string, error) {
	if err := p.ok(ctx); err != nil {
		return nil, "", err
	}
	return p.store.Get(ctx, key)
}

func (p *provenStore) PutIfVersion(ctx context.Context, key string, data []byte, expect string) (string, error) {
	if err := p.ok(ctx); err != nil {
		return "", err
	}
	return p.store.PutIfVersion(ctx, key, data, expect)
}

// defaultModelFor is the runtime answer to "which model when the caller named
// none": the platform's `ai` configuration, edited at admin.hanzo.ai, read on the
// request rather than at boot. DefaultModel is the floor it falls back to, so a
// deployment that has configured nothing behaves exactly as this binary shipped.
func defaultModelFor(ctx context.Context) []string {
	return aimodel.Route(ctx)
}
