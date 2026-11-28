package cloud

// peers.go — how the framework reaches the fleet's other apps from a process that does
// not host them.
//
// The registries in this package (the issue sink, the sync engine, the git importer)
// answer in-process when the owning app is co-resident. When it is not, the call goes to
// that app over the plane, and the plane's request and reply types are the host's op
// contracts, which the framework does not name. So the framework states each crossing as a
// function over its OWN shapes, and the host fills them in at boot, converting at the
// boundary (peers_host.go). A hook left nil answers [ErrNoPeer]: no such app here.

import (
	"context"
	"sync/atomic"
)

// Peers is the plane crossings the framework makes on its own account.
type Peers struct {
	// UpsertIssue files one external work item in the task app.
	UpsertIssue func(ctx context.Context, in IssueUpsert) (IssueUpsertResult, error)
	// Sync dispatches one event to the sync app.
	Sync func(ctx context.Context, ev SyncEvent) (SyncResult, error)
	// EnsureGitMirror declares or withdraws a repo's outbound mirror target.
	EnsureGitMirror func(ctx context.Context, org, project, repo, url string, enabled bool) error
	// ImportGitRepo asks the sync app to import a repo.
	ImportGitRepo func(ctx context.Context, req GitImportReq) error
	// InboundGitSync asks the sync app to advance one ref from an upstream push.
	InboundGitSync func(ctx context.Context, req GitInboundReq) (GitSyncResult, error)
	// GitRepoStatuses asks the sync app for the per-repo import and sync status.
	GitRepoStatuses func(ctx context.Context, org, project string, names []string) (map[string]GitRepoStatus, error)
	// ProjectOwnership asks the project app whether org owns idOrSlug (mine) and whether
	// another org does (other).
	ProjectOwnership func(ctx context.Context, org, idOrSlug string) (mine, other bool, err error)
	// TierOf asks commerce which tier subject is on, "" for none. ctx already states the org.
	// A tier is a class of plans (free, starter, pro, enterprise); the plan is PlanOf's.
	TierOf func(ctx context.Context, subject string) (string, error)
	// PlanOf asks commerce which catalog plan subject is served as ("unlimited", "pro-3x",
	// "go", ...), "" for none. ctx already states the org.
	PlanOf func(ctx context.Context, subject string) (string, error)
	// PlanHolderOf asks commerce which account's subscription serves subject's borrowed plan
	// ("person:hanzo/james", "org:acme"), "" when subject uses its own books or holds none.
	PlanHolderOf func(ctx context.Context, subject string) (string, error)
	// OwnerOf asks IAM whether person ("<home org>/<name>") holds an owner membership in
	// org.
	OwnerOf func(ctx context.Context, person, org string) (bool, error)
	// RateOf asks commerce for the published price of one unit of product/meter, in
	// nano-dollars; found is false when none is published.
	RateOf func(ctx context.Context, product, meter string) (nano int64, found bool, err error)
	// ModelPriceOf asks the model API what model lists at.
	ModelPriceOf func(ctx context.Context, model string) (ModelPrice, bool, error)
	// PlanCovers asks the ai app (read-only) whether payer's active paid plan
	// currently covers a call to model with remaining allowance.
	PlanCovers func(ctx context.Context, subject, org, actor, model string, fast bool) (bool, error)
	// FlagHeld asks the flags app whether the caller's org holds the flag key.
	FlagHeld func(ctx context.Context, key string) (bool, error)
	// BetaOn asks the flags app whether the platform's beta switch is on for the caller.
	BetaOn func(ctx context.Context) (bool, error)
	// KMS runs one op (get, put, delete or sign) of the kms app over the plane.
	KMS func(ctx context.Context, op KMSOp, ref string, value []byte) ([]byte, error)
}

// ModelPrice is what a model lists at, in US dollars a million tokens by class, each an
// exact decimal. A cache class the price does not state is "".
type ModelPrice struct {
	Input, Output, CacheRead, CacheWrite string
}

// KMSOp names one operation of the kms app.
type KMSOp string

const (
	KMSGet  KMSOp = "get"
	KMSPut  KMSOp = "put"
	KMSDel  KMSOp = "delete"
	KMSSign KMSOp = "sign"
)

var peers atomic.Pointer[Peers]

// SetPeers installs the plane crossings. The host calls it once, at boot.
func SetPeers(p Peers) { peers.Store(&p) }

func peersOf() Peers {
	if p := peers.Load(); p != nil {
		return *p
	}
	return Peers{}
}
