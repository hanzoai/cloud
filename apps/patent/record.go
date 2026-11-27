package patent

// record.go is what a conception has to name before anyone can be shown it was
// first: the IDEA a person had, the IMPLEMENTATION the model wrote from it, the
// DISCLOSURE that ties the two into a document, and the APPROVALS that carry it
// forward.
//
// THE EDGE IS THE POINT. An implementation names the idea ids it implements. That
// one relationship is the whole inventorship question under the USPTO's 2025
// guidance restated in HIP-1329: if the human's idea is on record before the tool
// produced the code, conception is documentable; if the only record is the code,
// it is not, however novel the code is. So the edge is REQUIRED on a write rather
// than optional — an implementation citing nothing is the pattern that loses, and
// this API refuses to file it quietly.
//
// EVERY FIELD DECIDING THE ARGUMENT IS THE SERVER'S. AuthoredBy is the validated
// principal, RecordedAt is this store's clock, Seq is the append order,
// ContentHash is the server's digest. A caller states WHAT it thought and WHERE
// the code is; it states nothing about WHEN, WHO or IN WHICH ORDER, because those
// are the facts under dispute.
//
// Debugging, refactoring and translating already-conceived code do not appear
// here at all, and should not. That is reduction to practice, not conception, and
// recording every lint pass as an invention is how a diary becomes noise.

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// statuses is the closed set a disclosure moves through. `filed` is set by a
// human who filed it, never inferred: this app files nothing, and an
// automatically-advancing status would be a claim about an act nobody performed.
var statuses = map[string]bool{"draft": true, "reviewed": true, "approved": true, "filed": true}

// reviews is the closed set of human dispositions over generated code.
// `accepted` is the one that reads worst in an examination — a human who took
// whatever came back — and it is still allowed, because refusing to record it
// would only hide it.
var reviews = map[string]bool{"accepted": true, "edited": true, "rewritten": true, "none": true}

// PatentSignature is a detached claim that a specific key signed this entry's digest.
// It is OPTIONAL and it is stored verbatim. Authorship does not depend on it —
// the author of every record here is the validated principal, which the request
// cannot forge — so a signature is corroboration a cryptographic review might
// want and never the basis of the claim.
type PatentSignature struct {
	// Algo names the scheme, e.g. ed25519. Empty means the record does not say.
	Algo string `json:"algo"`
	// KeyID names the key that signed. It is a reference, not public bytes.
	KeyID string `json:"key_id"`
	// Value is the signature, base64.
	Value string `json:"value"`
	// Verified is the SERVER's answer to whether it checked this signature. It is
	// false on a deployment with no verifier wired, and a record carrying an
	// unchecked signature says so rather than borrowing credibility.
	Verified bool `json:"verified"`
}

// PatentRejectedAlternative is one route considered and ruled out, with the reason.
// This is the field people leave out and the one that argues best: a path
// deliberately NOT taken shows the design was CHOSEN, which is what separates a
// human's conception from a model's plausible default.
type PatentRejectedAlternative struct {
	// Option is the approach, in the author's words.
	Option string `json:"option"`
	// Reason is why it was rejected — the constraint or failure that decided it.
	Reason string `json:"reason"`
}

// PatentIdea is one conception entry: a human statement of a technical problem
// and the mechanism proposed for it, recorded before the implementation that
// expresses it.
type PatentIdea struct {
	// Project is the sub-scope inside the org this entry is filed under. On a
	// write it is the SERVER's value and a project in the body is ignored.
	Project string `json:"project"`
	// ID is the entry's stable id, the key its versions accrue under:
	// lowercase, terse, and named for the idea rather than the file —
	// latency-aware-request-routing. Required on a write.
	ID string `json:"id"`
	// Kind is what kind of conception this is: problem, alternative,
	// architecture, mechanism, decision or rejection. Required on a write.
	Kind string `json:"kind"`
	// Title is the one-line name of the entry.
	Title string `json:"title"`
	// Statement is the operative idea in the author's own prose — definite
	// enough that a reader could implement it from this alone. Required, and the
	// field an examiner would actually read.
	Statement string `json:"statement"`
	// Problem is the technical problem being solved, and why the existing
	// approaches fail at it.
	Problem string `json:"problem"`
	// Alternatives are the routes considered and rejected, each with its reason.
	Alternatives []PatentRejectedAlternative `json:"alternatives"`
	// PriorArt is what the author knows already existed, named as the author
	// knows it. It is a statement of the inventor's awareness, not a search
	// result, and it is not a substitute for one.
	PriorArt string `json:"prior_art"`
	// PatentSignature is an optional detached signature over this entry's digest.
	PatentSignature *PatentSignature `json:"signature,omitempty"`
	// Revision is the producer's declared kind of this version: original,
	// corrected or retracted. A retracted latest version WITHDRAWS the id; every
	// earlier version stays retained.
	Revision string `json:"revision"`
	// ContentHash is the SERVER's digest of the recorded content and this
	// version's identity. Computed on a write; read back on a read.
	ContentHash string `json:"content_hash"`
	// Visibility is private, org or public. A write forces private.
	Visibility string `json:"visibility"`
	// AuthoredBy is the validated principal who recorded this version — an IAM
	// subject, never a body field. It is the §115 answer, and a model has none.
	AuthoredBy string `json:"authored_by"`
	// Seq is this version's place in the store's append order. The ordering
	// authority; never a client value.
	Seq int64 `json:"seq"`
	// RecordedAt is when this store accepted the version, RFC3339, from the
	// server clock.
	RecordedAt string `json:"recorded_at"`
	// Canonical is DERIVED on read: a version is canonical when it is the
	// latest-appended non-retracted version of its id.
	Canonical bool `json:"canonical"`
	// Notes is anything a later reader needs that the fields above cannot carry.
	Notes string `json:"notes"`
}

// PatentImplementation is the machine's part of the record, tied to the ideas it
// came out of. It is deliberately small: the claim is not "the code is
// interesting", it is "this artifact expresses that earlier human idea, under
// these inputs, and a person reviewed it".
type PatentImplementation struct {
	// Project is the SERVER's scope value.
	Project string `json:"project"`
	// ID is this implementation's stable id — usually the artifact or change it
	// records, e.g. router-v2-generated. Required.
	ID string `json:"id"`
	// IdeaIDs names the conceptions this implements. REQUIRED and non-empty:
	// an implementation citing nothing is the inverted order the guidance is
	// written against, and filing it silently would be the worst thing this
	// surface could do.
	IdeaIDs []string `json:"idea_ids"`
	// Repo is the repository the work landed in.
	Repo string `json:"repo"`
	// GitSHA is the commit. Together with Seq it is what makes the claim checkable
	// outside this app: a reader can go read the code.
	GitSHA string `json:"git_sha"`
	// GitBranch is the branch that commit was on.
	GitBranch string `json:"git_branch"`
	// GitDirty says the working tree carried uncommitted changes when this was
	// recorded. It is recorded because an exact commit claim that is not true is
	// worse than no commit claim.
	GitDirty bool `json:"git_dirty"`
	// Model names the system that produced the code, as it was addressed. This is
	// the field that answers "how much of this was a tool" without a guess.
	Model string `json:"model"`
	// PromptSHA256 is the digest of the prompt, not the prompt. The text often
	// carries third-party material; the digest lets a later reader prove the
	// same request was made.
	PromptSHA256 string `json:"prompt_sha256"`
	// Generated says the recorded artifact was produced with a model's
	// assistance rather than typed by hand.
	Generated bool `json:"generated"`
	// HumanReview is what a person did with the output: accepted, edited,
	// rewritten, or none. `none` on a novel mechanism is the disposition to
	// know about.
	HumanReview string `json:"human_review"`
	// ArtifactSHA256 is the digest of the recorded artifact — a diff, a file, a
	// build — when the caller has one and wants it addressed by content.
	ArtifactSHA256 string `json:"artifact_sha256"`
	// Revision is original, corrected or retracted.
	Revision string `json:"revision"`
	// ContentHash is the server's digest of the recorded content.
	ContentHash string `json:"content_hash"`
	// Visibility is private, org or public; forced private on a write.
	Visibility string `json:"visibility"`
	// AuthoredBy is the validated principal who recorded this. Authorship of the
	// IDEA is PatentIdea.AuthoredBy — this field records who filed the
	// implementation record, which is not the same question.
	AuthoredBy string `json:"authored_by"`
	// Seq is the append order, which is what GET /v1/patent/proof compares
	// against the cited ideas.
	Seq int64 `json:"seq"`
	// RecordedAt is the server's clock.
	RecordedAt string `json:"recorded_at"`
	// Canonical is DERIVED on read.
	Canonical bool `json:"canonical"`
	// Notes is anything a later reader needs.
	Notes string `json:"notes"`
}

// PatentDisclosure is the assembled document: a title, a statement of the
// invention, and the idea and implementation records it rests on. It is where a
// disclosure gets handed to an attorney, and its citations are the reason a
// reader can check it rather than take it.
type PatentDisclosure struct {
	// Project is the SERVER's scope value.
	Project string `json:"project"`
	// ID is the disclosure's stable id. Required.
	ID string `json:"id"`
	// Title names the invention the way the document does.
	Title string `json:"title"`
	// Summary is the invention in one paragraph, for the reader who decides
	// whether to read the rest.
	Summary string `json:"summary"`
	// Claims is the draft claim set, or a pointer to where it lives.
	Claims string `json:"claims"`
	// IdeaIDs are the conception entries this disclosure is built from. The
	// evidence half; a disclosure citing none is an assertion.
	IdeaIDs []string `json:"idea_ids"`
	// ImplementationIDs are the artifacts it describes.
	ImplementationIDs []string `json:"implementation_ids"`
	// Body is the document's text. Its digest is published beside it so a reader
	// can tell a stored body from a re-sent one.
	Body string `json:"body"`
	// BodySHA256 is the SERVER's digest of body. On a read it is the address of
	// what is stored; a body is never hashed from what a reader sent.
	BodySHA256 string `json:"body_sha256"`
	// Status is draft, reviewed, approved or filed. `approved` means the
	// approvals below carry a decision for each required role, which is DERIVED
	// on read, not a word a caller sets and gets believed.
	Status string `json:"status"`
	// FilingRef is a filing number a human recorded here after filing. Empty
	// means nothing has been filed, and no code path fills it.
	FilingRef string `json:"filing_ref"`
	// Revision is original, corrected or retracted.
	Revision string `json:"revision"`
	// ContentHash is the server's digest of the recorded content.
	ContentHash string `json:"content_hash"`
	// Visibility is private, org or public; forced private on a write.
	Visibility string `json:"visibility"`
	// AuthoredBy is the validated principal who recorded this version.
	AuthoredBy string `json:"authored_by"`
	// Seq is the append order.
	Seq int64 `json:"seq"`
	// RecordedAt is the server's clock.
	RecordedAt string `json:"recorded_at"`
	// Canonical is DERIVED on read.
	Canonical bool `json:"canonical"`
	// Approvals is DERIVED on read: the current decision of record from each
	// person who has ruled on this disclosure. Never an input — a caller cannot
	// approve its own document by writing a field in it.
	Approvals []PatentApproval `json:"approvals"`
}

// PatentApproval is one person's decision about one disclosure, attributed to
// them by the credential they arrived with. This is the CTO sign-off on the
// research record and the attorney's review, stored as an event rather than as a
// status a document hopes nobody audits.
type PatentApproval struct {
	// Project is the SERVER's scope value.
	Project string `json:"project"`
	// DisclosureID is the document this decides on. Required.
	DisclosureID string `json:"disclosure_id"`
	// ID is the server's handle for this decision, derived from the disclosure
	// and its author. Read-only: a caller cannot name someone else's approval.
	ID string `json:"id"`
	// Role is the capacity the person acted in: author, cto, reviewer, attorney
	// or counsel. Required, because "approved" by nobody is not a fact.
	Role string `json:"role"`
	// Decision is approved, rejected or needs-work. A needs-work is dated and
	// retained exactly like a sign-off, because it IS one.
	Decision string `json:"decision"`
	// Comment is the reviewer's note — the part an inventor reads.
	Comment string `json:"comment"`
	// Revision is original, corrected or retracted.
	Revision string `json:"revision"`
	// ContentHash is the server's digest of the recorded content.
	ContentHash string `json:"content_hash"`
	// AuthoredBy is the validated principal who decided. The same rule as
	// everywhere here: it comes from the credential, never the body, so one
	// person cannot record a decision on another's behalf.
	AuthoredBy string `json:"authored_by"`
	// Seq is the append order.
	Seq int64 `json:"seq"`
	// RecordedAt is the server's clock.
	RecordedAt string `json:"recorded_at"`
	// Canonical is DERIVED on read: this author's latest word on this
	// disclosure.
	Canonical bool `json:"canonical"`
}

// ── wire envelopes ───────────────────────────────────────────────────────────

// ideaBatch is one recorded batch of conception entries.
type ideaBatch struct {
	// Ideas are the entries to append, at most 2000.
	Ideas []PatentIdea `json:"ideas" url:"-"`
}

// implBatch is one recorded batch of implementation records.
type implBatch struct {
	// Implementations are the records to append, at most 2000.
	Implementations []PatentImplementation `json:"implementations" url:"-"`
}

// disclosureBatch is one recorded batch of disclosures.
type disclosureBatch struct {
	// Disclosures are the documents to append, at most 2000.
	Disclosures []PatentDisclosure `json:"disclosures" url:"-"`
}

// approvalBatch is one recorded batch of decisions.
type approvalBatch struct {
	// Approvals are the decisions to append, at most 2000.
	Approvals []PatentApproval `json:"approvals" url:"-"`
}

// patentWriteOut is what one write did. Recorded counts APPENDED versions, so a
// re-send of identical content reports 0 and is not an error — the entry is in
// the log either way, which is the only thing a caller needed.
type patentWriteOut struct {
	// Project is the scope the batch was filed under, as the server decided it.
	Project string `json:"project"`
	// Recorded is how many new versions this call appended.
	Recorded int `json:"recorded"`
	// Canonical is how many entries the org's log now holds for this kind.
	Canonical int `json:"canonical"`
}

// ideasIn narrows the conception listing.
type ideasIn struct {
	// ID reads one entry, which is how a single idea is addressed — there is no
	// second route for it.
	ID string `json:"id"`
	// Kind narrows to one kind of conception entry.
	Kind string `json:"kind"`
	// Project narrows to one project; empty reads the org's whole set.
	Project string `json:"project"`
}

// ideasOut is the canonical conception listing.
type ideasOut struct {
	// Data are the canonical entries, oldest append first.
	Data []PatentIdea `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// implsIn narrows the implementation listing.
type implsIn struct {
	// ID reads one implementation.
	ID string `json:"id"`
	// Idea narrows to implementations citing one conception entry — which is
	// how a correction finds every artifact built on the idea it invalidates.
	Idea string `json:"idea"`
	// Project narrows to one project.
	Project string `json:"project"`
}

// implsOut is the canonical implementation listing.
type implsOut struct {
	// Data are the canonical records, oldest append first.
	Data []PatentImplementation `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// discsIn narrows the disclosure listing.
type discsIn struct {
	// ID reads one disclosure.
	ID string `json:"id"`
	// Status narrows to one status.
	Status string `json:"status"`
	// Project narrows to one project.
	Project string `json:"project"`
}

// discsOut is the canonical disclosure listing.
type discsOut struct {
	// Data are the canonical disclosures, oldest append first.
	Data []PatentDisclosure `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// approvalsIn narrows the decision trail.
type approvalsIn struct {
	// Disclosure narrows to the decisions on one document.
	Disclosure string `json:"disclosure"`
	// Project narrows to one project.
	Project string `json:"project"`
}

// approvalsOut is the canonical decision listing.
type approvalsOut struct {
	// Data are the current decisions of record, oldest append first.
	Data []PatentApproval `json:"data"`
	// Total is len(data).
	Total int `json:"total"`
}

// ── the writes ──────────────────────────────────────────────────────────────

// RecordPatentIdeas appends conception entries. A write states the idea and
// nothing else: the project, the author, the clock, the append order and the
// content digest are all the server's, and a correction appends a version rather
// than editing the entry — which is the property that makes any of it usable as
// evidence at all.
//
// Example: {"ideas":[{"id":"route-by-queue-depth","kind":"mechanism","statement":"..."}]}
func (o ops) postIdeas(ctx context.Context, in *ideaBatch) (*patentWriteOut, error) {
	return o.record(ctx, "ideas", len(in.Ideas),
		func(st *store, project, author string) (int, error) {
			return st.ideaVersions(ctx, project, author, in.Ideas)
		},
		func(st *store, project string) (int, error) { return st.countIdeas(ctx, project) })
}

// RecordPatentImplementations appends the machine's half of the record, and the
// idea citation is the load-bearing field: it is what lets a reader see that the
// conception came first. An entry citing no idea is refused rather than filed.
//
// Example: {"implementations":[{"id":"router-v2","idea_ids":["route-by-queue-depth"],"model":"zen6-coder","git_sha":"..."}]}
func (o ops) postImpls(ctx context.Context, in *implBatch) (*patentWriteOut, error) {
	return o.record(ctx, "implementations", len(in.Implementations),
		func(st *store, project, author string) (int, error) {
			return st.implVersions(ctx, project, author, in.Implementations)
		},
		func(st *store, project string) (int, error) { return st.countImpls(ctx, project) })
}

// RecordPatentDisclosures appends the assembled document and the record ids it
// rests on. The document is a rendering of what the log already holds, and its
// citations are what let a reader check it instead of taking it.
func (o ops) postDisclosures(ctx context.Context, in *disclosureBatch) (*patentWriteOut, error) {
	return o.record(ctx, "disclosures", len(in.Disclosures),
		func(st *store, project, author string) (int, error) {
			return st.discVersions(ctx, project, author, in.Disclosures)
		},
		func(st *store, project string) (int, error) { return st.countDisclosures(ctx, project) })
}

// RecordPatentApprovals appends one person's decision about one disclosure. The
// approver is the validated principal and nothing else, so no caller records a
// sign-off on someone else's behalf — which is the only way this field would be
// worth reading.
//
// Example: {"approvals":[{"disclosure_id":"router-v2","role":"cto","decision":"approved","comment":"research log reviewed, the mechanism is ours"}]}
func (o ops) postApprovals(ctx context.Context, in *approvalBatch) (*patentWriteOut, error) {
	return o.record(ctx, "approvals", len(in.Approvals),
		func(st *store, project, author string) (int, error) {
			return st.approvalVersions(ctx, project, author, in.Approvals)
		},
		func(st *store, project string) (int, error) { return st.countApprovals(ctx, project) })
}

// record is the whole write path shared by the four kinds: resolve the tenant and
// the author, append, ship before acknowledging, report both counts. It exists
// once so that no future kind can be added with one of those four steps missing —
// an evidence store whose new endpoint forgot to ship would lose acknowledged
// records, and nothing in a handler review would catch that.
func (o ops) record(ctx context.Context, what string, n int,
	append func(*store, string, string) (int, error),
	canonical func(*store, string) (int, error)) (*patentWriteOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, zip.ErrBadRequest(what + " is empty")
	}
	if n > maxBatchItems {
		return nil, zip.ErrBadRequest("too many records in one batch")
	}
	proj, who := project(ctx), author(ctx)
	if what == "approvals" && who == "" {
		// A decision needs a person. A machine credential has a subject, but
		// there is no human judgement here to record, so it is refused rather
		// than attributed to a tenant.
		return nil, zip.ErrForbidden("an approval needs a validated user, not a machine credential")
	}
	added, err := append(st, proj, who)
	if err != nil {
		if !isValidation(err) {
			o.s.Log.Error("patent "+what+" failed", "org", org, "err", err)
		}
		return nil, err
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	can, err := canonical(st, proj)
	if err != nil {
		return nil, err
	}
	return &patentWriteOut{Project: proj, Recorded: added, Canonical: can}, nil
}

// isValidation distinguishes a refusal this package wrote on purpose from a
// storage fault. A 422 that says "cite the idea you implemented" is not an error
// worth an operator's attention, and logging every bad request turns the log into
// the thing it is reporting.
func isValidation(err error) bool {
	var he *zip.HTTPError
	return errors.As(err, &he) && he.Status >= 400 && he.Status < 500
}

// ── the reads ───────────────────────────────────────────────────────────────

// ListPatentIdeas returns the caller org's canonical conception entries, oldest
// appended first, which is the order the log says they were thought. ?id= narrows
// to one, ?kind= to one kind, ?project= to one project.
func (o ops) listIdeas(ctx context.Context, in *ideasIn) (*ideasOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	data, err := st.ideas(ctx, in.Project, in.ID, in.Kind)
	if err != nil {
		o.s.Log.Error("patent ideas list failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "patent list failed")
	}
	return &ideasOut{Data: data, Total: len(data)}, nil
}

// ListPatentImplementations returns the canonical implementation records with
// their citations. ?idea= is the query the record exists to answer: which
// artifacts rest on this one conception entry.
func (o ops) listImpls(ctx context.Context, in *implsIn) (*implsOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	data, err := st.impls(ctx, in.Project, in.ID, in.Idea)
	if err != nil {
		o.s.Log.Error("patent implementations list failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "patent list failed")
	}
	return &implsOut{Data: data, Total: len(data)}, nil
}

// ListPatentDisclosures returns the canonical disclosures, each carrying its
// current approvals.
func (o ops) listDisclosures(ctx context.Context, in *discsIn) (*discsOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	data, err := st.disclosures(ctx, in.Project, in.ID, in.Status)
	if err != nil {
		o.s.Log.Error("patent disclosures list failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "patent list failed")
	}
	return &discsOut{Data: data, Total: len(data)}, nil
}

// ListPatentApprovals returns the decision trail — each person's current word on
// each disclosure. ?disclosure= narrows to one document.
func (o ops) listApprovals(ctx context.Context, in *approvalsIn) (*approvalsOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	data, err := st.approvals(ctx, in.Project, in.Disclosure)
	if err != nil {
		o.s.Log.Error("patent approvals list failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "patent list failed")
	}
	return &approvalsOut{Data: data, Total: len(data)}, nil
}

// ── validation and normalization ────────────────────────────────────────────

// contentIdea, contentImpl, contentDisc and contentApproval are the parts a
// WRITE carries, as one value per kind, so the version digest is over the content
// and never over the server's own stamp. A record re-sent unchanged is the same
// version; a record re-sent at midnight on a different day is the same version
// too, which is what an idempotent append has to mean.
type contentIdea struct {
	Kind            string                      `json:"kind"`
	Title           string                      `json:"title"`
	Statement       string                      `json:"statement"`
	Problem         string                      `json:"problem"`
	Alternatives    []PatentRejectedAlternative `json:"alternatives"`
	PriorArt        string                      `json:"prior_art"`
	PatentSignature *PatentSignature            `json:"signature,omitempty"`
	Notes           string                      `json:"notes"`
}

type contentImpl struct {
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
	Notes          string   `json:"notes"`
}

type contentDisc struct {
	Title             string   `json:"title"`
	Summary           string   `json:"summary"`
	Claims            string   `json:"claims"`
	IdeaIDs           []string `json:"idea_ids"`
	ImplementationIDs []string `json:"implementation_ids"`
	Body              string   `json:"body"`
	Status            string   `json:"status"`
	FilingRef         string   `json:"filing_ref"`
}

type contentApproval struct {
	Role     string `json:"role"`
	Decision string `json:"decision"`
	Comment  string `json:"comment"`
}

func validIdea(a *PatentIdea) error {
	if strings.TrimSpace(a.ID) == "" {
		return zip.ErrBadRequest("an idea needs a stable id")
	}
	if !ideaKinds[a.Kind] {
		return zip.ErrBadRequest("kind must be problem, alternative, architecture, mechanism, decision or rejection")
	}
	if len(strings.TrimSpace(a.Statement)) < 20 {
		return zip.ErrBadRequest("statement is the conception itself — it has to be definite enough to implement")
	}
	return nil
}

func validImpl(a *PatentImplementation) error {
	if strings.TrimSpace(a.ID) == "" {
		return zip.ErrBadRequest("an implementation needs a stable id")
	}
	if len(a.IdeaIDs) == 0 {
		return zip.Errorf(422, "an implementation must cite the idea records it implements — "+
			"with nothing before it in the log, the record says the tool did the conceiving")
	}
	if a.HumanReview != "" && !reviews[a.HumanReview] {
		return zip.ErrBadRequest("human_review must be accepted, edited, rewritten or none")
	}
	return nil
}

func validDisclosure(a *PatentDisclosure) error {
	if strings.TrimSpace(a.ID) == "" {
		return zip.ErrBadRequest("a disclosure needs a stable id")
	}
	if len(a.IdeaIDs) == 0 && len(a.ImplementationIDs) == 0 {
		return zip.Errorf(422, "a disclosure must cite the records it rests on")
	}
	if a.Status != "" && !statuses[a.Status] {
		return zip.ErrBadRequest("status must be draft, reviewed, approved or filed")
	}
	return nil
}

func validApproval(a *PatentApproval) error {
	if strings.TrimSpace(a.DisclosureID) == "" {
		return zip.ErrBadRequest("an approval needs a disclosure_id")
	}
	if !humanRoles[a.Role] {
		return zip.ErrBadRequest("role must be author, cto, reviewer, attorney or counsel")
	}
	if !decisions[a.Decision] {
		return zip.ErrBadRequest("decision must be approved, rejected or needs-work")
	}
	return nil
}

// revision normalizes a declared version kind. An unrecognized value is read as
// original rather than dropped: a producer's typo must not silently rewrite
// history, and it must not silently withdraw an entry either — only the literal
// "retracted" does that.
func revision(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "corrected":
		return "corrected"
	case "retracted":
		return "retracted"
	default:
		return "original"
	}
}

// status defaults an unset disclosure status to draft, which is the only honest
// starting state for a document nobody has reviewed.
func status(s string) string {
	if s = strings.ToLower(strings.TrimSpace(s)); s == "" {
		return "draft"
	}
	return s
}

// review defaults an unset disposition to accepted, and that default is the one
// field here worth arguing about: a record that says nothing about what a human
// did with generated code most often means they took it as-is, and reading
// silence as "none" (no model involved) would understate the very fact the
// disclosure exists to state.
func review(s string) string {
	if s = strings.ToLower(strings.TrimSpace(s)); s == "" {
		return "accepted"
	}
	return s
}

// stampAt is the server's clock in the one format this surface publishes.
func stampAt() string { return clock().Format(time.RFC3339) }

// author is the validated human subject this request arrived as, which is the
// attribution every record carries. It is read off the REQUEST rather than the
// caller's identity alone because the org is not the question here: §115 asks
// which natural person conceived, and only the token knows. An empty answer is
// preserved as empty rather than defaulted to the org — a record authored by a
// tenant is a record authored by nobody, and proof says so.
func author(ctx context.Context) string {
	c, ok := cloud.Request(ctx)
	if !ok {
		return ""
	}
	// Validated-ness first: c.User() is the sanitized header the identity
	// middleware mints from a verified IAM token, and an unauthenticated request
	// is the one case where a header could ride along from the client. Asking
	// principal.Org is how that fact is checked rather than assumed.
	if _, ok := principal.Org(c); !ok {
		return ""
	}
	return strings.TrimSpace(c.User())
}
