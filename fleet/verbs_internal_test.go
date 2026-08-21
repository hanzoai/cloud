// Copyright © 2026 Hanzo AI. MIT License.

package fleet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The naming, against the fleet's own operations — and against the gate, which
// is the thing renaming could break and must not.

// readings is what a route MEANS, one real operation at a time.
//
// Every id here is declared by a subsystem in plugin/*/openapi.json, and the
// route is beside it, because the claim is that the phrase says what the route
// says. TestAPhraseSaysWhatTheRouteSays checks that the ids still exist, so a
// route that is renamed upstream turns this red instead of quietly testing a
// fleet that has moved on.
var readings = []struct{ id, want, route string }{
	// The shape that made the surface unreadable, whole.
	{"get_projects", "list_projects", "GET /v1/projects"},
	{"post_projects", "create_project", "POST /v1/projects"},
	{"get_projects_by_slug", "get_project", "GET /v1/projects/{slug}"},
	{"patch_projects_by_slug", "update_project", "PATCH /v1/projects/{slug}"},
	{"delete_projects_by_slug", "delete_project", "DELETE /v1/projects/{slug}"},
	{"get_projects_by_slug_deployments", "list_project_deployments", "GET /v1/projects/{slug}/deployments"},
	{"get_projects_by_slug_deployments_by_id", "get_project_deployment", "GET /v1/projects/{slug}/deployments/{id}"},
	{"delete_projects_by_slug_domains_by_host", "delete_project_domain", "DELETE /v1/projects/{slug}/domains/{host}"},

	// .../{id}/ACTION — the author wrote the verb, so it leads.
	{"post_projects_by_slug_deploy", "deploy_project", "POST /v1/projects/{slug}/deploy"},
	{"post_projects_by_slug_purge", "purge_project", "POST /v1/projects/{slug}/purge"},
	{"post_projects_by_slug_domains_by_host_verify", "verify_project_domain", "POST /v1/projects/{slug}/domains/{host}/verify"},
	{"post_projects_by_slug_deployments_by_id_complete", "complete_project_deployment", "POST /v1/projects/{slug}/deployments/{id}/complete"},
	{"post_projects_by_slug_releases_by_release_activate", "activate_project_release", "POST /v1/projects/{slug}/releases/{release}/activate"},

	// A singular segment is only an action when a parameter put it after a ROW.
	// `/v1/commerce/product` is a collection someone spelled singular, and reading
	// it as a verb produced `product_commerce` — for four methods at once.
	{"post_commerce_product", "create_commerce_product", "POST /v1/commerce/product"},
	{"patch_commerce_product_by_productid", "update_commerce_product", "PATCH /v1/commerce/product/{productid}"},
	{"delete_commerce_product_by_productid", "delete_commerce_product", "DELETE /v1/commerce/product/{productid}"},
	// …nor when the row comes AFTER it: `listing` is what {key} indexes into.
	{"put_commerce_store_by_storeid_listing_by_key", "set_commerce_store_listing", "PUT /v1/commerce/store/{storeid}/listing/{key}"},
	{"post_commerce_store_by_storeid_listing_by_key", "create_commerce_store_listing", "POST /v1/commerce/store/{storeid}/listing/{key}"},
	{"post_sandbox_by_id_exec", "exec_sandbox", "POST /v1/sandbox/{id}/exec — the action shape again"},

	// The inference surface.
	{"post_chat_completions", "create_chat_completion", "POST /v1/chat/completions"},
	{"get_models", "list_models", "GET /v1/models"},
	{"post_embeddings", "create_embedding", "POST /v1/embeddings"},
	{"post_rerank", "create_rerank", "POST /v1/rerank — one singular segment is the thing, not a verb"},

	// Spelling that a naive plural rule gets wrong in both directions.
	{"get_sandbox", "list_sandboxes", "GET /v1/sandbox — a singular address, and the list phrase is still plural"},
	{"get_sandbox_by_id", "get_sandbox", "GET /v1/sandbox/{id} — the member reads as the bare noun"},
	{"get_projects_sites", "list_project_sites", "GET /v1/projects/sites — the collection under its owner"},
	{"post_projects_by_slug_releases", "create_project_release", "POST /v1/projects/{slug}/releases — `releases` loses only one"},

	// A DECLARED id is already a verb on an object and is left alone.
	{"GetUserPreference", "GetUserPreference", "o11y declares its own ids"},
	{"ListTraceFunnels", "ListTraceFunnels", "…and they are not routes to read back"},
}

// declaredInSpecs is every operation id the fleet SERVES, read from the per-app
// documents.
//
// The anchor below asks whether a table entry still names a real route, and the
// catalog is the wrong place to ask: it carries the DISPATCHABLE subset (x-tool),
// so an operation that is served and merely untyped is absent from it. Reading it
// there conflated two independent questions and failed a NAMING test for a
// dispatchability reason — thirteen entries at once, including the whole
// inference surface, none of which had been renamed. The comment on `readings`
// already named this set; this is the code catching up to it.
func declaredInSpecs(t *testing.T) map[string]bool {
	t.Helper()
	specs, err := filepath.Glob(filepath.Join("..", "plugin", "*", "openapi.json"))
	if err != nil || len(specs) == 0 {
		t.Fatalf("no per-app documents to anchor the table against (%v)", err)
	}
	ids := map[string]bool{}
	for _, path := range specs {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc struct {
			Paths map[string]map[string]struct {
				OperationID string `json:"operationId"`
			} `json:"paths"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, methods := range doc.Paths {
			for method, op := range methods {
				if method != "parameters" && op.OperationID != "" {
					ids[op.OperationID] = true
				}
			}
		}
	}
	return ids
}

func TestAPhraseSaysWhatTheRouteSays(t *testing.T) {
	declared := declaredInSpecs(t)
	for _, r := range readings {
		if got := phrase(r.id); got != r.want {
			t.Errorf("%s\n  %s\n  reads as %q, want %q", r.route, r.id, got, r.want)
		}
		if !declared[r.id] {
			t.Errorf("%s is in this table and no subsystem declares it — the route moved, or the id did", r.id)
		}
	}
}

// TestEveryPublishedNameMeansExactlyOneOperation is the property a tools/call
// depends on: the door publishes a name, a client sends that name back, and the
// door must know which operation it meant. Over the whole corpus, at once,
// because that is the set one gather holds.
func TestEveryPublishedNameMeansExactlyOneOperation(t *testing.T) {
	all := gathered(t)
	offer(all)

	means := map[string]string{}
	kept := 0
	for _, tl := range all {
		if was, dup := means[tl.as]; dup {
			t.Fatalf("the door would publish %q for BOTH %s and %s — a call naming it is a coin toss", tl.as, was, tl.name)
		}
		means[tl.as] = tl.name
		if tl.as == tl.name {
			kept++
		}
	}
	// Reversible the other way too: nothing is published under a name that is
	// some OTHER operation's own id, which a describe of the child's own bytes
	// would otherwise send a model straight at.
	ids := map[string]string{}
	for _, tl := range all {
		ids[tl.name] = tl.name
	}
	for _, tl := range all {
		if id, taken := ids[tl.as]; taken && id != tl.name {
			t.Fatalf("%s is published as %q, which is %s's own id", tl.name, tl.as, id)
		}
	}
	// And a published name can never be mistaken for one of the door's own tools:
	// a phrase always carries a verb and an object, no app name in the manifest
	// has an underscore, and [Describe] has none either.
	for _, tl := range all {
		if !strings.Contains(tl.as, "_") && tl.as != tl.name {
			t.Errorf("%s is published as the bare word %q, which could shadow a subsystem tool", tl.name, tl.as)
		}
	}

	// Two different reasons an operation keeps its id, and only one of them is a
	// cost: a DECLARED id was already a verb on an object and was never a
	// candidate, while an AMBIGUOUS one is a phrase this refused to publish.
	declared, ambiguous := 0, 0
	for _, tl := range all {
		if tl.as != tl.name {
			continue
		}
		if _, _, _, isRoute := route(tl.name); isRoute {
			ambiguous++
			continue
		}
		declared++
	}
	t.Logf("MEASURED — %d operations survive the gate:", len(all))
	t.Logf("  %4d read back as a verb on an object", len(all)-kept)
	t.Logf("  %4d already were one — a subsystem declared its own id, and it is left alone", declared)
	t.Logf("  %4d keep a route for a name (%.1f%%): the phrase would have been ambiguous, so it is not used.",
		ambiguous, 100*float64(ambiguous)/float64(len(all)))
	t.Logf("       These are overwhelmingly the fleet's own duplicates — one handler at /tasks and")
	t.Logf("       /v1/tasks, or post_agent in one subsystem beside post_agents in another.")
}

// TestTheGateStillJudgesTheROUTE is the security bar for this change, and it is
// one claim: refuse() reads what the CHILD called a tool, and renaming happens
// after it. So the refused set cannot have moved.
//
// It is checked rather than asserted from the code's shape, because the failure
// it guards against is silent: an operation whose route says `post_iam_users`
// reads back as `create_iam_user`, which still trips clause 2 — but
// `delete_keys` reads back as `delete_key`, and a rule applied to THAT would
// have to re-derive a decision it has already made correctly once.
func TestTheGateStillJudgesTheROUTE(t *testing.T) {
	var held, offered []string
	for _, op := range Corpus(t) {
		if refuse(op.ID) {
			held = append(held, op.ID)
			continue
		}
		offered = append(offered, op.ID)
	}
	sort.Strings(held)

	// 1. Nothing refused is reachable under any name the door would publish.
	all := gathered(t)
	offer(all)
	withheld := map[string]bool{}
	for _, id := range held {
		withheld[id] = true
	}
	for _, tl := range all {
		if withheld[tl.name] {
			t.Fatalf("%s is refused and the door still holds it", tl.name)
		}
		if withheld[tl.as] {
			t.Fatalf("%s is published as %q, which is a REFUSED operation's id — naming it would reach the survivor "+
				"and a reader would think the refused one is offered", tl.name, tl.as)
		}
	}
	// 2. The set that survives is exactly the set the gate lets through: naming
	//    neither added an operation nor lost one.
	if len(all) != len(offered) {
		t.Fatalf("the gate passes %d operations and the door holds %d", len(offered), len(all))
	}

	t.Logf("MEASURED — over %d declared operations: %d refused, %d offered", len(held)+len(offered), len(held), len(offered))
	t.Logf("  the gate's input is the child's own id, before naming; see fleet/verbs.go.")
	t.Logf("  first refusals, in order: %s", strings.Join(held[:min(6, len(held))], " "))
}

// TestProseIsRationedToTheProductSurface is the OTHER half of the change and the
// one with a budget. It prints what prose costs so the ration is a decision
// somebody made on a number rather than a taste.
func TestProseIsRationedToTheProductSurface(t *testing.T) {
	all := gathered(t)
	offer(all)

	var routes, names, ranked, everything int
	described := 0
	for _, tl := range all {
		r, _ := json.Marshal(tl.name)
		n, _ := json.Marshal(tl.as)
		s, _ := json.Marshal(tl.as + " — " + summary(tl.desc))
		routes += len(r)
		names += len(n)
		everything += len(s)
		if rank(tl.name) < len(productStems) {
			ranked += len(s)
			described++
			continue
		}
		ranked += len(n)
	}
	if ranked >= everything {
		t.Fatalf("rationing prose to the product surface costs %d bytes and giving it to everything costs %d", ranked, everything)
	}

	t.Logf("MEASURED — the enum's own bytes over %d offered operations:", len(all))
	t.Logf("  routes, as it shipped         %7d   ← before", routes)
	t.Logf("  verb phrases                  %7d   (%+d: a phrase drops the method, the version and every", names, names-routes)
	t.Logf("                                          parameter, so the new surface is SHORTER than the old one)")
	t.Logf("  + a summary on the %3d ranked  %7d   (%+d against the routes, %.2fx)  ← shipped",
		described, ranked, ranked-routes, float64(ranked)/float64(routes))
	t.Logf("  + a summary on ALL of them    %7d   (%+d, %.1fx)", everything, everything-routes, float64(everything)/float64(routes))
	t.Logf("  The whole grouped tools/list was 71 KB. Five times the enum is not a surface that fits in a")
	t.Logf("  head, so prose stops at the product surface and [Describe] answers for the tail.")
}

// TestASummaryIsOneSentenceAndFits: the descriptors carry whole doc comments,
// and an enum carries one line of one.
func TestASummaryIsOneSentenceAndFits(t *testing.T) {
	for _, c := range []struct{ doc, want string }{
		{"Returns every project your org owns.\n\nEach row carries the slug, name,\nframework and status.",
			"Returns every project your org owns."},
		{"Returns one project of yours by slug — its settings, its live URL\nand the deployment currently serving it.",
			"Returns one project of yours by slug — its settings, its live URL and the deployment currently serving it."},
		{"", ""},
		{"   \n\n  ", ""},
	} {
		if got := summary(c.doc); got != c.want {
			t.Errorf("summary(%q)\n  = %q\n want %q", c.doc, got, c.want)
		}
	}
	over := 0
	for _, op := range Corpus(t) {
		if s := summary(op.Doc); len([]rune(s)) > summaryMax+1 { // +1 for the ellipsis
			over++
			if over < 3 {
				t.Errorf("%s summarises to %d characters: %q", op.ID, len([]rune(s)), s)
			}
		}
	}
	if over > 0 {
		t.Errorf("%d summaries are longer than %d characters", over, summaryMax)
	}
}

// Phrase is [phrase] for the wire tests, which run in package fleet_test and
// need to know what the door will call an operation before they can assert that
// it published it. Exported here rather than duplicated there for the same
// reason [Corpus] is: two readings of one rule is one reading too many.
func Phrase(op string) string { return phrase(op) }

// gathered is the corpus as [Door.gather] would hold it: refused operations
// dropped, one owner per name, sorted by [rank] then name. Everything this file
// asserts is asserted against THAT set, because it is the set the door names.
func gathered(t *testing.T) []named {
	t.Helper()
	var all []named
	owner := map[string]bool{}
	for _, op := range Corpus(t) {
		if refuse(op.ID) || owner[op.ID] {
			continue
		}
		owner[op.ID] = true
		all = append(all, named{app: op.App, name: op.ID, desc: op.Doc})
	}
	sort.Slice(all, func(i, j int) bool {
		if ri, rj := rank(all[i].name), rank(all[j].name); ri != rj {
			return ri < rj
		}
		return all[i].name < all[j].name
	})
	return all
}
