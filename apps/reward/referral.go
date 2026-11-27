package reward

// referral.go — a referral that activated, one that was retained, and the person it
// brought in welcomed.
//
// apps/referral holds the edges, a referrer org to the org it referred. What an
// edge DID is the referee org's use, so activation and retention are read from the
// usage rollup, never from a flag the referee could set: the referral activated on
// the first day the referee used any tokens, and was retained when the referee used
// tokens again at least seven days after that first day. The credit is the referrer
// org's owner's, the first live admin IAM names for it; a held org (one the platform
// runs for strangers) has no owner and earns nothing, nor does a referral of oneself.

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/refuse"

	"github.com/hanzoai/cloud/client"
	referralpeer "github.com/hanzoai/cloud/client/referral"
	"github.com/hanzoai/cloud/sdk"
)

// edgesFn reads one page of referral edges as the brand, a variable so a test answers it.
var edgesFn = func(ctx context.Context, offset, limit int) ([]client.ReferralEdge, error) {
	out, err := referralpeer.ReferralEdges(cloud.For(context.WithoutCancel(ctx), cloud.Brand()), &client.ReferralEdgesIn{Offset: offset, Limit: limit})
	if err != nil {
		return nil, err
	}
	return out.Edges, nil
}

const (
	// edgePage is how many edges are read at a time.
	edgePage = 1000
	// retainedAfter is how long after its first use a referee must come back.
	retainedAfter = 7 * 24 * time.Hour
	// milestoneEvery is how many activated friends make a milestone.
	milestoneEvery = 5
	// orgChunk bounds the orgs one rollup query names.
	orgChunk = 500
)

// activity is the first and last day an org used tokens.
type activity struct{ first, last time.Time }

// usedBy reads the days each org first and last used tokens.
func usedBy(ctx context.Context, orgs []string) (map[string]activity, error) {
	out := map[string]activity{}
	for chunk := range slices.Chunk(orgs, orgChunk) {
		args := make([]any, len(chunk))
		marks := ""
		for i, o := range chunk {
			args[i] = o
			if i > 0 {
				marks += ","
			}
			marks += "?"
		}
		rows, err := queryFn(ctx, `SELECT organization, toString(min(day)) AS first, toString(max(day)) AS last
			FROM `+rollup+` WHERE total_tokens > 0 AND organization IN (`+marks+`) GROUP BY organization`, args...)
		if err != nil {
			return nil, fmt.Errorf("usage rollup: %w", err)
		}
		for _, r := range rows {
			f, ferr := time.Parse(time.DateOnly, txt(r["first"]))
			l, lerr := time.Parse(time.DateOnly, txt(r["last"]))
			if ferr == nil && lerr == nil {
				out[txt(r["organization"])] = activity{f, l}
			}
		}
	}
	return out, nil
}

// sweepReferrals states the referrals that activated and the ones retained.
func (s *state) sweepReferrals(ctx context.Context) error {
	if !readyFn() {
		return nil
	}
	var edges []client.ReferralEdge
	for offset := 0; ; offset += edgePage {
		page, err := edgesFn(ctx, offset, edgePage)
		if err != nil {
			return err
		}
		edges = append(edges, page...)
		if len(page) < edgePage {
			break
		}
	}
	who, err := roster(ctx)
	if err != nil {
		return err
	}
	var referees []string
	for _, e := range edges {
		referees = append(referees, e.Referee)
	}
	used, err := usedBy(ctx, referees)
	if err != nil {
		return err
	}
	// parent is the org that referred each org, to find a referrer's own sponsor.
	parent := map[string]string{}
	for _, e := range edges {
		parent[e.Referee] = e.Referrer
	}
	activated := map[string]int{}
	var stated int
	for _, e := range edges {
		referrer, ok := who.owner[e.Referrer]
		referee := who.owner[e.Referee]
		if !ok || e.Referrer == e.Referee || authz.IsHeldOrg(e.Referrer) || (referee != "" && referee == referrer) {
			continue
		}
		// An org that used Hanzo before it claimed the code was not brought in by it:
		// a claim is open to any org at any time, and a sign-up is not.
		if first, did := used[e.Referee]; did && first.first.Before(time.Unix(e.CreatedAt, 0).UTC().Truncate(24*time.Hour)) {
			continue
		}
		// The sponsor link comes first and does not wait for the referee to use
		// anything: a person's sponsor is who brought them in. The engine refuses a
		// link that would make a cycle; that is permanent, so it is marked and said once.
		if referee != "" {
			linked := catalog.Event{ID: "referral.linked:" + e.Referee, Type: "referral.linked", Subject: referee, At: time.Unix(e.CreatedAt, 0).UTC(),
				Fields: map[string]any{"sponsor": referrer}}
			switch ok, err := s.once(ctx, linked); {
			case refuse.Is(err):
				s.base.Log.Warn("reward: a referral link was refused and is left", "referrer", referrer, "referee", referee, "err", err)
				if err := s.mark1(ctx, linked); err != nil {
					return err
				}
			case err != nil:
				return err
			case ok:
				stated++
			}
		}
		a, did := used[e.Referee]
		if !did {
			continue
		}
		events := []catalog.Event{{ID: "referral.activated:" + e.Referee, Type: "referral.activated", Subject: referrer, At: a.first, Fields: map[string]any{}}}
		// The referred person is welcomed for the same first task, when we know who they are.
		if referee != "" {
			events = append(events, catalog.Event{ID: "referral.welcomed:" + e.Referee, Type: "referral.welcomed", Subject: referee, At: a.first,
				Fields: map[string]any{"sponsor": referrer}})
		}
		activated[referrer]++
		// A friend of a friend: the referrer's own sponsor, one level up and no more.
		if grand, ok := who.owner[parent[e.Referrer]]; ok && grand != referrer && grand != referee && !authz.IsHeldOrg(parent[e.Referrer]) {
			events = append(events, catalog.Event{ID: "referral.network:" + e.Referee, Type: "referral.network", Subject: grand, At: a.first,
				Fields: map[string]any{"referee": e.Referee}})
		}
		if a.last.Sub(a.first) >= retainedAfter {
			events = append(events, catalog.Event{ID: "referral.retained:" + e.Referee, Type: "referral.retained", Subject: referrer, At: a.last, Fields: map[string]any{}})
		}
		for _, ev := range events {
			ok, err := s.once(ctx, ev)
			if err != nil {
				return err
			}
			if ok {
				stated++
			}
		}
	}
	// Every fifth activated friend is a milestone; the event for each multiple is stated
	// once, so a count that has passed ten has also been five.
	for _, referrer := range sorted(activated) {
		for m := milestoneEvery; m <= activated[referrer]; m += milestoneEvery {
			ok, err := s.once(ctx, catalog.Event{ID: "referral.milestone:" + referrer + ":" + strconv.Itoa(m), Type: "referral.milestone", Subject: referrer,
				At: time.Now().UTC(), Fields: map[string]any{"count": m}})
			if err != nil {
				return err
			}
			if ok {
				stated++
			}
		}
	}
	if stated > 0 {
		s.base.Log.Info("reward: referrals stated", "events", stated)
	}
	return nil
}
