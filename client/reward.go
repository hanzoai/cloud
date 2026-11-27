package client

import (
	"encoding/json"
	"fmt"
	"sort"
)

// RewardGitHub tells the rewards capability one GitHub delivery, exactly as GitHub
// signed it. The provider app verifies the signature and hands the body here; reward
// decides whether it earns, maps the GitHub account to the IAM person who linked it,
// and states the event to the rules engine. A delivery that earns nothing is
// answered, never failed.
const RewardGitHub = "reward_github"

// GitHubDelivery is one verified webhook delivery.
type GitHubDelivery struct {
	// Event is GitHub's X-GitHub-Event: pull_request, issues, star, watch, fork.
	Event string `json:"event"`
	// Delivery is GitHub's X-GitHub-Delivery id, kept for the log.
	Delivery string `json:"delivery"`
	// Body is the delivery's JSON body, verbatim.
	Body json.RawMessage `json:"body"`
}

// GitHubTold is what reward made of a delivery.
type GitHubTold struct {
	// Events is how many events it stated to the rules engine.
	Events int `json:"events"`
	// Reason is why it stated none, when it stated none.
	Reason string `json:"reason,omitempty"`
}

// IAMGitHubPeople resolves GitHub accounts, by their numeric user id, to the IAM
// person who linked each at sign-in. IAM writes that id on the user row, and it
// is the id a webhook carries, so the value that decides who is credited is the
// one GitHub signed and never one a person typed. The platform alone asks: it
// reads across every tenant.
const IAMGitHubPeople = "iam_github_people"

// GitHubPeopleIn names the GitHub user ids to resolve, at most 500.
type GitHubPeopleIn struct {
	IDs []string `json:"ids"`
}

// GitHubPerson is one resolved account.
type GitHubPerson struct {
	// ID is the GitHub user id asked about.
	ID string `json:"id"`
	// Subject is the IAM user id (the token's sub) of the person who linked it.
	Subject string `json:"subject"`
	// Org is the org that owns the user row.
	Org string `json:"org"`
}

// GitHubPeople holds one person for each id that resolved; an id nobody linked is
// absent.
type GitHubPeople struct {
	People []GitHubPerson `json:"people"`
}

// ReferralEdges lists the referrals of the deployment, referrer org to referee
// org, with where each stands. The platform alone asks: it reads across tenants.
const ReferralEdges = "referral_edges"

// ReferralEdgesIn pages the edges, oldest first.
type ReferralEdgesIn struct {
	// Offset is how many edges to skip.
	Offset int `json:"offset"`
	// Limit is how many to return, at most 1000.
	Limit int `json:"limit"`
}

// ReferralEdge is one referrer-to-referee edge.
type ReferralEdge struct {
	Referrer string `json:"referrer"`
	Referee  string `json:"referee"`
	// Status is signup or qualified.
	Status      string `json:"status"`
	CreatedAt   int64  `json:"createdAt"`
	QualifiedAt int64  `json:"qualifiedAt"`
}

// ReferralEdgeList is a page of edges.
type ReferralEdgeList struct {
	Edges []ReferralEdge `json:"edges"`
}

// LeaderboardBountyTotals answers the points of the bounty and bug-report awards
// each org holds, the term of the one score (Points) that the bounty board keeps.
// The platform alone asks: it reads across tenants.
const LeaderboardBountyTotals = "leaderboard_bounty_totals"

// BountyTotals is every org's bounty points; an org with none is absent.
type BountyTotals struct {
	// Orgs is one entry per org with points, sorted by org.
	Orgs []OrgTotal `json:"orgs"`
}

// OrgTotal is one org's bounty points.
type OrgTotal struct {
	Org    string `json:"org"`
	Points int64  `json:"points"`
}

// OrgTotals renders totals as a sorted slice, for the plane.
func OrgTotals(m map[string]int) []OrgTotal {
	out := make([]OrgTotal, 0, len(m))
	for k, v := range m {
		out = append(out, OrgTotal{Org: k, Points: int64(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Org < out[j].Org })
	return out
}

// Map is the totals keyed by org.
func (b *BountyTotals) Map() map[string]int {
	m := make(map[string]int, len(b.Orgs))
	for _, o := range b.Orgs {
		m[o.Org] = int(o.Points)
	}
	return m
}

// RewardEvent states one event to the rewards program on behalf of a process that
// is not the rewards capability: the provider app when a member links Discord, the
// referral app when an attributed sign-up happens. The program's rules engine takes
// the event as it takes any other, refusing one the catalog does not list exactly,
// and is idempotent by the event's id. The platform alone states events.
const RewardEvent = "reward_event"

// RewardEventIn is one event, as the rules engine's catalog lists it.
type RewardEventIn struct {
	// ID is the caller's own immutable id for the happening: the same id again is
	// the same event and awards nothing twice.
	ID string `json:"id"`
	// Type is the event's name in the catalog, such as discord.joined.
	Type string `json:"type"`
	// Subject is the IAM person the event is about: their token's sub.
	Subject string `json:"subject"`
	// Org is the org a credit action would land in, when a rule grants credit.
	Org string `json:"org,omitempty"`
	// Fields are the values the catalog lists for Type, exactly: one entry per
	// field, sorted by key.
	Fields []RewardField `json:"fields"`
	// Evidence is where an event on another site took place and what was checked
	// there, for an event whose type asks for it.
	Evidence json.RawMessage `json:"evidence,omitempty"`
}

// RewardField is one field of an event. Value is the field's JSON, so a number,
// a flag or a list of texts keeps its kind across the plane.
type RewardField struct {
	Key   string          `json:"key"`
	Value json.RawMessage `json:"value"`
}

// RewardFields renders an event's fields as a sorted slice, for the plane.
func RewardFields(m map[string]any) ([]RewardField, error) {
	out := make([]RewardField, 0, len(m))
	for k, v := range m {
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("field %q: %w", k, err)
		}
		out = append(out, RewardField{Key: k, Value: b})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// FieldMap is the fields keyed by name, each decoded from its JSON.
func (in *RewardEventIn) FieldMap() (map[string]any, error) {
	m := make(map[string]any, len(in.Fields))
	for _, f := range in.Fields {
		var v any
		if err := json.Unmarshal(f.Value, &v); err != nil {
			return nil, fmt.Errorf("field %q: %w", f.Key, err)
		}
		m[f.Key] = v
	}
	return m, nil
}

// RewardEventOut is what the rules engine made of an event.
type RewardEventOut struct {
	// Claims is how many claims the event made for staff.
	Claims int `json:"claims"`
}

// RewardLevels answers the level table of the rewards program: each level's number,
// the band it belongs to and the lifetime points it begins at, lowest first, as staff
// have it set. The public galaxy (apps/leaderboard) names a star's band from it. The
// platform alone asks.
const RewardLevels = "reward_levels"

// RewardLevelTable is the program's levels in order, the first beginning at 0 points.
type RewardLevelTable struct {
	Levels []RewardLevel `json:"levels"`
}

// RewardLevel is one level of the table.
type RewardLevel struct {
	// Level is the level's number.
	Level int `json:"level"`
	// Band is the named run of levels it belongs to; the level's own name where the
	// table gives it no band.
	Band string `json:"band"`
	// From is the lifetime points the level begins at.
	From int64 `json:"from"`
}

// RewardPeople answers, for IAM subjects whose reward profile is public, what that
// profile shows and the records an expert's score is made of: the badges held, the
// merged work and the usage by model family. A subject whose profile is private,
// was never made or is unknown is absent from the answer, and nothing says which.
// The platform alone asks.
const RewardPeople = "reward_people"

// RewardPeopleIn names the subjects asked about, at most 200, and the model
// families whose usage is totalled for each: lowercase words such as kai, enso and
// zen, at most 8. A family is a model id that is exactly it, or it followed by a
// dash or a digit.
type RewardPeopleIn struct {
	Subjects []string `json:"subjects"`
	Families []string `json:"families"`
}

// RewardPeopleOut is one person for each subject whose profile is public.
type RewardPeopleOut struct {
	People []RewardPerson `json:"people"`
}

// RewardPerson is a public reward profile and the records beside it.
type RewardPerson struct {
	// Subject is the subject asked about, so the caller can match the answer.
	Subject string `json:"subject"`
	// Level and LevelName are the level the profile shows; Points the lifetime
	// points.
	Level     int    `json:"level"`
	LevelName string `json:"levelName"`
	Points    int64  `json:"points"`
	// Badges are the three the profile shows first: the rarest, then the earliest.
	Badges []RewardBadge `json:"badges"`
	// Held is the id of every badge the subject holds.
	Held []string `json:"held"`
	// Pillars counts the claims awarded under each pillar, sorted by pillar.
	Pillars []PillarCount `json:"pillars"`
	// Contributions counts the merged pull requests and completed bounties awarded,
	// by repository and by event.
	Contributions []RewardContribution `json:"contributions"`
	// Usage totals the subject's lifetime tokens and requests for each family asked.
	// A family the subject never used is absent; the whole list is empty when the
	// warehouse could not be read.
	Usage []RewardUsage `json:"usage"`
}

// PillarCount is the claims awarded under one pillar.
type PillarCount struct {
	Pillar string `json:"pillar"`
	Count  int64  `json:"count"`
}

// PillarCounts renders pillar counts as a sorted slice, for the plane.
func PillarCounts(m map[string]int) []PillarCount {
	out := make([]PillarCount, 0, len(m))
	for k, v := range m {
		out = append(out, PillarCount{Pillar: k, Count: int64(v)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Pillar < out[j].Pillar })
	return out
}

// Count is the claims awarded under pillar, 0 when it is absent.
func (p *RewardPerson) Count(pillar string) int {
	for _, c := range p.Pillars {
		if c.Pillar == pillar {
			return int(c.Count)
		}
	}
	return 0
}

// RewardBadge is one badge as the public profile shows it.
type RewardBadge struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Pillar string `json:"pillar"`
	Rarity string `json:"rarity"`
}

// RewardContribution is the awarded claims for one event in one repository.
type RewardContribution struct {
	// Repo is owner/name, lowercase.
	Repo string `json:"repo"`
	// Event is github.pr.merged or bounty.completed.
	Event string `json:"event"`
	Count int    `json:"count"`
}

// RewardUsage is a subject's lifetime use of one model family.
type RewardUsage struct {
	Family   string `json:"family"`
	Requests int64  `json:"requests"`
	Tokens   int64  `json:"tokens"`
}
