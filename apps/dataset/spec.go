package dataset

// spec.go is what a dataset IS: a BOUND query over one tenant's own feature
// surface, a maturity horizon, where the splits cut, and the seed that decides
// membership when the window is larger than the plane will carry.
//
// Nothing on a spec ever becomes a SQL identifier. Dims resolve through a fixed
// allowlist to package-constant column names; the kind is checked against a
// closed set; the window, the seed, the share and the caps all BIND. That is not
// a convention here — [normalize] is the only way to obtain a [spec], it is
// total, and every statement in peer.go takes the normalised value.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/zap-proto/zip"
)

// ── the source ───────────────────────────────────────────────────────────────

// sourceTable is the ONE plane a dataset is derived from: the per-org feature
// surface, one row per (kind, subject, five-minute bucket).
//
// This plane does NOT own that table's DDL and never creates it. The rollup that
// writes it owns it, exactly as o11y owns the event.* DDL that cloud only reads —
// one writer per table, and a reader that finds it absent reports an honest gap
// rather than conjuring a second definition that would drift from the first.
//
// It is also the ONLY source admitted, and that is a correctness decision, not a
// convenience. A dataset row's coordinates must be the SAME coordinates the
// scorer sees; reading the raw event planes here would be a second reduction of
// the same facts, and two reductions of one stream diverge silently — the model
// then trains on coordinates production never produces.
const sourceTable = "hanzo.risk_feature"

// Subject kinds — the closed set the source surface carries. A dataset over a
// kind the surface does not have is a dataset of nothing, so the kind is checked
// at the endpoint rather than discovered as an empty result.
const (
	kindPerson  = "person"
	kindSession = "session"
	kindAccount = "account"
)

var kinds = []string{kindPerson, kindSession, kindAccount}

// dim is one measurable coordinate: the name a caller may use and the column it
// resolves to. Column is a PACKAGE CONSTANT reached only through [dimBy]; a
// caller names a dim, never a column.
type dim struct {
	// Name is the dim as this API publishes it.
	Name string
	// Column is the source column it reads. Never caller input.
	Column string
	// Unit is how to read the raw number, which is what turns a coordinate back
	// into a sentence when someone has to explain a decision.
	Unit string
}

// dims is the surface, in one place and in ONE order. That order is the order of
// every point vector this plane writes, so it is part of the contract: reordering
// it changes what coordinate 3 means in every dataset already materialised, which
// is why the order lives here and the per-dataset selection is recorded on the
// manifest.
var dims = []dim{
	{Name: "events", Column: "events", Unit: "product events in the bucket"},
	{Name: "sessions", Column: "sessions", Unit: "distinct sessions in the bucket"},
	{Name: "distincts", Column: "distincts", Unit: "distinct identities in the bucket"},
	{Name: "paths", Column: "paths", Unit: "distinct paths in the bucket"},
	{Name: "errors", Column: "errors", Unit: "captured failures in the bucket"},
	{Name: "calls", Column: "calls", Unit: "metered inference calls in the bucket"},
	{Name: "failures", Column: "failures", Unit: "metered calls that did not succeed"},
	{Name: "tokens", Column: "tokens", Unit: "tokens consumed in the bucket"},
	{Name: "spend", Column: "spend_nano", Unit: "nano-USD of metered spend in the bucket"},
	{Name: "ips", Column: "ips", Unit: "distinct client addresses in the bucket"},
}

// dimBy is THE ALLOWLIST: the only way a name becomes a column.
var dimBy = func() map[string]dim {
	m := make(map[string]dim, len(dims))
	for _, d := range dims {
		m[d.Name] = d
	}
	return m
}()

// ── the bounds ───────────────────────────────────────────────────────────────
//
// Every one of these exists because the alternative is a tenant that can spend
// the warehouse. The warehouse is a single stateful pod which has taken the API
// down once already, so an unbounded scan here is a fleet outage there.

const (
	// maxWindow is the longest window a dataset may cover. It is the SOURCE's own
	// retention: a window longer than the plane can hold is a window whose older
	// half is already gone, so the dataset would silently be shorter than it says.
	maxWindow = 400 * 24 * time.Hour

	// minWindow keeps a spec from naming an empty or inverted window, which reads
	// as "no rows" and is indistinguishable from a quiet tenant.
	minWindow = time.Minute

	// maxRows caps HOW MANY rows one materialisation holds. The rows are held in
	// this process to assign the splits and compute the digest — the two things
	// that CANNOT be done in the store without duplicating the definition of a
	// split — so a count on its own is a scan bound and NOTHING ELSE. What bounds
	// the memory is this count times [maxRowBytes], and that product is a bound
	// only because [maxSubjectBytes] exists.
	maxRows = 200_000

	// maxSubjectBytes bounds the ONE caller-sized value a row carries: the subject
	// identity, `subject_kind` + `subject`.
	//
	// A COUNT OVER CALLER-SIZED VALUES IS NOT A BOUND. Every other part of a row is
	// fixed by this package — the kind comes from a closed set, the coordinates are
	// at most len(dims) float64s, the instant is an instant. The subject is not: the
	// rollup that writes the source lifts it from `distinct_id`, `session_id` and
	// `user_id`, which arrive on /v1/event from the caller. `distinct_id` is capped
	// at 256 bytes on the anonymous lane and REPLACED by the token's own subject on
	// the signed one, but `session_id` — which the `session` rollup files as a
	// subject verbatim — is capped nowhere. So "200k rows is tens of megabytes" and
	// "eight jobs is a few hundred megabytes" were arithmetic over an unknown, and
	// one tenant's traffic decided the real number.
	//
	// 256 bytes is what the identified lane already states for a subject, for the
	// reason that carries over unchanged: a minted id is a uuid (36 bytes), the
	// value is KEYED — uniqExact over it is the whole point of the surface — and
	// something longer is not an id. With the bound in place, count times max IS the
	// byte bound, which is the only form in which either number means anything.
	//
	// It is enforced ONCE, at [representable], on the way OUT of the source and
	// therefore on the way IN to this process and to the rows table. There is no
	// second spelling downstream: every row this plane holds, writes or returns came
	// through that predicate.
	maxSubjectBytes = 256

	// maxHorizon bounds the maturity wait. A year is past every dispute window
	// that exists; beyond it the horizon is excluding data for no reason anybody
	// can name.
	maxHorizon = 365

	// maxNames and maxVersions bound how much of the store one tenant can claim.
	// Both tables are partitioned by (org, name), and partition count is a global
	// resource, so an unbounded number of names is a way to degrade every other
	// tenant's store from inside one tenant's own quota.
	maxNames    = 64
	maxVersions = 256

	// maxSeed bounds the seed's length. It is bound as a value, never
	// interpolated, so this is a size limit and not a safety one.
	maxSeed = 128
)

// The BYTE bounds, every one of them DERIVED. A byte ceiling written down beside a
// count is two numbers nothing keeps in agreement, and the one that was wrong here
// was always the byte one. These are computed from [maxSubjectBytes] and len(dims),
// so raising either moves them and no comment goes stale.
//
// They are vars and not consts only because len(dims) is a slice length. Nothing
// assigns them; [TestTheByteBoundIsDerivedFromTheValueBound] pins the arithmetic.
var (
	// maxRowBytes is one row's ceiling: its subject identity, its coordinates, and
	// the fixed remainder (the derived id, the instant, the split). It is what makes
	// every count below convertible into a size.
	maxRowBytes = maxSubjectBytes + 8*len(dims) + fixedRowBytes

	// maxResidentBytes is what ONE materialisation can hold in this process, and
	// maxProcessBytes is what all [maxJobs] of them can hold at once. This is the
	// claim that used to be a guess.
	maxResidentBytes = maxRows * maxRowBytes
	maxProcessBytes  = maxJobs * maxResidentBytes

	// maxPageBytes is the largest export response. The rows table holds only
	// subjects that passed [representable] on the way in, so the page count times
	// the row ceiling bounds the body — no second check on the read path.
	maxPageBytes = page * maxRowBytes
)

// fixedRowBytes is the part of a row this package fixes: a 64-hex derived id, an
// RFC-3339 instant, a split name, and the JSON punctuation around them. Rounded up;
// it is a ceiling, not a measurement.
const fixedRowBytes = 192

// name is what a dataset may be called: lower-case, digits and single hyphens,
// starting with a letter. It is BOUND everywhere it is used — including in the
// partition expression, which takes values — so this refusal is about the name
// being readable back, not about safety.
var name = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}[a-z0-9]$|^[a-z]$`)

// ── the spec ─────────────────────────────────────────────────────────────────

// spec is the normalised, bound query one dataset version is. It is produced
// ONLY by [normalize], so every field below has already been checked against the
// allowlists and the bounds above.
type spec struct {
	// Name identifies the dataset across its versions.
	Name string
	// Kind narrows to one subject kind. Empty takes every kind.
	Kind string
	// Dims are the coordinates, in the plane's own published order — never the
	// caller's, so two specs naming the same dims produce the same point vector.
	Dims []string
	// From and To bound the event window, half-open.
	From, To time.Time
	// Horizon is how long a row must have aged before it may be admitted, so a
	// fact that was not yet knowable at scoring time cannot reach a training set.
	Horizon time.Duration
	// Cuts are the two instants dividing train | val | test.
	Cuts [2]time.Time
	// Seed decides MEMBERSHIP — which subjects are admitted when the window holds
	// more than the plane will carry. It is on the manifest, so a capped
	// materialisation is reproducible instead of being whichever rows the store
	// happened to return first.
	Seed string
	// Rows caps this materialisation.
	Rows int
}

// record is the canonical, stored form of a spec — one struct for writing it and
// for reading it back, so the two can never describe different shapes. Every
// field is a string or an int: a stored spec has to be readable by whatever comes
// next, including a reader that does not link this package.
type record struct {
	Name    string   `json:"name"`
	Kind    string   `json:"kind,omitempty"`
	Dims    []string `json:"dims"`
	From    string   `json:"from"`
	To      string   `json:"to"`
	Horizon int      `json:"horizon"`
	Cuts    []string `json:"cuts"`
	Seed    string   `json:"seed"`
	Rows    int      `json:"rows"`
	// Source names the plane the rows came from, at the version of this plane
	// that wrote them. A dataset whose source is a table this code no longer
	// reads must still say where its bytes came from.
	Source string `json:"source"`
}

// record projects the spec into its stored form.
func (s spec) record() record {
	return record{
		Name:    s.Name,
		Kind:    s.Kind,
		Dims:    s.Dims,
		From:    stamp(s.From),
		To:      stamp(s.To),
		Horizon: int(s.Horizon / (24 * time.Hour)),
		Cuts:    []string{stamp(s.Cuts[0]), stamp(s.Cuts[1])},
		Seed:    s.Seed,
		Rows:    s.Rows,
		Source:  sourceTable,
	}
}

// canon renders the record as the canonical JSON stored on the manifest and fed
// to the digest. It is the ONE marshaller, so the bytes written and the bytes
// hashed are the same bytes by construction rather than by agreement.
//
// It is the WHOLE of what a dataset is, so a reader with the manifest row can say
// exactly what was asked for — and two specs that differ anywhere produce
// different bytes here, which is what makes the digest a claim about the question
// as well as the answer.
func (r record) canon() string {
	b, _ := json.Marshal(r)
	return string(b)
}

// canon renders this spec's stored form.
func (s spec) canon() string { return s.record().canon() }

// spec reads a stored record back. It is TOTAL over this plane's OWN bytes and
// refuses anything else: a manifest row whose spec cannot be read is a row whose
// dataset cannot be explained, and answering with a half-parsed spec would put an
// invented window on an audit reply.
func (r record) spec() (spec, error) {
	s := spec{Name: r.Name, Kind: r.Kind, Dims: r.Dims, Seed: r.Seed, Rows: r.Rows}
	var err error
	if s.From, err = instant("from", r.From); err != nil {
		return spec{}, err
	}
	if s.To, err = instant("to", r.To); err != nil {
		return spec{}, err
	}
	if len(r.Cuts) != 2 {
		return spec{}, fmt.Errorf("the stored spec does not carry two cuts")
	}
	for i, c := range r.Cuts {
		if s.Cuts[i], err = instant("cut", c); err != nil {
			return spec{}, err
		}
	}
	s.Horizon = time.Duration(r.Horizon) * 24 * time.Hour
	for _, d := range s.Dims {
		if _, ok := dimBy[d]; !ok {
			return spec{}, fmt.Errorf("the stored spec names dim %q, which this plane no longer publishes", d)
		}
	}
	return s, nil
}

// decode reads a stored spec from the manifest's canonical JSON.
func decode(canon string) (record, error) {
	var r record
	if err := json.Unmarshal([]byte(canon), &r); err != nil {
		return record{}, fmt.Errorf("the stored spec is unreadable: %w", err)
	}
	return r, nil
}

// columns is the projection this spec reads, in the spec's own dim order. The
// values are package constants from [dims]; the only thing composed into a
// statement is code.
func (s spec) columns() []string {
	out := make([]string, 0, len(s.Dims))
	for _, d := range s.Dims {
		out = append(out, dimBy[d].Column)
	}
	return out
}

// normalize turns what arrived off the wire into a spec, or refuses.
//
// It is TOTAL and it is the only constructor: there is no path from a request to
// a statement that does not pass through here, so "the window is bounded", "the
// dim is on the allowlist" and "the kind is real" are properties of the type
// rather than of the caller.
//
// `now` is passed rather than read so the horizon arithmetic is testable, which
// is the whole of R1: a maturity rule that cannot be tested is a maturity rule
// nobody has checked.
func normalize(in riskDatasetSpec, now time.Time) (spec, error) {
	var s spec

	s.Name = strings.ToLower(strings.TrimSpace(in.Name))
	if !name.MatchString(s.Name) {
		return spec{}, zip.ErrBadRequest("a dataset name is lower-case letters, digits and hyphens, 1 to 64 characters, starting with a letter")
	}

	s.Kind = strings.TrimSpace(in.Kind)
	if s.Kind != "" && !known(s.Kind) {
		return spec{}, zip.ErrBadRequest(fmt.Sprintf("subject kind %q is not one of %s", s.Kind, strings.Join(kinds, ", ")))
	}

	var err error
	if s.Dims, err = pick(in.Dims); err != nil {
		return spec{}, err
	}

	if s.From, err = instant("from", in.From); err != nil {
		return spec{}, err
	}
	if s.To, err = instant("to", in.To); err != nil {
		return spec{}, err
	}
	switch d := s.To.Sub(s.From); {
	case d < minWindow:
		return spec{}, zip.ErrBadRequest("the window is empty or inverted, which reads as a quiet tenant rather than as the mistake it is")
	case d > maxWindow:
		return spec{}, zip.ErrBadRequest(fmt.Sprintf("the window is longer than the %d-day retention of the source, so its older half is already gone", int(maxWindow.Hours()/24)))
	}

	if in.Horizon < 0 || in.Horizon > maxHorizon {
		return spec{}, zip.ErrBadRequest(fmt.Sprintf("the maturity horizon is a whole number of days between 0 and %d", maxHorizon))
	}
	s.Horizon = time.Duration(in.Horizon) * 24 * time.Hour

	// The horizon is applied to the WINDOW at the endpoint, not only at the read: a
	// spec whose whole window is younger than its horizon admits nothing, and
	// saying so now is better than a ready dataset with zero rows that looks like
	// a tenant with no activity.
	if !s.From.Add(s.Horizon).Before(now) {
		return spec{}, zip.ErrBadRequest("no part of this window has aged past the maturity horizon yet, so every row in it would be excluded")
	}

	if s.Cuts, err = cuts(in.Cuts, s.From, s.To); err != nil {
		return spec{}, err
	}

	s.Seed = strings.TrimSpace(in.Seed)
	if s.Seed == "" {
		// A seed is REQUIRED in the value even when the caller gives none, because
		// membership under the cap must be reproducible from the manifest alone.
		// Deriving it from the rest of the spec keeps it deterministic without
		// making the caller supply a magic string.
		s.Seed = s.Name
	}
	if len(s.Seed) > maxSeed {
		return spec{}, zip.ErrBadRequest(fmt.Sprintf("the seed is at most %d characters", maxSeed))
	}

	s.Rows = in.Rows
	if s.Rows <= 0 || s.Rows > maxRows {
		s.Rows = maxRows
	}
	return s, nil
}

// pick resolves the caller's dim names through the allowlist and returns them in
// the PLANE's order, never the caller's. Two specs naming the same set of dims in
// different orders are the same spec, and they produce byte-identical point
// vectors — without which the digest would depend on how a request was typed.
func pick(want []string) ([]string, error) {
	if len(want) == 0 {
		out := make([]string, 0, len(dims))
		for _, d := range dims {
			out = append(out, d.Name)
		}
		return out, nil
	}
	seen := make(map[string]bool, len(want))
	for _, w := range want {
		w = strings.TrimSpace(w)
		if _, ok := dimBy[w]; !ok {
			return nil, zip.ErrBadRequest(fmt.Sprintf("%q is not a published dim", w))
		}
		seen[w] = true
	}
	out := make([]string, 0, len(seen))
	for _, d := range dims {
		if seen[d.Name] {
			out = append(out, d.Name)
		}
	}
	return out, nil
}

// cuts validates the two split instants, or derives them.
//
// The derived pair is 70% and 85% of the window BY TIME. It is a default and not
// a policy: a dataset that wants different boundaries states them, and the pair
// is recorded on the manifest either way, so a reader never has to know which
// happened.
func cuts(want []string, from, to time.Time) ([2]time.Time, error) {
	var out [2]time.Time
	if len(want) == 0 {
		d := to.Sub(from)
		out[0] = from.Add(d * 70 / 100).Truncate(time.Second)
		out[1] = from.Add(d * 85 / 100).Truncate(time.Second)
		return out, nil
	}
	if len(want) != 2 {
		return out, zip.ErrBadRequest("cuts are exactly two instants: where train ends and where val ends")
	}
	for i, w := range want {
		t, err := instant("cut", w)
		if err != nil {
			return out, err
		}
		out[i] = t
	}
	if !out[0].After(from) || !out[1].After(out[0]) || !to.After(out[1]) {
		return out, zip.ErrBadRequest("the cuts must strictly increase and lie inside the window, or a split would be empty")
	}
	return out, nil
}

// instant parses one RFC 3339 timestamp and refuses anything else. Timestamps
// arrive as strings on the wire and are bound as the store's own DateTime
// literal, so this is the ONE place a caller's text becomes a time.
func instant(field, v string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(v))
	if err != nil {
		return time.Time{}, zip.ErrBadRequest(fmt.Sprintf("%s must be an RFC 3339 instant", field))
	}
	return t.UTC(), nil
}

func known(kind string) bool {
	return slices.Contains(kinds, kind)
}

// stamp renders an instant the one way this plane renders instants: RFC 3339 in
// UTC, to the second. The store's DateTime has one-second resolution, so a
// sub-second value on a spec would round-trip to something else and the digest
// would not reproduce.
func stamp(t time.Time) string { return t.UTC().Truncate(time.Second).Format(time.RFC3339) }

// literal binds an instant as the store's own DateTime literal — the same
// transport apps/analytics binds windows with, so a bound window is a bound value
// and never an interpolated one.
func literal(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }
