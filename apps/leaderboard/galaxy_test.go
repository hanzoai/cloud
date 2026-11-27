package leaderboard

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// The galaxy's world: eight builders and the platform's own held org.
//
//	orion-belt  98,010 points: level 99, the top band
//	andromeda    1,000 points: level 10, the second band
//	vegastar       810 points: level 9, the first band's top
//	rigel           10 points: level 1; listed itself as "Rigel Labs"
//	procyon          5 points; withdrew from the public listing
//	altair, deneb, capella: nothing
//
// Who referred whom, oldest first, with the edges a star must not draw marked.
var galaxyEdges = []client.ReferralEdge{
	{Referrer: "orion-belt", Referee: "andromeda"},
	{Referrer: "orion-belt", Referee: "vegastar"},
	{Referrer: "andromeda", Referee: "rigel"},
	{Referrer: "vegastar", Referee: "vegastar"}, // a referral of oneself
	{Referrer: "admin", Referee: "deneb"},       // a held org is no star
	{Referrer: "orion-belt", Referee: "ghost"},  // an org IAM does not hold
	{Referrer: "rigel", Referee: "orion-belt"},  // would close a loop: rigel > andromeda > orion-belt
	{Referrer: "procyon", Referee: "andromeda"}, // andromeda already has its referrer: the first claim stands
	{Referrer: "altair", Referee: "procyon"},
}

// centerRows answers the galaxy's one read of the last day.
func centerRows(tokens, orgs uint64) func(string, []any) []map[string]any {
	return func(sql string, _ []any) []map[string]any {
		if strings.Contains(sql, "uniqExactIf") {
			return []map[string]any{{"total_tokens": tokens, "orgs": orgs}}
		}
		return nil
	}
}

// galaxyWorld stands in every source the galaxy reads, at noon, and mounts the board.
func galaxyWorld(t *testing.T) *zip.App {
	t.Helper()
	at(t)
	orgs := []iamOrg{{"admin", "2025-01-01T00:00:00Z"}}
	for i, o := range []string{"orion-belt", "andromeda", "vegastar", "rigel", "procyon", "altair", "deneb", "capella"} {
		orgs = append(orgs, iamOrg{o, time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)})
	}
	world(t, orgs, map[string]uint64{"admin": 900_000_000_000}, nil, nil)
	ds := centerRows(123_456_789, 7)
	installFakeDS(t, func(sql string, args []any) []map[string]any {
		if rows := usageRows(map[string]uint64{"admin": 900_000_000_000})(sql, args); rows != nil {
			return rows
		}
		return ds(sql, args)
	})
	real := referralEdgesFn
	referralEdgesFn = func(_ context.Context, in *client.ReferralEdgesIn) (*client.ReferralEdgeList, error) {
		out := &client.ReferralEdgeList{Edges: []client.ReferralEdge{}}
		if in.Offset < len(galaxyEdges) {
			out.Edges = galaxyEdges[in.Offset:min(len(galaxyEdges), in.Offset+in.Limit)]
		}
		return out, nil
	}
	t.Cleanup(func() { referralEdgesFn = real })
	// The reward program is not mounted here: its table is the seeded one, until a test says otherwise.
	program := rewardLevelsFn
	rewardLevelsFn = func(context.Context) (*client.RewardLevelTable, error) {
		return nil, errors.New("reward is not mounted")
	}
	t.Cleanup(func() { rewardLevelsFn = program })
	app := mountApp(t)
	for org, points := range map[string]int{"orion-belt": 98_010, "andromeda": 1_000, "vegastar": 810, "rigel": 10, "procyon": 5} {
		if err := mountedStore.PutBountyAward(context.Background(), bountyAward{ID: "b-" + org, Org: org, Kind: "bounty", Points: points, CreatedAt: 1}); err != nil {
			t.Fatalf("award %s: %v", org, err)
		}
	}
	seedOrgOptin(t, "rigel", "Rigel Labs", true)
	seedOrgOptin(t, "procyon", "", false)
	return app
}

// galaxyOf reads GET /v1/leaderboard/galaxy as the headers' caller, or as nobody.
func galaxyOf(t *testing.T, app *zip.App, headers map[string]string) (GalaxyView, string) {
	t.Helper()
	code, body := doGet(t, app, "/v1/leaderboard/galaxy", headers)
	if code != http.StatusOK {
		t.Fatalf("galaxy: %d %s", code, body)
	}
	var v GalaxyView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("galaxy body: %v: %s", err, body)
	}
	return v, string(body)
}

// starAt is the index of the star whose mask is mask.
func starAt(t *testing.T, v GalaxyView, mask string) int {
	t.Helper()
	i := slices.Index(v.Stars.Mask, mask)
	if i < 0 {
		t.Fatalf("no star named %q in %q", mask, v.Stars.Mask)
	}
	return i
}

func TestTheSeededTableIsTenBandsOfTenLevels(t *testing.T) {
	tab := seeded()
	for _, c := range []struct{ points, band int }{
		{-5, 0}, {0, 0}, {9, 0}, {10, 0}, {39, 0}, {40, 0},
		{810, 0}, {999, 0}, // level 9 is the top of the first band
		{1000, 1}, {1999, 1}, {3999, 1}, {4000, 2}, {8999, 2},
		{9000, 3}, {16000, 4}, {25000, 5}, {36000, 6}, {49000, 7}, {64000, 8}, {81000, 9},
		{98009, 9}, {98010, 9}, {1_000_000_000, 9},
	} {
		if got := tab.bandOf(c.points); got != c.band {
			t.Errorf("bandOf(%d) = %d, want %d", c.points, got, c.band)
		}
	}
	if len(tab.bands) != 10 || tab.bands[0] != "Photon" || tab.bands[9] != "Singularity" || len(tab.from) != 100 {
		t.Errorf("seeded table: %d levels, bands %v", len(tab.from), tab.bands)
	}
	if tab.from[10] != 1000 || tab.from[50] != 25000 || tab.from[99] != 98010 {
		t.Errorf("level n must begin at 10·n² points: %v", tab.from)
	}
}

func TestTheProgramsTableIsReadAsItIs(t *testing.T) {
	levels := []client.RewardLevel{{Level: 1, Band: "Alpha", From: 0}, {Level: 2, Band: "Alpha", From: 100}, {Level: 3, Band: "Beta", From: 1000}, {Level: 4, Band: "Gamma", From: 5000}, {Level: 5, Band: "Gamma", From: 9000}}
	tab, ok := tableOf(levels)
	if !ok || !slices.Equal(tab.bands, []string{"Alpha", "Beta", "Gamma"}) {
		t.Fatalf("table: %+v %v", tab, ok)
	}
	for points, want := range map[int]int{0: 0, 99: 0, 100: 0, 999: 0, 1000: 1, 4999: 1, 5000: 2, 1_000_000: 2} {
		if got := tab.bandOf(points); got != want {
			t.Errorf("bandOf(%d) = %d, want %d", points, got, want)
		}
	}
	// A table that is none is not read: nothing, a first level above 0 points, a level that
	// does not begin above the last, and a level with no band.
	for name, bad := range map[string][]client.RewardLevel{
		"none":        nil,
		"above zero":  {{Level: 1, Band: "A", From: 5}},
		"not above":   {{Level: 1, Band: "A", From: 0}, {Level: 2, Band: "B", From: 0}},
		"going down":  {{Level: 1, Band: "A", From: 0}, {Level: 2, Band: "B", From: 100}, {Level: 3, Band: "C", From: 50}},
		"without one": {{Level: 1, Band: "A", From: 0}, {Level: 2, Band: "", From: 100}},
	} {
		if _, ok := tableOf(bad); ok {
			t.Errorf("%s: read as a table", name)
		}
	}
}

func TestSizeIsAFactorOfTwo(t *testing.T) {
	for points, want := range map[int]int{-3: 0, 0: 0, 1: 1, 2: 2, 3: 2, 4: 3, 7: 3, 8: 4, 1000: 10, 98010: 17} {
		if got := size(points); got != want {
			t.Errorf("size(%d) = %d, want %d", points, got, want)
		}
	}
}

func TestEveryRankedOrgIsAStarAndNoOtherIs(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyWorld(t)
		v, _ := galaxyOf(t, app, nil)
		s := v.Stars
		// Eight builders; the platform's own org is held and is not on the line.
		if !v.Available || len(s.ID) != 8 {
			t.Fatalf("stars: %+v", v)
		}
		for name, col := range map[string]int{"points": len(s.Points), "band": len(s.Band), "sponsor": len(s.Sponsor), "mask": len(s.Mask)} {
			if col != 8 {
				t.Errorf("%s has %d values for 8 stars", name, col)
			}
		}
		// The same line the standing ranks: points descending, then the earlier signup.
		st, _ := standing(t, app, nil)
		if st.Total != len(s.ID) {
			t.Errorf("the standing ranks %d, the galaxy draws %d", st.Total, len(s.ID))
		}
		want := []int{98_010, 1_000, 810, 10, 5, 0, 0, 0}
		if !slices.Equal(s.Points, want) {
			t.Errorf("points = %v, want %v", s.Points, want)
		}
		wantBand := []int{9, 1, 0, 0, 0, 0, 0, 0}
		if !slices.Equal(s.Band, wantBand) {
			t.Errorf("bands = %v, want %v", s.Band, wantBand)
		}
		if !slices.Equal(v.Bands, bandNames[:]) {
			t.Errorf("band names = %v", v.Bands)
		}
	})
}

func TestStarsAreNamedAsTheStandingNamesThem(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyWorld(t)
		v, body := galaxyOf(t, app, nil)
		// Masked in the middle; whole where the org listed itself; none where it withdrew.
		wantMask := []string{"or•••lt", "an•••da", "ve•••ar", "Rigel Labs", "", "al•••ir", "d•••", "ca•••la"}
		if !slices.Equal(v.Stars.Mask, wantMask) {
			t.Errorf("masks = %q, want %q", v.Stars.Mask, wantMask)
		}
		// A star that withdrew is still a star: it has points and a place, and no name.
		i := slices.Index(v.Stars.Mask, "")
		if i < 0 || v.Stars.Points[i] != 5 || v.Stars.ID[i] == "" {
			t.Errorf("the withdrawn star: %d", i)
		}
		// The masks are exactly what the standing sends for the same orgs.
		st, _ := standing(t, app, nil)
		for _, r := range st.Rows {
			if r.Handle != "" && !slices.Contains(v.Stars.Mask, r.Handle) {
				t.Errorf("the standing names %q and the galaxy does not", r.Handle)
			}
		}
		// No org id, handle beyond the mask, email or field that names a person is sent.
		for _, leak := range []string{"orion-belt", "andromeda", "vegastar", "procyon", "altair", "deneb", "capella", "ghost", "admin", "@", `"org"`, `"handle"`, `"email"`} {
			if strings.Contains(body, leak) {
				t.Errorf("the answer holds %q: %s", leak, body)
			}
		}
	})
}

func TestIDsAreOpaqueAndConsistent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyWorld(t)
		v, _ := galaxyOf(t, app, nil)
		orgs := []string{"orion-belt", "andromeda", "vegastar", "rigel", "procyon", "altair", "deneb", "capella"}
		seen := map[string]bool{}
		for _, id := range v.Stars.ID {
			if len(id) != 6 {
				t.Errorf("id %q is not 6 characters: 4 bytes for 8 stars", id)
			}
			if _, err := base64.RawURLEncoding.DecodeString(id); err != nil {
				t.Errorf("id %q is not base64url: %v", id, err)
			}
			if seen[id] {
				t.Errorf("id %q names two stars", id)
			}
			seen[id] = true
		}
		// An id is no function of the org that anyone can compute: not its name, and not the
		// first bytes of its digest under any plain encoding.
		for _, org := range orgs {
			sum := sha256.Sum256([]byte(org))
			for _, guess := range []string{org, base64.RawURLEncoding.EncodeToString(sum[:6]), hex.EncodeToString(sum[:4]), hex.EncodeToString(sum[:6])} {
				if seen[guess] {
					t.Errorf("an id is %q, which anyone can compute from %q", guess, org)
				}
			}
		}
		// The same star is the same id wherever the answer says it: its own column and the
		// sponsor column of the stars it referred.
		for i, s := range v.Stars.Sponsor {
			if s != "" && !seen[s] {
				t.Errorf("star %d is sponsored by %q, which is no star in the answer", i, s)
			}
		}
		// Two readers in the same minute are told the same ids.
		again, _ := galaxyOf(t, app, nil)
		if !slices.Equal(again.Stars.ID, v.Stars.ID) {
			t.Errorf("two readers, two sets of ids")
		}
		// A new read draws new ids: an id is of no use for following a star from one minute to the next.
		galaxies.expire()
		later, _ := galaxyOf(t, app, nil)
		if len(later.Stars.ID) != 8 || slices.ContainsFunc(later.Stars.ID, func(id string) bool { return seen[id] }) {
			t.Errorf("ids outlived their read: %q then %q", v.Stars.ID, later.Stars.ID)
		}
		if !slices.Equal(later.Stars.Points, v.Stars.Points) {
			t.Errorf("a new read changed the points: %v then %v", v.Stars.Points, later.Stars.Points)
		}
	})
}

func TestSponsorsAreTheReferralsOnTheLine(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyWorld(t)
		v, _ := galaxyOf(t, app, nil)
		// Each star, by name, and the star that referred it.
		for mask, by := range map[string]string{
			"or•••lt":    "",        // rigel's referral of orion-belt would close a loop: left out
			"an•••da":    "or•••lt", // orion-belt came first; procyon's later claim does not stand
			"ve•••ar":    "or•••lt", // its referral of itself is left out
			"Rigel Labs": "an•••da",
			"":           "al•••ir", // procyon, who withdrew, was still referred, and the link is an id
			"al•••ir":    "",
			"d•••":       "", // a held org's referral is no star's
			"ca•••la":    "",
		} {
			want := ""
			if by != "" {
				want = v.Stars.ID[starAt(t, v, by)]
			}
			if got := v.Stars.Sponsor[starAt(t, v, mask)]; got != want {
				t.Errorf("sponsor of %q = %q, want %q (%q)", mask, got, want, by)
			}
		}
	})
}

func TestLinksAreAForest(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		// Whatever the ledger holds, following sponsors up from any star ends.
		app := galaxyWorld(t)
		edges := append([]client.ReferralEdge{}, galaxyEdges...)
		edges = append(edges, client.ReferralEdge{Referrer: "capella", Referee: "deneb"}, client.ReferralEdge{Referrer: "deneb", Referee: "capella"},
			client.ReferralEdge{Referrer: "altair", Referee: "capella"}, client.ReferralEdge{Referrer: "procyon", Referee: "altair"})
		galaxyEdgesWere := referralEdgesFn
		referralEdgesFn = func(ctx context.Context, in *client.ReferralEdgesIn) (*client.ReferralEdgeList, error) {
			out := &client.ReferralEdgeList{Edges: []client.ReferralEdge{}}
			if in.Offset < len(edges) {
				out.Edges = edges[in.Offset:min(len(edges), in.Offset+in.Limit)]
			}
			return out, nil
		}
		t.Cleanup(func() { referralEdgesFn = galaxyEdgesWere })
		galaxies.reset()
		v, _ := galaxyOf(t, app, nil)
		up := map[string]string{}
		for i, id := range v.Stars.ID {
			up[id] = v.Stars.Sponsor[i]
		}
		for id := range up {
			steps := 0
			for at := id; at != ""; at = up[at] {
				if steps++; steps > len(up) {
					t.Fatalf("following sponsors up from %q never ends", id)
				}
			}
		}
	})
}

func TestEdgesAreReadAPageAtATime(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyWorld(t)
		var offsets []int
		filler := make([]client.ReferralEdge, edgePage)
		for i := range filler {
			filler[i] = client.ReferralEdge{Referrer: fmt.Sprintf("gone%d", i), Referee: fmt.Sprintf("away%d", i)}
		}
		edges := append(filler, client.ReferralEdge{Referrer: "capella", Referee: "deneb"})
		referralEdgesFn = func(_ context.Context, in *client.ReferralEdgesIn) (*client.ReferralEdgeList, error) {
			offsets = append(offsets, in.Offset)
			if in.Limit != edgePage {
				t.Errorf("limit = %d, want %d", in.Limit, edgePage)
			}
			out := &client.ReferralEdgeList{Edges: []client.ReferralEdge{}}
			if in.Offset < len(edges) {
				out.Edges = edges[in.Offset:min(len(edges), in.Offset+in.Limit)]
			}
			return out, nil
		}
		v, _ := galaxyOf(t, app, nil)
		if !slices.Equal(offsets, []int{0, edgePage}) {
			t.Errorf("pages read at offsets %v", offsets)
		}
		if v.Stars.Sponsor[starAt(t, v, "d•••")] != v.Stars.ID[starAt(t, v, "ca•••la")] {
			t.Errorf("an edge on the second page is not drawn")
		}
	})
}

func TestSelfIsTheReadersOwnStar(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyWorld(t)
		anon, _ := galaxyOf(t, app, nil)
		if anon.Self != "" {
			t.Errorf("a reader who is not signed in has a star: %q", anon.Self)
		}
		// A bare X-Org-Id with no verified user is still nobody.
		if v, _ := galaxyOf(t, app, map[string]string{"X-Org-Id": "andromeda"}); v.Self != "" {
			t.Errorf("an org id with no user is a reader: %q", v.Self)
		}
		ada, _ := galaxyOf(t, app, principalHeaders("andromeda", "ada"))
		if ada.Self == "" || ada.Self != ada.Stars.ID[starAt(t, ada, "an•••da")] {
			t.Errorf("andromeda's self = %q, its star is %q", ada.Self, ada.Stars.ID[starAt(t, ada, "an•••da")])
		}
		// The shared read holds no reader: everyone is told the same stars, and only self differs.
		bob, _ := galaxyOf(t, app, principalHeaders("capella", "bob"))
		if bob.Self != bob.Stars.ID[starAt(t, bob, "ca•••la")] || bob.Self == ada.Self {
			t.Errorf("capella's self = %q", bob.Self)
		}
		if !slices.Equal(ada.Stars.ID, bob.Stars.ID) || !slices.Equal(ada.Stars.Band, bob.Stars.Band) {
			t.Errorf("two readers, two galaxies")
		}
		// An org the board does not hold, the platform's own held one and an org IAM has
		// never heard of, has no star.
		for _, org := range []string{"admin", "newcomer"} {
			if v, _ := galaxyOf(t, app, principalHeaders(org, "x")); v.Self != "" {
				t.Errorf("%s has star %q", org, v.Self)
			}
		}
		// A reader's own star is not marked in what the next reader is told.
		if again, _ := galaxyOf(t, app, nil); again.Self != "" {
			t.Errorf("a signed-in read leaked into the shared one: %q", again.Self)
		}
	})
}

func TestTheCenterIsTheLastDay(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyWorld(t)
		var asked []dsCall
		inner := queryDatastore
		queryDatastore = func(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
			if strings.Contains(sql, "uniqExactIf") {
				asked = append(asked, dsCall{sql, args})
			}
			return inner(ctx, sql, args...)
		}
		v, _ := galaxyOf(t, app, nil)
		if v.Center.Tokens24h != 123_456_789 || v.Center.ActiveOrgs24h != 7 {
			t.Errorf("center = %+v", v.Center)
		}
		// The window is the pulse's twenty-four hours, 2026-11-07T14:00Z on, so the tokens are
		// the sum of the pulse's tokens by hour; the bound is bound, not written into the SQL.
		_, hours := pulseHoursSQL(newAxes(noon).hour0)
		if len(asked) != 1 || len(asked[0].args) != 1 || asked[0].args[0] != hours[0] || hours[0] != "2026-11-07 14:00:00" {
			t.Errorf("the center was read as %+v, want the pulse's hours from %v", asked, hours)
		}
		if strings.Contains(asked[0].sql, "2026") {
			t.Errorf("a bound written into the SQL: %s", asked[0].sql)
		}
	})
}

func TestTheGalaxyIsPublicAndNeverStored(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyWorld(t)
		req := httptest.NewRequest(http.MethodGet, "/v1/leaderboard/galaxy", nil)
		resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("no principal: %d", resp.StatusCode)
		}
		// It carries a reader's own star when there is one, so an intermediary must not keep it.
		if got := resp.Header.Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q", got)
		}
	})
}

func TestTheGalaxyIsReadOnceAMinuteAndSurvivesABlip(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyWorld(t)
		var centers, pages int
		inner := queryDatastore
		queryDatastore = func(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
			if strings.Contains(sql, "uniqExactIf") {
				centers++
			}
			return inner(ctx, sql, args...)
		}
		was := referralEdgesFn
		referralEdgesFn = func(ctx context.Context, in *client.ReferralEdgesIn) (*client.ReferralEdgeList, error) {
			pages++
			return was(ctx, in)
		}
		t.Cleanup(func() { referralEdgesFn = was })
		first, _ := galaxyOf(t, app, nil)
		galaxyOf(t, app, principalHeaders("andromeda", "ada"))
		galaxyOf(t, app, nil)
		if centers != 1 || pages != 1 {
			t.Fatalf("three readers read the center %d times and the edges %d, want once each", centers, pages)
		}
		// Past the minute a source that fails serves the last good read: the same stars.
		galaxies.expire()
		lines.expire()
		referralEdgesFn = func(context.Context, *client.ReferralEdgesIn) (*client.ReferralEdgeList, error) {
			return nil, errors.New("referral down")
		}
		v, _ := galaxyOf(t, app, nil)
		if !v.Available || !slices.Equal(v.Stars.ID, first.Stars.ID) {
			t.Fatalf("a blip must serve the last read: %+v", v)
		}
	})
}

func TestAGalaxyNeverReadIsHonestlyEmpty(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyWorld(t)
		datastoreEnabled = func() bool { return false }
		v, body := galaxyOf(t, app, principalHeaders("andromeda", "ada"))
		if v.Available || len(v.Stars.ID) != 0 || v.Self != "" {
			t.Fatalf("no warehouse: %s", body)
		}
		// Columns are lists, never null, so a page can read them without a guard.
		for _, col := range []string{`"id":[]`, `"points":[]`, `"size":[]`, `"band":[]`, `"sponsor":[]`, `"mask":[]`, `"self":""`} {
			if !strings.Contains(body, col) {
				t.Errorf("empty answer lacks %s: %s", col, body)
			}
		}
		if len(v.Bands) != 10 {
			t.Errorf("bands: %v", v.Bands)
		}
	})
}

func TestIDsAreAsShortAsTheirOrgsAllow(t *testing.T) {
	for n, want := range map[int]int{0: 4, 1: 4, 1850: 4, 4095: 4, 4096: 5, 1_000_000: 6, 1 << 40: 12, 1 << 62: 17} {
		if got := idWidth(n); got != want {
			t.Errorf("idWidth(%d) = %d, want %d", n, got, want)
		}
	}
}

func TestColumnsAreAboutHalfTheBytesOfObjects(t *testing.T) {
	// The platform's size today: about 1,850 orgs, a few hundred referred.
	const n = 1850
	g := GalaxyView{Available: true, Bands: bandNames[:], Stars: GalaxyStars{
		ID: make([]string, n), Points: make([]int, n), Size: make([]int, n), Band: make([]int, n), Sponsor: make([]string, n), Mask: make([]string, n),
	}}
	ids := opaque(func() []string {
		orgs := make([]string, n)
		for i := range orgs {
			orgs[i] = fmt.Sprintf("builder-%04d", i)
		}
		return orgs
	}())
	type star struct {
		ID      string `json:"id"`
		Points  int    `json:"points"`
		Size    int    `json:"size"`
		Band    int    `json:"band"`
		Sponsor string `json:"sponsor,omitempty"`
		Mask    string `json:"mask"`
	}
	objects := make([]star, n)
	for i := range n {
		g.Stars.ID[i], g.Stars.Points[i], g.Stars.Size[i], g.Stars.Band[i], g.Stars.Mask[i] = ids[i], max(0, 300-i), size(max(0, 300-i)), seeded().bandOf(max(0, 300-i)), mask(fmt.Sprintf("builder-%04d", i))
		if i%5 == 0 && i > 0 {
			g.Stars.Sponsor[i] = ids[i/5]
		}
		objects[i] = star{g.Stars.ID[i], g.Stars.Points[i], g.Stars.Size[i], g.Stars.Band[i], g.Stars.Sponsor[i], g.Stars.Mask[i]}
	}
	cols, _ := json.Marshal(g)
	objs, _ := json.Marshal(objects)
	t.Logf("%d stars: %d bytes as columns, %d as objects", n, len(cols), len(objs))
	if len(cols)*100 > len(objs)*60 {
		t.Errorf("columns are %d bytes against %d as objects: not about half", len(cols), len(objs))
	}
}

func TestOpaqueNamesEveryOrgOnceWhateverTheDraw(t *testing.T) {
	orgs := make([]string, 5000)
	for i := range orgs {
		orgs[i] = fmt.Sprintf("o%d", i)
	}
	ids := opaque(orgs)
	distinct := map[string]bool{}
	for _, id := range ids {
		distinct[id] = true
	}
	if len(ids) != len(orgs) || len(distinct) != len(orgs) {
		t.Fatalf("5000 orgs got %d ids, %d of them different", len(ids), len(distinct))
	}
	if slices.Equal(opaque(orgs), ids) {
		t.Fatalf("two draws gave the same ids: the key is not drawn")
	}
}

// galaxyLadder is the standing's own ladder (44 ranked: builder40 first, builder02 39th,
// valerypetrov 40th, builder01 41st, then idle1, idle2, idle3), with the galaxy's other
// sources answered and the board mounted: a line long enough that a reader's window is a
// part of it.
func galaxyLadder(t *testing.T) *zip.App {
	t.Helper()
	at(t)
	ladder44(t)
	ds := centerRows(1, 1)
	inner := queryDatastore
	queryDatastore = func(ctx context.Context, sql string, args ...any) ([]map[string]any, error) {
		if strings.Contains(sql, "uniqExactIf") {
			return ds(sql, args), nil
		}
		return inner(ctx, sql, args...)
	}
	realEdges, realLevels := referralEdgesFn, rewardLevelsFn
	referralEdgesFn = func(context.Context, *client.ReferralEdgesIn) (*client.ReferralEdgeList, error) {
		return &client.ReferralEdgeList{Edges: []client.ReferralEdge{}}, nil
	}
	rewardLevelsFn = func(context.Context) (*client.RewardLevelTable, error) {
		return nil, errors.New("reward is not mounted")
	}
	t.Cleanup(func() { referralEdgesFn, rewardLevelsFn = realEdges, realLevels })
	return mountApp(t)
}

// revealed is the 0-based ranks a galaxy carries exact points for.
func revealed(v GalaxyView) []int {
	var out []int
	for i, p := range v.Stars.Points {
		if p >= 0 {
			out = append(out, i)
		}
	}
	return out
}

func TestTheGalaxyIsCutByTheStandingsWindow(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyLadder(t)
		// Every kind of reader: nobody, one in the middle of the line, one near its end, one at the
		// top, one with no points, an org the board holds nothing of, and the platform's own.
		for name, who := range map[string]map[string]string{
			"anonymous":    nil,
			"builder20":    principalHeaders("builder20", "ann"),
			"builder01":    principalHeaders("builder01", "bob"),
			"builder40":    principalHeaders("builder40", "cat"),
			"valerypetrov": principalHeaders("valerypetrov", "val"),
			"idle2":        principalHeaders("idle2", "dan"),
			"admin":        principalHeaders("admin", "root"),
			"newcomer":     principalHeaders("newcomer", "eve"),
		} {
			v, body := galaxyOf(t, app, who)
			st, _ := standing(t, app, who)
			if len(v.Stars.ID) != st.Total {
				t.Fatalf("%s: %d stars, %d ranked", name, len(v.Stars.ID), st.Total)
			}
			// The ranks the galaxy carries points for are the ranks the standing sends rows for: the one
			// rule cuts both, so the galaxy tells a reader nothing the standing would not.
			var rows []int
			for _, r := range st.Rows {
				rows = append(rows, r.Rank-1)
			}
			if got := revealed(v); !slices.Equal(got, rows) {
				t.Errorf("%s: the galaxy shows ranks %v, the standing %v", name, got, rows)
			}
			for i := range v.Stars.ID {
				if slices.Contains(rows, i) {
					continue
				}
				// A locked star: no name, no exact points, and still a place, a size and a band.
				if v.Stars.Mask[i] != "" || v.Stars.Points[i] != -1 {
					t.Errorf("%s: star %d is outside the window and carries %q, %d", name, i, v.Stars.Mask[i], v.Stars.Points[i])
				}
				if v.Stars.ID[i] == "" || v.Stars.Band[i] < 0 || v.Stars.Size[i] < 0 {
					t.Errorf("%s: star %d lost its place: %+v", name, i, v.Stars)
				}
			}
			// What is shown is what the standing shows: the same points, and the same name for every
			// row but the reader's own, which the standing gives in full.
			for _, r := range st.Rows {
				i := r.Rank - 1
				if v.Stars.Points[i] != r.Points {
					t.Errorf("%s: rank %d has %d points, the standing says %d", name, r.Rank, v.Stars.Points[i], r.Points)
				}
				if !r.Self && v.Stars.Mask[i] != r.Handle {
					t.Errorf("%s: rank %d is %q, the standing says %q", name, r.Rank, v.Stars.Mask[i], r.Handle)
				}
			}
			if strings.Contains(body, "builder") || strings.Contains(body, "idle") {
				t.Errorf("%s: an org id is in the answer: %s", name, body)
			}
			if v.Self != "" {
				own := slices.Index(v.Stars.ID, v.Self)
				if v.Stars.Points[own] < 0 || v.Stars.Mask[own] == "" {
					t.Errorf("%s: the reader's own star carries %d points and %q", name, v.Stars.Points[own], v.Stars.Mask[own])
				}
			}
		}
	})
}

func TestOnlyTheTailIsNamedToAReaderWhoIsNotSignedIn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyLadder(t)
		v, _ := galaxyOf(t, app, nil)
		// 44 ranked, the last ten (ranks 35 to 44) are where a newcomer starts.
		want := make([]int, 0, tailRows)
		for i := len(v.Stars.ID) - tailRows; i < len(v.Stars.ID); i++ {
			want = append(want, i)
		}
		if got := revealed(v); !slices.Equal(got, want) {
			t.Fatalf("a reader who is not signed in is shown ranks %v, want %v", got, want)
		}
		for i := range v.Stars.ID {
			named := v.Stars.Mask[i] != ""
			if i < len(v.Stars.ID)-tailRows && (named || v.Stars.Points[i] != -1) {
				t.Errorf("rank %d is named to a stranger: %q %d", i+1, v.Stars.Mask[i], v.Stars.Points[i])
			}
		}
		// A bare X-Org-Id with no verified user is still nobody.
		bare, _ := galaxyOf(t, app, map[string]string{"X-Org-Id": "builder40"})
		if !slices.Equal(revealed(bare), want) {
			t.Errorf("an org id with no user was shown more than a stranger: %v", revealed(bare))
		}
	})
}

func TestTheSkyIsTheSameSkyForEveryReader(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyLadder(t)
		anon, _ := galaxyOf(t, app, nil)
		top, _ := galaxyOf(t, app, principalHeaders("builder40", "cat"))
		mid, _ := galaxyOf(t, app, principalHeaders("builder20", "ann"))
		for name, v := range map[string]GalaxyView{"top": top, "mid": mid} {
			if !slices.Equal(v.Stars.ID, anon.Stars.ID) || !slices.Equal(v.Stars.Size, anon.Stars.Size) ||
				!slices.Equal(v.Stars.Band, anon.Stars.Band) || !slices.Equal(v.Stars.Sponsor, anon.Stars.Sponsor) || !slices.Equal(v.Bands, anon.Bands) || v.Center != anon.Center {
				t.Errorf("%s reader is told another sky than a stranger", name)
			}
		}
		// The size is a factor of two of the points, for every star, named or not.
		l := lineOf(t)
		for i, e := range l.ranked {
			if anon.Stars.Size[i] != size(e.points) {
				t.Errorf("star %d (%d points) has size %d", i, e.points, anon.Stars.Size[i])
			}
		}
		if len(revealed(anon)) >= len(revealed(top))+len(revealed(mid)) {
			t.Errorf("the windows do not differ: %d, %d, %d", len(revealed(anon)), len(revealed(top)), len(revealed(mid)))
		}
	})
}

func TestTheReadersOwnStarIsAlwaysNamed(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyLadder(t)
		// idle3 holds nothing and is last; builder40 is first; both are in their own windows.
		for _, org := range []string{"idle3", "builder40", "builder20"} {
			v, _ := galaxyOf(t, app, principalHeaders(org, "x"))
			own := slices.Index(v.Stars.ID, v.Self)
			if own < 0 || v.Stars.Mask[own] != mask(org) || v.Stars.Points[own] < 0 {
				t.Errorf("%s: own star %d carries %q, %d", org, own, v.Stars.Mask[slices.Index(v.Stars.ID, v.Self)], v.Stars.Points[own])
			}
		}
	})
}

func TestBandsAreTheProgramsTable(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := galaxyLadder(t)
		rewardLevelsFn = func(context.Context) (*client.RewardLevelTable, error) {
			return &client.RewardLevelTable{Levels: []client.RewardLevel{
				{Level: 1, Band: "Alpha", From: 0}, {Level: 2, Band: "Alpha", From: 100}, {Level: 3, Band: "Beta", From: 1000}, {Level: 4, Band: "Gamma", From: 3500},
			}}, nil
		}
		v, _ := galaxyOf(t, app, nil)
		if !slices.Equal(v.Bands, []string{"Alpha", "Beta", "Gamma"}) {
			t.Fatalf("bands = %v, want the program's", v.Bands)
		}
		l := lineOf(t)
		for i, e := range l.ranked {
			want := 0
			switch {
			case e.points >= 3500:
				want = 2
			case e.points >= 1000:
				want = 1
			}
			if v.Stars.Band[i] != want {
				t.Errorf("star %d with %d points is band %d, want %d (the program's table, not the seeded one)", i, e.points, v.Stars.Band[i], want)
			}
		}
	})
}

func TestBandsFallBackToTheSeededTableOnlyWhenTheProgramCannotBeRead(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for name, answer := range map[string]func(context.Context) (*client.RewardLevelTable, error){
			"it fails":  func(context.Context) (*client.RewardLevelTable, error) { return nil, errors.New("reward is down") },
			"no levels": func(context.Context) (*client.RewardLevelTable, error) { return &client.RewardLevelTable{}, nil },
			"not a table": func(context.Context) (*client.RewardLevelTable, error) {
				return &client.RewardLevelTable{Levels: []client.RewardLevel{{Band: "A", From: 9}}}, nil
			},
			"band missing": func(context.Context) (*client.RewardLevelTable, error) {
				return &client.RewardLevelTable{Levels: []client.RewardLevel{{Band: "", From: 0}}}, nil
			},
		} {
			app := galaxyLadder(t)
			rewardLevelsFn = answer
			v, _ := galaxyOf(t, app, nil)
			if !slices.Equal(v.Bands, bandNames[:]) {
				t.Errorf("%s: bands = %v, want the seeded ten", name, v.Bands)
			}
			l := lineOf(t)
			for i, e := range l.ranked {
				if v.Stars.Band[i] != seeded().bandOf(e.points) {
					t.Errorf("%s: star %d band %d", name, i, v.Stars.Band[i])
				}
			}
			galaxies.reset()
		}
	})
}
