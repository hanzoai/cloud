package patent

// proof.go turns the log into a statement someone else can check.
//
// It is a READ and it writes nothing, because a proof that could be authored
// would be authored by the party with the most to gain. Every value in it is
// computed from records that were appended earlier under a server-assigned
// clock.
//
// THE FOLD. Records are ordered by the append clock and folded into one digest:
// root = H(root ‖ record_digest) in that order, where record_digest is the
// server's own content hash plus the seq it was stamped with. A root therefore
// commits to the whole ordered set — change one entry, or reorder two, and the
// root moves. That is the object worth anchoring outside this platform, and it is
// the reason `anchor` is a status in the answer rather than a claim: an internal
// chain proves the log has not been edited since it was read HERE. It proves
// nothing about whether this platform told the truth yesterday, and that is the
// half a dispute is actually about.
//
// THE VERDICT. For every implementation, compare its seq against the seq of each
// idea it cites. `before` is the inventorship question asked of the only thing
// that can answer it. When the cited idea is missing, the answer is `unproven`
// and not `after` — absence of a record is not evidence of a sequence, and
// conflating the two would let an empty log read as a refutation.
//
// THE GAPS. Every field of a matched record that says nothing is counted and
// named. An answer that only reports what is present is an answer that gets
// better when you record less.

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/zap-proto/zip"
)

// proofIn names what to prove. With neither, the whole org's log is folded.
type proofIn struct {
	// Disclosure folds the record set one document rests on, and reports on
	// that set alone — which is what an attorney asks for.
	Disclosure string `json:"disclosure"`
	// Idea narrows to the one entry and the artifacts citing it.
	Idea string `json:"idea"`
	// Project narrows inside the org; empty reads the caller's scope.
	Project string `json:"project"`
}

// PatentProofEntry is one record as the fold saw it: its place, its digest, and who the
// server says put it there.
type PatentProofEntry struct {
	// Seq is the append clock value. The ordering authority.
	Seq int64 `json:"seq"`
	// Kind is idea, implementation, disclosure or approval.
	Kind string `json:"kind"`
	// ID is the record's stable id.
	ID string `json:"id"`
	// ContentHash is the server's digest of the recorded content.
	ContentHash string `json:"content_hash"`
	// Digest is what the fold consumed: content hash and clock together, so two
	// versions of one id with identical content still differ in the fold.
	Digest string `json:"digest"`
	// AuthoredBy is the validated subject on the record.
	AuthoredBy string `json:"authored_by"`
	// RecordedAt is the server clock at the append.
	RecordedAt string `json:"recorded_at"`
	// Revision is original, corrected or retracted.
	Revision string `json:"revision"`
}

// PatentOrderCheck is one implementation-idea pair and whether the record says the
// idea came first.
type PatentOrderCheck struct {
	// Implementation is the artifact being examined.
	Implementation string `json:"implementation"`
	// Idea is the conception it cites.
	Idea string `json:"idea"`
	// Verdict is before, after or unproven.
	Verdict string `json:"verdict"`
	// GapSeq is how many appends separate them, negative when the implementation
	// precedes the idea. It is a distance in the LOG rather than in seconds,
	// because seconds are comparable only within one server's clock.
	GapSeq int64 `json:"gap_seq"`
	// Why states, in one sentence, what the verdict is resting on. A verdict
	// without its basis is the thing this whole surface exists to avoid.
	Why string `json:"why"`
}

// PatentAnchor is the record set's standing outside this deployment.
type PatentAnchor struct {
	// Status is unanchored or anchored. Nothing else: an anchored answer arrives
	// only from a real commitment, and this app never invents a transaction id.
	Status string `json:"status"`
	// Commitment is the fold root the anchor would name.
	Commitment string `json:"commitment"`
	// At is when the commitment was made, RFC3339, when there is one.
	At string `json:"at"`
	// Reference is where a third party can check it — a block, an index, a
	// published headline. Empty means nothing outside this response names it.
	Reference string `json:"reference"`
}

// PatentProof is the verification bundle for one record set.
type PatentProof struct {
	// Project is the scope the fold covered.
	Project string `json:"project"`
	// Root is the fold digest over every record in append order.
	Root string `json:"root"`
	// Records are the entries folded, oldest clock first.
	Records []PatentProofEntry `json:"records"`
	// Order is one verdict per implementation-idea pair.
	Order []PatentOrderCheck `json:"order"`
	// Verdict is the whole answer in one word: supported when every pair is
	// `before`, contested when any is `after`, unproven when any is missing, and
	// empty when there was nothing to fold. It is the WORST pair, because a chain
	// with one inverted edge is not a chain.
	Verdict string `json:"verdict"`
	// PatentAnchor is the external standing of this exact root.
	PatentAnchor PatentAnchor `json:"anchor"`
	// Coverage counts what the fold held, per kind, so a short list reads as a
	// short log and not as a complete one.
	Coverage PatentCoverage `json:"coverage"`
	// Gaps names every field of the covered records that states nothing, with
	// how many records leave it unstated.
	Gaps []PatentGap `json:"gaps"`
	// VerifiedAt is when this fold was computed. The records can have grown since;
	// the root is the set as of here.
	VerifiedAt string `json:"verified_at"`
}

// PatentCoverage counts the folded records by kind.
type PatentCoverage struct {
	// Ideas is how many conception entries the fold covered.
	Ideas int `json:"ideas"`
	// Implementations is how many implementation records it covered.
	Implementations int `json:"implementations"`
	// Disclosures is how many documents it covered.
	Disclosures int `json:"disclosures"`
	// Approvals is how many decisions it covered.
	Approvals int `json:"approvals"`
	// Versions is how many RETAINED versions were folded, which is at least the
	// sum above and is the number that says corrections happened.
	Versions int `json:"versions"`
}

// PatentGap is one unstated field across the covered records.
type PatentGap struct {
	// Field is the wire name, e.g. prior_art.
	Field string `json:"field"`
	// Missing is how many covered records leave it empty.
	Missing int `json:"missing"`
	// Why names what the absence costs, so a gap is a decision to fix rather
	// than a lint warning.
	Why string `json:"why"`
}

// gapReasons is what each absent field actually costs. Only fields that bear on
// conception or on the record's checkability are here; an absent Notes is nobody's
// problem.
var gapReasons = map[string]string{
	"prior_art":       "nothing states what the author knew already existed, so the boundary of the contribution is unmarked",
	"alternatives":    "nothing shows what was ruled out, so the design reads as received rather than chosen",
	"problem":         "the entry states a mechanism without the technical problem it solves",
	"git_sha":         "no commit is named, so the artifact cannot be read outside this record",
	"model":           "no system is named, so how much of this was a tool is unanswerable",
	"prompt_sha256":   "the request is not committed to, so it cannot be shown the same prompt produced it",
	"authored_by":     "the record has no human subject, which is the fact §115 asks for",
	"artifact_sha256": "the artifact is not content-addressed",
}

// GetPatentProof folds the caller's record set and answers the ordering question
// over it.
//
// ?disclosure= proves one document's set, ?idea= proves one entry and everything
// citing it, and neither proves the whole org. The answer is computed per request
// and writes nothing.
func (o ops) getProof(ctx context.Context, in *proofIn) (*PatentProof, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	proj := in.Project
	if proj == "" {
		proj = project(ctx)
	}
	p, err := st.proof(ctx, proj, in.Disclosure, in.Idea)
	if err != nil {
		o.s.Log.Error("patent proof failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "patent proof failed")
	}
	return p, nil
}

// proof folds one project's records. The narrowing is done here rather than in
// four loaders so the fold, the coverage counts and the ordering verdict cannot
// disagree about which records they were computed over — one predicate, one pass.
func (s *store) proof(ctx context.Context, project, disclosure, idea string) (*PatentProof, error) {
	ideas, err := s.allIdeas(ctx)
	if err != nil {
		return nil, err
	}
	impls, err := s.allImpls(ctx)
	if err != nil {
		return nil, err
	}
	discs, err := s.allDisclosures(ctx)
	if err != nil {
		return nil, err
	}
	apps, err := s.allApprovals(ctx)
	if err != nil {
		return nil, err
	}

	p := &PatentProof{Project: project, PatentAnchor: PatentAnchor{Status: anchorStatus()}, VerifiedAt: stampAt()}
	// The narrowed sets, before the fold: a disclosure read proves THAT
	// document's evidence, not everything the org happens to hold.
	wantIdeas, wantImpls := map[string]bool{}, map[string]bool{}
	scope := disclosure != "" || idea != ""
	if disclosure != "" {
		for _, d := range discs {
			if d.Project == project && d.ID == disclosure {
				for _, id := range d.IdeaIDs {
					wantIdeas[id] = true
				}
				for _, id := range d.ImplementationIDs {
					wantImpls[id] = true
				}
			}
		}
	}
	if idea != "" {
		wantIdeas[idea] = true
		wantImpls = nil // an idea read is about everything that cites it, so implementations are not pre-narrowed
	}

	all := make([]record, 0, len(ideas)+len(impls)+len(discs)+len(apps))
	for _, r := range ideas {
		if keep(project, r.Project, r.ID, wantIdeas, scope) {
			all = append(all, r)
			p.Coverage.Ideas++
		}
	}
	for _, r := range impls {
		if keepImpl(project, r, wantImpls, scope, wantIdeas) {
			all = append(all, r)
			p.Coverage.Implementations++
		}
	}
	for _, r := range discs {
		if !scope && inScope(r.Project, project) {
			all = append(all, r)
			p.Coverage.Disclosures++
		}
	}
	for _, r := range apps {
		if !scope && inScope(r.Project, project) {
			all = append(all, r)
			p.Coverage.Approvals++
		}
	}
	sortVersions(all)
	p.Coverage.Versions = len(all)

	// The fold, in one pass over the sorted set.
	root := hashJoin("patent/v1")
	for _, r := range all {
		e := PatentProofEntry{
			Seq: r.clock(), Kind: kindOf(r), ID: entryOf(r), ContentHash: hashOf(r),
			Digest:     hashJoin(hashOf(r), itoa(r.clock()), kindOf(r), entryOf(r)),
			AuthoredBy: authorOf(r), RecordedAt: atOf(r), Revision: revisionOf(r),
		}
		p.Records = append(p.Records, e)
		root = hashJoin(root, e.Digest)
	}
	p.Root = root
	p.PatentAnchor.Commitment = root

	// The ordering verdict, per implementation over the ideas it cites. The
	// lookup spans the WHOLE project's ideas, not just the proved set, so a
	// citation pointing outside the scope reads as unproven rather than absent.
	ideaSeq := map[string]int64{}
	for _, r := range ideas {
		if r.Project == project {
			if cur, ok := ideaSeq[r.ID]; !ok || r.Seq > cur {
				ideaSeq[r.ID] = r.Seq
			}
		}
	}
	worst := ""
	for _, m := range impls {
		if m.Project != project {
			continue
		}
		for _, want := range m.IdeaIDs {
			seq, ok := ideaSeq[want]
			c := PatentOrderCheck{Implementation: m.ID, Idea: want, GapSeq: m.Seq - seq}
			switch {
			case !ok:
				c.Verdict, c.Why = "unproven", "no conception entry with that id is on record in this project, so nothing establishes what preceded this artifact"
			case seq < m.Seq:
				c.Verdict, c.Why = "before", "the idea was appended to the log before the implementation citing it"
			case seq == m.Seq:
				c.Verdict, c.Why = "after", "both carry the same append clock, which cannot establish an order"
			default:
				c.Verdict, c.Why = "after", "the implementation was recorded first, which is the sequence the guidance reads as a tool having done the conceiving"
			}
			p.Order = append(p.Order, c)
			worst = worseVerdict(worst, c.Verdict)
		}
	}
	if worst == "" {
		worst = verdictForCoverage(p.Coverage)
	}
	p.Verdict = bundle(worst)
	p.Gaps = gapsFor(p.Records, ideas, impls)
	return p, nil
}

// The verdict lattice, worst wins, over the three things a pair can say. A
// missing record is a WORSE position than an inverted one — a bad order at least
// states what happened — so unproven beats after beats before.
//
// The aggregate answer is a different word from a pair's on purpose: `before` is
// a fact about one edge and `supported` is a claim about the whole set, and
// collapsing the two is how a bundle ends up reporting the status of its first
// record.
func worseVerdict(a, b string) string {
	rank := map[string]int{"": -1, "before": 0, "after": 1, "unproven": 2}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

// bundle turns the worst pair verdict into the answer for the whole record set.
func bundle(worst string) string {
	switch worst {
	case "before":
		return "supported"
	case "after":
		return "contested"
	case "unproven":
		return "unproven"
	}
	return "empty"
}

// verdictForCoverage is the empty-set case, and it is a pair verdict so `bundle`
// stays the only translation. A log with no implementation at all proves nothing
// about order, which is `unproven`; a log with NO RECORDS is `empty`, and the two
// must not read alike — one is a filing nobody finished and one is a filing
// somebody inverted.
func verdictForCoverage(c PatentCoverage) string {
	if c.Ideas == 0 && c.Implementations == 0 && c.Disclosures == 0 && c.Approvals == 0 {
		return ""
	}
	return "unproven"
}

// keep is the scope predicate for a record addressed by id.
func keep(want, have, id string, set map[string]bool, scope bool) bool {
	if !inScope(have, want) {
		return false
	}
	return !scope || set[id]
}

// keepImpl is keep for an implementation, with the extra arm an idea read needs:
// when narrowing to one idea, every artifact citing it is in scope regardless of
// the pre-narrowed id set.
func keepImpl(want string, r *implRecord, set map[string]bool, scope bool, ideas map[string]bool) bool {
	if !inScope(r.Project, want) {
		return false
	}
	if !scope {
		return true
	}
	if set != nil && set[r.ID] {
		return true
	}
	if len(ideas) == 1 && ideas[onlyKey(ideas)] {
		return cites(r.IdeaIDs, onlyKey(ideas))
	}
	return false
}

func onlyKey(m map[string]bool) string {
	for k := range m {
		return k
	}
	return ""
}

// gapsFor counts what the folded records leave unstated, and it is the half of
// this answer a caller would rather not see: an appraisal that only reports what
// is present gets better when you record less.
//
// AuthoredBy is checked here and nowhere else would it matter — a record with no
// human subject is the one failure this whole surface exists to make visible.
func gapsFor(entries []PatentProofEntry, ideas []*ideaRecord, impls []*implRecord) []PatentGap {
	counts := map[string]int{}
	n := func(field, value string) {
		if strings.TrimSpace(value) == "" {
			counts[field]++
		}
	}
	for _, e := range entries {
		n("authored_by", e.AuthoredBy)
	}
	for _, r := range ideas {
		n("prior_art", r.PriorArt)
		n("problem", r.Problem)
		if len(r.Alternatives) == 0 {
			counts["alternatives"]++
		}
	}
	for _, r := range impls {
		n("git_sha", r.GitSHA)
		n("model", r.Model)
		n("prompt_sha256", r.PromptSHA256)
		n("artifact_sha256", r.ArtifactSHA256)
	}
	out := make([]PatentGap, 0, len(counts))
	for f, c := range counts {
		out = append(out, PatentGap{Field: f, Missing: c, Why: gapReasons[f]})
	}
	sortGaps(out)
	return out
}

// anchorStatus is the one honest answer available: the fold root is committed
// outside this deployment only when a backend is wired to commit it.
func anchorStatus() string {
	if anchored() {
		return "anchored"
	}
	return "unanchored"
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func sortGaps(g []PatentGap) {
	sort.Slice(g, func(i, j int) bool {
		if g[i].Missing != g[j].Missing {
			return g[i].Missing > g[j].Missing
		}
		return g[i].Field < g[j].Field
	})
}
