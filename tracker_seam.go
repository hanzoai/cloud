package cloud

import (
	"context"
	"errors"
)

// tracker_seam.go is the inversion layer between the native TRACKER (clients/tracker)
// and the surfaces that FEED it work items without importing it — today the GitHub
// App webhook + backfill (clients/integrations), tomorrow any provider that mirrors
// external issues into the one Hanzo work-item store. It is the SAME idiom as
// sync_seam.go's SyncFunc and git_import.go's GitImporter: the tracker registers its
// sink here at Mount; feeders call UpsertIssue with NO import of the tracker package,
// so nothing imports tracker except apps (which mounts it). One seam, one direction,
// no cycles.

// IssueUpsert is a provider-agnostic external work item mirrored into the native
// tracker, keyed idempotently by ExtRef so a webhook redelivery or a backfill re-run
// UPDATES the same row instead of duplicating it. Flat + string-typed so it crosses
// the feeder→tracker seam without importing the tracker's domain types.
//
//   - Org         the tenant (resolved from the signed installation, never a header).
//   - Project     the IAM project scope; "" ⇒ the org's default project store.
//   - ProjectKey  the tracker team the item files under (e.g. "GH"); ensured on first use.
//   - ProjectName the display name for that team when it is first created (e.g. "GitHub").
//   - Repo        the git repo the item belongs to — the per-repo filter discriminator.
//   - ExtRef      the external anchor + idempotency key (e.g. "github:owner/repo#123").
//   - Kind/Source what it IS / which surface opened it ("issue"|"pr" / "git").
//   - State       the upstream open/closed state; the tracker maps it to a board column.
//   - Labels      upstream label names (the tracker joins them for storage).
type IssueUpsert struct {
	Org         string
	Project     string
	ProjectKey  string
	ProjectName string
	Repo        string
	ExtRef      string
	Kind        string
	Source      string
	Title       string
	Description string
	State       string // "open" | "closed"
	Assignee    string
	Labels      []string
}

// IssueUpsertResult reports what the upsert did — Created (a new row) vs updated,
// plus the tracker identity, so a feeder can log/count precisely (the backfill count).
type IssueUpsertResult struct {
	Created    bool
	Number     int
	Identifier string // KEY-<number>
}

// IssueSink is the tracker's upsert entry the sink registers at Mount; feeders reach
// it via UpsertIssue. A function, not a tracker noun — the one implementation
// (clients/tracker) registers it, and the feeders never see the store.
type IssueSink func(ctx context.Context, in IssueUpsert) (IssueUpsertResult, error)

// issueSink is the registered sink (nil until the tracker mounts). Read on the
// webhook path, written once at Mount (before serving), so a plain var suffices —
// the same discipline as syncFn / gitImporter.
var issueSink IssueSink

// RegisterIssueSink installs the tracker upsert sink. nil-safe.
func RegisterIssueSink(fn IssueSink) { issueSink = fn }

// ErrIssueSinkUnavailable is returned when the tracker is not mounted — fail-closed,
// never a silent success, so a feeder logs precisely rather than dropping the item.
var ErrIssueSinkUnavailable = errors.New("cloud: tracker issue sink not registered")

// UpsertIssue mirrors one external work item into the native tracker via the
// registered sink. Fails closed when the tracker is unmounted.
func UpsertIssue(ctx context.Context, in IssueUpsert) (IssueUpsertResult, error) {
	if issueSink == nil {
		return IssueUpsertResult{}, ErrIssueSinkUnavailable
	}
	return issueSink(ctx, in)
}
