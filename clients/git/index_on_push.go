package git

import (
	"context"
	"fmt"
	"sync"
	"time"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"

	"github.com/hanzoai/cloud"
	tasksclient "github.com/hanzoai/tasks/pkg/sdk/client"
	"github.com/hanzoai/tasks/pkg/sdk/temporal"
	tasksworker "github.com/hanzoai/tasks/pkg/sdk/worker"
	"github.com/hanzoai/tasks/pkg/sdk/workflow"
)

// index_on_push.go folds a repo's code into the code-intelligence index whenever a
// push lands on its default branch, so search / context / ask / refactor over
// git.hanzo.ai always reflect the current code without a manual re-index.
//
// Durable by construction: the push reactor ENQUEUES a durable index workflow on the
// ONE embedded hanzoai/tasks engine (cloud.EmbeddedTasks) — the unified queue for all
// async work — and returns immediately. A worker drains it and does the expensive
// tree read + embed, retried on failure, surviving a producer restart. This is the
// same in-process ExecuteWorkflow pattern the automations engine uses; the index job
// is one activity keyed idempotently by commit, so a re-enqueue of the same push is a
// no-op and a re-run re-reconciles the full tree (prune-on-index).
//
// Fail-soft: before the engine is wired (or if the enqueue fails) the reactor indexes
// inline on its own detached lifecycle goroutine — the identical fallback ai's durable
// ingest uses (ErrTasksNotConfigured → inline). Push-index is therefore always live;
// the engine only upgrades it from best-effort-inline to durable-retryable.
//
// Orthogonality: the git object plane never imports clients/code. The indexer is a
// func seam injected once at the composition root (SetIndexer), after both planes
// mount — git owns the reactor + the tree read (it holds the repo), code owns the
// indexing (code.IndexFiles). Nil (unwired) leaves push-index inert.
//
// Default branch only: the code index is per-repo, not per-branch, so a feature-branch
// push is skipped — indexing it would clobber the canonical index. The reactor gates on
// the repo's symbolic HEAD and only the pushed tip commit is indexed.

// indexQueue is this workflow family's own task queue (<service>-<purpose>).
const indexQueue = "code-index"

// IndexedFile is one text file handed across the seam to the code index: its
// repo-relative path and content.
type IndexedFile struct {
	Path    string
	Content string
}

// Indexer folds a pushed repo's text files into the code-intelligence index.
// Injected at the composition root so the git plane stays free of a clients/code
// import (the two planes never import each other).
type Indexer func(ctx context.Context, org, billingOrg, project, repo string, files []IndexedFile) error

var indexer Indexer

// SetIndexer injects the code-index reactor. The composition root calls it once,
// after the git and code subsystems both mount. Nil leaves push-index inert.
func SetIndexer(fn Indexer) { indexer = fn }

// indexInput is the durable workflow payload: the repo coordinates + the exact commit
// to index. Small by design — the worker reads the tree from the object plane at
// execution time rather than carrying file bytes through the durable store. Org is the
// isolation boundary (the index store is per-org); it also pays for its own indexing.
type indexInput struct {
	Org     string `json:"org"`
	Project string `json:"project"`
	Repo    string `json:"repo"`
	Commit  string `json:"commit"`
}

// Read-side bounds: skip a blob larger than the file cap, and stop once the tree read
// reaches the total cap — so the reactor never pulls an unbounded tree into memory
// before handing it to the indexer (which applies its own caps again). A binary blob
// is skipped outright; only text reaches the code plane.
const (
	maxIndexFileBytes  = 1 << 20  // 1 MiB per file
	maxIndexTotalBytes = 64 << 20 // 64 MiB per push
	maxIndexTreeFiles  = 20000
)

// indexOnPush enqueues a durable index of the pushed default-branch tip, or indexes
// inline if the engine is not ready. Detached + best-effort: it rides the lifecycle
// fan-out (its own goroutine, never blocks the push) and every failure is logged, not
// returned.
func indexOnPush(s *cloud.Service[state], ctx context.Context, ev cloud.LifecycleEvent) {
	if indexer == nil || ev.Kind != cloud.LifecyclePushLanded || ev.Org == "" || ev.Repo == "" || ev.Branch == "" {
		return
	}
	repo, err := openRepoForIndex(s, ev.Org, ev.Project, ev.Repo)
	if err != nil {
		s.Log.Warn("code-index: open failed", "org", ev.Org, "repo", ev.Repo, "err", err)
		return
	}
	if !isDefaultBranch(repo, ev.Branch) {
		return // a feature-branch push never rewrites the canonical index
	}
	in := indexInput{Org: ev.Org, Project: ev.Project, Repo: ev.Repo, Commit: ev.After}
	if err := enqueueIndex(ctx, in); err != nil {
		// Engine not wired yet / enqueue failed: index inline now (fail-soft, the ai
		// durable-ingest contract). The tree is already open — reuse it.
		if e := readAndIndex(ctx, s, in); e != nil {
			s.Log.Warn("code-index: inline index failed", "org", ev.Org, "repo", ev.Repo, "err", e)
			return
		}
		s.Log.Info("code-index: indexed inline (engine not ready)", "org", ev.Org, "repo", ev.Repo, "branch", ev.Branch)
		return
	}
	s.Log.Info("code-index: enqueued", "org", ev.Org, "repo", ev.Repo, "branch", ev.Branch, "commit", ev.After)
}

// ---- durable enqueue (producer) + worker ----

var errEngineNotReady = fmt.Errorf("tasks engine not ready")

// enqueueIndex starts a durable IndexRepoWorkflow for one push. The workflow id is
// keyed by commit so the same push never double-indexes (ExecuteWorkflow is idempotent
// on the id) and a redelivery is a no-op.
func enqueueIndex(ctx context.Context, in indexInput) error {
	cli, err := indexEngineClient()
	if err != nil {
		return err
	}
	_, err = cli.ExecuteWorkflow(ctx, tasksclient.StartWorkflowOptions{
		ID:        fmt.Sprintf("code.index:%s:%s:%s", in.Org, in.Repo, in.Commit),
		TaskQueue: indexQueue,
	}, IndexRepoWorkflow, in)
	return err
}

var (
	indexClientMu sync.Mutex
	indexClient   tasksclient.Client
)

// indexEngineClient lazily dials the ONE embedded engine and registers the index
// worker (once, process-lifetime), memoizing the client. Dialed on the always-
// registered `default` namespace — the embedded engine registers only `default` at
// boot, so a per-org namespace would block; isolation rides in the workflow input
// (indexInput.Org), mirroring ai's durable ingest. The engine is nil until
// wireDurableIngest runs (after MountAll), long before any push — but a nil engine
// fails soft to inline indexing.
func indexEngineClient() (tasksclient.Client, error) {
	indexClientMu.Lock()
	defer indexClientMu.Unlock()
	if indexClient != nil {
		return indexClient, nil
	}
	eng := cloud.EmbeddedTasks()
	if eng == nil {
		return nil, errEngineNotReady
	}
	cli, err := tasksclient.Dial(tasksclient.Options{
		HostPort:  fmt.Sprintf("127.0.0.1:%d", eng.ZAPPort()),
		Namespace: "default",
	})
	if err != nil {
		return nil, fmt.Errorf("code-index dial engine: %w", err)
	}
	w := tasksworker.New(cli, indexQueue, tasksworker.Options{})
	w.RegisterWorkflow(IndexRepoWorkflow)
	w.RegisterActivity(IndexRepoActivity)
	if err := w.Start(); err != nil {
		cli.Close()
		return nil, fmt.Errorf("code-index worker start: %w", err)
	}
	indexClient = cli
	return cli, nil
}

// IndexRepoWorkflow indexes one repo's pushed tip as a single durable activity, with
// retry/backoff. Exported for worker registration; not called directly.
func IndexRepoWorkflow(ctx workflow.Context, in indexInput) error {
	actCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		// A large repo's tree read + embed can take a while; cap generously.
		StartToCloseTimeout: 15 * time.Minute,
		RetryPolicy: &temporal.RetryPolicy{
			InitialInterval:    5 * time.Second,
			BackoffCoefficient: 2.0,
			MaximumInterval:    2 * time.Minute,
			MaximumAttempts:    4,
		},
	})
	return workflow.ExecuteActivity(actCtx, IndexRepoActivity, in).Get(actCtx, nil)
}

// IndexRepoActivity is the durable index step: read the repo tip's tree from the object
// plane and fold its text files into the org's code index. Idempotent (full-tree
// reconcile with prune), so a retry or redelivery re-converges. Exported for worker
// registration; not called directly.
func IndexRepoActivity(ctx context.Context, in indexInput) error {
	return readAndIndex(ctx, mounted, in)
}

// ---- tree read + index (shared DRY core: activity AND inline fallback) ----

// readAndIndex reads the repo's tip tree and hands its text files to the injected
// indexer. The ONE place the tree read + index call live, used by both the durable
// activity and the fail-soft inline path.
func readAndIndex(ctx context.Context, s *cloud.Service[state], in indexInput) error {
	fn := indexer
	if fn == nil || s == nil {
		return nil
	}
	repo, err := openRepoForIndex(s, in.Org, in.Project, in.Repo)
	if err != nil {
		return err
	}
	files, err := treeTextFiles(repo, in.Commit)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	// billingOrg = org: the repo owner's org pays for indexing its own code.
	return fn(ctx, in.Org, in.Org, in.Project, in.Repo, files)
}

// openRepoForIndex opens the repo's go-git handle over its S3-backed storer — the same
// read path the browse UI uses (refState / openGit).
func openRepoForIndex(s *cloud.Service[state], org, project, repo string) (*gogit.Repository, error) {
	st, err := s.State.storage.storer(org, project, repo)
	if err != nil {
		return nil, err
	}
	return gogit.Open(st, nil)
}

// isDefaultBranch reports whether branch is the repo's default (its symbolic HEAD
// target). initBare points HEAD at the default branch, so this holds for a bare repo
// with no checkout.
func isDefaultBranch(repo *gogit.Repository, branch string) bool {
	ref, err := repo.Reference(plumbing.HEAD, false) // false: keep the symbolic ref
	if err != nil {
		return false
	}
	return ref.Target().Short() == branch
}

// treeTextFiles resolves the commit to index (the pushed tip, else HEAD) and returns
// its text files, skipping binaries and oversize blobs, bounded by the read caps.
func treeTextFiles(repo *gogit.Repository, after string) ([]IndexedFile, error) {
	commit, err := resolveIndexCommit(repo, after)
	if err != nil {
		return nil, err
	}
	tree, err := commit.Tree()
	if err != nil {
		return nil, err
	}
	iter := tree.Files()
	defer iter.Close()

	var out []IndexedFile
	var total int
	err = iter.ForEach(func(f *object.File) error {
		if len(out) >= maxIndexTreeFiles || total >= maxIndexTotalBytes {
			return storer.ErrStop
		}
		if f.Size > maxIndexFileBytes {
			return nil
		}
		if bin, _ := f.IsBinary(); bin {
			return nil
		}
		txt, err := f.Contents()
		if err != nil {
			return nil // skip an unreadable blob, keep walking
		}
		total += len(txt)
		out = append(out, IndexedFile{Path: f.Name, Content: txt})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// resolveIndexCommit returns the pushed tip (ev.After) when it resolves, else the
// repo's HEAD commit.
func resolveIndexCommit(repo *gogit.Repository, after string) (*object.Commit, error) {
	if after != "" {
		if c, err := repo.CommitObject(plumbing.NewHash(after)); err == nil {
			return c, nil
		}
	}
	h, err := repo.Head()
	if err != nil {
		return nil, err
	}
	return repo.CommitObject(h.Hash())
}
