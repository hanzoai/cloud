package git

import (
	"context"
	"errors"

	"github.com/hanzoai/cloud/apps/git/objects"
	"github.com/hanzoai/cloud/sdk"
	luxlog "github.com/luxfi/log"
)

// RunnerRoute is where optional actions-runner daemon operations mount.
const RunnerRoute = "/v1/runner"

// RunnerMount is an optional hook invoked by git.Use to mount /v1/runner
// operations onto the plugin router. Distributions that carry the runner wire
// adapter register it before cloud.Listen.
var RunnerMount func(app cloud.Router, host RunnerHost) error

// RunnerHost exposes the store, blob log, and origin helpers an external
// RunnerMount adapter needs without exporting git's internal service state.
type RunnerHost struct {
	s *cloud.Service[state]
}

func (h RunnerHost) Log() luxlog.Logger                { return h.s.Log }
func (h RunnerHost) Origin() string                    { return origin(h.s) }
func (h RunnerHost) StoreFor(org string) (*Store, error) { return storeFor(h.s, org) }

// AppendLog writes lines to a task's log in the object store.
func (h RunnerHost) AppendLog(ctx context.Context, org string, task, first int64, lines []LogLine) (int64, error) {
	blobs := h.s.State.logs
	if blobs == nil {
		return 0, objects.ErrNoBlobs
	}
	return putLog(ctx, blobs, org, task, first, lines)
}

// SplitJoinToken parses an "<org>/<pool>.<secret>" runner registration token.
func SplitJoinToken(tok string) (org, pool, secret string, ok bool) { return split(tok) }

// OrgOfRunner extracts the owning org from a runner UUID ("<org>/runner_...").
func OrgOfRunner(uuid string) string { return orgOf(uuid) }

// IsDoneStatus reports whether a stored task status is terminal.
func IsDoneStatus(status string) bool { return done(status) }

// IsNoRunner reports whether err indicates an unknown or invalid runner credential.
func IsNoRunner(err error) bool { return errors.Is(err, errNoRunner) }

// IsNoPool reports whether err indicates an unknown pool or join token.
func IsNoPool(err error) bool { return errors.Is(err, errNoPool) }

// IsNoTask reports whether err indicates a missing task row.
func IsNoTask(err error) bool { return errors.Is(err, errNoTask) }

// Bump advances an org's runner task queue version when initializing from 0.
func (s *Store) Bump(ctx context.Context, org string) error { return s.bump(ctx, org) }
