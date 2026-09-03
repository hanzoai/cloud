package manifest

import (
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// alt is naive English and says so; these are the shapes it has to get right,
// stated as pairs because the rule is an involution and a one-way table would
// hide the direction that is wrong.
func TestAltIsTheOtherNumber(t *testing.T) {
	for _, p := range [][2]string{
		{"agent", "agents"},       // the ordinary case
		{"box", "boxes"},          // -x
		{"sandbox", "sandboxes"},  // -x, and a real row
		{"batch", "batches"},      // -ch
		{"brush", "brushes"},      // -sh
		{"entity", "entities"},    // -y after a consonant
		{"key", "keys"},           // -y after a vowel is NOT -ies
		{"pref", "prefs"},         // an abbreviation that still counts
		{"campaign", "campaigns"}, // renamed for number once; never again
		{"eval", "evals"},         //
		{"skill", "skills"},       //
		{"integration", "integrations"},
	} {
		if got := alt(p[0]); got != p[1] {
			t.Errorf("alt(%q) = %q, want %q", p[0], got, p[1])
		}
		if got := alt(p[1]); got != p[0] {
			t.Errorf("alt(%q) = %q, want %q — the rule must read both directions", p[1], got, p[0])
		}
	}
}

// A word with no number derives nothing, in either direction. Left to the naive
// rule /v1/dns would open /v1/dn and /v1/kms would open /v1/km — nonsense
// addresses resolving to real capabilities, which is worse than opening none.
func TestNoNumberDerivesNothing(t *testing.T) {
	for w := range noNumber {
		if got := alt(w); got != "" {
			t.Errorf("alt(%q) = %q — %q has no grammatical number and must derive no alias", w, got, w)
		}
	}
}

// noNumber is a vocabulary, and a vocabulary rots. Every entry must still name a
// capability this fleet has, or it is a word we no longer speak.
func TestNoNumberNamesRealCapabilities(t *testing.T) {
	have := map[string]bool{}
	for _, a := range Apps {
		have[a.Name] = true
	}
	for w := range noNumber {
		if !have[w] {
			t.Errorf("noNumber has %q and Apps does not — delete the line, or the list is describing a fleet we do not run", w)
		}
	}
}

// alt is an involution over the whole vocabulary: alt(alt(name)) is name. That
// is the property the alias rests on — it is what lets ONE function serve both
// directions, so nothing has to know whether the row it is holding is the
// singular or the plural.
func TestAltRoundTripsEveryName(t *testing.T) {
	for _, a := range Apps {
		other := alt(a.Name)
		if other == "" {
			continue
		}
		if back := alt(other); back != a.Name {
			t.Errorf("%s -> %s -> %s: the alias is not reversible, so one spelling of it addresses nothing",
				a.Name, other, back)
		}
	}
}

// THE CANONICAL ALWAYS WINS. An alias may only open an address nothing serves;
// it may never take one a row declares. Without this a rename could silently
// point a live prefix at a different app — the failure mode with no error and no
// log that manifest/prefix_owner_test.go guards from the other side.
func TestNoAliasShadowsACanonicalPrefix(t *testing.T) {
	declared := map[string]string{}
	for _, a := range Apps {
		for _, p := range a.Prefixes {
			declared[p] = a.Name
		}
	}
	for spelt, canonical := range alias {
		if owner, taken := declared[spelt]; taken {
			t.Errorf("%s is %s's declared prefix AND an alias of %s — a served address may not be rewritten",
				spelt, owner, canonical)
		}
		if _, real := declared[canonical]; !real {
			t.Errorf("%s aliases %s, which no row declares", spelt, canonical)
		}
	}
}

// One alias, one meaning. Two capabilities whose names differ only by number
// would each derive the other's address, and the map would keep whichever loop
// iteration ran last — a coin flip at init, in the routing table.
func TestNoTwoCapabilitiesShareAnAlias(t *testing.T) {
	seen := map[string]string{}
	for _, a := range Apps {
		other := alt(a.Name)
		if other == "" {
			continue
		}
		if first, clash := seen[other]; clash {
			t.Errorf("%s and %s both spell %q — two capabilities that differ only in number are ONE capability (HIP-0139 §2.4)",
				first, a.Name, other)
		}
		seen[other] = a.Name
	}
}

// Normalize touches the capability's own name segment and NOTHING else. A word
// that is some other app's name, sitting deep inside a subtree, belongs to the
// app that owns the subtree — /v1/git/projects is git's, whole, even while
// /v1/project is an alias somewhere else.
func TestNormalizeIsAnchoredAtTheRoot(t *testing.T) {
	for spelt, canonical := range alias {
		if got := Normalize(spelt); got != canonical {
			t.Fatalf("Normalize(%q) = %q, want %q", spelt, got, canonical)
		}
		// The same word one segment deeper is a different address and is left alone.
		deep := "/v1/git" + spelt
		if got := Normalize(deep); got != deep {
			t.Errorf("Normalize(%q) = %q — an alias is anchored at the root, so a sibling's word inside another app's subtree must be untouched",
				deep, got)
		}
		// The remainder rides along unchanged.
		if got, want := Normalize(spelt+"/zzq/x"), canonical+"/zzq/x"; got != want {
			t.Errorf("Normalize(%q) = %q, want %q", spelt+"/zzq/x", got, want)
		}
	}
	// A canonical path is returned byte-identical. This is every path we publish.
	for _, a := range Apps {
		for _, p := range a.Prefixes {
			if got := Normalize(p); got != p {
				t.Errorf("Normalize(%q) = %q — a declared prefix is already canonical", p, got)
			}
		}
	}
}

// BOTH SPELLINGS REACH THE SAME APP, asked of the REAL ROUTER.
//
// Not of Normalize, and not of OwnerOf — of the fasthttp router the host builds,
// with the host's own rewrite at the entry point, answered by the app that
// received the request. That is the only thing that proves the rewrite actually
// re-matches: Fiber caches a route bucket per request, and a path override that
// did not recompute it would leave every aliased request landing on the 404
// while every unit test above stayed green.
func TestBothSpellingsReachTheSameApp(t *testing.T) {
	fleet := front(t)
	probed := 0
	for _, a := range Apps {
		if a.Coresident {
			continue
		}
		other := alt(a.Name)
		if other == "" {
			continue
		}
		for _, p := range a.Prefixes {
			spelt := respell(p, a.Name, other)
			if spelt == p || alias[spelt] != p {
				continue // not an alias of this row (it shadows a canonical prefix)
			}
			probed++
			if to := destination(t, fleet, spelt); to != a.Name {
				t.Errorf("%s -> %s, want %s — the other spelling of a capability's name must reach it",
					spelt, to, a.Name)
			}
			// And the canonical spelling still does, unchanged.
			if to := destination(t, fleet, p); to != a.Name {
				t.Errorf("%s -> %s, want %s — the canonical address regressed", p, to, a.Name)
			}
		}
	}
	if probed == 0 {
		t.Fatal("no alias was probed — this gate proved nothing")
	}
	t.Logf("both spellings resolve for %d addresses", probed)
}

// front is the fleet's router with the host's rewrite in front of it — the same
// two lines cmd/cloud composes, in the same order, so what this test asks is
// what a request meets. It reuses router(t) (manifest/router_test.go) for the
// mounts, so there is one description of the routing table and this adds only
// the rewrite.
func front(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{AppName: "alias-endpoint", DisableStartupMessage: true})
	app.Use(zip.H(func(c *zip.Ctx) error {
		if p := c.Path(); p != "" {
			if canonical := Normalize(p); canonical != p {
				c.Fiber().Path(canonical)
			}
		}
		return c.Next()
	}))
	app.Use(router(t))
	return app
}

// THE ALIAS IS NOT PUBLISHED. The document, and therefore every generated SDK,
// every docs page and the agent tool list, names the canonical capability and
// only it. Publishing both spellings would put two names on one thing, which is
// the duplication the alias exists to END — the courtesy belongs to the router,
// never to the document.
func TestNoAliasIsPublished(t *testing.T) {
	spellings := make([]string, 0, len(alias))
	for spelt := range alias {
		spellings = append(spellings, spelt)
	}
	sort.Strings(spellings)

	// BOTH documents — the customer contract and the internal one. A second
	// spelling is a duplication wherever it is read, and an operator reads the
	// second file.
	for _, doc := range []string{"../openapi.yaml", "../private.yaml"} {
		raw, err := os.ReadFile(doc)
		if err != nil {
			t.Fatalf("%s: %v", doc, err)
		}
		body := string(raw)
		for _, spelt := range spellings {
			// A published path is a whole line's worth of address. Match the
			// address followed by a boundary, so /v1/agent never reads as a hit
			// inside /v1/agent.
			for _, boundary := range []string{"'", "\"", ":", "/", "}"} {
				if strings.Contains(body, spelt+boundary) {
					t.Errorf("%s publishes %q — the alias is the router's courtesy, not an address the document claims (HIP-0139 §2.2)",
						doc, spelt)
					break
				}
			}
		}
	}
}

// The alias is derived, so it is worth SEEING. Nothing here can fail; it prints
// what the rule opened, because the only way a nonsense address gets caught is a
// person reading the list.
func TestAliasesAreVisible(t *testing.T) {
	lines := make([]string, 0, len(alias))
	for spelt, canonical := range alias {
		lines = append(lines, spelt+" -> "+canonical)
	}
	sort.Strings(lines)
	t.Logf("%d aliased addresses:\n%s", len(lines), strings.Join(lines, "\n"))
}
