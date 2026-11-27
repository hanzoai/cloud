// galaxy.go — THE GALAXY: every ranked org as a star, and who brought whom as links.
//
//	GET /v1/leaderboard/galaxy    public; one star for each ranked org, opaque ids, names by the reveal window
//
// The standing shows a reader a window of the line; the galaxy draws the whole of it at
// once, for a page: each org the standing ranks is a star, with its band, a coarse size,
// the star of the org that referred it, if any, and, where the reader may see it, the
// points it holds (client.Points, the one score) and the name the standing would show for
// it. At the middle is what the platform did in the last day, which the page feeds the
// black hole with. It is the same line, read once for every reader (standing.go `current`).
//
// THE WINDOW IS THE ENFORCEMENT, as in the standing. A star's exact points and its name
// leave the server only for the ranks the reader may see, by the one rule the standing's
// rows are cut by (`seen`, from `revealSteps`): a signed-in reader's own rank, five below
// it and as many above as their points have earned; any other reader, the last tailRows
// ranks. The reader's own star is always in it. Every other star keeps its place, its
// band and a size bucket (the bit length of its points: a factor of two), so the sky is the
// same sky for everyone, and carries no name and `points: -1`. The galaxy therefore tells a
// reader nothing about an org that the standing would not.
//
// PUBLIC, like the standing: the op asks nobody who they are. What a signed-in reader adds
// is their window and `self`, the id of their own star, so the shared read holds no reader.
//
// BANDS are the reward program's: the ten bands of the 0-99 levels staff keep in the
// program's level table, read through the reward capability (`reward_levels`, a peer op) as
// the read of the galaxy is made. Where that read fails, or answers a table that is not
// one, the table the program is seeded with stands in: level n begins at 10·n² points and a
// band is ten levels.
//
// OPAQUE. A star is named by an id, and an id is the first bytes of an HMAC of the org
// under a random key drawn for each read of the galaxy: it is the same wherever the
// response mentions that star (the sponsor column), names no org however well the orgs
// are guessed, and means nothing once the read is replaced, a minute on. The referral
// graph leaves the server as those ids and nothing else; no org id, no handle beyond the
// standing's mask and no email is in the answer. An org that withdrew from public
// listing has no mask, as in the standing, and is still a star.
//
// COLUMNS. The stars are arrays side by side, not one object each, which is a little over
// half the bytes for the ~1,850 orgs the platform holds today: the keys an object repeats
// for every star are sent once, and what is left is the ids and the names, which are
// all there is to say.

package leaderboard

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"math/bits"
	"sort"
	"time"

	"github.com/hanzoai/cloud/client"
	referralpeer "github.com/hanzoai/cloud/client/referral"
	rewardpeer "github.com/hanzoai/cloud/client/reward"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
)

const (
	// galaxyTTL is how long a read of the galaxy is reused.
	galaxyTTL = time.Minute
	// bandWidth is how many levels make a band in the seeded table.
	bandWidth = 10
	// seededLevels is how many levels the seeded table has: 0 to 99.
	seededLevels = 100
	// edgePage is how many referral edges are read at a time.
	edgePage = 1000
)

// bandNames are the ten bands of the seeded table, ten levels each: the astrophysics
// ladder, from a photon to a singularity.
var bandNames = [...]string{"Photon", "Wave", "Focus", "Flare", "Comet", "Pulsar", "Nebula", "Quasar", "Magnetar", "Singularity"}

// referralEdgesFn is the referral ledger's edges, a variable so a test can answer it.
var referralEdgesFn = referralpeer.ReferralEdges

// rewardLevelsFn is the reward program's level table, a variable so a test can answer it.
var rewardLevelsFn = rewardpeer.RewardLevels

// table is the bands as a galaxy is drawn in them: where each level begins and which band
// it is in, lowest first, and the names of the bands in that order.
type table struct {
	// from[i] is the lifetime points level i begins at; band[i] is its band's index in bands.
	from  []int64
	band  []int
	bands []string
}

// seeded is the table the reward program is seeded with: level n begins at 10·n² points and
// a band is ten levels.
func seeded() *table {
	t := &table{from: make([]int64, seededLevels), band: make([]int, seededLevels), bands: bandNames[:]}
	for n := range seededLevels {
		t.from[n], t.band[n] = int64(10*n*n), n/bandWidth
	}
	return t
}

// tableOf is the table a program's levels make, or false where they make none: no level,
// a first level above 0 points, a level that does not begin above the one before it, or a
// level with no band.
func tableOf(levels []client.RewardLevel) (*table, bool) {
	if len(levels) == 0 || levels[0].From > 0 {
		return nil, false
	}
	t := &table{from: make([]int64, len(levels)), band: make([]int, len(levels))}
	for i, l := range levels {
		if l.Band == "" || (i > 0 && l.From <= levels[i-1].From) {
			return nil, false
		}
		if len(t.bands) == 0 || t.bands[len(t.bands)-1] != l.Band {
			t.bands = append(t.bands, l.Band)
		}
		t.from[i], t.band[i] = l.From, len(t.bands)-1
	}
	return t, true
}

// bandOf is the index in bands of the band points put a star in: that of the highest level
// that begins at or below them.
func (t *table) bandOf(points int) int {
	i := sort.Search(len(t.from), func(i int) bool { return t.from[i] > int64(points) })
	return t.band[max(i-1, 0)]
}

// size is the coarse size of a star: the bit length of its points, so a star is told apart
// from another only by a factor of two. It is all a locked star shows of its points.
func size(points int) int { return bits.Len(uint(max(points, 0))) }

// GalaxyCenter is what the middle of the galaxy was fed in the last day.
type GalaxyCenter struct {
	// Tokens24h is the tokens used in the last 24 hours: the sum of the pulse's tokens
	// by hour.
	Tokens24h int64 `json:"tokens24h"`
	// ActiveOrgs24h is how many different orgs used any of them.
	ActiveOrgs24h int64 `json:"activeOrgs24h"`
}

// GalaxyStars are the stars, as columns of one length: star i is the i-th value of
// each, best first.
type GalaxyStars struct {
	// ID is each star's opaque id, of no use after the next read.
	ID []string `json:"id"`
	// Points is each star's exact score where the reader's reveal window holds it, and
	// -1 where it does not: a star the reader has not earned a view of.
	Points []int `json:"points"`
	// Size is each star's coarse size, from 0: the bit length of its points, a factor of
	// two. It is every star's, in or out of the window, and is what a sky is drawn from.
	Size []int `json:"size"`
	// Band is each star's index in the response's bands.
	Band []int `json:"band"`
	// Sponsor is the id of the star that referred this one; empty where none did.
	Sponsor []string `json:"sponsor"`
	// Mask is the name the standing shows for the org where the reader's reveal window
	// holds it: masked in the middle, whole where it listed itself, empty where it
	// withdrew. It is empty for every star outside the window, which has points of -1.
	Mask []string `json:"mask"`
}

// GalaxyView is the whole answer of GET /v1/leaderboard/galaxy.
type GalaxyView struct {
	// Available is false when the warehouse is not connected or a source could not be
	// read and none ever was: the stars are then empty because nothing could be read.
	Available bool `json:"available"`
	// Center is what the platform did in the last day.
	Center GalaxyCenter `json:"center"`
	// Bands are the band names of the reward program's level table, lowest first; a
	// star's band indexes them.
	Bands []string `json:"bands"`
	// Stars are the ranked orgs.
	Stars GalaxyStars `json:"stars"`
	// Self is the id of the signed-in reader's own star; empty for a reader who is not
	// signed in or whose org the board does not hold.
	Self string `json:"self"`
}

// galaxyRead is one read of the galaxy: the columns every reader shares, the exact
// points and names which only a reader's window lets out, and each org's place in the
// line, which only the server holds.
type galaxyRead struct {
	view   GalaxyView
	points []int
	masks  []string
	at     map[string]int
}

// window is the galaxy as org may see it: the shared columns, with the exact points and
// the names of the stars in the reveal window and -1 and none for the rest. org is empty
// for a reader who is not signed in; a reader's own star is always in their window.
func (g *galaxyRead) window(org string, signed bool) GalaxyView {
	n := len(g.points)
	pos, points := -1, 0
	if signed {
		if i, ok := g.at[org]; ok {
			pos, points = i, g.points[i]
		}
	}
	lo, hi, _ := seen(pos, points, n)
	view := g.view
	view.Stars.Points, view.Stars.Mask = make([]int, n), make([]string, n)
	for i := range n {
		if i < lo || i >= hi {
			view.Stars.Points[i] = -1
			continue
		}
		view.Stars.Points[i], view.Stars.Mask[i] = g.points[i], g.masks[i]
	}
	if pos >= 0 {
		view.Self = view.Stars.ID[pos]
	}
	return view
}

// galaxies is the one shared read of the galaxy.
var galaxies memo[galaxyRead]

// galaxyCenterSQL is the tokens used and the orgs active since `from`, in one row, from
// the ledger the pulse reads by the hour. The bound is bound.
func galaxyCenterSQL(from time.Time) (string, []any) {
	return "SELECT sum(total_tokens) AS total_tokens, uniqExactIf(organization, organization != '') AS orgs FROM " +
		"hanzo.cloud_usage WHERE timestamp >= ?", []any{from.UTC().Format("2006-01-02 15:04:05")}
}

// idWidth is how many bytes of the HMAC name each of n stars: enough that a draw has two
// of them collide in fewer than one in 500, and never fewer than four. A short id is
// fewer bytes for every star and every sponsor link; it needs no more bits than the
// orgs there are, because it is no secret, only a name for a minute.
func idWidth(n int) int { return max(4, 1+(2*bits.Len(uint(n))+7)/8) }

// opaque names each of orgs by an id: the first bytes of the HMAC of the org under a key
// drawn for this call, in base64url. The ids are all different; a draw in which two
// orgs collide is drawn again.
func opaque(orgs []string) []string {
	width := idWidth(len(orgs))
	for {
		key := make([]byte, 32)
		_, _ = rand.Read(key)
		ids, seen := make([]string, len(orgs)), make(map[string]bool, len(orgs))
		unique := true
		for i, org := range orgs {
			mac := hmac.New(sha256.New, key)
			mac.Write([]byte(org))
			ids[i] = base64.RawURLEncoding.EncodeToString(mac.Sum(nil)[:width])
			if seen[ids[i]] {
				unique = false
				break
			}
			seen[ids[i]] = true
		}
		if unique {
			return ids
		}
	}
}

// referrers is, for each org on the line that was referred by another org on it, the
// org that referred it. An org has one referrer: the first claim stands. An edge that
// would close a loop is left out, so the links are a forest a page can follow up
// without a visited set.
func referrers(ctx context.Context, l *line) (map[string]string, error) {
	bctx := cloud.For(ctx, cloud.Brand())
	from := map[string]string{}
	for offset := 0; ; offset += edgePage {
		out, err := referralEdgesFn(bctx, &client.ReferralEdgesIn{Offset: offset, Limit: edgePage})
		if err != nil {
			return nil, err
		}
		for _, e := range out.Edges {
			_, known := l.at[e.Referrer]
			_, other := l.at[e.Referee]
			if !known || !other || e.Referrer == e.Referee || from[e.Referee] != "" || reaches(from, e.Referrer, e.Referee) {
				continue
			}
			from[e.Referee] = e.Referrer
		}
		if len(out.Edges) < edgePage {
			return from, nil
		}
	}
}

// reaches is whether going up from `org` through from comes to `target`.
func reaches(from map[string]string, org, target string) bool {
	for ; org != ""; org = from[org] {
		if org == target {
			return true
		}
	}
	return false
}

// readGalaxy reads every source and draws the stars. A source that cannot be read fails
// the whole read, as the standing's does: no term is left out for some orgs and not others.
func readGalaxy(ctx context.Context, st *optinStore, log reader) (*galaxyRead, error) {
	l, err := current(ctx, st)
	if err != nil {
		return nil, err
	}
	// Shared by every reader, so it outlives them, as the line's own read does.
	root, cancel := context.WithTimeout(context.Background(), lineTimeout)
	defer cancel()

	choices, err := st.Choices(root)
	if err != nil {
		return nil, err
	}
	from, err := referrers(root, l)
	if err != nil {
		return nil, err
	}
	sql, args := galaxyCenterSQL(newAxes(nowFn().UTC()).hour0)
	rows, err := queryDatastore(root, sql, args...)
	if err != nil {
		return nil, err
	}
	var center GalaxyCenter
	if len(rows) > 0 {
		center = GalaxyCenter{Tokens24h: aInt64(rows[0]["total_tokens"]), ActiveOrgs24h: aInt64(rows[0]["orgs"])}
	}

	orgs := make([]string, len(l.ranked))
	for i, e := range l.ranked {
		orgs[i] = e.org
	}
	ids, levels := opaque(orgs), levelsOf(root, log)
	n := len(orgs)
	g := &galaxyRead{points: make([]int, n), masks: make([]string, n), at: l.at, view: GalaxyView{
		Available: true,
		Center:    center,
		Bands:     levels.bands,
		Stars: GalaxyStars{
			ID: ids, Size: make([]int, n), Band: make([]int, n), Sponsor: make([]string, n),
		},
	}}
	for i, e := range l.ranked {
		g.points[i], g.masks[i] = e.points, handleOf(e.org, choices)
		g.view.Stars.Size[i] = size(e.points)
		g.view.Stars.Band[i] = levels.bandOf(e.points)
		if sponsor, ok := from[e.org]; ok {
			g.view.Stars.Sponsor[i] = ids[l.at[sponsor]]
		}
	}
	return g, nil
}

// levelsOf is the table the reward program keeps, or the seeded one where the program
// cannot be asked or answers no table.
func levelsOf(ctx context.Context, log reader) *table {
	out, err := rewardLevelsFn(cloud.For(ctx, cloud.Brand()))
	if err != nil {
		log.Warn("galaxy: the reward program's levels were not read; the seeded table stands", "err", err)
		return seeded()
	}
	t, ok := tableOf(out.Levels)
	if !ok {
		log.Warn("galaxy: the reward program's levels are no table; the seeded table stands")
		return seeded()
	}
	return t
}

// Returns every ranked org as a star, for a page to draw the community as a galaxy: each
// star holds its band in the reward program's level table, a coarse size (a factor of
// two), the star of the org that referred it, if any, and, only for the ranks the reader
// has earned a view of, its exact points and the name the standing shows for it. The
// window is the standing's: a signed-in reader sees their own star, five ranks below it
// and as many above as their points have earned; a reader who is not signed in sees the
// last ranks, where a newcomer starts. Every other star has `points` of -1 and no name,
// and is still drawn. The stars are columns of equal length, best first, and `center` is
// the tokens used and the orgs active in the last 24 hours. A star's id is opaque: it is
// the same wherever this answer mentions that star, names no org, and is replaced on the
// next read, about a minute later. A signed-in reader gets `self`, the id of their own
// star. Names are the standing's: masked in the middle, whole where the org listed
// itself, empty where it withdrew. No org id, handle or email is sent. No credential is
// needed to read it.
//
// Example: {}
func (o boardOps) galaxy(ctx context.Context, _ *cloud.Unit) (*GalaxyView, error) {
	noStore(ctx)
	g, err := galaxies.get(galaxyTTL, func() (*galaxyRead, error) { return readGalaxy(ctx, o.s.State.store, o.s.Log) })
	if err != nil {
		o.s.Log.Warn("galaxy sources unreadable; honest-empty", "err", err)
		return &GalaxyView{Bands: bandNames[:], Stars: GalaxyStars{ID: []string{}, Points: []int{}, Size: []int{}, Band: []int{}, Sponsor: []string{}, Mask: []string{}}}, nil
	}
	org, signed := principal.OrgFrom(ctx)
	view := g.window(org, signed)
	return &view, nil
}
