package marketplace

// expert_test.go — a person who makes their time available is a listing of kind
// expert, booked through the job that already exists. These tests drive it as the
// fleet does: published by a member, found in the shop, verified by a SuperAdmin
// alone, hired, delivered, released and rated, with the event each act states.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/dialect"
)

// heard is what an expert's acts state on the event plane, as the test hears it.
type heard struct {
	mu  sync.Mutex
	got []stated
}

type stated struct {
	org string
	in  client.EventIn
}

// listen substitutes the event plane's call and hands back what leaves.
func hear(t *testing.T) *heard {
	t.Helper()
	h := &heard{}
	prior := sendFn
	sendFn = func(ctx context.Context, in *client.EventIn) (*client.EventCaptured, error) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.got = append(h.got, stated{org: cloud.Who(ctx).Org, in: *in})
		return &client.EventCaptured{Accepted: 1}, nil
	}
	t.Cleanup(func() { sendFn = prior })
	return h
}

// await is every event heard once n have been: the statement is detached from the
// act that made it.
func (h *heard) await(t *testing.T, n int) []stated {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(5 * time.Millisecond) {
		h.mu.Lock()
		got := slices.Clone(h.got)
		h.mu.Unlock()
		if len(got) >= n || time.Now().After(deadline) {
			return got
		}
	}
}

// fact is the value of attribute name on an event, and whether it was stated.
func (s stated) fact(name string) (string, bool) {
	for _, a := range s.in.Attributes {
		if a.Name == name {
			return a.Value, true
		}
	}
	return "", false
}

// profile is an expert's own words.
func profile(rate string, areas ...string) map[string]any {
	return map[string]any{"rate": rate, "areas": areas, "languages": []string{"en", "es"},
		"timezone": "America/Los_Angeles", "availability": "Weekdays 9-17 PT"}
}

// expert lists the person who is member(org) as an expert: any member may, and
// the org's admin is not needed.
func (e *env) expert(w who, in map[string]any) Listing {
	e.t.Helper()
	var l Listing
	e.must(http.StatusCreated, http.MethodPost, "/v1/marketplace/listings", w, in, &l)
	return l
}

func TestAnExpertIsListedAndFound(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		h := hear(t)
		e.shopfront() // an agent for hire in the same shop: not an expert
		wallet := e.createWallet(buyer)

		l := e.expert(member(buyer), map[string]any{"kind": KindExpert, "title": "Ada Byron", "description": "I configure Hanzo Cloud for teams.",
			"recipient": wallet, "public": true, "expert": profile("150", "cloud", "freelance", "base")})
		if l.Kind != KindExpert || l.Tool != "u_"+buyer || l.Expert == nil || l.Expert.Person != "u_"+buyer ||
			l.Expert.Rate.String() != "150" || l.Expert.Verified || !l.Price.IsZero() {
			t.Fatalf("published %+v %+v", l, l.Expert)
		}
		// Areas keep the list's order whatever order they were said in.
		if want := []string{"base", "cloud", "freelance"}; !slices.Equal(l.Expert.Areas, want) {
			t.Fatalf("areas %v, want %v", l.Expert.Areas, want)
		}

		// The expert reads their own listing, with their identity.
		var mine listingPage
		e.must(200, http.MethodGet, "/v1/marketplace/listings", member(buyer), nil, &mine)
		if len(mine.Listings) != 1 || mine.Listings[0].Expert == nil || mine.Listings[0].Expert.Person != "u_"+buyer ||
			!mine.Listings[0].Expert.Self || !l.Expert.Self ||
			mine.Listings[0].Expert.Timezone != "America/Los_Angeles" || mine.Listings[0].Expert.Availability != "Weekdays 9-17 PT" {
			t.Fatalf("own listings %+v", mine.Listings)
		}

		// The org reads its own listings by kind, too.
		e.agent(buyer, "helper")
		e.list(buyer, publishReq{Kind: KindAgent, Tool: "helper", Title: "Helper"})
		for kind, n := range map[string]int{"": 2, KindExpert: 1, KindAgent: 1, KindApp: 0} {
			var own listingPage
			e.must(200, http.MethodGet, "/v1/marketplace/listings?kind="+kind, admin(buyer), nil, &own)
			if len(own.Listings) != n || (kind != "" && n > 0 && own.Listings[0].Kind != kind) {
				t.Fatalf("own listings kind=%q = %d, want %d", kind, len(own.Listings), n)
			}
		}
		if code, _, _ := e.req(http.MethodGet, "/v1/marketplace/listings?kind=cooking", member(buyer), "", nil); code != http.StatusBadRequest {
			t.Fatalf("an own-listings kind off the list = %d, want 400", code)
		}

		// Anyone finds experts by kind, without the person's identity, and the other
		// kinds are not among them.
		var all, shop Shop
		e.must(200, http.MethodGet, "/v1/marketplace/shop", anyone, nil, &all)
		e.must(200, http.MethodGet, "/v1/marketplace/shop?kind=expert", anyone, nil, &shop)
		if all.Total != 2 || shop.Total != 1 || shop.Facets.Kind[KindExpert] != 1 || len(shop.Listings) != 1 {
			t.Fatalf("shop total %d, experts %d %v", all.Total, shop.Total, shop.Facets.Kind)
		}
		got := shop.Listings[0]
		// A colleague of the expert reads the same org's listings and is not the expert.
		var colleague listingPage
		e.must(200, http.MethodGet, "/v1/marketplace/listings?kind=expert", who{org: buyer, user: "u_grace"}, nil, &colleague)
		if len(colleague.Listings) != 1 || colleague.Listings[0].Expert.Self {
			t.Fatalf("a colleague reads %+v, want the listing and not as their own", colleague.Listings)
		}
		if got.Kind != KindExpert || got.Tool != "" || got.Expert == nil || got.Expert.Person != "" || got.Expert.Rate.String() != "150" ||
			got.Expert.Self || !slices.Equal(got.Expert.Languages, []string{"en", "es"}) || got.Links.MCP.Op != opHire {
			t.Fatalf("the shop shows %+v %+v", got, got.Expert)
		}
		// By area, by words, by price: an hourly rate is a price.
		for q, n := range map[string]int{"?kind=expert&area=cloud": 1, "?kind=expert&area=bot": 0, "?area=freelance": 1,
			"?q=freelance": 1, "?q=hanzo": 1, "?kind=expert&price=priced": 1, "?kind=expert&price=free": 0} {
			var s Shop
			e.must(200, http.MethodGet, "/v1/marketplace/shop"+q, anyone, nil, &s)
			if s.Total != n {
				t.Fatalf("shop%s = %d, want %d", q, s.Total, n)
			}
		}
		if code, _, _ := e.req(http.MethodGet, "/v1/marketplace/shop?area=cooking", anyone, "", nil); code != http.StatusBadRequest {
			t.Fatalf("an area outside the list = %d, want 400", code)
		}

		// Listing is stated, once, about the person, with the facts the program rules on.
		evs := h.await(t, 1)
		if len(evs) != 1 || evs[0].in.Name != "expert.listed" || evs[0].org != buyer || evs[0].in.Subject != "u_"+buyer {
			t.Fatalf("stated %+v, want expert.listed for acme's expert", evs)
		}
		for name, want := range map[string]string{"listing": l.ID, "org": buyer, "person": "u_" + buyer, "areas": "base,cloud,freelance", "public": "true"} {
			if v, ok := evs[0].fact(name); !ok || v != want {
				t.Fatalf("expert.listed %s = %q (%v), want %q", name, v, ok, want)
			}
		}
	})
}

func TestAnExpertIsRefusedWhatTheShopCouldNotShow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		wallet := e.createWallet(buyer)
		good := func() map[string]any {
			return map[string]any{"kind": KindExpert, "title": "Ada", "recipient": wallet, "public": true, "expert": profile("150", "cloud")}
		}
		with := func(f func(m, x map[string]any)) map[string]any {
			m := good()
			f(m, m["expert"].(map[string]any))
			return m
		}
		for name, tc := range map[string]struct {
			w    who
			body map[string]any
			want int
		}{
			"nobody signed in":        {anyone, good(), http.StatusUnauthorized},
			"no profile":              {member(buyer), with(func(m, _ map[string]any) { delete(m, "expert") }), http.StatusBadRequest},
			"an area off the list":    {member(buyer), with(func(_, x map[string]any) { x["areas"] = []string{"cooking"} }), http.StatusBadRequest},
			"no area":                 {member(buyer), with(func(_, x map[string]any) { x["areas"] = []string{} }), http.StatusBadRequest},
			"a language that is not":  {member(buyer), with(func(_, x map[string]any) { x["languages"] = []string{"English"} }), http.StatusBadRequest},
			"no language":             {member(buyer), with(func(_, x map[string]any) { x["languages"] = []string{} }), http.StatusBadRequest},
			"a time zone that is not": {member(buyer), with(func(_, x map[string]any) { x["timezone"] = "Mars/Olympus" }), http.StatusBadRequest},
			"no hours":                {member(buyer), with(func(_, x map[string]any) { x["availability"] = " " }), http.StatusBadRequest},
			"a negative rate":         {member(buyer), with(func(_, x map[string]any) { x["rate"] = "-1" }), http.StatusBadRequest},
			"nothing for sale":        {member(buyer), with(func(_, x map[string]any) { x["rate"] = "0" }), http.StatusBadRequest},
			"a rate with no wallet":   {member(buyer), with(func(m, _ map[string]any) { delete(m, "recipient") }), http.StatusBadRequest},
			"another person's name":   {member(buyer), with(func(m, _ map[string]any) { m["tool"] = "u_globex" }), http.StatusUnprocessableEntity},
			"a profile on an agent": {admin(buyer), map[string]any{"kind": KindAgent, "tool": "x", "title": "x",
				"expert": profile("1", "cloud")}, http.StatusBadRequest},
		} {
			if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/listings", tc.w, "", tc.body); code != tc.want {
				t.Errorf("%s = %d (%s), want %d", name, code, b, tc.want)
			}
		}
		// A fixed price alone is enough to sell, and an hourly rate alone.
		p := good()
		p["expert"] = profile("0", "bot")
		p["price"] = "900"
		e.expert(member(buyer), p)
		// A person is listed once in their org, and another person of it is their own.
		if code, _, _ := e.req(http.MethodPost, "/v1/marketplace/listings", member(buyer), "", good()); code != http.StatusConflict {
			t.Fatalf("a second listing of the same person = %d, want 409", code)
		}
		other := who{org: buyer, user: "u_grace"}
		e.expert(other, good())
	})
}

func TestOnlyASuperAdminVerifies(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		h := hear(t)
		wallet := e.createWallet(buyer)
		l := e.expert(member(buyer), map[string]any{"kind": KindExpert, "title": "Ada", "recipient": wallet, "public": true,
			"expert": profile("150", "cloud")})
		verify := func(w who, on bool) (int, Listing) {
			var out Listing
			code, b, _ := e.req(http.MethodPost, "/v1/marketplace/listings/"+l.ID+"/verify", w, "", map[string]any{"verified": on})
			if code == http.StatusOK {
				if err := json.Unmarshal(b, &out); err != nil {
					t.Fatalf("verify: decode %s: %v", b, err)
				}
			}
			return code, out
		}
		// Not the expert, not their org's admin, not an admin of another org, not nobody.
		for name, w := range map[string]who{"the expert": member(buyer), "their org's admin": admin(buyer), "another org's admin": admin(seller),
			"nobody": anyone} {
			if code, _ := verify(w, true); code != http.StatusForbidden {
				t.Errorf("%s verifies = %d, want 403", name, code)
			}
		}
		// The expert cannot give themself the mark by editing or by publishing it.
		e.must(200, http.MethodPatch, "/v1/marketplace/listings/"+l.ID, member(buyer), map[string]any{"title": "Ada B.", "verified": true,
			"expert": map[string]any{"verified": true, "rate": "150", "areas": []string{"cloud"}, "languages": []string{"en"},
				"timezone": "UTC", "availability": "Always"}}, nil)
		var item ShopListing
		e.must(200, http.MethodGet, "/v1/marketplace/shop/"+l.ID, anyone, nil, &item)
		if item.Expert.Verified {
			t.Fatal("an expert gave themself the mark")
		}
		if got := h.await(t, 1); len(got) != 1 {
			t.Fatalf("a refused verification stated %d events, want only the listing", len(got))
		}

		// A SuperAdmin — and nobody less — gives it, whichever org the expert is in.
		code, out := verify(arbiter, true)
		if code != http.StatusOK || out.Expert == nil || !out.Expert.Verified || out.PublisherOrg != buyer {
			t.Fatalf("the arbiter verifies = %d %+v", code, out)
		}
		e.must(200, http.MethodGet, "/v1/marketplace/shop/"+l.ID, anyone, nil, &item)
		if !item.Expert.Verified {
			t.Fatal("the shop does not show the mark")
		}
		// An edit keeps it: the mark is staff's, and the expert's words are theirs.
		e.must(200, http.MethodPatch, "/v1/marketplace/listings/"+l.ID, member(buyer), map[string]any{"description": "Still me"}, nil)
		e.must(200, http.MethodGet, "/v1/marketplace/shop/"+l.ID, anyone, nil, &item)
		if !item.Expert.Verified || item.Description != "Still me" {
			t.Fatalf("an edit changed the mark: %+v", item)
		}
		// It is on the trail, under the one who gave it; saying it again changes and states nothing.
		verify(arbiter, true)
		if got := e.actions(platform, "marketplace.expert."); !slices.Equal(got, []string{"marketplace.expert.verified"}) {
			t.Fatalf("the trail has %v, want the one verification", got)
		}
		evs := h.await(t, 2)
		if len(evs) != 2 || evs[1].in.Name != "expert.verified" || evs[1].org != buyer || evs[1].in.Subject != "u_"+buyer {
			t.Fatalf("stated %+v, want expert.verified for the expert's org", evs)
		}
		if v, _ := evs[1].fact("listing"); v != l.ID {
			t.Fatalf("expert.verified listing = %q, want %q", v, l.ID)
		}
		// Taking it away is a SuperAdmin's too, is on the trail, and states nothing.
		if code, out := verify(arbiter, false); code != http.StatusOK || out.Expert.Verified {
			t.Fatalf("unverify = %d %+v", code, out.Expert)
		}
		if got := e.actions(platform, "marketplace.expert."); !slices.Equal(got, []string{"marketplace.expert.verified", "marketplace.expert.unverified"}) {
			t.Fatalf("the trail has %v", got)
		}
		// And only an expert listing has a mark to give.
		agent, _ := e.shopfront()
		if code, _, _ := e.req(http.MethodPost, "/v1/marketplace/listings/"+agent.ID+"/verify", arbiter, "", map[string]any{"verified": true}); code != http.StatusNotFound {
			t.Fatalf("verifying an agent = %d, want 404", code)
		}
	})
}

func TestAnExpertIsTheirOwnToEditAndWithdraw(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		wallet := e.createWallet(buyer)
		l := e.expert(member(buyer), map[string]any{"kind": KindExpert, "title": "Ada", "recipient": wallet, "public": true,
			"expert": profile("150", "cloud")})
		path := "/v1/marketplace/listings/" + l.ID
		// A colleague who is no admin has no say; the org's admin does.
		colleague := who{org: buyer, user: "u_grace"}
		if code, _, _ := e.req(http.MethodPatch, path, colleague, "", map[string]any{"title": "Grace"}); code != http.StatusForbidden {
			t.Fatalf("a colleague edits = %d, want 403", code)
		}
		if code, _, _ := e.req(http.MethodDelete, path, colleague, "", nil); code != http.StatusForbidden {
			t.Fatalf("a colleague withdraws = %d, want 403", code)
		}
		var got Listing
		e.must(200, http.MethodPatch, path, member(buyer), map[string]any{"title": "Ada Byron", "expert": profile("200", "agents", "models")}, &got)
		if got.Title != "Ada Byron" || got.Expert.Rate.String() != "200" || !slices.Equal(got.Expert.Areas, []string{"agents", "models"}) ||
			got.Expert.Person != "u_"+buyer {
			t.Fatalf("edited %+v %+v", got, got.Expert)
		}
		// An edit that sells nothing is refused as a publish is.
		if code, _, _ := e.req(http.MethodPatch, path, member(buyer), "", map[string]any{"expert": profile("0", "agents")}); code != http.StatusBadRequest {
			t.Fatalf("editing to sell nothing = %d, want 400", code)
		}
		e.must(200, http.MethodPatch, path, admin(buyer), map[string]any{"description": "Edited by the org"}, nil)
		// A profile is an expert's alone.
		agent, _ := e.shopfront()
		if code, _, _ := e.req(http.MethodPatch, "/v1/marketplace/listings/"+agent.ID, admin(seller), "", map[string]any{"expert": profile("1", "bot")}); code != http.StatusBadRequest {
			t.Fatalf("a profile on an agent = %d, want 400", code)
		}
		// The expert withdraws themself, and may list again.
		e.must(http.StatusNoContent, http.MethodDelete, path, member(buyer), nil, nil)
		var shop Shop
		e.must(200, http.MethodGet, "/v1/marketplace/shop?kind=expert", anyone, nil, &shop)
		if shop.Total != 0 {
			t.Fatalf("a withdrawn expert is in the shop: %d", shop.Total)
		}
		e.expert(member(buyer), map[string]any{"kind": KindExpert, "title": "Ada", "recipient": wallet, "public": true,
			"expert": profile("150", "cloud")})
	})
}

// An expert is booked through the job that exists: quoted, funded by a held
// authorization, accepted, delivered, released, rated. Nothing about it is new
// but the person.
func TestAnExpertIsBookedThroughTheJobFlow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		h := hear(t)
		e.fund(buyer, money.FromCents(100_000))
		wallet := e.createWallet(seller)
		l := e.expert(member(seller), map[string]any{"kind": KindExpert, "title": "Grace Hopper", "price": "500", "recipient": wallet,
			"public": true, "expert": profile("120", "configure", "freelance")})

		j := e.hire(buyer, map[string]any{"listing": l.ID, "brief": "Configure Hanzo Cloud for our team", "amount": "500.00"}, 30*24*time.Hour)
		if j.Status != StatusOpen || j.SellerOrg != seller || j.Listing != l.ID || j.Title != "Grace Hopper" || j.Amount.String() != "500" {
			t.Fatalf("booked %+v", j)
		}
		e.act(j.ID, "accept", member(seller), nil, http.StatusOK)
		e.act(j.ID, "deliver", member(seller), map[string]any{"note": "Configured", "url": "https://files.example.com/notes.pdf"}, http.StatusOK)
		e.act(j.ID, "release", member(buyer), nil, http.StatusOK)
		price, _ := money.ParseUSD("500")
		if got := e.balance(seller, wallet); got.Cmp(price) != 0 {
			t.Fatalf("the expert's org was paid %s, want 500", got)
		}

		// The buyer's rating is recorded, on the listing and the seller; the expert's
		// own of the buyer is not the expert's.
		var f Feedback
		e.must(201, http.MethodPost, "/v1/marketplace/jobs/"+j.ID+"/feedback", member(buyer), map[string]any{"rating": 5, "comment": "Excellent"}, &f)
		if f.Rating != 5 || f.About != seller || f.Listing != l.ID {
			t.Fatalf("feedback %+v", f)
		}
		e.must(201, http.MethodPost, "/v1/marketplace/jobs/"+j.ID+"/feedback", member(seller), map[string]any{"rating": 3}, nil)
		var item ShopListing
		e.must(200, http.MethodGet, "/v1/marketplace/shop/"+l.ID, anyone, nil, &item)
		if item.Reputation.Rating == nil || *item.Reputation.Rating != 5 || item.Reputation.Reviews != 1 || item.Reputation.Jobs.Completed != 1 {
			t.Fatalf("the expert's reputation = %+v, want one 5-star review of one job", item.Reputation)
		}

		// Listing, delivery and review are each stated once, about the expert, in
		// their org: the seller's own rating of the buyer is not a review of the expert.
		evs := h.await(t, 3)
		if len(evs) != 3 {
			t.Fatalf("stated %d events, want 3: %+v", len(evs), evs)
		}
		by := map[string]stated{}
		for _, ev := range evs {
			by[ev.in.Name] = ev
			if ev.org != seller || ev.in.Subject != "u_"+seller || ev.in.Product != "marketplace" {
				t.Fatalf("%s stated %+v, want the expert's org and person", ev.in.Name, ev)
			}
		}
		for name, want := range map[string]map[string]string{
			"expert.listed":        {"listing": l.ID},
			"expert.job.delivered": {"listing": l.ID, "job": j.ID, "buyer": buyer},
			"expert.review":        {"listing": l.ID, "job": j.ID, "buyer": buyer, "rating": "5"},
		} {
			ev, ok := by[name]
			if !ok {
				t.Fatalf("%s was not stated: %+v", name, evs)
			}
			for k, v := range want {
				if got, _ := ev.fact(k); got != v {
					t.Fatalf("%s %s = %q, want %q", name, k, got, v)
				}
			}
		}
		// Delivery came before any review, so it states no rating yet.
		if v, ok := by["expert.job.delivered"].fact("rating"); ok {
			t.Fatalf("expert.job.delivered states rating %q before any review", v)
		}
	})
}

// A job through any other listing states nothing about an expert.
func TestAJobThroughAnAgentStatesNoExpertEvent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		h := hear(t)
		l, _ := e.shopfront()
		e.fund(buyer, money.FromCents(100_000))
		j := e.hire(buyer, map[string]any{"listing": l.ID, "brief": "Summarize", "amount": "250.00"}, 30*24*time.Hour)
		e.act(j.ID, "accept", member(seller), nil, http.StatusOK)
		e.act(j.ID, "deliver", member(seller), map[string]any{"note": "Done"}, http.StatusOK)
		e.act(j.ID, "release", member(buyer), nil, http.StatusOK)
		e.must(201, http.MethodPost, "/v1/marketplace/jobs/"+j.ID+"/feedback", member(buyer), map[string]any{"rating": 4}, nil)
		time.Sleep(100 * time.Millisecond)
		if got := h.await(t, 0); len(got) != 0 {
			t.Fatalf("an agent's job stated %+v", got)
		}
	})
}

// Two requests of one person racing past the check still list them once: the
// store refuses the second, and the refusal is the conflict the check answers.
func TestAPersonIsListedOnceWhenRaced(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		newEnv(t)
		st := mounted.State.store
		l := Listing{PublisherOrg: buyer, Kind: KindExpert, Tool: "u_x", Title: "X", Price: money.Zero(),
			Expert: &Expert{Person: "u_x", Rate: money.FromCents(10_000), Areas: []string{"bot"}, Languages: []string{"en"},
				Timezone: "UTC", Availability: "Always"}}
		if _, err := st.Create(context.Background(), l); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Create(context.Background(), l); !dialect.Unique(err) {
			t.Fatalf("a second listing of one person = %v, want a unique violation", err)
		}
		// Another org lists the same person on its own.
		l.PublisherOrg = seller
		if _, err := st.Create(context.Background(), l); err != nil {
			t.Fatal(err)
		}
	})
}
