package git

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	luxlog "github.com/luxfi/log"

	"github.com/zap-proto/zip"
)

// maintenance.go keeps a hosted repo fast to CLONE. A repo populated by fetch /
// receive-pack accumulates its objects via index-pack, which writes a pack but no
// reachability BITMAP and no COMMIT-GRAPH — so every `git upload-pack` walks the
// whole object graph to build the clone pack (slow, and O(objects) work per
// clone). Repacking with a bitmap lets pack-objects REUSE the bitmap, and the
// commit-graph makes commit/ancestry queries (log, merge-base, the browse UI) a
// memory-mapped read. This is the same housekeeping every large git host runs; here it
// goes through the ONE hardened git-exec client (gitexec.go) with the SAME memory
// bounds + pack-concurrency slot as every other pack op, so gc'ing a multi-GB
// repo can never OOM the pod. Per the object-plane program (#31).

// runMaintenance repacks a bare repo into a single reachability-bitmapped pack
// and (re)writes its commit-graph with changed-path Bloom filters. Runs under one
// pack slot with packConfigArgs memory bounds. Both steps are crash-safe: git
// writes new files and swaps them atomically (repack under its own lock,
// commit-graph into objects/info), so an interrupted maintenance leaves the repo
// valid — just not yet optimized.
func runMaintenance(ctx context.Context, bareDir string) error {
	steps := []struct {
		name string
		args []string
	}{
		// -a -d -l: pack every local object into one pack and drop the packs it
		// supersedes; --write-bitmap-index: the clone-speed artifact.
		{"repack", append(packConfigArgs(""), "--git-dir="+bareDir,
			"repack", "-a", "-d", "-l", "--write-bitmap-index")},
		// --reachable: graph every ref tip; --changed-paths: path Bloom filters so
		// path-limited log (the browse history view) is fast too.
		{"commit-graph", []string{"--git-dir=" + bareDir,
			"commit-graph", "write", "--reachable", "--changed-paths"}},
	}
	return withPackSlot(ctx, func() error {
		for _, st := range steps {
			cmd, err := gitCmd(ctx, nil, st.args...)
			if err != nil {
				return err
			}
			stderr := &cappedBuffer{cap: stderrCap}
			cmd.Stderr = stderr
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("git %s: %w: %s", st.name, err, strings.TrimSpace(stderr.String()))
			}
		}
		return nil
	})
}

// autoMaintain fires git's own housekeeping heuristic after a push. `gc --auto`
// is a near-instant NO-OP unless the loose-object / pack-count thresholds are
// crossed, at which point git repacks (writing a bitmap for a bare repo) +
// rewrites the commit-graph — so repos stay fast as pushes accumulate packs,
// without a scheduler. It YIELDS: if every pack slot is busy serving clones it
// skips this round (the next push retries) rather than queue behind them. Fully
// best-effort — housekeeping never fails or blocks the push it follows, so the
// caller fires it as `go autoMaintain(...)`.
func autoMaintain(log luxlog.Logger, bareDir string) {
	if !tryAcquirePackSlot() {
		return
	}
	defer releasePackSlot()
	cmd, err := gitCmd(context.Background(), nil,
		append(packConfigArgs(""), "--git-dir="+bareDir, "gc", "--auto", "--quiet")...)
	if err != nil {
		log.Warn("git gc --auto build failed", "dir", bareDir, "err", err)
		return
	}
	stderr := &cappedBuffer{cap: stderrCap}
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		log.Warn("git gc --auto failed", "dir", bareDir, "err", err, "stderr", strings.TrimSpace(stderr.String()))
	}
}

// maintain repacks the org-validated bare repo. errNotFound when the repo is
// absent so the handler can 404.
func (s *storage) maintain(ctx context.Context, org, project, name string) error {
	if !s.exists(org, project, name) {
		return errNotFound
	}
	return runMaintenance(ctx, s.absRepoPath(org, project, name))
}

// gcOut reports a completed repack.
type gcOut struct {
	// Repo is the repo that was repacked.
	Repo string `json:"repo"`
	// SizeBytes is the size measured AFTER the repack — usually smaller, since
	// repacking drops the packs it supersedes.
	SizeBytes int64 `json:"sizeBytes"`
	// Maintained is always true; the call fails rather than reporting false.
	Maintained bool `json:"maintained"`
}

// gc repacks a repo into one bitmapped pack and rewrites its commit-graph, so
// the next clone reuses the bitmap instead of walking the whole object graph.
// Idempotent, and safe to interrupt — git swaps both artifacts atomically. It
// runs under one pack slot with the same memory bounds as a clone, so it can
// block behind heavy pack traffic rather than compete with it. Storage usage is
// re-measured afterwards, since a repack reclaims space.
//
// Example: {"name": "widgets"}
//
//	Response: {"repo": "widgets", "sizeBytes": 3072, "maintained": true}
func (o ops) gc(ctx context.Context, in *repoRef) (*gcOut, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	name := normalizeName(in.Name)
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("name must match ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$")
	}
	if err := o.s.State.storage.maintain(ctx, t.org, t.project, name); err != nil {
		if errors.Is(err, errNotFound) {
			return nil, zip.ErrNotFound("repo not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "maintain: %v", err)
	}
	// Repack reclaims redundant packs — re-measure so usage reflects the new size.
	size := recordUsage(o.s, context.WithoutCancel(ctx), t.org, t.project, name)
	return &gcOut{Repo: name, SizeBytes: size, Maintained: true}, nil
}
