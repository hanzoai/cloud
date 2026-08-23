package principal_test

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// The BRAND, the PROJECT and the MINTED principal, read from both sides of the
// client — the request and the bare context.Context a typed op receives.
//
// These accessors had no coverage at all, and they are the ones whose mistakes do
// not announce themselves: a brand that answers without a validated principal
// writes one tenant's org into another's key space, and a project that reports an
// authority it does not have hard-refuses a caller the identical REST call lets
// through. Each case below is one sentence from the doc comment above the
// function it exercises, so a rule that moves takes a named test with it.

// serve mounts one handler on a real zip.App and drives it with the headers the
// gateway sets, which is how the rest of this package's tests reach a *zip.Ctx —
// there is no constructor for one, and a hand-built fake would be testing the
// fake. The handler reports through the response so an assertion reads what a
// caller would.
func serve(t *testing.T, headers map[string]string, h func(*zip.Ctx) error) {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Get("/probe", h)
	req := httptest.NewRequest("GET", "/probe", nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if _, err := app.Test(req); err != nil {
		t.Fatalf("probe: %v", err)
	}
}

// validated is the header set SanitizeIdentity produces for a real principal:
// X-User-Id is what Validated reads, and it is present ONLY once a token verified.
func validated(extra map[string]string) map[string]string {
	h := map[string]string{"X-User-Id": "u-1", "X-Org-Id": "hanzo"}
	for k, v := range extra {
		h[k] = v
	}
	return h
}

// ── the brand: a SECOND fact, or none at all ────────────────────────────────

// Brand answers only for a validated principal carrying an issuer, and its doc is
// explicit that a caller must read !ok as "no second fact, never a brand". An
// unvalidated request that answered a brand would be the forgeable-identity hole
// one layer along: the header is client-supplied until SanitizeIdentity has
// spoken for it.
func TestBrand_NeedsAValidatedPrincipalAndAnIssuer(t *testing.T) {
	for _, tc := range []struct {
		name    string
		headers map[string]string
		want    string
		wantOK  bool
	}{
		{"unvalidated, brand header present", map[string]string{"X-User-Brand": "lux.id"}, "", false},
		{"validated, no brand header", validated(nil), "", false},
		{"validated, blank brand header", validated(map[string]string{"X-User-Brand": "   "}), "", false},
		{"validated with an issuer", validated(map[string]string{"X-User-Brand": "lux.id"}), "lux.id", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			serve(t, tc.headers, func(c *zip.Ctx) error {
				got, ok := principal.Brand(c)
				if got != tc.want || ok != tc.wantOK {
					t.Errorf("Brand() = %q,%v want %q,%v", got, ok, tc.want, tc.wantOK)
				}
				return c.NoContent(200)
			})
		})
	}
}

// WithBrand parks NOTHING when there is nothing to compare, and BrandFrom answers
// the same value on the far side. The pair exists so a typed op — which receives
// a context and no request — reads ONE fact rather than a second copy of it, so
// the round trip is the property worth pinning.
func TestBrand_CrossesTheClientOrParksNothing(t *testing.T) {
	t.Run("carried", func(t *testing.T) {
		serve(t, validated(map[string]string{"X-User-Brand": "zoo.ngo"}), func(c *zip.Ctx) error {
			ctx := principal.WithBrand(context.Background(), c)
			got, ok := principal.BrandFrom(ctx)
			if got != "zoo.ngo" || !ok {
				t.Errorf("BrandFrom = %q,%v want zoo.ngo,true", got, ok)
			}
			return c.NoContent(200)
		})
	})

	t.Run("nothing to compare parks nothing", func(t *testing.T) {
		serve(t, validated(nil), func(c *zip.Ctx) error {
			ctx := principal.WithBrand(context.Background(), c)
			if got, ok := principal.BrandFrom(ctx); ok {
				t.Errorf("BrandFrom = %q,true — an absent brand must not become one", got)
			}
			return c.NoContent(200)
		})
	})

	// A bare context never went through WithBrand, so it holds no brand — and a
	// caller must not read that as an empty-string brand.
	if got, ok := principal.BrandFrom(context.Background()); ok {
		t.Errorf("BrandFrom(bare ctx) = %q,true want false", got)
	}
}

// ── the project: a NARROWING, which is why it parks unconditionally ─────────

// WithProject parks unconditionally, and that asymmetry with WithOrg is the point:
// the org is an AUTHORITY so an unvalidated request must park nothing, while the
// project is a narrowing every consumer AND-s with an org that already gates. This
// pins the asymmetry so a later "consistency" edit cannot quietly gate it twice.
func TestProject_ParksEvenWithoutAValidatedPrincipal(t *testing.T) {
	serve(t, map[string]string{"X-Project-Id": "atlas"}, func(c *zip.Ctx) error {
		ctx := principal.WithProject(context.Background(), c)
		if got := principal.ProjectFrom(ctx); got != "atlas" {
			t.Errorf("ProjectFrom = %q want atlas — the project is a narrowing, not an authority", got)
		}
		if principal.Validated(c) {
			t.Fatal("fixture error: this case must be UNvalidated for the assertion to mean anything")
		}
		return c.NoContent(200)
	})
}

// A context with no request behind it answers DefaultProject — "no narrowing" —
// which is the same answer Project gives a request naming no project.
func TestProjectFrom_BareContextIsNoNarrowing(t *testing.T) {
	if got := principal.ProjectFrom(context.Background()); got != principal.DefaultProject {
		t.Errorf("ProjectFrom(bare ctx) = %q want %q", got, principal.DefaultProject)
	}
}

// ValidatedProjectFrom composes ONE rule from two inputs, and the carve-out is the
// whole reason it exists: a caller that read ProjectFrom and ValidatedFrom
// separately reconstructed a different rule, dropped the default-project
// exemption, and hard-enforced 402 on the agent MCP server while the identical REST
// call softened. The default project is never claim-backed, however validated the
// caller is.
func TestValidatedProjectFrom_DefaultIsNeverClaimBacked(t *testing.T) {
	t.Run("named project, validated", func(t *testing.T) {
		serve(t, validated(map[string]string{"X-Project-Id": "atlas"}), func(c *zip.Ctx) error {
			ctx := principal.WithValidated(principal.WithProject(context.Background(), c), c)
			project, ok := principal.ValidatedProjectFrom(ctx)
			if project != "atlas" {
				t.Errorf("project = %q want atlas", project)
			}
			if !ok {
				t.Error("a validated caller on a NAMED project is claim-backed; a cap may hard-enforce")
			}
			return c.NoContent(200)
		})
	})

	t.Run("default project, validated", func(t *testing.T) {
		serve(t, validated(nil), func(c *zip.Ctx) error {
			ctx := principal.WithValidated(principal.WithProject(context.Background(), c), c)
			project, ok := principal.ValidatedProjectFrom(ctx)
			if !principal.IsDefaultProject(project) {
				t.Fatalf("fixture error: project = %q, expected the default", project)
			}
			if ok {
				t.Error("the DEFAULT project is never claim-backed — this is the #70 spoof defense, " +
					"and dropping it is what made the agent MCP server 402 where REST warned")
			}
			return c.NoContent(200)
		})
	})
}

// ProjectScope is the same project read as a storage KEY, where the default means
// "no filter" and must therefore be empty rather than the literal "default" — a
// scope of "default" would key one org's rows under a project name.
func TestProjectScope_DefaultIsAnEmptyKey(t *testing.T) {
	serve(t, validated(nil), func(c *zip.Ctx) error {
		if got := principal.ProjectScope(c); got != "" {
			t.Errorf("ProjectScope = %q want \"\" — the default project is no filter", got)
		}
		return c.NoContent(200)
	})
	serve(t, validated(map[string]string{"X-Project-Id": "atlas"}), func(c *zip.Ctx) error {
		if got := principal.ProjectScope(c); got != "atlas" {
			t.Errorf("ProjectScope = %q want atlas", got)
		}
		return c.NoContent(200)
	})
}

// ── the minted principal, and the two admin scopes ──────────────────────────

// Mint COPIES the strings it stores, which is why Minted can be trusted after the
// caller reuses its buffer. The round trip and the copy are one property: a stored
// principal that aliased the caller's memory would report whatever that memory
// became.
func TestMint_StoresACopyAndMintedReadsItBack(t *testing.T) {
	serve(t, validated(nil), func(c *zip.Ctx) error {
		if _, ok := principal.Minted(c); ok {
			t.Error("Minted reported a principal before Mint stored one")
		}
		src := principal.Principal{Org: "hanzo", User: "u-1", Subject: "hanzo/u-1"}
		principal.Mint(c, src)

		got, ok := principal.Minted(c)
		if !ok {
			t.Fatal("Minted = !ok directly after Mint")
		}
		if got.Org != "hanzo" || got.User != "u-1" || got.Subject != "hanzo/u-1" {
			t.Errorf("Minted = %+v want the minted principal", got)
		}
		return c.NoContent(200)
	})
}

// The two admin scopes are separate facts and conflating them is a privilege
// escalation, so each predicate reads its OWN header and neither infers the other.
func TestAdminScopes_EachReadsItsOwnHeader(t *testing.T) {
	serve(t, validated(map[string]string{"X-User-IsOrgAdmin": "true"}), func(c *zip.Ctx) error {
		if !principal.IsOrgAdmin(c) {
			t.Error("IsOrgAdmin = false with X-User-IsOrgAdmin: true")
		}
		if principal.IsApp(c) {
			t.Error("IsApp inferred from an ORG-admin header — the two scopes are separate facts")
		}
		return c.NoContent(200)
	})
	serve(t, validated(map[string]string{"X-User-IsApp": "true"}), func(c *zip.Ctx) error {
		if !principal.IsApp(c) {
			t.Error("IsApp = false with X-User-IsApp: true")
		}
		if principal.IsOrgAdmin(c) {
			t.Error("IsOrgAdmin inferred from an APP header")
		}
		return c.NoContent(200)
	})
	// Anything that is not exactly "true" is not an admin. A header a client can
	// set must fail closed on every other spelling.
	for _, v := range []string{"", "false", "TRUE", "1", "yes"} {
		serve(t, validated(map[string]string{"X-User-IsOrgAdmin": v}), func(c *zip.Ctx) error {
			if principal.IsOrgAdmin(c) {
				t.Errorf("IsOrgAdmin = true for %q — only the exact string admits", v)
			}
			return c.NoContent(200)
		})
	}
}

// Refused and Refusal do NOT decide whether to refuse — the caller has already
// decided that. They decide the WORDING, and there are exactly two: a caller with
// no attested identity is told to authenticate, and a caller who has one but no
// org scope is told the scope is missing. Sending the first to the second sends a
// signed-in operator to re-authenticate, which is the wrong instruction and the
// reason both wordings live in one function.
//
// Both always refuse. A test that expected nil for a validated caller was reading
// them as a predicate, which they are not.
func TestRefusal_PicksTheWordingForTheCallerItHas(t *testing.T) {
	serve(t, nil, func(c *zip.Ctx) error {
		if got := principal.Refusal(c); got != "a validated principal is required" {
			t.Errorf("Refusal(no identity) = %q — an unattested caller is told to authenticate", got)
		}
		if principal.Refused(c) == nil {
			t.Error("Refused = nil; these always refuse, they only choose the sentence")
		}
		return c.NoContent(200)
	})

	serve(t, validated(nil), func(c *zip.Ctx) error {
		if got := principal.Refusal(c); got != "an org scope is required" {
			t.Errorf("Refusal(validated) = %q — an attested caller lacking a scope is told THAT, "+
				"not to sign in again", got)
		}
		if principal.Refused(c) == nil {
			t.Error("Refused = nil for a validated caller; the refusal still stands, the wording differs")
		}
		return c.NoContent(200)
	})
}

// ── the wallet: who pays, and the rules that decide it ──────────────────────

// WalletFor addresses a wallet with no request behind it, so it cannot ask the
// credential rules a request-borne payer goes through. Its three refusals and its
// one carve-out are therefore the whole contract, and each is here.
func TestWalletFor_TheRulesThatDecideAnAddress(t *testing.T) {
	t.Run("no org is no wallet", func(t *testing.T) {
		if _, ok := principal.WalletFor("", "alice"); ok {
			t.Error("WalletFor with a blank org resolved a wallet; there is no ledger to key")
		}
		if _, ok := principal.WalletFor("   ", "alice"); ok {
			t.Error("a whitespace org is a blank org")
		}
	})

	// A slash is the separator in <owner>/<name>, so a name carrying one could
	// address a wallet in an org the caller did not name. It is refused rather
	// than escaped, which is the only version of this that cannot be got wrong.
	t.Run("a slash in the name is refused, not escaped", func(t *testing.T) {
		for _, name := range []string{"acme/bob", "/bob", "bob/", "a/b/c"} {
			if _, ok := principal.WalletFor("hanzo", name); ok {
				t.Errorf("WalletFor(hanzo, %q) resolved — a name may not address another org", name)
			}
		}
	})

	// THE BLANK NAME IS ADDRESSED, NOT INFERRED: with no name this deliberately
	// means the ORG's own account, which is how the platform pool is credited.
	// Routing it through the credential rule would make funding that pool
	// impossible in the one org that holds it.
	t.Run("a blank name addresses the org account", func(t *testing.T) {
		w, ok := principal.WalletFor("hanzo", "")
		if !ok {
			t.Fatal("WalletFor(hanzo, \"\") = !ok — the org's own account must be addressable")
		}
		if w.Ledger == "" || w.Account == "" {
			t.Errorf("wallet = %+v; both halves come from the resolved account", w)
		}
	})

	// Both halves come from the RESOLVED account, so the ledger is the canonical
	// folded org rather than the raw string — a caller spelling the org with
	// different case or padding must not open a second ledger beside the first.
	t.Run("the ledger is canonical, not the string handed in", func(t *testing.T) {
		plain, ok1 := principal.WalletFor("hanzo", "")
		padded, ok2 := principal.WalletFor("  hanzo  ", "")
		if !ok1 || !ok2 {
			t.Fatalf("both spellings must resolve: %v %v", ok1, ok2)
		}
		if plain != padded {
			t.Errorf("padding opened a second wallet: %+v vs %+v", plain, padded)
		}
	})
}

// Payer is TOTAL where WalletOf is partial: it answers the empty string rather
// than a second ok, because a caller hands it straight to a gate and wants the
// gate's own fail-closed refusal (ErrNoLedger) instead of branching itself.
func TestPayer_IsEmptyRatherThanASecondBranch(t *testing.T) {
	// No validated principal: a ledger must never be keyed on a restored,
	// client-stated org, or an anonymous caller could probe a victim's balance.
	serve(t, map[string]string{"X-Org-Id": "victim"}, func(c *zip.Ctx) error {
		if got := principal.Payer(c); got != "" {
			t.Errorf("Payer = %q for an unvalidated caller — that keys a ledger on a forgeable header", got)
		}
		return c.NoContent(200)
	})

	serve(t, validated(map[string]string{"X-User-Name": "alice"}), func(c *zip.Ctx) error {
		if got := principal.Payer(c); got == "" {
			t.Error("Payer = \"\" for a validated caller with an org and a name")
		}
		return c.NoContent(200)
	})
}

// PayerFrom asks the same two questions off the HTTP path, through the same
// functions. A context with no caller behind it has no org and no user, so it
// resolves nobody — the plane's form of "unvalidated".
func TestPayerFrom_NoCallerIsNoPayer(t *testing.T) {
	if got := principal.PayerFrom(context.Background()); got != "" {
		t.Errorf("PayerFrom(bare ctx) = %q want \"\" — no caller is no payer", got)
	}
}

// TestProjectScopeFromIsProjectScopeAcrossTheClient is the reason that function
// exists rather than each plane applying its own default test. The storage key a
// typed op derives and the one a request-side caller derives must be the same
// key, or the same org's data lands in two places depending on which endpoint it
// came through.
func TestProjectScopeFromIsProjectScopeAcrossTheClient(t *testing.T) {
	for header, want := range map[string]string{
		"":        "", // names no project: the whole-org view
		"default": "", // the literal default IS that same scope
		"alpha":   "alpha",
	} {
		headers := map[string]string{}
		if header != "" {
			headers["X-Project-Id"] = header
		}
		serve(t, headers, func(c *zip.Ctx) error {
			if got := principal.ProjectScope(c); got != want {
				t.Errorf("ProjectScope(%q) = %q, want %q", header, got, want)
			}
			ctx := principal.WithProject(context.Background(), c)
			if got := principal.ProjectScopeFrom(ctx); got != want {
				t.Errorf("ProjectScopeFrom(%q) = %q, want %q", header, got, want)
			}
			return nil
		})
	}
}

// TestProjectScopeFromWithoutARequestNarrowsNothing pins the off-HTTP answer. A
// context with no request behind it must resolve to the whole-org view rather
// than to some project nobody selected.
func TestProjectScopeFromWithoutARequestNarrowsNothing(t *testing.T) {
	if got := principal.ProjectScopeFrom(context.Background()); got != "" {
		t.Errorf("a bare context resolved to project %q; it selects no database", got)
	}
}
