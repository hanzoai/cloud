package marketplace

// expertise.go — how good an expert is at each Hanzo product, from what is really
// recorded and nothing else, with every part of the score shown.
//
// A score is the sum of up to four parts, each from one record:
//
//	usage          lifetime tokens on the product's model family (kai, enso, zen)
//	badges         the reward badges held that show the product was used
//	contributions  merged pull requests and completed bounties awarded in the
//	               product's public repositories
//	jobs           jobs delivered and released in an area of the product, weighted
//	               by the stars buyers gave
//
// A part is left out when it cannot be computed from a record, and never estimated:
//
//   - usage exists for kai, enso and zen alone. The usage ledger (hanzo.cloud_usage
//     and its daily rollup) records the model and the org, user and project, and
//     never the product or app a call came through, so no other product has a
//     usage record to total.
//   - badges exist where the program's seed defines one: try-dev, try-bot, try-team,
//     try-base and try-commerce, and zen-pioneer, enso-navigator and kai-strategist.
//     It defines none for cloud, agents or creative.
//   - contributions need a repository in the table below.
//   - jobs come from the listing, and count for any expert.
//
// The first three come from the rewards program and count only for an expert whose
// reward profile is public; for any other, jobs alone count. All the weights are in
// one place, weights, for staff to tune.

import (
	"cmp"
	"context"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
)

const (
	// maxRoster is the most public experts the shop ranks and filters in memory.
	maxRoster = 5000
	// topPlaces is how many of the best of all experts hold a product's top mark.
	topPlaces = 3
	// shown is how many products an expert's read lists.
	shown = 3
	// rankBuild bounds the building of the ranking.
	rankBuild = 30 * time.Second
)

// product is one product an expert is scored in.
type product struct {
	// id is the name the shop and the score go by.
	id string
	// label is what the product is, in a line.
	label string
	// areas are the listing areas whose delivered jobs count for it.
	areas []string
	// badges are the reward badges that show it was used.
	badges []string
	// family is the model family whose usage counts for it; empty where the usage
	// ledger records no product to total.
	family string
	// repos are the public repositories, as owner/name in lowercase, whose merged
	// work counts for it.
	repos []string
}

// products is every product, in the order the shop lists them. It is the one table
// of what counts for which: a repository, a badge, a model family and an area each
// belong to the product on whose row they stand.
var products = []product{
	{id: "dev", label: "Hanzo Dev, the AI coding agent for the terminal",
		areas: []string{"configure"}, badges: []string{"try-dev"}, repos: []string{"hanzoai/dev"}},
	{id: "bot", label: "Hanzo Bot, a personal AI assistant across messaging channels",
		areas: []string{"bot"}, badges: []string{"try-bot"}, repos: []string{"hanzoai/bot"}},
	{id: "team", label: "Hanzo Team, the workspace where people and agents work together",
		areas: []string{"configure"}, badges: []string{"try-team"}, repos: []string{"hanzoai/team"}},
	{id: "base", label: "Hanzo Base, the multi-tenant SQLite backend",
		areas: []string{"base"}, badges: []string{"try-base"}, repos: []string{"hanzoai/base"}},
	{id: "commerce", label: "Hanzo Commerce, orders, subscriptions and billing",
		areas: []string{"commerce"}, badges: []string{"try-commerce"}, repos: []string{"hanzoai/commerce"}},
	{id: "cloud", label: "Hanzo Cloud, the platform and its API",
		areas: []string{"cloud"}, repos: []string{"hanzoai/cloud"}},
	{id: "agents", label: "Hanzo Agents, build and run AI agents",
		areas: []string{"agents"}, repos: []string{"hanzoai/agents"}},
	{id: "kai", label: "Kai, typed decision models",
		areas: []string{"models"}, badges: []string{"kai-strategist"}, family: "kai", repos: []string{"hanzoai/kai"}},
	{id: "enso", label: "Enso, Hanzo's frontier model family and router",
		areas: []string{"models"}, badges: []string{"enso-navigator"}, family: "enso", repos: []string{"hanzoai/enso"}},
	{id: "zen", label: "Zen, the open model family",
		areas: []string{"models"}, badges: []string{"zen-pioneer"}, family: "zen", repos: []string{"hanzoai/zen", "zenlm/zen"}},
	{id: "creative", label: "Creative AI: images, video and documents",
		areas: []string{"creative"}, repos: []string{"hanzoai/video"}},
}

// Products are the products an expert is scored in, in the order the shop lists them.
var Products = func() []string {
	ids := make([]string, len(products))
	for i, p := range products {
		ids[i] = p.id
	}
	return ids
}()

// families are the model families usage is totalled for.
func families() []string {
	var out []string
	for _, p := range products {
		if p.family != "" {
			out = append(out, p.family)
		}
	}
	return out
}

// Weights say what each record is worth toward a product's score. Staff tune them
// here; there is no other knob, and a part that weighs nothing is left out.
type Weights struct {
	// Usage is the points for each tenfold of tokens on a product's model family,
	// counted in UsageUnits: Usage × log10(1 + tokens / UsageUnit). A logarithm, so
	// a billion tokens is not worth ten thousand times a hundred thousand.
	Usage float64
	// UsageUnit is the tokens that make one UsageUnit: the program's own point.
	UsageUnit float64
	// Badge is the points for each badge held that shows the product was used.
	Badge float64
	// PullRequest and Bounty are the points for each merged pull request and each
	// completed bounty awarded in one of the product's repositories.
	PullRequest float64
	Bounty      float64
	// Job is the points for each job released in one of the product's areas, times
	// its stars over five; Unrated stands for the stars over five of a listing no
	// buyer has rated yet.
	Job     float64
	Unrated float64
}

// weights are the defaults: a badge is a try, a merged pull request is work, a
// bounty is harder work, and a job is what a buyer paid for.
var weights = Weights{Usage: 10, UsageUnit: client.TokensPerPoint, Badge: 20, PullRequest: 25, Bounty: 50, Job: 10, Unrated: 0.5}

// Score is an expert's score in one product and the parts it is made of.
type Score struct {
	// Product is the product's id.
	Product string `json:"product"`
	// Score is the sum of Parts, to one decimal.
	Score float64 `json:"score"`
	// Parts are the records that contributed.
	Parts Parts `json:"parts"`
}

// Parts are the records a score is made of. A record that is not recorded for the
// product, or is worth nothing, is left out.
type Parts struct {
	// Usage is the points for lifetime tokens on the product's model family.
	Usage float64 `json:"usage,omitempty"`
	// Badges is the points for the badges held that show the product was used.
	Badges float64 `json:"badges,omitempty"`
	// Contributions is the points for merged pull requests and completed bounties
	// in the product's public repositories.
	Contributions float64 `json:"contributions,omitempty"`
	// Jobs is the points for jobs released in the product's areas, weighted by stars.
	Jobs float64 `json:"jobs,omitempty"`
}

// tenth rounds to one decimal, away from zero at the half.
func tenth(x float64) float64 { return math.Round(x*10) / 10 }

// of scores an expert in every product they score in, in the order of products. t is
// the tally of their listing and p what the program said, nil when it said nothing.
func (w Weights) of(l Listing, t tally, p *client.RewardPerson) []Score {
	var out []Score
	for _, pr := range products {
		var parts Parts
		if p != nil {
			if pr.family != "" {
				for _, u := range p.Usage {
					if u.Family == pr.family && u.Tokens > 0 {
						parts.Usage = tenth(w.Usage * math.Log10(1+float64(u.Tokens)/w.UsageUnit))
					}
				}
			}
			for _, b := range pr.badges {
				if slices.Contains(p.Held, b) {
					parts.Badges += w.Badge
				}
			}
			for _, c := range p.Contributions {
				if slices.Contains(pr.repos, c.Repo) {
					switch c.Event {
					case "github.pr.merged":
						parts.Contributions += float64(c.Count) * w.PullRequest
					case "bounty.completed":
						parts.Contributions += float64(c.Count) * w.Bounty
					}
				}
			}
			parts.Badges, parts.Contributions = tenth(parts.Badges), tenth(parts.Contributions)
		}
		if t.completed > 0 && l.Expert != nil && slices.ContainsFunc(l.Expert.Areas, func(a string) bool { return slices.Contains(pr.areas, a) }) {
			stars := w.Unrated
			if t.ratings > 0 {
				stars = float64(t.stars) / float64(t.ratings) / 5
			}
			parts.Jobs = tenth(float64(t.completed) * w.Job * stars)
		}
		if sum := parts.Usage + parts.Badges + parts.Contributions + parts.Jobs; sum > 0 {
			out = append(out, Score{Product: pr.id, Score: tenth(sum), Parts: parts})
		}
	}
	return out
}

// dressing is what the shop adds to an expert's profile.
type dressing struct {
	// person is what the rewards program said of them; nil when it said nothing.
	person *client.RewardPerson
	// reward is its public face.
	reward *Reward
	// scores are their scores in every product they score in.
	scores []Score
	// top are the products in which they rank among the best.
	top []string
}

// score is the dressing's score in a product, 0 for none.
func (d dressing) score(product string) float64 {
	for _, s := range d.scores {
		if s.Product == product {
			return s.Score
		}
	}
	return 0
}

// best is the products they score highest in, best first, at most shown.
func (d dressing) best() []Score {
	out := slices.Clone(d.scores)
	slices.SortStableFunc(out, func(a, b Score) int { return cmp.Compare(b.Score, a.Score) })
	return out[:min(shown, len(out))]
}

// into writes the dressing on an expert's profile.
func (d dressing) into(e *Expert) {
	e.Reward, e.Products, e.Top = d.reward, d.best(), d.top
}

// ranking is the scores of every public expert at one moment, and who is among the
// best of each product.
type ranking struct {
	until  time.Time
	scores map[string][]Score // by listing, for every public expert; nil when it could not be made
	top    map[string][]string
}

// expertise ranks experts and dresses their listings.
type expertise struct {
	store  *Store
	people *standings
	log    interface{ Warn(msg string, args ...any) }
	now    func() time.Time

	mu   sync.Mutex
	rank *ranking
}

func newExpertise(store *Store, log interface{ Warn(msg string, args ...any) }) *expertise {
	x := &expertise{store: store, log: log, now: time.Now}
	x.people = &standings{log: log, now: func() time.Time { return x.now() }, got: map[string]kept{}}
	return x
}

// dress is what to show beside each expert among ls, by listing id. A listing of
// another kind has none. The ranking is the one of every public expert; a listing it
// does not hold — a private one, or one made since — is scored alone and ranks in
// nothing.
func (x *expertise) dress(ctx context.Context, ls []Listing) map[string]dressing {
	out := map[string]dressing{}
	var experts []Listing
	var subjects []string
	for _, l := range ls {
		if l.Expert != nil {
			experts = append(experts, l)
			subjects = append(subjects, l.Expert.Person)
		}
	}
	if len(experts) == 0 {
		return out
	}
	people, _ := x.people.of(ctx, subjects)
	r := x.ranked(ctx)
	var lone []Listing
	for _, l := range experts {
		p := people[l.Expert.Person]
		d := dressing{person: p}
		if p != nil {
			d.reward = rewardOf(p)
		}
		if scores, ok := r.scores[l.ID]; ok {
			d.scores, d.top = scores, r.top[l.ID]
		} else {
			lone = append(lone, l)
		}
		out[l.ID] = d
	}
	if len(lone) > 0 {
		if reps, err := x.store.reputations(ctx, lone); err != nil {
			x.log.Warn("marketplace: experts were not scored", "err", err)
		} else {
			for _, l := range lone {
				d := out[l.ID]
				d.scores = weights.of(l, *reps[l.ID], d.person)
				out[l.ID] = d
			}
		}
	}
	return out
}

// ranked is the ranking of every public expert, made at most once in rewardKept. One
// that could not be made — too many experts, the store failing — is empty, and
// tried again after rewardDown.
func (x *expertise) ranked(ctx context.Context) *ranking {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.rank != nil && x.now().Before(x.rank.until) {
		return x.rank
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rankBuild)
	defer cancel()
	r := &ranking{until: x.now().Add(rewardDown)}
	x.rank = r
	ls, err := x.store.experts(ctx, maxRoster+1)
	if err == nil && len(ls) > maxRoster {
		x.log.Warn("marketplace: too many experts to rank", "max", maxRoster)
		return r
	}
	if err != nil {
		x.log.Warn("marketplace: experts were not ranked", "err", err)
		return r
	}
	reps, err := x.store.reputations(ctx, ls)
	if err != nil {
		x.log.Warn("marketplace: experts were not ranked", "err", err)
		return r
	}
	subjects := make([]string, len(ls))
	for i, l := range ls {
		subjects[i] = l.Expert.Person
	}
	people, complete := x.people.of(ctx, subjects)
	if complete {
		r.until = x.now().Add(rewardKept)
	}
	r.scores, r.top = make(map[string][]Score, len(ls)), make(map[string][]string, len(ls))
	created := map[string]int64{}
	for _, l := range ls {
		r.scores[l.ID] = weights.of(l, *reps[l.ID], people[l.Expert.Person])
		created[l.ID] = l.CreatedAt
	}
	for _, pr := range products {
		var placed []string
		for id, scores := range r.scores {
			if slices.ContainsFunc(scores, func(s Score) bool { return s.Product == pr.id }) {
				placed = append(placed, id)
			}
		}
		at := func(id string) float64 {
			for _, s := range r.scores[id] {
				if s.Product == pr.id {
					return s.Score
				}
			}
			return 0
		}
		slices.SortFunc(placed, func(a, b string) int { return byScore(at(a), at(b), created[a], created[b], a, b) })
		for _, id := range placed[:min(topPlaces, len(placed))] {
			r.top[id] = append(r.top[id], pr.id)
		}
	}
	return r
}

// byScore orders two experts by a score: the higher first, then the newer listing,
// then the id, so the order is total and a page boundary never splits a tie.
func byScore(a, b float64, atA, atB int64, idA, idB string) int {
	return cmp.Or(cmp.Compare(b, a), cmp.Compare(atB, atA), strings.Compare(idA, idB))
}
