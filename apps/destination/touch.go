package destination

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/text"
)

// touch.go keeps the ad click a person arrived on, so a sale the server states
// later is credited to it.
//
// A browser event carries what its page captured: the click ids (gclid, gbraid,
// wbraid, fbclid and the _fbc built from it) and the Meta and Google browser ids
// (fbp, cid). A payment commerce states carries none of that; it knows the payer
// and the money. The touch is the join. The ad ids are kept only from an event
// whose visitor allowed Marketing.
//
// A touch keeps NO CONSENT. A choice stored from an earlier page can be withdrawn
// on one that never reaches here, so a sale is sent only under the choice its own
// checkout states (commerce stamps it on the sale), and one that states none
// reaches no platform. When an
// identified person's event carries any of it, it is recorded against (org,
// person); when a peer states a conversion for that person, the touch fills in
// what the payment could not know.
//
// THE MODEL is last click within the window. A new click replaces the old click
// set whole, so a conversion is credited to the most recent ad the person came
// through, and a click older than the window is not sent at all. Google Ads
// refuses a click conversion more than 90 days after its click, and Meta's
// longest click-through window is shorter than that, so 90 days is the longest
// window every platform can still act on. The platforms then run their own
// attribution over what arrives; this is only which click is offered to them.
//
// The email is kept as its SHA-256, never as the address.
//
// WHAT IS SEALED. Everything a touch holds about a visitor's browser is personal data,
// and the table is read by (org, person) alone: no predicate looks into a click id, a
// browser id, the agent, the page or the hashed email. So all twelve are sealed values
// (touchSealed), bound to their row (org, person), and nothing is sealed
// deterministically, since nothing looks a touch up by what it holds. A hashed email is
// still a stable identifier an ad platform matches on; sealed, it is not one a reader of
// the database can match against a list of addresses. What stays plain: org and person
// (the key every read and write names; person is an IAM subject or a wallet-prefixed
// payer, an identifier and not a name), the click time and the last-seen time (the
// prune and the eviction order by seen).

// lookback is the attribution window: how long after a click a conversion may
// still be credited to it. The one place it is set.
const lookback = 90 * 24 * time.Hour

// Touch is the browser state one person's conversions are matched with.
type Touch struct {
	Gclid, Gbraid, Wbraid, FBC string
	// Lifat, Twclid, Ttclid, Rdtcid and Oppref are the LinkedIn, X, TikTok, Reddit
	// and OpenAI click ids: what lets a sale the server states be credited to the ad.
	Lifat, Twclid, Ttclid, Rdtcid, Oppref string
	FBP, CID, UA, URL, Em                 string
	Clicked                               time.Time
}

func (t Touch) click() bool {
	return t.Gclid != "" || t.Gbraid != "" || t.Wbraid != "" || t.FBC != "" ||
		t.Lifat != "" || t.Twclid != "" || t.Ttclid != "" || t.Rdtcid != "" || t.Oppref != ""
}

// touchDDL is the touches table. (org, person) is the key because a person is
// matched inside the org whose destinations the conversion goes to.
const touchDDL = `
CREATE TABLE IF NOT EXISTS touches (
  org     TEXT NOT NULL,
  person  TEXT NOT NULL,
  gclid   TEXT NOT NULL DEFAULT '',
  gbraid  TEXT NOT NULL DEFAULT '',
  wbraid  TEXT NOT NULL DEFAULT '',
  fbc     TEXT NOT NULL DEFAULT '',
  lifat   TEXT NOT NULL DEFAULT '',
  twclid  TEXT NOT NULL DEFAULT '',
  ttclid  TEXT NOT NULL DEFAULT '',
  rdtcid  TEXT NOT NULL DEFAULT '',
  oppref  TEXT NOT NULL DEFAULT '',
  fbp     TEXT NOT NULL DEFAULT '',
  cid     TEXT NOT NULL DEFAULT '',
  ua      TEXT NOT NULL DEFAULT '',
  url     TEXT NOT NULL DEFAULT '',
  em      TEXT NOT NULL DEFAULT '',
  clicked BIGINT NOT NULL DEFAULT 0,
  seen    BIGINT NOT NULL,
  PRIMARY KEY (org, person)
);
CREATE INDEX IF NOT EXISTS ix_touches_seen ON touches(org, seen);
`

const touchCols = `gclid,gbraid,wbraid,fbc,lifat,twclid,ttclid,rdtcid,oppref,fbp,cid,ua,url,em,clicked`

// touchKey is the primary key a touch's sealed columns are bound to, and touchSealed the
// columns that are sealed, in the order of touchCols and of Touch.fields.
var (
	touchKey    = []string{"org", "person"}
	touchSealed = []string{"gclid", "gbraid", "wbraid", "fbc", "lifat", "twclid", "ttclid", "rdtcid", "oppref", "fbp", "cid", "ua", "url", "em"}
)

// fields are the sealed values of a touch, in the order of touchSealed.
func (t *Touch) fields() []*string {
	return []*string{&t.Gclid, &t.Gbraid, &t.Wbraid, &t.FBC, &t.Lifat, &t.Twclid, &t.Ttclid, &t.Rdtcid, &t.Oppref, &t.FBP, &t.CID, &t.UA, &t.URL, &t.Em}
}

// touchColumns are the columns added after the table first shipped, for a table that predates them.
var touchColumns = []string{"lifat", "twclid", "ttclid", "rdtcid", "oppref"}

// touch reads the person's touch; found=false when none was recorded.
func (s *Store) touch(ctx context.Context, org, person string) (Touch, bool, error) {
	var (
		t       Touch
		clicked int64
	)
	dest := []any{}
	for _, f := range t.fields() {
		dest = append(dest, f)
	}
	err := s.db.QueryRowContext(ctx, `SELECT `+touchCols+` FROM touches WHERE org=$1 AND person=$2`, org, person).
		Scan(append(dest, &clicked)...)
	if errors.Is(err, sql.ErrNoRows) {
		return Touch{}, false, nil
	}
	if err != nil {
		return Touch{}, false, fmt.Errorf("read touch: %w", err)
	}
	// Open what the row delivered, bound to the (org, person) the query named.
	r := s.fields.Row("touches", org, person)
	for i, f := range t.fields() {
		*f = r.Open(touchSealed[i], *f)
	}
	if err := r.Err(); err != nil {
		return Touch{}, false, fmt.Errorf("read touch: %w", err)
	}
	if clicked > 0 {
		t.Clicked = time.Unix(clicked, 0)
	}
	return t, true, nil
}

// keep writes the person's touch.
func (s *Store) keep(ctx context.Context, org, person string, t Touch) error {
	var clicked int64
	if !t.Clicked.IsZero() {
		clicked = t.Clicked.Unix()
	}
	r := s.fields.Row("touches", org, person)
	args := []any{org, person}
	for i, f := range t.fields() {
		args = append(args, r.Seal(touchSealed[i], *f))
	}
	if err := r.Err(); err != nil {
		return fmt.Errorf("keep touch: %w", err)
	}
	args = append(args, clicked, time.Now().Unix())
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO touches (org,person,`+touchCols+`,seen) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
		 ON CONFLICT(org,person) DO UPDATE SET
		   gclid=excluded.gclid, gbraid=excluded.gbraid, wbraid=excluded.wbraid, fbc=excluded.fbc,
		   lifat=excluded.lifat, twclid=excluded.twclid, ttclid=excluded.ttclid, rdtcid=excluded.rdtcid, oppref=excluded.oppref,
		   fbp=excluded.fbp, cid=excluded.cid, ua=excluded.ua, url=excluded.url, em=excluded.em,
		   clicked=excluded.clicked, seen=excluded.seen`, args...)
	if err != nil {
		return fmt.Errorf("keep touch: %w", err)
	}
	return nil
}

// A browser writes a touch, so what it may put there is bounded: each field
// clipped (an id is short; a page and an agent get more room) and each org held
// to maxTouches people, past which a new person is not recorded.
const (
	maxID      = 256
	maxLong    = 1024
	maxTouches = 100_000
)

func clip(s string, n int) string { return text.Trim(s, n) }

// touchOf is what one conversion says about its person's browser.
func touchOf(cv Conversion) Touch {
	u := cv.User
	t := Touch{CID: clip(u.ClientID, maxID), UA: clip(u.UserAgent, maxLong)}
	// The ad click, the platforms' browser ids and the hashed email are an ad
	// platform's, kept only from a visitor who allowed Marketing.
	if cv.Consent.Marketing {
		t.Gclid, t.Gbraid, t.Wbraid = clip(u.click("gclid"), maxID), clip(u.click("gbraid"), maxID), clip(u.click("wbraid"), maxID)
		t.Lifat, t.Twclid, t.Ttclid = clip(u.click("li_fat_id"), maxID), clip(u.click("twclid"), maxID), clip(u.click("ttclid"), maxID)
		t.Rdtcid, t.Oppref = clip(u.click("rdt_cid"), maxID), clip(u.click("oppref"), maxID)
		t.FBC, t.FBP, t.Em, t.Clicked = clip(fbcOf(u), maxID), clip(u.FBP, maxID), clip(u.em(), maxID), u.Clicked
	}
	// The page a conversion happened on is where a later sale is said to have
	// started. Every other page would move it on each view.
	if cv.Standard != EventCustom && cv.Standard != EventPageView {
		t.URL = clip(cv.URL, maxLong)
	}
	return t
}

// merge folds what an event said into what was kept. A new click replaces the
// whole click set, which is the last-click model; every other field is replaced
// only by a value, so an event that did not carry one keeps the last.
func merge(old, now Touch) Touch {
	out := old
	// The later click wins, by when it happened and not by when it arrived: a
	// redelivered or late event must not put an older click back.
	if now.click() && !now.Clicked.Before(old.Clicked) {
		out.Gclid, out.Gbraid, out.Wbraid, out.FBC, out.Clicked = now.Gclid, now.Gbraid, now.Wbraid, now.FBC, now.Clicked
		out.Lifat, out.Twclid, out.Ttclid, out.Rdtcid, out.Oppref = now.Lifat, now.Twclid, now.Ttclid, now.Rdtcid, now.Oppref
	}
	for _, f := range []struct {
		dst *string
		v   string
	}{
		{&out.FBP, now.FBP}, {&out.CID, now.CID}, {&out.UA, now.UA}, {&out.URL, now.URL}, {&out.Em, now.Em},
	} {
		if f.v != "" {
			*f.dst = f.v
		}
	}
	return out
}

// fill completes a peer-stated conversion from its person's touch: the click and
// the browser ids the payment could not carry. A field the conversion already has
// is left alone, and its consent is only ever its own.
func fill(cv *Conversion, t Touch) {
	u := &cv.User
	if len(u.Clicks) == 0 && t.click() {
		u.Clicks = map[string]string{}
		for k, v := range map[string]string{
			"gclid": t.Gclid, "gbraid": t.Gbraid, "wbraid": t.Wbraid,
			"li_fat_id": t.Lifat, "twclid": t.Twclid, "ttclid": t.Ttclid,
			"rdt_cid": t.Rdtcid, "oppref": t.Oppref,
		} {
			if v != "" {
				u.Clicks[k] = v
			}
		}
		u.FBC, u.Clicked = t.FBC, t.Clicked
	}
	u.FBP = pick(u.FBP, t.FBP)
	u.ClientID = pick(u.ClientID, t.CID)
	u.UserAgent = pick(u.UserAgent, t.UA)
	if u.Email == "" {
		u.Hashed = pick(u.Hashed, t.Em)
	}
	cv.URL = pick(cv.URL, t.URL)
}

func pick(have, kept string) string {
	if have != "" {
		return have
	}
	return kept
}

// stale drops a click older than the lookback. A click with no stated time came
// with this very event, so it is as fresh as the event.
func stale(cv *Conversion) {
	u := &cv.User
	if u.Clicked.IsZero() || cv.Time.IsZero() || cv.Time.Sub(u.Clicked) <= lookback {
		return
	}
	u.Clicks, u.FBC, u.Clicked = nil, "", time.Time{}
}

// wallet prefixes the key a buyer's touch is kept under for the wallet that
// paid. No browser event may write under it: a wallet name is guessable, a
// person's IAM subject is not.
const wallet = "payer:"

// recall records or reads the person's touch for one conversion. An event from
// a browser records what it carried; a conversion a peer stated reads it back.
// Only identified people have a touch, and an event carrying nothing a touch
// holds touches nothing, so ordinary traffic never reaches the store.
func (s *Store) recall(ctx context.Context, org string, cv *Conversion) error {
	if cv.Source == sourcePlane {
		return s.credit(ctx, org, cv)
	}
	if cv.Person == "" || len(cv.Person) > maxID || strings.HasPrefix(cv.Person, wallet) {
		return nil
	}
	now := touchOf(*cv)
	if !now.click() && now.FBP == "" && now.CID == "" && now.Em == "" {
		return nil
	}
	old, found, err := s.touch(ctx, org, cv.Person)
	if err != nil {
		return err
	}
	next := merge(old, now)
	if found && next == old {
		return nil
	}
	if !found {
		if err := s.room(ctx, org); err != nil {
			return err
		}
	}
	return s.keep(ctx, org, cv.Person, next)
}

// room makes room for one more person in org: past maxTouches the one seen
// longest ago goes, so a flood of invented people can push out the stalest
// touches but never shuts out a real visitor arriving now.
func (s *Store) room(ctx context.Context, org string) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM touches WHERE org=$1`, org).Scan(&n); err != nil {
		return fmt.Errorf("count touches: %w", err)
	}
	if n < maxTouches {
		return nil
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM touches WHERE org=$1 AND person=(SELECT person FROM touches WHERE org=$2 ORDER BY seen LIMIT 1)`, org, org)
	if err != nil {
		return fmt.Errorf("evict touch: %w", err)
	}
	return nil
}

// credit completes a peer-stated conversion from the buyer's touch: under the
// person when the sale names one, else under the wallet that paid. A sale that
// names both keeps the touch under the wallet too, because a renewal later
// names only the wallet.
func (s *Store) credit(ctx context.Context, org string, cv *Conversion) error {
	payer := strProp(cv.Properties, "payer")
	if len(cv.Person) > maxID || len(payer) > maxID {
		return nil
	}
	key := cv.Person
	if key == "" {
		if payer == "" {
			return nil
		}
		key = wallet + payer
	}
	t, found, err := s.touch(ctx, org, key)
	if err != nil || !found {
		return err
	}
	fill(cv, t)
	if cv.Person != "" && payer != "" {
		return s.keep(ctx, org, wallet+payer, t)
	}
	return nil
}

// prune forgets touches nobody has been seen with for longer than a consent
// lasts (13 months): past that the click is long stale and the choice expired.
func (s *Store) prune(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM touches WHERE seen < $1`, time.Now().Add(-395*24*time.Hour).Unix())
	if err != nil {
		return fmt.Errorf("prune touches: %w", err)
	}
	return nil
}

// sourcePlane is the wire a peer app states an occurrence on (apps/event).
const sourcePlane = "plane"

// fbcOf is the Meta click cookie: the one the page kept, else built from the
// fbclid with the time the click was captured, which is what Meta reads the
// creation time out of. Built with the send time instead, every conversion
// would claim its click happened at the moment it was sent.
func fbcOf(u UserData) string {
	if u.FBC != "" {
		return u.FBC
	}
	id := u.click("fbclid")
	if id == "" {
		return ""
	}
	at := u.Clicked
	if at.IsZero() {
		at = time.Now()
	}
	return fmt.Sprintf("fb.1.%d.%s", at.UnixMilli(), strings.TrimSpace(id))
}
