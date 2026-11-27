package marketplace

// expert.go — a person who makes their time available: to configure Hanzo, or to
// build with the Hanzo stack.
//
// An expert is a listing like any other, of kind expert, and is booked like any
// other: through a job (jobs.go), paid over x402, accepted, delivered, released,
// disputed, refunded and reviewed by the steps that already exist. What differs
// is what is sold. Every other kind is a thing the seller's org owns; an expert
// is a person, so the proof of ownership is the person: the listing is published
// by the verified principal it names, and is theirs to edit and withdraw.
//
//   - Price is the fixed engagement price, as for every kind; Rate is the hourly
//     rate. An expert sets one or both.
//   - Areas are what the expert works on, from a closed list.
//   - Verified is staff's word, and only a SuperAdmin gives it (Verify), on the
//     trail. An org admin, and the expert, cannot.
//
// The rewards program reads what experts do from the event plane: expert.listed,
// expert.verified, expert.job.delivered and expert.review, stated here after the
// act they describe has been made (events below).

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/hanzoai/cloud/client"
	eventpeer "github.com/hanzoai/cloud/client/event"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// Areas is what an expert works on, in the order the shop shows them: configuring
// Hanzo, building with Base, Commerce, Bot, Agents or Cloud, the models (Kai, Enso
// and Zen), creative AI, and freelance work for partners and companies building
// on Hanzo Cloud and the Hanzo OSS ecosystem.
var Areas = []string{"configure", "base", "commerce", "bot", "agents", "cloud", "models", "creative", "freelance"}

const (
	maxAvailability = 512
	maxLanguages    = 8
	// maxSkills and maxSkill bound the skills an expert names and the length of
	// each, in characters; maxLinks and maxLink the links and each one's length.
	maxSkills = 20
	maxSkill  = 40
	maxLinks  = 8
	maxLink   = 200
)

// language is a lowercase BCP 47 tag: "en", "es", "zh-cn".
var language = regexp.MustCompile(`^[a-z]{2,3}(-[a-z0-9]{2,8})*$`)

// Expert is the person behind a listing of kind expert.
type Expert struct {
	// Person is the expert's IAM identity: the verified principal that published
	// the listing, never taken off the wire. Only the expert's own org reads it.
	Person string `json:"person,omitempty"`
	// Rate is the hourly rate, exact to 18 decimal places of USD; "0" when the
	// expert sells only a fixed engagement (the listing's price).
	Rate money.Amount `json:"rate"`
	// Areas is what the expert works on: configure, base, commerce, bot, agents,
	// cloud, models, creative or freelance.
	Areas []string `json:"areas"`
	// Languages are the languages the expert works in, lowercase BCP 47 tags.
	Languages []string `json:"languages"`
	// Timezone is the expert's IANA time zone, "America/Los_Angeles".
	Timezone string `json:"timezone"`
	// Availability is the expert's weekly hours, in their own words.
	Availability string `json:"availability"`
	// Skills are what the expert is good at, in their own words: up to 20 names of
	// up to 40 characters, each once whatever its case.
	Skills []string `json:"skills"`
	// Links are where the expert's work can be seen: up to 8 https URLs.
	Links []string `json:"links"`
	// Verified is whether staff vouch for the expert. Only a SuperAdmin sets it.
	Verified bool `json:"verified"`
	// Reward is the expert's standing in the rewards program, present only when
	// they made their reward profile public (reward.go).
	Reward *Reward `json:"reward,omitempty"`
	// Products are the products the expert scores highest in, at most three, best
	// first, each with the parts its score is made of; absent until they score in
	// any (expertise.go).
	Products []Score `json:"products,omitempty"`
	// Top names the products in which the expert ranks among the best three of all
	// experts, in the order of the product list.
	Top []string `json:"top,omitempty"`
	// Self is whether the expert is the caller. It is set on the caller's own
	// org's listings and nowhere else, so a client finds its own listing without
	// knowing how an identity is spelled.
	Self bool `json:"self,omitempty"`
}

// expertIn is what an expert says of themself when they publish or edit. On an
// edit it replaces the whole profile.
type expertIn struct {
	// Rate is the hourly rate as a decimal USD string, exact to 18 places; empty
	// or "0" sells no hours.
	Rate string `json:"rate"`
	// Areas is what the expert works on, one or more of configure, base,
	// commerce, bot, agents, cloud, models, creative and freelance.
	Areas []string `json:"areas"`
	// Languages are 1 to 8 lowercase BCP 47 tags: "en", "es", "zh-cn".
	Languages []string `json:"languages"`
	// Timezone is an IANA time zone: "America/Los_Angeles".
	Timezone string `json:"timezone"`
	// Availability is the weekly hours in free form, at most 512 characters.
	Availability string `json:"availability"`
	// Skills are 0 to 20 names of 1 to 40 characters, trimmed; a name already said,
	// in any case, is kept once.
	Skills []string `json:"skills"`
	// Links are 0 to 8 https URLs of at most 200 characters, with a host and no
	// login in them.
	Links []string `json:"links"`
}

// apply sets the profile from what the expert said, refusing what the shop could
// not show: an area outside the list, a language that is not a tag, a time zone
// that is not one, hours that are not said. The person and the mark are not the
// expert's to set and are kept.
func (e *Expert) apply(in *expertIn) error {
	if in == nil {
		return zip.ErrBadRequest("an expert listing carries expert: rate, areas, languages, timezone and availability")
	}
	rate, err := money.ParseUSD(strings.TrimSpace(in.Rate))
	if err != nil || rate.IsNeg() {
		return zip.ErrBadRequest("rate must be a decimal USD amount of 0 or more with at most 18 decimals")
	}
	areas := unique(in.Areas)
	if len(areas) == 0 {
		return zip.ErrBadRequest("areas is one or more of " + strings.Join(Areas, ", "))
	}
	for _, a := range areas {
		if !slices.Contains(Areas, a) {
			return zip.ErrBadRequest("areas is one or more of " + strings.Join(Areas, ", "))
		}
	}
	langs := unique(in.Languages)
	if len(langs) == 0 || len(langs) > maxLanguages {
		return zip.ErrBadRequest("languages is 1 to 8 tags such as en, es or zh-cn")
	}
	for _, l := range langs {
		if !language.MatchString(l) {
			return zip.ErrBadRequest("languages is 1 to 8 tags such as en, es or zh-cn")
		}
	}
	tz := strings.TrimSpace(in.Timezone)
	if _, err := time.LoadLocation(tz); err != nil || tz == "" || tz == "Local" || len(tz) > maxName {
		return zip.ErrBadRequest("timezone is an IANA time zone such as America/Los_Angeles")
	}
	hours := strings.TrimSpace(in.Availability)
	if hours == "" || len(hours) > maxAvailability {
		return zip.ErrBadRequest("availability is the weekly hours, at most 512 characters")
	}
	skills, err := skillsOf(in.Skills)
	if err != nil {
		return err
	}
	links, err := linksOf(in.Links)
	if err != nil {
		return err
	}
	// Areas keep the order of the list, so the same set reads the same everywhere.
	slices.SortFunc(areas, func(a, b string) int { return cmp.Compare(slices.Index(Areas, a), slices.Index(Areas, b)) })
	e.Rate, e.Areas, e.Languages, e.Timezone, e.Availability = rate, areas, langs, tz, hours
	e.Skills, e.Links = skills, links
	return nil
}

// skillsOf is the skills an expert named, trimmed, blank ones dropped and each name
// kept once by its first spelling, refusing a name that is too long, one with a
// control character in it, and more than maxSkills.
func skillsOf(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			continue
		}
		if utf8.RuneCountInString(s) > maxSkill || strings.ContainsFunc(s, unicode.IsControl) {
			return nil, zip.ErrBadRequest("each skill is 1 to 40 characters with no control characters")
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	if len(out) > maxSkills {
		return nil, zip.ErrBadRequest("skills is at most 20 names")
	}
	return out, nil
}

// linksOf is the links an expert gave, trimmed, blank ones dropped and each kept
// once, refusing one that is not an https URL with a host and no login, one past
// maxLink characters, and more than maxLinks.
func linksOf(in []string) ([]string, error) {
	out := make([]string, 0, len(in))
	for _, l := range in {
		l = strings.TrimSpace(l)
		if l == "" || slices.Contains(out, l) {
			continue
		}
		u, err := url.Parse(l)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || len(l) > maxLink ||
			strings.ContainsFunc(l, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
			return nil, zip.ErrBadRequest("each link is an https URL of at most 200 characters, with a host and no login")
		}
		out = append(out, l)
	}
	if len(out) > maxLinks {
		return nil, zip.ErrBadRequest("links is at most 8 URLs")
	}
	return out, nil
}

// unique is the words of in, trimmed and lowercased, each once.
func unique(in []string) []string {
	out := make([]string, 0, len(in))
	for _, w := range in {
		if w = strings.ToLower(strings.TrimSpace(w)); w != "" && !slices.Contains(out, w) {
			out = append(out, w)
		}
	}
	return out
}

// sells refuses an expert who sells nothing: a rate, a price or both.
func (l *Listing) sells() error {
	if l.Expert != nil && l.Price.Sign() <= 0 && l.Expert.Rate.Sign() <= 0 {
		return zip.ErrBadRequest("an expert sets an hourly rate, a price or both")
	}
	return nil
}

// window is the profile as anyone may read it: without the person's identity.
func (e *Expert) window() *Expert {
	if e == nil {
		return nil
	}
	w := *e
	w.Person, w.Self = "", false
	return &w
}

// mine marks the experts among ls that the caller is, for a read of the caller's
// own org's listings.
func mine(ctx context.Context, ls ...*Listing) {
	c, ok := cloud.Request(ctx)
	if !ok || c.User() == "" {
		return
	}
	for _, l := range ls {
		if l.Expert != nil {
			l.Expert.Self = l.Expert.Person == c.User()
		}
	}
}

// expertColumns is the profile as the listings table holds it: lists joined by a
// comma, which neither an area nor a language tag can contain.
type expertColumns struct {
	rate, areas, languages, timezone, availability, person string
	// skills and links are JSON arrays: a skill may hold a comma and so may a URL.
	skills, links string
	verified      int
}

// columns is the profile as stored; the zero profile of a listing that is no
// expert's.
func (e *Expert) columns() expertColumns {
	if e == nil {
		return expertColumns{rate: "0", skills: "[]", links: "[]"}
	}
	return expertColumns{rate: e.Rate.AttoString(), areas: strings.Join(e.Areas, ","), languages: strings.Join(e.Languages, ","),
		timezone: e.Timezone, availability: e.Availability, person: e.Person, verified: boolInt(e.Verified),
		skills: listJSON(e.Skills), links: listJSON(e.Links)}
}

// listJSON stores a list as a JSON array, never null.
func listJSON(l []string) string {
	b, _ := json.Marshal(append([]string{}, l...))
	return string(b)
}

// expert is the profile a row holds.
func (c expertColumns) expert() (*Expert, error) {
	rate, err := money.ParseInt(c.rate)
	if err != nil {
		return nil, err
	}
	skills, err := list(c.skills)
	if err != nil {
		return nil, err
	}
	links, err := list(c.links)
	if err != nil {
		return nil, err
	}
	return &Expert{Person: c.person, Rate: rate, Areas: split(c.areas), Languages: split(c.languages),
		Timezone: c.timezone, Availability: c.availability, Skills: skills, Links: links, Verified: c.verified != 0}, nil
}

// list is a stored JSON array; never nil, so it reads [] and not null.
func list(s string) ([]string, error) {
	var out []string
	if s != "" {
		if err := json.Unmarshal([]byte(s), &out); err != nil {
			return nil, err
		}
	}
	if out == nil {
		out = []string{}
	}
	return out, nil
}

// split is a stored list; never nil, so it reads [] and not null.
func split(s string) []string {
	if s == "" {
		return []string{}
	}
	return strings.Split(s, ",")
}

// ── who the expert is ─────────────────────────────────────────────────────────

// self is the person a request is from, in the org it acts in: any member, since
// an expert lists themself and not their org. A SuperAdmin inspecting another org
// is not that org's member, and lists no expert there.
func self(ctx context.Context) (org, person string, err error) {
	if org, err = principal.Acting(ctx); err != nil {
		return "", "", err
	}
	if !cloud.Member.Admits(cloud.AuthorityIn(ctx)) {
		return "", "", cloud.Member.Refusal()
	}
	c, ok := cloud.Request(ctx)
	if !ok {
		return "", "", zip.ErrForbidden(cloud.Unattested)
	}
	if meter.Payer(c).Org() != org || c.User() == "" {
		return "", "", zip.ErrForbidden("an expert lists themself, from their own org")
	}
	return org, c.User(), nil
}

// editor is the org whose listing id the caller may edit or withdraw: the org an
// admin acts in, or the org of an expert editing their own listing.
func (o marketOps) editor(ctx context.Context, id string) (string, error) {
	org, err := publisher(ctx)
	if err == nil {
		return org, nil
	}
	home, person, serr := self(ctx)
	if serr != nil {
		return "", err
	}
	l, found, gerr := o.s.State.store.Get(ctx, home, strings.TrimSpace(id))
	if gerr != nil || !found || l.Expert == nil || l.Expert.Person != person {
		return "", err
	}
	return home, nil
}

// ── verification ──────────────────────────────────────────────────────────────

// verifyReq gives an expert listing staff's mark, or takes it away.
type verifyReq struct {
	// ID is the expert listing, from the path.
	ID string `json:"id"`
	// Verified is whether staff vouch for the expert.
	Verified bool `json:"verified"`
}

// Marks an expert listing verified, or takes the mark away, whichever org
// published it. Verified is staff's word that the person is who the listing says
// and does what it says: only a SuperAdmin (an owner of the reserved admin org)
// gives it, never an org admin and never the expert, and each change is on the
// audit trail with who made it. It answers the listing as it now stands. A
// listing that is not an expert's is a 404.
//
// Example: {"id": "lst_4f1c", "verified": true}
func (o marketOps) verify(ctx context.Context, in *verifyReq) (*Listing, error) {
	if !cloud.Super.Admits(cloud.AuthorityIn(ctx)) {
		return nil, cloud.Super.Refusal()
	}
	acting, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if o.s.State.audit == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "%v", errUnaudited)
	}
	st := o.s.State.store
	l, found, err := st.expert(ctx, strings.TrimSpace(in.ID))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "verify: %v", err)
	}
	if !found {
		return nil, zip.ErrNotFound("expert listing not found")
	}
	changed := l.Expert.Verified != in.Verified
	if changed {
		err = st.tx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `UPDATE listings SET verified=$1 WHERE publisher_org=$2 AND id=$3`,
				boolInt(in.Verified), l.PublisherOrg, l.ID); err != nil {
				return err
			}
			action := "marketplace.expert.verified"
			if !in.Verified {
				action = "marketplace.expert.unverified"
			}
			return o.trail(ctx, acting, acting, action, audit.Resource{Type: "marketplace.listing", ID: l.ID},
				map[string]any{"publisher": l.PublisherOrg, "verified": in.Verified})
		})
		if err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "verify: %v", err)
		}
		l.Expert.Verified = in.Verified
		l.UpdatedAt = time.Now().Unix()
	}
	if changed && in.Verified {
		o.state(l.PublisherOrg, eventVerified, l.ID, l.Expert.Person, listingFacts(l))
	}
	return &l, nil
}

// expert reads one expert listing by id, whoever published it.
func (s *Store) expert(ctx context.Context, id string) (Listing, bool, error) {
	rows, err := s.query(ctx, `SELECT `+columns+` FROM listings WHERE id=$1 AND kind=$2`, id, KindExpert)
	if err != nil || len(rows) == 0 {
		return Listing{}, false, err
	}
	return rows[0], true, nil
}

// taken reports whether person already has an expert listing in org: a person is
// listed once.
func (s *Store) taken(ctx context.Context, org, person string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM listings WHERE publisher_org=$1 AND kind=$2 AND person=$3`,
		org, KindExpert, person).Scan(&n); err != nil {
		return false, err
	}
	return n > 0, nil
}

// ── events ────────────────────────────────────────────────────────────────────

// What an expert does is stated on the org's event plane, where the rewards program
// reads it, the way every app states its own acts (apps/commerce/emit.go): after
// the act, detached, bounded, dropped past the ceiling and never an error, because
// the listing, the mark, the delivery and the review have been made whatever the
// plane answers. The occurrence concerns the expert: its subject is their IAM
// identity, and its attributes are the facts the program rules on.
//
//	expert.listed         listing, org, person, areas, public     an expert listing was published
//	expert.verified       listing, org, person, areas             staff verified an expert
//	expert.job.delivered  job, listing, org, buyer, person, rating  an expert delivered a job; rating is their mean stars so far, absent until reviewed
//	expert.review         job, listing, org, buyer, person, rating  a buyer rated an expert's job; rating is the stars given
const (
	eventListed    = "expert.listed"
	eventVerified  = "expert.verified"
	eventDelivered = "expert.job.delivered"
	eventReview    = "expert.review"
)

// maxStating is how many statements may be in flight; past it one is dropped.
const maxStating = 32

var stating = make(chan struct{}, maxStating)

// sendFn is the event plane's one call, a variable so a test sees what leaves.
// Only a test reassigns it.
var sendFn = eventpeer.EventCapture

// state states one occurrence on org's event plane. id keys the occurrence, so
// one stated twice is one row.
func (o marketOps) state(org, name, id, person string, facts []client.Signal) {
	select {
	case stating <- struct{}{}:
	default:
		return
	}
	in := &client.EventIn{Name: name, Product: "marketplace", Subject: person, Attributes: facts, ID: name + ":" + id}
	call, log := sendFn, o.s.Log
	go func() {
		defer func() { <-stating }()
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(cloud.For(context.Background(), org), 5*time.Second)
		defer cancel()
		if _, err := call(ctx, in); err != nil {
			log.Debug("marketplace: an expert event was not stated", "name", name, "org", org, "err", err)
		}
	}()
}

// listingFacts are the facts of a listing every expert event states.
func listingFacts(l Listing) []client.Signal {
	return []client.Signal{{Name: "listing", Value: l.ID}, {Name: "org", Value: l.PublisherOrg},
		{Name: "person", Value: l.Expert.Person}, {Name: "areas", Value: strings.Join(l.Expert.Areas, ",")}}
}

// stated states job j's step on its expert's listing, when the job was hired
// through an expert listing; a job of any other kind states nothing. rating is
// the stars to state, or 0 to state the expert's mean so far.
func (o marketOps) stated(ctx context.Context, name string, j Job, stars int) {
	if j.Listing == "" {
		return
	}
	st := o.s.State.store
	l, found, err := st.Get(ctx, j.SellerOrg, j.Listing)
	if err != nil || !found || l.Expert == nil {
		return
	}
	facts := append(listingFacts(l), client.Signal{Name: "job", Value: j.ID}, client.Signal{Name: "buyer", Value: j.BuyerOrg})
	id := j.ID
	switch {
	case stars > 0:
		facts = append(facts, client.Signal{Name: "rating", Value: strconv.Itoa(stars)})
		id += ":" + j.BuyerOrg
	default:
		reps, err := st.reputations(ctx, []Listing{l})
		if err == nil && reps[l.ID] != nil {
			if r := reputation(*reps[l.ID]); r.Rating != nil {
				facts = append(facts, client.Signal{Name: "rating", Value: strconv.FormatFloat(*r.Rating, 'f', 1, 64)})
			}
		}
	}
	o.state(l.PublisherOrg, name, id, l.Expert.Person, facts)
}
