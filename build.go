package cloud

import (
	"cmp"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/cloud/apps/commerce/transport"
	"github.com/hanzoai/cloud/apps/metering"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/internal/org"
	"github.com/hanzoai/cloud/openapi"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/ha"
	metrics "github.com/hanzoai/o11y/metrics"
	sqlitedrv "github.com/hanzoai/sqlite"
	s3 "github.com/hanzos3/go"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/finance"
	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/apps/s3admin"
	"github.com/hanzoai/cloud/clients"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/types"
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
//     clients.IsDisabled(err) and log a friendly "dep X needed by Y
//     not configured" message.
//
// JSON does not appear in any of these paths. A co-resident dependency is a
// direct Go method call; a peer that is elsewhere is reached over the peer plane
// (plane.Ask — ZAP bytes on the peer's own socket, addressed by name). JSON
// happens only at the gateway/ingress edge, through the zip jsonenc helper.
func BuildDeps(cfg *Config) Deps {
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
	BootMaster(DataDir())
	logMaster(logger)

	logger.Info(
		"building deps",
		"brand", cfg.Brand,
		"domain", cfg.Domain,
		"iam_issuer", cfg.IAMIssuer,
		"data_dir", cfg.DataDir,
		"enabled", cfg.Enable,
	)

	deps := Deps{
		Brand:     cfg.Brand,
		Version:   cfg.Version,
		Env:       cfg.Env,
		Domain:    cfg.Domain,
		IAMIssuer: cfg.IAMIssuer,
		DataDir:   cfg.DataDir,
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
	deps.Durable, deps.LiveMembers = buildDurability(cfg, logger)
	deps.Peers = peered(cfg)
	deps.IAM = pick(cfg, logger, "iam", "IAM", clients.DisabledIAM)
	deps.KMS = pickKMSClient(cfg, deps.Durable, logger)
	deps.Commerce = pickCommerceClient(cfg, logger)
	// Metering client BEFORE the AI client: deps.AI is wrapped in the metering
	// decorator (the ONE inference gate+meter — no exempt path, no bypass, no
	// side-channel key), which needs deps.Metering. nil-safe — an unconfigured
	// commerce URL yields a !Enabled() client, so the wrap is a transparent
	// pass-through and a dev deployment is never blocked.
	deps.Metering = buildMeteringClient(cfg, logger)
	installTierReader(deps.Metering, logger)
	// AI (completions, WRITE) and Embed (embeddings, READ-ONLY) are DISTINCT
	// credentials by concern: completions never ride the read-only publishable
	// (pk-) key — the gateway 403s a pk- key on any write endpoint — so deps.AI
	// resolves to the M2M identity, while deps.Embed keeps the pk- key (correct
	// least-privilege for a read-only call). Both meter through the ONE commerce path.
	deps.AI = meteredAIClient(pickCompletionsClient(cfg, logger), deps)
	deps.Embed = meteredAIClient(pickEmbedClient(cfg, logger), deps)
	installFinance(cfg, deps, logger)
	deps.O11y = pick(cfg, logger, "o11y", "O11y", clients.DisabledO11y)
	deps.VFS = pickVFSClient(cfg, logger)

	// Payments and Vault never co-resident. Disabled stub when no
	// endpoint, otherwise RPC.

	// Runtime-mutable edge-policy store (/v1/gateway config plane), layered over
	// the static env/flag defaults so an un-provisioned deployment behaves exactly
	// as the static config until an operator PUTs an override. New always returns a
	// working *Store (static-only if the SQLite file can't open), so the edge
	// middleware is never left without a policy source — a store-open error is
	// logged, not fatal.
	gp, err := edge.New(cfg.DataDir, authz.AdminOrg, staticEdgePolicy(cfg))
	if err != nil {
		logger.Warn("gateway policy store degraded to static-only", "err", err)
	}
	deps.GatewayPolicy = gp

	// The edge traffic sensor the abuse gate writes and /v1/gateway/traffic reads.
	// One object per process, hung off deps for the same reason the policy store
	// is: two of them would be two answers to "who is calling", and the middleware
	// and the subsystem would each be sure of a different one.
	deps.Traffic = edge.NewTraffic()

	return deps
}

// staticEdgePolicy projects the static env/flag edge config into the boot-default
// policy the edge.Store layers runtime overrides on top of. A disabled
// per-IP limiter (CLOUD_EDGE_RATELIMIT=false) maps to PerIPRPM 0 (a live no-op).
func staticEdgePolicy(cfg *Config) edge.Policy {
	p := edge.Policy{
		CORSOrigins: cfg.CORSOrigins,
		WindowSec:   cfg.EdgeRateWindowSec,
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
func buildMeteringClient(cfg *Config, log luxlog.Logger) *metering.Client {
	base := cfg.CommerceHTTPURL
	var httpClient metering.HTTPDoer
	inProcess := cfg.Enabled("commerce")
	if inProcess {
		if base == "" {
			base = transport.PlaceholderBase
		}
		httpClient = transport.Client(0) // in-process dispatch; no network timeout
	}
	m, err := metering.New(metering.Config{
		BaseURL: base,
		Token:   cfg.CommerceServiceToken,
		Org:     cfg.Brand, // X-Org-Id default for S2S; per-request org overrides.
		// Honor the documented METERING_TEST env: when "true", route every debit to
		// commerce's TEST/sandbox books (fin.RecordUsage in.Test=true) so a staging /
		// canary deployment records NO real money — and the usage-cap read (org.TestMode
		// via SQUARE_ENVIRONMENT=sandbox) sees the SAME test books. Unset in prod → live,
		// unchanged. Without this the flag was silently ignored (always live).
		Test:       strings.EqualFold(strings.TrimSpace(os.Getenv(metering.EnvTest)), "true"),
		FailOpen:   cfg.BillingFailOpen,
		HTTPClient: httpClient, // nil off the co-resident path → metering builds its own
	})
	if err != nil {
		// Only an unparseable URL reaches here. Fall back to a not-configured
		// client (no-op gate) rather than failing boot over billing wiring.
		log.Error("billing: invalid commerce URL, gate disabled", "err", err)
		m, _ = metering.New(metering.Config{})
	}
	// Observe every cap-check fail-open (timeout / slow / broken commerce) — a cap that
	// silently allows must never be silent. The completion still proceeds (fail-open).
	metering.OnCapError = func(err error) {
		log.Warn("spend-cap check failed open (allowing completion) — commerce authorize slow/unavailable", "err", err)
	}
	if m.Enabled() {
		log.Info("billing gate enabled", "commerce", boolStr(inProcess, "in-process", "http:"+base), "fail_open", cfg.BillingFailOpen)
	} else {
		log.Info("billing gate disabled (no commerce URL)")
	}
	return m
}

func boolStr(b bool, t, f string) string {
	if b {
		return t
	}
	return f
}

// installTierReader installs the embedded ai module's per-tier SKU gate reader so it
// resolves the caller's commerce subscription tier through the SAME co-resident
// commerce client the metering gate bills over — in-process (the commerce transport) when
// commerce is folded in, S2S HTTP with the service token otherwise — NEVER an authed
// self-call to the cloud edge. That self-call is the toothless-gate bug: the edge
// 401/403s a service call to /v1/billing/*, so the ai module's own HTTP lookup always
// returned "" in-cluster and every tier-gated SKU failed OPEN. This mirrors
// installFinance's SetBalanceReader: cloud owns the co-resident read, ai stays
// transport-agnostic. Fail-safe is preserved — Client.Tier folds a commerce error or
// an unknown plan to "", which the gate treats as ALLOW, so a commerce blip never
// locks out a paying caller. No-op when commerce is unreachable (metering !Enabled),
// leaving ai's standalone HTTP fallback in place.
func installTierReader(m *metering.Client, log luxlog.Logger) {
	if m == nil || !m.Enabled() {
		return
	}
	tierReader = func(ctx context.Context, subject, namespace string) (string, error) {
		return m.Tier(ctx, subject, namespace)
	}
	log.Info("ai per-tier SKU gate wired to co-resident commerce (in-process tier read, fail-safe)")
}

// installFinance constructs the ONE in-process finance ledger (per-org SQLite
// double-entry prepaid wallet), publishes it for every money consumer to resolve by
// the narrow finance.Client, and installs the embedded ai router's balance-read +
// usage-debit hooks so the PREPAID gate dispatches DIRECTLY to it — a typed in-proc
// call, no HTTP, no socket. There is NO exempt path (hanzoai/ai >= v1.805.8): every
// principal is gated on a positive prepaid balance, fail-closed. MUST run before
// ai.Use (the ai gate reads the hook per request; the hook must be installed first)
// — which BuildDeps guarantees (deps are built before UseAll).
func installFinance(cfg *Config, deps Deps, log luxlog.Logger) {
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
	// subject; keep them together. The gate reads a coarse cents balance (a >0
	// threshold only); the DEBIT is 18-decimal-exact.
	balanceReader = func(ctx context.Context, subject, namespace, currency string) (int64, error) {
		bal, err := fin.Balance(ctx, namespace, subject, currency, false)
		if err != nil {
			return 0, err
		}
		return bal.Cents(), nil
	}
	// The DEBIT is exact: the ai module emits the cost as a decimal-USD string, parsed
	// here to 18-decimal USD (1e-18) so a sub-cent call bills precisely and is never floored.
	usageRecorder = func(ctx context.Context, u UsageEvent) error {
		amt, err := money.ParseUSD(u.USD)
		if err != nil {
			return err
		}
		// THE ACT IS NAMED HERE, BY THE LEDGER. No Ref goes in, so finance mints the
		// entry's own id and each completion is its own debit. The ai module has no
		// server-chosen name to offer: its message row id is `Owner + "/" + Name`, both
		// read off the client's request body, and while that value was carried across
		// a pinned pair made every completion after the first free.
		return fin.RecordUsage(ctx, types.UsageInput{
			Org: u.Namespace, Subject: u.Subject, Amount: amt,
			Currency: u.Currency, Model: u.Model, Provider: u.Provider,
		})
	}
	log.Info("finance ledger wired (per-subject wallet in the org ledger, 18-decimal-exact, fail-closed)", "dataDir", cfg.DataDir)
}

// pick resolves one inter-subsystem client: enabled in THIS process → zero value
// (nil) so the subsystem's own Mount installs the in-process client; otherwise the
// fail-closed/no-op disabled stub. name is the enable-list id; disabled is the
// client's typed constructor.
//
// THERE IS NO THIRD CASE. A CLOUD_<X>_ZAP_ADDR used to select clients.<X>RPCAt,
// whose every method returned "not yet wired (zapc-gen pending)" — so configuring
// one produced a client that failed every call while the log line said
// "deps.<X> → ZAP RPC". That is worse than no client at all, because it looks
// configured: an operator reading the boot log saw the transport come up and the
// calls fail somewhere else. pickKMSClient made exactly this argument when it
// dropped CLOUD_KMS_ZAP_ADDR for the plane, and the argument is not specific to
// KMS. The peer plane is the transport (plane.Ask, over the peer's socket), and
// it is the only one; a subsystem that is not here and has no plane op is
// honestly disabled rather than falsely addressed.
func pick[T any](cfg *Config, log luxlog.Logger, name, label string, disabled func() T) T {
	if cfg.Enabled(name) {
		var zero T // enabled here → Mount fills deps.<label>
		return zero
	}
	return disabled()
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
func pickKMSClient(cfg *Config, dur *org.Durability, log luxlog.Logger) KMSClient {
	if cfg.Enabled("kms") {
		// The embedded-client constructor is registered by clients/kms in init()
		// (RegisterKMSClientFactory). cloud never imports clients/kms, so the KMS
		// library and its /v1/kms subsystem live in one package with no cloud⇄kms
		// import cycle. Absent the registration (clients/kms not linked into this
		// binary) KMS fails closed rather than pretending to host secrets.
		if kmsClientFactory == nil {
			log.Error("deps.KMS: kms enabled but no client factory registered (clients/kms not linked); failing closed")
			return clients.DisabledKMS()
		}
		c, err := kmsClientFactory(cfg, dur, log)
		if err != nil {
			log.Error("deps.KMS: embedded KMS unavailable, failing closed", "err", err)
			return clients.DisabledKMS()
		}
		return c
	}
	// The store is not in this process, which is the normal case: exactly one holds
	// it. Ask that one over the internal plane.
	//
	// There is no second way to reach it. CLOUD_KMS_ZAP_ADDR used to select
	// clients.KMSRPCAt, a stub whose every method returned "not yet wired
	// (zapc-gen pending)" — so configuring it produced a KMS client that failed
	// every call, which is worse than none because it looks configured. The plane
	// is the transport, and it is the only one.
	log.Info("deps.KMS → the kms app over the internal plane")
	return KMSPeer{}
}

// kmsClientFactory constructs the embedded in-process KMS client from cloud
// Config. clients/kms registers it in init(); pickKMSClient calls it so cloud
// depends on the KMSClient interface + this hook, never the concrete kms package
// — the same inversion the subsystem Registry already uses (cloud mounts every
// subsystem it never imports). Exactly one registration.
var kmsClientFactory func(cfg *Config, dur *org.Durability, log luxlog.Logger) (KMSClient, error)

// RegisterKMSClientFactory installs the embedded-KMS constructor. clients/kms
// calls this from its init(); it is the ONE inversion point that lets the KMS
// library and its /v1/kms subsystem share one package with no cloud⇄kms cycle.
func RegisterKMSClientFactory(f func(cfg *Config, dur *org.Durability, log luxlog.Logger) (KMSClient, error)) {
	kmsClientFactory = f
}

// ---- git-push-to-deploy ----

// GitPushEvent describes a push that just landed on the embedded git server: the
// org, the repo, the FULL ref that moved (refs/heads/<b> or refs/tags/<t>), and
// its new tip commit. CloneURL is the
// canonical clone URL of that repo (https://<host>/v1/git/<org>/<repo>.git) — the
// exact value an Application's RepoURL carries — so the builder can resolve which
// app (if any) tracks this ref and needs a rebuild. Tags reach the builder too:
// releases are cut by tag, so filtering them here would stop publishing silently.
type GitPushEvent struct {
	Org      string
	Project  string
	Repo     string
	Ref      string // FULL ref: refs/heads/<branch> or refs/tags/<tag>
	Commit   string
	CloneURL string
}

// IsBotActor reports whether a push actor is an automation identity rather than a
// person. Every inbound push transport asks this BEFORE firing OnGitPush: our own
// release and mirror automation push AS these identities, so without the guard a
// release's own commit triggers the next release, forever. The two wires we ingest
// spell a bot differently — GitHub suffixes App-authored logins with "[bot]", our
// forge attributes workflow-made pushes to its Actions system user — so the ONE
// predicate knows both.
//
// IT ANSWERS THE UNKNOWN ACTOR "BOT", and that direction is the point. This is a
// LOOP GUARD, so the question it really asks is "could this push be one of ours?"
// — and a delivery that names nobody is precisely when that cannot be ruled out.
// Answering "human" to an absent login let a push with an unparsed actor field
// through the one gate that stops a release rebuilding itself.
//
// The suffix is matched WITHOUT case for the same reason. A login is compared
// case-insensitively everywhere it is authenticated, so "renovate[BOT]" is the
// same account as "renovate[bot]" — and a guard that a different capitalization
// walks past is not one.
func IsBotActor(login string) bool {
	login = strings.TrimSpace(login)
	if login == "" {
		return true
	}
	return strings.HasSuffix(strings.ToLower(login), "[bot]") || strings.EqualFold(login, "hanzo-actions")
}

// pushBuilder is the registered git-push-to-deploy trigger. clients/platform
// installs it in Mount; clients/git calls OnGitPush after a push lands. The
// inversion keeps git⇄platform decoupled — git never imports platform — exactly
// like kmsClientFactory and the subsystem Registry. Exactly one registration.
//
// It answers HOW MANY BUILDS IT LAUNCHED. Most pushes track no application, so
// zero is ordinary rather than a failure — but zero and one are different facts,
// and a client that returns only an error collapses them into the same "accepted".
// The plane half already carried the number (plane.Built.Builds) while the
// in-process half threw it away, so the answer a caller got depended on which
// process the builder happened to be in.
var pushBuilder func(ctx context.Context, ev GitPushEvent) (int, error)

// RegisterPushBuilder installs the git-push-to-deploy trigger. clients/platform
// calls this from its Mount when co-resident; it is the ONE inversion point that
// lets the embedded git server launch a platform build with no git⇄platform cycle.
func RegisterPushBuilder(f func(ctx context.Context, ev GitPushEvent) (int, error)) {
	pushBuilder = f
}

// OnGitPush fires the push-to-deploy trigger for a landed push: the registered
// builder when platform is co-resident, the platform app over the plane when it
// is not.
//
// IT USED TO RETURN NIL WHEN NOTHING WAS REGISTERED, and that nil was the single
// most expensive value in this package. The push lands on GIT's embedded server
// and the builder belongs to PLATFORM — two apps, therefore two processes, so the
// builder was nil on every push that has ever landed in the split fleet. Each one
// returned success to a caller that had committed the client's objects and then
// triggered nothing, and no log line anywhere recorded that a build had not
// happened. "It compiles" and "HTTP 200" both held the whole time.
//
// Best-effort stays the CALLER's contract, not this function's silence: a push
// already committed must not fail because a build could not be triggered. But the
// caller can only honor that contract if it is told. ErrNoPeer says "no
// push-to-deploy in this fleet" — the one error a caller may read as fall back —
// and anything else is an outage worth alarming on. Erasing both into nil is what
// made those two indistinguishable.
//
// It returns the number of builds launched for the same reason: a caller that
// cannot tell "built" from "did nothing" guesses, and the guess is always the
// optimistic one.
func OnGitPush(ctx context.Context, ev GitPushEvent) (int, error) {
	if pushBuilder != nil {
		return pushBuilder(ctx, ev)
	}
	out, err := Ask[plane.PushIn, plane.Built](For(ctx, ev.Org), "platform", plane.PlatformPush, &plane.PushIn{
		Project: ev.Project, Repo: ev.Repo, Ref: ev.Ref,
		Commit: ev.Commit, CloneURL: ev.CloneURL,
	})
	if err != nil {
		return 0, err
	}
	return out.Builds, nil
}

// ---- first-party service release (push→build→image→CR rollout) ----

// ServiceReleaseEvent describes a proven, clean-semver image ready to roll live on
// an operator-managed first-party service. It is the payload of the release client
// that closes push→build→image→CR: after a build produces the image, the CR for
// this service is patched to it and the operator reconciles the Deployment.
//
//   - Service is the target CR metadata.name (the repo/service name ⇒ CR name,
//     mirroring universe's image-update.yml convention).
//   - Image is the full registry ref (repository:tag); the tag MUST be clean
//     semver (vX.Y.Z) — the releaser refuses every mutable/sha/suffixed form.
//   - SHA is the source commit for provenance (optional; logged, never gated on).
type ServiceReleaseEvent struct {
	Service string
	Image   string
	SHA     string
}

// serviceReleaser is the registered first-party CR-rollout client. clients/paas
// (the owner of the hanzo.ai/v1 Service CR control plane) installs it in Mount; a
// proven build calls OnServiceRelease, which patches spec.image on the matching
// CR. The inversion keeps package cloud from importing clients/paas (which imports
// cloud) — the same idiom as pushBuilder / kmsClientFactory. Exactly one
// registration.
var serviceReleaser func(ctx context.Context, ev ServiceReleaseEvent) error

// RegisterServiceReleaser installs the first-party CR-rollout hook. clients/paas
// calls this from its Mount when co-resident; it is the ONE inversion point that
// lets a build-completion path roll a proven image onto its operator Service CR
// with no cloud⇄paas import cycle.
func RegisterServiceReleaser(f func(ctx context.Context, ev ServiceReleaseEvent) error) {
	serviceReleaser = f
}

// OnServiceRelease rolls a proven image live by patching the matching hanzo.ai/v1
// Service CR's spec.image (the operator then reconciles the Deployment). The
// releaser enforces the clean-semver gate and CR-name resolution; this is only the dispatch.
//
// Like OnGitPush it used to return nil when unregistered, with the same result:
// a release that patched no CR, reported as a successful rollout. The proving
// build and the CR control plane are different apps, so that was every release.
//
// There is no ServiceReleaserRegistered() any more. A bool cannot carry "the app
// is elsewhere": it answered false both for a fleet with no paas control plane
// and for the ordinary split fleet where platform is simply the next process
// over, and apps/deploy turned that false into a 503 telling operators the
// release plane did not exist while it was up and reachable. Presence is
// answered by making the call — which is the only moment it is knowable anyway,
// since the router may start a lazy app on demand.
func OnServiceRelease(ctx context.Context, ev ServiceReleaseEvent) error {
	if serviceReleaser != nil {
		return serviceReleaser(ctx, ev)
	}
	_, err := Ask[plane.ReleaseIn, plane.Released](ctx, "platform", plane.PlatformRelease,
		&plane.ReleaseIn{Service: ev.Service, Image: ev.Image, SHA: ev.SHA})
	return err
}

// ---- git lifecycle event stream ----
//
// One event, many subscribers. push-to-deploy (OnGitPush) is the deploy
// subscriber-of-record and stays exactly as it is; this client generalizes the SAME
// inversion to N reactors (mirror-out, Slack-notify, …) so git/platform EMIT a
// lifecycle fact and never import the subscribers. It is deliberately SEPARATE
// from OnGitPush — the deploy path is single-registrant and synchronous, this
// stream is many-registrant and best-effort — so adding a reactor can never
// perturb push→deploy.

// LifecycleKind classifies a git lifecycle event. The value IS the wire name a
// subscription filters on.
type LifecycleKind string

const (
	LifecyclePushLanded   LifecycleKind = "push.landed"
	LifecycleBuildStarted LifecycleKind = "build.started"
	LifecycleDeployLive   LifecycleKind = "deploy.live"
	LifecycleDeployFailed LifecycleKind = "deploy.failed"
)

// LifecycleEvent is one git lifecycle fact fanned out to every registered
// subscriber. A plain data value — values, not places:
//   - Org/Project/Repo    the tenant + repo the fact happened in (the routing key).
//   - Branch/Before/After  the ref that moved and its old→new tip (a push).
//   - Pusher              who pushed (best-effort; "" for a client-less push).
//   - DeployID/Detail     the deployment id + a human one-liner (a deploy transition).
//   - Origin              "" for a native push; the source host when the refs
//     arrived via an inbound mirror sync — the loop-prevention client that lets the
//     outbound mirror subscriber suppress a re-mirror of refs it just pulled in.
type LifecycleEvent struct {
	Kind     LifecycleKind
	Org      string
	Project  string
	Repo     string
	Branch   string
	Before   string
	After    string
	Pusher   string
	DeployID string
	Detail   string
	Origin   string
}

// RepoFromCloneURL extracts the repo name from a git clone URL (last path segment,
// ".git" stripped) — the repo component of the (org,project,repo) routing key that
// a deploy emitter derives from an app/project's linked RepoURL. The ONE place this
// derivation lives, shared by the platform + projects deploy paths (no per-package
// copy).
func RepoFromCloneURL(u string) string {
	u = strings.TrimSuffix(strings.TrimSpace(u), "/")
	u = strings.TrimSuffix(u, ".git")
	if i := strings.LastIndexByte(u, '/'); i >= 0 {
		return u[i+1:]
	}
	return u
}

// lifecycleSubscribers is the fan-out list. Registration happens at Mount
// (single-threaded, before any request is served), so a plain slice is correct:
// EmitLifecycle only ever ranges it after every subsystem's Mount has run.
var lifecycleSubscribers []func(ctx context.Context, ev LifecycleEvent)

// RegisterLifecycleSubscriber adds a git-lifecycle reactor. Every subsystem that
// reacts to a push/deploy (mirror-out, Slack-notify) registers ONE here at Mount;
// git/platform EMIT via EmitLifecycle. The inversion keeps the emitters from
// importing the subscribers — the same pattern as RegisterPushBuilder, but
// many-registrant.
func RegisterLifecycleSubscriber(fn func(ctx context.Context, ev LifecycleEvent)) {
	if fn == nil {
		return
	}
	lifecycleSubscribers = append(lifecycleSubscribers, fn)
}

// ResetLifecycleSubscribers clears the registry. TEST-ONLY client (a test mounts and
// unmounts repeatedly); production registers once at Mount and never resets.
func ResetLifecycleSubscribers() { lifecycleSubscribers = nil }

// EmitLifecycle fans one event out to every registered subscriber, best-effort and
// NON-BLOCKING: each subscriber runs in its own goroutine on a cancel-immune
// context (the fact already happened — a request cancel must not abort the
// notify/mirror), so a slow reactor (a mirror push) can never delay the git/deploy
// path or another reactor. A panicking subscriber is contained so one bad reactor
// can neither crash the shared multi-tenant process nor starve the others; each
// subscriber logs its own errors.
func EmitLifecycle(ctx context.Context, ev LifecycleEvent) {
	subs := lifecycleSubscribers
	if len(subs) == 0 {
		return
	}
	bg := context.WithoutCancel(ctx)
	for _, fn := range subs {
		go func(fn func(context.Context, LifecycleEvent)) {
			defer func() { _ = recover() }()
			fn(bg, ev)
		}(fn)
	}
}

// pickCommerceClient resolves deps.Commerce — the typed inter-subsystem client the
// entitlements/licensing tier calls (GetOrgConfig, CheckEntitlement). When the
// commerce subsystem is co-resident (Enabled("commerce")) it returns the IN-PROCESS
// client via the factory apps/commerce/mount.go registers in init() — a direct Go
// call that reads the embedded commerce datastore (hanzoai/commerce MODULE, since
// the un-fork) + the @hanzo/plans vocabulary, no network hop (the HIP-0106
// co-resident default). The factory inversion stays because the concrete client
// (apps/commerce) imports apps/plan, which imports cloud — a direct
// call here would be a package cycle. Absent the registration it fails closed
// rather than pretending.
//
// NETWORK PATH PRESERVED: absent co-residency the ZAP-RPC + disabled fallbacks apply
// (out-of-process commerce, or not wired), so the remote proxy client
// (CLOUD_COMMERCE_ZAP_ADDR) is unchanged — the live default still selects the
// network client when commerce is not enabled in this process.
func pickCommerceClient(cfg *Config, log luxlog.Logger) CommerceClient {
	if cfg.Enabled("commerce") {
		if commerceClientFactory == nil {
			log.Error("deps.Commerce: commerce enabled but no client factory registered (subsystems not linked); failing closed")
			return clients.DisabledCommerce()
		}
		log.Info("deps.Commerce → in-process (embedded commerce)", "brand", cfg.Brand)
		return commerceClientFactory(cfg, log)
	}
	// Commerce is not in this process. The MONEY ops it owns are reachable over the
	// peer plane (plane/commerce: authorize, balance, credit, record, scope rules,
	// txns, usage) and the metering client already asks for them there. GetOrgConfig
	// and CheckEntitlement declare no plane
	// op, so there is nothing to ask and nothing to pretend: the honest client is
	// the disabled one, which says "enable the subsystem" rather than failing every
	// call from behind a configured-looking address.
	return clients.DisabledCommerce()
}

// commerceClientFactory constructs the embedded in-process Commerce client.
// apps/commerce/mount.go registers it in init(); pickCommerceClient calls it so
// package cloud depends on the CommerceClient interface + this hook, never the
// concrete apps/commerce package (whose entitlement client pulls apps/plan,
// which imports cloud — the hook is what keeps the package graph acyclic).
var commerceClientFactory func(cfg *Config, log luxlog.Logger) CommerceClient

// RegisterCommerceClientFactory installs the embedded-Commerce client constructor.
// apps/commerce/mount.go calls this from its init(); exactly one registration.
func RegisterCommerceClientFactory(f func(cfg *Config, log luxlog.Logger) CommerceClient) {
	commerceClientFactory = f
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

// pickCompletionsClient resolves deps.AI — the client the agents run path (and
// guide/crm/content/sitegen/code /ask) execute CHAT COMPLETIONS through. There is
// NO in-process "ai" mount that fills a nil deps.AI: inference is an external
// gateway, so this returns a concrete client, never nil.
//
// Completions are a WRITE endpoint: a read-only publishable (pk-) key 403s them. So
// this resolver NEVER rides a pk- key — that is the embed credential (pickEmbedClient).
// Preference order:
//  1. Static SECRET-key HTTP gateway when a base URL AND a completions-capable
//     (non-pk-) static key are configured — an explicit operator override (sk-).
//     A pk- key here is REFUSED (it would only 403 chat) and the resolver falls
//     through to M2M — THE fix for the intermittent publishable-key 403 on bot replies.
//  2. M2M HTTP gateway when a base URL AND the binary's IAM identity are present
//     (the durable Hanzo default): the client mints+refreshes a client-credentials
//     token from IAM_CLIENT_ID/SECRET — no static key to rotate. Secret never logged.
//  3. ZAP RPC when an addr is configured (split-deploy of a future ai subsystem).
//  4. Fail-closed stub otherwise — a run records an honest error, never fakes one.
func pickCompletionsClient(cfg *Config, log luxlog.Logger) AIClient {
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
	//
	// The CREDENTIAL is unchanged — same static key or same M2M identity, chosen
	// the same way — because who may ask is a different question from where the
	// peer is. Only the address and the route it travels change.
	via, base := aiRoute(cfg)
	if base != "" && cfg.AIAPIKey != "" && !publishableKey(cfg.AIAPIKey) {
		log.Info("deps.AI (completions) → static secret key", "at", base, "socket", via != nil, "default_model", DefaultModel)
		return clients.AIHTTPOn(base, cfg.AIAPIKey, DefaultModel, via)
	}
	if cfg.AIAPIKey != "" && publishableKey(cfg.AIAPIKey) {
		log.Info("deps.AI (completions) → refusing read-only publishable (pk-) key for chat; using M2M", "at", base)
	}
	if base != "" && cfg.AIAuthClientID != "" && cfg.AIAuthClientSecret != "" {
		if tokenURL := aiM2MTokenURL(cfg); tokenURL != "" {
			log.Info("deps.AI (completions) → IAM M2M", "at", base, "socket", via != nil,
				"token_url", tokenURL, "client_id", cfg.AIAuthClientID, "default_model", DefaultModel)
			return clients.AIHTTPM2MOn(base, tokenURL, cfg.AIAuthClientID, cfg.AIAuthClientSecret, DefaultModel, via)
		}
	}
	log.Info("deps.AI (completions) → disabled (no secret key, no IAM M2M identity, no gateway configured)")
	return clients.DisabledAI()
}

// pickEmbedClient resolves deps.Embed — the client code-index + KB knowledge run
// EMBEDDINGS through. Embeddings are a READ-ONLY endpoint, so the read-only
// publishable (pk-) key (CLOUD_AI_API_KEY ← cloud-ai-embed-key) is the CORRECT
// least-privilege credential here, and is used UNCHANGED — this path is deliberately
// not rewired. Preference order:
//  1. Static-key HTTP gateway when a base URL AND a static key are configured. The
//     key (pk- or sk-) is a KMS-injected secret; only base URL + model are logged.
//  2. Otherwise share the completions resolution (M2M / ZAP / fail-closed) so a
//     deploy with no dedicated embed key still indexes — no regression.
func pickEmbedClient(cfg *Config, log luxlog.Logger) AIClient {
	via, base := aiRoute(cfg)
	if base != "" && cfg.AIAPIKey != "" {
		log.Info("deps.Embed → static embed key", "at", base, "socket", via != nil, "default_model", DefaultModel)
		return clients.AIHTTPOn(base, cfg.AIAPIKey, DefaultModel, via)
	}
	return pickCompletionsClient(cfg, log)
}

// aiM2MTokenURL resolves IAM's client_credentials endpoint the agent runner mints
// its M2M inference token at. It MUST be reachable FROM INSIDE THE CLUSTER: the
// runner runs in-cluster and the public issuer host (https://hanzo.id) is fronted
// by Cloudflare, which 403s a server-side (non-browser) loopback POST with edge
// error 1006 — so minting against the PUBLIC issuer URL fails and every
// POST /v1/agents/:ref/run 502s. Measured: an in-cluster POST to
// https://hanzo.id/v1/iam/oauth/token answers 403/1006, http://iam.hanzo.svc/... 200.
// This mirrors the KMS login-broker resolution (apps/kms/mount.go) exactly — one
// split-horizon policy, no drift. Prefer, in order: an explicit override
// (CLOUD_AI_IAM_TOKEN_URL), the in-cluster IAM service base (IAM_URL — already
// wired to http://iam.hanzo.svc for JWKS), then the public issuer as a last resort
// (single-process / no split-horizon deploys). Returns "" only when no identity is
// resolvable, which keeps the M2M branch off (caller falls through to the stub).
func aiM2MTokenURL(cfg *Config) string {
	if override := strings.TrimSpace(os.Getenv("CLOUD_AI_IAM_TOKEN_URL")); override != "" {
		return override
	}
	if base := IAMBaseURL(cfg.IAMIssuer); base != "" {
		return base + "/v1/iam/oauth/token"
	}
	return ""
}

func pickVFSClient(cfg *Config, log luxlog.Logger) VFSClient {
	// deps.VFS must NEVER be nil (R-7): files.go and any other VFS consumer call
	// s.vfs.Put/Get/Delete unconditionally, so a nil here is a per-request 500
	// (dishonest degradation) instead of a fail-closed 502. Unlike the
	// nil-then-Mount-fills convention other subsystems use, nothing fills deps.VFS
	// after UseAll (Mount receives deps by value), so we ALWAYS hand back a
	// concrete client.
	// Real blob backend (.97): the shared SeaweedFS S3 gateway — the canonical,
	// key-based object store, reached with the SAME S3_ADMIN_* admin identity
	// clients/s3 uses (s3admin, one construction). Present only when those creds
	// are injected; a construction failure degrades to fail-closed rather than a
	// nil deref. Team blobs (avatars/attachments) round-trip through this to the
	// team-blobs bucket, org-scoped by the caller-built key prefix.
	if admin := s3admin.New(); admin.Configured() {
		v, err := clients.NewS3VFS(admin)
		if err != nil {
			log.Error("deps.VFS → S3 construction failed; falling back to fail-closed", "err", err)
			return clients.DisabledVFS()
		}
		log.Info("deps.VFS → SeaweedFS S3", "bucket", clients.TeamBlobBucket)
		return v
	}
	// No VFS endpoint and no S3 admin creds → fail-closed stub (R-7): Put/Get/Delete
	// return a non-nil error → files answer 502, never a nil-deref 500.
	return clients.DisabledVFS()
}

// durableBucket holds every org's HA-SQLite snapshot (and its writer lease). One
// bucket, keys laid out orgs/<slug>[/…]/<subsystem>.db per HIP-0302 — the durable
// twin of the on-disk DataDir layout.
//
// OPERATIONAL REQUIREMENTS the fence depends on (enforce in the SeaweedFS deployment,
// not in code):
//
//   - Object versioning + a no-expiry / no-lifecycle-deletion policy on this prefix.
//     The writer lease (orgs/<slug>/.owner) is the round's system of record; if the
//     gateway silently drops or rolls back that object, a monotone round can reset and
//     un-fence a zombie writer (Red M4). A local high-water-round floor per pod is the
//     future in-process defense; the object lifecycle is the operational one.
//   - RWO, per-writer PVCs for DataDir — NEVER an RWX shared volume (Red M5). Two pods
//     on one DataDir corrupt SQLite regardless of this fence; single-writer here is the
//     durable-copy fence, and per-pod RWO is the local-file guarantee the shard router
//     already relies on (see shardrouter.go).
const durableBucket = "org-db"

// durableProbePrefix namespaces the boot CAS-atomicity probe's throwaway objects
// (org.ProbeCAS writes one per boot) away from the orgs/ tree. A bucket lifecycle rule
// may reap ".probe/*"; the objects are tiny and never read after the probe.
const durableProbePrefix = ".probe/cas-"

// peered reports whether this deployment runs MORE THAN ONE writer.
//
// It matters only where the durable plane is off: with no fence, ownership of an
// org is decided by how many writers the deployment has and nothing else can
// decide it (see OrgStore.Owned).
//
// TWO SIGNALS, EITHER OF WHICH MEANS PEERS, and reading only the first is a hole
// in exactly the deployment that matters. A static CLOUD_PEERS names the writers
// outright. A live in-cluster deployment does not have to: CLOUD_PEER_SELECTOR is
// how it says "my writers are whichever pods match this", and membership is then
// read from the K8s API — so a two-replica production deployment routinely names a
// selector and NO peer list. Read from the list alone, both of its pods would
// answer "I am the only writer" the moment the object store went away, and each
// would bill every org: the double charge the gate exists to stop, arriving under
// the one configuration where it is most expensive.
//
// A selector is a DECLARATION of peers whether or not any are up yet, so it is read
// as one. The two errors are not the same size: over-detecting defers a debit until
// the plane is provable again, under-detecting bills it twice.
//
// It is a FUNCTION and not an expression at the call site because it is a rule
// about money and a rule nothing can call is a rule nothing can test — the first
// version of this was inline, and the test written for it recomputed the same
// expression and would have stayed green through any change to the real one.
func peered(cfg *Config) bool {
	return len(parsePeers(cfg.ShardPeers)) > 1 || strings.TrimSpace(cfg.PeerSelector) != ""
}

// buildDurability constructs the deployment's HA-durability factory, or nil when the
// deployment has no object store to be durable against (dev/single-node — every
// OrgStore then stays local-only). It composes the SeaweedFS S3 If-Match
// ConditionalStore (the SAME s3admin identity deps.VFS uses), the writer membership
// over CLOUD_PEERS (the SAME set the shard router elects on, so the store-layer owner
// and the routed owner agree), and the per-org envelope Cipher rooted at the KMS
// master. Any construction failure fails SAFE to nil (local-only) rather than crash
// the boot; an encryption-capable build with no usable cipher is REFUSED — a build
// that promises encryption never ships plaintext snapshots to the object store.
// It returns the durable factory AND the live-members reader for the shard router (the
// SAME election snapshot), non-nil together only when the plane is active; both nil when
// local-only (the router then stays on the static ordinal set).
func buildDurability(cfg *Config, log luxlog.Logger) (*org.Durability, func() []ha.Member) {
	// A multi-replica deployment REQUIRES the durable plane: with >1 writer, a per-org
	// store that is not hydrate-on-open + fenced is the outage this exists to fix.
	// disabledDurability logs at the severity the replica count warrants, so a
	// misconfigured prod deployment is never SILENTLY non-durable (Red L2).
	multiReplica := len(parsePeers(cfg.ShardPeers)) > 1

	// Durability is THE path — there is no operator toggle. It self-detects capability
	// at boot: no object store reachable (dev / native-Go / no S3 creds) → local-only,
	// same code path, graceful; a reachable store → PROVE its conditional-PUT atomicity
	// (org.ProbeCAS) before fencing a single byte of tenant data. A store that cannot be
	// proven atomic fails SAFE to local-only with a loud alert — never a silent-wrong
	// fence on a store that could split-brain.
	admin := s3admin.New()
	if !admin.Configured() {
		disabledDurability(log, multiReplica, "no S3 admin creds (S3_ADMIN_* unset)")
		return nil, nil
	}
	client, err := admin.Client()
	if err != nil {
		disabledDurability(log, multiReplica, fmt.Sprintf("S3 client construction failed: %v", err))
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := ensureDurableBucket(ctx, admin, client); err != nil {
		// Non-fatal: the bucket likely already exists; a later ship/hydrate retries.
		log.Warn("durability bucket ensure failed (continuing)", "bucket", durableBucket, "err", err)
	}
	cancel()

	// Prove the store enforces conditional-PUT atomically BEFORE fencing any tenant data
	// (the auto-H2 self-check that replaces the old opt-in flag). A store that cannot be
	// proven atomic fails SAFE to local-only + a loud alert — never a silent fence on a
	// store that could admit two writers for one round (split-brain). The result is
	// cached for the process life (deps.Durable is set once), so this probe runs once.
	// cond is the linearizable register the fence stands on, constructed HERE (not
	// hard-wired inside the fence) so a cache tier slots in as a decorator: a
	// read-through/write-through KV-in-front-of-S3 store (github.com/hanzoai/kv-go) can
	// wrap this one line to serve low-latency hydrate reads while the authoritative CAS
	// still lands on S3 — the fence reads and CASes through whatever ConditionalStore it
	// is handed. (Safety note for that tier: a stale cached lease read only costs a claim
	// retry, never safety — the S3 CAS is authoritative — so a write-through cache is
	// sound over the SAME cond that backs both the lease and the data ships.)
	cond := org.NewS3ConditionalStore(client, durableBucket)
	probeCtx, probeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err = org.ProbeCAS(probeCtx, cond, durableProbePrefix)
	probeCancel()
	if err != nil {
		disabledDurability(log, multiReplica, fmt.Sprintf("object-store conditional-PUT atomicity NOT confirmed — %v", err))
		return nil, nil
	}

	// Membership: LIVE when in-cluster + CLOUD_PEER_SELECTOR is set (a rolling upgrade's
	// changing pod set is tracked, a draining/dead pod is never elected an org's owner),
	// else the STATIC CLOUD_PEERS/self set — capability-detected, no flag (see
	// membership_writers.go). A single-pod deployment with no peers is its own sole writer.
	// The 2s refresh keeps a drained pod out of every peer's election within a bound the
	// terminationGracePeriod covers, so a rolling handoff loses no request.
	self := selfID(cfg)
	peers := parsePeers(cfg.ShardPeers)
	if len(peers) == 0 {
		peers = []org.Member{{ID: self, Addr: self}}
	}
	src := membershipSource(peers, cfg.PeerSelector, httpPortOf(cfg.ListenAddr), log)
	members := org.NewMembership(self, src, 2*time.Second)
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
	return org.NewDurability(cond, members, cipher,
		org.WithSeal(sqlitedrv.Checkpoint),
		org.WithReader(orgReader),
	), members.Members
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

// ensureDurableBucket creates the durable bucket if absent (idempotent).
func ensureDurableBucket(ctx context.Context, admin s3admin.Admin, client *s3.Client) error {
	ok, err := client.BucketExists(ctx, durableBucket)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	return client.MakeBucket(ctx, durableBucket, s3.MakeBucketOptions{Region: admin.Region()})
}

// durableCipher builds the per-org envelope Cipher from the base64 KMS master
// (CLOUD_KMS_MASTER_KEY_REF — the SAME key cek derives from). nil when no valid
// 32-byte master is configured (pure-Go dev → plaintext durable object, matching the
// plaintext local file).
func durableCipher(cfg *Config, log luxlog.Logger) *org.Cipher {
	ref := strings.TrimSpace(cfg.KMSMasterKeyRef)
	if ref == "" {
		return nil
	}
	master, err := base64.StdEncoding.DecodeString(ref)
	if err != nil {
		log.Warn("durable cipher: KMS master key ref is not valid base64", "err", err)
		return nil
	}
	c, err := org.NewCipher(master)
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
func selfID(cfg *Config) string {
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

// UseFunc is a subsystem's mount contract: register your routes on app, using
// deps for everything shared. Every subsystem in the fleet exports exactly this
// signature, so Wire references each one directly and the compiler checks it.
//
// app is a Router, not the concrete *zip.App, and that is the whole safety
// property: middleware a subsystem installs lands on the subtrees its App
// declares, never over the binary. Routes register exactly as before — absolute
// paths, same precedence. See scope.go. A subsystem that genuinely gates
// everything sets [Plugin.Global] and receives the bare app THROUGH THIS SAME
// TYPE; the grant changes which Router arrives, never the signature.
//
// THIS IS THE CONTRACT'S ONLY ENFORCEMENT, AND IT IS THE COMPILER. There is no
// second entry-point field to launder a divergent shape through, so an app whose
// Mount takes *zip.App, or a differently-aliased Router, cannot be assigned here
// and cannot reach a composition root. A doc comment stating the signature is
// what let five apps diverge from it unnoticed; a type is what checks it.
//
// A subsystem that needs the concrete *zip.App (a typed registrar, an embedded
// module's own mount) recovers it with [ZipApp] — the named hole, which reports
// nil rather than pretending, so a mount that truly needs the registry fails
// instead of serving routes no projection knows.
type UseFunc func(app Router, deps Deps) error

// ShutdownFunc releases a subsystem's process-lifetime resources (background
// goroutines, open DB handles) on graceful shutdown. It must be idempotent and
// bounded — Serve calls it within the shutdown deadline. ctx carries that
// deadline so a slow teardown is cut off rather than hanging SIGTERM.
type ShutdownFunc func(ctx context.Context) error

// CtxShutdown adapts a subsystem's zero-arg Shutdown() error to ShutdownFunc.
// Several subsystems expose the simpler form (their teardown ignores the
// deadline); this bridges the impedance mismatch in ONE place so the Wire
// entries stay declarative — no inline closures.
//
// It lives beside ShutdownFunc rather than in package apps because a Wire entry
// is copied verbatim into plugin/<app>/main.go by plugin/gen-app-cmds: an apps-local
// helper is unreachable from a standalone main, so that app falls back to the
// fat stub that links EVERY subsystem. Package-qualified here, it is nameable
// from anywhere and the per-app binary stays lean.
func CtxShutdown(f func() error) ShutdownFunc {
	return func(context.Context) error { return f() }
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
// It is a UseFunc — the doc above always CLAIMED it was one and the signature
// said otherwise, which is the whole reason a per-entry-point escape hatch is a
// bad idea. metrics installs no middleware anywhere (the package calls Use
// nowhere), so it mounts SCOPED like everyone else; it only ever needed the
// concrete app to register routes, and cloud.ZipApp is the named hole for that.
func UseMetrics(app Router, deps Deps) error {
	a := ZipApp(app)
	if a == nil {
		return fmt.Errorf("metrics: router is not a zip app")
	}
	if err := metrics.Use(a, metrics.Deps{
		Logger: luxlog.Default(), DataDir: deps.DataDir, Brand: deps.Brand,
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
// hanzoai/licensing is no longer such a case: since v0.1.10 it types its own ops,
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

	openapi.Describe("/v1/metrics/health", http.MethodGet,
		"How many metric series this deployment holds for your org",
		"Reports the native metrics store's live state for the calling tenant: the subsystem "+
			"version, the resolved `org`, and `series` — the number of distinct series actually "+
			"held right now, read out of the store rather than a constant. It is not a dependency "+
			"probe and has nothing downstream to fail on: the store is in-process, so this answers "+
			"200 whenever the process is up.\n\n"+
			"The tenant is the gateway-minted `X-Org-Id` header, falling back to the deployment "+
			"brand and then `default`. This surface trusts the edge rather than re-deriving the org "+
			"from a validated claim of its own, so it belongs behind the gateway and nowhere else.")

	openapi.Describe("/v1/metrics/batch", http.MethodPost,
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

	openapi.Describe("/v1/metrics/write", http.MethodPost,
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

	openapi.Describe("/v1/metrics/query", http.MethodGet,
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

	openapi.Describe("/v1/metrics/logs/health", http.MethodGet,
		"How many log records this deployment holds for your org",
		"Reports the native log store's live state for the calling tenant: the subsystem version "+
			"and `records`, the count actually held right now rather than a constant. Not a "+
			"dependency probe — the store is in-process, so this answers 200 whenever the process "+
			"is up.\n\n"+
			"The tenant is the gateway-minted `X-Org-Id` header, falling back to the deployment "+
			"brand and then `default`.")

	openapi.Describe("/v1/metrics/logs/write", http.MethodPost,
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

	openapi.Describe("/v1/metrics/logs/query", http.MethodGet,
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

	openapi.Describe("/v1/metrics/traces/health", http.MethodGet,
		"How many spans this deployment holds for your org",
		"Reports the native trace store's live state for the calling tenant: the subsystem version "+
			"and `spans`, the count actually held right now. Not a dependency probe — the store is "+
			"in-process, so this answers 200 whenever the process is up.\n\n"+
			"The tenant is the gateway-minted `X-Org-Id` header, falling back to the deployment "+
			"brand and then `default`.")

	openapi.Describe("/v1/metrics/traces/write", http.MethodPost,
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

	openapi.Describe("/v1/metrics/traces/trace", http.MethodGet,
		"Every span of one trace — the waterfall",
		"Answers `{spans}`: every span the org holds for the trace id in `id`, in the order they "+
			"were appended, which is what a waterfall view renders. Unlike the other reads there is "+
			"no count, no time range and no limit — a trace is addressed by id or not at all.\n\n"+
			"An id with no spans answers an EMPTY list, never a 404: the store cannot tell a trace "+
			"that never existed from one whose spans retention has already dropped, so it does not "+
			"pretend to. The tenant is the gateway-minted `X-Org-Id` header, falling back to the "+
			"deployment brand and then `default`, and a trace id belonging to another org is simply "+
			"not in this org's store.")

	openapi.Describe("/v1/metrics/traces/query", http.MethodGet,
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

// App describes one subsystem to mount. There is NO Order field: the slice
// position in apps.Wire() IS the mount order — the composition root lists
// subsystems in the exact sequence they mount (and, reversed, tear down), so order
// is data read top-to-bottom in one file, not ints scattered across the tree.
type Plugin struct {
	Name     string
	Use      UseFunc
	Shutdown ShutdownFunc // optional; nil means the subsystem has nothing to tear down.

	// Price is what ONE request to this subsystem's surface costs at the edge gate —
	// Free, Metered, or a positive number of cents (see price.go). It is REQUIRED:
	// the zero value is Undeclared, and TestPriceDeclared fails on it, so a new
	// subsystem cannot reach main until someone writes down what it costs. This is
	// the ONE place a surface's price is declared; DefaultPrice reads it and holds no
	// table of its own.
	Price Price

	// OwnsHealth marks a subsystem that serves its OWN GET /v1/<name>/health
	// (a real, fail-closed probe). Serve's generic liveness loop skips these so
	// its always-ok route never shadows the subsystem's real probe.
	OwnsHealth bool

	// Prefixes are the route subtrees whose middleware this subsystem may install.
	// Empty means the convention it already follows — /v1/<Name>, the same subtree
	// Serve's generic liveness route assumes — so only a subsystem that gates
	// something else has to name it. It bounds MIDDLEWARE, not route registration:
	// routes still register at absolute paths anywhere, as they always have.
	Prefixes []string

	// Global grants app-wide middleware: Mount receives the BARE app as its
	// Router instead of a scope, so what it installs runs for the whole binary.
	// It is the answer for a subsystem that genuinely gates everything (commerce
	// wraps all of /v1) and a lie for anyone else.
	//
	// IT IS A BOOL, AND THAT IS THE POINT. It used to be
	// `App func(*zip.App, Deps) error` — a second Mount FIELD with a second Mount
	// SIGNATURE — which braided two unrelated questions into one declaration:
	// "may this subsystem gate the binary" (policy) and "what shape is its entry
	// point" (type). Because the grant carried its own shape, a subsystem that
	// merely wanted the concrete *zip.App could get it by taking the grant, and
	// three did: agent, ai and commerce each held app-wide middleware authority
	// they had not asked for, purely because their Mount named a concrete type.
	// Nothing reported it — the doc comment claimed apps.TestWireFrozen policed
	// new grants, and no such test exists anywhere in this repo.
	//
	// Unbraided, the shape question has ONE answer for all 140 subsystems —
	// [UseFunc] — so the compiler checks it at every composition root and a
	// sixth divergent signature cannot link. The concrete app is still reachable
	// through [ZipApp], which is the named hole for it and always was; a global
	// subsystem simply gets one whose Router IS the app.
	Global bool

	// Door is this subsystem's PER-CALLER contribution to the MCP server: the tools
	// that exist because of who is asking, which the build-time projection cannot
	// hold. Nil — every subsystem but one — leaves the MCP server exactly the typed
	// ops.
	//
	// It is stated HERE, at the composition root, for the reason Price and
	// Prefixes are: what a binary serves is a property of the binary, declared
	// where the binary is assembled, not installed from inside a Mount that runs
	// after the MCP server is configured.
	Door zip.Source
}

// door is the ONE per-caller tool source of a binary. Two subsystems each
// claiming one would be two answers to "what else can this caller call", so the
// second is a composition error and not a merge.
func door(plugins []Plugin) (zip.Source, error) {
	var src zip.Source
	var held string
	for _, p := range plugins {
		if p.Door == nil {
			continue
		}
		if src != nil {
			return nil, fmt.Errorf("%s and %s both declare a Door — a binary has one per-caller tool source", held, p.Name)
		}
		src, held = p.Door, p.Name
	}
	return src, nil
}

// UseAll mounts every ENABLED subsystem in specs, in slice order — the order is
// the composition root's (apps.Wire()); UseAll does NOT sort.
//
// app is the concrete *zip.App from Serve. A Global spec receives it as its
// Router. Everyone else receives a scope bound to their declared Prefixes, so a
// subsystem's middleware reaches its own subtrees and nothing else, whatever its
// slice position. A subsystem that installs middleware outside them fails the
// mount — the binary refuses to boot half-gated rather than serving with a
// stranger's gate on.
//
// Every spec goes through spec.Use, whatever the grant: the Router it receives
// is the only difference, which is what makes [UseFunc] the fleet's ONE mount
// signature and lets the compiler check it at all 123 composition roots.
//
// Teardown is wired HERE, at mount time: right after a subsystem mounts, its
// ShutdownFunc (if any) is registered via app.OnShutdown. zip drains those hooks
// LIFO — AFTER the listeners stop accepting and in-flight requests drain — so
// registration-at-mount yields reverse-mount teardown (a dependency mounted before
// its dependents is torn down after them) with no subsystem torn down while a
// request still uses it. Only ENABLED specs mount, so only they register a hook;
// teardown needs no separate enablement gate.
func UseAll(app *zip.App, specs []Plugin, cfg *Config, deps Deps) error {
	logger := luxlog.Default()
	// Declare the composition root BEFORE anything mounts: TracingMiddleware resolves
	// hanzo.subsystem off this, DefaultPrice resolves each surface's declared price off
	// it, and the inventory (including what is switched OFF) is what
	// /v1/admin/subsystems reports. Built once, read lock-free per request.
	Declare(specs, cfg)
	for _, spec := range specs {
		if !cfg.Enabled(spec.Name) {
			logger.Debug("subsystem disabled", "name", spec.Name)
			continue
		}
		if spec.Use == nil {
			return fmt.Errorf("mount %s: no Mount — a subsystem that registers nothing is not composed, it is absent", spec.Name)
		}
		// The grant decides WHICH Router, never which signature. Global hands over
		// the bare app (a *zip.App is a Router); everyone else gets a scope bound
		// to their declared prefixes. One call, one shape, either way.
		var sc *scope
		r := Router(app)
		if !spec.Global {
			sc = newScope(app, spec.Name, spec.Prefixes)
			r = sc
		}
		if err := spec.Use(r, deps); err != nil {
			return fmt.Errorf("mount %s: %w", spec.Name, err)
		}
		if sc != nil {
			if err := sc.err(); err != nil {
				return fmt.Errorf("mount %s: %w", spec.Name, err)
			}
		}
		// Register teardown as a zip shutdown hook. zip runs hooks LIFO after the
		// drain (zip.App.Shutdown), so this reproduces the reverse-mount order the
		// hand-rolled reverse-loop gave — without the teardown-before-drain race.
		if spec.Shutdown != nil {
			app.OnShutdown(spec.Shutdown)
		}
		logger.Info("composed subsystem", "name", spec.Name)
	}
	return nil
}

// logMaster states how this process resolved its data-plane key. Nothing
// branches on it — but it is the line whose absence once let a boot announce a
// dev key it had not installed, so it reports the resolved source, or the reason
// there is none.
func logMaster(log luxlog.Logger) {
	switch {
	case MasterErr() != nil:
		log.Warn("data-plane key: NONE — store opens fail closed", "err", MasterErr())
	case MasterFrom() == MasterEnv:
		log.Info("data-plane key: from the environment", "from", MasterFrom())
	default:
		log.Warn("data-plane key: DEV (nothing configured, and the data directory was empty — dev/CI only)")
	}
}

// masterKeyBytes decodes the base64 KMS master (CLOUD_KMS_MASTER_KEY_REF) into
// the 32 raw bytes a subsystem needs to encrypt its own store. nil on absent or
// malformed — never a partial or wrong-length key, because a wrong key encrypts
// against a store no other key can open.
func masterKeyBytes(cfg *Config) []byte {
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
