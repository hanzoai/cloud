package reward

// profiles.go — what the marketplace asks of the program to rank an expert: the
// public profile of each subject asked about, and the records beside it that a
// profile does not show. It is the one place the program's rows are read for
// another app, and it reads only for a subject who turned their profile on.

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/orm"
	"github.com/hanzoai/reward/claim"
	"github.com/hanzoai/reward/engine"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
)

const (
	// maxAsked is the most subjects one ask names; maxFamilies the most model
	// families.
	maxAsked    = 200
	maxFamilies = 8
	// rosterKept is how long the directory is kept between asks: reading every
	// person of the deployment is the cost of mapping a subject to the name the
	// usage ledger knows them by, and a person's name does not change.
	rosterKept = 5 * time.Minute
)

// family is one model family: the word a model id begins with.
var family = regexp.MustCompile(`^[a-z]{2,16}$`)

// contributions are the events whose awarded claims count as merged work.
var contributions = []string{"github.pr.merged", "bounty.completed"}

// errNotPlatformRead is the refusal for a plane op asked by anyone else.
var errNotPlatformRead = zip.ErrForbidden("reward: only the platform reads across members")

// rolled is the directory as the usage ledger names people, kept for rosterKept.
type rolled struct {
	sync.Mutex
	at     time.Time
	bySub  map[string]string
	loaded bool
}

// slugs is the usage ledger's name for each subject asked, from the directory read
// at most every rosterKept.
func (s *state) slugs(ctx context.Context, subjects []string) (map[string]string, error) {
	s.roll.Lock()
	defer s.roll.Unlock()
	if !s.roll.loaded || time.Since(s.roll.at) > rosterKept {
		who, err := roster(ctx)
		if err != nil {
			return nil, err
		}
		s.roll.bySub = make(map[string]string, len(who.bySlug))
		for slug, sub := range who.bySlug {
			s.roll.bySub[sub] = slug
		}
		s.roll.at, s.roll.loaded = time.Now(), true
	}
	out := make(map[string]string, len(subjects))
	for _, sub := range subjects {
		if slug, ok := s.roll.bySub[sub]; ok {
			out[slug] = sub
		}
	}
	return out, nil
}

// exposeProfiles publishes the ask on the internal plane. routes calls it.
func (s *state) exposeProfiles() {
	cloud.Plane().Post("/reward/people", s.profiles,
		zip.WithOperationID(client.RewardPeople),
		zip.WithSummary("Read the public reward profiles of the subjects named, and the records beside them"))
}

// profiles answers, for each subject named whose reward profile is public, the
// profile as the public sees it — level, points, badges and the claims awarded under
// each pillar — and the records an expert is scored from: every badge held, the
// merged pull requests and completed bounties awarded by repository, and the
// lifetime tokens and requests on each model family asked for. A subject whose
// profile is private, was never made or is unknown is left out, and the answer does
// not say which. It names no email, org or redeemable balance. Only the platform asks.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (s *state) profiles(ctx context.Context, in *client.RewardPeopleIn) (*client.RewardPeopleOut, error) {
	if !platform(ctx) {
		return nil, errNotPlatformRead
	}
	if in == nil {
		return nil, zip.ErrBadRequest("people: subjects are required")
	}
	if len(in.Subjects) > maxAsked {
		return nil, zip.ErrBadRequest(fmt.Sprintf("people: at most %d subjects", maxAsked))
	}
	if len(in.Families) > maxFamilies {
		return nil, zip.ErrBadRequest(fmt.Sprintf("people: at most %d families", maxFamilies))
	}
	for _, f := range in.Families {
		if !family.MatchString(f) {
			return nil, zip.ErrBadRequest("people: a family is a lowercase word such as zen")
		}
	}
	out := &client.RewardPeopleOut{People: []client.RewardPerson{}}
	var subjects []string
	seen := map[string]bool{}
	for _, sub := range in.Subjects {
		if sub = strings.TrimSpace(sub); sub != "" && !seen[sub] {
			seen[sub] = true
			subjects = append(subjects, sub)
		}
	}
	for _, sub := range subjects {
		p, err := s.person(ctx, sub)
		if err != nil {
			return nil, zip.Errorf(502, "people: %v", err)
		}
		if p != nil {
			out.People = append(out.People, *p)
		}
	}
	if len(in.Families) == 0 || len(out.People) == 0 {
		return out, nil
	}
	used, err := s.used(ctx, out.People, in.Families)
	if err != nil {
		s.base.Log.Warn("reward: usage by family was not read", "err", err)
		return out, nil
	}
	for i := range out.People {
		out.People[i].Usage = used[out.People[i].Subject]
	}
	return out, nil
}

// person is what the program knows of sub for another app, or nil when sub's
// profile is not public.
func (s *state) person(ctx context.Context, sub string) (*client.RewardPerson, error) {
	prof, err := s.eng.Profile(ctx, sub)
	if err != nil {
		return nil, err
	}
	if !prof.Public || prof.Handle == "" {
		return nil, nil
	}
	pub, err := s.eng.Show(ctx, prof.Handle, "")
	if errors.Is(err, engine.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p := &client.RewardPerson{Subject: sub, Level: pub.Level, LevelName: pub.LevelName, Points: pub.Points,
		Badges: []client.RewardBadge{}, Held: []string{}, Pillars: client.PillarCounts(pub.Pillars),
		Contributions: []client.RewardContribution{}, Usage: []client.RewardUsage{}}
	for _, b := range pub.Top(3) {
		p.Badges = append(p.Badges, client.RewardBadge{ID: b.ID, Name: b.Name, Pillar: string(b.Pillar), Rarity: string(b.Rarity)})
	}
	for _, b := range pub.Badges {
		p.Held = append(p.Held, b.ID)
	}
	claims, err := orm.TypedQuery[claim.Claim](s.db).Filter("Subject=", sub).Filter("Status=", string(claim.Awarded)).GetAll(ctx)
	if err != nil {
		return nil, err
	}
	count := map[string]int{}
	for _, c := range claims {
		repo, _ := c.Fields["repo"].(string)
		if repo = strings.ToLower(strings.TrimSpace(repo)); repo != "" && slices.Contains(contributions, c.Event) {
			count[repo+" "+c.Event]++
		}
	}
	for _, k := range sorted(count) {
		repo, event, _ := strings.Cut(k, " ")
		p.Contributions = append(p.Contributions, client.RewardContribution{Repo: repo, Event: event, Count: count[k]})
	}
	return p, nil
}

// used totals each person's lifetime tokens and requests on the model families
// asked, from the usage rollup, keyed by subject. A model id is of a family when it
// is exactly the family or the family followed by a dash or a digit, and never when
// it names a vendor's path (provider/model): that is the model served, not ours.
func (s *state) used(ctx context.Context, people []client.RewardPerson, families []string) (map[string][]client.RewardUsage, error) {
	if !readyFn() {
		return nil, errors.New("the warehouse is not connected")
	}
	subjects := make([]string, len(people))
	for i, p := range people {
		subjects[i] = p.Subject
	}
	slugs, err := s.slugs(ctx, subjects)
	if err != nil {
		return nil, err
	}
	if len(slugs) == 0 {
		return nil, nil
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(slugs)), ",")
	pattern := "^(" + strings.Join(families, "|") + ")"
	args := []any{pattern, pattern + `([0-9-]|$)`}
	for slug := range slugs {
		args = append(args, slug)
	}
	rows, err := queryFn(ctx, `SELECT user_id, extract(lower(model), ?) AS family, sum(requests) AS requests, sum(total_tokens) AS tokens
		FROM `+rollup+` WHERE position(model, '/') = 0 AND match(lower(model), ?) AND user_id IN (`+marks+`)
		GROUP BY user_id, family`, args...)
	if err != nil {
		return nil, fmt.Errorf("usage rollup: %w", err)
	}
	out := map[string][]client.RewardUsage{}
	for _, r := range rows {
		sub, ok := slugs[txt(r["user_id"])]
		if !ok {
			continue
		}
		out[sub] = append(out[sub], client.RewardUsage{Family: txt(r["family"]), Requests: num(r["requests"]), Tokens: num(r["tokens"])})
	}
	return out, nil
}
