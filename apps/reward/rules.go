package reward

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/hanzoai/reward/catalog"
	"github.com/hanzoai/reward/rule"

	"github.com/hanzoai/cloud/sdk"
)

// publicRule is one active rule as anyone may read it: what earns, how much, and
// under what limit. It is the whole of what the program promises, so a page that
// lists rewards lists these and nothing it wrote itself.
type publicRule struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Pillar is the part of the program the rule belongs to.
	Pillar string `json:"pillar"`
	// Event says in plain words what has to happen for the rule to apply.
	Event string `json:"event"`
	// Points is what the rule adds to lifetime points when staff approve it.
	Points int64 `json:"points"`
	// Badge is the badge the rule awards, when it awards one.
	Badge *publicBadge `json:"badge,omitempty"`
	// CreditCents is the credit the rule grants, in cents. Present only when the
	// rule has a credit action.
	CreditCents int64 `json:"creditCents,omitempty"`
	// Limits says in plain words how often one person can earn it.
	Limits string `json:"limits"`
	// Review is whether staff verify each claim of the rule before it awards
	// anything.
	Review bool `json:"review"`
}

// publicBadge is a badge a rule awards.
type publicBadge struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Rarity string `json:"rarity"`
}

// pillarRules is the active rules of one pillar.
type pillarRules struct {
	ID    string       `json:"id"`
	Title string       `json:"title"`
	Rules []publicRule `json:"rules"`
}

// rulesOut is the active rules, grouped by pillar in the order the program draws them.
type rulesOut struct {
	Pillars []pillarRules `json:"pillars"`
}

// limitsText says a rule's limits in plain words.
func limitsText(l rule.Limits) string {
	var parts []string
	switch l.Once {
	case rule.Subject:
		parts = append(parts, "once per person")
	case rule.Keyed:
		parts = append(parts, "once per "+strings.ReplaceAll(l.KeyField, "+", " and "))
	}
	if l.PerDay > 0 {
		parts = append(parts, fmt.Sprintf("at most %d a day", l.PerDay))
	}
	if l.PerMonth > 0 {
		parts = append(parts, fmt.Sprintf("at most %d a month", l.PerMonth))
	}
	if len(parts) == 0 {
		return "no limit"
	}
	return strings.Join(parts, ", ")
}

// publicRules lists the active rules of the active programs, by pillar. An inactive
// program or rule is not on it, and nothing here reads a budget, a template or a
// condition: those are the staff's.
func publicRules(programs []*rule.Program) *rulesOut {
	out := &rulesOut{Pillars: []pillarRules{}}
	for _, info := range catalog.Pillars() {
		group := pillarRules{ID: string(info.ID), Title: info.Title, Rules: []publicRule{}}
		for _, p := range programs {
			if !p.Active {
				continue
			}
			for _, r := range p.Rules {
				if !r.Active || r.Pillar != info.ID {
					continue
				}
				v := publicRule{ID: r.ID, Name: r.Name, Pillar: string(r.Pillar), Limits: limitsText(r.Limits), Review: r.Approval != rule.Auto}
				v.Event = r.When.Event
				if ev, ok := catalog.Type(r.When.Event); ok {
					v.Event = ev.Title
				}
				for _, a := range r.Then {
					switch a.Type {
					case rule.Do.Points:
						v.Points += a.Points
					case rule.Do.Credit:
						v.CreditCents += a.Cents
					case rule.Do.Badge:
						if v.Badge != nil {
							continue
						}
						v.Badge = &publicBadge{ID: a.Badge, Name: a.Badge, Rarity: string(rule.Common)}
						if i := slices.IndexFunc(p.Badges, func(b rule.Badge) bool { return b.ID == a.Badge }); i >= 0 {
							v.Badge.Name = p.Badges[i].Name
							if p.Badges[i].Rarity != "" {
								v.Badge.Rarity = string(p.Badges[i].Rarity)
							}
						}
					}
				}
				group.Rules = append(group.Rules, v)
			}
		}
		out.Pillars = append(out.Pillars, group)
	}
	return out
}

// Lists what the rewards program pays for: every active rule of every active
// program, grouped by pillar (use, share, community, contribute, expert), each with
// what has to happen, the points, the badge or credit it awards, how often one person
// can earn it and whether staff verify each claim first. Public: it needs no
// credential, and it is the program's own list, so nothing else promises a reward.
func (s *state) rules(ctx context.Context, _ *cloud.Unit) (*rulesOut, error) {
	programs, err := s.eng.Programs(ctx)
	if err != nil {
		return nil, refusal(err)
	}
	if c, ok := cloud.Request(ctx); ok {
		c.SetHeader("Cache-Control", "public, max-age=60")
	}
	return publicRules(programs), nil
}
