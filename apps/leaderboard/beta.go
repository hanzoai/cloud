// beta.go — THE BETA BOARD: everyone on the hanzo.ai/beta waitlist, ranked by one
// score.
//
//	GET  /v1/leaderboard/beta              the board: the top N and the caller's row
//	plane leaderboard_beta_place           one record's place, for GET /v1/marketing/beta
//
// It is the leaderboard's because ranking people is this capability's job, and it
// answers at its own address rather than as a `scope` of GET /v1/leaderboard
// because it ranks waitlist records, not ledger rows: its row carries what a
// ledger row cannot (referrals, the application date), and one op is one shape.
// Marketing's place on the waitlist is read from here over the plane, so the
// place a person reads and the rank this board shows are one computation.
//
// THE SCORE is client.Points, the one formula every board shares (client/points.go):
//
//	+ StarPoints per star on GitHub marketing verified, once per repository
//	+ FollowPoints per founder link followed
//	+ ReferralPoints per org that signed up with the record's referral code
//	+ QualifiedPoints per such org that has made metered spend
//	+ 1 per TokensPerPoint tokens the person used, all time, in the record's org
//
// The record's stars and follows are counted from the actions it holds, not from the
// points it stored when each was filed, so a change to what an action earns reaches
// every record at once. A bug report or bounty a person claimed on their own record
// counts for nothing here: it is not verified, and the verified ones are the awards
// the bounty board keeps (GET /v1/leaderboard/bounty), which are an org's.
//
// Ties go to the earlier application, so everyone who applied before a newcomer
// stays ahead of them on the same score.
//
// EVERYONE WHO EVER APPLIED IS ON IT. A waitlist record exists only for a person
// who applied signed in; the leads that applied to a program before sign-in was
// asked for, or without an account, are on the board as rows of their own: no
// name, score 0, the date they filed. A lead is one row per person (one address,
// however many forms), and is claimed by a record that is the same person's (the
// address the record is filed on) or that carries its reference: then the record
// is the row, and it began when the lead did if that was earlier, so a person
// keeps the place their first form earned. A lead's row stays whether or not its
// address took itself off the mail: the list is the mail's gate, and nothing here
// writes to anyone. A share click counts for nothing: it cannot be
// verified, and a bot could click forever.
//
// A referral is an org's (apps/referral), so an org's referrals are counted once,
// on its earliest record, and never for a shared org (a brand's sign-up org
// holds strangers, whose referrals are no one person's). Tokens are the person's
// ledger rows, keyed "<org>/<name>" as the write path keys them, from the IAM
// username marketing keeps on the record.
//
// NAMES. A row is named only for a person who opted into the public board (PUT
// /v1/leaderboard/optin, the one consent this capability keeps), under the
// handle they chose; everyone else's name is null. The caller always sees their
// own row named. Tokens are a bucket on every row and exact only on a named row
// and the caller's.
//
// FAIL CLOSED. No validated principal is 401 and an unreadable waitlist 503. The
// warehouse or the referral peer not answering ranks without that term for every
// record alike, and the answer says which (`tokens`, `referrals`).

package leaderboard

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	referralpeer "github.com/hanzoai/cloud/client/referral"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

const (
	// betaRows is how many rows the board answers when the caller names none.
	betaRows = 25
	// tallyChunk and tokensChunk bound the orgs and ids one peer call or query names.
	tallyChunk  = 1000
	tokensChunk = 500
)

// The board's inputs, variables so a test can stand them in.
var (
	waitingFn    = crm.Waiting
	applicantsFn = crm.Applicants
	installFn    = crm.Install
	tallyFn      = referralpeer.ReferralTallies
)

// waiter is one ranked row: a waitlist record, or a lead no record claims (which
// holds no sub, org or name).
type waiter struct {
	sub, org, name                       string
	stars, follows, referrals, qualified int
	tokens                               int64
	score                                int
	// id orders rows that began together: the account for a record, the lead's
	// reference for a lead.
	id string
	// person and lead are what a record claims leads by: the CRM person its address
	// is, and the reference its application was filed under.
	person, lead string
	// began is when the application began, and applied is its day.
	began   time.Time
	applied string
}

// standings is the ranked waitlist, best first, and which terms were counted.
type standings struct {
	waiters           []waiter
	tokens, referrals bool
}

// betaData is what ranking reads of a waitlist record's data (apps/marketing beta.go).
type betaData struct {
	User    string       `json:"user"`
	Org     string       `json:"org"`
	Name    string       `json:"name"`
	Actions []betaAction `json:"actions"`
	Applied string       `json:"applied"`
	Lead    string       `json:"lead"`
}

// betaAction is one action marketing counted on a record.
type betaAction struct {
	Action string `json:"action"`
	Target string `json:"target"`
}

// counted is the stars and follows a record's actions hold, each once per target.
func counted(actions []betaAction) (stars, follows int) {
	seen := map[betaAction]bool{}
	for _, a := range actions {
		if seen[a] {
			continue
		}
		seen[a] = true
		switch a.Action {
		case "star":
			stars++
		case "follow":
			follows++
		}
	}
	return stars, follows
}

// brandCtx is a context acting for the brand whose waitlist it is, cancelled with ctx.
func brandCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	fctx, cancel := context.WithCancel(cloud.For(context.Background(), cloud.Brand()))
	stop := context.AfterFunc(ctx, cancel)
	return fctx, func() { stop(); cancel() }
}

// never is the end of time: the day of an application that carries no date, so it
// ranks after every dated one on a tie.
var never = time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC)

// layouts are the formats an application's stamps are written in: RFC 3339 by
// marketing, Base's date by the CRM for a record and for a form, and a bare day.
var layouts = []string{time.RFC3339, "2006-01-02 15:04:05.000Z", "2006-01-02 15:04:05Z", time.DateOnly}

// began is the earliest of stamps, each read in the layout it is written in. A
// stamp none of them reads is left out, and with none left it is never.
func began(stamps ...string) time.Time {
	out := never
	for _, s := range stamps {
		for _, layout := range layouts {
			if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
				if t.Before(out) {
					out = t.UTC()
				}
				break
			}
		}
	}
	return out
}

// dayOf is the day an application began, "2006-01-02"; empty for one with no date.
func dayOf(t time.Time) string {
	if t.Equal(never) {
		return ""
	}
	return t.Format(time.DateOnly)
}

// rank reads every waitlist record and every lead that applied, and ranks them.
func rank(ctx context.Context) (standings, error) {
	brand := cloud.Brand()
	bctx, cancel := brandCtx(ctx)
	defer cancel()
	if err := installFn(bctx, brand); err != nil {
		return standings{}, err
	}
	recs, err := waitingFn(bctx, brand)
	if err != nil {
		return standings{}, err
	}
	leads, err := applicantsFn(bctx, brand)
	if err != nil {
		return standings{}, err
	}
	ws := make([]waiter, 0, len(recs)+len(leads))
	for _, r := range recs {
		var d betaData
		if json.Unmarshal(r.Data, &d) != nil || strings.TrimSpace(d.User) == "" {
			continue
		}
		stars, follows := counted(d.Actions)
		ws = append(ws, waiter{sub: d.User, org: d.Org, name: d.Name, stars: stars, follows: follows, id: d.User, person: r.Person, lead: d.Lead, began: began(d.Applied, r.Created)})
	}
	// Records are first and oldest first: the referral tally counts an org's referrals
	// on its earliest, and a lead's date goes to the earliest record that claims it.
	ws = claim(ws, leads)
	st := standings{referrals: tallied(bctx, ws), tokens: used(ctx, ws)}
	for i := range ws {
		ws[i].score = client.Points(client.Sources{Tokens: ws[i].tokens, Referrals: ws[i].referrals, Qualified: ws[i].qualified, Stars: ws[i].stars, Follows: ws[i].follows})
		ws[i].applied = dayOf(ws[i].began)
	}
	slices.SortStableFunc(ws, func(a, b waiter) int {
		return cmp.Or(cmp.Compare(b.score, a.score), a.began.Compare(b.began), strings.Compare(a.id, b.id))
	})
	st.waiters = ws
	return st, nil
}

// claim puts the leads that applied beside the records. A person is one row
// however many forms their address filed, and began on the earliest. A record that
// is the person's (the CRM person its address is) or that was filed under one of
// their forms (its lead reference) claims them: the earliest such record begins
// when they did, if that was earlier, and the person is not a row of their own.
// What no record claims is a row with no account, no name and no score.
func claim(ws []waiter, leads []crm.Applicant) []waiter {
	type who struct {
		ref     string
		at      time.Time
		claimed bool
	}
	byPerson := map[string]*who{}
	byRef := map[string]*who{}
	var people []*who
	for _, l := range leads {
		at := began(l.At)
		w, ok := byPerson[l.Person]
		if !ok {
			w = &who{ref: l.Ref, at: at}
			byPerson[l.Person] = w
			people = append(people, w)
		}
		if at.Before(w.at) || (at.Equal(w.at) && l.Ref < w.ref) {
			w.ref, w.at = l.Ref, at
		}
		byRef[l.Ref] = w
	}
	for i := range ws {
		w := byPerson[ws[i].person]
		if w == nil {
			w = byRef[ws[i].lead]
		}
		if w == nil || w.claimed {
			continue
		}
		w.claimed = true
		if w.at.Before(ws[i].began) {
			ws[i].began = w.at
		}
	}
	for _, w := range people {
		if !w.claimed {
			ws = append(ws, waiter{id: "lead:" + w.ref, began: w.at})
		}
	}
	return ws
}

// shared reports whether org holds strangers: a brand's sign-up org or another the
// platform runs, other than the admin org.
func shared(org string) bool { return org != authz.AdminOrg && authz.IsHeldOrg(org) }

// tallied counts each org's referrals onto its earliest record, and reports
// whether the referral peer answered. ws is oldest first.
func tallied(ctx context.Context, ws []waiter) bool {
	first := map[string]int{}
	var orgs []string
	for i, w := range ws {
		if w.org == "" || shared(w.org) {
			continue
		}
		if _, ok := first[w.org]; !ok {
			first[w.org] = i
			orgs = append(orgs, w.org)
		}
	}
	for chunk := range slices.Chunk(orgs, tallyChunk) {
		out, err := tallyFn(ctx, &client.ReferralTallyIn{Orgs: chunk})
		if err != nil {
			for i := range ws {
				ws[i].referrals, ws[i].qualified = 0, 0
			}
			return false
		}
		for _, t := range out.Orgs {
			if i, ok := first[t.Org]; ok {
				ws[i].referrals, ws[i].qualified = t.Referrals, t.Qualified
			}
		}
	}
	return true
}

// used sets each record's all-time tokens from the rollup, and reports whether
// the warehouse answered.
func used(ctx context.Context, ws []waiter) bool {
	if !datastoreEnabled() || EnsureUsageRollup(ctx) != nil {
		return false
	}
	at := map[string][]int{}
	var ids []string
	for i, w := range ws {
		if w.org == "" || w.name == "" {
			continue
		}
		id := w.org + "/" + w.name
		if _, ok := at[id]; !ok {
			ids = append(ids, id)
		}
		at[id] = append(at[id], i)
	}
	for chunk := range slices.Chunk(ids, tokensChunk) {
		sqlStr, args := betaTokensSQL(chunk)
		rows, err := queryDatastore(ctx, sqlStr, args...)
		if err != nil {
			for i := range ws {
				ws[i].tokens = 0
			}
			return false
		}
		for _, r := range rows {
			for _, i := range at[aString(r["user_id"])] {
				ws[i].tokens = aInt64(r["tokens"])
			}
		}
	}
	return true
}

// bucket is a token count as a range, for a row whose owner is not named.
func bucket(t int64) string {
	switch {
	case t <= 0:
		return "0"
	case t < 1_000_000:
		return "<1M"
	case t < 10_000_000:
		return "1M-10M"
	case t < 100_000_000:
		return "10M-100M"
	case t < 1_000_000_000:
		return "100M-1B"
	default:
		return "1B+"
	}
}

// betaQuery shapes one read of the beta board.
type betaQuery struct {
	// Limit caps the rows, clamped to 1..100. Defaults to 25, which is also what a
	// non-positive value takes.
	Limit int `json:"limit"`
}

// BetaRow is one record on the beta board.
type BetaRow struct {
	// Rank is the record's place, 1 first.
	Rank int `json:"rank"`
	// Name is the handle its owner listed under on the public board; null for a
	// person who did not opt in, whom the page calls a builder. The caller's own
	// row is always named.
	Name *string `json:"name"`
	// You marks the caller's own row.
	You bool `json:"you"`
	// Score is what the row is ranked by: points, referrals, qualified referrals
	// and tokens, as GET /v1/leaderboard/beta's description states.
	Score int `json:"score"`
	// Referrals is how many orgs signed up with the record's referral code.
	Referrals int `json:"referrals"`
	// Qualified is how many of them have made metered spend.
	Qualified int `json:"qualified"`
	// Tokens is the person's all-time token usage as a range: 0, <1M, 1M-10M,
	// 10M-100M, 100M-1B or 1B+.
	Tokens string `json:"tokens"`
	// TokensUsed is the exact count, on a named row and the caller's; absent on
	// every other.
	TokensUsed int64 `json:"tokensUsed,omitempty"`
	// Applied is the day the application began, 2006-01-02: the record's own, or the
	// day of the lead it claims when that was earlier.
	Applied string `json:"applied"`
}

// BetaBoard is the ranked waitlist as the caller may see it.
type BetaBoard struct {
	// Rows are the top of the waitlist, best first.
	Rows []BetaRow `json:"rows"`
	// You is the caller's own row wherever it ranks; null when the caller holds
	// no record.
	You *BetaRow `json:"you"`
	// Of is how many people are on the waitlist: every account that applied, and
	// every lead that applied before it had an account.
	Of int `json:"of"`
	// Tokens is whether token usage was counted; false when the warehouse did not
	// answer, and no record's score has it then.
	Tokens bool `json:"tokens"`
	// Referrals is whether referrals were counted; false when the referral service
	// did not answer, and no record's score has them then.
	Referrals bool `json:"referrals"`
}

// betaBoard ranks everyone on the hanzo.ai/beta waitlist by one score: 1 per star
// on GitHub verified for the record and 1 per founder link followed, plus 1 per org
// that signed up with its referral code, plus 20 per such org that has made metered
// spend, plus 1 per 100,000 tokens the person used, all time. Ties go to the earlier
// application. Share clicks count for nothing.
//
// Everyone who ever applied is on it. A form filed before the person had an
// account is a row of its own (no name, score 0, the day it was filed) until their
// account's record claims it, and the record then begins when the form did.
//
// A row is named only for a person who opted into the public board (PUT
// /v1/leaderboard/optin); the caller always sees their own row named. Tokens are a
// range on every row and exact on a named row and the caller's. `tokens` and
// `referrals` say whether the warehouse and the referral service answered; a
// term that did not is left out of every score alike.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func (o boardOps) betaBoard(ctx context.Context, in *betaQuery) (*BetaBoard, error) {
	if _, err := tenantOf(ctx, "sign in to view the leaderboard"); err != nil {
		return nil, err
	}
	c, ok := requestOf(ctx)
	if !ok {
		return nil, zip.ErrUnauthorized("sign in to view the leaderboard")
	}
	p, ok := principal.Signed(c)
	if !ok {
		return nil, zip.ErrUnauthorized("sign in to view the leaderboard")
	}
	noStore(ctx)
	st, err := rank(ctx)
	if err != nil {
		o.s.Log.Warn("leaderboard: read the beta waitlist", "err", err)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the waitlist could not be read")
	}
	limit := clampLimit(in.Limit, betaRows)
	out := &BetaBoard{Rows: []BetaRow{}, Of: len(st.waiters), Tokens: st.tokens, Referrals: st.referrals}
	for i, w := range st.waiters {
		mine := w.sub == p.Subject
		if i >= limit && !mine {
			continue
		}
		row := o.betaRow(ctx, i+1, w, mine)
		if i < limit {
			out.Rows = append(out.Rows, row)
		}
		if mine {
			out.You = &row
		}
	}
	return out, nil
}

// betaRow is w as the caller sees it: named when its owner listed, or when it is
// the caller's own.
func (o boardOps) betaRow(ctx context.Context, rank int, w waiter, mine bool) BetaRow {
	row := BetaRow{Rank: rank, You: mine, Score: w.score, Referrals: w.referrals, Qualified: w.qualified, Tokens: bucket(w.tokens), Applied: w.applied}
	handle, listed := "", false
	if w.org != "" && w.name != "" && o.s != nil && o.s.State.store != nil {
		if u, err := o.s.State.store.GetUser(ctx, w.org+"/"+w.name); err == nil && u.Listed {
			handle, listed = u.Handle, true
		}
	}
	switch {
	case listed:
		name := cmp.Or(handle, w.name)
		row.Name = &name
	case mine:
		name := cmp.Or(w.name, "You")
		row.Name = &name
	}
	if row.Name != nil {
		row.TokensUsed = w.tokens
	}
	return row
}

// Standing is the place of the record sub holds: what GET /v1/marketing/beta
// answers, over the plane, from the same ranking the board shows.
func Standing(ctx context.Context, sub string) (*client.BetaPlace, error) {
	st, err := rank(ctx)
	if err != nil {
		return nil, err
	}
	out := &client.BetaPlace{Of: len(st.waiters), Tokens: st.tokens}
	for i, w := range st.waiters {
		if w.sub == sub {
			out.Place, out.Score = i+1, w.score
			break
		}
	}
	return out, nil
}

// exposeBetaPlace publishes a record's place on the internal plane. routes calls it.
func exposeBetaPlace(o boardOps) {
	cloud.Plane().Post("/leaderboard/beta/place", o.betaPlace,
		zip.WithOperationID(client.LeaderboardBetaPlace),
		zip.WithSummary("Answer one beta record's place on the waitlist, as the beta board ranks it"))
}

// betaPlace answers the place of the record a subject holds, for the brand whose
// waitlist it is: marketing asks it for GET /v1/marketing/beta. A subject that
// holds no record answers place 0.
func (o boardOps) betaPlace(ctx context.Context, in *client.BetaPlaceIn) (*client.BetaPlace, error) {
	if org, ok := cloud.Tenant(ctx); !ok || org != cloud.Brand() {
		return nil, zip.ErrForbidden("beta place: the brand asks")
	}
	sub := strings.TrimSpace(in.Subject)
	if sub == "" {
		return nil, zip.ErrBadRequest("beta place: a subject is required")
	}
	out, err := Standing(ctx, sub)
	if err != nil {
		o.s.Log.Warn("leaderboard: read the beta waitlist", "err", err)
		return nil, zip.Errorf(http.StatusServiceUnavailable, "the waitlist could not be read")
	}
	return out, nil
}
