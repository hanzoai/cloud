package marketplace

// reward.go — what the rewards program says of an expert, shown beside their
// listing.
//
// The program is another app, and in the fleet another process, so it is asked over
// the internal plane (reward_people, apps/reward/profiles.go) as the platform. It
// answers only for a person whose reward profile is public, and the shop shows only
// what that profile shows: level, points, the three rarest badges, the claims
// awarded under each pillar and the token tier. Never the subject, an email, an org
// or a redeemable balance.
//
// It is an addition and never a dependency. A program that is not mounted, is
// slow or answers an error leaves the field out, and the read it was asked for
// succeeds as it would without it. An answer is kept for rewardKept so a page of
// two hundred does not ask for two hundred profiles, and a failed ask is not
// repeated for rewardDown.

import (
	"context"
	"slices"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	rewardpeer "github.com/hanzoai/cloud/client/reward"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/authz"
)

const (
	// rewardWait bounds one ask of the program.
	rewardWait = 3 * time.Second
	// rewardKept is how long an answer, or the program's silence about a subject,
	// is kept.
	rewardKept = time.Minute
	// rewardDown is how long after a failed ask the program is not asked again.
	rewardDown = 10 * time.Second
	// rewardBatch is the most subjects one ask names: the program's own bound.
	rewardBatch = 200
	// rewardMax is how many answers are kept before the cache starts over.
	rewardMax = 20000
)

// Reward is an expert's public standing in the rewards program.
type Reward struct {
	// Level is the level the profile shows, such as 2.
	Level int `json:"level"`
	// LevelName is its name, such as Shipper.
	LevelName string `json:"levelName"`
	// Points are the lifetime points.
	Points int64 `json:"points"`
	// Tier is the highest token milestone the expert holds the badge of, never a
	// count of tokens; absent until they hold one.
	Tier *Tier `json:"tier,omitempty"`
	// Badges are the three the profile shows first, the rarest first.
	Badges []Badge `json:"badges"`
	// Pillars counts the claims awarded under each pillar.
	Pillars Pillars `json:"pillars"`
}

// Pillars counts the claims awarded under each of the program's five pillars.
type Pillars struct {
	// Use is claims for using Hanzo: models and products.
	Use int `json:"use"`
	// Share is claims for sharing it: referrals and posts.
	Share int `json:"share"`
	// Community is claims for joining in: Discord, stars and follows.
	Community int `json:"community"`
	// Contribute is claims for building it: merged work, bounties and demos.
	Contribute int `json:"contribute"`
	// Expert is claims for the work of an expert on the marketplace.
	Expert int `json:"expert"`
}

// Tier is a token milestone: the badge that says it was reached and how it reads.
type Tier struct {
	// ID is the badge's id, such as billionaire.
	ID string `json:"id"`
	// Label is how the milestone reads, such as 1B+.
	Label string `json:"label"`
	// Param is what the shop's tokenTier filter calls it. It is not on the wire.
	Param string `json:"-"`
}

// Badge is one badge as the public profile shows it.
type Badge struct {
	// ID is the badge's id, such as first-merge.
	ID string `json:"id"`
	// Name is what it is called.
	Name string `json:"name"`
	// Pillar is what it is for: use, share, community, contribute or expert.
	Pillar string `json:"pillar"`
	// Rarity is how rare it is, from common up.
	Rarity string `json:"rarity"`
}

// tiers are the token milestones in the order they are reached, with the ids of the
// badges the program's seed gives for them (hanzoai/reward engine/seed/rewards.json).
// A tier is the highest of these an expert holds.
var tiers = []Tier{
	{ID: "first-100k", Label: "100K+", Param: "100k"},
	{ID: "million-token-club", Label: "1M+", Param: "1m"},
	{ID: "five-million", Label: "5M+", Param: "5m"},
	{ID: "ten-million", Label: "10M+", Param: "10m"},
	{ID: "century", Label: "100M+", Param: "100m"},
	{ID: "billionaire", Label: "1B+", Param: "1b"},
	{ID: "titan", Label: "10B+", Param: "10b"},
	{ID: "colossus", Label: "100B+", Param: "100b"},
	{ID: "trillion-club", Label: "1T+", Param: "1t"},
}

// rank is the place of the highest tier among held in tiers, counted from 1: 0
// for none.
func rank(held []string) int {
	for i := len(tiers) - 1; i >= 0; i-- {
		if slices.Contains(held, tiers[i].ID) {
			return i + 1
		}
	}
	return 0
}

// rewardOf is what the shop shows of a person the program answered for.
func rewardOf(p *client.RewardPerson) *Reward {
	r := &Reward{Level: p.Level, LevelName: p.LevelName, Points: p.Points, Badges: make([]Badge, 0, len(p.Badges)),
		Pillars: Pillars{Use: p.Count("use"), Share: p.Count("share"), Community: p.Count("community"),
			Contribute: p.Count("contribute"), Expert: p.Count("expert")}}
	for _, b := range p.Badges {
		r.Badges = append(r.Badges, Badge{ID: b.ID, Name: b.Name, Pillar: b.Pillar, Rarity: b.Rarity})
	}
	if n := rank(p.Held); n > 0 {
		t := tiers[n-1]
		r.Tier = &t
	}
	return r
}

// standings asks the program about subjects and keeps what it says.
type standings struct {
	log interface{ Warn(msg string, args ...any) }
	now func() time.Time

	mu   sync.Mutex
	got  map[string]kept
	down time.Time
}

// kept is the program's answer about one subject; who is nil when it said nothing,
// which is what a private profile and an unknown subject both are.
type kept struct {
	at  time.Time
	who *client.RewardPerson
}

// of is the program's answer for each of subjects it has one for. complete is false
// when the program could not be asked about some: those are left out, and the
// caller should not keep what it makes of the rest for long.
func (s *standings) of(ctx context.Context, subjects []string) (out map[string]*client.RewardPerson, complete bool) {
	out = map[string]*client.RewardPerson{}
	var need []string
	seen := map[string]bool{}
	now := s.now()
	s.mu.Lock()
	for _, sub := range subjects {
		if sub == "" || seen[sub] {
			continue
		}
		seen[sub] = true
		if k, ok := s.got[sub]; ok && now.Sub(k.at) < rewardKept {
			if k.who != nil {
				out[sub] = k.who
			}
			continue
		}
		need = append(need, sub)
	}
	down := now.Before(s.down)
	s.mu.Unlock()
	if len(need) == 0 {
		return out, true
	}
	if down {
		return out, false
	}
	for batch := range slices.Chunk(need, rewardBatch) {
		// As the platform, from no request: a request on the context would be forwarded
		// in its place, and the shop is read by anyone.
		ask, cancel := context.WithTimeout(cloud.For(context.Background(), authz.AdminOrg), rewardWait)
		answer, err := rewardpeer.RewardPeople(ask, &client.RewardPeopleIn{Subjects: batch, Families: families()})
		cancel()
		s.mu.Lock()
		if err != nil {
			s.down = s.now().Add(rewardDown)
			s.mu.Unlock()
			s.log.Warn("marketplace: the rewards program was not asked", "err", err)
			return out, false
		}
		if len(s.got) > rewardMax {
			s.got = map[string]kept{}
		}
		at := s.now()
		for _, sub := range batch {
			s.got[sub] = kept{at: at}
		}
		for i := range answer.People {
			if p := &answer.People[i]; slices.Contains(batch, p.Subject) {
				s.got[p.Subject] = kept{at: at, who: p}
				out[p.Subject] = p
			}
		}
		s.mu.Unlock()
	}
	return out, true
}
