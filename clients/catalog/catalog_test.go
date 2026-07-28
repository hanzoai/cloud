package catalog

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/index"
	sqlitedrv "github.com/hanzoai/sqlite"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// Same build-tag-agnostic harness as the index suite it borrows the store from.
func TestMain(m *testing.M) {
	if sqlitedrv.EncryptionAvailable() && os.Getenv("CLOUD_KMS_MASTER_KEY_REF") == "" {
		_ = os.Setenv("CLOUD_KMS_MASTER_KEY_REF", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=")
	}
	os.Exit(m.Run())
}

// mount brings up the index (the store) and the catalog (the lens) on one app,
// exactly as apps.go orders them.
func mount(t *testing.T) *zip.App {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	deps := cloud.Deps{Logger: luxlog.New("test"), DataDir: t.TempDir()}
	if err := index.Mount(app, deps); err != nil {
		t.Fatalf("index.Mount: %v", err)
	}
	t.Cleanup(func() { _ = index.Shutdown() })
	if err := Mount(app, deps); err != nil {
		t.Fatalf("catalog.Mount: %v", err)
	}
	return app
}

// do issues one request. hdr carries the SANITIZED principal headers the gateway
// mints; a test that set them on a real request would be testing the forgery the
// identity middleware strips, not this surface.
func do(t *testing.T, app *zip.App, method, url, body string, hdr map[string]string) (int, string) {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, url, nil)
	} else {
		r = httptest.NewRequest(method, url, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(r)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}

func decode(t *testing.T, body string) Response {
	t.Helper()
	var out Response
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatalf("decode %q: %v", body, err)
	}
	return out
}

// as mints the SANITIZED principal headers for one org. A validated principal is
// an org AND a user — principal.Org refuses an X-Org-Id with no X-User-Id behind
// it, which is exactly the forgery the identity boundary exists to stop.
func as(org string) map[string]string {
	return map[string]string{"X-Org-Id": org, "X-User-Id": "u@" + org}
}

// seed publishes the cross-org corpus through the SAME reconcile the sync uses —
// there is no publish endpoint to call, which is the point.
func seed(t *testing.T, rows ...Entry) {
	t.Helper()
	if len(rows) == 0 {
		rows = []Entry{
			{ID: "hanzo/console", Org: "hanzo", Name: "console", Kind: "repo", Archetype: "app", Language: "TypeScript", Description: "the operator console", Forkable: true, Updated: "2026-07-01"},
			{ID: "lux/node", Org: "lux", Name: "node", Kind: "repo", Archetype: "infra", Language: "Go", Description: "lux blockchain node", Updated: "2026-07-02"},
			{ID: "zoo/zips", Org: "zoo", Name: "zips", Kind: "site", Archetype: "site", Language: "TypeScript", Description: "zoo improvement proposals", URL: "https://zips.zoo.ngo", Updated: "2026-07-03"},
		}
	}
	if _, _, err := reconcile(context.Background(), PublicOrg, rows); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

// TestCrossOrgSearch is the whole point: ONE query reaches hanzo, lux and zoo.
func TestCrossOrgSearch(t *testing.T) {
	app := mount(t)
	seed(t)

	code, body := do(t, app, http.MethodGet, "/v1/catalog", "", nil)
	if code != http.StatusOK {
		t.Fatalf("browse: %d %s", code, body)
	}
	got := decode(t, body)
	if got.Total != 3 {
		t.Fatalf("want 3 entries across orgs, got %d: %s", got.Total, body)
	}
	for _, org := range []string{"hanzo", "lux", "zoo"} {
		if got.Facets["org"][org] != 1 {
			t.Errorf("org facet missing %q: %v", org, got.Facets["org"])
		}
	}

	// Free text is the index's job, not ours.
	if got := decode(t, mustGet(t, app, "/v1/catalog?q=blockchain")); got.Total != 1 || got.Data[0].Org != "lux" {
		t.Errorf("q=blockchain should find only the lux node, got %+v", got.Data)
	}
	// The browse axes are exact-match.
	if got := decode(t, mustGet(t, app, "/v1/catalog?language=Go")); got.Total != 1 || got.Data[0].ID != "lux/node" {
		t.Errorf("language=Go should find only lux/node, got %+v", got.Data)
	}
	if got := decode(t, mustGet(t, app, "/v1/catalog?forkable=true")); got.Total != 1 || got.Data[0].ID != "hanzo/console" {
		t.Errorf("forkable=true should find only hanzo/console, got %+v", got.Data)
	}
	if got := decode(t, mustGet(t, app, "/v1/catalog?org=zoo&archetype=site")); got.Total != 1 {
		t.Errorf("org+archetype should compose, got %+v", got.Data)
	}
	// Every dimension the response FACETS is a dimension it filters.
	for dim, val := range map[string]string{"org": "lux", "kind": "repo", "archetype": "app", "language": "Go"} {
		got := decode(t, mustGet(t, app, "/v1/catalog?"+dim+"="+val))
		if got.Total == 0 || got.Total == 3 {
			t.Errorf("%s=%s did not filter (total %d of 3)", dim, val, got.Total)
		}
		if _, ok := got.Facets[dim]; !ok {
			t.Errorf("%s is filterable but not faceted", dim)
		}
	}
}

// TestForkableIsAskableBothWays is the wire half of gap (a). A boolean axis that
// can only ever narrow to "all of them" is a label; this pins that the negative
// case is expressible and that the facet reports BOTH sides, so a rail can show
// the split instead of a pill that filters nothing.
func TestForkableIsAskableBothWays(t *testing.T) {
	app := mount(t)
	seed(t) // console forkable, node and zips not

	all := decode(t, mustGet(t, app, "/v1/catalog"))
	if got := all.Facets["forkable"]; got["true"] != 1 || got["false"] != 2 {
		t.Fatalf("forkable facet must count both sides, got %v", got)
	}
	yes := decode(t, mustGet(t, app, "/v1/catalog?forkable=true"))
	no := decode(t, mustGet(t, app, "/v1/catalog?forkable=false"))
	if yes.Total != 1 || yes.Data[0].ID != "hanzo/console" {
		t.Errorf("forkable=true: %+v", yes.Data)
	}
	if no.Total != 2 {
		t.Errorf("forkable=false must select the complement, got %d: %+v", no.Total, no.Data)
	}
	if yes.Total+no.Total != all.Total {
		t.Errorf("the two sides must partition the corpus: %d + %d != %d", yes.Total, no.Total, all.Total)
	}
	for _, e := range no.Data {
		if e.Forkable {
			t.Errorf("%s answered the wrong side", e.ID)
		}
	}
	// false must reach the client as a value, not as an absent field: omitted, a
	// caller cannot tell "you cannot fork this" from "nobody said".
	if body := mustGet(t, app, "/v1/catalog?forkable=false"); !strings.Contains(body, `"forkable":false`) {
		t.Errorf("forkable:false must be on the wire: %s", body)
	}
}

// TestSourceSurvivesTheIndex is the wire half of gap (b): repo and template are
// not just built in sync.go, they round-trip the store and reach the caller.
func TestSourceSurvivesTheIndex(t *testing.T) {
	app := mount(t)
	seed(t, Entry{
		ID: "hanzo/kanban", Org: "hanzo", Name: "kanban", Kind: "site", Archetype: "site",
		URL: "https://kanban.hanzo.app", Repo: "https://github.com/hanzo-apps/kanban-lane",
		Template: "hanzo/example-kanban", Forkable: true, Updated: "2026-07-05",
	})
	got := decode(t, mustGet(t, app, "/v1/catalog?q=kanban"))
	if got.Total != 1 {
		t.Fatalf("want the one row, got %d", got.Total)
	}
	if e := got.Data[0]; e.Repo != "https://github.com/hanzo-apps/kanban-lane" || e.Template != "hanzo/example-kanban" {
		t.Fatalf("a demo must be traceable to its source and its parent: %+v", e)
	}
}

// TestPrivateProjectNeverLeaks is the tenancy boundary. acme's own catalog row
// is visible to acme and to NOBODY else — not another tenant, not an anonymous
// caller — while the published corpus is visible to all three.
func TestPrivateProjectNeverLeaks(t *testing.T) {
	app := mount(t)
	seed(t)

	// acme writes its own row through the org-scoped dialect, as any tenant would.
	code, body := do(t, app, http.MethodPost, "/v1/index/indexes/catalog/documents",
		`[{"id":"acme/secret-crm","org":"acme","name":"secret-crm","kind":"site","archetype":"app","updated":"2026-07-04"}]`,
		as("acme"))
	if code != http.StatusOK && code != http.StatusAccepted {
		t.Fatalf("acme write: %d %s", code, body)
	}

	for _, tc := range []struct {
		name string
		hdr  map[string]string
		want bool
	}{
		{"owner sees it", as("acme"), true},
		{"another tenant does not", as("globex"), false},
		{"anonymous does not", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decode(t, mustGetH(t, app, "/v1/catalog", tc.hdr))
			var found bool
			for _, e := range got.Data {
				if e.ID == "acme/secret-crm" {
					found = true
					if e.Scope != "org" {
						t.Errorf("a private row must be scoped %q, got %q", "org", e.Scope)
					}
				}
			}
			if found != tc.want {
				t.Fatalf("acme/secret-crm visible=%v, want %v: %v", found, tc.want, got.Data)
			}
			// Everyone still sees the published corpus.
			if got.Facets["org"]["lux"] != 1 {
				t.Errorf("published corpus missing for %s: %v", tc.name, got.Facets)
			}
		})
	}
}

// TestNoOneCanPublish proves the published corpus has no write door: every verb
// on /v1/catalog but GET is unroutable, so there is no gate to misconfigure and
// no credential that could promote a tenant row into the cross-org catalog.
func TestNoOneCanPublish(t *testing.T) {
	app := mount(t)
	for _, m := range []string{http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete} {
		code, _ := do(t, app, m, "/v1/catalog",
			`{"entries":[{"id":"acme/ad","org":"hanzo","name":"ad"}]}`, as("acme"))
		if code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
			t.Fatalf("%s /v1/catalog: got %d, want no write route", m, code)
		}
	}
	if got := decode(t, mustGet(t, app, "/v1/catalog")); got.Total != 0 {
		t.Fatalf("nothing should be published: %+v", got.Data)
	}
}

// TestSyncPrunes proves the corpus is a full swap, not an append: a project
// deleted upstream leaves the catalog on the next sync.
func TestSyncPrunes(t *testing.T) {
	app := mount(t)
	seed(t)
	kept, pruned, err := reconcile(context.Background(), PublicOrg,
		[]Entry{{ID: "lux/node", Org: "lux", Name: "node", Kind: "repo", Language: "Go", Updated: "2026-07-09"}})
	if err != nil || kept != 1 || pruned != 2 {
		t.Fatalf("re-sync: kept=%d pruned=%d err=%v; want 1/2", kept, pruned, err)
	}
	got := decode(t, mustGet(t, app, "/v1/catalog"))
	if got.Total != 1 || got.Data[0].ID != "lux/node" || got.Data[0].Updated != "2026-07-09" {
		t.Fatalf("swap did not converge: %+v", got.Data)
	}
}

func mustGet(t *testing.T, app *zip.App, url string) string { return mustGetH(t, app, url, nil) }

func mustGetH(t *testing.T, app *zip.App, url string, hdr map[string]string) string {
	t.Helper()
	code, body := do(t, app, http.MethodGet, url, "", hdr)
	if code != http.StatusOK {
		t.Fatalf("GET %s: %d %s", url, code, body)
	}
	return body
}

// TestProvenanceReachesTheAPI is the surface half of the fix: a marker the store
// gates but the API never emits protects nothing a reader can see. It also pins
// official as a tri-state browse axis, because "show me what is NOT ours" is the
// very next question a person asks once they learn the catalog carries other
// people's work.
func TestProvenanceReachesTheAPI(t *testing.T) {
	app := mount(t)
	seed(t,
		Entry{ID: "hanzo/ex-kanban", Org: "hanzo", Name: "ex-kanban", Kind: "site",
			Official: true, Forkable: true, Updated: "2026-07-01"},
		Entry{ID: "hanzo/kinetic", Org: "hanzo", Name: "kinetic", Kind: "site",
			Upstream: "UI8 \u2014 Fitness Pro: Website UI Kit", License: "UI8 commercial licence",
			Updated: "2026-07-02"},
	)

	all := decode(t, mustGet(t, app, "/v1/catalog"))
	got := map[string]Entry{}
	for _, e := range all.Data {
		got[e.Name] = e
	}
	if !got["ex-kanban"].Official {
		t.Error("the first-party marker never reached the API")
	}
	if e := got["kinetic"]; e.Official || e.Upstream == "" || e.License == "" {
		t.Errorf("a third-party kit must read as credited, not first-party: %+v", e)
	}
	if all.Facets["official"]["true"] != 1 || all.Facets["official"]["false"] != 1 {
		t.Errorf("official must be faceted both ways, got %v", all.Facets["official"])
	}
	for q, want := range map[string]string{
		"official=true": "hanzo/ex-kanban", "official=false": "hanzo/kinetic",
	} {
		r := decode(t, mustGet(t, app, "/v1/catalog?"+q))
		if r.Total != 1 || r.Data[0].ID != want {
			t.Errorf("%s: want [%s], got %+v", q, want, r.Data)
		}
	}
}

// TestTwoLanesOneCorpus is the information-architecture bug this axis fixes:
// /templates and /community are two VIEWS of this one corpus, cut on origin, so
// they can never disagree the way a second hand-kept catalog does. It asserts
// what each lane returns AND that the lanes partition — a row in no lane is a row
// no surface can show.
func TestTwoLanesOneCorpus(t *testing.T) {
	app := mount(t)
	seed(t,
		Entry{ID: "hanzo/folio", Org: "hanzo", Name: "folio", Kind: "site", Origin: OriginTemplate,
			URL: "https://folio.hanzo.app", Forkable: true, Updated: "2026-07-01"},
		Entry{ID: "hanzo/ex-kanban", Org: "hanzo", Name: "ex-kanban", Kind: "site", Origin: OriginCommunity,
			URL: "https://ex-kanban.hanzo.app", Template: "folio", Official: true, Updated: "2026-07-02"},
		Entry{ID: "acme/board", Org: "acme", Name: "board", Kind: "site", Origin: OriginCommunity,
			URL: "https://board.hanzo.app", Template: "folio", Updated: "2026-07-03"},
		Entry{ID: "hanzo/ui", Org: "hanzo", Name: "ui", Kind: "repo", Origin: OriginThirdParty,
			Upstream: "frappe/ui", License: "MIT", Updated: "2026-07-04"},
		Entry{ID: "lux/node", Org: "lux", Name: "node", Kind: "repo", Origin: OriginProduct,
			Updated: "2026-07-05"},
	)

	all := decode(t, mustGet(t, app, "/v1/catalog"))
	if got := all.Facets["origin"]; got[OriginTemplate] != 1 || got[OriginCommunity] != 2 ||
		got[OriginThirdParty] != 1 || got[OriginProduct] != 1 {
		t.Fatalf("every lane must be countable from the rail: %v", got)
	}
	tpl := decode(t, mustGet(t, app, "/v1/catalog?origin=template"))
	if tpl.Total != 1 || tpl.Data[0].ID != "hanzo/folio" {
		t.Errorf("/templates browses our starters: %+v", tpl.Data)
	}
	com := decode(t, mustGet(t, app, "/v1/catalog?origin=community"))
	if com.Total != 2 {
		t.Fatalf("/community browses what people built, got %d: %+v", com.Total, com.Data)
	}
	// Ours in the community lane are the ones carrying the marker — that is the
	// whole reason origin and official are two fields and not one value.
	mine := decode(t, mustGet(t, app, "/v1/catalog?origin=community&official=true"))
	if mine.Total != 1 || mine.Data[0].ID != "hanzo/ex-kanban" {
		t.Errorf("a seeded example is community AND ours: %+v", mine.Data)
	}
	// Lineage is browsable, which is what makes the lane readable rather than a
	// pile: everything built from folio, in one ask.
	from := decode(t, mustGet(t, app, "/v1/catalog?template=folio"))
	if from.Total != 2 {
		t.Errorf("the lineage axis must select every child of folio, got %d: %+v", from.Total, from.Data)
	}
	sum := 0
	for _, lane := range []string{OriginTemplate, OriginCommunity, OriginThirdParty, OriginProduct} {
		sum += decode(t, mustGet(t, app, "/v1/catalog?origin="+lane)).Total
	}
	if sum != all.Total {
		t.Errorf("the lanes must partition the corpus: %d != %d", sum, all.Total)
	}
	// origin must reach the wire on every row: an absent lane is the flattening
	// this field was added to end.
	if body := mustGet(t, app, "/v1/catalog?q=node"); !strings.Contains(body, `"origin":"product"`) {
		t.Errorf("origin must be on the wire: %s", body)
	}
}
