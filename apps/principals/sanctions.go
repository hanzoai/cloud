package principals

// sanctions.go — every name and address an org is known by, screened against the
// published lists, in this process.
//
// The lists are luxfi/aml's: pkg/screen fetches the OFAC SDN, UN consolidated, EU
// consolidated and UK OFSI files from their publishers, pkg/sanctions parses and
// matches them, and this package uses both as a library and copies neither. They
// are kept fresh the way that package's own daemon keeps them (cmd/amld): one load
// when the process first needs them and one a day after, every publisher retried, a
// publisher that fails keeping its previous load, and the WHOLE set refused once
// any list is older than 48 hours or has never loaded — "screening against three
// of four lists is not three-quarters of a control, because a person designated
// only on the missing list passes completely" (pkg/screen, Readiness.Unfit).
//
// Only the publishers are fetched. Nothing an org is known by leaves this process
// to be screened: the match runs here, over the loaded entries.
//
// A NAME is matched fuzzily, at luxfi/aml's threshold, and a match is a POSSIBLE
// one: a namesake is expected, and it is resolved by a person reading the other
// identifiers. That person is a Hanzo platform reviewer — SuperAdmin — and never
// an admin of the org the match names, which would be the org deciding its own
// case. An ADDRESS is matched exactly, and a designated digital currency address
// is a hit, not a namesake: it is recorded confirmed, and a reviewer may still
// clear it with a reason on the trail.
//
// What a hit means is the law's, not this package's: "U.S. persons are generally
// prohibited from dealing with the individuals and entities on the SDN List"
// (OFAC, Basic Information on OFAC and Sanctions, FAQ 91); "No funds or economic
// resources shall be made available, directly or indirectly, to or for the benefit
// of" a person listed (Council Regulation (EU) No 269/2014, Art. 2(2)); "A person
// ('P') must not make funds available directly or indirectly to a designated
// person" (Russia (Sanctions) (EU Exit) Regulations 2019, SI 2019/855, reg. 12(1)).

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/sdk/iam"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// published is where the lists come from: luxfi/aml's own fetch of the four
// publishers. It is a value so a test can hand the process the entries it
// screens against instead of the network.
var published = defaultPublished

// listStale is how old the newest load of a list may be before screening refuses
// rather than answers: the daily refresh twice missed. It is luxfi/aml's own
// bound (cmd/amld, listStale).
const listStale = 48 * time.Hour

// refreshEvery is the daily refresh, luxfi/aml's cadence (cmd/amld registers the
// load daily).
const refreshEvery = 24 * time.Hour

// lists are the loaded sanctions lists and the refresh that keeps them fresh.
type lists struct {
	store Matcher
	ready *screenReadiness
	fetch func(context.Context) []ScreenResult
	log   luxlog.Logger
	once  sync.Once
	stop  chan struct{}
	done  sync.WaitGroup
}

func newLists(fetch func(context.Context) []ScreenResult, log luxlog.Logger) *lists {
	return &lists{
		store: newScreenMatcher(listStale),
		ready: newScreenReadiness(screenSources),
		fetch: fetch,
		log:   log,
		stop:  make(chan struct{}),
	}
}

// start loads the lists now and every day after, until close. It runs the first
// time screening is asked for, never at mount: a process that only describes the
// surface, or a test that mounts it, reaches no publisher. The first load is the
// caller's to wait for (fit runs this under its Once, so every screening that
// arrives meanwhile waits with it), because an answer given before it would be
// made on no list at all; the daily refresh is a goroutine.
func (l *lists) start() {
	l.load()
	l.done.Add(1)
	go func() {
		defer l.done.Done()
		t := time.NewTicker(refreshEvery)
		defer t.Stop()
		for {
			select {
			case <-l.stop:
				return
			case <-t.C:
			}
			l.load()
		}
	}()
}

// close stops the refresh, cancels a load in flight, and keeps one from starting after it.
func (l *lists) close() {
	select {
	case <-l.stop:
	default:
		close(l.stop)
	}
	l.once.Do(func() {})
	l.done.Wait()
}

// load takes every publisher once. A publisher that fails keeps what it loaded
// last, and the failure is recorded on its readiness rather than read as a
// publisher that designates nobody.
func (l *lists) load() {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-l.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	defer cancel()
	results := l.fetch(ctx)
	for i := range results {
		r := &results[i]
		if r.Err != nil {
			l.log.Error("sanctions list did not load", "source", r.Source, "err", r.Err)
			continue
		}
		if err := l.store.Load(r.Source, r.Entries); err != nil {
			r.Err = err
			l.log.Error("sanctions list refused", "source", r.Source, "err", err)
			continue
		}
		l.log.Info("sanctions list loaded", "source", r.Source, "designations", len(r.Entries))
	}
	l.ready.Record(results, time.Now().UTC())
}

// fit says why screening cannot be relied on, if it cannot: a list never loaded,
// or older than listStale, fails the whole set.
func (l *lists) fit() error {
	l.once.Do(l.start)
	if unfit := l.ready.Unfit(time.Now().UTC(), listStale); len(unfit) > 0 {
		return fmt.Errorf("the %s list is not loaded or is more than %s old", strings.Join(unfit, ", "), listStale)
	}
	return l.store.Ready()
}

// Subject is one name an org is known by, and where the name came from.
type Subject struct {
	// Name is the name as the org stated it.
	Name string `json:"name"`
	// Role is where it came from: entity (the formed company), founder (a
	// beneficial owner the formation identified), or form (the name on its tax
	// form).
	Role string `json:"role"`
	// Kind is individual or entity; a name is screened only against list entries
	// of its own kind.
	Kind string `json:"kind"`
	// Birth is an individual's date of birth, when the org stated one: the
	// identifier that clears a namesake.
	Birth string `json:"-"`
}

// Place is a country an org is tied to by its own statement.
type Place struct {
	// Country is ISO 3166-1 alpha-2.
	Country string
	// Role is how: citizenship (W-8BEN line 2), organization (W-8BEN-E line 2) or
	// residence (a W-8's permanent residence address).
	Role string
}

// known is everything an org is known by: its names, its addresses, and the
// countries it is tied to.
type known struct {
	subjects  []Subject
	addresses []iam.WalletBrief
	places    []Place
}

// none reports that the org states nothing to screen.
func (k known) none() bool {
	return len(k.subjects) == 0 && len(k.addresses) == 0 && len(k.places) == 0
}

// Jurisdiction is a place under a comprehensive embargo.
type Jurisdiction struct {
	// Name is the jurisdiction as the program names it.
	Name string
	// Law is the program.
	Law string
}

// embargoed are the jurisdictions under comprehensive OFAC embargoes, keyed by
// the ISO 3166-1 code of the country they are in (OFAC, Sanctions Programs and
// Country Information, read 2026-09-24). A tie to one of them is a possible
// match a platform reviewer decides, never a clear: a general license can
// authorize a payment, and only a person reading the facts can say one does.
//
// Ukraine is here because three of its regions are — Crimea (EO 13685) and the
// so-called Donetsk and Luhansk People's Republics (EO 14065), and under EU and UK
// law the non-government-controlled areas of Kherson and Zaporizhzhia as well
// (Council Regulation (EU) 2022/263; SI 2019/855, Part 5) — and a country code
// cannot say whether an address is in one; the reviewer reads the address.
var embargoed = map[string]Jurisdiction{
	"CU": {Name: "Cuba", Law: "Cuban Assets Control Regulations, 31 CFR part 515"},
	"IR": {Name: "Iran", Law: "Iranian Transactions and Sanctions Regulations, 31 CFR part 560"},
	"KP": {Name: "North Korea", Law: "North Korea Sanctions Regulations, 31 CFR part 510"},
	"UA": {Name: "Ukraine: Crimea and the so-called Donetsk and Luhansk People's Republic regions",
		Law: "Executive Orders 13685 and 14065; Ukraine-/Russia-Related Sanctions Regulations, 31 CFR part 589"},
}

const (
	matchOpen      = "open"
	matchCleared   = "cleared"
	matchConfirmed = "confirmed"
)

const (
	screenClear       = "clear"
	screenReview      = "review"
	screenBlocked     = "blocked"
	screenUnscreened  = "unscreened"
	screenUnavailable = "unavailable"
)

// sightings screens what an org is known by against the lists and the embargoed
// jurisdictions, in this process, and answers what matched — or why the lists
// could not be screened against, which is never read as nothing matched.
func (l *lists) sightings(org string, k known) ([]sighting, error) {
	if err := l.fit(); err != nil {
		return nil, err
	}
	var out []sighting
	for _, s := range k.subjects {
		q := ScreenQuery{Name: s.Name, Kind: s.Kind}
		if s.Birth != "" {
			q.Birth = ScreenBirth{From: s.Birth, To: s.Birth}
		}
		ms, err := l.store.Search(q, MatchThreshold)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			out = append(out, listed(org, iam.Hit{Subject: s.Name, Role: s.Role, Kind: hitName}, m))
		}
	}
	for _, w := range k.addresses {
		if strings.TrimSpace(w.Address) == "" {
			continue
		}
		ms, err := l.store.Chain(w.Address)
		if err != nil {
			return nil, err
		}
		for _, m := range ms {
			out = append(out, listed(org, iam.Hit{Subject: w.Address, Role: "wallet", Kind: hitAddress}, m))
		}
	}
	for _, p := range k.places {
		if j, ok := embargoed[p.Country]; ok {
			out = append(out, place(org, p, j))
		}
	}
	return out, nil
}

// screen answers one org's verdict: its stored matches with what screening finds
// now. write records what screening found — a new match, a new hit, a reopened
// namesake — each on the trail in the transaction that writes it; a read (write
// false) records nothing and answers the verdict those records would make.
func (o ops) screen(ctx context.Context, org string, k known, write bool) (iam.Screening, error) {
	l := o.s.State.lists
	sc := iam.Screening{Lists: listsOf(l.ready.Lists()), At: time.Now().Unix()}
	seen, unfit := l.sightings(org, k)
	st := o.s.State.store
	var rows []matchRow
	if !write || len(seen) == 0 {
		stored, err := st.ofOrg(ctx, st.db, org)
		if err != nil {
			return iam.Screening{}, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
		}
		rows, _, _ = fold(stored, seen, sc.At)
	} else {
		err := o.commit(ctx, func(tx *sql.Tx) ([]audit.Record, error) {
			stored, err := st.ofOrg(ctx, tx, org)
			if err != nil {
				return nil, err
			}
			var changed []int
			var what []string
			rows, changed, what = fold(stored, seen, sc.At)
			var recs []audit.Record
			for n, i := range changed {
				r := rows[i]
				if err := st.putMatch(ctx, tx, r); err != nil {
					return nil, err
				}
				rec, err := o.record(ctx, org, "principal.match."+what[n], audit.Resource{Type: "principal.match", ID: r.id},
					map[string]any{"org": org, "list": r.list, "ref": r.ref, "status": r.status, "hits": len(r.hits), "score": r.score}, false)
				if err != nil {
					return nil, err
				}
				recs = append(recs, rec)
			}
			return recs, nil
		})
		if err != nil {
			return iam.Screening{}, err
		}
	}
	for i := range rows {
		sc.Matches = append(sc.Matches, matchOf(&rows[i]))
	}
	sc.Status, sc.Reason = verdict(rows, unfit, k.none())
	return sc, nil
}

// listsOf is each list's readiness in the shape a screening answers it.
func listsOf(fs []ScreenFitness) []iam.List {
	out := make([]iam.List, 0, len(fs))
	for _, f := range fs {
		out = append(out, iam.List(f))
	}
	return out
}

// verdict is an org's status. A confirmed match blocks whatever else is true —
// it is a decision about the org, and neither a rename nor a list that did not
// load undoes it. Otherwise lists unfit to screen against leave the org
// unscreened by them, an open match awaits a reviewer, an org stating nothing
// cannot be screened, and anything else is clear.
func verdict(rows []matchRow, unfit error, none bool) (string, string) {
	switch {
	case slices.ContainsFunc(rows, func(r matchRow) bool { return r.status == matchConfirmed }):
		return screenBlocked, "Something this org is known by is a confirmed match to a designated person or an embargoed jurisdiction. No payment " +
			"may be made to or from it: U.S. persons are generally prohibited from dealing with SDNs (OFAC FAQ 91), and funds may not be made " +
			"available to a listed person (Council Regulation (EU) No 269/2014, Art. 2(2); SI 2019/855, reg. 12(1))."
	case unfit != nil:
		return screenUnavailable, "Screening could not run: " + unfit.Error() +
			". An answer from a partial or stale set would read as clear for an org designated only on what is missing."
	case slices.ContainsFunc(rows, func(r matchRow) bool { return r.status == matchOpen }):
		return screenReview, "Something this org is known by is a possible match to a designated person or ties it to an embargoed " +
			"jurisdiction, awaiting a Hanzo platform reviewer. No payment clears until it is decided; it is put before a reviewer when a " +
			"payment to or from this org is cleared."
	case none:
		return screenUnscreened, "The org states nothing to screen: it has no formation, no tax form and no wallet."
	}
	return screenClear, "Nothing this org is known by matches a designation on the current OFAC, UN, EU or UK lists or ties it to an " +
		"embargoed jurisdiction, other than any a platform reviewer has cleared as a namesake."
}

// matchID is the org and the designation together: a decision is about whether
// this org is that person, whatever name, address or country brought them close.
func matchID(org, list, ref string) string {
	sum := sha256.Sum256([]byte(org + "\x00" + list + "\x00" + ref))
	return "hit_" + hex.EncodeToString(sum[:12])
}

// listed is a luxfi/aml match as this package records it. An address is a key —
// the designated one, or somebody else's money — so there is no namesake to
// resolve, and it is born confirmed; a reviewer may still clear it with a reason
// on the trail.
func listed(org string, h iam.Hit, m ScreenMatch) sighting {
	now := time.Now().Unix()
	r := matchRow{
		id: matchID(org, m.Entry.List, m.Entry.RefID), org: org, list: m.Entry.List, ref: m.Entry.RefID,
		entry: m.Entry.PrimaryName(), hits: []iam.Hit{h}, score: m.Score, reason: reasonOf(m), status: matchOpen,
		found: now, seen: now,
	}
	if h.Kind == hitAddress {
		r.status, r.decidedBy, r.decidedAt = matchConfirmed, byAddress, now
	}
	return sighting{row: r, hit: h}
}

// place is a tie to an embargoed jurisdiction as this package records it: open,
// for a reviewer.
func place(org string, p Place, j Jurisdiction) sighting {
	now := time.Now().Unix()
	h := iam.Hit{Subject: p.Country, Role: p.Role, Kind: hitJurisdiction}
	return sighting{hit: h, row: matchRow{
		id: matchID(org, hitJurisdiction, p.Country), org: org, list: hitJurisdiction, ref: p.Country, entry: j.Name,
		hits: []iam.Hit{h}, score: 1, status: matchOpen, found: now, seen: now,
		reason: fmt.Sprintf("the org's %s is %s: %s, under %s", p.Role, p.Country, j.Name, j.Law),
	}}
}

func reasonOf(m ScreenMatch) string {
	s := fmt.Sprintf("%s matched %q (%s) on the %s list", m.Reason, m.Name.Full, m.Name.Type, strings.ToUpper(m.Entry.List))
	if len(m.Agree) > 0 {
		s += "; agrees on " + strings.Join(m.Agree, ", ")
	}
	if len(m.Conflict) > 0 {
		s += "; conflicts on " + strings.Join(m.Conflict, ", ")
	}
	return s
}

func matchOf(r *matchRow) iam.Match {
	return iam.Match{ID: r.id, Org: r.org, List: r.list, Ref: r.ref, Entry: r.entry, Hits: slices.Clone(r.hits), Score: r.score,
		Reason: r.reason, Status: r.status, DecidedBy: r.decidedBy, DecidedAt: r.decidedAt, Note: r.note, FoundAt: r.found}
}

// matchesIn filters the review queue.
type matchesIn struct {
	// Status is open, cleared or confirmed; open when omitted. "all" lists every
	// match.
	Status string `json:"status,omitempty" query:"status"`
}

// matchList is a page of the review queue.
type matchList struct {
	Matches []iam.Match `json:"matches"`
}

// Lists sanctions matches across every org, oldest first — the platform's review
// queue. SuperAdmin only: a match names an org the platform screened, and the
// org it names is not the one to decide it.
//
// A match is one org against one designation or embargoed jurisdiction, with
// every name, address or country of the org that brought them close. Open
// matches are the ones no payment to or from their org clears past until a
// reviewer decides them; a match is recorded when a payment to or from its org is
// cleared.
func (o ops) matches(ctx context.Context, in *matchesIn) (*matchList, error) {
	if _, err := o.reviewer(ctx); err != nil {
		return nil, err
	}
	status := in.Status
	switch status {
	case "":
		status = matchOpen
	case "all":
		status = ""
	case matchOpen, matchCleared, matchConfirmed:
	default:
		return nil, zip.ErrBadRequest("status is open, cleared, confirmed or all")
	}
	rows, err := o.s.State.store.queue(ctx, status, 500)
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	out := &matchList{Matches: []iam.Match{}}
	for i := range rows {
		out.Matches = append(out.Matches, matchOf(&rows[i]))
	}
	noStore(ctx)
	return out, nil
}

// reviewer admits SuperAdmin to read the queue; decisions go through super.
func (o ops) reviewer(ctx context.Context) (string, error) {
	org, err := o.member(ctx)
	if err != nil {
		return "", err
	}
	if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return "", zip.ErrForbidden("the sanctions review queue is a Hanzo platform reviewer's (SuperAdmin); an org admin may not read it")
	}
	return org, nil
}

// decideIn is a reviewer's decision on one match.
type decideIn struct {
	// ID is the match, from the path.
	ID string `json:"id"`
	// Decision is cleared (a namesake: the identifiers show a different person) or
	// confirmed (the designated person).
	Decision string `json:"decision"`
	// Note is the reviewer's reason — which identifiers decided it. Required.
	Note string `json:"note"`
}

// Decides one sanctions match, as a Hanzo platform reviewer. SuperAdmin only —
// an admin of the org the match names is refused, because that would be the org
// deciding its own case — and the decision is written and put on the audit trail
// in one transaction: a decision the trail does not take is not made.
//
// cleared records a namesake: the org's names resemble a designated person's and
// the other identifiers show a different one. It covers the hits the reviewer
// read, and a later name close to the same person reopens it. confirmed records
// the designated person itself, and blocks every payment to or from the org
// whatever it is later called. A decision may be changed only by another
// decision, with its own note.
//
// Example: {"decision": "cleared", "note": "date of birth 1984-02-11 conflicts with the entry's 1961"}
func (o ops) decide(ctx context.Context, in *decideIn) (*iam.Match, error) {
	org, err := o.super(ctx, "decide a sanctions match")
	if err != nil {
		return nil, err
	}
	note := clean(in.Note)
	switch {
	case in.Decision != matchCleared && in.Decision != matchConfirmed:
		return nil, zip.ErrBadRequest("decision is cleared or confirmed")
	case note == "":
		return nil, zip.ErrBadRequest("note is required: a decision states which identifiers decided it")
	case len(note) > 2000:
		return nil, zip.ErrBadRequest("note is at most 2000 characters")
	}
	st := o.s.State.store
	m, err := st.match(ctx, st.db, strings.TrimSpace(in.ID))
	if err == errNotFound {
		return nil, zip.ErrNotFound("match not found")
	}
	if err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", err)
	}
	by, at := user(ctx), time.Now().Unix()
	err = o.commit(ctx, func(tx *sql.Tx) ([]audit.Record, error) {
		switch err := st.decide(ctx, tx, m.id, m.status, in.Decision, by, note, at); {
		case errors.Is(err, errMoved):
			return nil, zip.Errorf(http.StatusConflict, "the match changed while this ran; read it and decide again")
		case err != nil:
			return nil, err
		}
		rec, err := o.record(ctx, org, "principal.match.decided", audit.Resource{Type: "principal.match", ID: m.id},
			map[string]any{"org": m.org, "list": m.list, "ref": m.ref, "from": m.status, "to": in.Decision, "hits": len(m.hits)}, true)
		return []audit.Record{rec}, err
	})
	if err != nil {
		return nil, err
	}
	m.status, m.decidedBy, m.decidedAt, m.note = in.Decision, by, at, note
	out := matchOf(m)
	return &out, nil
}
