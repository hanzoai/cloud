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
