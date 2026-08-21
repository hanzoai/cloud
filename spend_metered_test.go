package cloud

import (
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/hanzoai/cloud/manifest"
)

// TestMeteredSurfacesRequireStanding is the drift check spend.go has cited since the
// day meteredTrees was written — "TestMeteredSurfacesRequireStanding fails if the two
// drift" — for a test that did not exist. Three comments in this repo have now been
// caught describing a gate nobody built, which is worse than no gate: it reads as
// enforced, so nobody looks. It did drift, in six ways, and every one of them was a
// surface that charges a customer and could not be gated:
//
//	provisioning  routed at /v1/{datastore,docdb,kv,search,sql,vector} — the list
//	              said "/v1/provisioning/", a path nothing answers.
//	projects      answers /v1/sites; only /v1/projects was listed.
//	venue         answers /v1/cloud; absent entirely.
//	tools         answers /v1/skills, /v1/plugins, /v1/mcp/servers beside /v1/tools.
//	ask auto automations content flow platform todo translate — missing outright.
//
// Price is a SOURCE fact: each plugin/<name>/main.go is its own composition root, so
// there is no list to walk at runtime. This reads the source, exactly as
// TestPriceDeclared does — the same walk, asking the next question.
func TestMeteredSurfacesRequireStanding(t *testing.T) {
	metered := meteredSurfaces(t)
	if len(metered) == 0 {
		t.Fatal("found no Price: cloud.Metered composition root — the pattern this test " +
			"recognises has moved, so it is asserting nothing")
	}

	declared := map[string]bool{}
	for _, name := range meteredApps {
		declared[name] = true
	}

	for _, name := range metered {
		if !declared[name] {
			t.Errorf("plugin/%s declares Price: cloud.Metered but is missing from meteredApps.\n"+
				"Money moves inside its handlers, so SpendGate must require standing before "+
				"they run — without the entry the surface is free the moment enforcement is "+
				"switched on. Add %q to meteredApps in spend.go.", name, name)
			continue
		}
		// The name is only half the fact. Assert the surface is actually reachable as
		// billable through the SAME resolution Billable performs, so a manifest row whose
		// prefixes move re-fails here rather than silently narrowing coverage.
		for _, prefix := range append([]string{"/v1/" + name}, manifest.PrefixesFor(name)...) {
			if prefix == "/v1" {
				continue // ai's terminal catch-all — see meteredPrefixes.
			}
			path := prefix + "/probe"
			if Reachable(path) {
				continue // the pay path is never gated, whatever declares it.
			}
			if !Billable(http.MethodPost, path) {
				t.Errorf("%s: POST %s is not Billable, but %s declares cloud.Metered.\n"+
					"meteredTrees is resolved from meteredApps through the manifest; if this "+
					"fails the two no longer agree about which paths the surface answers.",
					name, path, name)
			}
		}
	}

	// The reverse drift: an entry naming a surface that is no longer Metered would keep
	// gating a path nobody charges for, which is the outage half of the asymmetry.
	live := map[string]bool{}
	for _, name := range metered {
		live[name] = true
	}
	for _, name := range meteredApps {
		if !live[name] {
			t.Errorf("meteredApps lists %q, but plugin/%s does not declare Price: cloud.Metered.\n"+
				"Requiring standing for a surface nobody charges 402s a customer for free work.",
				name, name)
		}
	}
	t.Logf("%d metered surfaces resolve to %d billable trees", len(metered), len(meteredTrees))
}

// meteredSurfaces reads the app name out of every plugin/<name>/main.go that declares
// Price: cloud.Metered. The name comes from the DIRECTORY, which is the same key
// manifest.Apps and MountPrefixes use — gen-app-cmds already pins that a plugin
// directory and a manifest row are one-to-one.
func meteredSurfaces(t *testing.T) []string {
	t.Helper()
	roots, err := filepath.Glob(filepath.Join("plugin", "*", "main.go"))
	if err != nil {
		t.Fatalf("glob composition roots: %v", err)
	}
	if len(roots) == 0 {
		t.Fatal("no plugin/*/main.go found — this test is walking the wrong tree")
	}

	var out []string
	for _, root := range roots {
		src, err := os.ReadFile(root)
		if err != nil {
			t.Fatalf("read %s: %v", root, err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), root, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", root, err)
		}
		if pluginIsMetered(file) {
			out = append(out, filepath.Base(filepath.Dir(root)))
		}
	}
	return out
}

// pluginIsMetered reports whether a composition root declares Price: cloud.Metered.
// It matches the same []cloud.Plugin{{…}} literal TestPriceDeclared walks — the
// elements are implicit, so the slice type is what names cloud.Plugin.
func pluginIsMetered(file *ast.File) bool {
	found := false
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
				key, ok := kv.Key.(*ast.Ident)
				if !ok || key.Name != "Price" {
					continue
				}
				if sel, ok := kv.Value.(*ast.SelectorExpr); ok && sel.Sel.Name == "Metered" {
					found = true
				}
			}
		}
		return true
	})
	return found
}

// A Free app's surface is not billable just because a metered app is routed a
// SHORTER prefix over the same tree.
//
// provisioning is metered and routed /v1/vector and /v1/search/query; product is
// Free and routed the more specific /v1/vector/collections, /v1/vector/stats,
// /v1/search/indexes and /v1/search/stats. A bare HasPrefix scan bills all four
// on provisioning's standing, which gates a Free product behind a balance. The
// router resolves by longest prefix; ownership has to as well.
func TestFreeSurfaceUnderAMeteredPrefixIsNotBillable(t *testing.T) {
	for _, p := range []string{
		"/v1/vector/collections",
		"/v1/vector/stats",
		"/v1/search/indexes",
		"/v1/search/stats",
	} {
		if Billable("POST", p) {
			t.Errorf("Billable(POST %s) = true; product declares cloud.Free and owns this path", p)
		}
	}

	// The metered tree around them still bills — this must not become a hole.
	for _, p := range []string{"/v1/vector", "/v1/vector/anything-else", "/v1/search/query"} {
		if !Billable("POST", p) {
			t.Errorf("Billable(POST %s) = false; provisioning is metered and owns this path", p)
		}
	}
}

// OwnerOf resolves by LONGEST prefix, which is what makes the check above sound.
func TestOwnerOfPrefersTheMoreSpecificApp(t *testing.T) {
	if got := manifest.OwnerOf("/v1/vector/collections"); got != "product" {
		t.Errorf("OwnerOf(/v1/vector/collections) = %q, want product", got)
	}
	if got := manifest.OwnerOf("/v1/vector"); got != "provisioning" {
		t.Errorf("OwnerOf(/v1/vector) = %q, want provisioning", got)
	}
	// ai is declared the bare /v1 catch-all, so an otherwise-unmatched /v1 path
	// legitimately resolves to it — that IS the routing, not a miss. Only a path
	// outside every declared prefix is unowned.
	if got := manifest.OwnerOf("/v1/nothing-routes-here"); got != "ai" {
		t.Errorf("OwnerOf(unmatched /v1) = %q, want ai (the catch-all)", got)
	}
	// A parameterized prefix owns every spelling of its address: the manifest
	// says :org, a document says {org}, a request carries the slug itself.
	for _, p := range []string{
		"/v1/orgs/{org}/entitlements",
		"/v1/orgs/acme/entitlements",
		"/v1/orgs/acme/entitlements/anything-below",
	} {
		if got := manifest.OwnerOf(p); got != "entitlements" {
			t.Errorf("OwnerOf(%s) = %q, want entitlements (the :org row)", p, got)
		}
	}
	if got := manifest.OwnerOf("/healthz"); got != "" {
		t.Errorf("OwnerOf(outside every prefix) = %q, want empty", got)
	}
}

// ── the next question: does a Metered surface hold a meter at all? ───────────────

// TestMeteredSurfacesHoldAMeter asks the thing every check above assumes and none of
// them verify. cloud.Metered means ONE thing — "the charge for this surface is owned
// by a meter DOWNSTREAM of the edge" (price.go) — and the edge charges nothing on that
// promise. Three surfaces made the promise and kept no meter, so every request to them
// was free in a way no test could see: the price was declared, the standing was
// required, the meter did not exist.
//
// It is the same walk as the two tests above, asking the next question, and it reads
// SOURCE for the same reason they do: a composition root is a source fact.
//
// The two lists below are the honest coverage report, in code. A surface may only be
// absent from its own package's meter by being NAMED in one of them, with the reason —
// so the gap is a line somebody has to write, not an absence nobody can see.
func TestMeteredSurfacesHoldAMeter(t *testing.T) {
	metered := meteredSurfaces(t)
	if len(metered) == 0 {
		t.Fatal("found no Price: cloud.Metered composition root — asserting nothing")
	}

	var held int
	for _, name := range metered {
		// The packages the ROOT mounts, not apps/<name> — see appDirs. Assuming the
		// two names match reported apps/sandboxes, which does not exist, as a surface
		// holding no meter while apps/sandbox held one all along.
		var charges, zeroed, priced bool
		for _, dir := range appDirs(t, filepath.Join("plugin", name, "main.go")) {
			if c, z, pr := packagePrice(t, dir); c {
				charges, zeroed, priced = true, z, pr
				break
			}
		}
		switch {
		case charges:
			held++
			// A meter that resolves every fee to a zero default charges nothing until
			// somebody types a number — wired, gated, and free. That is a legitimate
			// posture, but it must be a SENTENCE somebody wrote, not a default nobody
			// noticed. See pricedAtZero.
			if !priced && pricedAtZero[name] == "" {
				t.Errorf("plugin/%s declares Price: cloud.Metered and its meter resolves every fee "+
					"to a default of 0 — it is wired, it is gated, and it posts nothing.\n"+
					"Give the fee a non-zero default (cloud.ResourceFeeCents, or a number of "+
					"your own), or name %q in pricedAtZero with the reason the surface is "+
					"deliberately free today.", name, name)
			}
			if zeroed {
				t.Errorf("apps/%s: every Meter call passes a literal 0 — the seam is wired and "+
					"records nothing.\nA debit of zero posts no ledger entry, so the surface "+
					"is free while reading as metered. Charge the fee the deployment "+
					"configures (cloud.ResourceFeeCents), or declare the surface Free.", name)
			}
		case meteredByAIWrapper[name]:
			// Correct, and deliberately not its own meter: these surfaces spend on
			// INFERENCE, and inference is metered once, where the tokens are counted —
			// build.go wraps Deps.AI/Deps.Embed in meteredAIClient. A second meter here
			// would bill the same tokens twice.
		case meteredWithoutAMeter[name]:
			t.Logf("apps/%s: declared Metered, charges nothing — known gap", name)
		default:
			t.Errorf("apps/%s declares Price: cloud.Metered but its package calls no meter "+
				"(Gate/Meter/MeterUsage/RecordUsage).\nMetered means a meter downstream of "+
				"the edge owns the charge, and the edge charges nothing on that promise — so "+
				"with no meter the surface is silently free. Wire the meter, or name it in "+
				"meteredWithoutAMeter with the reason.", name)
		}
	}

	// CONTROL. A matcher that stops recognising a call would turn every case above
	// into the "named" branch and pass. Pin the floor we measured.
	if held < 20 {
		t.Fatalf("only %d metered surfaces were seen to hold a meter; 20 do. The call "+
			"matcher has moved and this test is asserting nothing", held)
	}
	t.Logf("%d of %d metered surfaces hold their own meter; %d meter through the AI wrapper; "+
		"%d charge nothing", held, len(metered), len(meteredByAIWrapper), len(meteredWithoutAMeter))
}

// pricedAtZero names the metered surfaces whose meter is fully wired and whose
// price is deliberately zero, with the reason. The meter is real — the gate runs,
// the debit path exists — so Metered is the honest declaration; only the NUMBER is
// zero, and turning it on is one environment variable.
//
// It is separate from meteredWithoutAMeter, and the difference is the whole point:
// that list is a surface with no meter at all (a gap), this one is a surface with a
// meter and a policy. A reader who cannot tell them apart cannot tell a decision
// from an oversight.
var pricedAtZero = map[string]string{
	// Every sandbox class has been free since sandboxes existed, and shipping the
	// meter must not also ship a price — the first anybody would hear of it is a 402
	// on a button that worked yesterday. apps/sandbox/api.go's ResourceFee documents
	// this at the knob (SANDBOX_FEE_CENTS[_EXEC|_DEV|_DESKTOP]) and a test pins it.
	// Pricing it is a product decision, and a separate one from declaring it.
	"sandboxes": "every class free since inception; SANDBOX_FEE_CENTS turns it on — a values change, not a wiring gap",
}

// meteredByAIWrapper are the surfaces whose spend is INFERENCE, metered once by the
// wrapped AI client build.go installs (meteredAIClient → metered_ai.go), not by a
// meter of their own. A second meter would double-bill the same tokens.
var meteredByAIWrapper = map[string]bool{
	"ai":    true, // the completions surface itself.
	"ask":   true, // holds deps.AI and answers questions with it.
	"agent": true, // replays /v1/chat/completions in-process (aiCompleter).
}

// meteredWithoutAMeter is THE GAP, named so it is countable. Each entry declares
// cloud.Metered — the platform requires standing before its handlers run — and then
// charges nothing at all. They are not free by decision; nobody has priced them. The
// list must only ever shrink.
//
// It was EMPTY, and todo is the one entry. auto and flow were the last two to
// leave — both are typed passthroughs whose RUN op schedules real compute, so each
// grew the meter its declaration had been claiming (apps/auto/billing.go,
// apps/flow/billing.go: gate before the upstream call, debit after it succeeds, one
// ResourceFeeCents knob read by both).
//
// todo's board moved to the forge, and its meter did not move with it. The
// Bill.Gate/Bill.Meter pair lived in the store-backed ops (updateProject, listIssues,
// getIssue and their siblings), which lost their routes in that move and kept their
// code — so the meter that satisfied this test sat behind addresses nobody served,
// and the live surface (forgeCreateIssue, forgePatchIssue, claimIssue) has charged
// nothing since. Deleting the unrouted half made the gap visible rather than causing
// it. Pricing a write that lands in git.hanzo.ai rather than in a local store is the
// open question, and it is a pricing decision, not a typing one.
var meteredWithoutAMeter = map[string]bool{
	"todo": true,
}

// packageCharges reports whether dir's non-test sources call a meter, and whether
// EVERY positional Meter call in them passes a literal zero amount (a wired seam that
// records nothing — apps/security shipped exactly that).
func packageCharges(t *testing.T, dir string) (charges, allZero bool) {
	c, z, _ := packagePrice(t, dir)
	return c, z
}

// packagePrice is packageCharges plus the question a wired meter cannot answer on
// its own: does it charge a NUMBER?
//
// A meter priced at zero posts no ledger entry, so the surface is free while every
// structural check reads it as metered — the seam is wired, the standing is
// required, and the debit is a no-op. plugin/sandboxes passed this test that way
// for its whole life. The evidence is the DEFAULT a fee resolves to:
// cloud.FeeCents(env, kind, 0) is a surface that charges nothing unless an operator
// types a number, and cloud.ResourceFeeCents (or any non-zero default) is one that
// charges out of the box.
//
// priced is false only when the package resolves fees AND every one of them
// defaults to zero. A package that resolves none is not making a claim either way —
// its amount comes from somewhere this walk cannot see — so it is left alone.
func packagePrice(t *testing.T, dir string) (charges, allZero, priced bool) {
	fees, zeroFees := 0, 0
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	// Sub-packages hold handlers too (apps/<name>/<sub>/…), so walk the whole tree.
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && filepath.Ext(p) == ".go" && filepath.Dir(p) != dir {
			files = append(files, p)
		}
		return nil
	})

	meters, zeros := 0, 0
	for _, f := range files {
		if len(f) > 8 && f[len(f)-8:] == "_test.go" {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, src, 0)
		if err != nil {
			continue // generated or build-tagged oddity; the other files answer.
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			// WHAT IT CHARGES, asked before WHETHER it charges — the switch below
			// returns early on every name it does not recognise, and a fee resolver is
			// one of those. Counted here so a zero default is visible at all.
			//
			// FeeCents(envPrefix, kind, def): a literal 0 default is a surface that
			// charges nothing until an operator prices it. ResourceFeeCents carries the
			// platform's $1.00 default, so it is always priced.
			if sel.Sel.Name == "FeeCents" && len(call.Args) == 3 {
				fees++
				if lit, ok := call.Args[2].(*ast.BasicLit); ok && lit.Kind == token.INT && lit.Value == "0" {
					zeroFees++
				}
			}
			if sel.Sel.Name == "ResourceFeeCents" {
				fees++
			}
			switch sel.Sel.Name {
			// Gate/Meter/MeterUsage are ResourceMeter's names, and Allow/Debit are the
			// same two asked with a resolved cloud.Payer instead of loose strings (see
			// payer.go) — a surface that composes them is metered exactly as much as one
			// that does not. Authorize/Record are the metering client's own, which zen
			// calls directly because it supplies the upstream module's Gate/Meter as
			// function values rather than calling ours.
			case "Gate", "Allow", "Meter", "MeterUsage", "Debit", "RecordUsage", "Authorize", "Record":
				charges = true
			default:
				return true
			}
			// Meter(org, project, kind, amountCents, requestID, clientIP): a literal 0
			// in the amount slot posts nothing.
			if sel.Sel.Name == "Meter" && len(call.Args) == 6 {
				meters++
				if lit, ok := call.Args[3].(*ast.BasicLit); ok && lit.Kind == token.INT && lit.Value == "0" {
					zeros++
				}
			}
			return true
		})
	}
	return charges, meters > 0 && meters == zeros, !(fees > 0 && zeroFees == fees)
}
