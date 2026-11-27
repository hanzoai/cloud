// invite.go is the contract between the referral claim and the beta invites
// marketing keeps: whose code an invite's link carries, and which invites a
// signup answered. Both travel the internal plane, peer to peer.

package client

// ReferralOrg names the org whose referral code is asked for.
type ReferralOrg struct {
	// Org is the org the code belongs to.
	Org string `json:"org"`
}

// ReferralLink is one org's referral code and the share link that carries it.
type ReferralLink struct {
	// Code is the org's referral code, as its ?ref= link spells it.
	Code string `json:"code"`
	// Link is the brand's share link carrying the code.
	Link string `json:"link"`
}

// InviteJoin is a referee who claimed a member's code: which org's code, who
// claimed it, the invite their link carried, and their verified address.
type InviteJoin struct {
	// Referrer is the org whose code was claimed: an invite is marked only when
	// it was sent from that org.
	Referrer string `json:"referrer"`
	// Referee is the org that signed up.
	Referee string `json:"referee"`
	// Invite is the invite id the signup's link carried (utm_content); empty
	// when it carried none.
	Invite string `json:"invite"`
	// Email is the referee's verified address; empty when the claim had none.
	Email string `json:"email"`
}

// InviteJoined is how many invites a claim marked joined.
type InviteJoined struct {
	// Joined counts the invites marked; 0 when none matched.
	Joined int `json:"joined"`
}
