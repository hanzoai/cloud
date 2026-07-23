package cloud

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"
	"time"

	aiobject "github.com/hanzoai/ai/object"
	"github.com/hanzoai/cloud/cek"
	"github.com/hanzoai/cloud/clients/commerceinproc"
	"github.com/hanzoai/cloud/clients/metering"
	"github.com/hanzoai/cloud/internal/org"
	s3 "github.com/hanzoai/s3-go"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/clients"
	"github.com/hanzoai/cloud/clients/finance"
	"github.com/hanzoai/cloud/clients/gateway/edge"
	"github.com/hanzoai/cloud/clients/money"
	"github.com/hanzoai/cloud/clients/s3admin"
	"github.com/hanzoai/cloud/types"
)

// BuildDeps constructs the Deps used by every subsystem's Mount(app, deps).
//
// Wiring rules per HIP-0106 inter-subsystem contract:
//
//  1. If the subsystem is enabled in this process, the Client field is
//     left nil here. The subsystem's own Mount() will install a typed
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
// JSON does not appear in any of these paths. Inter-subsystem calls
// are ZAP-typed Go values either via direct method dispatch (mode 1)
// or via ZAP RPC over the wire (mode 2). JSON happens only at the
// gateway/ingress edge, through the zip jsonenc helper.
//
// Payments and Vault are special: they are NEVER in-process per
// HIP-0106 solo-vault CDE. Their clients always resolve via
// clients.PaymentsRPCAt / clients.VaultRPCAt; the disabled stub fires
// when no endpoint is configured.
func BuildDeps(cfg *Config) Deps {
	logger := luxlog.New("cloud")
	logger.Info(
		"building deps",
		"brand", cfg.Brand,
		"domain", cfg.Domain,
		"iam_issuer", cfg.IAMIssuer,
		"data_dir", cfg.DataDir,
		"enabled", cfg.Enable,
	)

	deps := Deps{
		Logger:          logger,
		Brand:           cfg.Brand,
		Version:         cfg.Version,
		Env:             cfg.Env,
		Domain:          cfg.Domain,
		IAMIssuer:       cfg.IAMIssuer,
		DataDir:         cfg.DataDir,
		AIDefaultModel:  cfg.AIDefaultModel,
		AIFallbackModel: cfg.AIFallbackModel,
	}

	// For each subsystem: enabled → leave nil (Mount fills it); not
	// enabled + endpoint → RPC client; not enabled + no endpoint →
	// disabled stub. The plain co-resident-or-RPC-or-disabled clients share
	// ONE resolver (pick); KMS/AI/VFS keep bespoke pickers because their
	// construction genuinely differs (embedded store / gateway preference /
	// S3-admin backend). O11y's disabled stub is a no-op (telemetry going
	// nowhere is normal), not fail-closed.
	deps.IAM = pick(cfg, logger, "iam", "IAM", cfg.IAMZAPAddr, clients.IAMRPCAt, clients.DisabledIAM)
	deps.KMS = pickKMSClient(cfg, logger)
	deps.Base = pick(cfg, logger, "base", "Base", cfg.BaseZAPAddr, clients.BaseRPCAt, clients.DisabledBase)
	deps.Commerce = pickCommerceClient(cfg, logger)
	// Metering client BEFORE the AI client: deps.AI is wrapped in the metering
	// decorator (the ONE inference gate+meter — no exempt path, no bypass, no
	// side-channel key), which needs deps.Metering. nil-safe — an unconfigured
	// commerce URL yields a !Enabled() client, so the wrap is a transparent
	// pass-through and a dev deployment is never blocked.
	deps.Metering = buildMeteringClient(cfg, logger)
	wireTierReader(deps.Metering, logger)
	// AI (completions, WRITE) and Embed (embeddings, READ-ONLY) are DISTINCT
	// credentials by concern: completions never ride the read-only publishable
	// (pk-) key — the gateway 403s a pk- key on any write endpoint — so deps.AI
	// resolves to the M2M identity, while deps.Embed keeps the pk- key (correct
	// least-privilege for a read-only call). Both meter through the ONE commerce path.
	deps.AI = meteredAIClient(pickCompletionsClient(cfg, logger), deps)
	deps.Embed = meteredAIClient(pickEmbedClient(cfg, logger), deps)
	wireFinance(cfg, logger)
	deps.O11y = pick(cfg, logger, "o11y", "O11y", cfg.O11yZAPAddr, clients.O11yRPCAt, clients.DisabledO11y)
	deps.VFS = pickVFSClient(cfg, logger)
	deps.MQ = pick(cfg, logger, "mq", "MQ", cfg.MQZAPAddr, clients.MQRPCAt, clients.DisabledMQ)
	deps.Durable = buildDurability(cfg, logger)

	// Payments and Vault never co-resident. Disabled stub when no
	// endpoint, otherwise RPC.
	deps.Payments = pickPaymentsClient(cfg, logger)
	deps.Vault = pickVaultClient(cfg, logger)

	// Runtime-mutable edge-policy store (/v1/gateway config plane), layered over
	// the static env/flag defaults so an un-provisioned deployment behaves exactly
	// as the static config until an operator PUTs an override. New always returns a
	// working *Store (static-only if the SQLite file can't open), so the edge
	// middleware is never left without a policy source — a store-open error is
	// logged, not fatal.
	gp, err := edge.New(cfg.DataDir, cfg.AdminOrg, staticEdgePolicy(cfg))
	if err != nil {
		logger.Warn("gateway policy store degraded to static-only", "err", err)
	}
	deps.GatewayPolicy = gp

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
// the gate DEBITS the in-process commerce handler over commerceinproc's self-routing
// transport — a direct Go call, no socket to commerce.hanzo.svc:8001. The base is
// pinned NON-EMPTY (real CLOUD_COMMERCE_HTTP_URL, else the in-process placeholder) so
// the gate stays ENABLED even after the standalone + its env are retired — a metering
// gate that silently no-ops is a free-money hole, so it must never drop to
// "not configured" while commerce is co-resident. The transport resolves the handler
// lazily (published by commerce.Mount before any request), so building the client
// here (pre-MountAll) is fine.
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
			base = commerceinproc.PlaceholderBase
		}
		httpClient = commerceinproc.Client(0) // in-process dispatch; no network timeout
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

// wireTierReader installs the embedded ai module's per-tier SKU gate reader so it
// resolves the caller's commerce subscription tier through the SAME co-resident
// commerce client the metering gate bills over — in-process (commerceinproc) when
// commerce is folded in, S2S HTTP with the service token otherwise — NEVER an authed
// self-call to the cloud edge. That self-call is the toothless-gate bug: the edge
// 401/403s a service call to /v1/billing/*, so the ai module's own HTTP lookup always
// returned "" in-cluster and every tier-gated SKU failed OPEN. This mirrors
// wireFinance's SetBalanceReader: cloud owns the co-resident read, ai stays
// transport-agnostic. Fail-safe is preserved — Client.Tier folds a commerce error or
// an unknown plan to "", which the gate treats as ALLOW, so a commerce blip never
// locks out a paying caller. No-op when commerce is unreachable (metering !Enabled),
// leaving ai's standalone HTTP fallback in place.
func wireTierReader(m *metering.Client, log luxlog.Logger) {
	if m == nil || !m.Enabled() {
		return
	}
	aiobject.SetTierReader(func(ctx context.Context, subject, namespace string) (string, error) {
		return m.Tier(ctx, subject, namespace)
	})
	log.Info("ai per-tier SKU gate wired to co-resident commerce (in-process tier read, fail-safe)")
}

// wireFinance constructs the ONE in-process finance ledger (per-org SQLite
// double-entry prepaid wallet), publishes it for every money consumer to resolve by
// the narrow finance.Client, and installs the embedded ai router's balance-read +
// usage-debit hooks so the PREPAID gate dispatches DIRECTLY to it — a typed in-proc
// call, no HTTP, no socket. There is NO exempt path (hanzoai/ai >= v1.805.8): every
// principal is gated on a positive prepaid balance, fail-closed. MUST run before
// ai.Mount (the ai gate reads the hook per request; the hook must be installed first)
// — which BuildDeps guarantees (deps are built before MountAll).
func wireFinance(cfg *Config, log luxlog.Logger) {
	if !cfg.Enabled("commerce") {
		return // money layer not co-resident (split-deploy); ai falls back to HTTP.
	}
	fin := finance.New(cfg.DataDir)
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
	aiobject.SetBalanceReader(func(ctx context.Context, subject, namespace, currency string) (int64, error) {
		bal, err := fin.Balance(ctx, namespace, subject, currency, false)
		if err != nil {
			return 0, err
		}
		return bal.Cents(), nil
	})
	// The DEBIT is exact: the ai module emits the cost as a decimal-USD string, parsed
	// here to 18-decimal USD (1e-18) so a sub-cent call bills precisely and is never floored.
	aiobject.SetUsageRecorder(func(ctx context.Context, u aiobject.UsageEvent) error {
		amt, err := money.ParseUSD(u.USD)
		if err != nil {
			return err
		}
		return fin.RecordUsage(ctx, types.UsageInput{
			Org: u.Namespace, Subject: u.Subject, Amount: amt,
			Currency: u.Currency, Model: u.Model, Provider: u.Provider, RequestID: u.RequestID,
		})
	})
	log.Info("finance ledger wired (per-subject wallet in the org ledger, 18-decimal-exact, fail-closed)", "dataDir", cfg.DataDir)
}

// pick resolves one inter-subsystem client under the HIP-0106 wiring rule shared
// by every co-resident-capable dependency: enabled in THIS process → zero value
// (nil) so the subsystem's own Mount installs the in-process client; not enabled
// but a ZAP endpoint is configured → an RPC client at that endpoint; neither →
// the fail-closed/no-op disabled stub. name is the enable-list id; label is the
// deps.<X> log tag; rpc/disabled are the client's typed constructors. This is the
// ONE implementation of that rule — KMS/AI/VFS opt out with bespoke pickers only
// because their construction genuinely differs.
func pick[T any](cfg *Config, log luxlog.Logger, name, label, zapAddr string, rpc func(string) T, disabled func() T) T {
	if cfg.Enabled(name) {
		var zero T // enabled here → Mount fills deps.<label>
		return zero
	}
	if zapAddr != "" {
		log.Info("deps."+label+" → ZAP RPC", "addr", zapAddr)
		return rpc(zapAddr)
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
func pickKMSClient(cfg *Config, log luxlog.Logger) KMSClient {
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
		c, err := kmsClientFactory(cfg, log)
		if err != nil {
			log.Error("deps.KMS: embedded KMS unavailable, failing closed", "err", err)
			return clients.DisabledKMS()
		}
		return c
	}
	if cfg.KMSZAPAddr != "" {
		log.Info("deps.KMS → ZAP RPC", "addr", cfg.KMSZAPAddr)
		return clients.KMSRPCAt(cfg.KMSZAPAddr)
	}
	return clients.DisabledKMS()
}

// kmsClientFactory constructs the embedded in-process KMS client from cloud
// Config. clients/kms registers it in init(); pickKMSClient calls it so cloud
// depends on the KMSClient interface + this hook, never the concrete kms package
// — the same inversion the subsystem Registry already uses (cloud mounts every
// subsystem it never imports). Exactly one registration.
var kmsClientFactory func(cfg *Config, log luxlog.Logger) (KMSClient, error)

// RegisterKMSClientFactory installs the embedded-KMS constructor. clients/kms
// calls this from its init(); it is the ONE inversion point that lets the KMS
// library and its /v1/kms subsystem share one package with no cloud⇄kms cycle.
func RegisterKMSClientFactory(f func(cfg *Config, log luxlog.Logger) (KMSClient, error)) {
	kmsClientFactory = f
}

// ---- git-push-to-deploy ----

// GitPushEvent describes a push that just landed on the embedded git server: the
// org, the repo, the branch that moved, and its new tip commit. CloneURL is the
// canonical clone URL of that repo (https://<host>/v1/git/<org>/<repo>.git) — the
// exact value an Application's RepoURL carries — so the builder can resolve which
// app (if any) tracks this branch and needs a rebuild.
type GitPushEvent struct {
	Org      string
	Project  string
	Repo     string
	Branch   string
	Commit   string
	CloneURL string
}

// pushBuilder is the registered git-push-to-deploy trigger. clients/platform
// installs it in Mount; clients/git calls OnGitPush after a push lands. The
// inversion keeps git⇄platform decoupled — git never imports platform — exactly
// like kmsClientFactory and the subsystem Registry. Exactly one registration.
var pushBuilder func(ctx context.Context, ev GitPushEvent) error

// RegisterPushBuilder installs the git-push-to-deploy trigger. clients/platform
// calls this from its Mount when co-resident; it is the ONE inversion point that
// lets the embedded git server launch a platform build with no git⇄platform cycle.
func RegisterPushBuilder(f func(ctx context.Context, ev GitPushEvent) error) {
	pushBuilder = f
}

// OnGitPush fires the registered push-to-deploy trigger for a landed push. It is a
// no-op when no builder is registered (git server running without the platform
// subsystem co-resident). Best-effort by contract: the caller must never fail the
// push the client already committed just because a build could not be triggered.
func OnGitPush(ctx context.Context, ev GitPushEvent) error {
	if pushBuilder == nil {
		return nil
	}
	return pushBuilder(ctx, ev)
}

// ---- first-party service release (push→build→image→CR rollout) ----

// ServiceReleaseEvent describes a proven, clean-semver image ready to roll live on
// an operator-managed first-party service. It is the payload of the release seam
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

// serviceReleaser is the registered first-party CR-rollout seam. clients/paas
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

// ServiceReleaserRegistered reports whether a first-party CR-rollout hook is
// installed (the paas control plane is co-resident). A caller uses it to know
// whether OnServiceRelease actually patches a CR or is a no-op, so it can be
// honest about which rollout path took effect.
func ServiceReleaserRegistered() bool { return serviceReleaser != nil }

// OnServiceRelease rolls a proven image live by patching the matching hanzo.ai/v1
// Service CR's spec.image (the operator then reconciles the Deployment). It is a
// no-op when no releaser is registered (a binary without the paas control plane
// co-resident). The releaser enforces the clean-semver gate and CR-name
// resolution; this is only the dispatch seam.
func OnServiceRelease(ctx context.Context, ev ServiceReleaseEvent) error {
	if serviceReleaser == nil {
		return nil
	}
	return serviceReleaser(ctx, ev)
}

// ---- git lifecycle event stream ----
//
// One event, many subscribers. push-to-deploy (OnGitPush) is the deploy
// subscriber-of-record and stays exactly as it is; this seam generalizes the SAME
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
//     arrived via an inbound mirror sync — the loop-prevention seam that lets the
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

// ResetLifecycleSubscribers clears the registry. TEST-ONLY seam (a test mounts and
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
// client via the factory apps/commerce.go registers in init() — a direct Go
// call that reads the embedded commerce datastore (hanzoai/commerce MODULE, since
// the un-fork) + the @hanzo/plans vocabulary, no network hop (the HIP-0106
// co-resident default). The factory inversion stays because the concrete client
// (clients/commerceinproc) imports clients/plan, which imports cloud — a direct
// call here would be a package cycle. Absent the registration it fails closed
// rather than pretending.
//
// NETWORK PATH PRESERVED: absent co-residency the ZAP-RPC + disabled fallbacks apply
// (out-of-process commerce, or not wired), so the remote proxy seam
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
	if cfg.CommerceZAPAddr != "" {
		log.Info("deps.Commerce → ZAP RPC", "addr", cfg.CommerceZAPAddr)
		return clients.CommerceRPCAt(cfg.CommerceZAPAddr)
	}
	return clients.DisabledCommerce()
}

// commerceClientFactory constructs the embedded in-process Commerce client.
// apps/commerce.go registers it in init(); pickCommerceClient calls it so
// package cloud depends on the CommerceClient interface + this hook, never the
// concrete commerceinproc package (whose entitlement client pulls clients/plan,
// which imports cloud — the hook is what keeps the package graph acyclic).
var commerceClientFactory func(cfg *Config, log luxlog.Logger) CommerceClient

// RegisterCommerceClientFactory installs the embedded-Commerce client constructor.
// apps/commerce.go calls this from its init(); exactly one registration.
func RegisterCommerceClientFactory(f func(cfg *Config, log luxlog.Logger) CommerceClient) {
	commerceClientFactory = f
}

// publishableKey reports whether apiKey is a read-only PUBLISHABLE key (pk-). The
// Hanzo gateway 403s a publishable key on any WRITE endpoint — "Publishable keys
// can only access read-only endpoints … use a secret key (sk-)". So the COMPLETIONS
// resolver must refuse it (it would only 403 chat), while the EMBED resolver accepts
// it (embeddings ARE read-only). pk- is the IAM key family's read-only member
// (hk-/sk-/pk-/fw_/hz_; clients/admission). This ONE predicate is the split's crux:
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
//     (non-pk-) static key are configured — an explicit operator override (sk-/hk-).
//     A pk- key here is REFUSED (it would only 403 chat) and the resolver falls
//     through to M2M — THE fix for the intermittent publishable-key 403 on bot replies.
//  2. M2M HTTP gateway when a base URL AND the binary's IAM identity are present
//     (the durable Hanzo default): the client mints+refreshes a client-credentials
//     token from IAM_CLIENT_ID/SECRET — no static key to rotate. Secret never logged.
//  3. ZAP RPC when an addr is configured (split-deploy of a future ai subsystem).
//  4. Fail-closed stub otherwise — a run records an honest error, never fakes one.
func pickCompletionsClient(cfg *Config, log luxlog.Logger) AIClient {
	if cfg.AIBaseURL != "" && cfg.AIAPIKey != "" && !publishableKey(cfg.AIAPIKey) {
		log.Info("deps.AI (completions) → HTTP gateway (static secret key)", "base_url", cfg.AIBaseURL, "default_model", cfg.AIDefaultModel)
		return clients.AIHTTPAt(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIDefaultModel)
	}
	if cfg.AIAPIKey != "" && publishableKey(cfg.AIAPIKey) {
		log.Info("deps.AI (completions) → refusing read-only publishable (pk-) key for chat; using M2M", "base_url", cfg.AIBaseURL)
	}
	if cfg.AIBaseURL != "" && cfg.AIAuthClientID != "" && cfg.AIAuthClientSecret != "" {
		tokenURL := aiM2MTokenURL(cfg)
		if tokenURL != "" {
			log.Info("deps.AI (completions) → HTTP gateway (IAM M2M)", "base_url", cfg.AIBaseURL,
				"token_url", tokenURL, "client_id", cfg.AIAuthClientID, "default_model", cfg.AIDefaultModel)
			return clients.AIHTTPM2M(cfg.AIBaseURL, tokenURL, cfg.AIAuthClientID, cfg.AIAuthClientSecret, cfg.AIDefaultModel)
		}
	}
	if cfg.AIZAPAddr != "" {
		log.Info("deps.AI (completions) → ZAP RPC", "addr", cfg.AIZAPAddr)
		return clients.AIRPCAt(cfg.AIZAPAddr)
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
	if cfg.AIBaseURL != "" && cfg.AIAPIKey != "" {
		log.Info("deps.Embed → HTTP gateway (static embed key)", "base_url", cfg.AIBaseURL, "default_model", cfg.AIDefaultModel)
		return clients.AIHTTPAt(cfg.AIBaseURL, cfg.AIAPIKey, cfg.AIDefaultModel)
	}
	return pickCompletionsClient(cfg, log)
}

// aiM2MTokenURL resolves IAM's client_credentials endpoint the agent runner mints
// its M2M inference token at. It MUST be reachable FROM INSIDE THE CLUSTER: the
// runner runs in-cluster and the public issuer host (https://hanzo.id) is fronted
// by Cloudflare, which 403s a server-side (non-browser) loopback POST with edge
// error 1006 — so minting against the PUBLIC issuer URL fails and every
// POST /v1/agents/:ref/run 502s (root-caused 2026-07-04: in-cluster POST to
// https://hanzo.id/v1/iam/oauth/token → 403/1006, while http://iam.hanzo.svc/... → 200).
// This mirrors the KMS login-broker resolution (clients/kms) exactly — one
// split-horizon policy, no drift. Prefer, in order: an explicit override
// (CLOUD_AI_IAM_TOKEN_URL), the in-cluster IAM service base (IAM_URL — already
// wired to http://iam.hanzo.svc for JWKS), then the public issuer as a last resort
// (single-process / no split-horizon deploys). Returns "" only when no identity is
// resolvable, which keeps the M2M branch off (caller falls through to the stub).
func aiM2MTokenURL(cfg *Config) string {
	if override := strings.TrimSpace(os.Getenv("CLOUD_AI_IAM_TOKEN_URL")); override != "" {
		return override
	}
	if base := strings.TrimRight(strings.TrimSpace(os.Getenv("IAM_URL")), "/"); base != "" {
		return base + "/v1/iam/oauth/token"
	}
	if iss := strings.TrimRight(strings.TrimSpace(cfg.IAMIssuer), "/"); iss != "" {
		return iss + "/v1/iam/oauth/token"
	}
	return ""
}

func pickVFSClient(cfg *Config, log luxlog.Logger) VFSClient {
	// deps.VFS must NEVER be nil (R-7): files.go and any other VFS consumer call
	// s.vfs.Put/Get/Delete unconditionally, so a nil here is a per-request 500
	// (dishonest degradation) instead of a fail-closed 502. Unlike the
	// nil-then-Mount-fills convention other subsystems use, nothing fills deps.VFS
	// after MountAll (Mount receives deps by value), so we ALWAYS hand back a
	// concrete client.
	if cfg.VFSZAPAddr != "" {
		log.Info("deps.VFS → ZAP RPC", "addr", cfg.VFSZAPAddr)
		return clients.VFSRPCAt(cfg.VFSZAPAddr)
	}
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

// buildDurability constructs the deployment's HA-durability factory, or nil when the
// deployment has no object store to be durable against (dev/single-node — every
// OrgStore then stays local-only). It composes the SeaweedFS S3 If-Match
// ConditionalStore (the SAME s3admin identity deps.VFS uses), the writer membership
// over CLOUD_PEERS (the SAME set the shard router elects on, so the store-layer owner
// and the routed owner agree), and the per-org envelope Cipher rooted at the KMS
// master. Any construction failure fails SAFE to nil (local-only) rather than crash
// the boot; an encryption-capable build with no usable cipher is REFUSED — a build
// that promises encryption never ships plaintext snapshots to the object store.
func buildDurability(cfg *Config, log luxlog.Logger) *Durability {
	// A multi-replica deployment REQUIRES the durable plane: with >1 writer, a per-org
	// store that is not hydrate-on-open + fenced is the outage this exists to fix.
	// disabledDurability logs at the severity the replica count warrants, so a
	// misconfigured prod deployment is never SILENTLY non-durable (Red L2).
	multiReplica := len(parsePeers(cfg.ShardPeers)) > 1

	// Explicit opt-in: the object-store fence rests on the deployed SeaweedFS enforcing
	// conditional-PUT (If-Match) atomically, which must be validated against the deployed
	// version before it fences real tenant data (the takeover-fence staging gate). Until
	// CLOUD_RESEARCH_DURABLE is set the store runs local-only — the shard router still
	// pins each org to one writer, so this is not the rolling-deploy outage; only the
	// cross-restart object-store snapshot waits for the opt-in.
	if !cfg.ResearchDurable {
		disabledDurability(log, multiReplica, "CLOUD_RESEARCH_DURABLE not set — HA object-store durability is opt-in pending the SeaweedFS conditional-PUT atomicity gate")
		return nil
	}

	admin := s3admin.New()
	if !admin.Configured() {
		disabledDurability(log, multiReplica, "no S3 admin creds (S3_ADMIN_* unset)")
		return nil
	}
	client, err := admin.Client()
	if err != nil {
		disabledDurability(log, multiReplica, fmt.Sprintf("S3 client construction failed: %v", err))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := ensureDurableBucket(ctx, admin, client); err != nil {
		// Non-fatal: the bucket likely already exists; a later ship/hydrate retries.
		log.Warn("durability bucket ensure failed (continuing)", "bucket", durableBucket, "err", err)
	}
	cancel()

	// Membership: CLOUD_PEERS (the shard router's set). A single-pod deployment with
	// no peers is its own sole writer — still hydrate-on-open + fenced ship across a
	// rolling restart.
	self := firstNonEmptyStr(strings.TrimSpace(cfg.ShardSelf), hostnameOr("cloud-0"))
	peers := parsePeers(cfg.ShardPeers)
	if len(peers) == 0 {
		peers = []org.Member{{ID: self, Addr: self}}
	}
	members := org.NewMembership(self, org.StaticSource(peers...), 5*time.Second)
	_ = members.Start(context.Background()) // static source: the initial refresh populates Members()

	cipher := durableCipher(cfg, log)
	if cipher == nil && cek.Encrypting() {
		// The master that satisfied cek must decode here too, so this is a genuine
		// misconfig, not a dev path: never ship plaintext snapshots AND never silently
		// drop durability — fail closed and log LOUDLY for the replica count.
		disabledDurability(log, multiReplica, "encryption-capable build but no durable cipher (would ship plaintext snapshots)")
		return nil
	}

	log.Info("durability enabled", "bucket", durableBucket, "self", self, "peers", len(peers), "encrypted", cipher != nil)
	return org.NewDurability(org.NewS3ConditionalStore(client, durableBucket), members, cipher)
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

// hostnameOr returns the OS hostname, or def when unavailable — a stable self id for
// a single-pod deployment that sets no CLOUD_POD_NAME.
func hostnameOr(def string) string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return def
}

func pickPaymentsClient(cfg *Config, log luxlog.Logger) PaymentsClient {
	if cfg.PaymentsZAPAddr != "" {
		log.Info("deps.Payments → ZAP RPC", "addr", cfg.PaymentsZAPAddr)
		return clients.PaymentsRPCAt(cfg.PaymentsZAPAddr)
	}
	return clients.DisabledPayments()
}

func pickVaultClient(cfg *Config, log luxlog.Logger) VaultClient {
	if cfg.VaultZAPAddr != "" {
		log.Info("deps.Vault → ZAP RPC", "addr", cfg.VaultZAPAddr)
		return clients.VaultRPCAt(cfg.VaultZAPAddr)
	}
	return clients.DisabledVault()
}

// MountFunc is a subsystem's mount contract: register your routes on app, using
// deps for everything shared. Every subsystem in the fleet exports exactly this
// signature, so Wire references each one directly and the compiler checks it.
//
// app was once `any`, on the stated grounds that an external module (licensing)
// exposed func(any, Deps) error and narrowing would break it — while licensing
// said it used `any` to avoid an import cycle in pkg/cloud. Each cited the other,
// and the cycle could not exist: this package already imports zip, and zip does
// not import cloud. The `any` bought nothing and cost every subsystem a Typed()
// wrapper plus a runtime type assertion whose failure branch was unreachable.
type MountFunc func(app *zip.App, deps Deps) error

// ShutdownFunc releases a subsystem's process-lifetime resources (background
// goroutines, open DB handles) on graceful shutdown. It must be idempotent and
// bounded — Serve calls it within the shutdown deadline. ctx carries that
// deadline so a slow teardown is cut off rather than hanging SIGTERM.
type ShutdownFunc func(ctx context.Context) error

// MountSpec describes one subsystem to mount. There is NO Order field: the slice
// position in apps.Wire() IS the mount order — the composition root lists
// subsystems in the exact sequence they mount (and, reversed, tear down), so order
// is data read top-to-bottom in one file, not ints scattered across the tree.
type MountSpec struct {
	Name     string
	Mount    MountFunc
	Shutdown ShutdownFunc // optional; nil means the subsystem has nothing to tear down.

	// OwnsHealth marks a subsystem that serves its OWN GET /v1/<name>/health
	// (a real, fail-closed probe). Serve's generic liveness loop skips these so
	// its always-ok route never shadows the subsystem's real probe.
	OwnsHealth bool
}

// MountAll mounts every ENABLED subsystem in specs, in slice order — the order is
// the composition root's (apps.Wire()); MountAll does NOT sort. app is the
// concrete *zip.App from Serve, handed to each MountFunc as itself.
//
// Teardown is wired HERE, at mount time: right after a subsystem mounts, its
// ShutdownFunc (if any) is registered via app.OnShutdown. zip drains those hooks
// LIFO — AFTER the listeners stop accepting and in-flight requests drain — so
// registration-at-mount yields reverse-mount teardown (a dependency mounted before
// its dependents is torn down after them) with no subsystem torn down while a
// request still uses it. Only ENABLED specs mount, so only they register a hook;
// teardown needs no separate enablement gate.
func MountAll(app *zip.App, specs []MountSpec, cfg *Config, deps Deps) error {
	logger := deps.Logger
	for _, spec := range specs {
		if !cfg.Enabled(spec.Name) {
			logger.Debug("subsystem disabled", "name", spec.Name)
			continue
		}
		if err := spec.Mount(app, deps); err != nil {
			return fmt.Errorf("mount %s: %w", spec.Name, err)
		}
		// Register teardown as a zip shutdown hook. zip runs hooks LIFO after the
		// drain (zip.App.Shutdown), so this reproduces the reverse-mount order the
		// hand-rolled reverse-loop gave — without the teardown-before-drain race.
		if spec.Shutdown != nil {
			app.OnShutdown(spec.Shutdown)
		}
		logger.Info("mounted subsystem", "name", spec.Name)
	}
	return nil
}
