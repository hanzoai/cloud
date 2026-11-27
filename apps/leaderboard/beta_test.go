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

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// person is one waitlist record as marketing writes it.
type person struct {
	sub, org, name string
	stars          int
	created        string
}

// claims names, by account, the CRM person its address is and the lead reference
// its application was filed under: what a record says of the leads it claims.
var claims = map[string][2]string{}

// claiming says that the record of sub is on the person and carries the lead.
func claiming(t *testing.T, sub, person, lead string) {
	t.Helper()
	claims[sub] = [2]string{person, lead}
	t.Cleanup(func() { delete(claims, sub) })
}

// leads answers the leads that applied, as the CRM lists them.
func leads(t *testing.T, got ...crm.Applicant) {
	t.Helper()
	was := applicantsFn
	applicantsFn = func(context.Context, string) ([]crm.Applicant, error) { return got, nil }
	t.Cleanup(func() { applicantsFn = was })
}

// standIn answers the waitlist with people, oldest first, and the referral
// tally and the warehouse as given. A nil tally answers no referrals; an error
// stands for a peer that did not answer. No lead has applied unless leads says so.
func standIn(t *testing.T, people []person, tally map[string][2]int, tallyErr error) {
	t.Helper()
	wi, ww, wt, wa := installFn, waitingFn, tallyFn, applicantsFn
	installFn = func(context.Context, string) error { return nil }
	applicantsFn = func(context.Context, string) ([]crm.Applicant, error) { return nil, nil }
	waitingFn = func(context.Context, string) ([]crm.Wait, error) {
		var out []crm.Wait
		for _, p := range people {
			raw, _ := json.Marshal(map[string]any{"user": p.sub, "org": p.org, "name": p.name, "actions": starActions(p.stars), "lead": claims[p.sub][1]})
			out = append(out, crm.Wait{Data: raw, Person: claims[p.sub][0], Created: p.created})
		}
		return out, nil
	}
	tallyFn = func(_ context.Context, in *client.ReferralTallyIn) (*client.ReferralTally, error) {
		if tallyErr != nil {
			return nil, tallyErr
		}
		out := &client.ReferralTally{}
		for _, o := range in.Orgs {
			if c, ok := tally[o]; ok {
				out.Orgs = append(out.Orgs, client.ReferralOrgTally{Org: o, Referrals: c[0], Qualified: c[1]})
			}
		}
		return out, nil
	}
	t.Cleanup(func() { installFn, waitingFn, tallyFn, applicantsFn = wi, ww, wt, wa })
}

// starActions is n verified stars, each on a repository of its own.
func starActions(n int) []map[string]string {
	out := []map[string]string{}
	for i := range n {
		out = append(out, map[string]string{"action": "star", "target": fmt.Sprintf("hanzoai/repo%d", i)})
	}
	return out
}

// tokensOf answers the beta tokens query from used, keyed "<org>/<name>".
func tokensOf(used map[string]int64) func(string, []any) []map[string]any {
	return func(sqlStr string, args []any) []map[string]any {
		if !strings.Contains(sqlStr, "GROUP BY user_id") {
			return nil
		}
		var rows []map[string]any
		for _, a := range args {
			id, _ := a.(string)
			if n, ok := used[id]; ok {
				rows = append(rows, map[string]any{"user_id": id, "tokens": n})
			}
		}
		return rows
	}
}

// mountBeta mounts the leaderboard with a signed-in person minted from X-User-Id,
// the way the identity boundary does.
func mountBeta(t *testing.T) (*zip.App, *optinStore) {
	t.Helper()
	t.Setenv("CLOUD_DATA_DIR", t.TempDir())
	t.Setenv("CLOUD_BRAND", "hanzo")
	app := zip.New(zip.Config{Logger: luxlog.NewNoOpLogger()})
	app.Use(zip.H(func(c *zip.Ctx) error {
		if sub := c.Header("X-User-Id"); sub != "" {
			org := c.Header("X-Org-Id")
			principal.Mint(c, principal.Principal{Org: org, User: sub, Subject: sub, Kind: principal.Person, Stable: true, Holder: org + "/" + sub})
		}
		return c.Continue()
	}))
	compose(app)
	if err := Use(app, cloud.Deps{MasterKey: testMaster}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	t.Cleanup(func() { _ = Shutdown(context.Background()) })
	return app, mountedStore
}

// boardAs reads the beta board as p.
func boardAs(t *testing.T, app *zip.App, p person, query string) BetaBoard {
	t.Helper()
	code, raw := doGet(t, app, "/v1/leaderboard/beta"+query, map[string]string{"X-Org-Id": p.org, "X-User-Id": p.sub, "X-User-Name": p.name})
	if code != http.StatusOK {
		t.Fatalf("board as %s: %d %s", p.sub, code, raw)
	}
	var b BetaBoard
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatalf("board: %v: %s", err, raw)
	}
	return b
}

var (
	ada = person{"sub-ada", "ada-org", "ada", 1, "2026-10-20 10:00:00.000Z"}
	bob = person{"sub-bob", "bob-org", "bob", 1, "2026-10-21 10:00:00.000Z"}
	cy  = person{"sub-cy", "cy-org", "cy", 0, "2026-10-22 10:00:00.000Z"}
)

// order is the board's subjects, best first.
func order(b BetaBoard) []string {
	var out []string
	for _, r := range b.Rows {
		out = append(out, fmt.Sprintf("%d:%d", r.Rank, r.Score))
	}
	return out
}

func TestTheBetaBoardRanksByOneScore(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, tc := range []struct {
			name   string
			people []person
			tally  map[string][2]int
			tokens map[string]int64
			want   []string // rank:score, best first
			first  person
		}{
			{"an earlier applicant is ahead on a tie", []person{ada, bob, cy}, nil, nil, []string{"1:1", "2:1", "3:0"}, ada},
			{"tokens move a person up", []person{ada, bob, cy}, nil, map[string]int64{"cy-org/cy": 2_500_000}, []string{"1:25", "2:1", "3:1"}, cy},
			{"a qualified referral outweighs stars", []person{ada, bob, cy}, map[string][2]int{"bob-org": {2, 1}}, nil, []string{"1:23", "2:1", "3:0"}, bob},
			{"a shared org's referrals are no one's", []person{ada, {"sub-dee", "hanzo", "dee", 0, "2026-10-23 10:00:00.000Z"}}, map[string][2]int{"hanzo": {50, 50}}, nil, []string{"1:1", "2:0"}, ada},
			{"an org's referrals count once, on its earliest record", []person{ada, {"sub-ann", "ada-org", "ann", 1, "2026-10-24 10:00:00.000Z"}}, map[string][2]int{"ada-org": {3, 0}}, nil, []string{"1:4", "2:1"}, ada},
		} {
			t.Run(tc.name, func(t *testing.T) {
				standIn(t, tc.people, tc.tally, nil)
				installFakeDS(t, tokensOf(tc.tokens))
				app, _ := mountBeta(t)
				b := boardAs(t, app, tc.first, "")
				if got := order(b); !slices.Equal(got, tc.want) {
					t.Fatalf("order = %v, want %v", got, tc.want)
				}
				if b.You == nil || b.You.Rank != 1 || !b.You.You || b.Of != len(tc.people) || !b.Tokens || !b.Referrals {
					t.Fatalf("board = %+v, you = %+v", b, b.You)
				}
			})
		}
	})
}

func TestAPersonIsNamedOnlyWithConsent(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		standIn(t, []person{ada, bob, cy}, nil, nil)
		installFakeDS(t, tokensOf(map[string]int64{"bob-org/bob": 3_000_000, "cy-org/cy": 1_000}))
		app, store := mountBeta(t)
		if err := store.PutUser(context.Background(), userOptin{UserID: "bob-org/bob", Org: "bob-org", Handle: "Bobby", Listed: true}, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			viewer person
			names  map[string]string // rank -> name, "" for null
			exact  map[string]int64  // rank -> tokensUsed shown
		}{
			{ada, map[string]string{"1": "Bobby", "2": "ada", "3": ""}, map[string]int64{"1": 3_000_000, "2": 0, "3": 0}},
			{cy, map[string]string{"1": "Bobby", "2": "", "3": "cy"}, map[string]int64{"1": 3_000_000, "2": 0, "3": 1_000}},
		} {
			b := boardAs(t, app, tc.viewer, "")
			for _, r := range b.Rows {
				rank := fmt.Sprint(r.Rank)
				got := ""
				if r.Name != nil {
					got = *r.Name
				}
				if got != tc.names[rank] || r.TokensUsed != tc.exact[rank] {
					t.Fatalf("as %s, rank %s: name %q tokensUsed %d, want %q %d", tc.viewer.name, rank, got, r.TokensUsed, tc.names[rank], tc.exact[rank])
				}
			}
			if b.Rows[2].Tokens != "<1M" && b.Rows[2].Tokens != "0" {
				t.Fatalf("an unnamed row shows a range: %+v", b.Rows[2])
			}
		}
	})
}

func TestTheBetaBoardLimitClamps(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var people []person
		for i := range 150 {
			people = append(people, person{fmt.Sprintf("sub-%03d", i), fmt.Sprintf("org-%03d", i), fmt.Sprintf("p%03d", i), 0, fmt.Sprintf("2026-10-20 10:%02d:%02d.000Z", i/60, i%60)})
		}
		standIn(t, people, nil, nil)
		installFakeDS(t, tokensOf(nil))
		app, _ := mountBeta(t)
		last := people[149]
		for _, tc := range []struct {
			query string
			rows  int
		}{{"", 25}, {"?limit=0", 25}, {"?limit=-3", 25}, {"?limit=1", 1}, {"?limit=500", 100}} {
			b := boardAs(t, app, last, tc.query)
			if len(b.Rows) != tc.rows || b.Of != 150 || b.You == nil || b.You.Rank != 150 {
				t.Fatalf("%q: %d rows of %d, you %+v", tc.query, len(b.Rows), b.Of, b.You)
			}
		}
	})
}

func TestThePlaceIsTheBoardsRank(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		standIn(t, []person{ada, bob, cy}, map[string][2]int{"cy-org": {1, 0}}, nil)
		installFakeDS(t, tokensOf(map[string]int64{"cy-org/cy": 1_200_000}))
		app, _ := mountBeta(t)
		b := boardAs(t, app, ada, "")
		for _, p := range []person{ada, bob, cy} {
			at, err := Standing(context.Background(), p.sub)
			if err != nil {
				t.Fatal(err)
			}
			row := slices.IndexFunc(b.Rows, func(r BetaRow) bool { return r.Rank == at.Place })
			if at.Place == 0 || row < 0 || b.Rows[row].Score != at.Score || at.Of != b.Of {
				t.Fatalf("%s: place %+v against the board %v", p.name, at, order(b))
			}
		}
		if at, _ := Standing(context.Background(), "sub-nobody"); at.Place != 0 || at.Of != 3 {
			t.Fatalf("no record: %+v", at)
		}
	})
}

func TestAPeerThatDoesNotAnswerIsLeftOutForEveryone(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		standIn(t, []person{ada, bob, cy}, map[string][2]int{"cy-org": {9, 9}}, errors.New("referral peer down"))
		oen := datastoreEnabled
		t.Cleanup(func() { datastoreEnabled = oen })
		datastoreEnabled = func() bool { return false }
		app, _ := mountBeta(t)
		b := boardAs(t, app, cy, "")
		if b.Tokens || b.Referrals || !slices.Equal(order(b), []string{"1:1", "2:1", "3:0"}) {
			t.Fatalf("board = %+v", b)
		}
	})
}

func TestTheBetaBoardNeedsASignedInPerson(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		standIn(t, []person{ada}, nil, nil)
		installFakeDS(t, tokensOf(nil))
		app, _ := mountBeta(t)
		if code, _ := doGet(t, app, "/v1/leaderboard/beta", nil); code != http.StatusUnauthorized {
			t.Fatalf("anonymous: %d", code)
		}
	})
}

// A lead that applied before it had an account is on the board as a row of its own.
var (
	lead1 = crm.Applicant{Ref: "lead_111111111111111111111111", Person: "person-one", At: "2026-10-01 09:00:00.000Z"}
	lead2 = crm.Applicant{Ref: "lead_222222222222222222222222", Person: "person-two", At: "2026-10-25 09:00:00.000Z"}
)

// rows is the board as day:score:named, best first, for a signed-in viewer.
func rows(b BetaBoard) []string {
	var out []string
	for _, r := range b.Rows {
		n := "-"
		if r.Name != nil {
			n = *r.Name
		}
		out = append(out, fmt.Sprintf("%s:%d:%s", r.Applied, r.Score, n))
	}
	return out
}

func TestEveryoneWhoEverAppliedIsOnTheBoard(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		standIn(t, []person{ada, bob, cy}, nil, nil)
		// One filed before every record, one after: both are rows, ranked by score and then by day.
		leads(t, lead1, lead2)
		installFakeDS(t, tokensOf(nil))
		app, _ := mountBeta(t)
		b := boardAs(t, app, cy, "")
		want := []string{"2026-10-20:1:-", "2026-10-21:1:-", "2026-10-01:0:-", "2026-10-22:0:cy", "2026-10-25:0:-"}
		if got := rows(b); !slices.Equal(got, want) {
			t.Fatalf("board = %v, want %v", got, want)
		}
		if b.Of != 5 || b.You == nil || b.You.Rank != 4 {
			t.Fatalf("of %d, you %+v", b.Of, b.You)
		}
		// A lead is a builder: no name, no address, nothing that says who.
		raw, _ := json.Marshal(b)
		if strings.Contains(string(raw), "@") || strings.Contains(string(raw), "person-") || strings.Contains(string(raw), "lead_") {
			t.Fatalf("the board says who a lead is: %s", raw)
		}
	})
}

func TestALeadEarnsNothingAndASignupMovesAPersonAboveIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		standIn(t, []person{cy}, map[string][2]int{"cy-org": {1, 0}}, nil)
		leads(t, lead1)
		installFakeDS(t, tokensOf(nil))
		app, _ := mountBeta(t)
		if got := order(boardAs(t, app, cy, "")); !slices.Equal(got, []string{"1:1", "2:0"}) {
			t.Fatalf("a referral climbs above an earlier lead: %v", got)
		}
	})
}

func TestAPersonIsOneRowHoweverManyFormsTheirAddressFiled(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		again := crm.Applicant{Ref: "lead_333333333333333333333333", Person: lead1.Person, At: "2026-10-12 09:00:00.000Z"}
		earlier := crm.Applicant{Ref: "lead_444444444444444444444444", Person: lead1.Person, At: "2026-09-30 09:00:00.000Z"}
		standIn(t, nil, nil, nil)
		leads(t, again, lead1, earlier)
		installFakeDS(t, tokensOf(nil))
		app, _ := mountBeta(t)
		b := boardAs(t, app, ada, "")
		if got := rows(b); !slices.Equal(got, []string{"2026-09-30:0:-"}) || b.Of != 1 {
			t.Fatalf("board = %v of %d, want one row, on the earliest form", got, b.Of)
		}
	})
}

func TestARecordClaimsTheLeadOfItsPersonOrItsReference(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		for _, tc := range []struct {
			name   string
			claims map[string][2]string // sub -> person, lead
			want   []string             // day:score:named as ada sees it, best first
			cy     int                  // cy's place
		}{
			{"by the person, a record keeps the place its first form earned",
				map[string][2]string{"sub-cy": {"person-one", ""}},
				[]string{"2026-10-20:1:ada", "2026-10-01:0:-", "2026-10-25:0:-"}, 2},
			{"by the reference, whoever filed it",
				map[string][2]string{"sub-cy": {"person-cy", lead1.Ref}},
				[]string{"2026-10-20:1:ada", "2026-10-01:0:-", "2026-10-25:0:-"}, 2},
			{"a lead filed later moves no one back, and is no row of its own",
				map[string][2]string{"sub-cy": {"person-two", ""}},
				[]string{"2026-10-20:1:ada", "2026-10-01:0:-", "2026-10-22:0:-"}, 3},
			{"a lead no one claims stays a row",
				map[string][2]string{"sub-cy": {"person-nobody", "lead_999999999999999999999999"}},
				[]string{"2026-10-20:1:ada", "2026-10-01:0:-", "2026-10-22:0:-", "2026-10-25:0:-"}, 3},
		} {
			t.Run(tc.name, func(t *testing.T) {
				standIn(t, []person{ada, cy}, nil, nil)
				for sub, c := range tc.claims {
					claiming(t, sub, c[0], c[1])
				}
				leads(t, lead1, lead2)
				installFakeDS(t, tokensOf(nil))
				app, _ := mountBeta(t)
				b := boardAs(t, app, ada, "")
				if got := rows(b); !slices.Equal(got, tc.want) || b.Of != len(tc.want) {
					t.Fatalf("board = %v of %d, want %v", got, b.Of, tc.want)
				}
				if at, err := Standing(context.Background(), cy.sub); err != nil || at.Place != tc.cy {
					t.Fatalf("cy's place = %+v %v, want %d", at, err, tc.cy)
				}
			})
		}
	})
}

func TestALeadGoesToTheEarliestRecordThatClaimsIt(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		first := person{"sub-one", "one-org", "one", 0, "2026-10-20 10:00:00.000Z"}
		second := person{"sub-two", "two-org", "two", 0, "2026-10-22 10:00:00.000Z"}
		standIn(t, []person{first, second}, nil, nil)
		claiming(t, first.sub, lead1.Person, "")
		claiming(t, second.sub, lead1.Person, "")
		leads(t, lead1)
		installFakeDS(t, tokensOf(nil))
		app, _ := mountBeta(t)
		b := boardAs(t, app, first, "")
		if got := rows(b); !slices.Equal(got, []string{"2026-10-01:0:one", "2026-10-22:0:-"}) || b.Of != 2 {
			t.Fatalf("board = %v of %d: one person's forms are one place, not two", got, b.Of)
		}
	})
}

func TestTiesOnTheSameInstantFallToTheID(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		a := crm.Applicant{Ref: "lead_aaaaaaaaaaaaaaaaaaaaaaaa", Person: "p-a", At: "2026-10-01 09:00:00.000Z"}
		z := crm.Applicant{Ref: "lead_zzzzzzzzzzzzzzzzzzzzzzzz", Person: "p-z", At: "2026-10-01 09:00:00.000Z"}
		standIn(t, nil, nil, nil)
		leads(t, z, a)
		installFakeDS(t, tokensOf(nil))
		mountBeta(t)
		st, err := rank(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(st.waiters) != 2 || st.waiters[0].id != "lead:"+a.Ref || st.waiters[1].id != "lead:"+z.Ref {
			t.Fatalf("waiters = %+v", st.waiters)
		}
	})
}

func TestThePlaceOfARecordAndTheBoardAgreeOverLeads(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		standIn(t, []person{ada, bob, cy}, nil, nil)
		claiming(t, cy.sub, lead2.Person, "")
		leads(t, lead1, lead2)
		installFakeDS(t, tokensOf(nil))
		app, _ := mountBeta(t)
		b := boardAs(t, app, ada, "")
		for _, p := range []person{ada, bob, cy} {
			at, err := Standing(context.Background(), p.sub)
			if err != nil {
				t.Fatal(err)
			}
			if at.Place == 0 || at.Of != b.Of || at.Of != 4 {
				t.Fatalf("%s: %+v against a board of %d", p.name, at, b.Of)
			}
			if row := b.Rows[at.Place-1]; row.Score != at.Score {
				t.Fatalf("%s: place %d is row %+v", p.name, at.Place, row)
			}
		}
	})
}

func TestAWaitlistThatCannotBeReadIsNoBoard(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		standIn(t, []person{ada}, nil, nil)
		was := applicantsFn
		applicantsFn = func(context.Context, string) ([]crm.Applicant, error) { return nil, errors.New("base down") }
		t.Cleanup(func() { applicantsFn = was })
		installFakeDS(t, tokensOf(nil))
		app, _ := mountBeta(t)
		if code, _ := doGet(t, app, "/v1/leaderboard/beta", map[string]string{"X-Org-Id": ada.org, "X-User-Id": ada.sub}); code != http.StatusServiceUnavailable {
			t.Fatalf("a board without the leads is not shown as the board: %d", code)
		}
	})
}

func TestAnApplicationBeganOnTheEarliestStampItCarries(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stamps []string
		want   string
	}{
		{"RFC 3339", []string{"2026-10-20T10:00:00Z"}, "2026-10-20"},
		{"Base's date", []string{"2026-10-20 10:00:00.000Z"}, "2026-10-20"},
		{"a bare day", []string{"2026-10-20"}, "2026-10-20"},
		{"the earliest of several formats", []string{"2026-10-22T10:00:00Z", "2026-10-21 09:00:00.000Z", "2026-10-23"}, "2026-10-21"},
		{"an unreadable stamp is left out", []string{"yesterday", "2026-10-22T10:00:00Z"}, "2026-10-22"},
		{"none is never", []string{"", "soon"}, ""},
	} {
		if got := dayOf(began(tc.stamps...)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
	// A day and the instant on it are ordered by the instant: an RFC 3339 application
	// in the evening is after a record Base stamped that morning.
	if !began("2026-10-20 08:00:00.000Z").Before(began("2026-10-20T20:00:00Z")) {
		t.Fatal("stamps in two formats are compared as instants")
	}
}
