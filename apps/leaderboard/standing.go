// standing.go — THE EARN-YOUR-VIEW BOARD.
//
//	GET /v1/leaderboard/standing    the public board, a window of it, masked
//
// EVERY ORG THAT HAS EVER EXISTED IN IAM IS ON IT, held orgs aside (the ones the
// platform runs for strangers, which are no builder's place in line). An org that
// has done nothing holds 0 points and sits at the bottom. The rank is by points,
// then the org that signed up first, then org id, so a place is stable.
//
// POINTS are client.Points, the one formula the bounty board and the beta waitlist
// share, counted all-time from what cloud already records: tokens from the usage
// rollup, referrals from the referral ledger, stars and follows from the beta
// waitlist records marketing verified them on, and the bounty awards this
// capability keeps. Nothing is estimated: a source that cannot be read fails the
// whole line, which is served from the last good read, or not at all.
//
// PUBLIC. Anyone may read it, signed in or not, and nobody is asked who they are:
// the op resolves the caller with principal.OrgFrom and treats an absent principal as
// a reader, where the rest of this capability refuses one (tenantOf). That is the
// pattern apps/research's public listing uses, and it loosens nothing else.
//
// THE WINDOW IS THE WHOLE ENFORCEMENT. A reader sees where THEY stand in the line:
// the ranks from (their rank - reveal) to (their rank + belowRows), and `locked` ranks
// above that. How far up they may look is earned, from their own points (revealSteps);
// the rest of the board is not hidden from the page, it never leaves the server. A
// reader who is not signed in, or has no rank, sees where a newcomer starts: the last
// tailRows ranks.
//
// NAMES. The board shows usernames and points and hides the middle of the name:
//
//	the caller's own row     the full handle, self: true
//	an org that listed       the full handle it chose (PUT /v1/leaderboard/optin[/org])
//	an org that withdrew     no handle at all; the page says "A builder"
//	everyone else            the handle masked (mask)
//
// A handle is never an email, a real name or an org id: the masked form of an org's
// id is two letters at each end of it, which names no one.

package leaderboard

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	iampeer "github.com/hanzoai/cloud/client/iam"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

const (
	// maskDots stands for the hidden middle of a masked handle.
	maskDots = "•••"
	// tailRows is how many of the last ranks a reader with no rank sees: where a
	// newcomer starts.
	tailRows = 10
	// belowRows is how many ranks under their own a signed-in reader sees.
	belowRows = 5
	// everyone is a reveal that reaches the top of the board.
	everyone = -1
)

// bandLevels is how many levels one band holds.
const bandLevels = 10

// bands are the rewards program's ten bands of levels, 0 to 99 in tens, lowest
// first: Photon is levels 0-9, Wave 10-19, and so on to Singularity at 90-99. Show
// is how many ranks above their own a reader in the band may see; the last band
// sees everyone.
var bands = []struct {
	Name string
	Show int
}{
	{"Photon", 5},
	{"Wave", 10},
	{"Focus", 25},
	{"Flare", 50},
	{"Comet", 100},
	{"Pulsar", 250},
	{"Nebula", 500},
	{"Quasar", 1000},
	{"Magnetar", 2500},
	{"Singularity", everyone},
}

// levelAt is the lifetime points level n needs: 10*n*n.
func levelAt(n int) int { return 10 * n * n }

// revealStep is one rung of how far up a reader may look: with At points or more
// they see Show ranks above their own. A rung is a band, and begins at the points
// its first level needs.
type revealStep struct{ At, Show int }

var revealSteps = func() []revealStep {
	steps := make([]revealStep, len(bands))
	for b, band := range bands {
		steps[b] = revealStep{At: levelAt(b * bandLevels), Show: band.Show}
	}
	return steps
}()

// revealAt is the rung a reader with this many points stands on.
func revealAt(points int) int {
	i := 0
	for j, s := range revealSteps {
		if points >= s.At {
			i = j
		}
	}
	return i
}

// above is how many ranks over a reader at pos (0-based) a rung shows.
func (s revealStep) above(pos int) int {
	if s.Show == everyone {
		return pos
	}
	return min(s.Show, pos)
}

// viewport is the ranks [lo, hi) (0-based) a reader at pos with these points may
// see among total: their own rank, belowRows under it, and as many above it as their
// rung shows. next is the rung after theirs, absent when nothing above is locked.
func viewport(points, pos, total int) (lo, hi int, next *StandingNext) {
	i := revealAt(points)
	step := revealSteps[i]
	lo, hi = pos-step.above(pos), min(total, pos+1+belowRows)
	if lo > 0 && i+1 < len(revealSteps) {
		n := revealSteps[i+1]
		next = &StandingNext{Reveal: n.above(pos) - step.above(pos), At: n.At, More: n.At - points}
	}
	return lo, hi, next
}

// seen is the ranks [lo, hi) (0-based) of a line of total a reader may see, and the rung
// after theirs. A reader at pos with these points sees what viewport gives them; a reader
// with no place on the line (pos < 0), signed in or not, sees the last tailRows, where a
// newcomer starts. It is the one rule of who sees whom: the standing's rows and the
// galaxy's names and points are both cut by it.
func seen(pos, points, total int) (lo, hi int, next *StandingNext) {
	if pos < 0 {
		return max(0, total-tailRows), total, nil
	}
	return viewport(points, pos, total)
}

// mask hides the middle of a handle: the first two and last two characters of one
// of six or more, the first character of a shorter one.
func mask(handle string) string {
	r := []rune(handle)
	switch {
	case len(r) == 0:
		return ""
	case len(r) >= 6:
		return string(r[:2]) + maskDots + string(r[len(r)-2:])
	default:
		return string(r[:1]) + maskDots
	}
}

// StandingRow is one rank on the board.
type StandingRow struct {
	// Rank is the 1-based place on the whole board, not the window.
	Rank int `json:"rank"`
	// Handle is the name to show: masked, or full for the reader's own row and for an
	// org that listed. Empty for an org that withdrew; show it as "A builder".
	Handle string `json:"handle"`
	// Points is the score the board ranks by.
	Points int `json:"points"`
	// Self marks the reader's own row.
	Self bool `json:"self"`
}

// StandingNext is the next rung of how far up the reader may look.
type StandingNext struct {
	// Reveal is how many more ranks above the reader that rung unlocks.
	Reveal int `json:"reveal"`
	// At is the points at which it unlocks.
	At int `json:"at"`
	// More is At minus the reader's points: what is left to earn.
	More int `json:"more"`
}

// StandingSelf is the signed-in reader's own place.
type StandingSelf struct {
	// Ranked is false for a reader with no usage and no award yet. Rank is then 0 and
	// the window is the one a newcomer sees.
	Ranked bool `json:"ranked"`
	// Rank is the reader's place on the whole board.
	Rank int `json:"rank"`
	// Points is the reader's score.
	Points int `json:"points"`
	// Handle is the reader's own full handle.
	Handle string `json:"handle"`
	// Next is the next rung to unlock; absent when nothing above is locked.
	Next *StandingNext `json:"next,omitempty"`
}

// StandingView is the whole answer of GET /v1/leaderboard/standing.
type StandingView struct {
	// Available is false when the warehouse is not connected: Rows is then empty
	// because nothing could be read, not because nobody has used anything.
	Available bool `json:"available"`
	// Total is how many orgs are ranked: every org IAM holds that is not a held one.
	Total int `json:"total"`
	// Scored is how many of them hold any point; the rest are at the bottom.
	Scored int `json:"scored"`
	// Locked is how many ranks lie above the first row, which the reader has not
	// earned a view of. They are counted, never sent.
	Locked int `json:"locked"`
	// Rows are the ranks the reader may see, best first. Always a list.
	Rows []StandingRow `json:"rows"`
	// Self is the reader's own place; absent when not signed in.
	Self *StandingSelf `json:"self,omitempty"`
}

// entry is one ranked org before it is named.
type entry struct {
	org    string
	src    client.Sources
	points int
	// since is when the org signed up, per IAM; never when IAM did not say.
	since time.Time
}

// errNoWarehouse is a line asked for while the usage warehouse is not connected.
var errNoWarehouse = errors.New("leaderboard: usage warehouse not connected")

// line is the whole ranking, best first, as of one read of its sources.
type line struct {
	ranked []entry
	// scored is how many entries hold any point.
	scored int
	// at is each org's index in ranked.
	at map[string]int
}

// entry is org's entry, if it is on the line.
func (l *line) entry(org string) (entry, bool) {
	i, ok := l.at[org]
	if !ok {
		return entry{}, false
	}
	return l.ranked[i], true
}

// tokenRank is e's place among the orgs by tokens used, 1 first; 0 for an org that
// has used none.
func (l *line) tokenRank(e entry) int {
	if e.src.Tokens <= 0 {
		return 0
	}
	above := 0
	for _, o := range l.ranked {
		if o.src.Tokens > e.src.Tokens {
			above++
		}
	}
	return above + 1
}

// lineTTL is how long a read of the sources is reused.
const lineTTL = time.Minute

// lineTimeout bounds one read of the sources, which a reader's own cancel does not
// stop: the read is shared, and one reader leaving must not fail it for the rest.
const lineTimeout = 45 * time.Second

// lines is the one shared read of the line.
var lines memo[line]

// directoryFn is IAM's directory, a variable so a test can answer it.
var directoryFn = iampeer.IAMDirectory

// resetStanding forgets what the public boards have read.
func resetStanding() {
	lines.reset()
	pulses.reset()
	galaxies.reset()
	badges.reset()
}

// current is the line, as the shared read last had it.
func current(ctx context.Context, st *optinStore) (*line, error) {
	return lines.get(lineTTL, func() (*line, error) { return readLine(ctx, st) })
}

// readLine reads every source and ranks the orgs.
func readLine(ctx context.Context, st *optinStore) (*line, error) {
	if !datastoreEnabled() {
		return nil, errNoWarehouse
	}
	if err := EnsureUsageRollup(ctx); err != nil {
		return nil, err
	}
	// The read is shared by every reader, so it acts for the platform and not for
	// whoever asked, and outlives them.
	root, cancel := context.WithTimeout(context.Background(), lineTimeout)
	defer cancel()

	dir, err := directoryFn(cloud.For(root, authz.AdminOrg))
	if err != nil {
		return nil, err
	}
	byOrg := make(map[string]*entry, len(dir.Orgs))
	names := make([]string, 0, len(dir.Orgs))
	for _, o := range dir.Orgs {
		if o.Name == "" || authz.IsHeldOrg(o.Name) {
			continue
		}
		if _, ok := byOrg[o.Name]; !ok {
			byOrg[o.Name] = &entry{org: o.Name, since: began(o.Created)}
			names = append(names, o.Name)
		}
	}

	rows, err := queryDatastore(root, standingUsageSQL)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if e, ok := byOrg[aString(r["organization"])]; ok {
			e.src.Tokens = aInt64(r["total_tokens"])
		}
	}

	brand := cloud.Brand()
	bctx := cloud.For(root, brand)
	for chunk := range slices.Chunk(names, tallyChunk) {
		out, err := tallyFn(bctx, &client.ReferralTallyIn{Orgs: chunk})
		if err != nil {
			return nil, err
		}
		for _, t := range out.Orgs {
			if e, ok := byOrg[t.Org]; ok {
				e.src.Referrals, e.src.Qualified = t.Referrals, t.Qualified
			}
		}
	}

	if err := installFn(bctx, brand); err != nil {
		return nil, err
	}
	recs, err := waitingFn(bctx, brand)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		var d betaData
		if json.Unmarshal(r.Data, &d) != nil {
			continue
		}
		if e, ok := byOrg[d.Org]; ok {
			stars, follows := counted(d.Actions)
			e.src.Stars += stars
			e.src.Follows += follows
		}
	}

	awards, err := st.BountyTotals(root)
	if err != nil {
		return nil, err
	}
	for org, points := range awards {
		if e, ok := byOrg[org]; ok {
			e.src.Bounty = points
		}
	}

	l := &line{ranked: make([]entry, 0, len(byOrg))}
	for _, e := range byOrg {
		e.points = client.Points(e.src)
		if e.points > 0 {
			l.scored++
		}
		l.ranked = append(l.ranked, *e)
	}
	slices.SortFunc(l.ranked, func(a, b entry) int {
		return cmp.Or(cmp.Compare(b.points, a.points), a.since.Compare(b.since), strings.Compare(a.org, b.org))
	})
	l.at = make(map[string]int, len(l.ranked))
	for i, e := range l.ranked {
		l.at[e.org] = i
	}
	return l, nil
}

// handleOf is the name another reader sees for org.
func handleOf(org string, choices map[string]choice) string {
	c, ok := choices[org]
	switch {
	case !ok:
		return mask(org)
	case !c.Listed:
		return ""
	}
	return strings.TrimSpace(c.Handle)
}

// Returns the public board of who has used and built the most on Hanzo, shown as a
// window around the reader. Every org IAM holds is ranked, by points: tokens used,
// referrals, verified stars, follows and bounty awards. A signed-in reader sees their own rank, the ranks
// just below it and as many above it as their points have earned; the rest are
// counted in `locked` and never sent. A reader who is not signed in sees the last
// ranks, where a newcomer starts. Names are masked in the middle; the reader's own is
// whole, an org that listed itself is whole, and one that withdrew has none. No
// credential is needed to read it.
//
// Example: {}
func (o boardOps) standing(ctx context.Context, _ *cloud.Unit) (*StandingView, error) {
	noStore(ctx)
	view := &StandingView{Rows: []StandingRow{}}
	st := o.s.State.store
	l, err := current(ctx, st)
	if err != nil {
		o.s.Log.Warn("standing sources unreadable; honest-empty", "err", err)
		return view, nil
	}
	choices, err := st.Choices(ctx)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "read listing choices: %v", err)
	}
	ranked := l.ranked
	view.Available, view.Total, view.Scored = true, len(ranked), l.scored

	org, signed := principal.OrgFrom(ctx)
	pos := -1
	if signed {
		pos = slices.IndexFunc(ranked, func(e entry) bool { return e.org == org })
	}

	points := 0
	if pos >= 0 {
		points = ranked[pos].points
	}
	lo, hi, next := seen(pos, points, len(ranked))
	view.Locked = lo

	for i := lo; i < hi; i++ {
		e := ranked[i]
		row := StandingRow{Rank: i + 1, Points: e.points, Self: i == pos}
		if row.Self {
			row.Handle = o.selfHandle(ctx, org)
		} else {
			row.Handle = handleOf(e.org, choices)
		}
		view.Rows = append(view.Rows, row)
	}
	if signed {
		self := &StandingSelf{Handle: o.selfHandle(ctx, org), Next: next}
		if pos >= 0 {
			self.Ranked, self.Rank, self.Points = true, pos+1, ranked[pos].points
		}
		view.Self = self
	}
	return view, nil
}

// selfHandle is how the reader appears to themselves: the handle they chose, else
// their org's display name, else their username, else their org id.
func (o boardOps) selfHandle(ctx context.Context, org string) string {
	st := o.s.State.store
	id := selfIDOf(ctx, org)
	if id != "" {
		if u, err := st.GetUser(ctx, id); err == nil && strings.TrimSpace(u.Handle) != "" {
			return u.Handle
		}
	}
	if g, err := st.GetOrg(ctx, org); err == nil && strings.TrimSpace(g.Display) != "" {
		return g.Display
	}
	if id != "" {
		return nameOf(id)
	}
	return org
}
