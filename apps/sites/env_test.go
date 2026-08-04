package sites

import (
	"reflect"
	"testing"

	luxlog "github.com/luxfi/log"
)

// The environment as production actually sets it, read off the running pod: ONE
// site variable is set and every other default applies. Both processes that mount
// the edge boot from exactly this.
func prodEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{
		"CLOUD_SITES_APEX", "CLOUD_SITES_SELF_DOMAINS",
		"CLOUD_SITES_FIRSTPARTY_APEX", "CLOUD_SITES_FIRSTPARTY", "CLOUD_SITES_FIRSTPARTY_ORG",
		"CLOUD_DOMAIN",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("CLOUD_SITES_RESERVED", "stg")
}

// THE REGRESSION. The edge and the per-app children each resolved Config for
// themselves, from two spellings of the first-party keys (CLOUD_SITES_FIRSTPARTY_*
// against CLOUD_SITES_FIRST_PARTY_*) with two sets of defaults. Neither spelling of
// the first-party keys is set in production, so the two processes booted different
// policies off their DEFAULTS alone: the edge resolved no first-party apex, so
// hanzo.ai never entered its self-domain set, so every hanzo.ai host — api.hanzo.ai
// included — was a custom-domain candidate and took the per-request binding lookup
// that the self-domain exclusion exists to keep off that path.
//
// There is now ONE resolution, so "which process answered" cannot select a policy.
func TestConfigFromEnvIsTheOnlyResolution(t *testing.T) {
	prodEnv(t)

	// The router passes "" (its --domain is already published as CLOUD_DOMAIN);
	// cloud.Listen passes the Config.Domain it resolved. Same answer.
	edge := ConfigFromEnv("")
	child := ConfigFromEnv("api.hanzo.ai")
	if !reflect.DeepEqual(edge, child) {
		t.Fatalf("the two mounts resolve different policy:\n edge  = %+v\n child = %+v", edge, child)
	}

	if want := []string{"hanzo.app", "hanzo.ai"}; !reflect.DeepEqual(edge.SelfDomains, want) {
		t.Errorf("SelfDomains = %v, want %v — hanzo.ai missing makes api.hanzo.ai a custom-domain candidate", edge.SelfDomains, want)
	}
	if edge.FirstPartyApex != "hanzo.ai" || edge.FirstPartyOrg != "hanzo" {
		t.Errorf("first-party = %q/%q, want hanzo.ai/hanzo", edge.FirstPartyApex, edge.FirstPartyOrg)
	}
}

// The self-domain exclusion is what keeps the hot api/console path out of the
// binding resolver, and keeps a customer binding from shadowing a real host.
func TestSelfDomainsCoverTheBrandApex(t *testing.T) {
	prodEnv(t)
	srv := New(ConfigFromEnv(""), luxlog.New("test"))

	for _, h := range []string{"hanzo.ai", "api.hanzo.ai", "cd.hanzo.ai", "hanzo.app", "x.hanzo.app"} {
		if !IsSelfHost(h) {
			t.Errorf("IsSelfHost(%q) = false — a customer could take a claim row on it", h)
		}
		if srv.customCandidate(h) {
			t.Errorf("customCandidate(%q) = true — our own host entered the per-request binding lookup", h)
		}
	}
	// A genuinely external domain must still be bindable, or custom domains break.
	if !srv.customCandidate("yadota.tech") {
		t.Error("customCandidate(yadota.tech) = false — real custom domains would stop resolving")
	}
}

// The first-party apex has to reach the PUBLISHED set, not just the local slice the
// constructor was still building.
//
// SetSelfDomains COPIES its argument, so publishing before the fold left the
// first-party apex out of IsSelfHost — the one predicate that answers "is this host
// ours". The test above cannot see it: production lists hanzo.ai in SelfDomains AND
// as the first-party apex, so the set is right there by the first route regardless.
// Under a config that names it only as FirstPartyApex, every non-allowlisted
// <label>.<fpApex> became a custom-domain CANDIDATE on the apex carrying
// api/login/console — a claim row a customer could take.
func TestFirstPartyApexReachesThePublishedSet(t *testing.T) {
	prodEnv(t)
	t.Cleanup(func() { SetSelfDomains(nil) })

	srv := New(Config{
		Apex:            "hanzo.app",
		SelfDomains:     nil, // names the first-party apex NOWHERE but FirstPartyApex
		FirstPartyApex:  "hanzo.ai",
		FirstPartyOrg:   "hanzo",
		FirstPartySites: []string{"cd"},
	}, luxlog.New("test"))

	for _, h := range []string{"hanzo.ai", "api.hanzo.ai", "login.hanzo.ai"} {
		if !IsSelfHost(h) {
			t.Errorf("IsSelfHost(%q) = false — the first-party apex never reached the published set", h)
		}
		if srv.customCandidate(h) {
			t.Errorf("customCandidate(%q) = true — a customer could bind one of our own hosts", h)
		}
	}
	// The exclusion must stay narrow: a genuinely external domain is still bindable.
	if !srv.customCandidate("yadota.tech") {
		t.Error("customCandidate(yadota.tech) = false — real custom domains would stop resolving")
	}
}

// The operator variable ADDS to the baked-in denylist and can never subtract from
// it. This is why an empty CLOUD_SITES_RESERVED is not a hole: the labels an
// attacker wants are in reserved.go, not in the environment.
func TestReservedEnvCannotUnreserve(t *testing.T) {
	prodEnv(t)
	t.Setenv("CLOUD_SITES_RESERVED", "") // the emptiest possible operator input
	t.Cleanup(func() { SetReservedExtra(nil) })

	New(ConfigFromEnv(""), luxlog.New("test"))

	// The nine labels the old CLOUD_SITES_RESERVED default named. Every one is
	// baked in, so dropping the variable entirely changes nothing.
	for _, l := range []string{"www", "api", "app", "admin", "mail", "ftp", "cdn", "static", "assets"} {
		if !IsReserved(l) {
			t.Errorf("IsReserved(%q) = false with an empty env — a tenant could publish it", l)
		}
	}
	// And the extras still add.
	t.Setenv("CLOUD_SITES_RESERVED", "quux")
	New(ConfigFromEnv(""), luxlog.New("test"))
	if !IsReserved("quux") {
		t.Error("operator extras no longer add")
	}
	if !IsReserved("admin") {
		t.Error("setting extras dropped a baked-in label")
	}
}

// Blanks MUST drop. An empty label is the apex itself, so a trailing comma would
// otherwise reserve — or self-claim — the whole zone.
func TestConfigFromEnvDropsBlanks(t *testing.T) {
	prodEnv(t)
	t.Setenv("CLOUD_SITES_RESERVED", "stg, www ,")
	t.Setenv("CLOUD_SITES_SELF_DOMAINS", " , ")

	cfg := ConfigFromEnv("api.hanzo.ai")
	if want := []string{"stg", "www"}; !reflect.DeepEqual(cfg.Reserved, want) {
		t.Errorf("Reserved = %#v, want %v", cfg.Reserved, want)
	}
	// An all-blank list is no list at all, so the derived default must still apply
	// rather than leaving the brand apex out of the exclusion set.
	if want := []string{"hanzo.app", "hanzo.ai"}; !reflect.DeepEqual(cfg.SelfDomains, want) {
		t.Errorf("SelfDomains = %#v, want %v", cfg.SelfDomains, want)
	}
}

// An explicit operator value wins over every default, and a deployment on another
// brand gets ITS own registrable domain in the exclusion set.
func TestConfigFromEnvOverrides(t *testing.T) {
	prodEnv(t)
	t.Setenv("CLOUD_SITES_APEX", "lux.page")
	t.Setenv("CLOUD_SITES_FIRSTPARTY_APEX", "lux.network")
	t.Setenv("CLOUD_SITES_FIRSTPARTY", "docs")
	t.Setenv("CLOUD_SITES_FIRSTPARTY_ORG", "lux")

	cfg := ConfigFromEnv("api.lux.network")
	if want := []string{"lux.page", "lux.network"}; !reflect.DeepEqual(cfg.SelfDomains, want) {
		t.Errorf("SelfDomains = %v, want %v", cfg.SelfDomains, want)
	}
	if cfg.Apex != "lux.page" || cfg.FirstPartyOrg != "lux" ||
		!reflect.DeepEqual(cfg.FirstPartySites, []string{"docs"}) {
		t.Errorf("overrides lost: %+v", cfg)
	}
}

func TestRegistrableDomain(t *testing.T) {
	for in, want := range map[string]string{
		"api.hanzo.ai": "hanzo.ai", "hanzo.app": "hanzo.app", "a.b.c.hanzo.ai": "hanzo.ai",
		"localhost": "localhost", "HANZO.AI:8080": "hanzo.ai", "hanzo.ai.": "hanzo.ai",
	} {
		if got := registrableDomain(in); got != want {
			t.Errorf("registrableDomain(%q) = %q, want %q", in, got, want)
		}
	}
}
