package client

// LeaderboardBetaPlace answers one beta record's place on the waitlist. The
// leaderboard ranks the waitlist (apps/leaderboard beta.go) and marketing's
// GET /v1/marketing/beta reads the caller's place from it, so the place a
// person reads and the rank the board shows are one computation.
const LeaderboardBetaPlace = "leaderboard_beta_place"

// BetaPlaceIn names the beta record whose place is asked: the IAM subject that
// holds it. The waitlist is the calling brand's; the brand rides the call.
type BetaPlaceIn struct {
	// Subject is the IAM subject the record is keyed by.
	Subject string `json:"subject"`
}

// BetaPlace is a beta record's standing on the waitlist.
type BetaPlace struct {
	// Place is the record's place, 1 first; 0 when the subject holds no record.
	Place int `json:"place"`
	// Of is how many people are on the waitlist, as the board counts them.
	Of int `json:"of"`
	// Score is what the record is ranked by (apps/leaderboard beta.go `score`).
	Score int `json:"score"`
	// Tokens is whether token usage was counted: false when the warehouse did not
	// answer, and the score is then without it for every record alike.
	Tokens bool `json:"tokens"`
}

// ReferralTallies counts the referrals of each org named: every org it
// referred, and those of them that qualified by making metered spend.
const ReferralTallies = "referral_tallies"

// ReferralTallyIn names the referrer orgs to count.
type ReferralTallyIn struct {
	// Orgs are the referrer orgs, at most 1000.
	Orgs []string `json:"orgs"`
}

// ReferralTally is the referrals of each org asked about that has any.
type ReferralTally struct {
	// Orgs holds one entry per org with at least one referral.
	Orgs []ReferralOrgTally `json:"orgs"`
}

// ReferralOrgTally is one org's referrals.
type ReferralOrgTally struct {
	// Org is the referrer org.
	Org string `json:"org"`
	// Referrals is every org that signed up with its code.
	Referrals int `json:"referrals"`
	// Qualified is how many of them have made metered spend.
	Qualified int `json:"qualified"`
}

// ReferralSeries lists when each org that signed up with a referral code did, for
// the public pulse (apps/leaderboard pulse.go), which draws how fast the platform
// spreads. It answers moments and nothing else: no referee, no referrer, no code.
const ReferralSeries = "referral_series"

// ReferralSeriesIn names how far back to list.
type ReferralSeriesIn struct {
	// Since is the earliest moment wanted, in Unix seconds.
	Since int64 `json:"since"`
}

// ReferralSignups is the moments referred orgs signed up, oldest first.
type ReferralSignups struct {
	// At holds one Unix time, in seconds, per referred org that signed up since the
	// moment asked for. At most 100,000.
	At []int64 `json:"at"`
}
