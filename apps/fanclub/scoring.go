// SPDX-License-Identifier: Apache-2.0 OR MIT

package fanclub

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// Profile describes one fictional industry seed profile for opt-in synthetic
// workspace fixtures. Profiles are strictly demonstration templates — never real
// customers or tenant boundaries.
type Profile struct {
	// ID is the profile slug ("northstar", "solstice", "harbor").
	ID string `json:"id"`
	// Name is the fictional organization display name.
	Name string `json:"name"`
	// Industry describes the sector the profile models.
	Industry string `json:"industry"`
	// Noun is the domain word for people ("supporters", "patrons", "members").
	Noun string `json:"noun"`
	// Color is the UI accent color for this profile.
	Color string `json:"color"`
	// Plan is the illustrative workspace label.
	Plan string `json:"plan"`
}

// Journey describes a reusable decision workflow that produces human-reviewable
// simulation proposals.
type Journey struct {
	// ID is the journey slug ("retention", "return", "growth").
	ID string `json:"id"`
	// Title is the human-readable workflow name.
	Title string `json:"title"`
	// Subtitle describes the evidence condition this journey evaluates.
	Subtitle string `json:"subtitle"`
	// Metric identifies the primary KPI family.
	Metric string `json:"metric"`
	// Icon names the UI glyph category.
	Icon string `json:"icon"`
}

// RiskScore is the output of the transparent deterministic rules baseline. It
// is NOT a trained ML model and makes no commercial uplift claim.
type RiskScore struct {
	// Risk is a bounded deterministic score in [4, 98].
	Risk int `json:"risk"`
	// Priority buckets Risk into "High", "Medium", or "Low".
	Priority string `json:"priority"`
	// NextBest recommends the next safe human-review step.
	NextBest string `json:"nextBest"`
	// Reason lists the explicit rule conditions that fired.
	Reason []string `json:"reason"`
	// Baseline identifies the deterministic rule set version.
	Baseline string `json:"baseline"`
}

// MemberDoc is the JSON document persisted in Cloud Base collection fc_members.
type MemberDoc struct {
	// MemberID is the stable domain identifier (e.g. "northstar-001").
	MemberID string `json:"member_id"`
	// Name is the synthetic person display name.
	Name string `json:"name"`
	// Tier is the membership tier ("Core", "Plus", "Premium").
	Tier string `json:"tier"`
	// Segment is the lifecycle segment ("New", "Established", "Advocate").
	Segment string `json:"segment"`
	// Status is the membership status ("Active", "Trial", "Lapsed").
	Status string `json:"status"`
	// Consent records whether marketing outreach is permitted.
	Consent bool `json:"consent"`
	// Visits90d is recent participation count over 90 days.
	Visits90d int `json:"visits_90d"`
	// Engagement is an integer participation index in [0, 100].
	Engagement int `json:"engagement"`
	// DaysToRenewal is days until the next renewal boundary.
	DaysToRenewal int `json:"days_to_renewal"`
	// LifetimeValue is illustrative demo credits, never booked revenue.
	LifetimeValue int `json:"lifetime_value"`
	// ObservedLapse is the synthetic holdout ground-truth flag (0 or 1).
	ObservedLapse int `json:"observed_lapse"`
	// Profile names which fictional seed profile generated this fixture.
	Profile string `json:"profile,omitempty"`
	// Synthetic explicitly flags fictional demonstration records.
	Synthetic bool `json:"synthetic"`
	// CreatedAt is the RFC3339 timestamp when the fixture record was dated.
	CreatedAt string `json:"created_at"`
}

// MemberView is one member record enriched with its Base record ID and rules score.
type MemberView struct {
	// RecordID is the authoritative Cloud Base record ID in fc_members.
	RecordID string `json:"recordId"`
	MemberDoc
	// Score is the deterministic rules baseline score.
	Score RiskScore `json:"score"`
}

// ProposalDoc is the JSON document persisted in Cloud Base collection fc_proposals.
type ProposalDoc struct {
	// MemberID references MemberDoc.MemberID in fc_members.
	MemberID string `json:"member_id"`
	// MemberName is captured for display; joined live when reading.
	MemberName string `json:"member_name,omitempty"`
	// Journey is the journey slug ("retention", "return", "growth").
	Journey string `json:"journey"`
	// Action describes the proposed human-reviewed intervention.
	Action string `json:"action"`
	// Reason summarizes the rule evidence behind the proposal.
	Reason string `json:"reason"`
	// Status is "Pending", "Approved (simulation)", or "Rejected".
	Status string `json:"status"`
	// Mode is always "simulation-only" (no external side effects).
	Mode string `json:"mode"`
	// ExternalSideEffects is always false.
	ExternalSideEffects bool `json:"external_side_effects"`
	// CreatedBy is the validated IAM actor who generated the proposal.
	CreatedBy string `json:"created_by"`
	// CreatedAt is the RFC3339 creation timestamp.
	CreatedAt string `json:"created_at"`
	// DecidedBy is the validated IAM admin who approved or rejected the proposal.
	DecidedBy string `json:"decided_by,omitempty"`
	// DecidedAt is the RFC3339 decision timestamp.
	DecidedAt string `json:"decided_at,omitempty"`
	// TaskID is the Cloud Tasks durable simulation task ID minted on approval.
	TaskID string `json:"task_id,omitempty"`
}

// ProposalView is one proposal record enriched with its Base record ID and live consent.
type ProposalView struct {
	// ID is the authoritative Cloud Base record ID in fc_proposals.
	ID string `json:"id"`
	ProposalDoc
	// Consent reflects the member's current consent state in fc_members.
	Consent bool `json:"consent"`
}

// AuditDoc is the JSON document persisted in Cloud Base collection fc_audit.
type AuditDoc struct {
	// Actor is the IAM-validated actor identifier.
	Actor string `json:"actor"`
	// Event is the dot-delimited audit event name.
	Event string `json:"event"`
	// Subject optionally identifies the proposal, journey, or collection affected.
	Subject string `json:"subject,omitempty"`
	// Details holds structured non-secret provenance metadata.
	Details map[string]any `json:"details"`
	// CreatedAt is the RFC3339 timestamp when the receipt was recorded.
	CreatedAt string `json:"created_at"`
}

// AuditView is one audit entry enriched with its Cloud Base record ID.
type AuditView struct {
	// ID is the authoritative Cloud Base record ID in fc_audit.
	ID string `json:"id"`
	AuditDoc
}

// DayActivity is one day of illustrative weekly participation counts.
type DayActivity struct {
	// Day is the 3-letter weekday label.
	Day string `json:"day"`
	// Value is the illustrative event count.
	Value int `json:"value"`
}

// Metrics summarizes aggregate workspace statistics (safe to ground AI answers on; no PII).
type Metrics struct {
	// Members is the total number of records in fc_members.
	Members int `json:"members"`
	// AtRisk is the count of members with rules risk >= 70.
	AtRisk int `json:"atRisk"`
	// Consented is the count of members with outreach consent enabled.
	Consented int `json:"consented"`
	// ValueAtRisk sums illustrative demo credits across the elevated-risk cohort.
	ValueAtRisk int `json:"valueAtRisk"`
	// Engagement is the mean engagement score across all members (0 when empty).
	Engagement int `json:"engagement"`
	// Pending is the count of proposals awaiting human approval.
	Pending int `json:"pending"`
	// Approved is the count of simulation-approved proposals.
	Approved int `json:"approved"`
	// WeeklyActivity holds 7 illustrative weekday buckets.
	WeeklyActivity []DayActivity `json:"weeklyActivity"`
}

// ConfusionMatrix records holdout classification counts for the rules baseline.
type ConfusionMatrix struct {
	// TP is true positives.
	TP int `json:"tp"`
	// FP is false positives.
	FP int `json:"fp"`
	// FN is false negatives.
	FN int `json:"fn"`
	// TN is true negatives.
	TN int `json:"tn"`
}

// Evaluation reports actually computed holdout metrics for the rules baseline.
type Evaluation struct {
	// Label names the evaluated method ("Deterministic rules baseline").
	Label string `json:"label"`
	// Data describes the evaluated slice.
	Data string `json:"data"`
	// Size is the number of evaluated rows.
	Size int `json:"size"`
	// Precision is TP / (TP + FP) as an integer percentage.
	Precision int `json:"precision"`
	// Recall is TP / (TP + FN) as an integer percentage.
	Recall int `json:"recall"`
	// Accuracy is (TP + TN) / Size as an integer percentage.
	Accuracy int `json:"accuracy"`
	// Confusion holds the raw TP/FP/FN/TN counts.
	Confusion ConfusionMatrix `json:"confusion"`
	// Caution explicitly discloses that these are synthetic baseline metrics, not trained AI.
	Caution string `json:"caution"`
}

var syntheticProfiles = []Profile{
	{ID: "northstar", Name: "Northstar United", Industry: "Sports & community", Noun: "supporters", Color: "#c5f579", Plan: "Club Intelligence"},
	{ID: "solstice", Name: "Solstice Museum", Industry: "Arts & culture", Noun: "patrons", Color: "#acb9ff", Plan: "Patron Intelligence"},
	{ID: "harbor", Name: "Harbor Alliance", Industry: "Membership association", Noun: "members", Color: "#f7c69b", Plan: "Association Intelligence"},
}

var defaultJourneys = []Journey{
	{ID: "retention", Title: "Renewal Radar", Subtitle: "Identify upcoming renewals with elevated lapse risk", Metric: "renewal", Icon: "shield"},
	{ID: "return", Title: "Second Visit", Subtitle: "Engage first-time attendees with consent-based follow-ups", Metric: "return", Icon: "repeat"},
	{ID: "growth", Title: "Membership Growth", Subtitle: "Identify appropriate upgrade or participation opportunities", Metric: "growth", Icon: "trending-up"},
}

var (
	firstNames = []string{"Avery", "Jordan", "Morgan", "Reese", "Riley", "Taylor", "Sam", "Jamie", "Alex", "Casey", "Charlie", "Drew", "Hayden", "Quinn", "Robin", "Sage", "Kai", "Dakota", "Blair", "Skyler", "Emery", "Rowan", "Parker", "Finley"}
	lastNames  = []string{"Carter", "Rivera", "Chen", "Patel", "Stewart", "Williams", "Lopez", "Kim", "Shaw", "Martin", "Davis", "Sato", "Clark", "Murphy", "Ali", "Stone", "Miller", "Singh", "Brown", "Nguyen"}
)

func findProfile(id string) (Profile, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return syntheticProfiles[0], true
	}
	for _, p := range syntheticProfiles {
		if p.ID == id {
			return p, true
		}
	}
	return Profile{}, false
}

func findJourney(id string) (Journey, bool) {
	for _, j := range defaultJourneys {
		if j.ID == id {
			return j, true
		}
	}
	return Journey{}, false
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// ScoreMember evaluates the transparent deterministic rules baseline for one
// member record. It is purely rule-based and deterministic, not a trained model.
func ScoreMember(m MemberDoc) RiskScore {
	score := 30
	if m.DaysToRenewal < 65 {
		score += 20
	}
	switch {
	case m.Visits90d == 0:
		score += 24
	case m.Visits90d < 5:
		score += 15
	default:
		score -= 8
	}
	switch {
	case m.Engagement < 50:
		score += 22
	case m.Engagement > 85:
		score -= 18
	}
	if m.Status == "Lapsed" {
		score += 24
	}
	risk := clampInt(score, 4, 98)

	priority := "Low"
	if risk >= 70 {
		priority = "High"
	} else if risk >= 43 {
		priority = "Medium"
	}

	nextBest := "Monitor engagement"
	switch {
	case !m.Consent:
		nextBest = "No outreach permitted"
	case risk >= 70:
		nextBest = "Human review: renewal follow-up"
	case m.Visits90d <= 1:
		nextBest = "Offer a second experience"
	case m.Engagement > 72:
		nextBest = "Consider membership upgrade"
	}

	r1 := "Recent participation"
	if m.Visits90d <= 1 {
		r1 = "Low recent participation"
	}
	r2 := "Renewal not imminent"
	if m.DaysToRenewal < 45 {
		r2 = "Renewal approaching"
	}
	r3 := "Engagement observed"
	if m.Engagement < 35 {
		r3 = "Low engagement"
	}

	return RiskScore{
		Risk:     risk,
		Priority: priority,
		NextBest: nextBest,
		Reason:   []string{r1, r2, r3},
		Baseline: "deterministic-rules-v1",
	}
}

// GenerateSyntheticMembers builds 90 reproducible fictional member records for
// opt-in demo seeding. Every record carries Synthetic: true.
func GenerateSyntheticMembers(profile Profile, now time.Time) []MemberDoc {
	out := make([]MemberDoc, 0, 90)
	plen := len(profile.ID)
	for i := 0; i < 90; i++ {
		seed := (i*19 + plen*23) % 101
		visits := (i*5 + plen*3) % 13
		engagement := clampInt(18+((i*31+plen*11)%77), 0, 100)
		days := 8 + ((i*11 + plen) % 158)
		consent := i%9 != 0
		status := "Active"
		if i%17 == 0 {
			status = "Lapsed"
		} else if i%11 == 0 {
			status = "Trial"
		}
		tier := "Core"
		if i%6 == 0 {
			tier = "Premium"
		} else if i%4 == 0 {
			tier = "Plus"
		}
		segment := "Established"
		if i%5 == 0 {
			segment = "New"
		} else if i%6 == 0 {
			segment = "Advocate"
		}
		doc := MemberDoc{
			MemberID:      fmt.Sprintf("%s-%03d", profile.ID, i+1),
			Name:          firstNames[i%len(firstNames)] + " " + lastNames[(i*3+plen)%len(lastNames)],
			Tier:          tier,
			Segment:       segment,
			Status:        status,
			Consent:       consent,
			Visits90d:     visits,
			Engagement:    engagement,
			DaysToRenewal: days,
			LifetimeValue: 80 + seed*25,
			Profile:       profile.ID,
			Synthetic:     true,
			CreatedAt:     now.UTC().Add(-time.Duration(i*5+2) * 24 * time.Hour).Format(time.RFC3339),
		}
		risk := ScoreMember(doc).Risk
		if (risk > 55 && i%5 != 0) || (risk <= 55 && i%23 == 0) {
			doc.ObservedLapse = 1
		}
		out = append(out, doc)
	}
	return out
}

func computeMetrics(org string, members []MemberView, proposals []ProposalView) Metrics {
	days := []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"}
	weekly := make([]DayActivity, len(days))
	for i, d := range days {
		weekly[i] = DayActivity{Day: d, Value: 9 + ((i*19 + len(org)*13) % 27)}
	}
	m := Metrics{
		Members:        len(members),
		WeeklyActivity: weekly,
	}
	engSum := 0
	for _, mem := range members {
		engSum += mem.Engagement
		if mem.Consent {
			m.Consented++
		}
		if mem.Score.Risk >= 70 {
			m.AtRisk++
			m.ValueAtRisk += mem.LifetimeValue
		}
	}
	if len(members) > 0 {
		m.Engagement = int(math.Round(float64(engSum) / float64(len(members))))
	}
	for _, p := range proposals {
		switch p.Status {
		case "Pending":
			m.Pending++
		case "Approved (simulation)":
			m.Approved++
		}
	}
	return m
}

func computeEvaluation(members []MemberView) Evaluation {
	ev := Evaluation{
		Label:   "Deterministic rules baseline",
		Data:    "Held-out rows (index modulo 3) from Cloud Base fc_members",
		Caution: "Deterministic rules baseline on workspace records — not a trained ML model, not commercial uplift proof, and not qualified train-to-serve.",
	}
	var tp, fp, fn, tn int
	for i, m := range members {
		if i%3 != 0 {
			continue
		}
		ev.Size++
		pred := m.Score.Risk >= 55
		obs := m.ObservedLapse != 0
		switch {
		case pred && obs:
			tp++
		case pred && !obs:
			fp++
		case !pred && obs:
			fn++
		default:
			tn++
		}
	}
	ev.Confusion = ConfusionMatrix{TP: tp, FP: fp, FN: fn, TN: tn}
	if tp+fp > 0 {
		ev.Precision = int(math.Round(100 * float64(tp) / float64(tp+fp)))
	}
	if tp+fn > 0 {
		ev.Recall = int(math.Round(100 * float64(tp) / float64(tp+fn)))
	}
	if ev.Size > 0 {
		ev.Accuracy = int(math.Round(100 * float64(tp+tn) / float64(ev.Size)))
	}
	return ev
}
