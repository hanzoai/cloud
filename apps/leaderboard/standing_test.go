package leaderboard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"
)

// usageRows answers standingUsageSQL with one row per org: its all-time tokens.
func usageRows(tokens map[string]uint64) func(string, []any) []map[string]any {
	return func(sql string, _ []any) []map[string]any {
		if sql != standingUsageSQL {
			return nil
		}
		var rows []map[string]any
		for org, n := range tokens {
			rows = append(rows, map[string]any{"organization": org, "total_tokens": n})
		}
		return rows
	}
}

// iamOrg is an org as IAM's directory lists it: its slug and when it signed up.
type iamOrg struct{ name, created string }

// world stands in every source the line reads: IAM's directory, the usage warehouse,
// the waitlist records and the referral ledger. Nothing else answers.
func world(t *testing.T, orgs []iamOrg, tokens map[string]uint64, people []person, tally map[string][2]int) {
	t.Helper()
	t.Setenv("CLOUD_BRAND", "hanzo")
	was := directoryFn
	directoryFn = func(context.Context) (*client.Directory, error) {
		d := &client.Directory{}
		for _, o := range orgs {
			d.Orgs = append(d.Orgs, client.DirectoryOrg{Name: o.name, Created: o.created})
		}
		return d, nil
	}
	t.Cleanup(func() { directoryFn = was })
	standIn(t, people, tally, nil)
	installFakeDS(t, usageRows(tokens))
}

// ladder44 is the whole line: builder01..builder40 at 100..4000 points (10M tokens
// is 100 points, so builder10 is at the 1000 that begins Wave and builder40 at the
// 4000 that begins Focus), valerypetrov at 150 from the seeded awards alone, and three orgs
// that have done nothing, who signed up in the order idle1, idle2, idle3. The
// platform's own admin org has more tokens than any of them and is not on it. 44
// ranked: builder40 first, builder02 39th, valerypetrov 40th, builder01 41st, then
// idle1, idle2, idle3.
func ladder44(t *testing.T) {
	t.Helper()
	tokens := map[string]uint64{"admin": 900_000_000_000}
	orgs := []iamOrg{{"admin", "2025-01-01T00:00:00Z"}, {"valerypetrov", "2026-01-15T00:00:00Z"},
		{"idle1", "2026-01-01T00:00:00Z"}, {"idle2", "2026-02-01T00:00:00Z"}, {"idle3", "2026-03-01T00:00:00Z"}}
	for i := 1; i <= 40; i++ {
		name := fmt.Sprintf("builder%02d", i)
		tokens[name] = uint64(i) * 10_000_000
		orgs = append(orgs, iamOrg{name, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, i).Format(time.RFC3339)})
	}
	world(t, orgs, tokens, nil, nil)
}

// standing reads GET /v1/leaderboard/standing as the headers' caller, or as nobody.
func standing(t *testing.T, app *zip.App, headers map[string]string) (StandingView, string) {
	t.Helper()
	code, body := doGet(t, app, "/v1/leaderboard/standing", headers)
	if code != http.StatusOK {
		t.Fatalf("standing: %d %s", code, body)
	}
	var v StandingView
	if err := json.Unmarshal(body, &v); err != nil {
		t.Fatalf("standing body: %v: %s", err, body)
	}
	return v, string(body)
}

func TestMask(t *testing.T) {
	for in, want := range map[string]string{
		"valerypetrov": "va•••ov",
		"abcdef":       "ab•••ef", // six: the long form
		"abcde":        "a•••",    // five: the short form
		"tiny":         "t•••",
		"a":            "a•••",
		"ünïcödé":      "ün•••dé", // characters, not bytes
		"":             "",
	} {
		if got := mask(in); got != want {
			t.Errorf("mask(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHandleOf(t *testing.T) {
	choices := map[string]choice{
		"listed":  {Handle: " Ada ", Listed: true},
		"retired": {Handle: "Bob", Listed: false},
	}
	for org, want := range map[string]string{
		"listed":      "Ada",     // opted in: whole
		"retired":     "",        // opted out: none, never masked
		"unchosen":    "un•••en", // no choice: masked
		"verylongorg": "ve•••rg",
	} {
		if got := handleOf(org, choices); got != want {
			t.Errorf("handleOf(%q) = %q, want %q", org, got, want)
		}
	}
}

func TestRevealStepsAtEachThreshold(t *testing.T) {
	// Deep enough in a long line that no rung is capped by the board's top.
	const pos, total = 5000, 10000
	for _, c := range []struct {
		points, above int
	}{
		{0, 5}, {999, 5},
		{1000, 10}, {3999, 10},
		{4000, 25}, {8999, 25},
		{9000, 50}, {15999, 50},
		{16000, 100}, {24999, 100},
		{25000, 250}, {35999, 250},
		{36000, 500}, {48999, 500},
		{49000, 1000}, {63999, 1000},
		{64000, 2500}, {80999, 2500},
		{81000, pos}, // everyone above
	} {
		lo, hi, _ := viewport(c.points, pos, total)
		if pos-lo != c.above {
			t.Errorf("%d points: sees %d above, want %d", c.points, pos-lo, c.above)
		}
		if hi != pos+1+belowRows {
			t.Errorf("%d points: hi=%d, want %d", c.points, hi, pos+1+belowRows)
		}
	}
}

// The rung table is the program's: band b begins at the points level 10b needs,
// 10*n*n, and the last band, Singularity, shows everyone.
func TestRevealStepsAreTheBands(t *testing.T) {
	wantShow := []int{5, 10, 25, 50, 100, 250, 500, 1000, 2500, everyone}
	if len(revealSteps) != 10 || len(bands) != 10 {
		t.Fatalf("%d rungs, %d bands, want 10 each", len(revealSteps), len(bands))
	}
	for b, s := range revealSteps {
		n := b * 10
		if s.At != 10*n*n {
			t.Errorf("band %d (level %d) begins at %d, want %d", b, n, s.At, 10*n*n)
		}
		if s.Show != wantShow[b] {
			t.Errorf("band %d shows %d, want %d", b, s.Show, wantShow[b])
		}
	}
	if got, want := revealSteps[9].At, 81000; got != want {
		t.Errorf("Singularity begins at %d, want %d", got, want)
	}
	if revealSteps[9].Show != everyone {
		t.Errorf("the last rung shows %d, want everyone", revealSteps[9].Show)
	}
	// The feed names the rungs from the second, from the same list.
	wantLevels := []string{"Wave", "Focus", "Flare", "Comet", "Pulsar", "Nebula", "Quasar", "Magnetar", "Singularity"}
	if !slices.Equal(levels, wantLevels) {
		t.Errorf("levels = %v, want %v", levels, wantLevels)
	}
	if got := rungAt(); len(got) != len(levels) || got[0] != 1000 || got[8] != 81000 {
		t.Errorf("rungAt = %v", got)
	}
	// The last rung reaches the top whatever the reader's place, and nothing is next.
	for _, pos := range []int{0, 1, 7, 999, 123456} {
		lo, _, next := viewport(81000, pos, 200000)
		if lo != 0 || next != nil {
			t.Errorf("Singularity at rank %d: lo=%d next=%+v, want the top and nothing next", pos+1, lo, next)
		}
	}
}

func TestViewportNext(t *testing.T) {
	const pos, total = 500, 1000
	// 0 points sees 5 above; the next rung, 1000 points, shows 10: five more.
	_, _, next := viewport(0, pos, total)
	if next == nil || next.Reveal != 5 || next.At != 1000 || next.More != 1000 {
		t.Fatalf("0 points: next = %+v", next)
	}
	// 26000 points (Pulsar) sees 250 above; the next rung, Nebula at 36000 points, shows
	// all 500 above: 250 more.
	_, _, next = viewport(26000, pos, total)
	if next == nil || next.Reveal != 250 || next.At != 36000 || next.More != 10000 {
		t.Fatalf("26000 points: next = %+v", next)
	}
	// 36000 points sees 500 above, all of them: nothing is locked, so nothing is next.
	if _, _, next = viewport(36000, pos, total); next != nil {
		t.Fatalf("36000 points sees everyone above: next = %+v, want none", next)
	}
	// Deeper in the line: 4000 points sees 25 of the 5000 above; the next rung, 9000 points, shows 50: 25 more.
	_, _, next = viewport(4600, 5000, 10000)
	if next == nil || next.Reveal != 25 || next.At != 9000 || next.More != 4400 {
		t.Fatalf("4600 points: next = %+v", next)
	}
	// From Magnetar the last rung shows all 5000 above: 2500 more at 81000.
	_, _, next = viewport(70000, 5000, 10000)
	if next == nil || next.Reveal != 2500 || next.At != 81000 || next.More != 11000 {
		t.Fatalf("70000 points: next = %+v", next)
	}
	// Singularity sees everyone: nothing is locked, so nothing is next.
	if _, _, next = viewport(81000, 5000, 10000); next != nil {
		t.Fatalf("singularity: next = %+v, want none", next)
	}
	// Near the top a rung can show everything there is: nothing left to unlock.
	if _, _, next = viewport(0, 3, total); next != nil {
		t.Fatalf("rank 4 sees all 3 above at 0 points: next = %+v, want none", next)
	}
	// A rung that shows more than exists above is capped at what exists.
	_, _, next = viewport(0, 8, total)
	if next == nil || next.Reveal != 3 {
		t.Fatalf("rank 9: next = %+v, want a reveal of the 3 left", next)
	}
}

// lineOf reads the line the way the board does, from the mounted store.
func lineOf(t *testing.T) *line {
	t.Helper()
	l, err := current(context.Background(), mountedStore)
	if err != nil {
		t.Fatalf("line: %v", err)
	}
	return l
}

func pointsOf(t *testing.T, l *line, org string) int {
	t.Helper()
	e, ok := l.entry(org)
	if !ok {
		t.Fatalf("%s is not on the line", org)
	}
	return e.points
}

func TestPointsFromEverySource(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		people := []person{
			{"sub-s", "star", "sue", 3, "2026-10-20 10:00:00.000Z"},  // three stars
			{"sub-a", "all", "al", 1, "2026-10-21 10:00:00.000Z"},    // one star
			{"sub-h", "hanzo", "hal", 5, "2026-10-22 10:00:00.000Z"}, // a held org's records are no one's
		}
		world(t,
			[]iamOrg{{"tok", "2026-01-01T00:00:00Z"}, {"ref", "2026-01-02T00:00:00Z"}, {"star", "2026-01-03T00:00:00Z"},
				{"bounty", "2026-01-04T00:00:00Z"}, {"all", "2026-01-05T00:00:00Z"}, {"none", "2026-01-06T00:00:00Z"}},
			map[string]uint64{"tok": 5_000_000, "all": 2_000_000, "outsider": 99_000_000_000},
			people,
			map[string][2]int{"ref": {3, 2}, "all": {1, 1}},
		)
		mountApp(t)
		if err := mountedStore.PutBountyAward(context.Background(), bountyAward{ID: "b1", Org: "bounty", Kind: "bounty", Points: 70, CreatedAt: 1}); err != nil {
			t.Fatal(err)
		}
		if err := mountedStore.PutBountyAward(context.Background(), bountyAward{ID: "b2", Org: "all", Kind: "bug_report", Points: 5, CreatedAt: 2}); err != nil {
			t.Fatal(err)
		}
		l := lineOf(t)
		for org, want := range map[string]int{
			"tok":    50,                  // 5M tokens
			"ref":    3 + 2*20,            // three referred orgs, two of them qualified: the 20 is beside the 1
			"star":   3,                   // three stars
			"bounty": 70,                  // an award
			"all":    20 + 1 + 20 + 1 + 5, // 2M tokens, a referral that qualified, a star, an award
			"none":   0,
		} {
			if got := pointsOf(t, l, org); got != want {
				t.Errorf("%s = %d points, want %d", org, got, want)
			}
		}
		// "outsider" is in the warehouse and not in IAM, and the held "hanzo" holds
		// records: neither is ranked.
		for _, org := range []string{"outsider", "hanzo", "admin"} {
			if _, ok := l.entry(org); ok {
				t.Errorf("%s must not be ranked", org)
			}
		}
		if len(l.ranked) != 6 || l.scored != 5 {
			t.Errorf("ranked %d, scored %d; want 6 and 5", len(l.ranked), l.scored)
		}
		var order []string
		for _, e := range l.ranked {
			order = append(order, e.org)
		}
		if strings.Join(order, " ") != "bounty tok all ref star none" {
			t.Errorf("order: %v", order)
		}
	})
}

func TestCountedStarsAndFollows(t *testing.T) {
	stars, follows := counted([]betaAction{
		{"star", "hanzoai/a"}, {"star", "hanzoai/b"}, {"star", "hanzoai/a"}, // a repo counts once
		{"follow", "zach/github"}, {"follow", "zach/github"}, {"follow", "antje/x"},
		{"bug_report", "hanzoai/a#1"}, {"bounty", "hanzoai/b#2"}, // a claim is not verified: nothing
	})
	if stars != 2 || follows != 2 {
		t.Fatalf("stars=%d follows=%d, want 2 and 2", stars, follows)
	}
}

func TestOneScoreForEveryBoard(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		// The values the owner set.
		for _, c := range []struct {
			name string
			src  client.Sources
			want int
		}{
			{"nothing", client.Sources{}, 0},
			{"99,999 tokens", client.Sources{Tokens: 99_999}, 0},
			{"100,000 tokens", client.Sources{Tokens: 100_000}, 1},
			{"a referral", client.Sources{Referrals: 1}, 1},
			{"a qualified referral is 20 beside its 1", client.Sources{Referrals: 1, Qualified: 1}, 21},
			{"a star is 1", client.Sources{Stars: 1}, 1},
			{"a follow is 1", client.Sources{Follows: 1}, 1},
			{"an award is its points", client.Sources{Bounty: 50}, 50},
		} {
			if got := client.Points(c.src); got != c.want {
				t.Errorf("%s: %d, want %d", c.name, got, c.want)
			}
		}

		// And the boards agree: valerypetrov's score is the same on the bounty board and
		// on the standing, with every source in it.
		world(t,
			[]iamOrg{{"valerypetrov", "2026-01-15T00:00:00Z"}, {"rival", "2026-01-16T00:00:00Z"}},
			map[string]uint64{"valerypetrov": 3_000_000, "rival": 90_000_000},
			[]person{{"sub-v", "valerypetrov", "valerypetrov", 2, "2026-10-20 10:00:00.000Z"}},
			map[string][2]int{"valerypetrov": {2, 1}},
		)
		app := mountApp(t)
		want := 150 + 30 + 2 + 20 + 2 // three seeded awards, 3M tokens, two referrals and one qualified, two stars
		v, _ := standing(t, app, principalHeaders("valerypetrov", "valerypetrov"))
		if v.Self == nil || v.Self.Points != want || v.Self.Rank != 2 {
			t.Fatalf("standing self: %+v, want %d points at rank 2", v.Self, want)
		}
		code, body := doGet(t, app, "/v1/leaderboard/bounty", principalHeaders("valerypetrov", "valerypetrov"))
		if code != http.StatusOK {
			t.Fatalf("bounty: %d %s", code, body)
		}
		var b BountyBoard
		_ = json.Unmarshal(body, &b)
		if b.Self == nil || b.Self.TotalScore != want || b.Self.TokensUsed != 3_000_000 || b.Self.GlobalTokenRank != 2 {
			t.Fatalf("bounty self: %+v, want %d points, 3M tokens, token rank 2", b.Self, want)
		}
	})
}

func TestEveryOrgIsRankedAndTheIdleAreAtTheBottom(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ladder44(t)
		mountApp(t)
		l := lineOf(t)
		if len(l.ranked) != 44 || l.scored != 41 {
			t.Fatalf("ranked %d, scored %d; want 44 and 41", len(l.ranked), l.scored)
		}
		var tail []string
		for _, e := range l.ranked[41:] {
			tail = append(tail, fmt.Sprintf("%s:%d", e.org, e.points))
		}
		// Nothing earned: earliest signup first.
		if strings.Join(tail, " ") != "idle1:0 idle2:0 idle3:0" {
			t.Fatalf("tail = %v", tail)
		}
		if l.ranked[0].org != "builder40" || l.ranked[38].org != "builder02" || l.ranked[39].org != "valerypetrov" || l.ranked[40].org != "builder01" {
			t.Fatalf("order: %v %v %v %v", l.ranked[0].org, l.ranked[38].org, l.ranked[39].org, l.ranked[40].org)
		}
		if _, ok := l.entry("admin"); ok {
			t.Fatal("the admin org is held and not ranked")
		}
	})
}

func TestEqualPointsGoToTheEarlierSignup(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		world(t,
			[]iamOrg{{"zed", "2026-02-01T00:00:00Z"}, {"amy", "2026-03-01T00:00:00Z"}, {"bea", "2026-03-01T00:00:00Z"}, {"cal", "unknown"}},
			map[string]uint64{"zed": 500_000, "amy": 500_000, "bea": 500_000, "cal": 500_000}, nil, nil)
		mountApp(t)
		var got []string
		for _, e := range lineOf(t).ranked {
			got = append(got, e.org)
		}
		// valerypetrov is not in IAM here. Equal points: signup, then org id; no date last.
		if strings.Join(got, " ") != "zed amy bea cal" {
			t.Fatalf("order = %v", got)
		}
	})
}

func TestStandingTop(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ladder44(t)
		app := mountApp(t)
		v, body := standing(t, app, principalHeaders("builder40", "forty"))
		if !v.Available || v.Total != 44 || v.Scored != 41 || v.Locked != 0 {
			t.Fatalf("top: %+v", v)
		}
		// Rank 1 sees itself and the five under it, and nothing is locked.
		if len(v.Rows) != 6 || v.Rows[0].Rank != 1 || !v.Rows[0].Self || v.Rows[5].Rank != 6 {
			t.Fatalf("top rows: %+v", v.Rows)
		}
		if v.Self == nil || !v.Self.Ranked || v.Self.Rank != 1 || v.Self.Points != 4000 || v.Self.Next != nil {
			t.Fatalf("top self: %+v", v.Self)
		}
		if v.Rows[0].Handle != "forty" {
			t.Fatalf("own handle must be whole: %q", v.Rows[0].Handle)
		}
		assertWindow(t, body, v)
	})
}

func TestStandingMiddle(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ladder44(t)
		app := mountApp(t)
		// builder10 holds 1000 points, 31st of 44: Wave, which sees 10 above.
		v, body := standing(t, app, principalHeaders("builder10", "ten"))
		if v.Self == nil || v.Self.Rank != 31 || v.Self.Points != 1000 {
			t.Fatalf("middle self: %+v", v.Self)
		}
		// Ranks 21..36: ten above, itself, and the five under it.
		if len(v.Rows) != 16 || v.Rows[0].Rank != 21 || v.Rows[15].Rank != 36 || v.Locked != 20 {
			t.Fatalf("middle window: locked=%d rows=%+v", v.Locked, v.Rows)
		}
		// Focus at 4000 points shows 25 above: 15 more.
		if n := v.Self.Next; n == nil || n.Reveal != 15 || n.At != 4000 || n.More != 3000 {
			t.Fatalf("middle next: %+v", n)
		}
		assertWindow(t, body, v)
	})
}

// A reader under the first rung sees what a newcomer is owed: 5 above.
func TestStandingPhotonSeesFiveAbove(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ladder44(t)
		app := mountApp(t)
		// builder02 holds 200 points, 39th of 44.
		v, body := standing(t, app, principalHeaders("builder02", "two"))
		if v.Self == nil || v.Self.Rank != 39 || v.Self.Points != 200 {
			t.Fatalf("photon self: %+v", v.Self)
		}
		// Ranks 34..44: five above, itself, and the five under it.
		if len(v.Rows) != 11 || v.Rows[0].Rank != 34 || v.Rows[10].Rank != 44 || v.Locked != 33 {
			t.Fatalf("photon window: locked=%d rows=%+v", v.Locked, v.Rows)
		}
		// Wave at 1000 points shows 10: five more.
		if n := v.Self.Next; n == nil || n.Reveal != 5 || n.At != 1000 || n.More != 800 {
			t.Fatalf("photon next: %+v", n)
		}
		assertWindow(t, body, v)
	})
}

func TestStandingBottomIsAnOrgThatHasDoneNothing(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ladder44(t)
		app := mountApp(t)
		v, body := standing(t, app, principalHeaders("idle3", "three"))
		if v.Self == nil || !v.Self.Ranked || v.Self.Rank != 44 || v.Self.Points != 0 {
			t.Fatalf("bottom self: %+v", v.Self)
		}
		// 0 points sees 5 above: ranks 39..44, nothing below.
		if len(v.Rows) != 6 || v.Rows[0].Rank != 39 || v.Rows[5].Rank != 44 || !v.Rows[5].Self || v.Locked != 38 {
			t.Fatalf("bottom window: locked=%d rows=%+v", v.Locked, v.Rows)
		}
		// 1000 points shows 10: five more.
		if n := v.Self.Next; n == nil || n.Reveal != 5 || n.At != 1000 || n.More != 1000 {
			t.Fatalf("bottom next: %+v", n)
		}
		assertWindow(t, body, v)
	})
}

func TestStandingSignedOutSeesTheTail(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ladder44(t)
		app := mountApp(t)
		v, body := standing(t, app, nil)
		if !v.Available || v.Total != 44 || v.Locked != 34 || v.Self != nil {
			t.Fatalf("signed out: %+v", v)
		}
		if len(v.Rows) != tailRows || v.Rows[0].Rank != 35 || v.Rows[9].Rank != 44 {
			t.Fatalf("signed-out rows: %+v", v.Rows)
		}
		for _, r := range v.Rows {
			if r.Self {
				t.Fatalf("a reader with no principal has no row: %+v", r)
			}
		}
		assertWindow(t, body, v)
	})
}

func TestStandingSignedInOutsideIAMSeesTheTail(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ladder44(t)
		app := mountApp(t)
		v, _ := standing(t, app, principalHeaders("ghost", "newcomer"))
		if v.Self == nil || v.Self.Ranked || v.Self.Rank != 0 || v.Self.Points != 0 || v.Self.Handle != "newcomer" {
			t.Fatalf("unranked self: %+v", v.Self)
		}
		if len(v.Rows) != tailRows || v.Locked != 34 || v.Rows[0].Rank != 35 {
			t.Fatalf("unranked window: locked=%d rows=%+v", v.Locked, v.Rows)
		}
	})
}

// assertWindow is the enforcement check: the response carries exactly one contiguous
// run of ranks, `locked` counts the ranks before it, no org id appears whole, and a
// row holds a rank, a handle, points and a self mark, and nothing else.
func assertWindow(t *testing.T, body string, v StandingView) {
	t.Helper()
	if len(v.Rows) == 0 {
		t.Fatal("no rows")
	}
	lo, hi := v.Rows[0].Rank, v.Rows[len(v.Rows)-1].Rank
	if v.Locked != lo-1 {
		t.Errorf("locked=%d but the first row is rank %d", v.Locked, lo)
	}
	if len(v.Rows) != hi-lo+1 {
		t.Errorf("rows %d..%d are not contiguous: %d rows", lo, hi, len(v.Rows))
	}
	for i := 1; i <= 40; i++ {
		if org := fmt.Sprintf("builder%02d", i); strings.Contains(body, org) {
			t.Errorf("the response carries the whole org id %s: %s", org, body)
		}
	}
	for _, leak := range []string{"@", "email", "orgId", "valerypetrov", "idle"} {
		if strings.Contains(body, leak) {
			t.Errorf("response carries %q: %s", leak, body)
		}
	}
	var raw struct {
		Rows []map[string]any `json:"rows"`
	}
	_ = json.Unmarshal([]byte(body), &raw)
	for _, r := range raw.Rows {
		for k := range r {
			switch k {
			case "rank", "handle", "points", "self":
			default:
				t.Errorf("a row carries %q", k)
			}
		}
	}
}

func TestStandingNames(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		orgs := []iamOrg{}
		for i, n := range []string{"shorty", "tiny", "adaorg", "ghostorg", "coorg", "selforg", "valerypetrov"} {
			orgs = append(orgs, iamOrg{n, time.Date(2026, 1, 1+i, 0, 0, 0, 0, time.UTC).Format(time.RFC3339)})
		}
		world(t, orgs, map[string]uint64{
			"shorty":   900_000_000, // 6 characters: the long mask
			"tiny":     800_000_000, // 4 characters: the short mask
			"adaorg":   600_000_000, // listed by its user
			"ghostorg": 500_000_000, // withdrew
			"coorg":    400_000_000, // listed by its admin
			"selforg":  100_000_000, // 1000 points: Wave, which sees ten above
		}, nil, nil)
		app := mountApp(t)
		seedUserOptin(t, "adaorg/ada", "adaorg", "Ada", true)
		seedUserOptin(t, "ghostorg/gus", "ghostorg", "Gus", false)
		seedOrgOptin(t, "coorg", "Co Ltd", true)
		seedUserOptin(t, "selforg/sam", "selforg", "Sammy", false) // withdrawn, and still whole to itself

		v, body := standing(t, app, principalHeaders("selforg", "sam"))
		// valerypetrov holds 150 points from the seeded awards alone and ranks 7th, under
		// selforg, who sees all five above it and the one under it.
		want := []string{"sh•••ty", "t•••", "Ada", "", "Co Ltd", "Sammy", "va•••ov"}
		if len(v.Rows) != len(want) || v.Locked != 0 {
			t.Fatalf("rows: locked=%d %+v", v.Locked, v.Rows)
		}
		for i, r := range v.Rows {
			if r.Rank != i+1 || r.Handle != want[i] {
				t.Errorf("rank %d handle = %q, want %q", r.Rank, r.Handle, want[i])
			}
			if r.Self != (i == 5) {
				t.Errorf("rank %d self = %v", r.Rank, r.Self)
			}
		}
		if v.Self == nil || v.Self.Handle != "Sammy" {
			t.Errorf("self: %+v", v.Self)
		}
		for _, n := range []string{"shorty", "tiny", "ghostorg", "Gus", "adaorg", "ada", "valerypetrov", "selforg"} {
			if strings.Contains(body, `"`+n+`"`) || strings.Contains(body, n+`"`) {
				t.Errorf("response names %q: %s", n, body)
			}
		}
	})
}

func TestStandingOptInThroughThePut(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		world(t,
			[]iamOrg{{"shorty", "2026-01-01T00:00:00Z"}, {"viewer", "2026-01-02T00:00:00Z"}, {"valerypetrov", "2026-01-03T00:00:00Z"}},
			map[string]uint64{"shorty": 10_000_000, "viewer": 1_000_000}, nil, nil)
		app := mountApp(t)
		read := func() string {
			v, _ := standing(t, app, principalHeaders("viewer", "vee"))
			for _, r := range v.Rows {
				if r.Points == 100 {
					return r.Handle
				}
			}
			t.Fatalf("no 100 point row: %+v", v.Rows)
			return ""
		}
		if got := read(); got != "sh•••ty" {
			t.Fatalf("before opt-in: %q", got)
		}
		code, body := doJSON(t, app, http.MethodPut, "/v1/leaderboard/optin", principalHeaders("shorty", "shorty"), userOptinReq{Listed: true, Handle: "Shorty"})
		if code != http.StatusOK {
			t.Fatalf("opt in: %d %s", code, body)
		}
		if got := read(); got != "Shorty" {
			t.Fatalf("after opt-in: %q", got)
		}
		if code, body = doJSON(t, app, http.MethodPut, "/v1/leaderboard/optin", principalHeaders("shorty", "shorty"), userOptinReq{Listed: false}); code != http.StatusOK {
			t.Fatalf("opt out: %d %s", code, body)
		}
		if got := read(); got != "" {
			t.Fatalf("after opt-out: %q, want none", got)
		}
	})
}

func TestStandingIsPublicAndTheRestIsNot(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ladder44(t)
		app := mountApp(t)
		if code, body := doGet(t, app, "/v1/leaderboard/standing", nil); code != http.StatusOK {
			t.Fatalf("standing with no principal: %d %s", code, body)
		}
		// A client-sent org with no verified user is still nobody.
		if code, body := doGet(t, app, "/v1/leaderboard/standing", map[string]string{"X-Org-Id": "builder40"}); code != http.StatusOK || strings.Contains(string(body), `"self":{`) {
			t.Fatalf("a bare X-Org-Id must read as anonymous: %d %s", code, body)
		}
		for _, path := range []string{
			"/v1/leaderboard",
			"/v1/leaderboard/bounty",
			"/v1/leaderboard/activity",
			"/v1/leaderboard/optin",
			"/v1/leaderboard/beta",
		} {
			if code, _ := doGet(t, app, path, nil); code != http.StatusUnauthorized {
				t.Errorf("GET %s with no principal = %d, want 401", path, code)
			}
		}
		for _, path := range []string{"/v1/leaderboard/optin", "/v1/leaderboard/optin/org"} {
			if code, _ := doJSON(t, app, http.MethodPut, path, nil, map[string]any{"listed": true}); code != http.StatusUnauthorized {
				t.Errorf("PUT %s with no principal = %d, want 401", path, code)
			}
		}
	})
}

func TestStandingWithoutAWarehouseRanksNobody(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ladder44(t)
		datastoreEnabled = func() bool { return false }
		app := mountApp(t)
		// The seeded awards exist, but a ranking without the tokens would be a wrong one.
		v, body := standing(t, app, nil)
		if v.Available || v.Total != 0 || len(v.Rows) != 0 || !strings.Contains(body, `"rows":[]`) {
			t.Fatalf("no warehouse: %s", body)
		}
	})
}

func TestStandingIsCachedAndSurvivesABlip(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ladder44(t)
		f := installFakeDS(t, usageRows(map[string]uint64{"builder01": 10_000_000}))
		app := mountApp(t)
		standing(t, app, nil)
		standing(t, app, nil)
		var n int
		for _, c := range f.allCalls() {
			if c.sql == standingUsageSQL {
				n++
			}
		}
		if n != 1 {
			t.Fatalf("the warehouse was read %d times for two readers, want 1", n)
		}
		// Past the TTL a failed read serves the last good line.
		lines.expire()
		directoryFn = func(context.Context) (*client.Directory, error) { return nil, errors.New("iam down") }
		if v, _ := standing(t, app, nil); !v.Available || v.Total != 44 {
			t.Fatalf("a blip must serve the last line: %+v", v)
		}
	})
}

func TestBountyFloorIsGone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		installFakeDS(t, nil)
		app := mountApp(t)
		code, body := doGet(t, app, "/v1/leaderboard/bounty", principalHeaders("valerypetrov", "valerypetrov"))
		if code != http.StatusOK {
			t.Fatalf("code=%d %s", code, body)
		}
		if strings.Contains(string(body), "4252634") {
			t.Fatalf("a token total the warehouse never reported is in the board: %s", body)
		}
		var b BountyBoard
		_ = json.Unmarshal(body, &b)
		if b.Self == nil || b.Self.TokensUsed != 0 || b.Self.GlobalTokenRank != 0 || b.Self.TotalScore != 150 {
			t.Fatalf("self: %+v", b.Self)
		}
	})
}

func TestADownSourceIsAskedOnceNotOncePerReader(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ladder44(t)
		var asked int
		directoryFn = func(context.Context) (*client.Directory, error) { asked++; return nil, errors.New("iam down") }
		app := mountApp(t)
		for range 3 {
			if v, _ := standing(t, app, nil); v.Available || len(v.Rows) != 0 {
				t.Fatalf("a line that was never read ranks nobody: %+v", v)
			}
		}
		if asked != 1 {
			t.Fatalf("IAM was asked %d times for three readers, want 1", asked)
		}
	})
}
