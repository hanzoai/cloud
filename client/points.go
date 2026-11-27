package client

// The ONE score. Every board that ranks people or orgs by points reads it from
// here: the public standing, the pulse, the bounty board and the beta waitlist, so
// a number means the same on all of them. A board that spelled its own sum would be
// a second rule, and two rules for one score drift apart.

const (
	// TokensPerPoint is how many tokens ever used make one point.
	TokensPerPoint = 100_000
	// ReferralPoints is what each org that signed up with the member's referral
	// code adds.
	ReferralPoints = 1
	// QualifiedPoints is what each such org that has made metered spend adds,
	// beside its ReferralPoints.
	QualifiedPoints = 20
	// StarPoints is what one verified GitHub star adds: once per person per repo.
	StarPoints = 1
	// FollowPoints is what one follow of a founder's link adds: once per link.
	FollowPoints = 1
)

// Sources are everything the score is made of, counted all-time from what cloud
// records. A term a source has not recorded is zero, never estimated.
type Sources struct {
	// Tokens ever used, prompt and completion, from the usage ledger.
	Tokens int64
	// Referrals is the orgs that signed up with the referral code.
	Referrals int
	// Qualified is how many of those have made metered spend.
	Qualified int
	// Stars is the verified GitHub stars, one per person per repository.
	Stars int
	// Follows is the founder links followed, one per person per link.
	Follows int
	// Bounty is the points of the verified bounty and bug-report awards.
	Bounty int
}

// Points is the score of s: the one formula.
func Points(s Sources) int {
	return int(s.Tokens/TokensPerPoint) +
		s.Referrals*ReferralPoints + s.Qualified*QualifiedPoints +
		s.Stars*StarPoints + s.Follows*FollowPoints +
		s.Bounty
}
