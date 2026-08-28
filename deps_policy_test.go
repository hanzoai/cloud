package cloud

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/brand"
)

// Deps is the DEPENDENCY surface: the things a subsystem cannot obtain for
// itself because obtaining them is the bootstrap. A client it must be handed
// (you cannot fetch your KMS client from a store you need that client to open),
// the process's own identity, the roots that must exist before any store opens.
//
// It is not the deployment's configuration. The difference is not stylistic: a
// value on this struct is reachable from every subsystem, invisible at every
// call site, and impossible to see the runtime contents of without attaching a
// debugger — so a POLICY that lives here is a decision nobody can find. That is
// not hypothetical. apps/platform read deps.Domain as the git apex it trusts for
// builds; deps.Domain is the API host (api.hanzo.ai) and the forge is a SIBLING
// (git.hanzo.ai), so the self-hosted-git allowance could never match and EVERY
// native build was refused, silently falling back to GitHub — until GitHub
// access moved and the whole build train stopped (fixed dc84b46d).
//
// So: every field here is justified in one line below, and a field that is not
// in this map fails the test. Adding one is deliberate and reviewed, which is
// the entire point — the previous cost of adding a policy value to Deps was
// zero, and zero-cost is how five of them got here.
var depsFieldRationale = map[string]string{
	// --- process identity: true at exec, unchanged for the process lifetime ---
	"Brand": "WHOSE deployment this process is — one brand per binary, fixed at exec, and half of every tenant-scoped key; a per-request brand is resolved from the Host separately (brand.ForHostOK) and is a different fact",
	"Version": "the build this binary IS, stamped as X-Api-Version so a rollout can be verified from outside; " +
		"a link-time fact the process cannot look up",
	"Env":       "which of the 3 envs this process runs in (mainnet|testnet|devnet), an attribution label stamped on metered usage; never a gate — every env bills against its own ledger",
	"Domain":    "the deployment's OWN public API host, needed to build absolute URLs back to itself; it is the HOST and never the apex — anything needing the apex calls brand.Apex",
	"IAMIssuer": "the trust root: which IAM's signing keys validate a JWT. A boot fact, and one a subsystem must be handed because verifying identity precedes every store it could read it from",
	"DataDir":   "the on-disk root every per-org store opens beneath; resolved before config because the stores it roots open before config is read",

	// --- roots that must exist before any store opens ---
	"MasterKey": "the 32-byte at-rest KEK, decoded once so subsystems that seal their own stores do not each provision a key",
	"Durable":   "the HA-durability factory every OrgStore routes through; nil means local-only, which is a capability, not a setting",
	"Peers": "whether this deployment runs more than one writer — the ONLY thing that can answer who owns an org when Durable is nil, " +
		"and a fact about the deployment's topology that no subsystem can look up from inside the process",
	"LiveMembers": "the live writer set the shard router routes on; non-nil only when the durable plane is up",

	// --- clients: the bootstrap proper. Each is handed in because acquiring it
	//     is the thing that cannot bootstrap itself. ---
	"IAM":      "identity client",
	"KMS":      "secrets client — the canonical example: you cannot fetch your KMS credential from KMS",
	"Commerce": "typed inter-subsystem billing client",
	"AI":       "chat-completions client (a WRITE endpoint), authenticated by the binary's M2M identity",
	"Embed":    "embeddings client (READ-ONLY), split from AI so a publishable pk- key can never reach the completions path",
	"O11y":     "telemetry client",
	"VFS":      "object-store client",
	"Metering": "the commerce billing client the request-edge gate meters against",
	"Audit":    "the append-only audit Recorder, constructed once so the query endpoint reads the store the middleware writes",

	"GatewayPolicy": "the runtime-mutable edge-policy store — a live STORE the edge reads through, not a static setting",
	"Traffic":       "the edge's in-memory traffic sensor, written by the abuse gate and read by /v1/gateway/traffic",
}

// TestDepsCarriesNoPolicy fails when a field enters Deps without a written
// justification. It is a speed bump on purpose: the check a reviewer cannot
// perform by eye once the struct passes ~20 fields.
func TestDepsCarriesNoPolicy(t *testing.T) {
	tp := reflect.TypeFor[Deps]()
	var undocumented []string
	seen := map[string]bool{}
	for field := range tp.Fields() {
		name := field.Name
		seen[name] = true
		if _, ok := depsFieldRationale[name]; !ok {
			undocumented = append(undocumented, name)
		}
	}
	if len(undocumented) > 0 {
		sort.Strings(undocumented)
		t.Fatalf("Deps grew %d field(s) with no justification: %v\n\n"+
			"Deps is the DEPENDENCY surface. Before adding to depsFieldRationale, answer:\n"+
			"  can a subsystem obtain this for itself once the process is up?\n"+
			"    yes -> it is not a dependency. It is configuration, and it belongs\n"+
			"           wherever its ONE home is; a copy here becomes a second home.\n"+
			"    no  -> justify it in one line and add it.\n"+
			"A model name, a feature toggle, a pricing or routing decision is never a\n"+
			"dependency: there is nothing to connect to and nothing to fail.",
			len(undocumented), undocumented)
	}
	// The map may not outlive the struct either, or it becomes a list of fields
	// nobody can find.
	var stale []string
	for name := range depsFieldRationale {
		if !seen[name] {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Fatalf("depsFieldRationale justifies %v, which Deps no longer has — delete the entries", stale)
	}
}

// TestPolicyFieldsStayOutOfDeps names the five values this file exists to keep
// out, and the ONE home each went to. A field re-entering under its old name
// fails here with the reason rather than silently working.
func TestPolicyFieldsStayOutOfDeps(t *testing.T) {
	evicted := map[string]string{
		"AIDefaultModel":  "a routing+pricing decision, not an address. Home: the cloud.DefaultModel constant (model.go).",
		"AIFallbackModel": "same, and worse — `best` is a SKU in the GATEWAY's catalog with its own server-side route. Home: the cloud.FallbackModel constant (model.go).",
		"Self":            "had ZERO readers for its whole life. The id is real and lives in selfID(cfg); the copy on Deps was dead.",
	}
	tp := reflect.TypeFor[Deps]()
	for name, why := range evicted {
		if _, ok := tp.FieldByName(name); ok {
			t.Errorf("Deps.%s is back. It was removed because: %s", name, why)
		}
	}
	// Config is the other half: an evicted value must not keep its env knob, or
	// the field is gone and the second home is not.
	tc := reflect.TypeFor[Config]()
	for _, name := range []string{"AIDefaultModel", "AIFallbackModel"} {
		if _, ok := tc.FieldByName(name); ok {
			t.Errorf("Config.%s is back — the struct field was the second home, and this is it", name)
		}
	}
}

// TestModelDefaultHasNoDeploymentKnob proves the deletion is real: the env
// variables that used to move the model are inert. A knob that still reads is a
// second home no matter what the struct says.
func TestModelDefaultHasNoDeploymentKnob(t *testing.T) {
	t.Setenv("CLOUD_AI_DEFAULT_MODEL", "deepseek-v4-flash")
	t.Setenv("CLOUD_AI_FALLBACK_MODEL", "gpt-4o-mini")
	cfg := LoadConfig()
	if DefaultModel != "enso-flash" {
		t.Fatalf("DefaultModel = %q — the constant moved; update this pin deliberately", DefaultModel)
	}
	// Nothing on Config may carry the overridden values.
	v := reflect.ValueOf(*cfg)
	tp := v.Type()
	for i := 0; i < tp.NumField(); i++ {
		if v.Field(i).Kind() != reflect.String {
			continue
		}
		switch v.Field(i).String() {
		case "deepseek-v4-flash", "gpt-4o-mini":
			t.Fatalf("Config.%s picked up a deleted model knob — the env var still has a reader", tp.Field(i).Name)
		}
	}
}

// TestIssuerHasOneResolution is the regression pin for the divergence that made
// this whole exercise concrete.
//
// Config.IAMIssuer applied the brand fallback and the package-level IAMIssuer()
// did not — it read CLOUD_IAM_ISSUER raw and stopped. So on any deployment that
// let the brand supply the issuer (the documented white-label path: lux, zoo,
// pars) the process held TWO answers, "https://lux.id" and "". Empty is not a
// harmless disagreement: it flows to IAMBase(), which is what the sk- API key
// resolver dials, and an unresolvable base makes every API-key request
// authenticate as ANONYMOUS — silently, with the JWT path still working
// perfectly.
func TestIssuerHasOneResolution(t *testing.T) {
	for _, id := range []string{"hanzo", "lux", "zoo", "pars", "bootnode", "not-a-brand"} {
		t.Setenv("CLOUD_BRAND", id)
		os.Unsetenv("CLOUD_IAM_ISSUER")
		cfg := LoadConfig()
		if cfg.IAMIssuer == "" {
			t.Fatalf("brand %q: Config.IAMIssuer is empty", id)
		}
		if got := IAMIssuer(); got != cfg.IAMIssuer {
			t.Fatalf("brand %q: two answers for one fact — Config.IAMIssuer=%q, IAMIssuer()=%q",
				id, cfg.IAMIssuer, got)
		}
	}
	// An explicit pin still wins, and still wins in BOTH places.
	t.Setenv("CLOUD_BRAND", "lux")
	t.Setenv("CLOUD_IAM_ISSUER", "https://id.example.test")
	cfg := LoadConfig()
	if cfg.IAMIssuer != "https://id.example.test" || IAMIssuer() != "https://id.example.test" {
		t.Fatalf("pinned issuer not honoured in both homes: Config=%q, IAMIssuer()=%q", cfg.IAMIssuer, IAMIssuer())
	}
}

// TestDomainDerivesFromBrand pins the asymmetry this fixed: IAMIssuer derived
// from the brand and Domain did not, so an unpinned lux deployment resolved
// issuer=https://lux.id alongside domain=api.hanzo.ai and built every
// self-referential URL pointing at another brand's host.
func TestDomainDerivesFromBrand(t *testing.T) {
	want := map[string]string{
		"hanzo": "api.hanzo.ai",
		"lux":   "api.lux.network",
		"zoo":   "api.zoo.ngo",
		"pars":  "api.pars.network",
	}
	for id, host := range want {
		t.Setenv("CLOUD_BRAND", id)
		os.Unsetenv("CLOUD_DOMAIN")
		if got := LoadConfig().Domain; got != host {
			t.Errorf("brand %q: Domain = %q, want %q", id, got, host)
		}
	}
	// An operator pin still wins outright.
	t.Setenv("CLOUD_BRAND", "lux")
	t.Setenv("CLOUD_DOMAIN", "api.custom.example")
	if got := LoadConfig().Domain; got != "api.custom.example" {
		t.Fatalf("pinned CLOUD_DOMAIN = %q, want api.custom.example", got)
	}
}

// TestApexIsOneDerivation pins brand.Apex as the single reduction of a host to
// the apex, and in particular the multi-label-suffix case that separates it from
// the "last two labels" rule it replaced in apps/sites — where the output seeds
// SelfDomains, the set deciding a host is OURS and not a tenant's to claim. Last
// two labels of api.acme.co.uk is "co.uk": the deployment claimed a public
// suffix, i.e. every domain under it.
func TestApexIsOneDerivation(t *testing.T) {
	for _, c := range []struct{ host, apex string }{
		{"api.hanzo.ai", "hanzo.ai"},
		{"hanzo.ai", "hanzo.ai"},
		{"API.HANZO.AI:8443", "hanzo.ai"},
		{"api.hanzo.ai.", "hanzo.ai"}, // trailing root dot
		{"cloud.hanzo.ai", "hanzo.ai"},
		{"api.lux.network", "lux.network"},
		{"api.acme.co.uk", "acme.co.uk"},             // NOT "co.uk"
		{"api.tenant.github.io", "tenant.github.io"}, // NOT "github.io"
		{"localhost", "localhost"},
		{"", ""},
	} {
		if got := brand.Apex(c.host); got != c.apex {
			t.Errorf("brand.Apex(%q) = %q, want %q", c.host, got, c.apex)
		}
	}
	// A deployment's forge/CI/CD are SIBLINGS of its API, and Sibling is how they
	// are named from the one domain a deployment is configured with.
	if got := brand.Sibling("api.hanzo.ai", "git"); got != "git.hanzo.ai" {
		t.Errorf("brand.Sibling(api.hanzo.ai, git) = %q, want git.hanzo.ai", got)
	}
	if got := brand.Sibling("cloud.hanzo.ai", "git"); got != "git.hanzo.ai" {
		t.Errorf("brand.Sibling(cloud.hanzo.ai, git) = %q, want git.hanzo.ai "+
			"— the old TrimPrefix(\"api.\") rule returned git.cloud.hanzo.ai, "+
			"a forge host platform's own allowlist would then refuse", got)
	}
}

// TestJWKSHasOneDerivation pins the JWKS endpoint to one resolution.
//
// CLOUD_JWKS_URL exists because the public issuer host is fronted by Cloudflare,
// which 403s a server-side loopback (iamurl.go), so production pins the
// IN-CLUSTER address: CLOUD_JWKS_URL=http://iam.hanzo.svc/v1/iam/.well-known/jwks
// against CLOUD_IAM_ISSUER=https://hanzo.id.
//
// JWKSURLFor honoured that and was unexported, so the two planes that could not
// reach it — durable.go's gated ZAP listener and apps/base's per-app pool — each
// concatenated the suffix onto the ISSUER instead and silently ignored the
// override. Both were fetching signing keys from the public host that refuses
// them, while the edge validator used the working in-cluster one: one fleet,
// two answers, and the failure is a plane that cannot verify a token the edge
// just accepted.
func TestJWKSHasOneDerivation(t *testing.T) {
	const issuer = "https://hanzo.id"
	t.Setenv("CLOUD_JWKS_URL", "http://iam.hanzo.svc/v1/iam/.well-known/jwks")
	got := JWKSURLFor(issuer)
	if got != "http://iam.hanzo.svc/v1/iam/.well-known/jwks" {
		t.Fatalf("JWKSURLFor ignored CLOUD_JWKS_URL: got %q", got)
	}
	if strings.HasPrefix(got, issuer) {
		t.Fatalf("JWKSURLFor built the URL off the issuer (%q) instead of honouring the "+
			"pin — that is the derivation durable.go and apps/base used to copy", got)
	}
	// Unset, it falls back to the HIP-0111 convention.
	os.Unsetenv("CLOUD_JWKS_URL")
	if want := issuer + "/v1/iam/.well-known/jwks"; JWKSURLFor(issuer) != want {
		t.Fatalf("JWKSURLFor(%q) = %q, want %q", issuer, JWKSURLFor(issuer), want)
	}
	// And Config agrees with the function, in both directions.
	t.Setenv("CLOUD_IAM_ISSUER", issuer)
	t.Setenv("CLOUD_JWKS_URL", "http://iam.hanzo.svc/v1/iam/.well-known/jwks")
	if cfg := LoadConfig(); cfg.JWKSURL != JWKSURLFor(cfg.IAMIssuer) {
		t.Fatalf("Config.JWKSURL=%q, JWKSURLFor=%q — two answers again", cfg.JWKSURL, JWKSURLFor(cfg.IAMIssuer))
	}

	// The behaviour above cannot catch the actual defect: durable.go and
	// apps/base did not call this function wrongly, they did not call it at all.
	// So the recurrence is a subsystem BUILDING the path again, and the only
	// thing that sees that is the source. One derivation means one place spells
	// the suffix as a URL; everywhere else it is prose in a comment.
	const suffix = `"/v1/iam/.well-known/jwks`
	var builders []string
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// `.claude` and `.worktrees` hold WORKTREES — other checkouts of this
			// same repo, at other commits, that git puts inside the working tree.
			// Walking into them makes this gate read a different revision's source
			// and blame this one for it: it failed here naming
			// `.claude/worktrees/agent-…/apps/base/pool.go`, a file that is not in
			// this commit at all. It cannot fire in CI, which checks out clean, so
			// it is a phantom that only ever wastes the person who has a worktree.
			switch d.Name() {
			case "vendor", ".git", "node_modules", ".claude", ".worktrees":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		if filepath.ToSlash(p) == "token_validator.go" {
			return nil // the one derivation
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		if strings.Contains(string(b), suffix) {
			builders = append(builders, p)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(builders) > 0 {
		sort.Strings(builders)
		t.Errorf("%v build the JWKS path themselves.\n"+
			"Call cloud.JWKSURLFor(issuer). Concatenating the suffix onto the issuer "+
			"ignores CLOUD_JWKS_URL, and production pins it because the public issuer "+
			"host 403s a server-side loopback — that is the fleet verifying one set of "+
			"signing keys at the edge and a different set behind it.", builders)
	}
}

// TestNoStrayDomainDefaults keeps the literal from growing a second home again.
// "api.hanzo.ai" was spelled independently in config.go AND apps/sites/env.go,
// each with its own brand-blind default, so the deployment's own host was a fact
// stated twice and nothing made the two agree.
func TestNoStrayDomainDefaults(t *testing.T) {
	if brand.APIHost("hanzo") != "api.hanzo.ai" {
		t.Fatalf("brand.APIHost(hanzo) = %q, want api.hanzo.ai", brand.APIHost("hanzo"))
	}
	// The root Config default must come from that one function, not a literal.
	os.Unsetenv("CLOUD_DOMAIN")
	t.Setenv("CLOUD_BRAND", "zoo")
	if got, want := LoadConfig().Domain, brand.APIHost("zoo"); got != want {
		t.Fatalf("Config.Domain = %q, want brand.APIHost(zoo) = %q — a literal crept back in", got, want)
	}
	if strings.Contains(brand.APIHost("zoo"), "hanzo") {
		t.Fatal("a zoo deployment resolved a hanzo host")
	}
}
