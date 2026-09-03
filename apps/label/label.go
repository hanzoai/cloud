// Package label is the ground-truth plane: what actually turned out to be
// fraud, who said so, and when they could first have said it.
//
// It closes the loop the model plane cannot close for itself. /v1/risk decides,
// /v1/ml learns — and learning needs an answer key that arrives LATE, from
// several places, sometimes in disagreement:
//
//	chargeoff   our own books writing a balance off
//	dispute     a card network's adjudicated chargeback, normalised by commerce
//	case        a compliance determination closed under /v1/aml
//	refund      a merchant refunding with a fraud reason
//	review      one analyst's call on one decision
//	sample      a judged draw from the below-the-line reproducible sample
//
// THREE HARD PARTS, EACH ANSWERED BY A NAMED MECHANISM.
//
//	LATENCY     Every assertion carries three times: `at`, when the judged event
//	            happened; `seen`, when the filer says it became knowable; and
//	            `knowable`, the later of `seen` and the server clock at the
//	            write — derived here, never supplied. A chargeback lands 30 to
//	            120 days after the transaction. Resolve takes an observation
//	            instant and shows only what was knowable then, so a training set
//	            built for an event can never contain a label that did not exist
//	            when the model would have had to act. The guard reads the DERIVED
//	            instant, because a guard whose only input is a value the caller
//	            chose is exactly as strong as the caller's honesty.
//	            resolve.go, Window; fact.go, Fact.Knowable.
//
//	CONFLICT    Two sources that disagree BOTH stay. A total order over
//	            adjudication weight picks the one in force and RETURNS the losers
//	            beside it, so a contested label is visible as contested rather
//	            than resolved into silence. There is no UPDATE statement in this
//	            package. resolve.go, stronger().
//
//	PROVENANCE  Every assertion names its source, the evidence record behind it
//	            and the identity that filed it — the last stamped server-side from
//	            the validated principal, never from the body. A label with no
//	            evidence is refused at admission, because a label that cannot be
//	            traced cannot be defended when the adverse action it fed is
//	            challenged. fact.go, admit().
//
// TWO PLANES, ONE DIRECTION. The tenant's own encrypted SQLite file is the record
// (store.go); hanzo.risk_label is a derived copy for joining at training scale
// (mirror.go). The record is written first and the mirror's failure is reported
// rather than fatal. Nothing here rides /v1/event, which is best-effort by
// design and drops on purpose.
//
// SHIP BEFORE ACK. Every op that writes ships the tenant's file to its durable
// object before it answers, and an unacked ship fails the request (state.ship).
// cloud deploys strategy Recreate at one replica: the successor hydrates the
// durable snapshot OVER the local file, so a write that was acknowledged and not
// shipped is not merely at risk — it is overwritten by an older copy of the same
// tenant's history on the ordinary rollout path.
//
// WHY IT IS ITS OWN SUBSYSTEM. Its writers are mostly not the decision plane —
// commerce adjudicates the dispute, the compliance face closes the case, an
// analyst files the review — its readers are the dataset materialiser and the
// evaluator, and its retention clock is its own: a label that fed an adverse
// action is a compliance record whose life is not the life of the decision that
// cited it. One owner per file also means the decision plane's single-writer
// SQLite is never opened by a second process.
package label

import (
	"context"
	"fmt"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/tenant"
	"github.com/hanzoai/namespace"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// state is everything the surface reads: the per-tenant record planes, the
// brand half of the tenant key, and the log.
type state struct {
	// brand is the half of the tenant key that never comes from a header. It
	// comes from Deps.Brand, which the binary is started with.
	brand  string
	stores *cloud.OrgStore[*store]
	// derived is how the columnar copy is reached. It is a VALUE on the state and
	// not a package function, so a suite can stand in for a warehouse this box
	// does not have — and so a test that means to exercise an unreachable
	// warehouse says so, instead of passing because the environment happened to
	// lack one. A test that depends on something being absent stops testing the
	// day it is present, and nothing announces that.
	derived columnar
	log     luxlog.Logger
}

// mounted is the process handle, so Shutdown closes what Mount opened without a
// second owner of the state.
var mounted *state

// Mount registers /v1/label.
//
// EVERY INHERITED CAPABILITY IS NAMED HERE, EXPLICITLY:
//
//	IAM auth     SanitizeIdentity mints the verified org upstream (serve.go).
//	             This package never validates a token and never can.
//	tenant gate  cloud.Bridge(), which the composer installs — the fused host
//	             at its root, a plugin program in its constructor. A typed op
//	             receives only a context; Bridge is what parks the validated
//	             principal in it, and this package only reads it.
//	durability   cloud.WithDurable routes each tenant file through the ha-elected
//	             single writer, and every op that writes calls state.ship before
//	             it answers. Wiring the option alone is NOT durability: it only
//	             makes a ship possible, and a subsystem that never ships holds an
//	             acknowledged record in a local file that the next pod hydrates
//	             an older snapshot over. cloud deploys strategy Recreate at one
//	             replica, so that is the ordinary rollout and not a rare fault.
//	logs         cloud.NewBase gives the scoped luxlog.
//
// NOT metered, deliberately, and this is the one inherited capability declined
// rather than wired. A label can be the input to an adverse action, so recording
// one is a compliance obligation and not a purchase; a balance gate in front of
// it would mean a tenant that fell behind on its bill could no longer record the
// ground truth for a decision it is about to be challenged on. The DoS bound is
// carried by the per-op limits in typed.go instead, which cost nothing to be
// wrong about.
func Use(app cloud.Router, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("label.Use:  nil app")
	}
	s, err := build(deps)
	if err != nil {
		return err
	}
	mounted = s.State
	routes(app, s)
	s.Log.Info("label plane mounted", "brand", cloud.Brand(), "env", cloud.Env())
	return nil
}

// build constructs the subsystem. Separate from Mount because Mount also
// installs the routes and publishes the process handle, and a test wants the
// state without either — the same split apps/risk and apps/ml make for the same
// reason.
func build(deps cloud.Deps) (*cloud.Service[*state], error) {
	if luxlog.Default() == nil {
		return nil, fmt.Errorf("label.Use:  nil luxlog.Default()")
	}
	if cloud.DataDir() == "" {
		return nil, fmt.Errorf("label.Use:  empty cloud.DataDir(), so no record could be kept")
	}
	if cloud.Brand() == "" {
		// The brand is half the tenant key. A deployment that did not state one
		// cannot mint a key and every request would refuse — better to say so at
		// boot than once per request.
		return nil, fmt.Errorf("label.Use:  no brand, so no tenant key can be minted")
	}
	b := cloud.NewBase(deps, "label")
	return &cloud.Service[*state]{Base: b, State: &state{
		brand:   cloud.Brand(),
		stores:  cloud.NewOrgStore[*store](b, "label", openStore),
		derived: warehouse,
		log:     b.Log,
	}}, nil
}

// Shutdown closes every open record plane, which is what flushes each tenant's
// file to its durable slot.
func Shutdown() error {
	if mounted == nil || mounted.stores == nil {
		return nil
	}
	return mounted.stores.CloseAll()
}

// scope is everything an op needs to act for one tenant: the minted, qualified
// key the SHARED columnar plane is written under, the NAME of the tenant's own
// file, and the identity that asserted. All three come from the same validated
// principal, resolved once.
type scope struct {
	tenant tenant.Key
	// ns names the record plane this request writes. It travels with the scope so
	// the ship names the same file the write went to — a ship that re-derived the
	// name from anything else would be a second answer to "which file is this".
	ns namespace.Namespace
	by string
}

// tenantOf resolves the caller's scope from the VALIDATED principal.
//
// FAIL CLOSED OFF THE HTTP PATH. A CLI LocalInvoke has no request, so there is no
// validated principal and no tenant to act for — the same refusal a forged
// X-Org-Id gets, from the same line, with no second gate to keep in sync.
func tenantOf(ctx context.Context, s *cloud.Service[*state]) (scope, *store, error) {
	c, ok := cloud.Request(ctx)
	if !ok {
		return scope{}, nil, zip.ErrForbidden("no validated principal")
	}
	org, err := principal.Acting(ctx)
	if err != nil {
		return scope{}, nil, err
	}
	// ONE MINT, and it is [tenant.Of]. The key this plane writes is the same key
	// apps/datasets writes and apps/risk reads, so a second spelling of it here
	// would be two planes disagreeing about whose a row is — which is the defect
	// with the sign flipped, and it is silent. The mint canonicalises the brand
	// half and refuses one no registry vouches for; a local Qualify did neither,
	// so a deployment started with CLOUD_BRAND=Hanzo would have filed `Hanzo/acme`
	// against a reader asking for `hanzo/acme`.
	t, err := tenant.Of(ctx, s.State.brand)
	if err != nil {
		return scope{}, nil, err
	}
	// THE ORG IS THE GRAIN, AND THE PROJECT IS NOT.
	//
	// The derived columnar copy is keyed on `<brand>/<org>` and can be keyed on
	// nothing finer: that is the key hanzo.risk_feature carries, and a label that
	// cannot be joined to a feature row is a label nobody can train on. Naming the
	// RECORD at a finer grain would give the two planes different opinions about
	// whose row it is — one project's retention sweep would identify ids in its own
	// file and delete them from a warehouse partition shared with every other
	// project, and a resolve would answer from a slice of the tenant's ground truth
	// while a materialisation answered from all of it. One grain, both planes.
	//
	// It is also the right grain on its own terms. Ground truth is about an entity
	// in the tenant's namespace — this account defrauded us — and a project is a
	// division of compute, not of what turned out to be true.
	ns, err := cloud.OrgNamespace(org, "")
	if err != nil {
		return scope{}, nil, err
	}
	st, err := s.State.stores.For(ns)
	if err != nil {
		return scope{}, nil, err
	}
	return scope{tenant: t, ns: ns, by: actor(c, org)}, st, nil
}

// ship is the SHIP-BEFORE-ACK step: the tenant's file goes to its durable object,
// fenced at the lease round, before this request tells anyone the record was
// kept. It names the namespace the write went to — the one carried on the scope —
// so a write and its ship can never address different files.
//
// IT IS NOT BEST EFFORT, AND THIS IS THE ONE PLACE THAT MATTERS MOST. cloud runs
// strategy Recreate at one replica: every rollout is an ungraceful teardown for
// anything not yet shipped, and the successor hydrates the DURABLE snapshot over
// the local file. A write that was acknowledged and not shipped is therefore not
// merely at risk — it is overwritten, silently, by an older copy of the same
// tenant's history. For a plane whose rows can be the input to an adverse action,
// "we told you we kept it" has to mean it survives the pod that said so.
//
// An unacked ship means this replica is not the org's elected writer, or was
// deposed mid-request. That is a failure, never a shrug: acknowledging a local
// write on a non-owner is how two divergent copies of one tenant's compliance
// record come to exist. The caller retries against the new owner, and every write
// on this surface is idempotent on the assertion's content digest, so a retry
// costs nothing. The two sibling durable planes hold the same contract
// (apps/research shipFor, apps/books shipLedger).
//
// On a local-only deployment there is no Durability and Sync acks trivially, so
// this is a no-op there rather than a second code path.
func (s *state) ship(ns namespace.Namespace) error {
	acked, err := s.stores.Sync(ns)
	if err != nil {
		return err
	}
	if !acked {
		return fmt.Errorf("this replica is not the elected writer for the tenant, so the write is not acknowledged")
	}
	return nil
}

// actor renders WHO asserted, as `<home org>/<user>`.
//
// ALWAYS QUALIFIED, never conditionally. The home org is the caller's identity
// anchor and the effective org is the one being acted in, and for a platform
// SuperAdmin acting inside a customer's tenant the two DIFFER — which is exactly
// the case an adverse-action audit most needs to see. Rendering the pair only
// when they differ would make the common form and the significant form
// indistinguishable in a log; rendering both always means `acme/u_alice` and
// `admin/u_z` are one shape and the difference is visible at a glance.
//
// It is read from the VALIDATED principal. There is no wire field for it and
// there cannot be: an attributable record whose attribution the caller chose is
// not attributable.
func actor(c *zip.Ctx, org string) string {
	home := principal.Owner(c)
	if home == "" {
		// A pre-rollout gateway has not minted the home claim. The effective org
		// is then the only anchor there is, and naming it is more honest than
		// leaving the record unattributed.
		home = org
	}
	user := strings.TrimSpace(c.User())
	if user == "" {
		return home
	}
	return home + "/" + user
}
