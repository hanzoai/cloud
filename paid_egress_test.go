package cloud

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestFreeSurfaceBuysNothing is the half of the price gate that was only ever
// human review.
//
// TestPriceDeclared asserts somebody ANSWERED what a surface costs.
// TestMeteredSurfacesRequireStanding asserts a Metered answer is wired through to
// the standing gate. Neither asks whether a FREE answer is TRUE, and that is the
// shape of the one that got through: websearch declared cloud.Free while calling
// Brave's paid search API and Mojeek's prepaid one, so the edge charged nothing,
// Billable said no standing was needed, and no meter downstream debited anybody.
// The vendor's invoice was the only place the calls were recorded. The
// declaration was reviewed — it says Free in a diff somebody approved — and
// review is exactly what does not scale to 128 composition roots.
//
// A CREDENTIAL IS THE EVIDENCE, because it is the one thing a paid call cannot do
// without. You cannot reach Brave without its subscription token or DataForSEO
// without its login, so a package that names one either spends money or carries a
// credential for no reason. It is a source fact, greppable and stable, and it does
// not depend on recognising an HTTP call.
//
// IT FOLLOWS IMPORTS, through the same first-party walk coresidence_test.go
// makes, because a plugin links a package GRAPH and the binary can spend whatever
// any package in that graph can spend. Checking only the app named beside Mount
// would miss a free surface that reaches a paid one through a composition client —
// which is how websearch's own engines are reached from the answer engine.
//
// WHAT IT DOES NOT SEE, stated because a green run is otherwise read as more
// assurance than it is: this walks FREE roots only. A root declared Metered is
// exempt from here entirely, and Metered is not a blanket absolution — it says one
// meter owns one charge, not that every path in that binary's closure is covered.
// apps/websearch was Metered for its search fee while render.go reached the crawl
// pod by a second path that no fee applied to. Walking a metered root's closure for
// vendor reaches its own meters do not cover is the next thing this check should
// learn; until it does, a Metered declaration moves a surface out of this net.
//
// It is a TEST and not a runtime check for the reason TestPriceDeclared is: a
// surface priced Free over a paid vendor is a composition root somebody did not
// finish, and the place to catch that is before it ships rather than on a
// customer's request at 3am.
func TestFreeSurfaceBuysNothing(t *testing.T) {
	roots, err := filepath.Glob(filepath.Join("plugin", "*", "main.go"))
	if err != nil {
		t.Fatalf("glob composition roots: %v", err)
	}
	if len(roots) == 0 {
		t.Fatal("no plugin/*/main.go found — this test is walking the wrong tree, " +
			"which would make it pass vacuously forever")
	}

	named := map[string]map[string]string{} // package dir -> what it names, memoised
	seen := map[string]bool{}               // markers found anywhere, for the vacuity check

	var free, findings int
	for _, root := range roots {
		name := filepath.Base(filepath.Dir(root))
		price, err := declaredPrice(root)
		if err != nil {
			t.Fatalf("%s: %v", root, err)
		}
		if price == "Free" {
			free++
		}

		dir := filepath.Dir(root)
		pkgs := []string{dir}
		for imp := range links(t, dir) {
			pkgs = append(pkgs, strings.TrimPrefix(imp, modulePath))
		}
		sort.Strings(pkgs)

		for _, pkg := range pkgs {
			creds, ok := named[pkg]
			if !ok {
				creds = costlyIn(pkg)
				named[pkg] = creds
			}
			for cred, why := range creds {
				// Recorded from EVERY root, not only the free ones. A credential
				// reachable solely from a correctly-Metered surface is still a live
				// entry, and scoping this to the free walk made the four that matter
				// most — the two search keys and DataForSEO — look like dead names the
				// moment their surfaces were priced properly.
				seen[cred] = true
				if price != "Free" {
					continue
				}
				if _, ok := freeOfVendor[name+":"+cred]; ok {
					continue
				}
				findings++
				t.Errorf("plugin/%s declares Price: cloud.Free, but %s names %s (%s).\n"+
					"A free surface that buys from a vendor is money leaving with nothing "+
					"authorized and nothing recorded — the edge charges nothing, Billable "+
					"requires no standing, and no meter downstream debits anybody.\n"+
					"Either declare it cloud.Metered and debit the paid path through a "+
					"ResourceMeter (apps/websearch/meter.go is the worked example), or, if "+
					"the credential reaches nothing billable from here, say so in "+
					"freeOfVendor with the reason.",
					name, pkg, cred, why)
			}
		}
	}

	if free == 0 {
		t.Fatal("matched no Price: cloud.Free composition root — the pattern this test " +
			"recognises has moved, so it is asserting nothing")
	}
	// The registry IS the assertion, so pin that every entry can still fire. A
	// credential no root reaches has been renamed or deleted out from under this
	// list, and it would go on "passing" forever.
	var unseen []string
	for cred := range costly {
		if !seen[cred] {
			unseen = append(unseen, cred)
		}
	}
	sort.Strings(unseen)
	if len(unseen) > 0 {
		t.Errorf("paidVendor names %v, which no composition root reaches at all.\n"+
			"A credential nothing links to cannot fail this test, so the entry asserts "+
			"nothing: delete it, or fix the spelling it has drifted from.", unseen)
	}
	t.Logf("walked %d free surfaces over %d packages; %d markers in the registry "+
		"(%d credentials, %d addresses), %d findings",
		free, len(named), len(costly), len(paidVendor), len(capacity), findings)
}

// paidVendor names the credentials that unlock something COSTLY, and what it
// costs. Each entry is a claim about the world, made once by a human, that the
// walk above then enforces on every composition root forever — the same shape as
// meteredApps and unpricedRoot.
//
// THE ADMISSION RULE IS CREDENTIAL SCOPE, and it is the sharpest thing in this
// file. Ask who the credential belongs to:
//
//	orgs/<org>/…            the TENANT's own credential. They hold the account,
//	                        the vendor bills them, and we spend nothing. Free is
//	                        correct — apps/notify sends SMS this way, and it is why
//	                        notify is not metered while apps/tel is.
//	a bare ref from env     the DEPLOYMENT's credential, resolved once at Mount.
//	at Mount                Hanzo holds the account and Hanzo is invoiced, per call,
//	                        for whoever happens to call. That is a paid surface
//	                        however local the address looks.
//
// It is a better rule than "is it a third party" and better than "is it in the
// cluster", because it explains every entry below without exception: the search
// keys, DataForSEO, the identity-verification key, the bank aggregators. It is
// also the rule that would have caught websearch on day one — WEBSEARCH_BRAVE_KEY
// is read from process env, never from orgs/<org>/.
//
// COSTLY, NOT MERELY THIRD-PARTY. The first cut of this list held only outside
// vendors, and that line does not survive contact: a headless browser we run, an
// SFU we run and a sandbox pod we run are all real money per request, and a
// customer charged nothing for them is the same hole as a customer charged
// nothing for Brave. Where the invoice comes from decides who we pay, never
// whether the act was free. So the test is: does serving this consume capacity
// somebody had to buy?
//
// Our own IDENTITY tokens do not qualify — IAM, KMS, commerce, S3 and the
// registry authenticate us to ourselves and unlock no capacity — and listing them
// would turn this into noise nobody reads, which is how every list like it stops
// working.
var paidVendor = map[string]string{
	// Outside vendors, on their own price lists.
	"WEBSEARCH_BRAVE_KEY":  "Brave Search API — per-query subscription",
	"WEBSEARCH_MOJEEK_KEY": "Mojeek Search API — prepaid balance, which answers \"insufficient balance\" when spent",
	"DATAFORSEO_LOGIN":     "DataForSEO — billed per call against the vendor's own price list",
	"DATAFORSEO_PASSWORD":  "DataForSEO — billed per call against the vendor's own price list",
	"CF_API_TOKEN":         "Cloudflare — Workers, DNS and edge resources on a paid account",
	"DO_API_TOKEN":         "DigitalOcean — droplets, clusters and volumes billed by the hour",
	"NAMECOM_TOKEN":        "Name.com — domain registrations and renewals, billed per name",
	"CLOUD_IDV_KEY_REF":    "Persona/Onfido/Stripe identity checks — a few dollars per inquiry, on the deployment's key",
	"CLOUD_PLAID_SECRET":   "Plaid — per bank connection and per data pull, on the deployment's key",
	"TEL_CARRIER_KEY":      "telephony carrier — numbers, and per-message and per-minute charges",

	// Capacity we run ourselves, bought by the node-hour rather than by invoice.
	"CRAWL_API_TOKEN":      "the render pod — a headless browser held for up to 45s per page",
	"LIVEKIT_API_KEY":      "the media server — a live audio/video pipe per admitted seat",
	"CODE_EXEC_API_KEY":    "the interpreter — a sandbox pod per program run",
	"PIECES_RUNNER_SECRET": "the auto engine's runner — a JS connector piece executed on its pods",
	"ZROK_ADMIN_TOKEN":     "the share fabric — a public ingress tunnel held open per share",

	// NOT S3_ADMIN_*, and it is the useful counter-example. Object storage costs
	// real money, but that credential builds the SHARED client every subsystem
	// reaches the store through (apps/s3admin, behind deps.VFS), so nearly every
	// plugin in the fleet links it and the check would fire on all of them. A
	// credential that marks "this binary can talk to the object store" says nothing
	// about whether serving a request spends anything. What costs money is the
	// object DATA PLANE, and apps/s3 — which owns it — is already Metered and
	// debits per operation. Mark the act, never the library.
}

// capacity is the SECOND axis of evidence, and it exists because the first one
// does not survive the cluster boundary.
//
// paidVendor's premise is that a credential marks a paid act, and outside the
// cluster that holds — you cannot reach Brave without its token. INSIDE it, the
// ADDRESS is the authorization: a pod reached at a service name spends GPU
// minutes, sandbox pods, MPC rounds or on-chain gas with no key anywhere in the
// caller's source. Red walked straight through the gap: apps/wallets deploys a
// Safe contract with real gas over `mpc-api-svc`, and apps/knowledge runs pieces
// on the auto engine's pods over `auto.hanzo.svc` — the very capacity plugin/auto
// prices — and neither named a vendor credential, so a check that only looked for
// one reported both as clean.
//
// So the rule is the same rule, asked of a wider kind of evidence: what marks the
// act? Outside, a credential. Inside, the address of the thing that costs money,
// or the mount path of the capacity credential a pod is handed. Both are string
// literals in the caller's own source, which is what makes either checkable.
//
// A LITERAL PREFIX, matched inside any string, so a URL built by concatenation or
// carried on an env default is caught the same way as a bare constant.
var capacity = map[string]string{
	"auto.hanzo.svc":  "the auto engine's sandbox pods — a JS piece run per call",
	"crawl.hanzo.svc": "the render pod — a headless browser per page",
	// The ring has no address literal to match: its nodes arrive entirely through
	// this env, so the ENV NAME is the marker. Same evidence, one step earlier.
	"CLOUD_WALLETS_MPC_ADDR": "the MPC ring — a CGGMP21 round, and a Safe deploy is on-chain gas",
	"studio:8188":            "the GPU render service — an image or video generated per call",
	"kubernetes.default.svc": "the cluster API — workloads scheduled onto nodes we pay for",
	"/etc/livekit-keys":      "the media server's mounted credential — a live pipe per seat",
}

// costly is the two registries as one lookup, because the WALK asks one question:
// does this package name something that costs money? They stay separate above
// because they are different kinds of claim, and a reader adding an entry needs to
// know which kind they are making.
var costly = func() map[string]string {
	m := make(map[string]string, len(paidVendor)+len(capacity))
	for k, v := range paidVendor {
		m[k] = v
	}
	for k, v := range capacity {
		m[k] = v
	}
	return m
}()

// freeOfVendor names the "<plugin>:<CREDENTIAL>" pairs where a free surface links
// a paid credential and still buys nothing — it is reachable only through a
// package the surface never calls into. Each entry is a claim checked by a human
// once, and it names the CREDENTIAL rather than exempting the plugin wholesale, so
// a root that later grows a second vendor still fails.
//
// Keep it small. An entry here is a promise about a call graph, which is the
// weakest thing in this file: the walk can see that a package is LINKED, not
// whether a function in it ever runs. Declaring a surface Metered costs nothing
// when no money moves — the edge charges Metered zero, and a meter with no debit
// in it never fires — so pricing the surface honestly is usually cheaper than a
// promise somebody has to keep re-checking. Every entry below is here because the
// promise is the TRUE statement and Metered would be the false one.
var freeOfVendor = map[string]string{
	// A CHOKE POINT, CHECKED. Every Cloudflare call in this binary would have to
	// come through cloudflare.With/New, whose only non-test caller is
	// apps/projects/edgecred.go's newEdge, whose only caller is the edge field
	// initialised inside projects.Use. None of these four mounts projects, so
	// that field is nil in their processes. What they reach projects FOR is one
	// symbol each and neither touches it: catalog reads projects.Ready/LiveSites
	// (a SQL query on the store), and billing, link and team reach it only through
	// agents' SetDeployObserver, which stores an interface in a package global and
	// is called from agents.Use — which none of them run either.
	"billing:CF_API_TOKEN": "links apps/projects for SetDeployObserver only; mounts no route that reaches newEdge",
	"catalog:CF_API_TOKEN": "links apps/projects for Ready/LiveSites, which read the store; the edge client is nil here",
	"link:CF_API_TOKEN":    "links apps/projects for SetDeployObserver only; mounts no route that reaches newEdge",
	"team:CF_API_TOKEN":    "links apps/projects for SetDeployObserver only; mounts no route that reaches newEdge",

	// THE SAME TWO LINKS, A SECOND VENDOR — and the claim is narrower than the
	// one above rather than a repeat of it. The renderer arrived with
	// apps/projects/shot.go, whose every function is UNEXPORTED: capture is
	// reachable only from shotOf, and shotOf is registered exactly once, at
	// apps/projects/projects.go's `GET /v1/projects/:slug/shot`, by projects'
	// own Mount. Neither of these two calls projects.Use, so there is no way
	// into the renderer from either — not a nil client this time, but no
	// callable symbol at all.
	"billing:crawl.hanzo.svc": "reaches apps/projects for SetDeployObserver; the renderer is unexported behind a route projects alone mounts",
	"billing:CRAWL_API_TOKEN": "reaches apps/projects for SetDeployObserver; the renderer is unexported behind a route projects alone mounts",
	"catalog:crawl.hanzo.svc": "reaches apps/projects for Ready/LiveSites; the renderer is unexported behind a route projects alone mounts",
	"catalog:CRAWL_API_TOKEN": "reaches apps/projects for Ready/LiveSites; the renderer is unexported behind a route projects alone mounts",
	"link:crawl.hanzo.svc":    "reaches apps/projects for SetDeployObserver; the renderer is unexported behind a route projects alone mounts",
	"link:CRAWL_API_TOKEN":    "reaches apps/projects for SetDeployObserver; the renderer is unexported behind a route projects alone mounts",
	"team:crawl.hanzo.svc":    "reaches apps/projects for SetDeployObserver; the renderer is unexported behind a route projects alone mounts",
	"team:CRAWL_API_TOKEN":    "reaches apps/projects for SetDeployObserver; the renderer is unexported behind a route projects alone mounts",

	// The import is a TYPE, not a client: apps/admin/core.State declares a
	// *digitalocean.Client field, and apps/plugin uses core only for Admit,
	// CallerCreds and the OK/Err helpers. It never builds a State and never reads
	// .DO, so digitalocean.New is on no path it mounts.
	"plugins:DO_API_TOKEN": "imports apps/admin/core for its helpers; the DO client arrives as a struct field type and is never constructed",

	// LINKED, NEVER MOUNTED — the app singleton settles it. apps/content and
	// apps/automations both refuse every op when their package `mounted` is nil,
	// and it is set only by their own Mount, which only their own plugin calls.
	// cmd/cloud spawns each app as its OWN PROCESS, so there is no fused binary in
	// which these could be co-resident. guide and integrations reach content only
	// through automations' dispatcher, which is not mounted either.
	//
	// catalogsync was the third entry and it is the reason this note is worth
	// reading twice: it called content.EnsureCatalogAsset FOR REAL, meaning the
	// render was its whole purpose and the nil check refused it every time. An
	// entry here says a reach is harmless; for that one it also said the app could
	// not do its job, and nobody read it that way for as long as it sat in a list
	// of things that are fine.
	"guide:studio:8188":        "reaches content only via automations.InvokeTool; automations.mounted is nil here",
	"integrations:studio:8188": "reaches content only via automations.Deliver; automations.mounted is nil here",

	// The knowledge symbols these two use are the vector READ leg (Semantic →
	// index().searchDoc). The piece runner is reachable only from syncConnector,
	// which knowledge.routes registers and only knowledge.Use calls — and
	// pieceSync takes a *cloud.Service[state] that only Mount constructs.
	"search:auto.hanzo.svc":       "uses knowledge.Semantic, the vector read; syncConnector is not mounted here",
	"search:PIECES_RUNNER_SECRET": "uses knowledge.Semantic, the vector read; the piece runner is not reachable",
	"team:auto.hanzo.svc":         "reaches knowledge through search.ForOrg's read leg only",
	"team:PIECES_RUNNER_SECRET":   "reaches knowledge through search.ForOrg's read leg only",

	// Both reach apps/wallets only for the payee LOOKUP (ResolvePaymentTarget /
	// Mounted) — x402 directly, marketplace through x402. A lookup resolves an
	// address; it starts no custody and no round. And wallets.mounted is nil
	// outside the wallets binary, so even the lookup falls through to the plane
	// RPC, which is a read on another process's store.
	"x402:CLOUD_WALLETS_MPC_ADDR":        "reaches wallets only for the payee lookup; a lookup starts no round, and wallets.mounted is nil here",
	"marketplace:CLOUD_WALLETS_MPC_ADDR": "reaches wallets only via x402's payee lookup; same nil singleton, same read",

	// REACHABLE AND SPENDING, AND STILL CORRECTLY FREE — platform sudo. treasury
	// really does anchor on-chain (SendTransaction) and really does mint a ring key
	// through wallet.TreasuryAnchorSigner, but every one of its six admin ops opens
	// with `admin(ctx)`, which is c.IsAdmin(). Its two tenant routes are reads. So
	// the spend is Hanzo's own and there is no tenant on the request to attribute it
	// to — Metered would assert a debit that must never exist.
	//
	// Note the literal this walk matched here is inside an ERROR MESSAGE naming the
	// env, not a use of it. That is why an entry states the REACHABILITY rather than
	// the string: the string is only what made someone look.
	"treasury:CLOUD_WALLETS_MPC_ADDR": "platform sudo only (admin(ctx) → IsAdmin on all six spending ops); tenant routes are reads",

	// SUPERADMIN ONLY, on the marker's own terms. treasury really does anchor
	// on-chain and deploy really does write to the cluster, but every spending op in
	// each opens with an admin check — treasury's six with `admin(ctx)` (IsAdmin),
	// deploy's writes with its own `guard` (IsSuperAdmin). The spend is Hanzo's own
	// and there is no tenant on the request to attribute it to.
	"treasury:SendTransaction()": "platform sudo only (admin(ctx) on all six spending ops); tenant routes are reads",
	"deploy:InClusterConfig()":   "platform sudo only (guard → IsSuperAdmin on every cluster write)",

	// INFRASTRUCTURE, NOT A TENANT SURFACE. apps/cron mounts zero routes — it is a
	// facet of tasks that runs a background scheduler — and the Jobs it creates come
	// from operator-authored ConfigMaps labelled cron.hanzo.ai/enabled in one
	// platform namespace, re-read at fire time. There is no path for a caller to
	// hand in a manifest, and the Job count tracks the cron cadence rather than
	// request volume. Nothing here scales with a tenant.
	"tasks:InClusterConfig()": "apps/cron mounts no routes; its Jobs come from operator ConfigMaps in one platform namespace",

	// ARMED, NOT FIRING. apps/books holds a complete Plaid client, and the
	// credential is the deployment's — so if a tenant could reach it, this would be
	// the compliance leak again. Nothing can: the two routes that would seal a link
	// (bank_api.go's token and exchange handlers) return 501 unconditionally after
	// the org check, so no item is ever stored, and the sync short-circuits on an
	// empty item list BEFORE it resolves credentials. No link, no call, no invoice.
	//
	// It is an exemption rather than a meter precisely because it is unreachable:
	// pricing a path nobody can take would put a fee in the diff and no behaviour
	// behind it. The day those handlers do something, this entry must fail — which
	// is the point of naming the pair rather than the plugin.
	"books:CLOUD_PLAID_SECRET": "the link handlers 501 unconditionally, so no item is stored and the sync short-circuits before it reads credentials",

	// REACHABLE, AND STILL CORRECTLY FREE — platform sudo, so there is no tenant
	// to bill. deploy's every cluster WRITE (dashSync, engineReconcile) is wrapped
	// in its own `guard`, which is principal.IsSuperAdmin and the package's one
	// fail-closed refusal; the constant this walk matched is a read projection's
	// server field, not a client target. Everything a tenant reaches here is a
	// scoped read.
	"deploy:kubernetes.default.svc": "platform sudo only (guard → IsSuperAdmin on every write); the matched constant is a read projection, not a client",

	// admin genuinely drives DigitalOcean, including spend-increasing
	// mutations (snapshot, resize, scale). But every one of those handlers opens
	// with core.Admit, which is principal.IsSuperAdmin — membership of the
	// reserved `admin` org — so the only caller is Hanzo's own platform operator
	// and the spend is Hanzo's own infrastructure. There is no tenant on the
	// request to attribute it to, and a surface with no customer cannot have a
	// customer price. Metered would assert a debit that must never exist.
	"admin:DO_API_TOKEN": "platform sudo only (core.Admit → IsSuperAdmin); drives Hanzo's own infra, so there is no tenant to bill",
}

// ── the shapes ──────────────────────────────────────────────────────────────
//
// A registry of literals only ever catches what somebody already thought of, and
// 19 names walking 89 surfaces is a thin net. Two SHAPES catch the rest, because
// inside the cluster the thing that costs money always looks like one of them.

// WHY THERE IS NO "ANY IN-CLUSTER ADDRESS" RULE, measured rather than argued.
//
// The obvious generalization is to flag every `*.svc` / `host:port` literal a Free
// root reaches, on the theory that a pod answering is a node-hour. It was tried
// here: 45 findings against 89 surfaces, and about four of them were real. The
// rest were the money plane itself (commerce.hanzo.svc), telemetry ingest
// (o11y.hanzo.svc), and the shared datastores (vector, search, engine, dns) — all
// always-on services whose cost does not scale with the request, and one of which
// is literally the thing you call TO pay. A check that flags the ledger as an
// unbilled expense is a check nobody will read, and forty exemptions is not a net,
// it is a sieve with a list of holes.
//
// The distinction that matters is not WHERE the pod is, it is whether answering
// starts work that scales with the request: a GPU render, a sandbox pod, a browser,
// an MPC round. That is not derivable from an address, so it stays a curated list
// (`capacity`, above) and the shapes below carry what generalizes cleanly.

// spendCall names the CALLS that cost money by themselves, whatever they are
// pointed at. They are the other half of the same gap: an on-chain transaction
// and a cluster-API write both spend without naming a vendor or an address that
// this walk could otherwise see, because the address arrives from a mounted
// service account or a chain client built elsewhere.
// It names the CREDENTIAL-BEARING call, never the constructor. NewForConfig was in
// this list for one run and had to come out: it builds a Kubernetes client from
// whatever REST config it is handed, and the two Free surfaces it caught — the
// fleet registry and the admin infra scan — both hand it a kubeconfig the TENANT
// registered, to read the TENANT's own cluster. That is their bill, not ours.
// InClusterConfig is the precise one, because it can only ever mean the service
// account WE mounted, on the cluster WE pay for.
var spendCall = map[string]string{
	"SendTransaction": "an on-chain transaction — gas, paid from the platform key",
	"InClusterConfig": "the cluster API from a mounted service account — workloads on nodes we pay for",
}

// costlyIn reports what a package names that costs money: a registry marker, a
// cluster address, or a call that spends by itself.
//
// String literals only for the first two, so a comment that merely mentions a key
// — brave.go explains its own — is not an invocation. A literal is where a
// credential and an address are always named, whether read from the environment
// or fetched by KMS ref.
func costlyIn(dir string) map[string]string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		// An import can name a package this walk does not own (a generated tree, a
		// build-tagged variant). Silence, never a finding invented from an absence.
		return nil
	}
	found := map[string]string{}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok {
					if why, hit := spendCall[sel.Sel.Name]; hit {
						found[sel.Sel.Name+"()"] = why
					}
				}
			}
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			v, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			for marker, why := range costly {
				if strings.Contains(v, marker) {
					found[marker] = why
				}
			}
			return true
		})
	}
	return found
}

// appDirs is the apps/* packages a composition root MOUNTS, read from the root's
// own imports.
//
// It exists because plugin/<name> and apps/<name> are not the same key and a table
// saying they are is wrong for twelve of them today: plugin/sandboxes mounts
// apps/sandbox, plugin/audit mounts apps/auditlog, plugin/ai mounts BOTH ai and
// zen. Every check that walks from a root to its package derives the edge rather
// than assuming it, so a rename moves both halves at once.
func appDirs(t *testing.T, root string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), root, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", root, err)
	}
	var out []string
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		if rel, ok := strings.CutPrefix(path, modulePath+"apps/"); ok {
			out = append(out, filepath.Join("apps", rel))
		}
	}
	sort.Strings(out)
	return out
}

// declaredPrice reads what a composition root says its surface costs, as the
// ident's NAME ("Free", "Metered", …) rather than a resolved value — the root is
// read as SOURCE, the same walk TestPriceDeclared makes, asking the next question.
func declaredPrice(root string) (string, error) {
	src, err := os.ReadFile(root)
	if err != nil {
		return "", err
	}
	file, err := parser.ParseFile(token.NewFileSet(), root, src, 0)
	if err != nil {
		return "", err
	}
	var price string
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		arr, ok := lit.Type.(*ast.ArrayType)
		if !ok || !isCloudPlugin(arr.Elt) {
			return true
		}
		for _, elt := range lit.Elts {
			plugin, ok := elt.(*ast.CompositeLit)
			if !ok {
				continue
			}
			for _, field := range plugin.Elts {
				kv, ok := field.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := kv.Key.(*ast.Ident); !ok || key.Name != "Price" {
					continue
				}
				if sel, ok := kv.Value.(*ast.SelectorExpr); ok {
					price = sel.Sel.Name
				}
			}
		}
		return true
	})
	return price, nil
}
