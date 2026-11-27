package reward

// backfill.go — what members earned before there was a program, written once.
//
// The legacy points are the beta-board scoring cloud already applies, counted from
// what cloud recorded, and they are NOT a second formula: every term is
// client.Points, the one score the standing, the bounty board and the waitlist
// share, applied to one source at a time (tokens at 100K per point, a referred
// signup, a referred org with spend, a verified star, a follow, a bounty award). A
// term no source has recorded is zero and is never estimated: no source records a
// fork before the program, so the backfill writes none.
//
// WHO IT IS FOR. Tokens are a person's, read by the "<org>/<name>" the ledger
// records them under. Referrals and bounty awards are an org's, credited to the
// org's owner (people.go). Stars and follows are a waitlist record's, credited to
// the person it names. Nobody outside the directory earns.
//
// The engine writes it as legacy rows: counted in lifetime points, levels and rank,
// never redeemable, idempotent per person and source (the first count stands), and
// one trail record names who ran each call.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"

	"github.com/hanzoai/reward/ledger"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	leaderboardpeer "github.com/hanzoai/cloud/client/leaderboard"
	referralpeer "github.com/hanzoai/cloud/client/referral"
	"github.com/hanzoai/cloud/sdk"
)

// program is the one program cloud mounts.
const program = "hanzo-rewards"

// batch is the most rows the engine takes in one backfill.
const batch = 5000

// The sources of legacy counts, as the platform reads them; variables so a test answers them.
var (
	tallyFn   = referralpeer.ReferralTallies
	bountyFn  = leaderboardpeer.LeaderboardBountyTotals
	waitingFn = crm.Waiting
	installFn = crm.Install
)

// legacyOut is what a backfill wrote.
type legacyOut struct {
	// By is who ran it, as the trail names them.
	By string `json:"by"`
	// Subjects is how many people it credited, Entries how many rows it wrote that
	// were new, and Points their sum.
	Subjects int   `json:"subjects"`
	Entries  int   `json:"entries"`
	Points   int64 `json:"points"`
	// Rows is how many rows it offered: the rest were counted by an earlier run.
	Rows int `json:"rows"`
	// Sources says, per source, how many people it credited and with how many points.
	Sources map[string]sourceOut `json:"sources"`
}

// sourceOut is one source's share of a backfill.
type sourceOut struct {
	People int   `json:"people"`
	Points int64 `json:"points"`
}

// legacyRows reads every source and returns the rows to write.
func (s *state) legacyRows(ctx context.Context) ([]ledger.Past, error) {
	who, err := roster(ctx)
	if err != nil {
		return nil, err
	}
	var rows []ledger.Past
	add := func(sub, source string, src client.Sources, evidence string) {
		if pts := client.Points(src); pts > 0 && sub != "" {
			rows = append(rows, ledger.Past{Subject: sub, Source: source, Points: int64(pts), Evidence: evidence})
		}
	}

	if readyFn() {
		tokens, err := queryFn(ctx, `SELECT user_id, sum(total_tokens) AS total FROM `+rollup+` GROUP BY user_id`)
		if err != nil {
			return nil, fmt.Errorf("usage rollup: %w", err)
		}
		byPerson := map[string]int64{}
		for _, r := range tokens {
			if sub, ok := who.bySlug[txt(r["user_id"])]; ok {
				byPerson[sub] += num(r["total"])
			}
		}
		for _, sub := range sorted(byPerson) {
			add(sub, "usage.tokens", client.Sources{Tokens: byPerson[sub]}, strconv.FormatInt(byPerson[sub], 10)+" tokens used")
		}
	}

	brand := cloud.For(context.WithoutCancel(ctx), cloud.Brand())
	orgs := sorted(who.owner)
	for chunk := range slices.Chunk(orgs, 1000) {
		out, err := tallyFn(brand, &client.ReferralTallyIn{Orgs: chunk})
		if err != nil {
			return nil, fmt.Errorf("referral tally: %w", err)
		}
		for _, t := range out.Orgs {
			add(who.owner[t.Org], "referral.signup", client.Sources{Referrals: t.Referrals}, fmt.Sprintf("%d orgs signed up with org %s's code", t.Referrals, t.Org))
			add(who.owner[t.Org], "referral.paying", client.Sources{Qualified: t.Qualified}, fmt.Sprintf("%d of them made metered spend", t.Qualified))
		}
	}

	if err := installFn(brand, cloud.Brand()); err != nil {
		return nil, fmt.Errorf("waitlist: %w", err)
	}
	recs, err := waitingFn(brand, cloud.Brand())
	if err != nil {
		return nil, fmt.Errorf("waitlist: %w", err)
	}
	stars, follows := map[string]map[string]bool{}, map[string]map[string]bool{}
	for _, r := range recs {
		var d struct {
			User    string `json:"user"`
			Actions []struct {
				Action string `json:"action"`
				Target string `json:"target"`
			} `json:"actions"`
		}
		if json.Unmarshal(r.Data, &d) != nil || !who.member[d.User] {
			continue
		}
		for _, a := range d.Actions {
			set := stars
			if a.Action == "follow" {
				set = follows
			} else if a.Action != "star" {
				continue
			}
			if set[d.User] == nil {
				set[d.User] = map[string]bool{}
			}
			set[d.User][a.Target] = true
		}
	}
	for _, sub := range sorted(stars) {
		add(sub, "github.star", client.Sources{Stars: len(stars[sub])}, fmt.Sprintf("%d repositories starred, verified on the waitlist", len(stars[sub])))
	}
	for _, sub := range sorted(follows) {
		add(sub, "github.follow", client.Sources{Follows: len(follows[sub])}, fmt.Sprintf("%d accounts followed, verified on the waitlist", len(follows[sub])))
	}

	bounty, err := bountyFn(brand)
	if err != nil {
		return nil, fmt.Errorf("bounty totals: %w", err)
	}
	totals := bounty.Map()
	for _, org := range sorted(totals) {
		add(who.owner[org], "bounty", client.Sources{Bounty: totals[org]}, fmt.Sprintf("%d points of bounty awards to org %s", totals[org], org))
	}
	return rows, nil
}

// legacy writes the backfill and reports it. by is the SuperAdmin who ran it.
func (s *state) legacy(ctx context.Context, by string) (*legacyOut, error) {
	rows, err := s.legacyRows(ctx)
	if err != nil {
		return nil, err
	}
	out := &legacyOut{By: by, Rows: len(rows), Sources: map[string]sourceOut{}}
	people := map[string]bool{}
	for chunk := range slices.Chunk(rows, batch) {
		res, err := s.eng.Backfill(ctx, program, chunk, by)
		if err != nil {
			return out, err
		}
		out.Entries += res.Written
		out.Points += res.Points
		for _, r := range chunk {
			people[r.Subject] = true
			x := out.Sources[r.Source]
			x.People++
			x.Points += r.Points
			out.Sources[r.Source] = x
		}
	}
	out.Subjects = len(people)
	return out, nil
}
