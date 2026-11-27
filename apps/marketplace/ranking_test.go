package marketplace

// ranking_test.go — experts in the shop beyond the list: skills and links, the
// standing the rewards program gives a public profile, the orders and filters that
// read it, and the score in each product.
//
// The program is another process in the fleet, so it is stood in on the real plane
// as principal is: the market asks it by name, as the platform, exactly as it does
// across sockets.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/orm"
	rewardmod "github.com/hanzoai/reward"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk/locus"
)

// ── the stand-in for the rewards program ─────────────────────────────────────

// program answers reward_people from the people a test sets, and keeps what it was
// asked.
type program struct {
	mu     sync.Mutex
	people map[string]client.RewardPerson
	asks   [][]string
	fams   [][]string
	status int // when it is not 0 the program fails with it
}

func serveProgram(t *testing.T) *program {
	t.Helper()
	p := &program{people: map[string]client.RewardPerson{}}
	app := zip.New(zip.Config{AppName: "reward"})
	app.Post("/reward/people", func(ctx context.Context, in *client.RewardPeopleIn) (*client.RewardPeopleOut, error) {
		if zip.CallerOf(ctx).Org != platform {
			return nil, zip.ErrForbidden("only the platform reads across members")
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		p.asks = append(p.asks, slices.Clone(in.Subjects))
		p.fams = append(p.fams, slices.Clone(in.Families))
		if p.status != 0 {
			return nil, zip.Errorf(p.status, "the program is down")
		}
		out := &client.RewardPeopleOut{People: []client.RewardPerson{}}
		for _, s := range in.Subjects {
			if rp, ok := p.people[s]; ok {
				out.People = append(out.People, rp)
			}
		}
		return out, nil
	}, zip.WithOperationID(client.RewardPeople))
	listen(t, app, "reward")
	return p
}

func (p *program) set(fn func(map[string]client.RewardPerson)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fn(p.people)
}

func (p *program) asked() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.asks)
}

// visible is a person with a public reward profile holding the badges named.
func visible(sub string, level int, points int64, held ...string) client.RewardPerson {
	rp := client.RewardPerson{Subject: sub, Level: level, LevelName: fmt.Sprintf("Level %d", level), Points: points,
		Badges: []client.RewardBadge{}, Held: held, Pillars: client.PillarCounts(map[string]int{"use": 0, "share": 0, "community": 0, "contribute": 0, "expert": 0}),
		Contributions: []client.RewardContribution{}, Usage: []client.RewardUsage{}}
	if rp.Held == nil {
		rp.Held = []string{}
	}
	for _, h := range held[:min(3, len(held))] {
		rp.Badges = append(rp.Badges, client.RewardBadge{ID: h, Name: "Badge " + h, Pillar: "use", Rarity: "common"})
	}
	return rp
}

// ── experts ──────────────────────────────────────────────────────────────────

// listing is an expert to put in the shop.
type listing struct {
	name     string
	rate     string
	areas    []string
	langs    []string
	verified bool
	at       int64 // created_at
	skills   []string
	jobs     []int // the stars of each job released through the listing; 0 is unrated
}

// person is the IAM subject of the expert a spec names.
func (s listing) person() string { return "u_" + s.name }

// add puts an expert in the shop, public, with the jobs and ratings the spec says.
func (e *env) add(s listing) Listing {
	e.t.Helper()
	rate := money.Zero()
	if s.rate != "" {
		var err error
		if rate, err = money.ParseUSD(s.rate); err != nil {
			e.t.Fatal(err)
		}
	}
	org := "org_" + s.name
	st := mounted.State.store
	l, err := st.Create(context.Background(), Listing{PublisherOrg: org, Kind: KindExpert, Tool: s.person(), Ref: s.person(),
		Title: s.name, Public: true, Price: money.Zero(), Expert: &Expert{Person: s.person(), Rate: rate,
			Areas: orElse(s.areas, []string{"cloud"}), Languages: orElse(s.langs, []string{"en"}), Timezone: "UTC",
			Availability: "Always", Verified: s.verified, Skills: s.skills, Links: []string{}}})
	if err != nil {
		e.t.Fatalf("add %s: %v", s.name, err)
	}
	if s.at != 0 {
		if _, err := st.db.Exec(`UPDATE listings SET created_at = $1 WHERE id = $2`, s.at, l.ID); err != nil {
			e.t.Fatal(err)
		}
		l.CreatedAt = s.at
	}
	for i, stars := range s.jobs {
		job := fmt.Sprintf("job_%s_%d", s.name, i)
		if _, err := st.db.Exec(`INSERT INTO jobs (id, listing, title, buyer_org, seller_org, amount, category, rail, status, review, recipient,
			clearance, created_at, updated_at) VALUES ($1, $2, 'x', 'buyer', $3, '1', 'service', $4, $5, 1, 'w', 'c', 1, 1)`,
			job, l.ID, org, RailX402, StatusReleased); err != nil {
			e.t.Fatal(err)
		}
		if stars > 0 {
			if _, err := st.db.Exec(`INSERT INTO feedback (job, from_org, about, listing, rating, at) VALUES ($1, 'buyer', $2, $3, $4, 1)`,
				job, org, l.ID, stars); err != nil {
				e.t.Fatal(err)
			}
		}
	}
	return l
}

func orElse[T any](v, d []T) []T {
	if len(v) == 0 {
		return d
	}
	return v
}

// settle forgets what the shop kept about the program and the ranking, so the next
// read asks again.
func (e *env) settle() {
	x := mounted.State.expertise
	x.mu.Lock()
	x.rank = nil
	x.mu.Unlock()
	x.people.mu.Lock()
	x.people.got, x.people.down = map[string]kept{}, time.Time{}
	x.people.mu.Unlock()
}

// shop reads the shop with the query.
func (e *env) shop(query string) Shop {
	e.t.Helper()
	var s Shop
	e.must(200, http.MethodGet, "/v1/marketplace/shop"+query, anyone, nil, &s)
	return s
}

// names are the titles of a page, in order.
func names(s Shop) []string {
	out := make([]string, len(s.Listings))
	for i, l := range s.Listings {
		out[i] = l.Title
	}
	return out
}

// asMap is the parts that count, by name.
func asMap(p Parts) map[string]float64 {
	out := map[string]float64{}
	for name, v := range map[string]float64{"usage": p.Usage, "badges": p.Badges, "contributions": p.Contributions, "jobs": p.Jobs} {
		if v != 0 {
			out[name] = v
		}
	}
	return out
}

// ── skills and links ─────────────────────────────────────────────────────────

func TestSkillsAndLinksAreTheExpertsToSayAndAreShownToAll(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		wallet := e.createWallet(buyer)
		body := func(f func(x map[string]any)) map[string]any {
			x := profile("150", "cloud")
			f(x)
			return map[string]any{"kind": KindExpert, "title": "Ada", "recipient": wallet, "public": true, "expert": x}
		}
		l := e.expert(member(buyer), body(func(x map[string]any) {
			x["skills"] = []string{" Go ", "go", "ClickHouse", "", "RAG"}
			x["links"] = []string{"https://github.com/ada", " https://ada.example.com/work?x=1,2 ", "https://github.com/ada", ""}
		}))
		// Trimmed, blanks dropped, a name once whatever its case (the first spelling
		// kept), a link once.
		if want := []string{"Go", "ClickHouse", "RAG"}; !slices.Equal(l.Expert.Skills, want) {
			t.Fatalf("skills %v, want %v", l.Expert.Skills, want)
		}
		if want := []string{"https://github.com/ada", "https://ada.example.com/work?x=1,2"}; !slices.Equal(l.Expert.Links, want) {
			t.Fatalf("links %v, want %v", l.Expert.Links, want)
		}
		// Every read of the expert carries them, and the shop's still hides who they are.
		var own listingPage
		e.must(200, http.MethodGet, "/v1/marketplace/listings", member(buyer), nil, &own)
		var item ShopListing
		e.must(200, http.MethodGet, "/v1/marketplace/shop/"+l.ID, anyone, nil, &item)
		var shop Shop
		e.must(200, http.MethodGet, "/v1/marketplace/shop?kind=expert", anyone, nil, &shop)
		for name, got := range map[string]*Expert{"own": own.Listings[0].Expert, "item": item.Expert, "shop": shop.Listings[0].Expert} {
			if !slices.Equal(got.Skills, l.Expert.Skills) || !slices.Equal(got.Links, l.Expert.Links) {
				t.Errorf("%s read: skills %v links %v", name, got.Skills, got.Links)
			}
		}
		if item.Expert.Person != "" {
			t.Fatal("the shop shows who the expert is")
		}
		// A skill is searchable.
		if got := e.shop("?kind=expert&q=clickhouse"); got.Total != 1 {
			t.Fatalf("a search for a skill found %d", got.Total)
		}

		// An edit replaces the whole profile: what it leaves out is empty. An edit that
		// names no profile keeps it.
		var edited Listing
		e.must(200, http.MethodPatch, "/v1/marketplace/listings/"+l.ID, member(buyer), map[string]any{"title": "Ada B."}, &edited)
		if len(edited.Expert.Skills) != 3 || len(edited.Expert.Links) != 2 {
			t.Fatalf("an edit of the title changed the profile: %+v", edited.Expert)
		}
		e.must(200, http.MethodPatch, "/v1/marketplace/listings/"+l.ID, member(buyer), map[string]any{"expert": profile("150", "cloud")}, &edited)
		if edited.Expert.Skills == nil || len(edited.Expert.Skills) != 0 || edited.Expert.Links == nil || len(edited.Expert.Links) != 0 {
			t.Fatalf("a profile that leaves them out has skills %#v links %#v, want empty lists", edited.Expert.Skills, edited.Expert.Links)
		}
		raw, _ := json.Marshal(edited.Expert)
		if !strings.Contains(string(raw), `"skills":[]`) || !strings.Contains(string(raw), `"links":[]`) {
			t.Fatalf("an expert with none reads %s, want [] and not null", raw)
		}
		e.must(200, http.MethodPatch, "/v1/marketplace/listings/"+l.ID, member(buyer), map[string]any{"expert": func() map[string]any {
			x := profile("150", "cloud")
			x["skills"] = []string{"Rust"}
			return x
		}()}, &edited)
		if !slices.Equal(edited.Expert.Skills, []string{"Rust"}) {
			t.Fatalf("replaced skills %v", edited.Expert.Skills)
		}

		// What the shop could not show is refused, on publish and on edit alike.
		long := strings.Repeat("x", 41)
		many := make([]string, 21)
		for i := range many {
			many[i] = fmt.Sprintf("skill %d", i)
		}
		links := make([]string, 9)
		for i := range links {
			links[i] = fmt.Sprintf("https://example.com/%d", i)
		}
		for name, x := range map[string]map[string]any{
			"a skill past 40":          {"skills": []string{long}},
			"21 skills":                {"skills": many},
			"a skill with a newline":   {"skills": []string{"a\nb"}},
			"http":                     {"links": []string{"http://example.com"}},
			"no scheme":                {"links": []string{"example.com/ada"}},
			"javascript":               {"links": []string{"javascript:alert(1)"}},
			"no host":                  {"links": []string{"https:///path"}},
			"a login":                  {"links": []string{"https://ada:secret@example.com"}},
			"a space":                  {"links": []string{"https://example.com/a b"}},
			"a link past 200":          {"links": []string{"https://example.com/" + strings.Repeat("a", 190)}},
			"nine links":               {"links": links},
			"a protocol-relative link": {"links": []string{"//example.com"}},
		} {
			prof := profile("150", "cloud")
			for k, v := range x {
				prof[k] = v
			}
			if code, b, _ := e.req(http.MethodPatch, "/v1/marketplace/listings/"+l.ID, member(buyer), "", map[string]any{"expert": prof}); code != http.StatusBadRequest {
				t.Errorf("edit with %s = %d (%s), want 400", name, code, b)
			}
			other := who{org: seller, user: "u_" + name}
			if code, b, _ := e.req(http.MethodPost, "/v1/marketplace/listings", other, "", map[string]any{"kind": KindExpert, "title": "X",
				"recipient": "w", "expert": prof}); code != http.StatusBadRequest {
				t.Errorf("publish with %s = %d (%s), want 400", name, code, b)
			}
		}
		// The limits themselves are allowed.
		at := profile("150", "cloud")
		at["skills"] = many[:20]
		at["links"] = links[:8]
		at["skills"] = append(at["skills"].([]string)[:19], strings.Repeat("é", 40))
		e.must(200, http.MethodPatch, "/v1/marketplace/listings/"+l.ID, member(buyer), map[string]any{"expert": at}, &edited)
		if len(edited.Expert.Skills) != 20 || len(edited.Expert.Links) != 8 {
			t.Fatalf("at the limits: %d skills %d links", len(edited.Expert.Skills), len(edited.Expert.Links))
		}
	})
}

// A store from before skills and links reads each expert with none.
func TestAnExpertFromBeforeSkillsReadsWithNone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		l := e.add(listing{name: "old"})
		st := mounted.State.store
		if _, err := st.db.Exec(`UPDATE listings SET skills = '', links = '' WHERE id = $1`, l.ID); err != nil {
			t.Fatal(err)
		}
		got := e.shop("?kind=expert").Listings[0].Expert
		if got.Skills == nil || got.Links == nil || len(got.Skills)+len(got.Links) != 0 {
			t.Fatalf("an old expert reads skills %#v links %#v", got.Skills, got.Links)
		}
	})
}

// ── the rewards program ──────────────────────────────────────────────────────

func TestAPublicRewardProfileIsShownAndNothingElse(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		prog := serveProgram(t)
		ada, bob, cy := e.add(listing{name: "ada", at: 1000}), e.add(listing{name: "bob", at: 1001}), e.add(listing{name: "cy", at: 1002})
		prog.set(func(m map[string]client.RewardPerson) {
			rp := visible("u_ada", 2, 640, "million-token-club", "billionaire", "first-100k", "first-merge")
			rp.Badges = []client.RewardBadge{{ID: "billionaire", Name: "Billionaire", Pillar: "use", Rarity: "epic"},
				{ID: "million-token-club", Name: "Million Token Club", Pillar: "use", Rarity: "rare"},
				{ID: "first-merge", Name: "First Merge", Pillar: "contribute", Rarity: "common"}}
			rp.LevelName = "Shipper"
			rp.Pillars = client.PillarCounts(map[string]int{"use": 3, "share": 0, "community": 1, "contribute": 2, "expert": 0})
			rp.Contributions = []client.RewardContribution{{Repo: "hanzoai/base", Event: "github.pr.merged", Count: 2}}
			rp.Usage = []client.RewardUsage{{Family: "zen", Requests: 7, Tokens: 123456}}
			m["u_ada"] = rp
			m["u_cy"] = visible("u_cy", 0, 10) // public, and holds no token badge
			// bob's profile is private: the program says nothing of him.
		})

		shop := e.shop("?kind=expert")
		by := map[string]*Expert{}
		for _, l := range shop.Listings {
			by[l.Title] = l.Expert
		}
		a := by["ada"].Reward
		if a == nil || a.Level != 2 || a.LevelName != "Shipper" || a.Points != 640 {
			t.Fatalf("ada's reward = %+v", a)
		}
		// The highest token badge held, never a count: the order held in is not the order of the tiers.
		if a.Tier == nil || a.Tier.ID != "billionaire" || a.Tier.Label != "1B+" {
			t.Fatalf("ada's tier = %+v, want billionaire 1B+", a.Tier)
		}
		if len(a.Badges) != 3 || a.Badges[0].ID != "billionaire" || a.Badges[0].Rarity != "epic" || a.Badges[2].Pillar != "contribute" {
			t.Fatalf("ada's badges = %+v", a.Badges)
		}
		if a.Pillars != (Pillars{Use: 3, Community: 1, Contribute: 2}) {
			t.Fatalf("ada's pillars = %+v", a.Pillars)
		}
		if c := by["cy"].Reward; c == nil || c.Tier != nil || c.Level != 0 {
			t.Fatalf("cy's reward = %+v, want a profile with no tier", c)
		}
		if by["bob"].Reward != nil {
			t.Fatalf("bob's private profile shows %+v", by["bob"].Reward)
		}

		// The wire carries the field names the storefront reads, and no more of the program.
		var raw struct {
			Listings []map[string]any `json:"listings"`
		}
		code, b, _ := e.req(http.MethodGet, "/v1/marketplace/shop?kind=expert&limit=1&sort=points", anyone, "", nil)
		if code != 200 || json.Unmarshal(b, &raw) != nil {
			t.Fatalf("%d %s", code, b)
		}
		reward := raw.Listings[0]["expert"].(map[string]any)["reward"].(map[string]any)
		for _, k := range []string{"level", "levelName", "points", "tier", "badges", "pillars"} {
			if _, ok := reward[k]; !ok {
				t.Errorf("reward lacks %q: %v", k, reward)
			}
		}
		if len(reward) != 6 {
			t.Errorf("reward has %d fields, want the six: %v", len(reward), reward)
		}
		if tier := reward["tier"].(map[string]any); len(tier) != 2 || tier["id"] != "billionaire" || tier["label"] != "1B+" {
			t.Errorf("tier = %v", tier)
		}
		// Nothing that is not the profile's own face: no subject, no records behind the score.
		_, whole, _ := e.req(http.MethodGet, "/v1/marketplace/shop?kind=expert", anyone, "", nil)
		for _, leak := range []string{"u_ada", "u_bob", "u_cy", `"held"`, `"subject"`, `"requests"`, `"tokens"`, "hanzoai/base", "github.pr.merged", "redeemable", "email", "123456"} {
			if strings.Contains(string(whole), leak) {
				t.Errorf("the shop shows %q: %s", leak, whole)
			}
		}
		var item, plain ShopListing
		e.must(200, http.MethodGet, "/v1/marketplace/shop/"+ada.ID, anyone, nil, &item)
		if item.Expert.Reward == nil || item.Expert.Reward.Points != 640 {
			t.Fatalf("the shop item shows %+v", item.Expert.Reward)
		}
		e.must(200, http.MethodGet, "/v1/marketplace/shop/"+bob.ID, anyone, nil, &plain)
		if plain.Expert.Reward != nil {
			t.Fatalf("a private profile shows on the shop item: %+v", plain.Expert.Reward)
		}
		// The expert's own listings read shows it too, beside who they are.
		var own listingPage
		e.must(200, http.MethodGet, "/v1/marketplace/listings", admin("org_ada"), nil, &own)
		if len(own.Listings) != 1 || own.Listings[0].Expert.Reward == nil || own.Listings[0].Expert.Reward.Level != 2 {
			t.Fatalf("own listings = %+v", own.Listings)
		}
		_ = cy

		// The program is asked as the platform, for the families usage is totalled on.
		prog.mu.Lock()
		defer prog.mu.Unlock()
		if len(prog.asks) == 0 || !slices.Equal(prog.fams[0], []string{"kai", "enso", "zen"}) {
			t.Fatalf("asked %v for %v", prog.asks, prog.fams)
		}
	})
}

// An answer is kept, silence is kept, and a program that is down, slow or not there
// leaves the field out and fails nothing.
func TestTheRewardsProgramIsAnAdditionAndNeverADependency(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		now := time.Unix(1_800_000_000, 0)
		mounted.State.expertise.now = func() time.Time { return now }
		e.add(listing{name: "ada", at: 1000})
		e.add(listing{name: "bob", at: 1001})

		// Not there at all: the dial fails, and the read is as it was.
		got := e.shop("?kind=expert")
		if got.Total != 2 || got.Listings[0].Expert.Reward != nil || got.Listings[1].Expert.Reward != nil {
			t.Fatalf("with no program: %+v", got)
		}

		prog := serveProgram(t)
		prog.set(func(m map[string]client.RewardPerson) { m["u_ada"] = visible("u_ada", 1, 100, "first-100k") })
		// The failed ask is not repeated at once: the shop does not wait on a dead program for each read.
		now = now.Add(rewardDown / 2)
		if e.shop("?kind=expert").Listings[1].Expert.Reward != nil || prog.asked() != 0 {
			t.Fatalf("a failed ask was repeated at once (asked %d)", prog.asked())
		}
		now = now.Add(rewardDown)
		first := e.shop("?kind=expert")
		if first.Listings[1].Expert.Reward == nil || prog.asked() != 1 {
			t.Fatalf("after the pause: reward %+v asked %d", first.Listings[1].Expert.Reward, prog.asked())
		}
		// An answer is kept for a minute, and so is a subject the program said nothing of.
		e.shop("?kind=expert")
		e.shop("?kind=expert&sort=level")
		if prog.asked() != 1 {
			t.Fatalf("a page asked the program again within its minute: %d asks", prog.asked())
		}
		now = now.Add(rewardKept + time.Second)
		e.shop("?kind=expert")
		if prog.asked() != 2 {
			t.Fatalf("after the minute the program was asked %d times, want 2", prog.asked())
		}

		// A program that answers an error: the read succeeds, the field is gone, and it is not hammered.
		e.settle()
		prog.mu.Lock()
		prog.status = http.StatusServiceUnavailable
		prog.mu.Unlock()
		before := prog.asked()
		for range 3 {
			got = e.shop("?kind=expert")
			if got.Total != 2 || got.Listings[1].Expert.Reward != nil {
				t.Fatalf("with the program failing: %+v", got)
			}
		}
		if n := prog.asked() - before; n != 1 {
			t.Fatalf("a failing program was asked %d times in a row, want 1", n)
		}
		// An order that reads it still answers, with nobody placed.
		if got = e.shop("?kind=expert&sort=level"); got.Total != 2 {
			t.Fatalf("level order with the program down: %+v", got)
		}
		if got = e.shop("?kind=expert&minLevel=1"); got.Total != 0 {
			t.Fatalf("a level filter with the program down kept %d, want none", got.Total)
		}
		// And it recovers.
		prog.mu.Lock()
		prog.status = 0
		prog.mu.Unlock()
		now = now.Add(rewardDown + time.Second)
		if e.shop("?kind=expert").Listings[1].Expert.Reward == nil {
			t.Fatal("the program came back and the shop did not notice")
		}
	})
}

// The fixtures of the orders and filters: six experts, the program's word about four.
func orderings(t *testing.T) (*env, *program) {
	t.Helper()
	e := newEnv(t)
	prog := serveProgram(t)
	e.add(listing{name: "ada", rate: "150", at: 1000, verified: true, langs: []string{"en", "es"}, jobs: []int{5, 5}})
	e.add(listing{name: "bob", rate: "80", at: 1001, langs: []string{"en"}, jobs: []int{4}})
	e.add(listing{name: "cy", at: 1002, langs: []string{"zh-cn"}})
	e.add(listing{name: "dee", rate: "200", at: 1003, langs: []string{"es"}, jobs: []int{0, 0, 0}})
	e.add(listing{name: "eli", rate: "120", at: 1004, langs: []string{"en", "zh-tw"}, jobs: []int{3}})
	e.add(listing{name: "fay", rate: "100", at: 1005, verified: true})
	e.add(listing{name: "gus", rate: "90", at: 1006}) // an expert the program has never heard of, with no jobs
	prog.set(func(m map[string]client.RewardPerson) {
		m["u_ada"] = visible("u_ada", 3, 900, "first-100k", "million-token-club", "billionaire")
		m["u_bob"] = visible("u_bob", 1, 150, "first-100k", "first-merge")
		m["u_cy"] = visible("u_cy", 3, 900, "first-100k", "million-token-club", "five-million")
		m["u_fay"] = visible("u_fay", 0, 10)
		// dee, eli and gus: private, or never made
	})
	return e, prog
}

func TestEveryOrderBothWaysPutsWhoCannotBePlacedLast(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e, _ := orderings(t)
		for _, tc := range []struct{ sort, dir, want string }{
			{"level", "desc", "cy ada bob fay gus eli dee"},
			{"level", "asc", "fay bob cy ada gus eli dee"},
			{"points", "desc", "cy ada bob fay gus eli dee"},
			{"points", "asc", "fay bob cy ada gus eli dee"},
			{"tokens", "desc", "ada cy bob fay gus eli dee"},
			{"tokens", "asc", "fay bob cy ada gus eli dee"},
			{"rating", "desc", "ada bob eli gus fay dee cy"},
			{"rating", "asc", "eli bob ada gus fay dee cy"},
			{"jobs", "desc", "dee ada eli bob gus fay cy"},
			{"jobs", "asc", "gus fay cy eli bob ada dee"},
			{"rate", "desc", "dee ada eli fay gus bob cy"},
			{"rate", "asc", "bob gus fay eli ada dee cy"},
			{"newest", "desc", "gus fay eli dee cy bob ada"},
			{"newest", "asc", "ada bob cy dee eli fay gus"},
			{"", "", "gus fay eli dee cy bob ada"},
			{"level", "", "cy ada bob fay gus eli dee"},
			{"", "asc", "ada bob cy dee eli fay gus"},
		} {
			q := "?kind=expert&sort=" + tc.sort + "&dir=" + tc.dir
			full := e.shop(q)
			if got := strings.Join(names(full), " "); got != tc.want || full.Total != 7 {
				t.Errorf("%s = %q (total %d), want %q", q, got, full.Total, tc.want)
				continue
			}
			// A page boundary never repeats or skips: pages of two are the whole.
			var paged []string
			for off := 0; off < 8; off += 2 {
				p := e.shop(fmt.Sprintf("%s&limit=2&offset=%d", q, off))
				if p.Total != 7 {
					t.Errorf("%s page %d total %d", q, off, p.Total)
				}
				paged = append(paged, names(p)...)
			}
			if got := strings.Join(paged, " "); got != tc.want {
				t.Errorf("%s in pages of two = %q, want %q", q, got, tc.want)
			}
		}
	})
}

func TestEveryFilterKeepsWhoMeetsIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e, _ := orderings(t)
		for q, want := range map[string]string{
			"language=en":                "gus fay eli bob ada",
			"language=es":                "dee ada",
			"language=zh":                "eli cy",
			"language=zh-cn":             "cy",
			"language=ZH-TW":             "eli",
			"language=fr":                "",
			"verified=true":              "fay ada",
			"verified=false":             "gus fay eli dee cy bob ada",
			"minLevel=1":                 "cy bob ada",
			"minLevel=3":                 "cy ada",
			"minLevel=4":                 "",
			"minLevel=0":                 "gus fay eli dee cy bob ada",
			"badge=million-token-club":   "cy ada",
			"badge=first-merge":          "bob",
			"badge=no-such-badge":        "",
			"tokenTier=100k":             "cy bob ada",
			"tokenTier=1m":               "cy ada",
			"tokenTier=5m":               "cy ada",
			"tokenTier=1b":               "ada",
			"tokenTier=1T":               "",
			"rateMin=100":                "fay eli dee ada",
			"rateMax=90":                 "gus bob",
			"rateMin=100&rateMax=150":    "fay eli ada",
			"rateMin=0":                  "gus fay eli dee bob ada",
			"rateMax=1000":               "gus fay eli dee bob ada",
			"rateMin=0.5&rateMax=80":     "bob",
			"language=en&minLevel=1":     "bob ada",
			"tokenTier=100k&rateMax=100": "bob",
			"verified=true&language=es":  "ada",
			"q=bob&minLevel=1":           "bob",
		} {
			got := e.shop("?kind=expert&" + q)
			if g := strings.Join(names(got), " "); g != want {
				t.Errorf("%s = %q, want %q", q, g, want)
			}
			if got.Total != len(got.Listings) {
				t.Errorf("%s: total %d for %d listed", q, got.Total, len(got.Listings))
			}
			// The facets stay the ordinary filters': the whole set of experts, however they were kept.
			if got.Facets.Kind[KindExpert] != 7 && !strings.HasPrefix(q, "q=") {
				t.Errorf("%s: facets %v changed with the new filters", q, got.Facets.Kind)
			}
		}
		// The total is the number kept, past the page.
		if p := e.shop("?kind=expert&language=en&limit=1&offset=1"); p.Total != 5 || len(p.Listings) != 1 || p.Listings[0].Title != "fay" {
			t.Fatalf("a page of the kept = %v of %d", names(p), p.Total)
		}
		if p := e.shop("?kind=expert&language=en&offset=9"); p.Total != 5 || len(p.Listings) != 0 {
			t.Fatalf("past the end = %v of %d", names(p), p.Total)
		}
		// An ordinary filter and a new one together.
		if got := e.shop("?kind=expert&area=cloud&minLevel=3&sort=points&dir=asc"); strings.Join(names(got), " ") != "cy ada" {
			t.Fatalf("combined = %v", names(got))
		}
	})
}

func TestAValueNoParameterTakesIsARefusal(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e, _ := orderings(t)
		for q, why := range map[string]string{
			"kind=expert&sort=cleverness":         "sort is one of",
			"kind=expert&dir=sideways":            "dir is asc or desc",
			"kind=expert&language=English":        "language is a BCP 47 tag",
			"kind=expert&verified=maybe":          "verified is true or false",
			"kind=expert&minLevel=-1":             "minLevel is a whole number",
			"kind=expert&minLevel=high":           "minLevel is a whole number",
			"kind=expert&minLevel=1.5":            "minLevel is a whole number",
			"kind=expert&badge=Not%20A%20Badge":   "badge is a badge id",
			"kind=expert&tokenTier=2m":            "tokenTier is one of",
			"kind=expert&rateMin=cheap":           "rateMin is a decimal USD amount",
			"kind=expert&rateMax=-5":              "rateMax is a decimal USD amount",
			"kind=expert&rateMin=200&rateMax=100": "rateMin is more than rateMax",
			"kind=expert&product=cooking":         "product is one of",
			"kind=expert&product=base&sort=level": "leave sort out",
			"sort=level":                          "are for kind=expert",
			"kind=agent&minLevel=1":               "are for kind=expert",
			"language=en":                         "are for kind=expert",
			"kind=tool&product=base":              "are for kind=expert",
			"kind=expert&limit=500&sort=level":    "limit is at most",
			"kind=expert&sort=level&offset=-1":    "offset is not negative",
		} {
			code, b, _ := e.req(http.MethodGet, "/v1/marketplace/shop?"+q, anyone, "", nil)
			if code != http.StatusBadRequest || !strings.Contains(string(b), why) {
				t.Errorf("shop?%s = %d %s, want a 400 saying %q", q, code, b, why)
			}
		}
		// What is not asked for is the ordinary read, and costs nothing more: it never reaches the roster.
		for _, in := range []shopQuery{{Kind: KindExpert}, {Kind: KindExpert, Area: "cloud", Q: "x", Rating: 3, Limit: 5, Offset: 1}, {Kind: KindAgent},
			{Kind: KindExpert, Price: "free", Category: "x", Seller: "y"}} {
			if r, err := rosterOf(context.Background(), &in); r != nil || err != nil {
				t.Errorf("%+v is an ordinary read, got a roster %v %v", in, r, err)
			}
		}
	})
}

// What a private reward profile has is nowhere in a read, whatever the order or filter.
func TestAPrivateProfileNeverLeaks(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e, _ := orderings(t)
		for _, q := range []string{"", "&sort=level", "&sort=tokens&dir=asc", "&sort=points", "&minLevel=0", "&tokenTier=100k", "&product=base", "&limit=1&offset=3"} {
			var raw struct {
				Listings []map[string]any `json:"listings"`
			}
			code, b, _ := e.req(http.MethodGet, "/v1/marketplace/shop?kind=expert"+q, anyone, "", nil)
			if code != 200 || json.Unmarshal(b, &raw) != nil {
				t.Fatalf("%s: %d %s", q, code, b)
			}
			for _, l := range raw.Listings {
				ex := l["expert"].(map[string]any)
				if l["title"] == "dee" || l["title"] == "eli" || l["title"] == "gus" {
					for _, k := range []string{"reward", "person", "self"} {
						if _, ok := ex[k]; ok {
							t.Errorf("%s: %v shows %q: %v", q, l["title"], k, ex[k])
						}
					}
				}
				if _, ok := ex["person"]; ok {
					t.Errorf("%s: the shop shows who %v is", q, l["title"])
				}
			}
			for _, sub := range []string{"u_ada", "u_bob", "u_cy", "u_dee", "u_eli", "u_fay", "u_gus"} {
				if strings.Contains(string(b), sub) {
					t.Errorf("%s: the shop shows the subject %s", q, sub)
				}
			}
		}
	})
}

// ── the score in each product ────────────────────────────────────────────────

func TestAScoreIsTheSumOfWhatIsRecordedAndShowsItsParts(t *testing.T) {
	w := Weights{Usage: 10, UsageUnit: 100_000, Badge: 20, PullRequest: 25, Bounty: 50, Job: 10, Unrated: 0.5}
	expert := Listing{Expert: &Expert{Areas: []string{"base", "models"}}}
	parts := func(scores []Score, product string) map[string]float64 {
		for _, s := range scores {
			if s.Product == product {
				return asMap(s.Parts)
			}
		}
		return nil
	}
	score := func(scores []Score, product string) float64 {
		for _, s := range scores {
			if s.Product == product {
				return s.Score
			}
		}
		return 0
	}
	eq := func(got, want map[string]float64) bool {
		if len(got) != len(want) {
			return false
		}
		for k, v := range want {
			if got[k] != v {
				return false
			}
		}
		return true
	}

	// Nothing recorded, nothing scored.
	if got := w.of(Listing{Expert: &Expert{Areas: []string{"base"}}}, tally{}, nil); len(got) != 0 {
		t.Fatalf("nothing recorded scores %+v", got)
	}

	// Each part alone, from its one record.
	person := &client.RewardPerson{
		Held:          []string{"try-base", "zen-pioneer", "first-merge", "try-cloud-never-defined"},
		Contributions: []client.RewardContribution{{Repo: "hanzoai/base", Event: "github.pr.merged", Count: 2}, {Repo: "hanzoai/base", Event: "bounty.completed", Count: 1}, {Repo: "someone/else", Event: "github.pr.merged", Count: 9}, {Repo: "hanzoai/bot", Event: "github.star", Count: 9}},
		Usage:         []client.RewardUsage{{Family: "zen", Requests: 10, Tokens: 900_000}, {Family: "kai", Tokens: 0}},
	}
	got := w.of(Listing{Expert: &Expert{}}, tally{}, person)
	if p := parts(got, "base"); !eq(p, map[string]float64{"badges": 20, "contributions": 100}) {
		t.Errorf("base = %v, want a badge (20) and 2 PRs and a bounty (100)", p)
	}
	// 900,000 tokens is 9 units of 100,000: 10 x log10(1 + 9) = 10.
	if p := parts(got, "zen"); !eq(p, map[string]float64{"usage": 10, "badges": 20}) {
		t.Errorf("zen = %v, want usage 10 and a badge 20", p)
	}
	if score(got, "zen") != 30 || score(got, "base") != 120 {
		t.Errorf("scores zen %v base %v", score(got, "zen"), score(got, "base"))
	}
	if parts(got, "kai") != nil || parts(got, "bot") != nil || parts(got, "cloud") != nil {
		t.Errorf("a product with no record scored: %+v", got)
	}
	// Usage exists for the three model families alone: a product that records none has no usage part,
	// and so does a family nobody used.
	for _, s := range got {
		if s.Parts.Usage != 0 && s.Product != "zen" {
			t.Errorf("%s has a usage part", s.Product)
		}
	}

	// Jobs weigh the stars a buyer gave; a listing nobody rated weighs the unrated factor.
	rated := tally{completed: 4, ratings: 2, stars: 9} // a mean of 4.5 of 5: 0.9
	got = w.of(expert, rated, nil)
	if p := parts(got, "base"); !eq(p, map[string]float64{"jobs": 36}) {
		t.Errorf("rated jobs = %v, want 4 x 10 x 0.9", p)
	}
	// An area maps to its products: models is kai, enso and zen; base is only base.
	for _, id := range []string{"kai", "enso", "zen"} {
		if p := parts(got, id); !eq(p, map[string]float64{"jobs": 36}) {
			t.Errorf("%s jobs = %v", id, p)
		}
	}
	if parts(got, "commerce") != nil || parts(got, "dev") != nil {
		t.Errorf("an area outside the expert's scored: %+v", got)
	}
	if p := parts(w.of(expert, tally{completed: 2}, nil), "base"); !eq(p, map[string]float64{"jobs": 10}) {
		t.Errorf("unrated jobs = %v, want 2 x 10 x 0.5", p)
	}
	if got = w.of(Listing{Expert: &Expert{Areas: []string{"freelance"}}}, rated, nil); len(got) != 0 {
		t.Errorf("freelance is no product: %+v", got)
	}
	if got = w.of(Listing{Expert: &Expert{Areas: []string{"configure"}}}, tally{completed: 1, ratings: 1, stars: 5}, nil); len(got) != 2 ||
		got[0].Product != "dev" || got[1].Product != "team" {
		t.Errorf("configure is dev and team: %+v", got)
	}

	// All four together are the contract's example, to the tenth.
	whole := w.of(Listing{Expert: &Expert{Areas: []string{"models"}}}, tally{completed: 5, ratings: 20, stars: 49}, &client.RewardPerson{
		Held:          []string{"zen-pioneer"},
		Contributions: []client.RewardContribution{{Repo: "zenlm/zen", Event: "github.pr.merged", Count: 4}},
		Usage:         []client.RewardUsage{{Family: "zen", Tokens: 1_000_000_000}},
	})
	if p := parts(whole, "zen"); !eq(p, map[string]float64{"usage": 40, "badges": 20, "contributions": 100, "jobs": 24.5}) || score(whole, "zen") != 184.5 {
		t.Errorf("zen = %v (%v), want 40 + 20 + 100 + 24.5 = 184.5", p, score(whole, "zen"))
	}

	// The weights are the only knob: a part that weighs nothing is not listed.
	w.PullRequest, w.Bounty, w.Badge = 0, 0, 0
	if got = w.of(Listing{Expert: &Expert{}}, tally{}, person); parts(got, "base") != nil || len(got) != 1 || got[0].Product != "zen" {
		t.Errorf("with the weights at zero: %+v", got)
	}
	w.Job = 1
	if p := parts(w.of(expert, rated, nil), "base"); !eq(p, map[string]float64{"jobs": 3.6}) {
		t.Errorf("with a job at 1 = %v", p)
	}
}

// The tables hold together, and name what the program really has.
func TestTheProductTablesAreConsistent(t *testing.T) {
	if want := []string{"dev", "bot", "team", "base", "commerce", "cloud", "agents", "kai", "enso", "zen", "creative"}; !slices.Equal(Products, want) {
		t.Fatalf("products %v, want %v", Products, want)
	}
	repos, badges := map[string]string{}, map[string]string{}
	for _, p := range products {
		if strings.TrimSpace(p.label) == "" {
			t.Errorf("%s has no label", p.id)
		}
		for _, a := range p.areas {
			if !slices.Contains(Areas, a) {
				t.Errorf("%s names the area %q, which is not one", p.id, a)
			}
		}
		for _, r := range p.repos {
			owner, name, ok := strings.Cut(r, "/")
			if !ok || owner == "" || name == "" || strings.Contains(name, "/") || r != strings.ToLower(r) {
				t.Errorf("%s names the repository %q, which is not lowercase owner/name", p.id, r)
			}
			if other, dup := repos[r]; dup {
				t.Errorf("%q counts for %s and %s", r, other, p.id)
			}
			repos[r] = p.id
		}
		for _, b := range p.badges {
			if other, dup := badges[b]; dup {
				t.Errorf("badge %q is for %s and %s", b, other, p.id)
			}
			badges[b] = p.id
		}
		if p.family != "" && p.family != p.id {
			t.Errorf("%s meters usage on the family %q", p.id, p.family)
		}
	}
	for _, a := range Areas {
		if a != "freelance" && !slices.ContainsFunc(products, func(p product) bool { return slices.Contains(p.areas, a) }) {
			t.Errorf("the area %q counts for no product", a)
		}
	}
	if !slices.Equal(families(), []string{"kai", "enso", "zen"}) {
		t.Errorf("usage is metered on %v", families())
	}
	if want := []string{"100k", "1m", "5m", "10m", "100m", "1b", "10b", "100b", "1t"}; !slices.Equal(tierParams(), want) {
		t.Errorf("tiers %v, want %v", tierParams(), want)
	}

	// Every badge named is one the program's seed defines.
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	raw, err := locus.System("reward", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = raw.Close() })
	db, err := orm.AdaptSQL(raw)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := rewardmod.Mount(zip.New(zip.Config{AppName: "reward"}), rewardmod.Config{DB: db})
	if err != nil {
		t.Fatal(err)
	}
	progs, err := eng.Programs(context.Background())
	if err != nil || len(progs) == 0 {
		t.Fatalf("programs %v %v", progs, err)
	}
	seeded := map[string]bool{}
	for _, b := range progs[0].Badges {
		seeded[b.ID] = true
	}
	for b := range badges {
		if !seeded[b] {
			t.Errorf("the table names the badge %q, which the program does not define", b)
		}
	}
	for _, tr := range tiers {
		if !seeded[tr.ID] {
			t.Errorf("the tier %q is not a badge the program defines", tr.ID)
		}
	}
}

// Four experts in one product, one of them private, and the ranking among them.
func TestTheBestOfAProductAreMarkedAndTheProductOrdersTheShop(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		prog := serveProgram(t)
		e.add(listing{name: "e1", areas: []string{"base"}, at: 1001, jobs: []int{5, 5}})
		e.add(listing{name: "e2", areas: []string{"cloud"}, at: 1002})
		e.add(listing{name: "e3", areas: []string{"base"}, at: 1003, jobs: []int{5, 5, 5, 5, 5}}) // private
		e.add(listing{name: "e4", areas: []string{"cloud"}, at: 1004})
		e.add(listing{name: "e5", areas: []string{"cloud"}, at: 1005}) // public, nothing recorded
		e.add(listing{name: "e6", areas: []string{"cloud"}, at: 1006}) // private, nothing recorded
		base := func(prs int) []client.RewardContribution {
			return []client.RewardContribution{{Repo: "hanzoai/base", Event: "github.pr.merged", Count: prs}}
		}
		prog.set(func(m map[string]client.RewardPerson) {
			p1 := visible("u_e1", 1, 10)
			p1.Contributions = base(4)
			p2 := visible("u_e2", 1, 10, "try-base", "zen-pioneer")
			p2.Contributions = base(3)
			p2.Usage = []client.RewardUsage{{Family: "zen", Tokens: 900_000}}
			p4 := visible("u_e4", 1, 10)
			p4.Contributions = base(1)
			m["u_e1"], m["u_e2"], m["u_e4"], m["u_e5"] = p1, p2, p4, visible("u_e5", 1, 10)
			// e3 and e6 hold the same work behind a private profile: the program says nothing of them.
		})

		by := map[string]*Expert{}
		for _, l := range e.shop("?kind=expert").Listings {
			by[l.Title] = l.Expert
		}
		scores := func(x *Expert) string {
			var out []string
			for _, s := range x.Products {
				out = append(out, fmt.Sprintf("%s=%v", s.Product, s.Score))
			}
			return strings.Join(out, ",")
		}
		// e1: 4 PRs (100) + 2 five-star jobs in base (20). e2: 3 PRs (75) + a badge (20); zen 10 + 20.
		// e3 is private: its five jobs alone (50). e4: one PR (25). e5 and e6: nothing.
		for name, want := range map[string]string{"e1": "base=120", "e2": "base=95,zen=30", "e3": "base=50", "e4": "base=25", "e5": "", "e6": ""} {
			if got := scores(by[name]); got != want {
				t.Errorf("%s scores %q, want %q", name, got, want)
			}
		}
		if p := asMap(by["e1"].Products[0].Parts); p["contributions"] != 100 || p["jobs"] != 20 || len(p) != 2 {
			t.Errorf("e1's parts = %v", p)
		}
		if p := asMap(by["e3"].Products[0].Parts); len(p) != 1 || p["jobs"] != 50 {
			t.Errorf("a private expert's parts = %v, want jobs alone", p)
		}
		if p := asMap(by["e2"].Products[1].Parts); p["usage"] != 10 || p["badges"] != 20 {
			t.Errorf("e2's zen parts = %v", p)
		}
		// Top three of all experts in a product: e4 scores in base and is fourth.
		for name, want := range map[string][]string{"e1": {"base"}, "e2": {"base", "zen"}, "e3": {"base"}, "e4": nil, "e5": nil, "e6": nil} {
			if !slices.Equal(by[name].Top, want) {
				t.Errorf("%s ranks top in %v, want %v", name, by[name].Top, want)
			}
		}

		// The product orders the shop: the best first, those with none last, and nobody dropped.
		for q, want := range map[string]string{
			"product=base":            "e1 e2 e3 e4 e6 e5",
			"product=zen":             "e2 e6 e5 e4 e3 e1",
			"product=base&dir=asc":    "e4 e3 e2 e1 e6 e5",
			"product=kai":             "e6 e5 e4 e3 e2 e1",
			"product=base&limit=2":    "e1 e2",
			"product=base&offset=4":   "e6 e5",
			"product=base&minLevel=1": "e1 e2 e4 e5",
		} {
			got := e.shop("?kind=expert&" + q)
			if g := strings.Join(names(got), " "); g != want {
				t.Errorf("%s = %q, want %q", q, g, want)
			}
			if q == "product=base" && got.Total != 6 {
				t.Errorf("%s: total %d, want every expert kept", q, got.Total)
			}
		}

		// A change in the weights is a change in the ranking, once it is read again.
		prior := weights
		t.Cleanup(func() { weights = prior })
		weights.PullRequest, weights.Job = 0, 10
		e.settle()
		by = map[string]*Expert{}
		for _, l := range e.shop("?kind=expert").Listings {
			by[l.Title] = l.Expert
		}
		if got := scores(by["e1"]); got != "base=20" || scores(by["e4"]) != "" || len(by["e4"].Top) != 0 {
			t.Errorf("with pull requests worth nothing: e1 %q e4 %q top %v", got, scores(by["e4"]), by["e4"].Top)
		}
		if !slices.Equal(by["e3"].Top, []string{"base"}) || !slices.Equal(by["e1"].Top, []string{"base"}) {
			t.Errorf("tops with the new weights: e1 %v e3 %v", by["e1"].Top, by["e3"].Top)
		}
	})
}

// The ranking is of every public expert, not of the page, and a listing that is not
// in it is scored alone and ranks in nothing.
func TestTopIsOfAllExpertsAndNotThePage(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		for i := range 5 {
			e.add(listing{name: fmt.Sprintf("x%d", i), areas: []string{"base"}, at: int64(1000 + i), jobs: make([]int, 5-i)})
		}
		// x0 has five unrated jobs and x4 one, at half a stars-fifth each: 25, 20, 15, 10 and 5.
		// The fourth is not among the best three of all, though on a page of its own it is the only one.
		var page Shop
		e.must(200, http.MethodGet, "/v1/marketplace/shop?kind=expert&limit=1&offset=3&sort=newest&dir=asc", anyone, nil, &page)
		if len(page.Listings) != 1 || page.Listings[0].Title != "x3" || len(page.Listings[0].Expert.Top) != 0 {
			t.Fatalf("the fourth: %v top %v", names(page), page.Listings[0].Expert.Top)
		}
		e.must(200, http.MethodGet, "/v1/marketplace/shop?kind=expert&limit=1&sort=newest&dir=asc", anyone, nil, &page)
		if page.Listings[0].Title != "x0" || !slices.Equal(page.Listings[0].Expert.Top, []string{"base"}) {
			t.Fatalf("x0 ranks top in %v on a page by itself, want base", page.Listings[0].Expert.Top)
		}

		// An expert whose listing is private is scored alone, from its own jobs, and ranks in nothing.
		pr := e.add(listing{name: "solo", areas: []string{"base"}, at: 2000, jobs: []int{5, 5, 5, 5, 5, 5, 5, 5}})
		if _, err := mounted.State.store.db.Exec(`UPDATE listings SET public = 0 WHERE id = $1`, pr.ID); err != nil {
			t.Fatal(err)
		}
		e.settle()
		var own listingPage
		e.must(200, http.MethodGet, "/v1/marketplace/listings?kind=expert", admin("org_solo"), nil, &own)
		if x := own.Listings[0].Expert; len(x.Products) != 1 || x.Products[0].Score != 80 || len(x.Top) != 0 {
			t.Fatalf("a private listing scores %+v top %v, want base=80 and no top", x.Products, x.Top)
		}
		var board Shop
		e.must(200, http.MethodGet, "/v1/marketplace/shop?kind=expert&product=base", anyone, nil, &board)
		if board.Total != 5 || board.Listings[0].Title != "x0" {
			t.Fatalf("the shop shows the private expert: %v", names(board))
		}
	})
}

// As many experts as the shop ranks in memory are ordered, past the limits of one
// query; one more is a refusal to order, not a slow read.
func TestTooManyExpertsToOrderIsARefusal(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		e := newEnv(t)
		st := mounted.State.store
		insert := func(from, to int) {
			t.Helper()
			tx, err := st.db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			for i := from; i < to; i++ {
				person := fmt.Sprintf("u_%05d", i)
				if _, err := tx.Exec(`INSERT INTO listings (id, publisher_org, kind, tool, ref, title, price, public, created_at, rate, areas, languages,
				timezone, availability, person, folded) VALUES ($1, $2, 'expert', $3, $4, 'x', '0', 1, $5, '1000000000000000000', 'cloud', 'en', 'UTC', 'a', $6, 'x')`,
					fmt.Sprintf("lst_%05d", i), fmt.Sprintf("org%05d", i), person, person, 1000+i, person); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		}
		insert(0, maxRoster)
		got := e.shop("?kind=expert&sort=jobs&limit=2&offset=" + fmt.Sprint(maxRoster-2))
		if got.Total != maxRoster || len(got.Listings) != 2 || got.Listings[1].Title != "x" {
			t.Fatalf("ordering %d experts = %d of %d", maxRoster, len(got.Listings), got.Total)
		}
		// Every one of them is ranked too: a page of two is told it ranks in nothing, by the same ranking.
		if x := got.Listings[0].Expert; len(x.Top) != 0 {
			t.Fatalf("an expert with no record ranks in %v", x.Top)
		}
		insert(maxRoster, maxRoster+1)
		if code, b, _ := e.req(http.MethodGet, "/v1/marketplace/shop?kind=expert&sort=rate", anyone, "", nil); code != http.StatusBadRequest || !strings.Contains(string(b), "narrow") {
			t.Fatalf("ordering %d experts = %d %s, want a 400", maxRoster+1, code, b)
		}
		// A narrower search is fine, and so is the ordinary read.
		if got := e.shop("?kind=expert&sort=rate&q=x&area=cloud&seller=org00007"); got.Total != 1 {
			t.Fatalf("a narrowed order = %d", got.Total)
		}
		if got := e.shop("?kind=expert&limit=3"); got.Total != maxRoster+1 || len(got.Listings) != 3 {
			t.Fatalf("the ordinary read = %d of %d", len(got.Listings), got.Total)
		}
	})
}
