package patent

// store.go is the durable record: one per-org SQLite (HIP-0302, physical-file
// isolation) holding the conception entries through hanzoai/orm's `_entities`.
//
// It is deliberately the same SHAPE Hanzo Research's store is (HIP-0512),
// restated over this app's kinds rather than reached across a process boundary:
// apps are separate binaries, so importing research's store would give patent a
// second owner of files research owns. Every property copied over is load-bearing
// for evidence, not for tidiness:
//
//   - VERSIONED, NOT WRITE-ONCE. A correction APPENDS a version under the same
//     stable id and the prior version is RETAINED, never mutated. A record you
//     could edit in place proves nothing about the moment it was written, which
//     is the only moment anyone is asking about.
//   - CONTENT HASH IS THE VERSION IDENTITY, computed by the SERVER over the
//     content it was handed. A byte-identical re-send is a no-op; any change
//     mints a new version. So the history cannot be quietly rewritten by
//     re-posting it.
//   - SEQ IS THE AUTHORITY, assigned inside the write transaction from a
//     per-store monotone counter and never taken from a client. That is what
//     makes "which came first" answerable at all: a record's place in the order
//     is a fact about this server's log, not a claim in a request body.
//   - PRIVATE BY DEFAULT. A write forces visibility='private'. Publication is
//     POST /v1/patent/grants, a separate authorization, never implied by
//     recording something.
//
// Three fields on every record — Seq, RecordedAt, AuthoredBy — are stamped here
// and by the handlers, and no input field reaches any of them. AuthoredBy is the
// validated principal, so authorship is a property of the CALL rather than a
// string in the body: a model has no subject and therefore cannot author
// anything here, which is 35 U.S.C. §115 expressed as a type rather than as a
// policy nobody checks.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/hanzoai/orm"
	ormdb "github.com/hanzoai/orm/db"
)

// The orm kinds this record store holds. The "patent-" prefix keeps them
// disjoint from every other subsystem's kinds in the one process-wide registry:
// patent and research can be linked into the same host, and a shared kind name
// would put two apps' records in one keyspace.
const (
	kindIdea     = "patent-idea"
	kindImpl     = "patent-implementation"
	kindDisc     = "patent-disclosure"
	kindApproval = "patent-approval"
	kindMeta     = "patent-meta"
	seqID        = "seq"
)

func init() {
	orm.Register[ideaRecord](kindIdea)
	orm.Register[implRecord](kindImpl)
	orm.Register[discRecord](kindDisc)
	orm.Register[approvalRecord](kindApproval)
	orm.Register[metaRecord](kindMeta)
}

// stamp is what the server adds to a record and no input can carry: the append
// clock, the wall time this store observed, and the validated subject that wrote
// it. It is embedded in the STORED records only — none of these types reaches the
// published document, where an embedded struct would have to be reasoned about
// separately.
type stamp struct {
	Seq        int64  `json:"seq"`
	RecordedAt string `json:"recorded_at"`
	AuthoredBy string `json:"authored_by"`
}

func (s stamp) clock() int64 { return s.Seq }

func (s *stamp) setSeq(n int64) { s.Seq = n }

type ideaRecord struct {
	stamp
	Project         string                      `json:"project"`
	ID              string                      `json:"id"`
	ContentHash     string                      `json:"content_hash"`
	Revision        string                      `json:"revision"`
	Kind            string                      `json:"kind"`
	Title           string                      `json:"title"`
	Statement       string                      `json:"statement"`
	Problem         string                      `json:"problem"`
	Alternatives    []PatentRejectedAlternative `json:"alternatives"`
	PriorArt        string                      `json:"prior_art"`
	PatentSignature *PatentSignature            `json:"signature,omitempty"`
	Visibility      string                      `json:"visibility"`
	Notes           string                      `json:"notes"`
}

func (r *ideaRecord) stable() string { return r.Project + "\x00" + r.ID }
func (r *ideaRecord) key() string    { return "i:" + hashJoin(r.Project, r.ID, r.ContentHash) }
func (r *ideaRecord) gone() bool     { return r.Revision == "retracted" }
func (r *ideaRecord) id() string     { return r.ID }

type implRecord struct {
	stamp
	Project        string   `json:"project"`
	ID             string   `json:"id"`
	ContentHash    string   `json:"content_hash"`
	Revision       string   `json:"revision"`
	IdeaIDs        []string `json:"idea_ids"`
	Repo           string   `json:"repo"`
	GitSHA         string   `json:"git_sha"`
	GitBranch      string   `json:"git_branch"`
	GitDirty       bool     `json:"git_dirty"`
	Model          string   `json:"model"`
	PromptSHA256   string   `json:"prompt_sha256"`
	Generated      bool     `json:"generated"`
	HumanReview    string   `json:"human_review"`
	ArtifactSHA256 string   `json:"artifact_sha256"`
	Visibility     string   `json:"visibility"`
	Notes          string   `json:"notes"`
}

func (r *implRecord) stable() string { return r.Project + "\x00" + r.ID }
func (r *implRecord) key() string    { return "m:" + hashJoin(r.Project, r.ID, r.ContentHash) }
func (r *implRecord) gone() bool     { return r.Revision == "retracted" }
func (r *implRecord) id() string     { return r.ID }

type discRecord struct {
	stamp
	Project           string   `json:"project"`
	ID                string   `json:"id"`
	ContentHash       string   `json:"content_hash"`
	Revision          string   `json:"revision"`
	Title             string   `json:"title"`
	Summary           string   `json:"summary"`
	Claims            string   `json:"claims"`
	IdeaIDs           []string `json:"idea_ids"`
	ImplementationIDs []string `json:"implementation_ids"`
	Body              string   `json:"body"`
	BodySHA256        string   `json:"body_sha256"`
	Status            string   `json:"status"`
	FilingRef         string   `json:"filing_ref"`
	Visibility        string   `json:"visibility"`
}

func (r *discRecord) stable() string { return r.Project + "\x00" + r.ID }
func (r *discRecord) key() string    { return "d:" + hashJoin(r.Project, r.ID, r.ContentHash) }
func (r *discRecord) gone() bool     { return r.Revision == "retracted" }
func (r *discRecord) id() string     { return r.ID }

// approvalRecord is one person's decision about one disclosure. Its stable id is
// (project, disclosure, author) and NOT the disclosure, because N signatures must
// coexist as N canonical approvals rather than supersede one another — while a
// person changing their own mind appends a version over their own prior word,
// which is retained too.
type approvalRecord struct {
	stamp
	Project      string `json:"project"`
	ID           string `json:"id"`
	DisclosureID string `json:"disclosure_id"`
	ContentHash  string `json:"content_hash"`
	Revision     string `json:"revision"`
	Role         string `json:"role"`
	Decision     string `json:"decision"`
	Comment      string `json:"comment"`
}

func (r *approvalRecord) stable() string {
	return r.Project + "\x00" + r.DisclosureID + "\x00" + r.AuthoredBy
}
func (r *approvalRecord) key() string {
	return "a:" + hashJoin(r.Project, r.DisclosureID, r.AuthoredBy, r.ContentHash)
}
func (r *approvalRecord) gone() bool { return r.Revision == "retracted" }
func (r *approvalRecord) id() string { return r.DisclosureID }

// metaRecord is the per-store append clock. Absent reads as 0.
type metaRecord struct {
	Seq int64 `json:"seq"`
}

// record is everything the shared write path needs of a kind, and it is
// satisfied by a pointer to each one: identity, order, withdrawal, its own id for
// sorting, and the one field the append transaction is allowed to set.
type record interface {
	stable() string
	key() string
	clock() int64
	gone() bool
	id() string
	setSeq(int64)
}

// store is one org's record store. db is orm's typed-record layer over the
// *sql.DB the org store already opened (cek-encrypted, single-writer, WAL); the
// store owns the Close, the caller owns the file.
type store struct {
	db  orm.DB
	raw *sql.DB
}

func openStore(raw *sql.DB) (*store, error) {
	db, err := orm.AdaptSQL(raw)
	if err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("patent open store: %w", err)
	}
	return &store{db: db, raw: raw}, nil
}

func (s *store) Close() error { return s.raw.Close() }

// canonical folds versions to one per stable id: the max-clock version, dropped
// when that latest version retracts the id, which then has NO canonical version
// at all. Every version stays retained regardless.
func canonical(recs []record) map[string]record {
	latest := map[string]record{}
	for _, r := range recs {
		if cur, ok := latest[r.stable()]; !ok || r.clock() > cur.clock() {
			latest[r.stable()] = r
		}
	}
	for k, r := range latest {
		if r.gone() {
			delete(latest, k)
		}
	}
	return latest
}

// loadAll reads every version of one kind. Per-org corpora are small, so the
// derived views fold in memory; the escalation when one stops being small is an
// orm-indexed filter, the same note the research store carries.
func loadAll[T any](ctx context.Context, s *store, kind string) ([]T, error) {
	var recs []T
	if _, err := s.db.Query(kind).GetAll(ctx, &recs); err != nil {
		return nil, fmt.Errorf("patent load %s: %w", kind, err)
	}
	return recs, nil
}

// put appends one batch of versions in a single transaction, idempotent by the
// content hash each already carries and stamped with the next per-store seq. It
// returns how many NEW versions landed, so a byte-identical re-send reports 0
// rather than duplicating an entry: a diary that grows when you re-read it is not
// a diary.
func put(ctx context.Context, s *store, kind string, recs []record) (int, error) {
	added := 0
	err := s.db.RunInTransactionWith(ctx, &orm.TxOptions{}, func(tx orm.DB) error {
		added = 0 // reset per attempt so a retried transaction never double-counts
		seq, e := loadSeq(ctx, tx)
		if e != nil {
			return e
		}
		for _, r := range recs {
			next := seq + 1
			r.setSeq(next)
			created, e := tx.CreateIfAbsent(ctx, tx.NewKey(kind, r.key(), 0, nil), r)
			if e != nil {
				return fmt.Errorf("%s insert: %w", kind, e)
			}
			if created {
				seq = next
				added++
			}
		}
		return putSeq(ctx, tx, seq)
	})
	if err != nil {
		return 0, fmt.Errorf("patent put %s: %w", kind, err)
	}
	return added, nil
}

func loadSeq(ctx context.Context, tx orm.DB) (int64, error) {
	var m metaRecord
	err := tx.Get(ctx, tx.NewKey(kindMeta, seqID, 0, nil), &m)
	if err != nil {
		if errors.Is(err, ormdb.ErrNoSuchEntity) {
			return 0, nil
		}
		return 0, err
	}
	return m.Seq, nil
}

func putSeq(ctx context.Context, tx orm.DB, seq int64) error {
	_, err := tx.Put(ctx, tx.NewKey(kindMeta, seqID, 0, nil), &metaRecord{Seq: seq})
	return err
}

// hashJoin digests parts with a NUL between each, so no two inputs that differ
// only in where the boundary falls can collide.
func hashJoin(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// digestOf is the server's content hash: the JSON of the value as encoding/json
// writes it, which is deterministic for a fixed type (struct fields in
// declaration order, map keys sorted). Only what the CALLER sent is hashed — the
// stamp is not part of it, so an identical re-send stays an identical version
// no matter when or by whom it arrives.
func digestOf(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", fmt.Errorf("patent digest: %w", err)
	}
	return hashJoin(string(b)), nil
}

// sortVersions orders records by the append clock, and by id only to break an
// exact tie. The clock is what a reader is asking about, so it is never
// approximated by a timestamp.
func sortVersions(recs []record) {
	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].clock() != recs[j].clock() {
			return recs[i].clock() < recs[j].clock()
		}
		return recs[i].id() < recs[j].id()
	})
}
