package claw

// The approval queue.
//
// A reviewer answers three kinds of request from one queue: a command an
// executor stopped to ask about, a capability a plugin wants, and a change the
// system agent proposed. The client asks the three lists at once and merges
// them into a single pane (ui/src/app/exec-approval.ts), which is why the queue
// is one queue read three ways rather than three stores.
//
//	exec.approval.list      commands waiting on a person
//	plugin.approval.list    capabilities a plugin asked for
//	openclaw.approval.list  changes the system agent proposed
//
// Nothing in this cloud raises an approval. clients/exec forwards a program to
// an isolated executor and answers with its output — there is no point in that
// run where a command stops to ask — and clients/plugin mounts a
// deployment-wide manifest rather than a per-org install that could request a
// capability. So all three lists are empty, and empty is the truth rather than
// a placeholder: the pane renders nothing, which is what an operator with
// nothing to review should see.
//
// The resolver is not registered. approval.resolve records one reviewer's
// decision, and a decision needs something to decide on; with an empty queue it
// could only ever answer that the approval is gone. The three lists are here
// because a true empty list is an answer; a resolver that can only refuse is
// not.
//
// What a real one needs is something that raises a request and a place to hold
// it until an operator decides. When that lands it is ONE queue — these three
// methods are its three readings, discriminated by approvalKind — and the
// resolver arrives with it, along with the two events an approval raises
// (openclaw.approval.requested and openclaw.approval.resolved).
//
// Parameters are not bound. The protocol declares no schema for any of the
// three and the TypeScript that serves them ignores what they are sent, so
// closing them here would refuse a caller the protocol accepts.

func init() {
	Register("exec.approval.list", Approvals, execApprovals)
	Register("plugin.approval.list", Approvals, pluginApprovals)
	Register("openclaw.approval.list", Approvals, agentApprovals)
}

// approval is one request awaiting a decision, in the field set every reader of
// the merged queue depends on: the client drops an entry with no id, no
// lifetime, or a request its kind cannot parse (ui/src/app/exec-approval.ts).
// approvalKind is what routes a decision back to the right owner, so a producer
// sets it to the kind whose list it is answering.
//
// Request is the reviewer-safe half — what the change is called, what it would
// run, and the digest that identifies the exact proposal a decision applies to.
// The operation itself stays with whoever raised it, so a reviewer decides on a
// description and a digest rather than on a payload the gateway could alter.
type approval struct {
	Kind      string   `json:"approvalKind"`
	ID        string   `json:"id"`
	Request   any      `json:"request"`
	Decisions []string `json:"allowedDecisions,omitempty"`
	Created   int64    `json:"createdAtMs"`
	ExpiresAt int64    `json:"expiresAtMs"`
}

// The three kinds one queue is read through.
const (
	approvalExec   = "exec"
	approvalPlugin = "plugin"
	approvalAgent  = "system-agent"
)

// Each list answers a bare array, not an object carrying one. The client merges
// the three with Promise.allSettled and reads a fulfilled non-array as an empty
// contribution, so a wrapper would empty the pane rather than fill it.
func execApprovals(*Call) (any, error)   { return []approval{}, nil }
func pluginApprovals(*Call) (any, error) { return []approval{}, nil }
func agentApprovals(*Call) (any, error)  { return []approval{}, nil }
