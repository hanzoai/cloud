package marketplace

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/tools"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
)

// A listing sells only what its seller owns, asked of the app that owns each
// kind: another org's thing and one that does not exist are refused alike.
func TestListingOwnership(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t, "conn_hello")
		tools.Default().Register(ownRows{byOrg: map[string][]string{seller: {"function_digest"}, buyer: {"function_digest"}}})
		agentID := e.agent(seller, "analyst")
		e.agent(buyer, "rival")
		e.must(201, http.MethodPost, "/v1/tool/skills", admin(seller), map[string]any{"name": "triage", "content": "# Triage"}, nil)
		e.peers.set(func(s *stand) { s.projects[seller] = map[string]bool{"atlas": true} })

		for _, tc := range []struct {
			as          who
			kind, thing string
			want        int
			ref         string
		}{
			{admin(seller), KindAgent, "analyst", 201, agentID},
			{admin(seller), KindAgent, agentID, 201, agentID},
			{admin(seller), KindPersona, "analyst", 201, agentID},
			{admin(seller), KindAgent, "rival", 422, ""},
			{admin(seller), KindAgent, "nobody", 422, ""},
			{admin(seller), KindSkill, "triage", 201, "triage"},
			{admin(seller), KindSkill, "ghost", 422, ""},
			{admin(seller), KindApp, "atlas", 201, "atlas"},
			{admin(seller), KindApp, "someone-elses", 422, ""},
			{admin(seller), KindMCP, "search", 422, ""},
			{admin(seller), KindTool, "conn_ghost", 422, ""},
			// Resolving is not owning. A connector action every org reaches is the
			// platform's: no org sells it, whichever asks.
			{admin(seller), KindTool, "conn_hello", 403, ""},
			{admin(buyer), KindTool, "conn_hello", 403, ""},
			{arbiter, KindTool, "conn_hello", 201, client.ToolResource("connector", "conn_hello")},
			// An org's own function runs for that org alone: a per-call offer of it is
			// one no other org could take.
			{admin(seller), KindTool, "function_digest", 422, ""},
		} {
			code, b, _ := e.req(http.MethodPost, "/v1/marketplace/listings", tc.as, "",
				publishReq{Kind: tc.kind, Tool: tc.thing, Title: tc.kind + " " + tc.thing, Public: true})
			if code != tc.want {
				t.Errorf("%s lists %s %q = %d (%s), want %d", tc.as.org, tc.kind, tc.thing, code, b, tc.want)
				continue
			}
			if code == 201 && !strings.Contains(string(b), `"ref":"`+tc.ref+`"`) {
				t.Errorf("list %s %q = %s, want ref %s", tc.kind, tc.thing, b, tc.ref)
			}
		}
		// The same skill name is not the buyer's to sell.
		if code, _, _ := e.req(http.MethodPost, "/v1/marketplace/listings", admin(buyer), "",
			publishReq{Kind: KindSkill, Tool: "triage", Title: "Triage"}); code != 422 {
			t.Fatalf("another org sold the seller's skill: %d", code)
		}
	})
}

// An edit changes a listing's copy, price, wallet, visibility and docs under
// publish's rules, and only its own org's admin makes it.
func TestListingEdits(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		wallet := e.createWallet(seller)
		e.agent(seller, "hello")
		l := e.list(seller, publishReq{Kind: KindAgent, Tool: "hello", Title: "Hello"})
		patch := func(as who, body map[string]any, want int) Listing {
			t.Helper()
			var out Listing
			code, b, _ := e.req(http.MethodPatch, "/v1/marketplace/listings/"+l.ID, as, "", body)
			if code != want {
				t.Fatalf("patch %v as %+v = %d (%s), want %d", body, as, code, b, want)
			}
			if code == 200 {
				_ = json.Unmarshal(b, &out)
			}
			return out
		}
		patch(admin(seller), map[string]any{"price": "3"}, 400) // a price with nowhere to be paid
		patch(admin(seller), map[string]any{"docs": "http://insecure.example"}, 400)
		patch(admin(seller), map[string]any{"title": ""}, 400)
		patch(member(seller), map[string]any{"title": "Mine now"}, 403)
		patch(admin(buyer), map[string]any{"title": "Mine now"}, 404)
		got := patch(admin(seller), map[string]any{"title": "Hello, world", "price": "3", "recipient": wallet, "public": true,
			"docs": "https://docs.example.com/hello"}, 200)
		if got.Title != "Hello, world" || got.Price.String() != "3" || got.Recipient != wallet || !got.Public ||
			got.Docs != "https://docs.example.com/hello" || got.Kind != KindAgent || got.Tool != "hello" || got.UpdatedAt < got.CreatedAt {
			t.Fatalf("edited %+v", got)
		}
		var item ShopListing
		e.must(200, http.MethodGet, "/v1/marketplace/shop/"+l.ID, anyone, nil, &item)
		if item.Title != "Hello, world" || item.Links.Docs != "https://docs.example.com/hello" {
			t.Fatalf("the shop shows %+v", item)
		}
		patch(admin(seller), map[string]any{"public": false}, 200)
		if code, _, _ := e.req(http.MethodGet, "/v1/marketplace/shop/"+l.ID, anyone, "", nil); code != 404 {
			t.Fatalf("a withdrawn listing is still in the shop: %d", code)
		}
	})
}

// The one catalog: every public listing of every kind, read by anyone, searched,
// faceted by kind, category, price and rating, and paged — a private listing never
// in it, a seller's public face beside each, and the ways to buy each.
func TestShopSearchFacetsAndPages(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t, "conn_a", "conn_b", "conn_c")
		wallet, till := e.createWallet(seller), e.createWallet(platform)
		e.agent(seller, "analyst")
		e.peers.set(func(s *stand) {
			s.projects[seller] = map[string]bool{"atlas": true}
			s.documented[seller], s.documented[platform] = true, true
		})
		e.list(seller, publishReq{Kind: KindAgent, Tool: "analyst", Title: "Research analyst", Category: "research",
			Price: "250", Recipient: wallet, Public: true})
		e.list(seller, publishReq{Kind: KindApp, Tool: "atlas", Title: "Atlas maps", Category: "maps", Public: true})
		for i, tool := range []string{"conn_a", "conn_b", "conn_c"} {
			price := ""
			if i == 0 {
				price = "0.01"
			}
			e.shelve(publishReq{Kind: KindTool, Tool: tool, Title: fmt.Sprintf("Research tool %d", i),
				Category: "research", Price: price, Recipient: till, Public: true})
		}
		e.shelve(publishReq{Kind: KindTool, Tool: "conn_a", Title: "Private research", Category: "research"})

		shop := func(query string) Shop {
			t.Helper()
			var s Shop
			e.must(200, http.MethodGet, "/v1/marketplace/shop"+query, anyone, nil, &s)
			return s
		}
		all := shop("")
		if all.Total != 5 || len(all.Listings) != 5 || all.Facets.Kind[KindTool] != 3 || all.Facets.Kind[KindAgent] != 1 ||
			all.Facets.Kind[KindApp] != 1 || all.Facets.Price["priced"] != 2 || all.Facets.Price["free"] != 3 ||
			all.Facets.Category["research"] != 4 || all.Facets.Rating["unrated"] != 5 {
			t.Fatalf("the whole shop = total %d facets %+v", all.Total, all.Facets)
		}
		for _, l := range all.Listings {
			if strings.Contains(l.Title, "Private") {
				t.Fatal("a private listing is in the shop")
			}
			if l.Seller.Org != l.PublisherOrg || !l.Seller.Documented || (l.Kind == KindTool) != (l.PublisherOrg == platform) {
				t.Fatalf("a %s's seller face %+v, want its publisher, documented", l.Kind, l.Seller)
			}
		}
		// Search, and the kind facet counted as if the kind filter were not there.
		research := shop("?q=RESEARCH&kind=tool")
		if research.Total != 3 || research.Facets.Kind[KindAgent] != 1 || research.Facets.Kind[KindTool] != 3 {
			t.Fatalf("research tools = total %d kinds %v", research.Total, research.Facets.Kind)
		}
		if got := shop("?q=research+analyst"); got.Total != 1 || got.Listings[0].Kind != KindAgent {
			t.Fatalf("two words = %+v", got.Listings)
		}
		if got := shop("?price=priced&category=research"); got.Total != 2 {
			t.Fatalf("priced research = %d, want 2", got.Total)
		}
		if got := shop("?rating=4"); got.Total != 0 || got.Facets.Rating["unrated"] != 5 {
			t.Fatalf("four stars and up = %d, want none yet (facets %v)", got.Total, got.Facets.Rating)
		}
		// Paging: the page is cut, the total is not.
		p1, p2 := shop("?limit=2"), shop("?limit=2&offset=4")
		if p1.Total != 5 || len(p1.Listings) != 2 || p2.Total != 5 || len(p2.Listings) != 1 || p1.Listings[0].ID == p2.Listings[0].ID {
			t.Fatalf("pages = %d/%d and %d/%d", len(p1.Listings), p1.Total, len(p2.Listings), p2.Total)
		}
		for _, bad := range []string{"?kind=planet", "?price=cheap", "?rating=9", "?limit=500", "?offset=-1"} {
			if code, _, _ := e.req(http.MethodGet, "/v1/marketplace/shop"+bad, anyone, "", nil); code != 400 {
				t.Fatalf("shop%s = %d, want 400", bad, code)
			}
		}
		// The ways to buy: a tool is installed, everything else is hired.
		for _, l := range all.Listings {
			wantOp := opHire
			if l.Kind == KindTool {
				wantOp = opInstall
			}
			if l.Links.MCP.Tool != "marketplace" || l.Links.MCP.Op != wantOp || !strings.HasPrefix(l.Links.CLI, "hanzo marketplace ") {
				t.Fatalf("links for a %s = %+v", l.Kind, l.Links)
			}
		}
	})
}

// The ways the shop names to buy a listing are the operations this surface
// serves, by the names the command line and the MCP server derive for them. The
// hanzo command line folds a path to words (hanzoai/cli genproduct.rs): the
// segments after the product, and "create" after a POST whose path also answers
// another method.
func TestShopLinksNameRealOperations(t *testing.T) {
	app := alone(t)
	paths, methods := map[string]string{}, map[string]int{}
	for _, c := range app.Commands() {
		paths[c.OperationID] = c.Path
		methods[c.Path]++
	}
	for op, cli := range map[string]string{opInstall: cliTool, opHire: cliHire} {
		path, ok := paths[op]
		if !ok {
			t.Fatalf("no operation %s is served", op)
		}
		words := strings.Split(strings.TrimPrefix(path, "/v1/"), "/")
		if methods[path] > 1 {
			words = append(words, "create")
		}
		if want := "hanzo " + strings.Join(words, " ") + " "; !strings.HasPrefix(cli, want) {
			t.Fatalf("the shop names %q for %s, and the command line derives %q", cli, op, want)
		}
	}
}

// One org's listings never push another seller out of the shop: the shop is
// searched, faceted and paged in the store rather than within a window of the
// newest, and an org publishes at most maxShop listings.
func TestNoSellerCrowdsOutAnother(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		const spammer = "spammer"
		e := newEnv(t)
		e.agent(seller, "needle")
		needle := e.list(seller, publishReq{Kind: KindAgent, Tool: "needle", Title: "Needle finder", Public: true})
		if _, err := mounted.State.store.db.Exec(`UPDATE listings SET created_at = created_at - 60 WHERE id = $1`, needle.ID); err != nil {
			t.Fatal(err)
		}
		e.agent(spammer, "bot")
		for i := range maxShop {
			e.list(spammer, publishReq{Kind: KindAgent, Tool: "bot", Title: fmt.Sprintf("Spam %d", i), Public: true})
		}
		if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/listings", admin(spammer), "",
			publishReq{Kind: KindAgent, Tool: "bot", Title: "One more", Public: true}); code != http.StatusConflict {
			t.Fatalf("listing %d = %d (%s), want 409", maxShop+1, code, b)
		}
		shop := func(query string) Shop {
			t.Helper()
			var s Shop
			e.must(200, http.MethodGet, "/v1/marketplace/shop"+query, anyone, nil, &s)
			return s
		}
		if got := shop("?q=needle"); got.Total != 1 || len(got.Listings) != 1 || got.Listings[0].ID != needle.ID {
			t.Fatalf("search for the needle = total %d", got.Total)
		}
		if got := shop("?seller=" + seller); got.Total != 1 {
			t.Fatalf("the seller's own listings = total %d", got.Total)
		}
		all := shop(fmt.Sprintf("?limit=1&offset=%d", maxShop))
		if all.Total != maxShop+1 || all.Facets.Kind[KindAgent] != maxShop+1 || len(all.Listings) != 1 || all.Listings[0].ID != needle.ID {
			t.Fatalf("the last page of %d = %+v", all.Total, all.Listings)
		}
	})
}

// A Kai capability is listed with its verified evaluation card and shared
// representation hash, installed across orgs through the train plane, downloadable
// as a .tar when open-sourced ($0), and sealed + priced per fire over x402 when
// monetized.
func TestCapabilityListingInstallWeightsAndX402Price(t *testing.T) {
	e := newEnv(t)
	wallet := e.createWallet(seller)
	rep := strings.Repeat("a", 64)
	baseSha := strings.Repeat("b", 64)
	ossSha := strings.Repeat("c", 64)
	proSha := strings.Repeat("d", 64)

	e.peers.set(func(s *stand) {
		s.capabilities[seller] = map[string]client.CapabilityOwned{
			"kai-support": {
				Mine: true, Name: "kai-support", SHA256: ossSha, BaseSHA256: baseSha,
				Representation: rep, Form: "head", Rank: 2, Bytes: 32768,
				QuestionKey: "queue", QuestionType: "choice", Labels: []string{"billing", "bug", "sales"},
				Repairs: 28, Damage: 0, FireRate: 0.19, Specificity: 0.996, OODMoved: 0, LatencyUs: 3.4,
			},
			"kai-pro-judge": {
				Mine: true, Name: "kai-pro-judge", SHA256: proSha, BaseSHA256: baseSha,
				Representation: rep, Form: "pair", Rank: 3, Bytes: 65536,
				QuestionKey: "rubric", QuestionType: "score", Labels: []string{"0", "1", "2", "3", "4"},
				Repairs: 112, Damage: 2, FireRate: 0.24, Specificity: 0.991, OODMoved: 0, LatencyUs: 5.1,
			},
		}
	})

	oss := e.list(seller, publishReq{
		Kind: KindCapability, Tool: "kai-support", Title: "Kai Support Queue Router",
		Category: "routing", Public: true, Capability: &capabilityIn{License: "MIT"},
	})
	if oss.Ref != client.CapabilityResource(seller, ossSha) || oss.Capability == nil || oss.Capability.License != "MIT" || oss.Capability.Repairs != 28 {
		t.Fatalf("oss capability listing = %+v, spec = %+v", oss, oss.Capability)
	}

	var item ShopListing
	e.must(200, http.MethodGet, "/v1/marketplace/shop/"+oss.ID, anyone, nil, &item)
	if item.Capability == nil || item.Capability.Representation != rep || item.Links.MCP.Op != opInstall {
		t.Fatalf("shop capability item = %+v", item)
	}

	var w ShopCapabilityWeights
	e.must(200, http.MethodGet, "/v1/marketplace/shop/"+oss.ID+"/weights", anyone, nil, &w)
	if w.SHA256 != ossSha || w.Representation != rep || w.Tar == "" {
		t.Fatalf("shopWeights = %+v", w)
	}

	e.install(buyer, "kai-support")
	e.peers.mu.Lock()
	installedRef := e.peers.installedCaps[buyer]["kai-support"]
	e.peers.mu.Unlock()
	if installedRef != oss.Ref {
		t.Fatalf("buyer installed %q, want %q", installedRef, oss.Ref)
	}

	pro := e.list(seller, publishReq{
		Kind: KindCapability, Tool: "kai-pro-judge", Title: "Kai Pro Rubric Judge",
		Category: "evaluation", Price: "0.002", Recipient: wallet, Public: true,
	})
	if pro.Capability == nil || pro.Capability.License != "commercial" {
		t.Fatalf("pro capability listing = %+v", pro.Capability)
	}
	if code, _, _ := e.req(http.MethodGet, "/v1/marketplace/shop/"+pro.ID+"/weights", anyone, "", nil); code != http.StatusForbidden {
		t.Fatalf("monetized capability weights returned %d, want 403", code)
	}
	reg := &registry{store: mounted.State.store}
	terms, priced, err := reg.Price(t.Context(), pro.Ref)
	if err != nil || !priced || terms.Amount.String() != "0.002" || terms.RecipientOrg != seller || terms.RecipientWalletID != wallet {
		t.Fatalf("Price(%s) = %+v, priced=%v, err=%v", pro.Ref, terms, priced, err)
	}
}
