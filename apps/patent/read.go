package patent

// read.go is the derived view: the canonical listing of each kind, the
// approval join a disclosure read needs, the version history of one entry, and
// the visibility grant a write never implied.
//
// RETAINED IS THE HISTORY AND CANONICAL IS THE VIEW OVER IT, and both counts are
// reported wherever one is — a dedupe that reads as deletion is exactly how a
// record-keeping system stops being trusted with records.

import (
	"context"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"
)

// ideas returns the canonical conception entries, oldest append first.
func (s *store) ideas(ctx context.Context, project, id, kind string) ([]PatentIdea, error) {
	recs, err := loadAll[ideaRecord](ctx, s, kindIdea)
	if err != nil {
		return nil, err
	}
	canon := canonical(recordsOf(pointers(recs)))
	out := make([]PatentIdea, 0, len(canon))
	for _, r := range sorted(canon) {
		ir := r.(*ideaRecord)
		if !inScope(ir.Project, project) || (id != "" && ir.ID != id) || (kind != "" && ir.Kind != kind) {
			continue
		}
		out = append(out, ideaView(ir, true))
	}
	return out, nil
}

// impls returns the canonical implementation records, oldest append first.
func (s *store) impls(ctx context.Context, project, id, idea string) ([]PatentImplementation, error) {
	recs, err := loadAll[implRecord](ctx, s, kindImpl)
	if err != nil {
		return nil, err
	}
	canon := canonical(recordsOf(pointers(recs)))
	out := make([]PatentImplementation, 0, len(canon))
	for _, r := range sorted(canon) {
		ir := r.(*implRecord)
		if !inScope(ir.Project, project) || (id != "" && ir.ID != id) || !cites(ir.IdeaIDs, idea) {
			continue
		}
		out = append(out, implView(ir, true))
	}
	return out, nil
}

// disclosures returns the canonical disclosures with their current approvals.
// The join is done over the whole approval set once rather than per disclosure:
// a disclosure read is one page of one org's log, and N queries for N documents
// would be N ways to disagree with each other mid-read.
func (s *store) disclosures(ctx context.Context, project, id, status string) ([]PatentDisclosure, error) {
	recs, err := loadAll[discRecord](ctx, s, kindDisc)
	if err != nil {
		return nil, err
	}
	apps, err := s.approvalIndex(ctx)
	if err != nil {
		return nil, err
	}
	canon := canonical(recordsOf(pointers(recs)))
	out := make([]PatentDisclosure, 0, len(canon))
	for _, r := range sorted(canon) {
		dr := r.(*discRecord)
		if !inScope(dr.Project, project) || (id != "" && dr.ID != id) || (status != "" && dr.Status != status) {
			continue
		}
		v := discView(dr, true)
		v.Approvals = apps[dr.Project+"\x00"+dr.ID]
		out = append(out, v)
	}
	return out, nil
}

// approvals returns each person's current decision, oldest append first.
func (s *store) approvals(ctx context.Context, project, disclosure string) ([]PatentApproval, error) {
	recs, err := loadAll[approvalRecord](ctx, s, kindApproval)
	if err != nil {
		return nil, err
	}
	canon := canonical(recordsOf(pointers(recs)))
	out := make([]PatentApproval, 0, len(canon))
	for _, r := range sorted(canon) {
		ar := r.(*approvalRecord)
		if !inScope(ar.Project, project) || (disclosure != "" && ar.DisclosureID != disclosure) {
			continue
		}
		out = append(out, approvalView(ar, true))
	}
	return out, nil
}

// approvalIndex maps (project, disclosure) to its current decisions, sorted.
func (s *store) approvalIndex(ctx context.Context) (map[string][]PatentApproval, error) {
	recs, err := loadAll[approvalRecord](ctx, s, kindApproval)
	if err != nil {
		return nil, err
	}
	out := map[string][]PatentApproval{}
	for _, r := range sorted(canonical(recordsOf(pointers(recs)))) {
		ar := r.(*approvalRecord)
		k := ar.Project + "\x00" + ar.DisclosureID
		out[k] = append(out[k], approvalView(ar, true))
	}
	return out, nil
}

// allIdeas etc. hand the retained history to proof, which needs every version
// rather than the deduped view: proof is a statement about the log, and a log
// read through its own dedupe cannot show a correction happened.

func (s *store) allIdeas(ctx context.Context) ([]*ideaRecord, error) {
	recs, err := loadAll[ideaRecord](ctx, s, kindIdea)
	return pointers(recs), err
}

func (s *store) allImpls(ctx context.Context) ([]*implRecord, error) {
	recs, err := loadAll[implRecord](ctx, s, kindImpl)
	return pointers(recs), err
}

func (s *store) allDisclosures(ctx context.Context) ([]*discRecord, error) {
	recs, err := loadAll[discRecord](ctx, s, kindDisc)
	return pointers(recs), err
}

func (s *store) allApprovals(ctx context.Context) ([]*approvalRecord, error) {
	recs, err := loadAll[approvalRecord](ctx, s, kindApproval)
	return pointers(recs), err
}

// pointers turns a loaded slice of values into pointers to the SAME backing
// array, so a grant that mutates one is writing the record the caller re-stores.
func pointers[T any](in []T) []*T {
	out := make([]*T, len(in))
	for i := range in {
		out[i] = &in[i]
	}
	return out
}

// countX is the canonical count a write reports beside what it appended.
func (s *store) countIdeas(ctx context.Context, project string) (int, error) {
	recs, err := loadAll[ideaRecord](ctx, s, kindIdea)
	if err != nil {
		return 0, err
	}
	return countScope(canonical(recordsOf(pointers(recs))), project), nil
}

func (s *store) countImpls(ctx context.Context, project string) (int, error) {
	recs, err := loadAll[implRecord](ctx, s, kindImpl)
	if err != nil {
		return 0, err
	}
	return countScope(canonical(recordsOf(pointers(recs))), project), nil
}

func (s *store) countDisclosures(ctx context.Context, project string) (int, error) {
	recs, err := loadAll[discRecord](ctx, s, kindDisc)
	if err != nil {
		return 0, err
	}
	return countScope(canonical(recordsOf(pointers(recs))), project), nil
}

func (s *store) countApprovals(ctx context.Context, project string) (int, error) {
	recs, err := loadAll[approvalRecord](ctx, s, kindApproval)
	if err != nil {
		return 0, err
	}
	return countScope(canonical(recordsOf(pointers(recs))), project), nil
}

// setGrant moves ONE record kind's visibility, and nothing else. A write always
// records private; publication is a separate decision with a separate call, which
// is the only way "I recorded it" and "everyone can read it" stay different facts.
func (s *store) setGrant(ctx context.Context, project, kind, id, visibility string) (int, error) {
	// The ORM kind the record lives under, from the same word the caller used:
	// one mapping, and the branch below reads the caller's word too, so the two
	// cannot drift the way a name translated twice can.
	kindName := map[string]string{"idea": kindIdea, "implementation": kindImpl, "disclosure": kindDisc}[kind]
	var recs []record
	switch kind {
	case "idea":
		r, err := s.allIdeas(ctx)
		if err != nil {
			return 0, err
		}
		recs = recordsOf(r)
	case "implementation":
		r, err := s.allImpls(ctx)
		if err != nil {
			return 0, err
		}
		recs = recordsOf(r)
	case "disclosure":
		r, err := s.allDisclosures(ctx)
		if err != nil {
			return 0, err
		}
		recs = recordsOf(r)
	default:
		return 0, zip.ErrBadRequest("kind must be idea, implementation or disclosure")
	}
	n := 0
	for _, r := range recs {
		match := false
		switch v := r.(type) {
		case *ideaRecord:
			if v.Project == project && v.ID == id && v.Visibility != visibility {
				v.Visibility = visibility
				match = true
			}
		case *implRecord:
			if v.Project == project && v.ID == id && v.Visibility != visibility {
				v.Visibility = visibility
				match = true
			}
		case *discRecord:
			if v.Project == project && v.ID == id && v.Visibility != visibility {
				v.Visibility = visibility
				match = true
			}
		}
		if !match {
			continue
		}
		if err := s.putOne(ctx, kindName, r); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// putOne rewrites a record in place under its OWN key. This is the one place a
// stored version is mutated, and the one fact that makes it acceptable: what
// changed is visibility, which is not part of the content hash and is a state of
// the PUBLISHER rather than of the invention. Seq, RecordedAt, AuthoredBy and
// ContentHash are untouched, so a grant can never move a record in the order or
// change what it says.
func (s *store) putOne(ctx context.Context, kind string, r record) error {
	if _, err := s.db.Put(ctx, s.db.NewKey(kind, r.key(), 0, nil), r); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "patent grant failed")
	}
	return nil
}

// timelineIn names one entry's whole version history.
type timelineIn struct {
	// ID is the entry's stable id. Required.
	ID string `json:"id"`
	// Kind is idea, implementation or disclosure — which log to read.
	Kind string `json:"kind"`
	// Project narrows inside the org; empty is the caller's own scope.
	Project string `json:"project"`
}

// PatentTimelineEntry is one version of one entry, in append order.
type PatentTimelineEntry struct {
	// Seq is the append clock value this version was stamped with.
	Seq int64 `json:"seq"`
	// Revision is original, corrected or retracted.
	Revision string `json:"revision"`
	// ContentHash is this version's identity.
	ContentHash string `json:"content_hash"`
	// RecordedAt is the server clock at the append.
	RecordedAt string `json:"recorded_at"`
	// AuthoredBy is the validated subject who appended it.
	AuthoredBy string `json:"authored_by"`
	// Canonical marks the version a listing would return: the latest non-retracted
	// one. Exactly one entry per id is canonical, or none, when the latest retracts.
	Canonical bool `json:"canonical"`
	// Superseded marks a retained version that a later one replaced. Retained and
	// superseded are both true of most of a long history, and that is the point.
	Superseded bool `json:"superseded"`
}

// PatentTimeline is one entry's whole retained history, which is the answer to "what
// did we believe before we corrected it" — the question every versioned record
// set eventually gets asked and most log systems cannot answer.
type PatentTimeline struct {
	// Kind is which log was read.
	Kind string `json:"kind"`
	// ID is the entry named.
	ID string `json:"id"`
	// Project is the scope it is filed under.
	Project string `json:"project"`
	// Versions are every retained version, oldest append first.
	Versions []PatentTimelineEntry `json:"versions"`
	// Retained is len(versions).
	Retained int `json:"retained"`
	// Found is false when no version of that id exists in this org.
	Found bool `json:"found"`
}

// ReadPatentTimeline returns every retained version of one entry, in append
// order. ?id= names it and ?kind= says which log; a retraction is shown as the
// withdrawal it is rather than hidden.
func (o ops) getTimeline(ctx context.Context, in *timelineIn) (*PatentTimeline, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if in.ID == "" {
		return nil, zip.ErrBadRequest("a timeline needs an id")
	}
	if !grantKinds[in.Kind] {
		return nil, zip.ErrBadRequest("kind must be idea, implementation or disclosure")
	}
	proj := in.Project
	if proj == "" {
		proj = project(ctx)
	}
	tl := &PatentTimeline{Kind: in.Kind, ID: in.ID, Project: proj}
	var recs []record
	switch in.Kind {
	case "idea":
		all, e := st.allIdeas(ctx)
		if e != nil {
			return nil, fail(o, org, "timeline", e)
		}
		recs = recordsOf(all)
	case "implementation":
		all, e := st.allImpls(ctx)
		if e != nil {
			return nil, fail(o, org, "timeline", e)
		}
		recs = recordsOf(all)
	case "disclosure":
		all, e := st.allDisclosures(ctx)
		if e != nil {
			return nil, fail(o, org, "timeline", e)
		}
		recs = recordsOf(all)
	}
	mine := make([]record, 0, len(recs))
	var latest int64 = -1
	for _, r := range recs {
		if entryOf(r) == in.ID && projectOf(r) == proj {
			mine = append(mine, r)
			if r.clock() > latest {
				latest = r.clock()
			}
		}
	}
	sortVersions(mine)
	for _, r := range mine {
		tl.Versions = append(tl.Versions, PatentTimelineEntry{
			Seq: r.clock(), Revision: revisionOf(r), ContentHash: hashOf(r),
			RecordedAt: atOf(r), AuthoredBy: authorOf(r),
			Canonical:  r.clock() == latest && !r.gone(),
			Superseded: r.clock() < latest,
		})
	}
	tl.Retained = len(tl.Versions)
	tl.Found = tl.Retained > 0
	return tl, nil
}

// fail is the one error path every read in this file shares: log the real cause
// with the org, answer the caller a written sentence that names no internal.
func fail(o ops, org, what string, err error) error {
	o.s.Log.Error("patent "+what+" failed", "org", org, "err", err)
	return zip.Errorf(http.StatusInternalServerError, "patent %s failed", what)
}

// ── small helpers shared by every read above ────────────────────────────────

// recordsOf turns pointers to a kind's records into the interface the shared
// fold and sort work over, without every call site building the slice by hand.
func recordsOf[P interface {
	*T
	record
}, T any](in []P) []record {
	out := make([]record, len(in))
	for i := range in {
		out[i] = in[i]
	}
	return out
}

// sorted returns a fold's values in append order.
func sorted(byStable map[string]record) []record {
	out := make([]record, 0, len(byStable))
	for _, r := range byStable {
		out = append(out, r)
	}
	sortVersions(out)
	return out
}

// inScope is the project filter, where an empty request scope reads the whole
// org — a project is a column inside the tenant, not the tenant.
func inScope(have, want string) bool { return want == "" || have == want }

// countScope counts canonical records in one scope, "" being all of them.
func countScope(byStable map[string]record, project string) int {
	n := 0
	for _, r := range byStable {
		if inScope(projectOf(r), project) {
			n++
		}
	}
	return n
}

// cites is the ?idea= filter: does this implementation name that entry.
func cites(ids []string, want string) bool {
	if want == "" {
		return true
	}
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

// The per-kind accessors the shared code above needs without a type switch in
// every loop. Each is total: an unexpected record type is the programmer's bug,
// not the caller's, and reads as the zero value rather than panicking mid-read.

func entryOf(r record) string {
	switch v := r.(type) {
	case *ideaRecord:
		return v.ID
	case *implRecord:
		return v.ID
	case *discRecord:
		return v.ID
	case *approvalRecord:
		return v.DisclosureID
	}
	return ""
}

func projectOf(r record) string {
	switch v := r.(type) {
	case *ideaRecord:
		return v.Project
	case *implRecord:
		return v.Project
	case *discRecord:
		return v.Project
	case *approvalRecord:
		return v.Project
	}
	return ""
}

func hashOf(r record) string {
	switch v := r.(type) {
	case *ideaRecord:
		return v.ContentHash
	case *implRecord:
		return v.ContentHash
	case *discRecord:
		return v.ContentHash
	case *approvalRecord:
		return v.ContentHash
	}
	return ""
}

func revisionOf(r record) string {
	switch v := r.(type) {
	case *ideaRecord:
		return v.Revision
	case *implRecord:
		return v.Revision
	case *discRecord:
		return v.Revision
	case *approvalRecord:
		return v.Revision
	}
	return ""
}

func atOf(r record) string {
	switch v := r.(type) {
	case *ideaRecord:
		return v.RecordedAt
	case *implRecord:
		return v.RecordedAt
	case *discRecord:
		return v.RecordedAt
	case *approvalRecord:
		return v.RecordedAt
	}
	return ""
}

func authorOf(r record) string {
	switch v := r.(type) {
	case *ideaRecord:
		return v.AuthoredBy
	case *implRecord:
		return v.AuthoredBy
	case *discRecord:
		return v.AuthoredBy
	case *approvalRecord:
		return v.AuthoredBy
	}
	return ""
}

func kindOf(r record) string {
	switch r.(type) {
	case *ideaRecord:
		return "idea"
	case *implRecord:
		return "implementation"
	case *discRecord:
		return "disclosure"
	case *approvalRecord:
		return "approval"
	}
	return ""
}

// ideaView, implView, discView and approvalView turn a stored record into its
// wire shape. `canonical` is DERIVED here and is never stored, so it cannot
// disagree with the history it was computed from.

func ideaView(r *ideaRecord, canon bool) PatentIdea {
	return PatentIdea{Project: r.Project, ID: r.ID, Kind: r.Kind, Title: r.Title,
		Statement: r.Statement, Problem: r.Problem, Alternatives: r.Alternatives,
		PriorArt: r.PriorArt, PatentSignature: r.PatentSignature, Revision: r.Revision,
		ContentHash: r.ContentHash, Visibility: r.Visibility, AuthoredBy: r.AuthoredBy,
		Seq: r.Seq, RecordedAt: r.RecordedAt, Canonical: canon, Notes: r.Notes}
}

func implView(r *implRecord, canon bool) PatentImplementation {
	return PatentImplementation{Project: r.Project, ID: r.ID, IdeaIDs: r.IdeaIDs,
		Repo: r.Repo, GitSHA: r.GitSHA, GitBranch: r.GitBranch, GitDirty: r.GitDirty,
		Model: r.Model, PromptSHA256: r.PromptSHA256, Generated: r.Generated,
		HumanReview: r.HumanReview, ArtifactSHA256: r.ArtifactSHA256, Revision: r.Revision,
		ContentHash: r.ContentHash, Visibility: r.Visibility, AuthoredBy: r.AuthoredBy,
		Seq: r.Seq, RecordedAt: r.RecordedAt, Canonical: canon, Notes: r.Notes}
}

func discView(r *discRecord, canon bool) PatentDisclosure {
	return PatentDisclosure{Project: r.Project, ID: r.ID, Title: r.Title, Summary: r.Summary,
		Claims: r.Claims, IdeaIDs: r.IdeaIDs, ImplementationIDs: r.ImplementationIDs,
		Body: r.Body, BodySHA256: r.BodySHA256, Status: r.Status, FilingRef: r.FilingRef,
		Revision: r.Revision, ContentHash: r.ContentHash, Visibility: r.Visibility,
		AuthoredBy: r.AuthoredBy, Seq: r.Seq, RecordedAt: r.RecordedAt, Canonical: canon}
}

func approvalView(r *approvalRecord, canon bool) PatentApproval {
	return PatentApproval{Project: r.Project, DisclosureID: r.DisclosureID, ID: r.ID,
		Role: strings.TrimSpace(r.Role), Decision: r.Decision, Comment: r.Comment,
		Revision: r.Revision, ContentHash: r.ContentHash, AuthoredBy: r.AuthoredBy,
		Seq: r.Seq, RecordedAt: r.RecordedAt, Canonical: canon}
}
