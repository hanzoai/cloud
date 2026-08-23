// fleet.go — the platform's view of its OWN service tier.
//
// It mounts /v1/platform/fleet on the unified cloud binary and reads the operator's
// `hanzo.ai/v1` `App` CustomResource — the one workload kind the fleet runs on:
//
//	GET  /v1/platform/fleet            — the fleet drift board: list every operator
//	                                     App CR across the platform namespaces, read
//	                                     declared vs running tag + health from the CR
//	                                     (+ its status), and attach the drift verdict
//	                                     (drift.go).
//	GET  /v1/platform/fleet/:app       — one app row by CR name.
//	POST /v1/platform/fleet/:app/deploy— zero-downtime ROLLING RESTART of the app's
//	                                     Deployment (the `kubectl rollout restart`
//	                                     mechanism): re-pulls the DECLARED image,
//	                                     recreates pods gracefully. It never changes the
//	                                     declared TAG (that stays a git commit, the one
//	                                     thing Hanzo CD's selfHeal reconciles), so there
//	                                     is no drift to revert. This is `hanzo deploy`.
//
// FLEET vs PROJECTS/:project/APPS — two collections, two names. `fleet` is the
// PLATFORM's own tier (the shared services the platform itself runs on: iam, kms,
// gateway, …), observed from the operator App CRs. `projects/:project/apps` is a
// CUSTOMER's apps, owned by platform's own store. They answer different questions,
// so they carry different names under the one /v1/platform prefix. This file used to
// be a second top-level product (`/v1/paas`) — the same platform under a second
// name, which is exactly the duplicate definition the one-way rule forbids.
//
// SECURITY — every route is authorized off ONE IAM identity through the
// platform's one gate (cloud.Scope.Admits, gate.go), applied at the top of each
// typed op by board.admit: the read routes take cloud.Admin,
// which admits a validated principal who is a SuperAdmin OR an OrgAdmin, the
// deploy takes cloud.Super, and each handler then
// CONFINES a non-super caller to its own org's platform namespaces
// (scopedNamespaces, keyed on principal.Org — never a client header). A SuperAdmin
// observes/acts on the whole fleet; an OrgAdmin only on the namespaces its org owns;
// a plain member or an unauthenticated caller is refused 403. So a tenant admin can
// never observe — or restart — another org's, or a platform, app, and the platform
// operator drives the board off a plain `hanzo login` with no shared token.
//
// k8s client: built in-process from the in-cluster service account
// (rest.InClusterConfig) with a KUBECONFIG fallback for local/dev — the identical
// construction clients/ml uses. When no kubeconfig is resolvable the board mounts
// anyway and every endpoint fails closed (503 + the real init error; the shared
// /v1/platform/health route reports "degraded"), never status-theater.

package platform

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/k8s"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/namespace"
	"github.com/zap-proto/zip"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// k8s.Apps is the operator App CR — the one workload kind the fleet runs on. The
// drift board reads it across the platform namespaces. Asserted in the tests; a
// typo here silently breaks the whole board.

// k8s.Deployments is the live Deployment behind each Service — the source of the
// RUNNING tag (the operator Service CR status does not surface the running image,
// so the running tag is observed from the Deployment's container, exactly as the
// platform inventory reads it in inventory.ts). Read-only for this subsystem.

// nsClass is THE namespace classifier: the one place that decides what a platform
// namespace MEANS. Total — every input yields a decision and an unrecognised
// namespace is classified OUT (ok=false) rather than guessed at, so the reader can
// never reach beyond the platform tier.
//
// It replaces three separate encodings of this single fact, which had drifted out
// of agreement: the nsEnv map (3 namespaces), nsOrg's suffix-strip (knew only
// -devnet/-testnet), and scanOrder's literal list (the same 3). Because a
// `tenant-<org>` namespace matched none of them, it classified as its own org and
// was never scanned — every tenant workload was invisible BY CONSTRUCTION. Deriving
// tenant and env from one function is what makes that class of drift impossible:
// there is no second place left to disagree with.
//
// tenant is the authorization axis (scopedNamespaces confines a non-super OrgAdmin
// to namespaces whose tenant equals their validated org); env is the lifecycle
// label. Cross-cluster federation (lux-k8s/zoo-k8s) is a follow-up phase.
func nsClass(ns string) (tenant, env string, ok bool) {
	switch ns {
	case "hanzo", "hanzo-mainnet":
		return "hanzo", "main", true
	case "hanzo-testnet":
		return "hanzo", "test", true
	case "hanzo-devnet":
		return "hanzo", "dev", true
	}
	// tenant-<org>: a customer's own namespace. The org IS the tenant key, so it
	// authorizes exactly like a first-party namespace with no special case.
	if t, found := strings.CutPrefix(ns, "tenant-"); found && t != "" {
		return t, "main", true
	}
	return "", "", false
}

// envOf is nsClass's env projection ("" when the namespace is not ours).
func envOf(ns string) string { _, env, _ := nsClass(ns); return env }

// k8s.Namespaces backs discovery. Listing NAMESPACES is the honest question — "which
// namespaces are ours?" — and asks it of the one authority that knows. Deriving the
// set from a cluster-wide CR list would answer a different question ("where are
// there CRs?") and pull every tenant's objects through this process to do it.

// nsScanTTL bounds how stale a discovered scan set may be. A new tenant namespace
// appears on the board within this window without a redeploy.
const nsScanTTL = 60 * time.Second

// discoverNamespaces returns every namespace in the cluster that nsClass recognises,
// first-party ones first (main before test/dev, so a bare app name still resolves to
// production) then tenants in stable order.
//
// This is what finally makes tenant-<org> workloads visible: they were classified
// correctly but never SCANNED, because the scan set was a literal list. Discovery
// asks the cluster instead of hardcoding an answer that goes stale the moment a
// tenant is onboarded.
//
// Fail-SAFE, never fail-open: if the list fails we fall back to the first-party set,
// which is narrower than the truth — a tenant admin can lose visibility of its own
// namespace, never gain visibility of someone else's. nsClass still filters, so a
// namespace that is not ours can never enter the set however discovery goes.
func discoverNamespaces(s *cloud.Service[fleetState], ctx context.Context) []string {
	cache := s.State.scan
	if cache != nil {
		cache.mu.Lock()
		if cache.ns != nil && time.Since(cache.at) < nsScanTTL {
			cached := append([]string(nil), cache.ns...)
			cache.mu.Unlock()
			return cached
		}
		cache.mu.Unlock()
	}

	if s.State.dyn == nil {
		return scanOrder()
	}
	list, err := s.State.dyn.Resource(k8s.Namespaces).List(ctx, metav1.ListOptions{})
	if err != nil {
		return scanOrder()
	}
	// The first-party set is ALWAYS present: it is known-good by definition, and a
	// namespace that does not exist simply yields no CRs (observeFleet tolerates
	// NotFound). Discovery only ever ADDS tenants — so an empty or partial listing
	// degrades to today's behavior instead of blanking the board.
	firstParty := scanOrder()
	known := make(map[string]bool, len(list.Items))
	for i := range list.Items {
		if _, _, ok := nsClass(list.Items[i].GetName()); ok {
			known[list.Items[i].GetName()] = true
		}
	}
	out := make([]string, 0, len(known)+len(firstParty))
	for _, ns := range firstParty { // stable, main-first
		out = append(out, ns)
		delete(known, ns)
	}
	out = append(out, slices.Sorted(maps.Keys(known))...)

	if cache != nil {
		cache.mu.Lock()
		cache.ns, cache.at = out, time.Now()
		cache.mu.Unlock()
	}
	return append([]string(nil), out...)
}

// appNameRE constrains the :app path segment to a DNS-1123 label (every Service
// CR metadata.name satisfies this). Validated at the boundary; it is the
// injection guard for the CR name a deploy/read targets.
var appNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// imageRepoRE constrains a deploy's target image repository. A registry path of
// host/namespace/name segments (letters, digits, ., -, _, /). The tag is
// validated separately (deploy accepts any non-empty tag so a controlled hotfix
// to a floating tag is possible, but the drift board then flags it loudly).
var imageRepoRE = regexp.MustCompile(`^[a-z0-9][a-z0-9._/-]*[a-z0-9]$`)

const fleetUserAgent = "hanzo-cloud-platform-fleet"

// fleetState is the fleet board's own data; shared deps live in the embedded cloud.Base.
type fleetState struct {
	dyn     dynamic.Interface // nil when no kubeconfig resolved (fail-closed)
	initErr string            // why dyn is nil, surfaced by health + ready()

	// the discovered scan set (discoverNamespaces), rebuilt every nsScanTTL so a
	// newly-onboarded tenant namespace appears without a redeploy. A POINTER:
	// cloud.Service stores State by value, so a mutex living directly in `state`
	// would be copied (go vet: "assignment copies lock value") and each copy would
	// guard nothing. One cache, shared by every copy of the struct.
	scan *nsCache
}

// nsCache is the discovered scan set behind its own lock.
type nsCache struct {
	mu sync.Mutex
	ns []string
	at time.Time
}

// buildFleet resolves the in-process k8s dynamic client (fail-closed: when no
// kubeconfig resolves the board still mounts and every endpoint 503s honestly).
func buildFleet(b cloud.Base) fleetState {
	st := fleetState{scan: &nsCache{}}
	if dyn, err := newFleetDynamic(); err != nil {
		st.initErr = err.Error()
		b.Log.Warn("kubernetes client unavailable; /v1/platform/fleet will fail closed", "err", err)
	} else {
		st.dyn = dyn
	}
	b.Log.Info("fleet board mounted",
		"prefix", "/v1/platform/fleet", "k8s", st.dyn != nil, "brand", b.Brand, "env", b.Env)
	return st
}

// fleetRoutes registers the /v1/platform/fleet surface. Every mutating/observing
// route is behind the IAM guard (SuperAdmin or org-confined OrgAdmin).
//
// It is a SIBLING of the /v1/platform/projects/:project/apps surface, not a second
// copy of it: `fleet` is the platform's OWN service tier (the operator App CRs the
// platform runs on), `projects/:project/apps` is a CUSTOMER's apps. Two different
// collections need two different names — calling both "apps" under one prefix would
// be the duplicate definition this fold exists to remove.
func fleetRoutes(app *zip.App, s *cloud.Service[fleetState]) {
	b := board{s: s}
	// FLAT paths, not a Group: `Group("/v1/platform/fleet").Get("")` composes to the
	// literal "/v1/platform/fleet/", and that trailing slash is what the OpenAPI
	// emitter publishes — so every generated SDK would call a path the manifest
	// prefix does not name. Fiber happens to match both forms, which is exactly why
	// this hides: the router forgives it and the CONTRACT does not.
	//
	// The gate is no longer cloud.Guard around the handler, because a typed op has
	// no zip.Handler to wrap; it is board.admit as the first line inside each op,
	// applying the SAME cloud.Scope.Admits and answering the SAME cloud.Scope.Refusal.
	// The scope each route needs is stated in its own doc comment, which is where a
	// caller reads it.
	zip.Get(app, "/v1/platform/fleet", b.listFleet)
	zip.Get(app, "/v1/platform/fleet/:app", b.getFleetApp)
	// MUTATION is superadmin-only (cloud.Super), NOT the broader read gate: the
	// only namespaces this board scans are the platform's OWN tier (hanzo{,-testnet,
	// -devnet}), so a rolling restart here recreates a SHARED platform service
	// (iam/kms/gateway/…). Per the 2026-07-08 admin-org P0 a brand-org ("hanzo")
	// admin is a CUSTOMER-org admin, not a platform operator — restarting prod iam is
	// a platform-operator action. Gating the read board (below) any wider is bounded
	// (observe, audit-logged); gating a restart wider is a live DoS lever (RED H1).
	zip.Post(app, "/v1/platform/fleet/:app/deploy", b.deployFleet, zip.WithStatus(http.StatusAccepted))

	// Native release client: install the first-party CR-rollout hook (build.go's
	// RegisterServiceReleaser inversion) so a proven, clean-semver image rolls onto
	// its Service CR here — the direct-CR replacement for the image-update.yml
	// GitOps hop (rollout.go).
	registerReleaser(s)

	// And on the plane, for the reason registerReleaser alone was not enough: the
	// build that proves an image runs in a different process from this control
	// plane, so the in-process hook was nil on every release that mattered.
	exposeRelease(s)

	// Internal plane: platform.fleet answers THIS board's observation, bound to THIS
	// service, so the admin god-view (/v1/admin/products + the overview drift KPIs)
	// gets the same scan and the same tenant confinement listFleet applies (rpc.go).
	// Registering here rather than in Mount is what lets the method hold s: an
	// exposure with no service has only the package global to read, and that global
	// is the in-process client this whole move exists to delete.
	exposeFleet(s)
}

// THE ROLE GATE is admit(cloud.Admin) on the read routes and
// admit(cloud.Super) on the mutation, at the top of each op — the platform's
// one authorization rule (gate.go, HIP-0519), parameterised by how much authority
// each route needs. The read board admits a SuperAdmin OR an admin of its own
// org, which lets the platform operator drive it off a plain `hanzo login` with
// no shared token; the deploy/restart admits platform sudo only, because it
// recreates a SHARED platform service and a brand-org admin is a customer-org
// admin (the 2026-07-08 admin-org P0), which is also what closes the fleet-restart
// DoS lever (RED H1).
//
// The ROLE only admits; the TENANT boundary is enforced inside each handler by
// scopedNamespaces(c): a SuperAdmin observes the whole fleet, an OrgAdmin is
// CONFINED to the platform namespaces its own validated org owns, so a tenant
// admin can never observe — or restart — another org's, or a platform, app.
// This mirrors runner.go's org attribution (default to the caller's org, refuse a
// foreign one) for a READ/RESTART surface.

// fleetPrincipal is WHO is asking: the platform's three authority facts
// (cloud.Authority) plus the validated tenant key this surface confines on. It is
// a VALUE rather than a place, because the SAME caller arrives two ways — as an
// HTTP request on /v1/platform/fleet, and as a delegated capability on the
// internal plane (rpc.go) — and the two must reach the identical verdict.
// Extraction differs; the rule (cloud.Scope.Admits) does not.
type fleetPrincipal struct {
	cloud.Authority
	org string // the VALIDATED tenant key; "" when the caller carries none
}

// requestPrincipal reads one off an HTTP request — the authority headers
// SanitizeIdentity strips on ingress and re-mints only from validated claims.
func requestPrincipal(c *zip.Ctx) fleetPrincipal {
	org, _ := principal.Org(c)
	return fleetPrincipal{Authority: cloud.AuthorityOf(c), org: org}
}

// capPrincipal reads one off the identity a plane call carries. It is the SAME
// set of headers requestPrincipal reads, forwarded by zip from the gateway's
// assertion — a peer can only pass on authority it already held — and the org
// key goes through principal.OrgOf, the same decision Org applies to a request.
// So a caller cannot widen itself by crossing the socket.
func capPrincipal(who zip.Caller) fleetPrincipal {
	org, _ := principal.OrgOf(who.User, who.Org)
	return fleetPrincipal{
		Authority: cloud.Authority{
			Validated: strings.TrimSpace(who.User) != "",
			Super:     who.Admin,
			OrgAdmin:  who.OrgAdmin,
		},
		org: org,
	}
}

// mayObserve is the read board's check, on the plane transport: the SAME
// cloud.Admin scope the HTTP routes are guarded with, applied to the authority a
// capability carries. It only admits; scopeNamespaces is what confines the
// caller it admits.
func (p fleetPrincipal) mayObserve() bool { return cloud.Admin.Admits(p.Authority) }

// scopeNamespaces is the TENANT boundary — the one confinement rule, applied to the
// scanned set. A SuperAdmin sees every scanned namespace (the whole fleet). A
// non-super caller sees ONLY the namespaces its own validated org owns
// (nsOrg(ns) == org), bounded to its own tenant exactly as /v1/platform/runner bounds a build
// to the caller's org. The org comes from the validated principal — never a client
// header, never a request or payload field — so it cannot be widened by a forged
// X-Org-Id or by anything a peer puts on the wire. A caller whose org owns no
// scanned namespace gets an EMPTY set (an empty board / a clean 404), never another
// org's data.
//
// It is applied AT THE SCAN, before any CR is read, so a confined caller never even
// lists another org's apps — the boundary is not a filter over rows already fetched.
func scopeNamespaces(all []string, p fleetPrincipal) []string {
	if p.Super {
		return all
	}
	// ★ BOTH SIDES CANONICALISED. nsOrg returns a namespace's org key, which the
	// namespace was BUILT from with namespace.Sanitize; p.org is the RAW `owner`
	// claim. Comparing them directly applied the slugger to one side of an
	// authorization test: an org named "Acme" owns namespace "…acme-<hash>" and
	// so never matched its OWN rows, while any org whose raw name is the literal
	// "acme-<hash>" matched them instead — and the slugger is public code, so
	// that value is offline-computable. Sanitize is injective, so canonicalising
	// both sides is collision-free rather than merely symmetric. Found by red
	// alongside the identical bug on the delivery board (cd.go owns).
	slug := namespace.Sanitize(p.org)
	if slug == "" {
		return nil
	}
	out := make([]string, 0, len(all))
	for _, ns := range all {
		// owner() IS THE PREDICATE THE DELIVERY BOARD ASKS (cd.go owns). This read
		// used nsOrg, which maps the brand namespaces onto org "hanzo" — so the
		// ORG ADMIN of the brand org was handed the platform tier here while the
		// delivery board refused the identical caller. One question answered two
		// ways is two policies, and this one is the CTO rule that a per-org
		// isAdmin is NEVER platform-privileged (HIP-0519): observing the tier
		// every tenant runs on is a platform-operator act, which is exactly what
		// the deploy route below already says about restarting one.
		//
		// A SuperAdmin returned above, so nothing narrows for them; and a legacy
		// `tenant-<org>` namespace still resolves to its own org, so no tenant
		// loses its own board.
		if owner(ns) == slug {
			out = append(out, ns)
		}
	}
	return out
}

// scopedNamespaces is scopeNamespaces over an HTTP request: discover the scanned
// set, then confine it to what this caller may observe.
func scopedNamespaces(s *cloud.Service[fleetState], ctx context.Context, c *zip.Ctx) []string {
	return scopeNamespaces(discoverNamespaces(s, ctx), requestPrincipal(c))
}

// targetNamespaces narrows the caller's authorized namespaces (scopedNamespaces)
// to a single lifecycle env when ?env=main|test|dev is given, else returns them all
// in prod-first scan order. It composes the AUTH confinement (scopedNamespaces) with
// the optional env SELECTION — orthogonal: env can only narrow WITHIN the caller's
// own authorized set, never reach outside it.
func targetNamespaces(s *cloud.Service[fleetState], ctx context.Context, c *zip.Ctx, env string) []string {
	nss := scopedNamespaces(s, ctx, c)
	env = strings.TrimSpace(env)
	if env == "" {
		return nss
	}
	out := make([]string, 0, len(nss))
	for _, ns := range nss {
		if envOf(ns) == env {
			out = append(out, ns)
		}
	}
	return out
}

// nsOrg returns the platform org that OWNS a scanned namespace: the namespace with
// its lifecycle-env suffix removed ("hanzo"→hanzo, "hanzo-testnet"→hanzo,
// "hanzo-devnet"→hanzo). It is the tenant key a namespace belongs to — the axis
// scopedNamespaces confines a non-super OrgAdmin to. Derived from nsClass.
func nsOrg(ns string) string { tenant, _, _ := nsClass(ns); return tenant }

// nsForEnv maps a lifecycle env to its scanned namespace ("main"→hanzo,
// "test"→hanzo-testnet, "dev"→hanzo-devnet), "" for an unknown env. The inverse of
// envOf over the scanned set, used to reject an invalid ?env with a clean 400.
func nsForEnv(env string) string {
	for _, ns := range scanOrder() {
		if envOf(ns) == env {
			return ns
		}
	}
	return ""
}

// ── observe: the drift board (inventory.ts) ──────────────────────────────────

// AppView is one service row on the drift board: the observed tags + topology +
// the derived drift verdict. It is the Go analogue of the platform's `AppView`
// (apps-api.ts) so console renders the same shape the Dokploy board did.
type AppView struct {
	ID          string   `json:"id"`          // <org>/<app>/<env>, e.g. hanzoai/iam/main
	Org         string   `json:"org"`         // image namespace, e.g. hanzoai
	App         string   `json:"app"`         // service / CR name, e.g. iam
	Env         string   `json:"env"`         // main|test|dev
	Repo        string   `json:"repo"`        // owner/repo, e.g. hanzoai/iam
	Registry    string   `json:"registry"`    // spec.image.repository verbatim (ghcr.io/hanzoai/iam); Org and Repo are read off it
	Role        string   `json:"role"`        // operator spec.role (sql|kv|generic|ingress|…) or "" — the one declared class field
	DeclaredTag string   `json:"declaredTag"` // spec.image.tag — the tag the CR SAYS to run; anything but vX.Y.Z is red drift
	RunningTag  string   `json:"runningTag"`  // the tag the live Deployment actually runs; "" when unreadable — unknown, not a guess
	LatestTag   string   `json:"latestTag"`   // newest released tag; "" until the GH release reader lands, so "stale" cannot fire yet
	Health      string   `json:"health"`      // green|yellow|red|"" (unknown)
	Phase       string   `json:"phase"`       // operator status.phase (Running/…)
	Cluster     string   `json:"cluster"`     // always "hanzo-k8s"; cross-cluster federation is a follow-up phase
	Namespace   string   `json:"namespace"`   // namespace the row was scanned from: a platform one (hanzo, hanzo-testnet, …) or a tenant-<org>
	Endpoints   []string `json:"endpoints"`   // status.endpoints the operator published for this service; empty until it reconciles
	Drift       Verdict  `json:"drift"`       // declared vs running vs latest as flags, plus their rolled-up severity
}

// fleetQuery narrows the drift board. Every field rides the query string, which is
// what a board's filters have always been.
type fleetQuery struct {
	// Env narrows to one lifecycle env: main, test or dev.
	Env string `json:"env"`
	// Health narrows to one health colour: green, yellow or red.
	Health string `json:"health"`
	// Org narrows to one image namespace.
	Org string `json:"org"`
	// Drift is `1` or `true` to show only rows that have actually drifted. It is
	// a STRING and not a bool because those two spellings are exactly what the
	// board has always accepted, and a bool would silently widen that to `?drift`
	// alone and to `TRUE` — a behaviour change wearing a type change's clothes.
	Drift string `json:"drift"`
}

// driftTally counts a board by drift severity.
type driftTally struct {
	// OK is how many rows run what they declare.
	OK int `json:"ok"`
	// Yellow is how many have drifted within tolerance.
	Yellow int `json:"yellow"`
	// Red is how many have drifted badly.
	Red int `json:"red"`
}

// fleetSummary is the board's roll-up.
type fleetSummary struct {
	// Total is how many rows the board returned, after filtering.
	Total int `json:"total"`
	// ByDrift counts those rows green, yellow and red.
	ByDrift driftTally `json:"byDrift"`
}

// driftBoard is the platform's own service tier and where it has drifted.
type driftBoard struct {
	// Apps are the service rows, ordered by org, then app, then env.
	Apps []AppView `json:"apps"`
	// Summary counts the board by drift severity.
	Summary fleetSummary `json:"summary"`
}

// listFleet returns the platform's own service tier, and where it has drifted.
//
// It returns the board for the services the PLATFORM itself runs — iam, kms,
// gateway and the rest — as `{apps, summary}`: per service its environment, health,
// phase, the image tag its CR DECLARES, the tag actually running, and the drift
// between them, plus a summary counting the board green, yellow and red.
//
// This is not a customer surface. `/v1/platform/projects/:project/apps` is a
// tenant's apps; this is the tier those tenants run ON, which is why the two are
// named differently rather than sharing a prefix.
//
// Admission is scoped at the SCAN, before any CR is read: a platform SuperAdmin
// observes the whole fleet, an org admin observes only their own org's namespaces,
// and an org that owns none gets an empty board — a non-super caller never even
// lists another org's services. Narrow further with `env`, `health`, `org`, or
// `drift=1` for only what has drifted.
//
// It degrades honestly rather than failing whole: a namespace that does not exist
// is skipped, and a running-state read the caller cannot make leaves the running
// tag empty — an unknown, never a guess — while the declared, health and phase
// columns still render.
func (b board) listFleet(ctx context.Context, in *fleetQuery) (*driftBoard, error) {
	s := b.s
	c, err := admit(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	if err := fleetReady(s); err != nil {
		return nil, err
	}
	// Observe only the namespaces this caller is authorized for: the whole fleet for
	// a SuperAdmin, the caller's own org namespaces for an OrgAdmin (empty board for
	// an org that owns none). The tenant boundary is applied at the scan, before any
	// CR is read, so a non-super caller never even lists another org's apps.
	views, err := observeFleet(s, ctx, scopedNamespaces(s, ctx, c))
	if err != nil {
		return nil, err
	}

	env := strings.TrimSpace(in.Env)
	fleetHealth := strings.TrimSpace(in.Health)
	org := strings.TrimSpace(in.Org)
	driftOnly := in.Drift == "1" || in.Drift == "true"

	out := make([]AppView, 0, len(views))
	byDrift := map[DriftSeverity]int{SeverityOK: 0, SeverityYellow: 0, SeverityRed: 0}
	for _, v := range views {
		if env != "" && v.Env != env {
			continue
		}
		if fleetHealth != "" && v.Health != fleetHealth {
			continue
		}
		if org != "" && v.Org != org {
			continue
		}
		if driftOnly && v.Drift.Severity == SeverityOK {
			continue
		}
		out = append(out, v)
		byDrift[v.Drift.Severity]++
	}

	return &driftBoard{
		Apps: out,
		Summary: fleetSummary{
			Total: len(out),
			ByDrift: driftTally{
				OK:     byDrift[SeverityOK],
				Yellow: byDrift[SeverityYellow],
				Red:    byDrift[SeverityRed],
			},
		},
	}, nil
}

// fleetRef addresses one platform service on the board, optionally within one
// lifecycle env.
type fleetRef struct {
	// App is the service's CR name, from the path. It must be a DNS-1123 label.
	App string `json:"app"`
	// Env narrows the scan to one lifecycle env: main, test or dev. Omitted, the
	// namespaces are scanned in lifecycle order and the first match wins, so a
	// bare name resolves to PRODUCTION.
	Env string `json:"env"`
}

// getFleetApp returns one platform service, resolved to production by default.
//
// It returns a single platform service by its CR name, with the same
// declared-versus-running and drift facts the board carries. The name must be a
// DNS-1123 label; anything else is 400.
//
// Namespaces are scanned in lifecycle order — main, then test, then dev — and the
// first match wins, so a bare name resolves to PRODUCTION. The scan covers only the
// namespaces the caller is authorized for, so an org admin can never read a service
// outside their own org, and a name found in none of them is 404 rather than a leak.
func (b board) getFleetApp(ctx context.Context, in *fleetRef) (*AppView, error) {
	s := b.s
	c, err := admit(ctx, cloud.Admin)
	if err != nil {
		return nil, err
	}
	if err := fleetReady(s); err != nil {
		return nil, err
	}
	name := slugOf(in.App)
	if !appNameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("app must be a DNS-1123 label")
	}
	for _, ns := range targetNamespaces(s, ctx, c, in.Env) {
		obj, err := s.State.dyn.Resource(k8s.Apps).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fleetK8sErr(s, "get", err)
		}
		repository, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "repository")
		v := observeCR(obj, ns, envOf(ns), runningTagOf(s, ctx, ns, name, repository))
		return &v, nil
	}
	return nil, zip.ErrNotFound("app not found in the platform namespaces")
}

// observeFleet lists every App CR across the given namespaces (the caller's
// authorized set, per scopedNamespaces) and maps each to an AppView. A namespace
// that does not exist / is empty is skipped, never fatal (the board must still
// render the reachable namespaces).
func observeFleet(s *cloud.Service[fleetState], ctx context.Context, namespaces []string) ([]AppView, error) {
	var views []AppView
	for _, ns := range namespaces {
		// Running state: one Deployment list per namespace, indexed by name — the
		// running-tag source (inventory.ts). Best-effort: a Deployment RBAC/list
		// error leaves runningTag empty (an honest unknown) rather than failing the
		// whole board, so the declared/health/phase columns still render.
		running := runningTagsIn(s, ctx, ns)
		env := envOf(ns)
		list, err := s.State.dyn.Resource(k8s.Apps).Namespace(ns).List(ctx, metav1.ListOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return nil, fleetK8sErr(s, "list", err)
		}
		for i := range list.Items {
			cr := &list.Items[i]
			views = append(views, observeCR(cr, ns, env, running[cr.GetName()]))
		}
	}
	sort.Slice(views, func(i, j int) bool {
		if views[i].Org != views[j].Org {
			return views[i].Org < views[j].Org
		}
		if views[i].App != views[j].App {
			return views[i].App < views[j].App
		}
		return views[i].Env < views[j].Env
	})
	return views, nil
}

// ── deploy: rolling restart (zero-downtime) ──────────────────────────────────

// restartedAtAnnotation is the pod-template annotation a rolling restart stamps —
// the exact key/mechanism `kubectl rollout restart` uses, so the deploy path is a
// first-class rollout, not a bespoke trigger.
const restartedAtAnnotation = "hanzo.ai/restartedAt"

// deploy performs a zero-downtime ROLLING RESTART of the app's Deployment: it
// stamps the pod-template restartedAt annotation (the `kubectl rollout restart`
// mechanism), which the default RollingUpdate strategy turns into a graceful
// pod-by-pod recreate that re-pulls the declared image.
//
// SUPERADMIN-only (operatorGuard on the route): the board scans only the platform's
// OWN tier, so a restart here recreates a shared platform service — a platform-
// operator action, not a customer-org-admin one (RED H1). The env MUST be named
// explicitly (?env=main|test|dev) — a bare deploy never silently targets production
// (RED L1). Within that env it targets only the namespaces the caller is authorized
// for (targetNamespaces).
//
// It deliberately does NOT change the declared image TAG: that is the one property
// Hanzo CD's selfHeal reconciles from universe git, so a tag change is still a git
// commit (the one way to change WHAT runs). A restart re-runs WHAT IS DECLARED with
// no drift for CD to revert — the honest, GitOps-compatible "redeploy this app".
// restartRef addresses the platform service to restart, in a named env.
type restartRef struct {
	// App is the service's CR name, from the path. It must be a DNS-1123 label.
	App string `json:"app"`
	// Env is REQUIRED and must be main, test or dev. A bare call does not default
	// to production, which is what closes the fat-finger and confused-deputy
	// hazard.
	//
	// It carries no `validate:"required"`: the handler already refuses an empty env
	// with the sentence that names the three values, and a validator tag would
	// replace that sentence with a generic one. The requirement is stated here and
	// enforced there, once.
	Env string `json:"env"`
}

// restarted is what a rolling restart answers: which service was rolled, where.
type restarted struct {
	// OK is always true — a failure is an error, not a false here.
	OK bool `json:"ok"`
	// App is the service that was restarted.
	App string `json:"app"`
	// Namespace is the namespace its Deployment was patched in.
	Namespace string `json:"namespace"`
	// Env is that namespace's lifecycle env.
	Env string `json:"env"`
	// RestartedAt is the timestamp stamped onto the pod template, RFC3339 UTC.
	RestartedAt string `json:"restartedAt"`
}

// deployFleet rolls a platform service's pods, in a named environment.
//
// It triggers a rolling restart of one platform service's Deployment by stamping a
// fresh restart annotation, and answers 202 with the app, the namespace, the
// environment and the timestamp. It restarts pods; it does NOT change the image — a
// version change is the release path, not this.
//
// SuperAdmin ONLY, and deliberately narrower than the read gate beside it. The only
// namespaces this board touches are the platform's own tier, so a restart here
// recycles a SHARED service every tenant depends on. A brand-org admin is a
// customer-org admin, not a platform operator: observing the board is bounded and
// audited, and restarting production identity is not.
//
// `?env=main|test|dev` is REQUIRED — a bare call does not default to production,
// which is what closes the fat-finger and confused-deputy hazard — and any other
// value is 400. A service with no Deployment to restart in that environment is 404.
func (b board) deployFleet(ctx context.Context, in *restartRef) (*restarted, error) {
	s := b.s
	c, err := admit(ctx, cloud.Super)
	if err != nil {
		return nil, err
	}
	if err := fleetReady(s); err != nil {
		return nil, err
	}
	name := slugOf(in.App)
	if !appNameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("app must be a DNS-1123 label")
	}
	// L1: never SILENTLY target production. A restart must name its lifecycle env
	// explicitly (?env=main|test|dev) — a bare deploy no longer defaults to the
	// prod (main) namespace, closing the fat-finger / confused-deputy prod hazard.
	env := strings.TrimSpace(in.Env)
	if env == "" {
		return nil, zip.ErrBadRequest("specify ?env=main|test|dev — deploy does not default to production")
	}
	if nsForEnv(env) == "" {
		return nil, zip.ErrBadRequest("env must be one of main|test|dev")
	}
	ns, err := resolveTargetIn(s, ctx, name, targetNamespaces(s, ctx, c, env))
	if err != nil {
		return nil, err
	}
	restartedAt := time.Now().UTC().Format(time.RFC3339)
	patch := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{%q:%q}}}}}`, restartedAtAnnotation, restartedAt)
	if _, err := s.State.dyn.Resource(k8s.Deployments).Namespace(ns).Patch(
		ctx, name, k8stypes.MergePatchType, []byte(patch), metav1.PatchOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, zip.ErrNotFound("app " + name + " has no Deployment to restart in " + ns)
		}
		return nil, fleetK8sErr(s, "restart", err)
	}
	s.Log.Info("fleet rolling restart", "app", name, "namespace", ns, "restartedAt", restartedAt, "actor", principal.Owner(c))
	return &restarted{OK: true, App: name, Namespace: ns, Env: envOf(ns), RestartedAt: restartedAt}, nil
}

// resolveTarget finds the namespace an App CR lives in, scanning ALL platform
// namespaces in env order (main→test→dev) so a bare lookup targets production. The
// release path (release.go) uses this — it is a machine rollout with no per-caller
// identity to confine — so it keeps the full scan. Returns a clean 404 when the App
// exists in none of them.
func resolveTarget(s *cloud.Service[fleetState], ctx context.Context, name string) (string, error) {
	return resolveTargetIn(s, ctx, name, scanOrder())
}

// resolveTargetIn is resolveTarget's core over an EXPLICIT namespace list — the
// identity-scoped deploy/getApp pass the caller's authorized set so an OrgAdmin can
// never resolve (and thus restart/read) an app outside its own org. An empty list
// (an OrgAdmin owning no scanned namespace) resolves to a clean 404, never a leak.
func resolveTargetIn(s *cloud.Service[fleetState], ctx context.Context, name string, namespaces []string) (string, error) {
	for _, ns := range namespaces {
		if _, err := s.State.dyn.Resource(k8s.Apps).Namespace(ns).Get(ctx, name, metav1.GetOptions{}); err == nil {
			return ns, nil
		} else if !apierrors.IsNotFound(err) {
			return "", fleetK8sErr(s, "get", err)
		}
	}
	return "", zip.ErrNotFound("app " + name + " not found in the platform namespaces")
}

// ── k8s plumbing ────────────────────────────────────────────────────────────

func fleetReady(s *cloud.Service[fleetState]) error {
	if s.State.dyn == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "platform fleet: kubernetes client not configured: %s", s.State.initErr)
	}
	return nil
}

// k8sErr maps a raw API error to an honest gateway-level error. RBAC denials name
// the missing access so the operator knows exactly what to grant the cloud service
// account (get/list on apps.hanzo.ai). Mirrors ml.k8sErr.
func fleetK8sErr(s *cloud.Service[fleetState], op string, err error) error {
	s.Log.Error("k8s op failed", "op", op, "resource", k8s.Apps.Resource, "err", err)
	if apierrors.IsForbidden(err) {
		return zip.Errorf(http.StatusBadGateway,
			"%s apps: kubernetes RBAC denied (cloud service account needs %s on apps.hanzo.ai): %v",
			op, op, err)
	}
	return zip.Errorf(http.StatusBadGateway, "%s apps failed: %v", op, err)
}

// newDynamic builds the dynamic client from the in-cluster service account,
// falling back to KUBECONFIG / ~/.kube/config for local/dev — identical to
// clients/ml.newDynamic.
func newFleetDynamic() (dynamic.Interface, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(
			clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{})
		cfg, err = cc.ClientConfig()
		if err != nil {
			return nil, fmt.Errorf("no in-cluster config and no kubeconfig: %w", err)
		}
	}
	cfg.UserAgent = fleetUserAgent
	return dynamic.NewForConfig(cfg)
}

// ── pure mapping helpers (unit-tested without a cluster) ─────────────────────

// scanOrder returns the platform namespaces in a stable env order (main first),
// so a bare app-name read/deploy resolves to production before test/dev.
// Every entry MUST classify under nsClass (asserted in fleet_test.go) — nsClass is
// the one place that decides what a namespace means, and a namespace listed here
// but unclassified would render rows with an empty tenant, i.e. rows no OrgAdmin
// could ever be confined to. hanzo-mainnet was missing until 2026-07-25, so its
// CRs were invisible on the board.
//
// First-party only: a `tenant-<org>` namespace classifies correctly (nsClass) but
// is not yet DISCOVERED, because the scan set is computed without a cluster client.
// Threading the dynamic client through so the set is listed from the cluster is the
// follow-up that makes tenant workloads appear; the authorization axis they need
// already exists.
func scanOrder() []string {
	return []string{"hanzo", "hanzo-mainnet", "hanzo-testnet", "hanzo-devnet"}
}

// orgFromRepository derives the image namespace ("org") from an image repo:
// `ghcr.io/hanzoai/chat` → `hanzoai`; `docker.io/grafana/grafana` → `grafana`.
// Falls back to the whole repo when it has no namespace segment. Ported verbatim
// from inventory.ts `orgFromRepository`.
func orgFromRepository(repository string) string {
	parts := nonEmpty(strings.Split(repository, "/"))
	if len(parts) >= 3 {
		return parts[1]
	}
	if len(parts) == 2 {
		return parts[0]
	}
	return repository
}

// repoFromRepository derives the owner/repo GitHub coordinate from an image repo:
// `ghcr.io/hanzoai/chat` → `hanzoai/chat` (the image path minus the registry
// host). Ported verbatim from inventory.ts `repoFromRepository`.
func repoFromRepository(repository string) string {
	parts := nonEmpty(strings.Split(repository, "/"))
	if len(parts) >= 3 {
		return strings.Join(parts[1:], "/")
	}
	return strings.Join(parts, "/")
}

func nonEmpty(in []string) []string {
	out := in[:0]
	for _, s := range in {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// healthFromStatus rolls the operator's reconciled Service status up to the
// apps-table health vocabulary. The operator populates status.readyReplicas /
// status.replicas (and phase); we prefer that reconciled truth over re-deriving
// from the Deployment (the operator already did that join). Mirrors
// inventory.ts healthFromDeployment semantics: desired 0 ⇒ yellow (intentionally
// scaled to zero, not unhealthy), ready>=desired ⇒ green, some ready ⇒ yellow,
// none ⇒ red. Empty when the status carries no replica counts yet.
func fleetHealthFromStatus(status map[string]any) string {
	desired, hasDesired := fleetNestedInt(status, "replicas")
	fleetReady, _ := fleetNestedInt(status, "readyReplicas")
	if !hasDesired {
		// Fall back to availableReplicas if the operator only reports that.
		if avail, ok := fleetNestedInt(status, "availableReplicas"); ok {
			if avail > 0 {
				return "green"
			}
			return "red"
		}
		return "" // no replica signal yet — unknown, never a fabricated green
	}
	if desired == 0 {
		return "yellow"
	}
	if fleetReady >= desired {
		return "green"
	}
	if fleetReady > 0 {
		return "yellow"
	}
	return "red"
}

// observeCR maps one App CR (+ its operator-reconciled status + the running tag
// observed from the live Deployment) into an AppView, attaching the drift verdict.
// This is inventory.ts observeService fused with apps-api.ts toAppView:
// declared tag from the CR spec, running tag from the Deployment (passed in),
// health + phase + endpoints from the operator-reconciled CR status.
func observeCR(obj *unstructured.Unstructured, namespace, env, runningTag string) AppView {
	name := obj.GetName()
	repository, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "repository")
	declaredTag, _, _ := unstructured.NestedString(obj.Object, "spec", "image", "tag")
	role, _, _ := unstructured.NestedString(obj.Object, "spec", "role")

	status, _, _ := unstructured.NestedMap(obj.Object, "status")
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	endpoints := nestedStringSlice(status, "endpoints")

	obs := Observed{DeclaredTag: declaredTag, RunningTag: runningTag}
	return AppView{
		ID:          orgFromRepository(repository) + "/" + name + "/" + env,
		Org:         orgFromRepository(repository),
		App:         name,
		Env:         env,
		Repo:        repoFromRepository(repository),
		Registry:    repository,
		Role:        role,
		DeclaredTag: declaredTag,
		RunningTag:  runningTag,
		LatestTag:   "", // GH-release reader is a follow-up phase (release-reader.ts)
		Health:      fleetHealthFromStatus(status),
		Phase:       phase,
		Cluster:     "hanzo-k8s",
		Namespace:   namespace,
		Endpoints:   endpoints,
		Drift:       ComputeDrift(obs),
	}
}

// runningTagsIn lists the Deployments in a namespace and returns a map of
// Deployment-name → running image tag (the container whose image repo the caller
// later matches against the CR's declared repo, in runningTagOf; here we index by
// name and keep the first container's tag as the default). Best-effort: any list
// error yields an empty map so the board still renders declared/health/phase.
func runningTagsIn(s *cloud.Service[fleetState], ctx context.Context, namespace string) map[string]string {
	out := map[string]string{}
	list, err := s.State.dyn.Resource(k8s.Deployments).Namespace(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		s.Log.Warn("list deployments for running tag failed; running tag will be empty",
			"namespace", namespace, "err", err)
		return out
	}
	for i := range list.Items {
		d := &list.Items[i]
		out[d.GetName()] = firstContainerTag(d)
	}
	return out
}

// runningTagOf reads a single Deployment's running tag, matching the container
// whose image repo equals the CR's declared repo (so a sidecar like replicate/otel
// is never mistaken for the app), falling back to the first container. Mirrors
// inventory.ts runningTagFromDeployment. Best-effort: any error → "".
func runningTagOf(s *cloud.Service[fleetState], ctx context.Context, namespace, name, declaredRepository string) string {
	d, err := s.State.dyn.Resource(k8s.Deployments).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return ""
	}
	return runningTagFromDeployment(d, declaredRepository)
}

// nestedInt reads an integer-valued key from an unstructured map, tolerating the
// int64/float64 the k8s decoder may produce.
func fleetNestedInt(m map[string]any, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	switch v := m[key].(type) {
	case int64:
		return int(v), true
	case int:
		return v, true
	case float64:
		return int(v), true
	default:
		return 0, false
	}
}

// nestedStringSlice reads a []string key from an unstructured map (the k8s decoder
// yields []any of string).
func nestedStringSlice(m map[string]any, key string) []string {
	if m == nil {
		return nil
	}
	raw, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, e := range raw {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// deploymentContainers extracts the pod-template container images from an
// unstructured Deployment (spec.template.spec.containers[].image).
func deploymentContainers(dep *unstructured.Unstructured) []string {
	if dep == nil {
		return nil
	}
	raw, ok, _ := unstructured.NestedSlice(dep.Object, "spec", "template", "spec", "containers")
	if !ok {
		return nil
	}
	imgs := make([]string, 0, len(raw))
	for _, c := range raw {
		cm, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if img, ok := cm["image"].(string); ok && img != "" {
			imgs = append(imgs, img)
		}
	}
	return imgs
}

// runningTagFromDeployment picks the running tag from a Deployment by matching the
// container whose image repository equals the CR's declared repository (so a
// sidecar can never be mistaken for the app), falling back to the first container.
// Mirrors inventory.ts runningTagFromDeployment.
func runningTagFromDeployment(dep *unstructured.Unstructured, declaredRepository string) string {
	imgs := deploymentContainers(dep)
	if len(imgs) == 0 {
		return ""
	}
	for _, img := range imgs {
		if repoFromImageRef(img) == declaredRepository {
			return tagFromImageRef(img)
		}
	}
	return tagFromImageRef(imgs[0])
}

// firstContainerTag is the default running tag for the namespace-indexed map: the
// first container's tag. The per-service exact match (runningTagFromDeployment)
// is used when the declared repo is known; this keeps the list pass O(deployments)
// without a Get per service.
func firstContainerTag(dep *unstructured.Unstructured) string {
	imgs := deploymentContainers(dep)
	if len(imgs) == 0 {
		return ""
	}
	return tagFromImageRef(imgs[0])
}

// repoFromImageRef splits `ghcr.io/hanzoai/iam:v1` → `ghcr.io/hanzoai/iam`.
// A digest ref (`repo@sha256:…`) keeps the repo; a bare repo returns itself.
func repoFromImageRef(ref string) string {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		ref = ref[:at]
	}
	// A ':' after the last '/' is the tag separator (a ':' in a registry host:port
	// segment lives before a '/', so guard on the last slash).
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash {
		return ref[:colon]
	}
	return ref
}

// tagFromImageRef splits `ghcr.io/hanzoai/iam:v1` → `v1`. A digest ref returns the
// digest; a bare repo (no tag) returns "".
func tagFromImageRef(ref string) string {
	if at := strings.LastIndex(ref, "@"); at >= 0 {
		return ref[at+1:]
	}
	slash := strings.LastIndex(ref, "/")
	colon := strings.LastIndex(ref, ":")
	if colon > slash && colon < len(ref)-1 {
		return ref[colon+1:]
	}
	return ""
}
