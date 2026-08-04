package risk

// address.go — A MODEL IS A VALUE, AND THIS IS ITS NAME.
//
// # The place that was here before
//
// The `model` table is keyed on the tenant and written with ON CONFLICT DO
// UPDATE, so it is ONE CELL per organisation and every write destroys the state
// before it. That single fact is what made a family of separate problems: there
// was nothing to roll back TO, so rollback needed an op that shipped the masses
// out to the caller and an op that took them back in; two fitted models could not
// both be named, so champion-and-challenger had nowhere to live; and no decision
// could say which model produced it, because the only answer available was "the
// cell, as it was at some instant, which is gone".
//
// A content address ends all of them at once. It is not a new abstraction over the
// cell — it is the cell stopping being the only name.
//
// # What is a value here, and what is emphatically NOT
//
// The in-process model STAYS MUTABLE, and that is the design rather than a
// concession. The model is mass counters over half-space trees incremented on
// every event, and one encoded value MEASURED AT THE REACHABLE SHAPE is 466 KiB:
// 25 trees x 511 nodes x two windows of float64 masses, every one carrying a full
// mantissa because folding a window blends them. The sweep writes every
// [saveEvery] events or every [saveInterval], so "one immutable value per write"
// is 466 KiB twice a minute for every active organisation — 56 MiB an hour, each,
// to record a counter going up. That is not a value plane, it is a landfill.
//
// So identity here is a SUCCESSION OF STATES and the value is the PUBLICATION:
//
//	THE WORKING STATE  in-process counters, and the `model` row that lets a
//	                   killed process resume them. A PLACE, deliberately: its
//	                   whole job is "what an ungraceful stop would lose". Nobody
//	                   names it, nobody cites it, nobody audits it. It is not a
//	                   snapshot and this file stops calling it one.
//	A PUBLISHED VALUE  minted at a boundary somebody marked, addressed by its own
//	                   content, immutable, and retained under a bound stated in
//	                   BYTES. THIS is what a rollback names, what a challenger is,
//	                   and what an adverse decision is reconstructed against.
//
// # What goes into the address, and what must not
//
// The address covers everything that makes two models answer the same event
// differently, and nothing else:
//
//	shape    the engine's own Digest: layout version, dimension count, trees,
//	         depth, blend, window, and the feature inventory in order. Masses are
//	         meaningless against a different one.
//	seed     the geometry generator. Identical masses under two seeds are two
//	         different partitions of the space and score differently, so a name
//	         that ignored the seed would call them one value.
//	position learned, seen and cut — how much is behind the masses, where in the
//	         open window they sit, and the threshold in force.
//	masses   Ref, Cur and Hist, hashed as IEEE-754 BITS and never as text. A
//	         decimal rendering is a lossy function of a float64, so two runs
//	         holding the same number could print it differently and be named
//	         apart. apps/dataset reached this conclusion first and this is the same
//	         reasoning applied to the same problem.
//	warmed   the FOLD WATERMARK: how far this organisation's own surface has
//	         already been folded in. Not redundant with `learned`. Two models with
//	         identical masses reached by different routes hold different beliefs
//	         about what remains to be folded, and one of them will re-teach history
//	         the other will not — so they are different values and a name that
//	         omitted the watermark would hide it.
//
// THE TENANT IS NOT IN THE ADDRESS, and that is the security decision in this
// file rather than an oversight. An address is a content address: the same content
// must produce the same name, or it is not one. Salting it with the organisation
// would make isolation depend on a name being UNGUESSABLE, and isolation that
// rests on an unguessable name is not isolation — it is obscurity with a hash in
// front of it. Isolation here is a PREDICATE and stays one: the shelf FILE is the
// organisation ([plane.for_] resolves it through [cloud.OrgNamespace]), and the
// qualified key is the leading bound term of every statement inside it. So an
// organisation holding another's exact address resolves NOTHING — not because it
// could not guess the name, but because the name is not the authority.
// [TestAddress_AForeignOrgResolvesNothing] is that claim, made falsifiable.
//
// It also has a use: two organisations whose models are literally identical get
// one address, and that is a FINDING about a fold gone wrong, not a leak. A salted
// name would have hidden it.
//
// # Labels are not in it either
//
// This model is UNSUPERVISED — half-space mass counters, fitted from behaviour
// with no disposition read anywhere (apps/risk imports neither the label plane nor
// the reference plane). So "which bytes did this learn from" is answered by the
// address plus this organisation's own observation record (ring.go), and a label
// is a MEASUREMENT taken beside the model rather than an input to it. Folding the
// label plane into a model's name would claim a dependency that does not exist.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/luxfi/aml/pkg/anomaly"
	"github.com/zap-proto/zip"
)

// address names one model value by its content.
//
// It is a PURE function of the value: no clock, no identity, no counter. Two
// processes handed the same masses agree on the name without talking, which is
// what makes publication idempotent and makes a rollback target nameable at all.
//
// The organisation is absent by design — see this file's header. [anomaly.Snapshot]
// carries an OrgID, and it is skipped: that field is the SLOT the engine plants
// into, not part of the mathematics, and it is set from the validated principal on
// the way in ([plane.adopt]) rather than trusted off a stored row.
func address(s anomaly.Snapshot, warmed time.Time) string {
	h := sha256.New()
	// Domain-separated, so a digest from this plane can never be mistaken for one
	// from another — apps/dataset writes "hanzo.dataset\x00" for the same reason.
	h.Write([]byte("hanzo.risk.model\x00"))
	h.Write([]byte(s.Digest))
	h.Write([]byte{0})
	_ = binary.Write(h, binary.BigEndian, uint32(s.Version))
	_ = binary.Write(h, binary.BigEndian, s.Seed)
	_ = binary.Write(h, binary.BigEndian, s.Learned)
	_ = binary.Write(h, binary.BigEndian, uint32(s.Seen))
	_ = binary.Write(h, binary.BigEndian, math.Float64bits(s.Cut))
	_ = binary.Write(h, binary.BigEndian, warmed.UTC().Unix())
	for _, m := range [][][]float64{s.Ref, s.Cur} {
		_ = binary.Write(h, binary.BigEndian, uint32(len(m)))
		for _, tree := range m {
			_ = binary.Write(h, binary.BigEndian, uint32(len(tree)))
			for _, v := range tree {
				_ = binary.Write(h, binary.BigEndian, math.Float64bits(v))
			}
		}
	}
	_ = binary.Write(h, binary.BigEndian, uint32(len(s.Hist)))
	for _, v := range s.Hist {
		_ = binary.Write(h, binary.BigEndian, math.Float64bits(v))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// addressBytes is the width of a rendered address: SHA-256 in hex.
const addressBytes = sha256.Size * 2

// admitAddress is the ONE door a caller-supplied address comes through: it is the
// exact width of a rendered SHA-256 and it is lower-case hex, or it is refused.
//
// It is a BOUND before it is a validation. Without it an address is an unbounded
// caller string reaching a query — parameterised, so not an injection, but still a
// caller deciding how much this process reads and compares. With it the only thing
// that reaches the store is something shaped like a name.
func admitAddress(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", zip.ErrBadRequest("'address' names one of your own published model values; GET /v1/risk/state reports them")
	}
	if len(s) != addressBytes {
		return "", zip.Errorf(400, "an address is %d hex characters; this one is %d", addressBytes, len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return "", zip.ErrBadRequest("an address is lower-case hex")
	}
	return strings.ToLower(s), nil
}

// maxModelBodyBytes is what ONE published value's encoded state may cost, and it is
// ENFORCED rather than assumed ([plane.mint] refuses past it). That is what makes the
// row bound below a bound: the body is the only term in it whose size is not already
// fixed, so a ceiling on the body is a ceiling on the row.
//
// IT IS SIZED ON THE WIDEST SHAPE THE SEARCH GRID DECLARES, rather than on the
// deployment's default topology. A published value used to only ever come from a
// residency running that default — 466 KiB measured, so 768 KiB with headroom was the
// whole story — because a search winner could not be adopted at all: install refused a
// shape change. It replants now, so 40 trees of 2047 regions over two windows is a
// value an organisation can legitimately publish and must therefore be able to store.
//
// MEASURED, at full occupancy: 2,214,100 bytes for that shape with every region carrying
// a full mantissa. Occupancy is the term that matters and the one a fitted sample gets
// wrong by an order of magnitude — a deep tree over a short history leaves most regions
// at zero, and a zero costs two bytes where a blended mass costs twenty. This is the
// busy organisation's number, with room for the grid to widen.
//
// [TestModel_TheWidestShapeFitsItsOwnBound] is that measurement, run rather than
// recorded, so a grid that grows past this bound turns a test red instead of turning up
// as a 413 on some organisation's adoption.
const maxModelBodyBytes = 3 << 20

// maxModelRowBytes is what ONE retained value costs on the organisation's own
// shelf, worst case, including its primary-key index and SQLite's own per-row
// overhead. Every term is a bound: the body by [maxModelBodyBytes], the address by
// [addressBytes], the key by [maxTenant], the shape by [addressBytes] again, and
// the four integers by their width.
const maxModelRowBytes = maxModelBodyBytes + addressBytes + maxTenant + addressBytes + 4*8 +
	(maxTenant + addressBytes + 8) + 256

// modelBudget is what ONE organisation's published model history may cost ON DISK.
// Bytes, not values, for the reason [recordBudget] and [policyBudget] are both in
// bytes: every organisation's shelf lives on one volume, so a history bounded only
// in rows is one organisation filling the disk another organisation's model is
// stored on.
//
// It moved with [maxModelBodyBytes] and for the same reason. The rollback depth this
// history is FOR — a champion, a challenger and eight points to go back to — is the
// requirement; the budget is what that depth costs once the widest shape a search can
// win in is a shape an organisation can hold. It is a CEILING and not an allocation:
// only an organisation that publishes ten of the widest values reaches it, where ten at
// the default shape cost under 5 MiB.
const modelBudget = 32 << 20

// modelValues is that budget in values, and it is both the bound the disposal
// enforces and the bound the read reports against, because those must be one
// number. At [maxModelRowBytes] it is ten: enough for a champion, a challenger and
// eight points to roll back to.
const modelValues = modelBudget / maxModelRowBytes

// publishedDDL is the value history, on the organisation's own shelf.
//
// APPEND-ONLY. There is no UPDATE against it anywhere and the only DELETE is
// [plane.disposeOldValues], which retention drives; TestPublished_IsAppendOnly
// holds that closed. That is the whole difference from the `model` table beside
// it, which is one overwritten cell per organisation and is meant to be.
//
// `seq` orders the history so retention can dispose of the oldest and so the count
// disposed of is DERIVABLE rather than stored: sequences are contiguous from 1, so
// the lowest surviving one minus one IS how many are gone. A figure that cannot
// drift from the thing it describes.
//
// The address is not the sole key. The tenant leads it for the same reason it
// leads every other statement in this package: the file is already the
// organisation, and the qualified key is what tells two brands' identically named
// organisations apart on the one file they share.
const publishedDDL = `CREATE TABLE IF NOT EXISTS published (
	tenant  TEXT    NOT NULL,
	address TEXT    NOT NULL,
	seq     INTEGER NOT NULL,
	body    BLOB    NOT NULL,
	shape   TEXT    NOT NULL,
	learned INTEGER NOT NULL,
	warmed  INTEGER NOT NULL,
	at      INTEGER NOT NULL,
	PRIMARY KEY (tenant, address)
)`

// publishedIndexDDL orders the history the way retention and the read walk it, so
// both are a range scan under the organisation and never a scan of the file.
const publishedIndexDDL = `CREATE INDEX IF NOT EXISTS published_seq ON published(tenant, seq)`

// value is one published model value as its own record: the name, what it is, and
// when it entered the history. The masses are NOT here — they are read only by
// [plane.adopt], which is the one caller that needs them, and they never reach a
// wire type.
type value struct {
	// Address names it by content.
	Address string
	// Seq is its position in THIS organisation's history, from 1 and contiguous
	// until retention disposes of the oldest.
	Seq int64
	// Shape is the model space the masses are only meaningful against.
	Shape string
	// Learned is how many events are behind the masses.
	Learned int64
	// Warmed is how far the organisation's own surface had been folded in.
	Warmed time.Time
	// At is the server clock when it was published. The organisation does not
	// supply it: a record whose date the audited party chose is not one.
	At time.Time
}

// model is one published value AS IT IS STORED: the masses, and the SHAPE they are
// only meaningful against.
//
// The two travel together because they are one value. Masses restored into a
// different space are not stale, they are meaningless — so a store that held the
// masses alone could only ever put them back into the space that was already running,
// which is precisely why a search winner was unadoptable. [plane.install] rebuilds
// the space from this shape and proves it by digest before believing a single mass.
//
// The masses are INLINE, so a body written before this plane recorded shapes decodes
// into exactly the same masses with the shape ABSENT — and absent is honest rather
// than defaulted: install refuses to replant onto a shape nobody recorded instead of
// guessing that it was the deployment's own.
type model struct {
	anomaly.Snapshot
	Shape shape `json:"shape"`
}

// publish mints the caller organisation's current working state as a value on its
// own shelf, and answers with the name.
//
// IT IS IDEMPOTENT ON THE VALUE, which is what a content address is for: an
// unchanged model publishes to the name it already has, mints nothing, and reports
// minted=false. So publishing at every boundary is free rather than the cheapest
// way to fill a disk — the same property that makes [plane.enact] safe to call on
// every deploy, reached here by content rather than by comparing fields.
//
// A model that has learned nothing is REFUSED, not published: planted is not
// learned, and a value that reproduces nothing is not a value.
func (p *plane) publish(t tenant) (value, bool, error) {
	r, err := p.resident(t)
	if err != nil {
		return value{}, false, err
	}
	r.mu.Lock()
	snap, held := r.mod.Snapshot(string(t))
	// THE SHAPE THE STORE IS ACTUALLY RUNNING, off the residency's own record of it
	// ([plane.plant] reads it back from the store rather than trusting what it was
	// handed). A value whose recorded shape does not produce its digest is refused on
	// adoption, so both must come from one place, and this is that place.
	m := model{Snapshot: snap, Shape: shapeOf(r.cfg)}
	warmed := r.warmed
	r.mu.Unlock()
	if !held || snap.Learned == 0 {
		return value{}, false, nil
	}
	return p.mint(t, m, warmed)
}

// mint records ONE model value on an organisation's own shelf, addressed by its
// content, and answers with the record and whether anything was written.
//
// It is the ONE writer of the value history. Two callers reach it — an organisation
// publishing its working state ([plane.publish]) and a search publishing the winning
// shape it has just fitted ([plane.fitWinner]) — and they differ only in which model
// they hand over. The bound, the address, the sequence, the idempotence and the
// retention are therefore one implementation rather than two that agree today.
func (p *plane) mint(t tenant, m model, warmed time.Time) (value, bool, error) {
	body, err := json.Marshal(m)
	if err != nil {
		return value{}, false, fmt.Errorf("risk: encode model value: %w", err)
	}
	if len(body) > maxModelBodyBytes {
		// The bound, named, in the dimension that binds. A body this plane cannot
		// retain is refused here rather than written and discovered by a full volume.
		return value{}, false, zip.Errorf(413,
			"this model encodes to %d bytes, over the %d-byte bound every retained value is a multiple of",
			len(body), maxModelBodyBytes)
	}
	sh, err := p.for_(t)
	if err != nil {
		return value{}, false, err
	}
	v := value{
		Address: address(m.Snapshot, warmed),
		Shape:   m.Digest,
		Learned: m.Learned,
		Warmed:  warmed.UTC().Truncate(time.Second),
		At:      p.now().UTC().Truncate(time.Second),
	}
	// Already published? Answer with the record that is already there. Reading it
	// back rather than reporting the value we just computed is deliberate: the
	// answer is then the HISTORY's account of it, including the sequence and the
	// clock from when it was first minted, which is what an audit asks for.
	if held, ok, err := p.valueAt(t, v.Address); err != nil {
		return value{}, false, err
	} else if ok {
		return held, false, nil
	}
	var high sql.NullInt64
	if err := sh.db.QueryRow(`SELECT MAX(seq) FROM published WHERE tenant = ?`, string(t)).Scan(&high); err != nil {
		return value{}, false, fmt.Errorf("risk: read model history: %w", err)
	}
	v.Seq = high.Int64 + 1
	if _, err := sh.db.Exec(
		`INSERT INTO published (tenant, address, seq, body, shape, learned, warmed, at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		string(t), v.Address, v.Seq, body, v.Shape, v.Learned, v.Warmed.Unix(), v.At.Unix(),
	); err != nil {
		return value{}, false, fmt.Errorf("risk: publish model value: %w", err)
	}
	if err := p.disposeOldValues(sh, t); err != nil {
		// The value LANDED. A disposal that failed is a retention that will be retried
		// on the next publication, not a publication to undo.
		p.log.Warn("model history is over its retention and could not be trimmed",
			"tenant", string(t), "err", err)
	}
	return v, true, nil
}

// valueAt reads one published value's RECORD by name, without its masses.
//
// The organisation is the leading bound predicate and the shelf is its own file,
// so another organisation's value is not merely filtered out — it is not in the
// file being read. That is what [TestAddress_AForeignOrgResolvesNothing] holds:
// an address is a name, never an authority.
func (p *plane) valueAt(t tenant, addr string) (value, bool, error) {
	sh, err := p.for_(t)
	if err != nil {
		return value{}, false, err
	}
	v := value{Address: addr}
	var warmed, at int64
	err = sh.db.QueryRow(
		`SELECT seq, shape, learned, warmed, at FROM published WHERE tenant = ? AND address = ?`,
		string(t), addr,
	).Scan(&v.Seq, &v.Shape, &v.Learned, &warmed, &at)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return value{}, false, nil
	case err != nil:
		return value{}, false, fmt.Errorf("risk: read model value: %w", err)
	}
	v.Warmed, v.At = time.Unix(warmed, 0).UTC(), time.Unix(at, 0).UTC()
	return v, true, nil
}

// modelAt reads one published value's MODEL — its masses and the shape they describe
// — for the one caller that needs them.
//
// It is separate from [plane.valueAt] because a model is hundreds of kilobytes and
// every other reader wants the record: a single function returning both would put that
// body on the path of every list and every report. Same organisation predicate, same
// file.
func (p *plane) modelAt(t tenant, addr string) (model, bool, error) {
	sh, err := p.for_(t)
	if err != nil {
		return model{}, false, err
	}
	var body []byte
	err = sh.db.QueryRow(`SELECT body FROM published WHERE tenant = ? AND address = ?`,
		string(t), addr).Scan(&body)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return model{}, false, nil
	case err != nil:
		return model{}, false, fmt.Errorf("risk: read model value: %w", err)
	}
	var m model
	if err := json.Unmarshal(body, &m); err != nil {
		return model{}, false, fmt.Errorf("risk: decode model value: %w", err)
	}
	return m, true, nil
}

// values reads an organisation's own published history, newest first, and says how
// many values retention has disposed of.
func (p *plane) values(t tenant, limit int) ([]value, int, error) {
	if limit <= 0 || limit > modelValues {
		limit = modelValues
	}
	sh, err := p.for_(t)
	if err != nil {
		return nil, 0, err
	}
	rows, err := sh.db.Query(
		`SELECT address, seq, shape, learned, warmed, at FROM published
		 WHERE tenant = ? ORDER BY seq DESC LIMIT ?`, string(t), limit)
	if err != nil {
		return nil, 0, fmt.Errorf("risk: read model history: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []value
	for rows.Next() {
		var (
			v          value
			warmed, at int64
		)
		if err := rows.Scan(&v.Address, &v.Seq, &v.Shape, &v.Learned, &warmed, &at); err != nil {
			return nil, 0, fmt.Errorf("risk: scan model value: %w", err)
		}
		v.Warmed, v.At = time.Unix(warmed, 0).UTC(), time.Unix(at, 0).UTC()
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("risk: read model history: %w", err)
	}
	return out, p.disposedValues(sh, t), nil
}

// disposedValues is how many values retention has taken, DERIVED from the lowest
// surviving sequence rather than counted: sequences are contiguous from 1, so a
// history whose oldest is 7 has lost 6. Zero for an organisation that has never
// published. Same derivation as [plane.disposed] over the policy history, for the
// same reason — a stored counter is a second account of one fact.
func (p *plane) disposedValues(sh *shelf, t tenant) int {
	var low sql.NullInt64
	if err := sh.db.QueryRow(`SELECT MIN(seq) FROM published WHERE tenant = ?`, string(t)).Scan(&low); err != nil {
		return 0
	}
	if !low.Valid || low.Int64 <= 1 {
		return 0
	}
	return int(low.Int64 - 1)
}

// disposeOldValues enforces the budget by disposing of the oldest values past
// [modelValues]. It is the only statement in this package that removes a published
// value, and what it removed stays countable.
func (p *plane) disposeOldValues(sh *shelf, t tenant) error {
	_, err := sh.db.Exec(
		`DELETE FROM published WHERE tenant = ? AND seq <= (
			SELECT MAX(seq) - ? FROM published WHERE tenant = ?
		)`, string(t), modelValues, string(t))
	if err != nil {
		return fmt.Errorf("risk: dispose model values: %w", err)
	}
	return nil
}

// descends is the published value the working state grew out of, DERIVED and never
// stored: it is the newest value whose mass count the working state has reached or
// passed.
//
// Deriving it is what keeps a rollback honest. Adopting an older value moves the
// count BACKWARD, and the same query then answers with that older value — where a
// stored head pointer would be a second fact to keep in step with the first. Every
// direction is right for free.
//
// A tie is possible and it is reported rather than hidden: two values can share a
// mass count when an organisation rolls back and then learns differently, and the
// most recently published of them is answered. What the working state descends
// from is then ambiguous BY CONSTRUCTION — the count is the only ordering the
// masses carry — and a caller that needs it exact publishes before it decides.
func (p *plane) descends(t tenant, learned int64) (value, bool, error) {
	sh, err := p.for_(t)
	if err != nil {
		return value{}, false, err
	}
	var (
		v          value
		warmed, at int64
	)
	err = sh.db.QueryRow(
		`SELECT address, seq, shape, learned, warmed, at FROM published
		 WHERE tenant = ? AND learned <= ? ORDER BY learned DESC, seq DESC LIMIT 1`,
		string(t), learned,
	).Scan(&v.Address, &v.Seq, &v.Shape, &v.Learned, &warmed, &at)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return value{}, false, nil
	case err != nil:
		return value{}, false, fmt.Errorf("risk: read model history: %w", err)
	}
	v.Warmed, v.At = time.Unix(warmed, 0).UTC(), time.Unix(at, 0).UTC()
	return v, true, nil
}
