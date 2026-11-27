// bounty.go — THE BOUNTY & REWARDS BOARD: open bounties, bug-report awards,
// and the gamified contributor leaderboard.
//
//	GET  /v1/leaderboard/bounty            bounty hunters, recent bug awards, and caller standing
//	POST /v1/admin/leaderboard/bounty      record a bounty or bug-report award + send email (SuperAdmin)

package leaderboard

import (
	"cmp"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	notifypeer "github.com/hanzoai/cloud/client/notify"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// bountyQuery shapes one read of the bounty & rewards board.
type bountyQuery struct {
	// Limit caps the hunter and award rows returned, clamped to 1..100. Defaults to 25.
	Limit int `json:"limit"`
}

// BountyHunter is one ranked contributor on the bounty & rewards leaderboard.
type BountyHunter struct {
	// Rank is the contributor's 1-based standing on the bounty board.
	Rank int `json:"rank"`
	// Handle is the contributor's public display handle.
	Handle string `json:"handle"`
	// Org is the contributor's organization slug when listed.
	Org string `json:"org,omitempty"`
	// Self marks the signed-in caller's own row.
	Self bool `json:"self"`
	// BugsReported is how many verified bug reports this contributor has filed.
	BugsReported int `json:"bugsReported"`
	// BountiesMerged is how many bounty pull requests this contributor has landed.
	BountiesMerged int `json:"bountiesMerged"`
	// BountyPoints is the sum of points earned from bug reports and bounties.
	BountyPoints int `json:"bountyPoints"`
	// CreditsEarnedCents is the total AI credits in US cents granted for bounties and bug reports.
	CreditsEarnedCents int64 `json:"creditsEarnedCents"`
	// TokensUsed is the contributor's all-time token volume in the warehouse.
	TokensUsed int64 `json:"tokensUsed"`
	// GlobalTokenRank is the contributor's global organization rank by token volume (0 if unranked).
	GlobalTokenRank int `json:"globalTokenRank,omitempty"`
	// TotalScore is the org's points by client.Points, the one score every board shares.
	TotalScore int `json:"totalScore"`
	// Badge is the gamified tier title earned by the contributor.
	Badge string `json:"badge"`
}

// BountyAwardView is one verified bug report or merged bounty award in the recent feed.
type BountyAwardView struct {
	// ID is the unique award identifier.
	ID string `json:"id"`
	// Handle is the recipient's display name.
	Handle string `json:"handle"`
	// Kind is bug_report or bounty.
	Kind string `json:"kind"`
	// Target is the repository or issue reference (e.g. hanzo-inc/cloud#index-lock).
	Target string `json:"target"`
	// Summary describes what was reported or fixed.
	Summary string `json:"summary"`
	// Points is the leaderboard points awarded.
	Points int `json:"points"`
	// CreditsCents is the AI credit grant in US cents awarded with this bounty.
	CreditsCents int64 `json:"creditsCents"`
	// CreatedAt is the unix timestamp (seconds) when the award was recorded.
	CreatedAt int64 `json:"createdAt"`
}

// BountyBoard is the response from GET /v1/leaderboard/bounty.
type BountyBoard struct {
	// Hunters are the top-ranked bug hunters and bounty contributors, best first.
	Hunters []BountyHunter `json:"hunters"`
	// RecentAwards are the newest verified bug reports and bounty payouts.
	RecentAwards []BountyAwardView `json:"recentAwards"`
	// Self is the signed-in caller's own bounty standing when signed in.
	Self *BountyHunter `json:"self,omitempty"`
}

// BountyAwardIn is the SuperAdmin request body for POST /v1/admin/leaderboard/bounty.
type BountyAwardIn struct {
	// ID is an optional idempotency key for the award; generated when empty.
	ID string `json:"id"`
	// Org is the recipient's organization slug.
	Org string `json:"org"`
	// User is the recipient's IAM username within Org.
	User string `json:"user"`
	// Handle is the display name shown on the bounty board.
	Handle string `json:"handle"`
	// Email is the recipient's email address to send the celebratory reward + rank update notification to.
	Email string `json:"email"`
	// Kind is bug_report (default) or bounty.
	Kind string `json:"kind"`
	// Target names the repo, PR, or issue (e.g. hanzo-inc/cloud#123).
	Target string `json:"target"`
	// Summary is a one-line description of the bug report or fix.
	Summary string `json:"summary"`
	// Points overrides the default points for Kind when positive.
	Points int `json:"points"`
	// CreditsCents records the AI credits (in US cents) granted alongside this award.
	CreditsCents int64 `json:"creditsCents"`
}

// BountyAwardOut is the response from POST /v1/admin/leaderboard/bounty.
type BountyAwardOut struct {
	// Award is the recorded bounty award.
	Award BountyAwardView `json:"award"`
	// Hunter is the recipient's updated standing on the bounty board.
	Hunter BountyHunter `json:"hunter"`
	// EmailSent is true when a celebratory notification email was dispatched to Email.
	EmailSent bool `json:"emailSent"`
}

func badgeFor(bugs, bounties, score int, tokens int64) string {
	switch {
	case bugs >= 3 && tokens >= 1_000_000:
		return "Apex Bug Hunter & Power Builder"
	case bounties >= 3 || score >= 500:
		return "Grandmaster Contributor"
	case bugs >= 1 || bounties >= 1:
		return "Verified Bug Hunter"
	case tokens >= 1_000_000:
		return "Power Builder"
	default:
		return "Builder"
	}
}

// BountyBoard returns the ranked bug hunters and bounty contributors, and the recent bug and bounty awards.
func (o boardOps) bountyBoard(ctx context.Context, in *bountyQuery) (*BountyBoard, error) {
	org, err := tenantOf(ctx, "sign in to view the bounty board")
	if err != nil {
		return nil, err
	}
	noStore(ctx)
	limit := clampLimit(in.Limit, 25)
	return o.assembleBountyBoard(ctx, org, limit)
}

func (o boardOps) assembleBountyBoard(ctx context.Context, callerOrg string, limit int) (*BountyBoard, error) {
	awards, err := o.s.State.store.ListBountyAwards(ctx, 200)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "read bounty awards: %v", err)
	}

	byOrg := map[string]*BountyHunter{}
	var orgs []string
	recent := make([]BountyAwardView, 0, min(len(awards), limit))
	for i, a := range awards {
		if i < limit {
			recent = append(recent, BountyAwardView{
				ID:           a.ID,
				Handle:       cmp.Or(a.Handle, a.Org),
				Kind:         a.Kind,
				Target:       a.Target,
				Summary:      a.Summary,
				Points:       a.Points,
				CreditsCents: a.CreditsCents,
				CreatedAt:    a.CreatedAt,
			})
		}
		h, ok := byOrg[a.Org]
		if !ok {
			h = &BountyHunter{
				Handle: cmp.Or(a.Handle, a.Org),
				Org:    a.Org,
				Self:   a.Org == callerOrg,
			}
			byOrg[a.Org] = h
			orgs = append(orgs, a.Org)
		}
		if a.Kind == "bounty" {
			h.BountiesMerged++
		} else {
			h.BugsReported++
		}
		h.BountyPoints += a.Points
		h.CreditsEarnedCents += a.CreditsCents
	}

	// Every number below the awards comes from the one line the public standing ranks
	// (standing.go), so a hunter's score here is the score there. An org the line does
	// not hold, or a line that cannot be read, leaves tokens and global rank at zero
	// and the score at its awards alone: nothing is estimated.
	l, lerr := current(ctx, o.s.State.store)
	if lerr != nil {
		l = &line{}
		o.s.Log.Debug("bounty board: standing line unreadable", "err", lerr)
	}

	hunters := make([]BountyHunter, 0, len(byOrg))
	for _, org := range orgs {
		h := byOrg[org]
		h.TotalScore = client.Points(client.Sources{Bounty: h.BountyPoints})
		if e, ok := l.entry(org); ok {
			h.TotalScore, h.TokensUsed = e.points, e.src.Tokens
			h.GlobalTokenRank = l.tokenRank(e)
		}
		h.Badge = badgeFor(h.BugsReported, h.BountiesMerged, h.TotalScore, h.TokensUsed)
		hunters = append(hunters, *h)
	}
	slices.SortStableFunc(hunters, func(a, b BountyHunter) int {
		return cmp.Or(
			cmp.Compare(b.TotalScore, a.TotalScore),
			cmp.Compare(b.BountyPoints, a.BountyPoints),
			cmp.Compare(b.TokensUsed, a.TokensUsed),
			strings.Compare(a.Org, b.Org),
		)
	})

	var self *BountyHunter
	for i := range hunters {
		hunters[i].Rank = i + 1
		if hunters[i].Self {
			cp := hunters[i]
			self = &cp
		}
	}
	if len(hunters) > limit {
		hunters = hunters[:limit]
	}

	return &BountyBoard{
		Hunters:      hunters,
		RecentAwards: recent,
		Self:         self,
	}, nil
}

// AwardBounty records a verified bug report or bounty PR award on the leaderboard,
// opts the recipient's handle into public listing, and sends a celebratory email
// announcing their points, AI credits, and updated leaderboard rank. SuperAdmin only.
func (o boardOps) awardBounty(ctx context.Context, in *BountyAwardIn) (*BountyAwardOut, error) {
	c, ok := requestOf(ctx)
	if !ok || !principal.Super(c) {
		return nil, zip.ErrForbidden("SuperAdmin required")
	}
	org := strings.TrimSpace(in.Org)
	if org == "" {
		return nil, zip.ErrBadRequest("org is required")
	}
	kind := strings.ToLower(strings.TrimSpace(in.Kind))
	if kind == "" {
		kind = "bug_report"
	}
	if kind != "bug_report" && kind != "bounty" {
		return nil, zip.ErrBadRequest("kind must be bug_report or bounty")
	}
	target := strings.TrimSpace(in.Target)
	if target == "" {
		return nil, zip.ErrBadRequest("target is required")
	}
	summary := strings.TrimSpace(in.Summary)
	if summary == "" {
		return nil, zip.ErrBadRequest("summary is required")
	}
	pts := in.Points
	if pts <= 0 {
		if kind == "bounty" {
			pts = 100
		} else {
			pts = 50
		}
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		var b [8]byte
		_, _ = rand.Read(b[:])
		id = "bty_" + hex.EncodeToString(b[:])
	}
	handle := cmp.Or(strings.TrimSpace(in.Handle), strings.TrimSpace(in.User), org)
	userID := org + "/" + cmp.Or(strings.TrimSpace(in.User), org)
	now := nowFn().Unix()

	award := bountyAward{
		ID:           id,
		Org:          org,
		UserID:       userID,
		Handle:       handle,
		Email:        strings.TrimSpace(in.Email),
		Kind:         kind,
		Target:       target,
		Summary:      summary,
		Points:       pts,
		CreditsCents: in.CreditsCents,
		CreatedAt:    now,
	}
	if err := o.s.State.store.PutBountyAward(ctx, award); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "save bounty award: %v", err)
	}
	resetStanding() // the line is read again, with this award in it
	go func() {
		_ = bus.Announce(bus.Pulse{Kind: "bounty", Key: id, Org: org, Title: kind, Detail: strconv.Itoa(pts)})
	}()
	// Opt the org and user handle onto the public leaderboard so their climb is visible.
	_ = o.s.State.store.PutOrg(ctx, orgOptin{Org: org, Display: handle, Listed: true}, now)
	_ = o.s.State.store.PutUser(ctx, userOptin{UserID: userID, Org: org, Handle: handle, Listed: true}, now)

	board, err := o.assembleBountyBoard(ctx, org, 100)
	if err != nil {
		return nil, err
	}
	var hunter BountyHunter
	if board.Self != nil {
		hunter = *board.Self
	}

	emailSent := false
	if award.Email != "" {
		creditLine := ""
		if award.CreditsCents > 0 {
			creditLine = fmt.Sprintf("\n- AI Credits Granted: +$%.2f USD", float64(award.CreditsCents)/100.0)
		}
		rankLine := fmt.Sprintf("#%d on the Hanzo Bounty Board (Total Score: %d pts)", hunter.Rank, hunter.TotalScore)
		if hunter.GlobalTokenRank > 0 {
			rankLine += fmt.Sprintf(" · #%d Globally by Token Volume (%d tokens)", hunter.GlobalTokenRank, hunter.TokensUsed)
		}
		subj := fmt.Sprintf("You earned +%d Bounty Points on Hanzo! (Rank #%d)", award.Points, hunter.Rank)
		if award.CreditsCents > 0 {
			subj = fmt.Sprintf("You earned $%.2f in AI Credits + %d Bounty Points on Hanzo! (Rank #%d)", float64(award.CreditsCents)/100.0, award.Points, hunter.Rank)
		}
		body := fmt.Sprintf("Hi %s,\n\nThank you for helping make Hanzo better!\n\nYour contribution has been verified and rewarded:\n- Contribution: %s (%s)\n- Points Awarded: +%d Bounty Points%s\n- Badge Unlocked: %s\n- Current Standing: %s\n\nSee yourself climbing the ranks on the live boards:\n- https://hanzo.ai/leaderboard\n- https://hanzo.ai/bounty\n\n— Zach & The Hanzo Team",
			handle, award.Summary, award.Target, award.Points, creditLine, hunter.Badge, rankLine)
		sctx, cancel := context.WithTimeout(cloud.For(context.Background(), "hanzo"), 10*time.Second)
		_, serr := notifypeer.NotifySend(sctx, &client.Send{
			Org:     "hanzo",
			Channel: "email",
			To:      award.Email,
			Subject: subj,
			Body:    body,
		})
		cancel()
		emailSent = serr == nil
	}

	return &BountyAwardOut{
		Award: BountyAwardView{
			ID:           award.ID,
			Handle:       award.Handle,
			Kind:         award.Kind,
			Target:       award.Target,
			Summary:      award.Summary,
			Points:       award.Points,
			CreditsCents: award.CreditsCents,
			CreatedAt:    award.CreatedAt,
		},
		Hunter:    hunter,
		EmailSent: emailSent,
	}, nil
}
