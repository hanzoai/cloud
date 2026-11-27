package patent

import (
	"context"
	"net/http"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// maxBatchItems bounds one recorded batch so a single request cannot pin the
// writer with an unbounded body. The global body limit is the coarse guard; this
// is the honest per-batch one.
const maxBatchItems = 2000

// ideaKinds is the closed set of conception entries. Each is a different answer
// to "what did the human know before the prompt", and `rejection` is in the set
// on purpose: what was tried and ruled out is the strongest available evidence
// that a specific path was CHOSEN rather than received.
var ideaKinds = map[string]bool{
	"problem": true, "alternative": true, "architecture": true,
	"mechanism": true, "decision": true, "rejection": true,
}

// humanRoles is the closed set of sign-off authorities on a disclosure. It is
// named rather than free text so "approved" always means approved by somebody
// with a stated job, and a blank role can never carry a disclosure forward.
var humanRoles = map[string]bool{
	"author": true, "cto": true, "reviewer": true, "attorney": true, "counsel": true,
}

// decisions is the closed set an approval takes. `needs-work` is a decision and
// not an absence of one: it is dated, attributed and retained like a sign-off.
var decisions = map[string]bool{"approved": true, "rejected": true, "needs-work": true}

// state is what this deployment can promise about the records it keeps, decided
// at build from what the host actually wired — not from a request.
type state struct {
	stores  *cloud.OrgStore[*store]
	durable bool
	// signable is whether a KMS that could verify a detached signature is in
	// reach. It is a capability check, never an assumption: absent, no record
	// carrying a signature is reported as checked.
	signable bool
}

func Use(app cloud.Router, deps cloud.Deps) error {
	return cloud.Use(app, deps, "patent", build, routes)
}

func build(b cloud.Base) (state, error) {
	// Each org's patent.db rides the same HA path research's takes: elected
	// single writer, hydrate-on-open, fenced ship. b.Durable is nil on a local
	// deployment, where the store is the plain local file.
	return state{
		stores:   cloud.NewOrgStore(b, "patent", openStore),
		durable:  b.Durable != nil,
		signable: b.KMS != nil,
	}, nil
}

// anchored reports whether record roots are committed OUTSIDE this platform. It
// is false on every deployment today, and it is a function rather than a field
// because it is the seam: wiring it means folding the daily root into the
// Hanzo-L1 ledger anchor (apps/wallet.TreasuryAnchorSigner), which is a quorum
// decision and not this package's to invent.
var anchored = func() bool { return false }

// Shutdown closes every open per-org store, so a graceful stop flushes and
// releases each org's SQLite file.
func Shutdown() error { return shutdownStores() }

//go:generate go run github.com/zap-proto/zip/cmd/zipdoc

// ops carries the subsystem's state onto every typed op: a typed handler takes a
// context and its decoded In, so the state rides on the receiver.
type ops struct{ s *cloud.Service[state] }

func routes(app cloud.Router, s *cloud.Service[state]) {
	shutdownStores = s.State.stores.CloseAll
	g := app.Group("/v1/patent")
	o := ops{s: s}
	// The conception record first, the machine's work second, the document that
	// cites both third. One POST and one GET per kind — a listing narrowed by
	// ?id= IS the read of one record — so there is never a second address for
	// fetching one thing.
	g.Post("/ideas", o.postIdeas)             // append conception versions (idempotent by content)
	g.Get("/ideas", o.listIdeas)              // canonical ideas (?id= ?kind= ?project=)
	g.Post("/implementations", o.postImpls)   // append AI-assisted implementations, citing ideas
	g.Get("/implementations", o.listImpls)    // canonical implementations (?id= ?idea= ?project=)
	g.Post("/disclosures", o.postDisclosures) // assemble the disclosure document
	g.Get("/disclosures", o.listDisclosures)  // canonical disclosures (?id= ?status=)
	g.Post("/approvals", o.postApprovals)     // role sign-off on a disclosure (CTO, attorney)
	g.Get("/approvals", o.listApprovals)      // the approval trail (?disclosure=)
	g.Get("/proof", o.getProof)               // the verification bundle + the ordering verdict
	g.Get("/timeline", o.getTimeline)         // one idea's full version history, in append order
	g.Post("/grants", o.postGrant)            // the separate visibility decision; a write never grants it
	g.Get("/health", o.health)                // what is wired here, in honest states
}

var shutdownStores = func() error { return nil }

// orgStore resolves the caller's org-scoped store. Every op leads with it: the
// org is parked on the context by cloud.Bridge from a verified IAM token, and an
// In field could only ever be a tenant key the caller asserted for itself.
func (o ops) orgStore(ctx context.Context) (*store, string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, "", err
	}
	st, err := storeFor(o.s.State.stores, org)
	if err != nil {
		o.s.Log.Error("patent store open failed", "org", org, "err", err)
		return nil, "", zip.Errorf(http.StatusInternalServerError, "patent store unavailable")
	}
	return st, org, nil
}

// shipDurable is the ship-before-ack every write runs after its commit: a ship
// this replica cannot acknowledge (it is not the elected writer, or is read-only)
// is an error, never a silent local success — an evidence record that a reader
// cannot retrieve is not evidence.
func (o ops) shipDurable(org string) error {
	acked, err := shipFor(o.s.State.stores, org)
	if err != nil || !acked {
		if err != nil {
			o.s.Log.Warn("patent durable ship failed", "org", org, "err", err)
		}
		return zip.Errorf(http.StatusServiceUnavailable, "patent store failover in progress; retry")
	}
	return nil
}

// project is the caller's project sub-scope — a column inside the org, never a
// tenant key.
func project(ctx context.Context) string {
	return principal.ProjectFrom(ctx)
}

// clock is the ONE place this package reads wall time. It exists as a function
// so the tests can prove a stamped value survives a round trip, and so no
// handler can be written that takes a timestamp from the wire instead.
var clock = func() time.Time { return time.Now().UTC() }

// healthIn is a placeholder so health stays a typed op with no query surface.
type healthIn struct{}

// patentHealth is what this deployment actually guarantees about the records it is
// keeping — named honestly, including the parts that are not wired.
type patentHealth struct {
	// Durable is whether acknowledged writes are fenced to a durable object.
	// False means a store is local to this pod: the records stand, failover does
	// not carry them.
	Durable bool `json:"durable"`
	// Anchored is whether record roots are committed outside this platform.
	// False is the common case and the important one: without it the chain
	// proves order WITHIN the platform's own honesty and nothing beyond it.
	Anchored bool `json:"anchored"`
	// SignatureVerification is whether the server can verify a submitted
	// detached signature. Attribution does not depend on it — the author of
	// every record is the validated principal, never a body field — but a
	// record carrying a signature it cannot check says so.
	SignatureVerification bool `json:"signature_verification"`
	// ServerClockSays is what stamps a record: always "server", because no
	// client-supplied timestamp is read anywhere in this package.
	ServerClockSays string `json:"stamped_by"`
	// RecordedAt is the server's time now, RFC3339 — so a reader can tell how
	// stale its own view is.
	RecordedAt string `json:"now"`
}

// GetPatentHealth reports what this deployment actually guarantees about the
// records it keeps, in honest states. Anchored and durable are the two that
// change what a record is worth, and an unwired one reads as unwired rather than
// as a green light.
func (o ops) health(ctx context.Context, _ *healthIn) (*patentHealth, error) {
	// The org lookup is the auth check: an unauthenticated caller has no store
	// to ask about, and health on a surface that keeps other tenants' evidence
	// should not answer for one it cannot resolve.
	if _, _, err := o.orgStore(ctx); err != nil {
		return nil, err
	}
	return &patentHealth{
		Durable:               o.s.State.durable,
		Anchored:              anchored(),
		SignatureVerification: o.s.State.signable,
		ServerClockSays:       "server",
		RecordedAt:            clock().Format(time.RFC3339),
	}, nil
}

// grantIn is the SEPARATE authorization a record's write never implies. A
// conception entry is private the moment it is appended; publishing one to the
// org, or to a counterparty in a licence negotiation, is its own act.
type grantIn struct {
	// Project locates the record inside the caller's own org.
	Project string `json:"project"`
	// ID is the record's stable id.
	ID string `json:"id"`
	// Kind is idea, implementation or disclosure — what the id names.
	Kind string `json:"kind"`
	// Visibility is private, org or public.
	Visibility *string `json:"visibility"`
}

// patentGrantOut reports how many records a grant moved. A target that matched nothing
// is a 404 rather than a zero here, so a caller never reads a typo'd id as success.
type patentGrantOut struct {
	// Updated is how many records the grant moved to the visibility asked for.
	// It counts every RETAINED version of the target, so it is at least one.
	Updated int `json:"updated"`
}

// GrantPatentVisibility makes the decision a recording never implied: whether an
// idea, implementation or disclosure stays private to its author's project, opens
// to the org, or goes public. Publication of a conception entry is a
// disclosure under 35 U.S.C. §102 in every jurisdiction that reads it that way,
// so it is always a separate call and never a side effect of writing one down.
func (o ops) postGrant(ctx context.Context, in *grantIn) (*patentGrantOut, error) {
	st, org, err := o.orgStore(ctx)
	if err != nil {
		return nil, err
	}
	if in.ID == "" {
		return nil, zip.ErrBadRequest("grant needs an id")
	}
	if !grantKinds[in.Kind] {
		return nil, zip.ErrBadRequest("kind must be idea, implementation or disclosure")
	}
	if in.Visibility == nil || !visibilities[*in.Visibility] {
		return nil, zip.ErrBadRequest("visibility must be one of private, org, public")
	}
	proj := in.Project
	if proj == "" {
		proj = project(ctx)
	}
	n, err := st.setGrant(ctx, proj, in.Kind, in.ID, *in.Visibility)
	if err != nil {
		o.s.Log.Error("patent grant failed", "org", org, "err", err)
		return nil, zip.Errorf(http.StatusInternalServerError, "patent grant failed")
	}
	if n == 0 {
		return nil, zip.ErrNotFound("no records for that target")
	}
	if e := o.shipDurable(org); e != nil {
		return nil, e
	}
	return &patentGrantOut{Updated: n}, nil
}

// visibilities and grantKinds are the closed sets a grant is checked against, so
// a typo refuses loudly instead of listing everything under an unset filter.
var (
	visibilities = map[string]bool{"private": true, "org": true, "public": true}
	grantKinds   = map[string]bool{"idea": true, "implementation": true, "disclosure": true}
)

// storeFor is the ONE way this package reaches a database: name it through
// orgns.OrgNamespace — the single place a validated org goes through — and ask
// the registry for that name, so "which file does this request touch" has one
// answer from one input.
func storeFor(stores *cloud.OrgStore[*store], org string) (*store, error) {
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return nil, err
	}
	return stores.For(ns)
}

// shipFor names the same database the write went to and ships THAT one, so a
// write and its ship can never address different files.
func shipFor(stores *cloud.OrgStore[*store], org string) (bool, error) {
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return false, err
	}
	return stores.Sync(ns)
}
