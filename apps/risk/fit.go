package risk

// fit.go is the REGISTRY. A fit is one estimation of one shape against one
// bounded window of one tenant's own behaviour, kept as a record in that
// tenant's own file.
//
// THE NOUN IS `fit`, NOT `model`. Two reasons, and the second is the load-bearing
// one. `/v1/ml/models` already belongs to apps/ml (kserve InferenceService,
// manifest/apps.go), and zip refuses two owners for one prefix at compose time —
// so a registry called "models" does not fail in production, it fails the build,
// which is the good outcome but only once. And `fit` is already the engine's own
// word for the thing (luxfi/aml pkg/models.Fit, pkg/topology.Fit): the estimated
// parameters produced by fitting one shape to one window. A second word for it
// would be a second vocabulary, and the exhaustive search cannot rank across two.
//
// WHAT IS IMMUTABLE AND WHAT IS NOT, STATED ONCE. The ARTEFACT is immutable: the
// algorithm, the geometry, the source window with its digest, the feature
// inventory it read and the metrics measured on the held-out split are written
// when the fit completes and never again. Digest fingerprints exactly that set,
// and fit_test.go asserts it does not move. The ROLE is not immutable and could
// not be — a champion becomes a retired champion, that is what promotion is —
// so every transition is APPENDED to fit_move with who moved it and why. A model
// that changed and cannot say who changed it is a control nobody owns.
//
// THE SPLIT IS TEMPORAL AND THEN GROUPED BY SUBJECT, NEVER RANDOM. A hash split
// puts the same account, device or address on both sides of the line, and the
// model then memorises the entity rather than the behaviour: offline separation
// that does not survive contact with a subject it has not seen. So the cut is a
// point in time, and any subject straddling it is pulled WHOLE to the side its
// first row fell on.
//
// AND THE LABEL MUST HAVE BEEN KNOWABLE. A row is admitted to the measurement
// only once its maturity horizon has elapsed — a dispute lands 30 to 120 days
// after the payment it judges, so a set joined on event time alone knows the
// future and measures beautifully against a world that has already happened.
// Horizon is on the fit, it is applied at admission, and there is a test that
// inserts a label arriving after the horizon and asserts it is excluded.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/luxfi/aml/pkg/replay"
	"github.com/luxfi/aml/pkg/velocity"
	"github.com/zap-proto/zip"
)

// ── the vocabulary ──────────────────────────────────────────────────────────

// The roles a fit can hold. Exactly one champion and at most one challenger per
// tenant, enforced by partial unique indexes in the schema rather than by the
// code that writes them: a concurrent double-promote then loses at the index,
// which is a place that cannot be forgotten.
const (
	// roleCandidate is a fit that has been estimated and decides nothing.
	roleCandidate = "candidate"
	// roleChallenger is a fit scoring the live stream alongside the champion.
	// It is SHADOW by construction — its store is built with anomaly's own
	// Shadow flag set and its action is never returned — so there is no second
	// shadow mechanism to keep in step with the first.
	roleChallenger = "challenger"
	// roleChampion is the fit whose score decides.
	roleChampion = "champion"
	// roleRetired is a fit that has been superseded. It keeps its learned state,
	// which is what makes rolling back to it instant rather than a re-warm.
	roleRetired = "retired"
)

// The statuses a fit passes through. Durable, because the queue is the record:
// a process that dies mid-fit must come back saying "fitting" and not "ready".
const (
	fitQueued    = "queued"
	fitFitting   = "fitting"
	fitReady     = "ready"
	fitRefused   = "refused"
	fitCancelled = "cancelled"
)

// algoForest is the streaming half-space forest — the one algorithm this plane
// can estimate today, and the one the decision path actually runs. It is named
// rather than assumed so a fit records WHICH estimator produced it, and so the
// day a second one lands the registry does not have to be reshaped to hold it.
const algoForest = "forest"

// sourceHistory is the name of the only source a fit can be estimated from
// here: the tenant's own recorded decisions. It is a NAME and not a hard-coded
// path because the fit record cites what it read, and a citation that cannot
// name the thing is not a citation.
const sourceHistory = "history"

// defaultHorizon is how long a payment-lane row must age before its label is
// treated as final. 120 days sits past the Visa and Mastercard dispute windows,
// so a row admitted at that age is one no further chargeback can re-judge.
const defaultHorizon = 120

// defaultWindow is how many days of a tenant's own history a fit reads when the
// caller does not say. A year: long enough to hold a seasonal cycle, short
// enough that the tail is behaviour the tenant would still recognise.
const defaultWindow = 365

// fitRows is the row cap. It is the SAME bound the exhaustive search takes,
// because both replay the tenant's history through a sandbox and the cost is
// the same cost — one bound, one place, no second number to drift.
const fitRows = 5000

// fitCents is what one fit costs the caller. Priced like a search and for the
// same reason: it is seconds to minutes of a shared pod's CPU, not the
// microseconds a screen takes, and an unpriced expensive act reachable by
// anyone with a key is a denial of service with a billing address.
const fitCents = searchCents

// ── the record ──────────────────────────────────────────────────────────────

// fitRow is one registry entry as it sits in the tenant's file.
type fitRow struct {
	ID      string
	At      time.Time
	By      string
	Algo    string
	Shape   candidate
	Source  fitSource
	Metrics fitMetrics
	Profile fitProfile
	Digest  string
	Role    string
	Status  string
	Refusal string
	// Served is when this fit first became champion, empty when it never has.
	// It is what makes a rollback distinguishable from a first promotion: a fit
	// that has already decided real traffic has been proven in the only way that
	// counts, so returning to it needs no further ceremony.
	Served string
	// Retired is when it was last stood down.
	Retired string
}

// fitSource is the window a fit was estimated over, as a VALUE. Everything here
// is recorded at estimation time and never recomputed: a source that is a query
// to be re-run is a source that answers differently once the rows behind it age
// out, which is the difference between lineage and a claim of lineage.
type fitSource struct {
	// Name is the surface read. Today: the tenant's own recorded decisions.
	Name string `json:"name"`
	// Version is monotone per (tenant, name). It is minted by the write, so two
	// fits over the same name can always be ordered and neither can claim the
	// other's number.
	Version int `json:"version"`
	// From and To bound the window, half-open, in UTC.
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	// Horizon is how many days a row had to have aged before it was admitted.
	Horizon int `json:"horizon"`
	// Cut is where the temporal split fell.
	Cut time.Time `json:"cut"`
	// Rows, Train and Test are the admitted counts. Train + Test == Rows.
	Rows  int `json:"rows"`
	Train int `json:"train"`
	Test  int `json:"test"`
	// Judged is how many TEST rows carry a matured disposition, and Productive
	// and Unproductive are the two classes. Reported before anything is trained
	// on them, so imbalance is visible rather than discovered in the metrics.
	Judged       int `json:"judged"`
	Productive   int `json:"productive"`
	Unproductive int `json:"unproductive"`
	// Dims is the feature set, in the order the model reads it.
	Dims []string `json:"dims"`
	// Inventory is the engine's own shape digest: the inventory in order plus the
	// detector's geometry. State fitted under one inventory cannot be restored
	// into another, and this is the value that decides it.
	Inventory string `json:"inventory"`
	// Digest fingerprints the admitted rows and their split assignment. Two fits
	// over one window either agree on it or the registry says they do not.
	Digest string `json:"digest"`
}

// fitMetrics is measured on the HELD-OUT split, never on the rows the model
// learned from.
//
// EVERY RATE IS A POINTER. An unmeasured proportion reported as 0.0 reads as a
// perfect model, and the one thing this plane must never do is present the
// absence of a measurement as a good measurement. replay makes the same choice
// for the same reason (luxfi/aml pkg/replay: an absent false-positive
// proportion, never a zero one).
type fitMetrics struct {
	// Rows is how many held-out rows were replayed.
	Rows int `json:"rows"`
	// Scored is how many of them the model was warm enough to score, and Warm is
	// how many passed before it would score at all. A shape that warms slowly
	// costs real coverage and the cost is reported rather than amortised away.
	Scored int `json:"scored"`
	Warm   int `json:"warm"`
	// Alerted is how many of the scored rows crossed the threshold.
	Alerted int `json:"alerted"`
	// Judged is how many scored rows carry a matured disposition.
	Judged int `json:"judged"`
	// AUC is the probability the model ranks a productive row above an
	// unproductive one. Absent unless both classes are present: an AUC over one
	// class is not a number, it is a division.
	AUC *float64 `json:"auc,omitempty"`
	// Prevalence is the productive share of the judged rows. AUC without it is
	// uninterpretable on a one-in-ten-thousand problem.
	Prevalence *float64 `json:"prevalence,omitempty"`
	// Separation is the mean score of the alerted set minus the mean of the
	// rest. It is the objective the exhaustive search already ranks on, so a fit
	// and a search trial are comparable rather than merely adjacent.
	Separation *float64 `json:"separation,omitempty"`
	// Realised is Alerted/Scored against the appetite the shape stated. The gap
	// between the two IS the governance report.
	Realised *float64 `json:"realised,omitempty"`
	// Lift is precision in the alerted set divided by prevalence: how much
	// better than chance the top slice is. One on an unhelpful model, and the
	// number an operator can actually reason about.
	Lift *float64 `json:"lift,omitempty"`
}

// fitProfile is what DRIFT is measured against: the distributions this fit saw
// while it was being estimated. It is stored ON the fit because drift is a
// statement about a particular model against the world it was fitted to, and a
// profile recomputed later from today's data cannot make that statement.
type fitProfile struct {
	// Dims holds, per feature, a ten-bin histogram of the coordinate the model
	// read, normalised to sum one.
	Dims map[string][]float64 `json:"dims,omitempty"`
	// Score is the score distribution in the same 32 bands the engine's own
	// State reports, normalised.
	Score []float64 `json:"score,omitempty"`
	// Rate is the productive share of the judged rows at estimation time.
	// Absent when nothing was judged.
	Rate *float64 `json:"rate,omitempty"`
	// Scored is how many WARM rows the distributions above were built from. It is
	// kept because it decides whether an index against them means anything: a
	// reference built from forty rows is a reference nothing can be compared to,
	// and a drift reading has to be able to say so rather than report a number.
	Scored int `json:"scored,omitempty"`
	// Seed is the tree geometry these distributions were produced under. Drift
	// replays under the SAME one or the score index is geometry noise; see
	// newSeed. It is drawn from the system CSPRNG, it stays in the tenant's own
	// encrypted file, no wire record carries it, and nothing published determines
	// it — a geometry anyone can reproduce is a geometry anyone can search for a
	// region to hide in.
	Seed uint64 `json:"seed,omitempty"`
}

// move is one role transition, appended and never amended.
type move struct {
	ID     string
	Fit    string
	At     time.Time
	By     string
	Was    string
	Now    string
	Reason string
	// Stood is the fit this one displaced, when it displaced one. It is on the
	// row so the pair is readable from either side without a second query.
	Stood string
}

// ── the store ───────────────────────────────────────────────────────────────

// fitSchema is appended to the tenant file's shape in store.go. It lives there
// and not here for one reason: a second migration path is a second way for a
// file to be half-shaped, and there is one way to open a tenant file.

func putFit(db *sql.DB, r fitRow) error {
	shape, err := json.Marshal(r.Shape)
	if err != nil {
		return err
	}
	src, err := json.Marshal(r.Source)
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO fit (id, at, by, algo, shape, source, metrics, profile,
		digest, role, status, refusal, served, retired)
		VALUES (?, ?, ?, ?, ?, ?, '{}', '{}', '', ?, ?, '', '', '')`,
		r.ID, stamp(r.At), r.By, r.Algo, string(shape), string(src), r.Role, r.Status)
	return err
}

// sealFit writes the estimated artefact ONCE, and only onto a fit that is still
// being estimated. The status guard in the WHERE clause is what makes the
// artefact immutable in the store rather than by convention: a second seal, a
// racing worker or a cancelled fit's late completion all match zero rows.
func sealFit(db *sql.DB, id string, src fitSource, m fitMetrics, p fitProfile, digest string) error {
	s, err := json.Marshal(src)
	if err != nil {
		return err
	}
	mb, err := json.Marshal(m)
	if err != nil {
		return err
	}
	pb, err := json.Marshal(p)
	if err != nil {
		return err
	}
	res, err := db.Exec(`UPDATE fit SET source = ?, metrics = ?, profile = ?, digest = ?,
		status = ? WHERE id = ? AND status = ?`,
		string(s), string(mb), string(pb), digest, fitReady, id, fitFitting)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return errNotFitting
	}
	return nil
}

// errNotFitting is what a completion finds when the fit it was estimating is no
// longer the one being estimated — cancelled, or already sealed. It is a normal
// outcome of a cancellable queue, not a failure.
var errNotFitting = errors.New("risk: this fit is no longer being estimated")

// markFit moves a fit's STATUS. Role never moves through here: a status is the
// queue's business and a role is a person's, and one function doing both is one
// function a bug in either half can drive.
func markFit(db *sql.DB, id, status, refusal string) error {
	_, err := db.Exec(`UPDATE fit SET status = ?, refusal = ? WHERE id = ?`, status, refusal, id)
	return err
}

func scanFit(scan func(...any) error) (fitRow, error) {
	var r fitRow
	var at, shape, src, metrics, profile string
	if err := scan(&r.ID, &at, &r.By, &r.Algo, &shape, &src, &metrics, &profile,
		&r.Digest, &r.Role, &r.Status, &r.Refusal, &r.Served, &r.Retired); err != nil {
		return fitRow{}, err
	}
	r.At = unstamp(at)
	_ = json.Unmarshal([]byte(shape), &r.Shape)
	_ = json.Unmarshal([]byte(src), &r.Source)
	_ = json.Unmarshal([]byte(metrics), &r.Metrics)
	_ = json.Unmarshal([]byte(profile), &r.Profile)
	return r, nil
}

const fitColumns = `id, at, by, algo, shape, source, metrics, profile, digest, role, status, refusal, served, retired`

func getFit(db *sql.DB, id string) (fitRow, error) {
	r, err := scanFit(db.QueryRow(`SELECT `+fitColumns+` FROM fit WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		// 404 and never 403. A 403 distinguishes "exists and is not yours" from
		// "does not exist", which is an oracle a neighbour can enumerate with.
		return fitRow{}, zip.ErrNotFound("no such fit")
	}
	return r, err
}

func listFits(db *sql.DB, limit int) ([]fitRow, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.Query(`SELECT `+fitColumns+` FROM fit ORDER BY at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []fitRow
	for rows.Next() {
		r, err := scanFit(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// fitInRole reads the one fit holding a role, if any. The partial unique index
// is what makes "the one" true; this reads it back.
func fitInRole(db *sql.DB, role string) (fitRow, bool, error) {
	r, err := scanFit(db.QueryRow(`SELECT `+fitColumns+` FROM fit WHERE role = ?`, role).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return fitRow{}, false, nil
	}
	if err != nil {
		return fitRow{}, false, err
	}
	return r, true, nil
}

// nextVersion mints the next monotone version for a source name. It reads MAX
// over the tenant's own rows, so two tenants' version 3 are unrelated numbers
// and neither can be inferred from the other.
func nextVersion(db *sql.DB, name string) (int, error) {
	rows, err := db.Query(`SELECT source FROM fit`)
	if err != nil {
		return 0, err
	}
	defer func() { _ = rows.Close() }()
	top := 0
	for rows.Next() {
		var body string
		if err := rows.Scan(&body); err != nil {
			return 0, err
		}
		var s fitSource
		if json.Unmarshal([]byte(body), &s) == nil && s.Name == name && s.Version > top {
			top = s.Version
		}
	}
	return top + 1, rows.Err()
}

func putMove(db *sql.DB, m move) error {
	_, err := db.Exec(`INSERT INTO fit_move (id, fit, at, by, was, now, reason, stood)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Fit, stamp(m.At), m.By, m.Was, m.Now, m.Reason, m.Stood)
	return err
}

func fitMoves(db *sql.DB, fit string) ([]move, error) {
	rows, err := db.Query(`SELECT id, fit, at, by, was, now, reason, stood
		FROM fit_move WHERE fit = ? ORDER BY at ASC`, fit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []move
	for rows.Next() {
		var m move
		var at string
		if err := rows.Scan(&m.ID, &m.Fit, &at, &m.By, &m.Was, &m.Now, &m.Reason, &m.Stood); err != nil {
			return nil, err
		}
		m.At = unstamp(at)
		out = append(out, m)
	}
	return out, rows.Err()
}

// setRole writes a role transition: the row's new role, the displaced row's, and
// the append-only move record, in ONE transaction. Split across statements a
// crash between them leaves two champions or none, and the partial index would
// then refuse every subsequent write with an error nobody could act on.
func setRole(db *sql.DB, r fitRow, want, by, reason string, now time.Time) (move, error) {
	tx, err := db.Begin()
	if err != nil {
		return move{}, err
	}
	defer func() { _ = tx.Rollback() }()

	// Stand down whoever holds the role. A champion being displaced goes to
	// retired and KEEPS its learned state; a challenger goes back to candidate,
	// because a trial that ends is not a model that failed.
	stood := ""
	if want == roleChampion || want == roleChallenger {
		fallback := roleRetired
		if want == roleChallenger {
			fallback = roleCandidate
		}
		var id, was string
		err := tx.QueryRow(`SELECT id, role FROM fit WHERE role = ? AND id != ?`, want, r.ID).Scan(&id, &was)
		switch {
		case err == nil:
			stood = id
			if _, err := tx.Exec(`UPDATE fit SET role = ?, retired = ? WHERE id = ?`,
				fallback, stamp(now), id); err != nil {
				return move{}, err
			}
			if err := putMoveTx(tx, move{
				ID: newID("move"), Fit: id, At: now, By: by, Was: was, Now: fallback,
				Reason: reason, Stood: r.ID,
			}); err != nil {
				return move{}, err
			}
		case errors.Is(err, sql.ErrNoRows):
		default:
			return move{}, err
		}
	}

	served := r.Served
	if want == roleChampion && served == "" {
		served = stamp(now)
	}
	retired := r.Retired
	if want == roleRetired {
		retired = stamp(now)
	}
	if _, err := tx.Exec(`UPDATE fit SET role = ?, served = ?, retired = ? WHERE id = ?`,
		want, served, retired, r.ID); err != nil {
		return move{}, err
	}
	m := move{ID: newID("move"), Fit: r.ID, At: now, By: by, Was: r.Role, Now: want, Reason: reason, Stood: stood}
	if err := putMoveTx(tx, m); err != nil {
		return move{}, err
	}
	return m, tx.Commit()
}

func putMoveTx(tx *sql.Tx, m move) error {
	_, err := tx.Exec(`INSERT INTO fit_move (id, fit, at, by, was, now, reason, stood)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		m.ID, m.Fit, stamp(m.At), m.By, m.Was, m.Now, m.Reason, m.Stood)
	return err
}

// ── admission: horizon, split, disposition ──────────────────────────────────

// disposition maps this plane's review vocabulary onto the ENGINE's, which is
// the one the exhaustive search already ranks by (luxfi/aml pkg/replay). A
// second vocabulary would leave topology.Search unable to name a winner over
// rows this registry produced, which is the one thing it refuses to fake.
func disposition(label string) replay.Disposition {
	switch label {
	case "fraud", "chargeback", "abuse":
		return replay.Productive
	case "legitimate":
		return replay.Unproductive
	}
	return replay.Unjudged
}

// matured selects the rows a fit may read: inside the window, and OLD ENOUGH
// that no further label can arrive to change what they mean.
//
// (rule.go's admit is the rule grammar's term-admission check. Same English
// word, unrelated act — so this one takes the name of what it actually returns.)
//
// The horizon is the whole of it. A dispute lands 30 to 120 days after the
// payment it judges. Admit a payment from last week and its label is "whatever
// we know so far", which for the productive class is almost always "nothing" —
// so the model learns that recent payments are clean, which is not a fact about
// payments, it is a fact about the calendar.
func matured(rows []replayed, from, to time.Time, horizon int, now time.Time) []replayed {
	mature := now.AddDate(0, 0, -horizon)
	out := make([]replayed, 0, len(rows))
	for _, r := range rows {
		at := r.obs.at
		if at.Before(from) || !at.Before(to) || at.After(mature) {
			continue
		}
		out = append(out, r)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].obs.at.Before(out[j].obs.at) })
	return out
}

// split cuts the admitted rows into train and test.
//
// TEMPORAL FIRST, THEN WHOLE SUBJECTS. The cut is a point in time — a model is
// used on tomorrow, so it is measured on later rows than it learned from. Then
// every subject that straddles the cut is pulled ENTIRELY to the side its first
// row fell on, because the same account on both sides lets the model recognise
// the account instead of the behaviour, and the measurement then reports how
// well it memorises.
//
// A CONSEQUENCE WORTH STATING, because it surprises people and it is correct:
// the held-out side ends up holding only subjects FIRST SEEN after the cut. So
// the measurement is out-of-time and out-of-entity at once, which is the
// question that matters — how does this model do on an account it has never
// met. It also means a window with little entity churn holds out almost
// nothing, and that case is REFUSED by the caller rather than reported (see
// lifecycle.go's minHeld): a scorecard measured on four rows is worse than none.
//
// Returns train, test and the instant the cut fell on.
func split(rows []replayed, share float64) ([]replayed, []replayed, time.Time) {
	if len(rows) == 0 {
		return nil, nil, time.Time{}
	}
	n := int(float64(len(rows)) * share)
	if n <= 0 {
		n = 1
	}
	if n >= len(rows) {
		n = len(rows) - 1
	}
	cut := rows[n].obs.at

	// First appearance decides a subject's side, so the assignment is a pure
	// function of the ordered rows and two runs cannot disagree.
	side := make(map[string]bool, len(rows))
	for i, r := range rows {
		if _, seen := side[r.obs.subject]; !seen {
			side[r.obs.subject] = i < n
		}
	}
	var train, test []replayed
	for _, r := range rows {
		if side[r.obs.subject] {
			train = append(train, r)
		} else {
			test = append(test, r)
		}
	}
	return train, test, cut
}

// sourceDigest fingerprints exactly what was admitted and how it was split. Two
// fits over one window agree on it or the registry says they do not — which is
// the only form of reproducibility claim this plane is in a position to make.
func sourceDigest(train, test []replayed, dims []string) string {
	h := sha256.New()
	fmt.Fprintf(h, "v1|%s|", strings.Join(dims, ","))
	for _, part := range []struct {
		tag  string
		rows []replayed
	}{{"train", train}, {"test", test}} {
		ids := make([]string, 0, len(part.rows))
		for _, r := range part.rows {
			ids = append(ids, r.obs.id)
		}
		sort.Strings(ids)
		fmt.Fprintf(h, "%s:%s|", part.tag, strings.Join(ids, ","))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// fitDigest fingerprints the ARTEFACT — algorithm, geometry, the rows and split,
// and the feature inventory read. It is what a caller pins and what an auditor
// compares. Metrics are deliberately outside it: they are derived from the same
// four things, so including them would make the digest depend on itself.
func fitDigest(algo string, shape candidate, src fitSource) string {
	h := sha256.New()
	fmt.Fprintf(h, "v1|%s|%d|%d|%d|%g|%g|%s|%s|%d|%s|",
		algo, shape.Trees, shape.Depth, shape.Window, shape.Blend, shape.Review,
		src.Name, src.Digest, src.Horizon, src.Inventory)
	return hex.EncodeToString(h.Sum(nil))
}

// ── the estimation ──────────────────────────────────────────────────────────

// newSeed mints a version's tree geometry.
//
// IT MUST BE FIXED, AND IT MUST NOT BE DERIVABLE. Two requirements that pull
// opposite ways, and the earlier answer got the second one wrong.
//
// Fixed, because otherwise score drift cannot be measured at all: left at zero
// anomaly draws the seed from the system CSPRNG at construction, so a fit's
// sandbox and the drift reading's sandbox hold DIFFERENT TREES and produce
// different scores on identical rows. The index between their distributions is
// then geometry noise — measured at 0.11 on byte-identical data before the seed
// was pinned, which sits inside the reporting band and is therefore worse than a
// large error, because it reads as a small finding.
//
// Not derivable, because the geometry IS the secret. anomaly's own warning says
// it: a predictable geometry can be probed for a region to hide activity in.
// Deriving it from the version's identifier made it public — the identifier is
// on every /v1/ml/fits response, the shape beside it, so anyone holding a key
// for their OWN org could stand up an exact replica of the forest deciding their
// traffic and search it for a quiet region. A version's seed is drawn here from
// the system CSPRNG, kept in the tenant's own encrypted file (the profile the
// wire record does not carry, and the snapshot beside it), and read back from
// there by drift. Nothing published determines it.
func newSeed() (uint64, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("risk: a version's geometry could not be drawn: %w", err)
	}
	return binary.BigEndian.Uint64(b[:]), nil
}

// sandbox is a fresh model over fresh rings at one geometry. Nothing outside it
// is touched: an estimation can never move the live model's counters and two
// tenants' sandboxes can never see each other's rings.
func sandbox(shape candidate, seed uint64) (*anomaly.Store, *velocity.Store, error) {
	vel := velocity.New(velocity.Config{})
	model, err := anomaly.New(anomaly.Config{
		Trees: shape.Trees, Depth: shape.Depth, Window: shape.Window, Blend: shape.Blend,
		Appetite: anomaly.Appetite{Review: shape.Review, Sample: 0.001},
		Shadow:   true, // a sandbox never alerts for real
		Seed:     seed,
	}, vel)
	if err != nil {
		return nil, nil, err
	}
	return model, vel, nil
}

// project is THE projection, and it is one function on purpose.
//
// A fit's reference distributions and a drift reading's live distributions have
// to be produced the same way or the comparison between them is meaningless —
// and "the same way" is not something two call sites can be trusted to stay. Two
// code paths building one distribution WILL drift apart in the binning, in the
// burn-in, or in which rows they count, and every drift index computed across
// them would then be measuring the difference between the two implementations.
// So both go through here. That is the same argument the engine makes for
// keeping one feature projection behind Assess and Inspect.
//
// ONLY WARM ROWS ARE BINNED. A cold sandbox spends its first Appetite.Warm rows
// with most entity-relative coordinates at their neutral value, because the
// entity has no history yet. Bin those and the distribution is dominated by how
// long the burn-in was rather than by the traffic — so a reference built over
// 500 rows and a live profile built over 900 would differ by construction, on
// identical data. Warm rows are the ones the deciding model actually reads, and
// they are the ones drift is a statement about.
func project(ctx context.Context, t Tenant, model *anomaly.Store, vel *velocity.Store, rows []replayed) (
	fitProfile, projected, error,
) {
	prof := fitProfile{Dims: map[string][]float64{}}
	bins := map[string][]float64{}
	score := make([]float64, scoreBands)
	var p projected
	for _, r := range rows {
		select {
		case <-ctx.Done():
			return fitProfile{}, p, ctx.Err()
		default:
		}
		record(vel, t, r.obs)
		tx, ent := txOf(t, r.obs)
		a := model.Inspect(tx, ent)
		if a.Scored {
			p.scored++
			score[binOf(a.Score, scoreBands)]++
			for _, v := range a.Values {
				b, ok := bins[v.Feature]
				if !ok {
					b = make([]float64, profileBins)
				}
				b[binOf(v.X, profileBins)]++
				bins[v.Feature] = b
			}
			switch disposition(r.label) {
			case replay.Productive:
				p.judged++
				p.productive++
			case replay.Unproductive:
				p.judged++
			}
		} else {
			p.warm++
		}
		// Assess is what advances the forest. Inspect above reads the same
		// projection without learning, which is how the coordinates are read out
		// without being counted twice.
		_, _ = model.Assess(tx, ent)
	}
	for name, b := range bins {
		prof.Dims[name] = normalise(b)
	}
	prof.Score = normalise(score)
	if p.judged > 0 {
		prof.Rate = ptr(round4(float64(p.productive) / float64(p.judged)))
	}
	p.rows = len(rows)
	prof.Scored = p.scored
	return prof, p, nil
}

// projected is what one projection pass saw.
type projected struct {
	rows       int
	scored     int
	warm       int
	judged     int
	productive int
}

// estimate fits one shape to one tenant's admitted history and measures it on
// the held-out split.
//
// The two phases are deliberately different. On TRAIN the rows go through
// project, which records them, reads their coordinates and ASSESSES them — that
// last call is what advances the forest. On TEST they are recorded, because the
// aggregates must still advance or every feature reads as a first sighting, and
// only INSPECTED, which scores without learning. That asymmetry is what makes
// the split held out rather than decorative.
func estimate(ctx context.Context, t Tenant, shape candidate, seed uint64, train, test []replayed) (
	anomaly.Snapshot, fitMetrics, fitProfile, error,
) {
	model, vel, err := sandbox(shape, seed)
	if err != nil {
		return anomaly.Snapshot{}, fitMetrics{}, fitProfile{}, err
	}
	prof, _, err := project(ctx, t, model, vel, train)
	if err != nil {
		return anomaly.Snapshot{}, fitMetrics{}, fitProfile{}, err
	}
	prof.Seed = seed

	m := fitMetrics{Rows: len(test)}
	var alerted, rest []float64
	var scores []float64
	var positive []bool
	var pos, neg int
	for _, r := range test {
		select {
		case <-ctx.Done():
			return anomaly.Snapshot{}, fitMetrics{}, fitProfile{}, ctx.Err()
		default:
		}
		record(vel, t, r.obs)
		tx, ent := txOf(t, r.obs)
		a := model.Inspect(tx, ent)
		if !a.Scored {
			m.Warm++
			continue
		}
		m.Scored++
		// STRICTLY above, which is the engine's own rule (anomaly.weigh): a score
		// equal to the cut is inside the appetite. Restating it as >= here would
		// make the fit's realised share disagree with the live model's over
		// exactly the rows that sit on the line.
		if a.Score > a.Cut {
			m.Alerted++
			alerted = append(alerted, a.Score)
		} else {
			rest = append(rest, a.Score)
		}
		switch disposition(r.label) {
		case replay.Productive:
			m.Judged++
			pos++
			scores = append(scores, a.Score)
			positive = append(positive, true)
		case replay.Unproductive:
			m.Judged++
			neg++
			scores = append(scores, a.Score)
			positive = append(positive, false)
		}
	}

	if m.Scored > 0 {
		m.Realised = ptr(round4(float64(m.Alerted) / float64(m.Scored)))
	}
	if len(alerted) > 0 && len(rest) > 0 {
		m.Separation = ptr(round4(mean(alerted) - mean(rest)))
	}
	if m.Judged > 0 {
		p := round4(float64(pos) / float64(m.Judged))
		m.Prevalence = &p
	}
	if pos > 0 && neg > 0 {
		m.AUC = ptr(round4(auc(scores, positive)))
		if lift, ok := liftAt(scores, positive, shape.Review); ok {
			m.Lift = ptr(round4(lift))
		}
	}

	snap, ok := model.Snapshot(t.String())
	if !ok {
		// The forest holds nothing for this tenant, which after a train pass
		// means the pass admitted no row the model could key on. Saying so is the
		// answer; a snapshot of nothing restored later would be a model that
		// scores from state it never had.
		return anomaly.Snapshot{}, m, prof, errNoLearned
	}
	return snap, m, prof, nil
}

var errNoLearned = errors.New("risk: the training split produced no learned state, so there is nothing to serve")

// profileBins is how finely a feature's coordinate distribution is kept. Ten:
// the population-stability index is conventionally read over deciles, and a
// finer grid makes the index dominated by sampling noise in the tails.
const profileBins = 10

// scoreBands is how finely the score distribution is kept. Thirty-two, which is
// the granularity the engine's own State reports, so an operator comparing this
// profile against a live State is comparing like with like.
//
// It is accumulated HERE, over warm rows only, rather than read off the engine's
// lifetime histogram — that one includes every pre-warm row, so its shape is a
// function of how long the replay was, and a reference over 700 rows would
// differ from a window of 1000 on identical traffic. A drift index that moves
// with the window length is measuring the window.
const scoreBands = 32

func binOf(x float64, n int) int {
	if math.IsNaN(x) || x <= 0 {
		return 0
	}
	i := int(x * float64(n))
	if i >= n {
		return n - 1
	}
	return i
}

func normalise(h []float64) []float64 {
	var sum float64
	for _, v := range h {
		sum += v
	}
	if sum == 0 {
		return h
	}
	out := make([]float64, len(h))
	for i, v := range h {
		out[i] = v / sum
	}
	return out
}

func ptr(f float64) *float64 { return &f }

// auc is the area under the ROC curve, by the rank identity: the probability a
// randomly drawn productive row outranks a randomly drawn unproductive one.
//
// Computed from MID-RANKS rather than by sweeping a threshold, so ties count as
// half a win in both directions — which matters here because a warming forest
// returns the same score for whole runs of rows and a naive sweep would score
// those as clean wins.
//
// Written out rather than pulled from a statistics library because it is this
// long, it is exact, and the alternative is a direct dependency carried for one
// call. gonum is present transitively and would serve; it would also be a second
// place this plane's arithmetic lives.
func auc(scores []float64, positive []bool) float64 {
	n := len(scores)
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return scores[idx[a]] < scores[idx[b]] })

	ranks := make([]float64, n)
	for i := 0; i < n; {
		j := i
		for j+1 < n && scores[idx[j+1]] == scores[idx[i]] {
			j++
		}
		mid := float64(i+j)/2 + 1 // ranks are one-based
		for k := i; k <= j; k++ {
			ranks[idx[k]] = mid
		}
		i = j + 1
	}
	var sum float64
	var pos, neg int
	for i, p := range positive {
		if p {
			sum += ranks[i]
			pos++
		} else {
			neg++
		}
	}
	if pos == 0 || neg == 0 {
		return 0
	}
	return (sum - float64(pos)*float64(pos+1)/2) / (float64(pos) * float64(neg))
}

// liftAt is precision in the top share of the ranking divided by prevalence:
// how many times better than chance the slice an analyst actually looks at is.
// One means the model has bought nothing; it is the number an operator can
// reason about without knowing what an AUC is.
func liftAt(scores []float64, positive []bool, share float64) (float64, bool) {
	n := len(scores)
	take := int(math.Ceil(float64(n) * share))
	if n == 0 || take <= 0 || take > n {
		return 0, false
	}
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return scores[idx[a]] > scores[idx[b]] })
	var hits, all int
	for i := 0; i < take; i++ {
		if positive[idx[i]] {
			hits++
		}
	}
	for _, p := range positive {
		if p {
			all++
		}
	}
	if all == 0 {
		return 0, false
	}
	base := float64(all) / float64(n)
	if base == 0 {
		return 0, false
	}
	return (float64(hits) / float64(take)) / base, true
}

// ── the contract ────────────────────────────────────────────────────────────

// mountFit registers the lifecycle plane's leaves on the shared /v1/ml group.
//
// It declares its OWN group at the same prefix rather than taking one as a
// parameter, because cmd/zipdoc resolves a route's path from the
// `g := <router>.Group("/prefix")` assignment IN THE FILE THAT REGISTERS IT — a
// group arriving as an argument is a prefix it cannot see, and prose filed under
// a path that does not exist is silently dropped from the document and from the
// MCP tool list.
//
// cloud.Bridge is NOT reinstalled here. mount() installed it on this prefix
// before its own leaves and fiber matches middleware by registration order, so
// everything registered after it — including all of this — is behind it. A
// second Bridge would run the identity plumbing twice per request for no gain.
// typed_wire_test.go proves the chain rather than asserting it: an op below
// answers 200 for a validated principal, which it can only do if the org was
// parked before it ran.
func mountFit(app cloud.Router, o ops) {
	gml := app.Group("/v1/ml")

	zip.Post(gml, "/fits", o.fit,
		zip.WithOperationID("mlFit"),
		zip.WithSummary("Estimate a new version of this tenant's model"),
		zip.WithStatus(202),
		zip.WithTags("ml"))
	zip.Get(gml, "/fits", o.fits,
		zip.WithOperationID("mlFits"),
		zip.WithSummary("List this tenant's model versions"),
		zip.WithTags("ml"))
	zip.Get(gml, "/fits/:id", o.fitDetail,
		zip.WithOperationID("mlFitDetail"),
		zip.WithSummary("Read one model version with its lineage and role history"),
		zip.WithTags("ml"))
	zip.Post(gml, "/fits/:id/cancel", o.cancelFit,
		zip.WithOperationID("mlCancelFit"),
		zip.WithSummary("Cancel an estimation that is queued or running"),
		zip.WithTags("ml"))
	zip.Put(gml, "/fits/:id/role", o.setFitRole,
		zip.WithOperationID("mlSetFitRole"),
		zip.WithSummary("Promote a version, put it on trial, roll back to it, or retire it"),
		zip.WithTags("ml"))
	zip.Get(gml, "/fits/:id/tally", o.fitTally,
		zip.WithOperationID("mlFitTally"),
		zip.WithSummary("Read how a challenger answered the same stream as the champion"),
		zip.WithTags("ml"))

	zip.Get(gml, "/schedule", o.schedule,
		zip.WithOperationID("mlSchedule"),
		zip.WithSummary("Read when this tenant's model is re-estimated"),
		zip.WithTags("ml"))
	zip.Put(gml, "/schedule", o.setSchedule,
		zip.WithOperationID("mlSetSchedule"),
		zip.WithSummary("Set when this tenant's model is re-estimated"),
		zip.WithTags("ml"))

	zip.Get(gml, "/drift", o.drift,
		zip.WithOperationID("mlDrift"),
		zip.WithSummary("Measure whether the deciding model still fits the world it was estimated on"),
		zip.WithTags("ml"))
}

// ── the shapes ──────────────────────────────────────────────────────────────

// mlFitIn asks for one estimation.
//
// It is answered 202 with a queued record and never runs in the request: an
// estimation replays thousands of rows through a fresh forest on a pod that is
// also serving authorisations, and a caller able to hold that open is a caller
// able to take the decision plane down.
type mlFitIn struct {
	// Algo is the estimator. "forest" is the streaming half-space forest the
	// decision path runs, and today it is the only one this plane can estimate;
	// anything else is refused by name rather than silently substituted.
	Algo string `json:"algo,omitempty"`
	// Shape is the detector's geometry. Absent takes the shape currently
	// running, which is what makes "re-estimate what I already have on fresher
	// data" the default rather than a thing to spell out.
	Shape *mlCandidate `json:"shape,omitempty"`
	// Horizon is how many days a row must have aged before it may be admitted.
	// It is what keeps a judgement that did not exist at scoring time out of the
	// set: a dispute lands 30 to 120 days after the payment it judges, so 120 is
	// the default and a shorter one is a deliberate statement about a faster lane.
	Horizon int `json:"horizon,omitempty"`
	// Window is how many days of this tenant's own history to read. Default 365.
	Window int `json:"window,omitempty"`
	// Rows caps how many recorded observations are replayed, 1..5000.
	Rows int `json:"rows,omitempty"`
	// Note is why this estimation was asked for. It is kept on the record
	// because "who asked for this model and why" is the first question anyone
	// reviewing a decline asks.
	Note string `json:"note,omitempty"`
}

// mlFitRecord is one registry entry.
//
// LEARNED STATE IS NOT ON IT AND WILL NOT BE. The mass counters describe where a
// tenant's activity is dense, and handing them out over an API publishes the
// shape of that tenant's customers to anyone holding a key for it. Digest is
// what a caller pins; the state stays in the tenant's own encrypted file.
type mlFitRecord struct {
	// ID identifies the version.
	ID string `json:"id"`
	// At is when it was asked for, RFC 3339 in UTC.
	At string `json:"at"`
	// By is who asked for it — the note the request carried, or the trigger's own
	// name when the schedule asked. "Who wanted this model" is the first question
	// anyone reviewing a decline asks.
	By string `json:"by"`
	// Algo is the estimator that produced it.
	Algo string `json:"algo"`
	// Shape is the geometry it was estimated at.
	Shape mlCandidate `json:"shape"`
	// Digest fingerprints the artefact: the estimator, the geometry, the rows and
	// their split, and the feature inventory read. It does not move once the fit
	// is ready, which is what "immutable" means here and what a test asserts.
	Digest string `json:"digest,omitempty"`
	// Role is candidate, challenger, champion or retired. Exactly one champion
	// and at most one challenger per tenant.
	Role string `json:"role"`
	// Status is queued, fitting, ready, refused or cancelled.
	Status string `json:"status"`
	// Refusal says why there is no artefact when there is none. It is stated
	// rather than returned as a fit with an invented score — most often it is
	// that too little of the window had matured for a measurement to mean
	// anything, which is a fact about the data and not a failure of the plane.
	Refusal string `json:"refusal,omitempty"`
	// Served is when it first decided real traffic, empty when it never has.
	Served string `json:"served,omitempty"`
	// Retired is when it was last stood down.
	Retired string `json:"retired,omitempty"`
	// Source is the window it was estimated over.
	Source mlFitSource `json:"source"`
	// Metrics are measured on the held-out split.
	Metrics mlFitMetrics `json:"metrics"`
}

// mlFitSource is what a version was estimated from, recorded as a value at
// estimation time and never recomputed. A lineage that is a query to be re-run
// answers differently once the rows behind it age out, which is the difference
// between lineage and a claim of lineage.
type mlFitSource struct {
	// Name is the surface read: this tenant's own recorded decisions.
	Name string `json:"name"`
	// Version is monotone per tenant per surface, so two versions can always be
	// ordered and neither can claim the other's number.
	Version int `json:"version"`
	// From is the start of the window read, RFC 3339.
	From string `json:"from"`
	// To is the end of the window read, half-open, RFC 3339.
	To string `json:"to"`
	// Horizon is how many days a row had aged before it was admitted.
	Horizon int `json:"horizon"`
	// Cut is where the split fell. It is a point in TIME, and every row of one
	// subject then goes wholly to one side: a random split puts the same account
	// on both sides and the model memorises the account instead of the behaviour.
	Cut string `json:"cut,omitempty"`
	// Rows is how many observations were admitted after the horizon.
	Rows int `json:"rows"`
	// Train is how many of them the model learned from.
	Train int `json:"train"`
	// Test is how many were held out for the measurement.
	Test int `json:"test"`
	// Judged is how many held-out rows carry a matured judgement. It is the
	// number that decides whether any of the metrics below exist at all.
	Judged int `json:"judged"`
	// Productive is how many judged rows were confirmed as the thing being
	// looked for. Reported beside Unproductive so imbalance is visible before
	// anyone reads a metric.
	Productive int `json:"productive"`
	// Unproductive is how many judged rows were confirmed as ordinary.
	Unproductive int `json:"unproductive"`
	// Dims is the feature set, in the order the model reads it.
	Dims []string `json:"dims,omitempty"`
	// Inventory is the engine's own shape digest. State estimated under one
	// inventory cannot be restored into another, and this is the value that
	// decides it — so a fit whose inventory no longer matches the running one is
	// visibly unservable rather than silently wrong.
	Inventory string `json:"inventory,omitempty"`
	// Digest fingerprints the admitted rows and their split assignment.
	Digest string `json:"digest,omitempty"`
}

// mlFitMetrics is measured on the held-out split, never on the rows the model
// learned from.
//
// EVERY RATE IS ABSENT RATHER THAN ZERO when it could not be measured. A rate
// reported as 0.0 because nothing was judged reads as a perfect model, and the
// one thing this plane must not do is present the absence of a measurement as a
// good measurement.
type mlFitMetrics struct {
	// Rows is how many held-out rows were replayed.
	Rows int `json:"rows"`
	// Scored is how many of them the model was warm enough to score. Everything
	// below is measured over these rows and no others.
	Scored int `json:"scored"`
	// Warm is how many passed before the model would score at all. A shape that
	// warms slowly costs real coverage, and the cost is reported rather than
	// amortised away.
	Warm int `json:"warm"`
	// Alerted is how many scored rows crossed the threshold.
	Alerted int `json:"alerted"`
	// Judged is how many scored rows carry a matured judgement.
	Judged int `json:"judged"`
	// AUC is the probability the model ranks a productive row above an
	// unproductive one. Absent unless both classes are present.
	AUC *float64 `json:"auc,omitempty"`
	// Prevalence is the productive share of the judged rows. An AUC without it is
	// uninterpretable on a one-in-ten-thousand problem.
	Prevalence *float64 `json:"prevalence,omitempty"`
	// Separation is the mean score of the alerted set minus the mean of the rest
	// — the same objective the exhaustive search ranks its candidates on, so a
	// version and a search trial are comparable rather than merely adjacent.
	Separation *float64 `json:"separation,omitempty"`
	// Realised is Alerted over Scored against the appetite the shape states.
	Realised *float64 `json:"realised,omitempty"`
	// Lift is precision in the alerted slice divided by prevalence: how many
	// times better than chance the rows an analyst actually looks at are.
	Lift *float64 `json:"lift,omitempty"`
}

// mlFitsIn pages the registry.
type mlFitsIn struct {
	// Limit bounds the page, 1..200, default 50.
	Limit int `json:"limit,omitempty"`
}

// mlFitPage is the registry, newest first.
type mlFitPage struct {
	// Items are the versions.
	Items []mlFitRecord `json:"items"`
	// Champion is the identifier of the version deciding right now, empty when
	// the tenant runs the shipped default shape.
	Champion string `json:"champion,omitempty"`
	// Challenger is the version on trial beside it, if there is one.
	Challenger string `json:"challenger,omitempty"`
	// Estimating is the version currently being fitted, if any. One per tenant:
	// a second request is refused rather than queued.
	Estimating string `json:"estimating,omitempty"`
}

// mlFitDetailOut is one version with everything kept about it.
type mlFitDetailOut struct {
	// Fit is the record.
	Fit mlFitRecord `json:"fit"`
	// Moves is every role transition, oldest first. Appended and never amended:
	// a model that changed and cannot say who changed it is a control nobody owns.
	Moves []mlFitMove `json:"moves"`
	// Drift is every open degradation recorded against this version.
	Drift []mlDriftAlarm `json:"drift,omitempty"`
	// Servable reports whether this version's learned state is present and
	// restorable RIGHT NOW. A version that cannot be served cannot be promoted,
	// and knowing that before the promotion is the point.
	Servable bool `json:"servable"`
	// Refusal says why it is not servable, when it is not.
	Refusal string `json:"refusal,omitempty"`
}

// mlFitMove is one role transition.
type mlFitMove struct {
	// At is when it happened, RFC 3339.
	At string `json:"at"`
	// By is who decided, taken from the identity the edge validated and never
	// from the request body. A control that changed hands and cannot name who
	// moved it is a control nobody owns.
	By string `json:"by"`
	// Was is the role held before the transition.
	Was string `json:"was"`
	// Now is the role held after it.
	Now string `json:"now"`
	// Reason is why. It is required on a promotion because a control changing
	// hands with no stated reason is a change nobody can review.
	Reason string `json:"reason,omitempty"`
	// Stood is the version this one displaced, or the one that displaced it.
	Stood string `json:"stood,omitempty"`
}

// mlFitRoleIn moves a version's role.
//
// THERE IS NO SEPARATE ROLLBACK. A version is immutable and champion is a role,
// so rolling back is putting a prior version back in the champion role — the
// same act, the same record, the same one operation. A second mechanism would be
// a second thing to get wrong on the day it is needed most.
type mlFitRoleIn struct {
	// ID is the version, from the path.
	ID string `json:"id"`
	// Role is candidate, challenger, champion or retired.
	//
	// Reaching champion requires the version to have earned it: it must already
	// be the challenger, or it must be a version that has decided real traffic
	// before (which is what makes a rollback instant), or there must be no
	// incumbent to displace. An unproven model put straight into the decision
	// path is the failure this whole plane exists to prevent.
	Role string `json:"role"`
	// Reason is why. Required for champion and challenger.
	Reason string `json:"reason,omitempty"`
}

// mlTallyIn reads a challenger's side-by-side record.
type mlTallyIn struct {
	// ID is the version, from the path.
	ID string `json:"id"`
	// Limit bounds how many recent comparisons are counted, 1..5000.
	Limit int `json:"limit,omitempty"`
}

// mlTallyOut is how the two models answered the SAME stream.
//
// It reports agreement and the two alert shares and NOT a winner. A winner needs
// judged rows, the judgements arrive weeks later on the decisions themselves,
// and a challenger declared better on the strength of agreeing with the
// incumbent has measured which model reproduces the incumbent's policy.
type mlTallyOut struct {
	// Fit is the challenger being tallied.
	Fit string `json:"fit"`
	// Champion is the version it ran beside — the one whose answer was actually
	// returned to the caller on every one of these decisions.
	Champion string `json:"champion,omitempty"`
	// Rows is how many decisions both models saw.
	Rows int `json:"rows"`
	// Scored is how many the challenger was warm enough to score. A challenger
	// well below Rows here is still warming and its shares mean nothing yet.
	Scored int `json:"scored"`
	// Alerted is how many decisions the challenger would have alerted on.
	Alerted int `json:"alerted"`
	// Incumbent is how many the champion's recorded score crosses the
	// challenger's own threshold on — the same line applied to both models,
	// which is the comparison an operator wants.
	Incumbent int `json:"incumbent"`
	// Agreed is how often the two reached the same verdict.
	Agreed int `json:"agreed"`
	// Refusal says why there is nothing to report, when there is nothing.
	Refusal string `json:"refusal,omitempty"`
}

// ── the ops ─────────────────────────────────────────────────────────────────

// Fit estimates a new version of this tenant's model from its own recorded
// behaviour, and answers 202 with the queued record.
//
// It never runs in the request. The estimation replays up to five thousand rows
// through a fresh forest — seconds to minutes on a pod that is also serving
// authorisations — so it is queued behind four bounds: one in flight per tenant,
// two across the whole deployment, a ten-minute ceiling, and the row cap. The
// fifth is the caller's own ledger, debited here, before any of it is queued.
//
// Example: {"horizon": 120, "window": 365, "note": "quarterly refresh"}
func (o ops) fit(ctx context.Context, in *mlFitIn) (*mlFitRecord, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	by := strings.TrimSpace(in.Note)
	if by == "" {
		by = "requested"
	}
	now := time.Now()
	// Gated and metered inside enqueue, which is the ONE path a fit is created
	// by. Doing it here as well would be a second place for the price to live,
	// and the scheduler — which does not come through this op — would still not
	// pay it.
	id, err := enqueue(ctx, o.s, sc, db, by, roleCandidate, *in, now)
	if err != nil {
		return nil, err
	}

	r, err := getFit(db, id)
	if err != nil {
		return nil, err
	}
	return wireFit(r), nil
}

// Fits lists this tenant's model versions, newest first, and names which one is
// deciding, which is on trial and which is being estimated.
func (o ops) fits(ctx context.Context, in *mlFitsIn) (*mlFitPage, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	rows, err := listFits(db, in.Limit)
	if err != nil {
		return nil, err
	}
	out := &mlFitPage{Items: make([]mlFitRecord, 0, len(rows))}
	for _, r := range rows {
		out.Items = append(out.Items, *wireFit(r))
	}
	if r, ok, err := fitInRole(db, roleChampion); err == nil && ok {
		out.Champion = r.ID
	}
	if r, ok, err := fitInRole(db, roleChallenger); err == nil && ok {
		out.Challenger = r.ID
	}
	if id, ok := o.s.State.bench.running(sc.tenant, kindFit); ok {
		out.Estimating = id
	}
	return out, nil
}

// FitDetail reads one version with its lineage, its whole role history, any open
// drift recorded against it, and whether its learned state can be served right
// now. Answers 404 for an identifier this tenant does not own — never 403, which
// would distinguish "exists and is not yours" from "does not exist" and let a
// neighbour enumerate.
func (o ops) fitDetail(ctx context.Context, in *mlRef) (*mlFitDetailOut, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	r, err := getFit(db, in.ID)
	if err != nil {
		return nil, err
	}
	moves, err := fitMoves(db, r.ID)
	if err != nil {
		return nil, err
	}
	out := &mlFitDetailOut{Fit: *wireFit(r)}
	for _, m := range moves {
		out.Moves = append(out.Moves, mlFitMove{
			At: stamp(m.At), By: m.By, Was: m.Was, Now: m.Now, Reason: m.Reason, Stood: m.Stood,
		})
	}
	alarms, err := openDrift(db, r.ID)
	if err != nil {
		return nil, err
	}
	for _, a := range alarms {
		out.Drift = append(out.Drift, mlDriftAlarm{At: stamp(a.At), Says: a.Says, Rows: a.Rows, Scored: a.Scored})
	}
	if err := servable(o.s, db, r); err != nil {
		out.Refusal = err.Error()
	} else {
		out.Servable = true
	}
	return out, nil
}

// CancelFit stops an estimation that is queued or running. The record stays and
// says it was cancelled: a queue entry that vanishes leaves nobody able to tell
// a cancelled fit from one that was never asked for.
func (o ops) cancelFit(ctx context.Context, in *mlRef) (*mlFitRecord, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	r, err := getFit(db, in.ID)
	if err != nil {
		return nil, err
	}
	if r.Status != fitQueued && r.Status != fitFitting {
		return nil, zip.Errorf(409, "this fit is %s and there is nothing to cancel", r.Status)
	}
	o.s.State.bench.stop(sc.tenant, r.ID)
	if err := markFit(db, r.ID, fitCancelled, "cancelled"); err != nil {
		return nil, err
	}
	again, err := getFit(db, r.ID)
	if err != nil {
		return nil, err
	}
	return wireFit(again), nil
}

// SetFitRole moves a version between candidate, challenger, champion and
// retired. It is the ONE operation that changes which model decides.
//
// Promotion to champion has a precondition and it is the point of the whole
// plane: the version must already be the challenger, or must have decided real
// traffic before, or there must be no incumbent. So a new model reaches the
// decision path only after it has scored the live stream alongside the one it
// replaces — and a ROLLBACK, which is promoting a version that has served,
// passes immediately and always. The failure this prevents is the one worth
// preventing: the emergency where a rollback is refused by the same gate that
// should have stopped the promotion.
//
// The displaced champion keeps its learned state, which is why rolling back is
// instant rather than a re-warm.
//
// Example: {"role": "champion", "reason": "beat the incumbent over 14 days"}
func (o ops) setFitRole(ctx context.Context, in *mlFitRoleIn) (*mlFitRecord, error) {
	sc, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	want := strings.TrimSpace(in.Role)
	switch want {
	case roleCandidate, roleChallenger, roleChampion, roleRetired:
	default:
		return nil, zip.Errorf(422,
			"role must be one of candidate, challenger, champion, retired")
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" && (want == roleChampion || want == roleChallenger) {
		return nil, zip.Errorf(422,
			"putting a model into the decision path is a decision, and it is recorded with its reason")
	}
	r, err := getFit(db, in.ID)
	if err != nil {
		return nil, err
	}
	if r.Role == want {
		return wireFit(r), nil
	}
	if want == roleChampion || want == roleChallenger {
		if err := servable(o.s, db, r); err != nil {
			return nil, zip.Errorf(409, "%s", err.Error())
		}
	}
	if want == roleChampion {
		if err := earned(db, r); err != nil {
			return nil, err
		}
	}

	// Keep every resident model's state BEFORE the roles move. The champion
	// about to be displaced is holding counters this process has been advancing
	// since it started, and a rollback that came back to a stale snapshot would
	// be a rollback to a model that is warming.
	if err := keepAll(o.s, sc.tenant, db); err != nil {
		o.s.Log.Warn("risk: learned state was not kept before a role change",
			"tenant", sc.tenant.String(), "fit", r.ID, "err", err)
	}
	// WHO decided, from the identity the edge validated — never from the body.
	// The same helper the live/shadow switch records itself with, because "who
	// changed this control" must mean one thing across the plane.
	who := by(ctx, sc)
	now := time.Now()
	if _, err := setRole(db, r, want, who, reason, now); err != nil {
		return nil, err
	}
	// An alarm about a model that no longer decides is noise that hides the next
	// real one. The rows are closed, never deleted.
	if want == roleRetired || want == roleCandidate {
		if err := clearDrift(db, r.ID, now); err != nil {
			return nil, err
		}
	}
	// The cached role pair is this process's memory of who serves, and it has
	// just stopped being true. Dropping it is what makes the next request read
	// the file — and the next request is the one this promotion has to reach.
	o.s.State.stable.forget(sc.tenant)
	// Then load the incoming version into its own geometry NOW rather than on
	// that next request, so a promotion either takes effect or fails here, where
	// the caller is still listening.
	hydrate(o.s, sc.tenant, db)
	o.s.Log.Info("risk: a model version changed role",
		"tenant", sc.tenant.String(), "fit", r.ID, "was", r.Role, "now", want, "by", who)
	emit(sc.org, "risk.fit.role", map[string]any{"fit": r.ID, "was": r.Role, "now": want})

	again, err := getFit(db, r.ID)
	if err != nil {
		return nil, err
	}
	return wireFit(again), nil
}

// FitTally reads how a challenger answered the same stream as the champion:
// same decisions, same rings, same feature vector, two verdicts.
func (o ops) fitTally(ctx context.Context, in *mlTallyIn) (*mlTallyOut, error) {
	_, db, err := tenantState(ctx, o.s)
	if err != nil {
		return nil, err
	}
	r, err := getFit(db, in.ID)
	if err != nil {
		return nil, err
	}
	t, err := readTally(db, r.ID, in.Limit)
	if err != nil {
		return nil, err
	}
	out := &mlTallyOut{
		Fit: t.Fit, Champion: t.Champion, Rows: t.Rows, Scored: t.Scored,
		Alerted: t.Alerted, Incumbent: t.Incumbent, Agreed: t.Agreed,
	}
	if t.Rows == 0 {
		out.Refusal = "this version has not scored beside the champion, so there is nothing to compare"
	}
	return out, nil
}

// ── preconditions ───────────────────────────────────────────────────────────

// servable reports whether a version could serve traffic right now: it was
// estimated, its feature inventory is the one running, its geometry can be
// housed, and its learned state is present and restorable.
//
// The inventory check is the sharp one. A snapshot estimated under one feature
// set is refused by the engine on restore, so promoting such a version would
// leave the tenant with a champion whose store holds nothing — warming, and
// therefore declining to score, for as long as nobody noticed. Catching it at
// the promotion is catching it while somebody is still watching.
func servable(s *stateService, db *sql.DB, r fitRow) error {
	if r.Status != fitReady {
		return fmt.Errorf("this version is %s, so there is nothing to serve", r.Status)
	}
	if r.Source.Inventory != "" && r.Source.Inventory != s.State.digest {
		return errors.New("this version was estimated against a different feature inventory, and state estimated " +
			"under one inventory cannot be restored into another")
	}
	// The geometry, checked rather than housed. Housing it here would mint a seat
	// for a version nobody is serving, and evict one that is.
	if err := checkShape(r.Shape); err != nil {
		return err
	}
	if _, err := getModel(db, fitKey(r.ID)); err != nil {
		return errors.New("this version's learned state is not in this tenant's store, so promoting it would " +
			"leave the model warming and declining to score")
	}
	return nil
}

// trialFloor is how many decisions a challenger must have SCORED beside the
// incumbent before it may take the decision path. It is driftFloor's number for
// driftFloor's reason: below a couple of hundred scored rows the two shares the
// tally reports are noise with a decimal point, and a promotion justified by
// noise is an unmeasured model in front of customers wearing a measurement's
// clothes.
const trialFloor = driftFloor

// earned holds a promotion to the precondition that makes champion-challenger
// mean something.
//
// IT IS THE TALLY THAT EARNS IT, NOT THE ROLE. Reading the role alone made the
// gate two writes wide: PUT role=challenger then PUT role=champion promoted an
// unmeasured version over the incumbent with a tally of zero rows, which is the
// exact sequence the gate exists to refuse. So the question asked is the one the
// docstring always claimed — has this version scored the live stream beside the
// one it replaces — and it is answered from the comparisons actually recorded.
//
// The two exemptions stay, and they are the ones worth having: a version that
// has already DECIDED real traffic goes back instantly (that is a rollback, and
// a rollback refused by the promotion gate is the worst failure this plane has),
// and a first version with nothing to displace has no incumbent to be measured
// against.
func earned(db *sql.DB, r fitRow) error {
	if r.Served != "" {
		return nil // a rollback: this version has already decided real traffic
	}
	if _, ok, err := fitInRole(db, roleChampion); err != nil {
		return err
	} else if !ok {
		return nil // nothing to displace
	}
	if r.Role != roleChallenger {
		return zip.Errorf(409,
			"this version has never scored beside the incumbent; put it on trial as the challenger first, "+
				"or roll back to a version that has already decided")
	}
	t, err := readTally(db, r.ID, trialDepth)
	if err != nil {
		return err
	}
	if t.Scored < trialFloor {
		return zip.Errorf(409,
			"this version has scored %d of the %d decisions it rode along with, and a comparison needs %d before "+
				"it says anything; leave it on trial, or roll back to a version that has already decided",
			t.Scored, t.Rows, trialFloor)
	}
	return nil
}

// ── conversions ─────────────────────────────────────────────────────────────

func wireFit(r fitRow) *mlFitRecord {
	out := &mlFitRecord{
		ID: r.ID, At: stamp(r.At), By: r.By, Algo: r.Algo, Shape: mlCandidate(r.Shape),
		Digest: r.Digest, Role: r.Role, Status: r.Status, Refusal: r.Refusal,
		Served: r.Served, Retired: r.Retired,
		Source: mlFitSource{
			Name: r.Source.Name, Version: r.Source.Version,
			From: stamp(r.Source.From), To: stamp(r.Source.To), Horizon: r.Source.Horizon,
			Rows: r.Source.Rows, Train: r.Source.Train, Test: r.Source.Test,
			Judged: r.Source.Judged, Productive: r.Source.Productive, Unproductive: r.Source.Unproductive,
			Dims: r.Source.Dims, Inventory: r.Source.Inventory, Digest: r.Source.Digest,
		},
		Metrics: mlFitMetrics{
			Rows: r.Metrics.Rows, Scored: r.Metrics.Scored, Warm: r.Metrics.Warm,
			Alerted: r.Metrics.Alerted, Judged: r.Metrics.Judged,
			AUC: r.Metrics.AUC, Prevalence: r.Metrics.Prevalence, Separation: r.Metrics.Separation,
			Realised: r.Metrics.Realised, Lift: r.Metrics.Lift,
		},
	}
	if !r.Source.Cut.IsZero() {
		out.Source.Cut = stamp(r.Source.Cut)
	}
	return out
}
