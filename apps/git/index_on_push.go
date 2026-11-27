package git

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	codepeer "github.com/hanzoai/cloud/client/code"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	"github.com/hanzoai/account"
	"github.com/hanzoai/tasks/pkg/sdk/temporal"
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
// Orthogonality: git owns the reactor and the tree read (it holds the repo); code
// owns what is done with the tree, over the plane. The same tree is handed to two
// ops: code_graph restates what it says about its code into the org's graph,
// keyed to the commit, and code_index folds it into the search index. Each is a
// reconcile, so a retry of either re-converges, and a failure of one does not
// stop the other.
//
// Default branch only: the code index is per-repo, not per-branch, so a feature-branch
// push is skipped — indexing it would clobber the canonical index. The reactor gates on
// the repo's symbolic HEAD and only the pushed tip commit is indexed.

// indexQ is this reactor's durable queue on the ONE engine (see reactor.go).
var indexQ = &queue{
	name: "code-index",
	wf:   IndexRepoWorkflow,
	acts: []any{IndexRepoActivity},
}

// IndexedFile is one text file handed across the client to the code index: its
// repo-relative path and content.
type IndexedFile struct {
	Path    string
	Content string
}

// indexInput is the durable workflow payload: the repo coordinates + the exact commit
// to index. Small by design — the worker reads the tree from the object plane at
// execution time rather than carrying file bytes through the durable store. Org is the
// isolation boundary (the index store is per-org); it also pays for its own indexing.
type indexInput struct {
	Org     string `json:"org"`
	Project string `json:"project"`
	Repo    string `json:"repo"`
	Commit  string `json:"commit"`
	// Payer is the account that pays for the embeddings, in the billing_account
	// claim's form (account.Account.String), so it survives the durable queue as
	// text and parses back as the account it was.
	Payer string `json:"payer"`
	// Pushed is when the default branch took the commit. The graph files what the
	// tree states as of it: a push, and not the commit's own clock, is what makes
	// a tree the branch's, so a reset to an older commit is filed as the change it
	// is, and a retry files at the push it retries.
	Pushed time.Time `json:"pushed"`
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
	if ev.Kind != cloud.LifecyclePushLanded || ev.Org == "" || ev.Repo == "" || ev.Branch == "" {
		return
	}
	repo, done, err := reach(s, cloud.For(context.Background(), ev.Org), ev.Org, ev.Project, ev.Repo)
	if err != nil {
		s.Log.Warn("code-index: open failed", "org", ev.Org, "repo", ev.Repo, "err", err)
		return
	}
	defer done()
	if !isDefaultBranch(ctx, repo, ev.Branch) {
		return // a feature-branch push never rewrites the canonical index
	}
	in := indexInput{Org: ev.Org, Project: ev.Project, Repo: ev.Repo, Commit: ev.After, Payer: indexPayer(ev).String(), Pushed: time.Now().UTC()}
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

// indexPayer is who pays for indexing a push: the person who pushed, resolved at
// the push. A push no person made — a sync from a linked repository — is the
// repository's own org's work, and its org account pays on purpose.
func indexPayer(ev cloud.LifecycleEvent) account.Account {
	if !ev.Payer.Zero() {
		return ev.Payer
	}
	return meter.PayerFor(ev.Org, "")
}

// ---- durable enqueue (producer) + worker ----

// enqueueIndex starts a durable IndexRepoWorkflow for one push. The workflow id is
// keyed by commit so the same push never double-indexes (ExecuteWorkflow is idempotent
// on the id) and a redelivery is a no-op.
func enqueueIndex(ctx context.Context, in indexInput) error {
	return indexQ.start(ctx, fmt.Sprintf("code.index:%s:%s:%s", in.Org, in.Repo, in.Commit), in)
}

// IndexRepoWorkflow indexes one repo's pushed tip, and files what it states into the
// graph, as a single durable activity with retry/backoff. Exported for worker
// registration; not called directly.
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
	return readAndIndex(ctx, mounted.Load(), in)
}

// ---- tree read + index (shared DRY core: activity AND inline fallback) ----

// readAndIndex reads the repo's tip tree once and hands its text files to the code
// plane twice: to the graph, as what the commit states, and to the index. The ONE
// place the tree read and both calls live, used by both the durable activity and
// the fail-soft inline path.
func readAndIndex(ctx context.Context, s *cloud.Service[state], in indexInput) error {
	if s == nil {
		return nil
	}
	repo, done, err := reach(s, cloud.For(ctx, in.Org), in.Org, in.Project, in.Repo)
	if err != nil {
		return err
	}
	defer done()
	rev, _, err := repo.Resolve(ctx, in.Commit)
	if err != nil {
		return err
	}
	files, whole, err := treeTextFiles(ctx, repo, rev)
	if err != nil {
		return err
	}
	out := make([]client.IndexFile, 0, len(files))
	for _, f := range files {
		out = append(out, client.IndexFile{Path: f.Path, Content: f.Content})
	}
	// The graph first: it is free, and a commit's structure does not wait on an
	// embedding it has no use for.
	gerr := fileGraph(s, in, rev, out, whole)
	if len(out) == 0 {
		return gerr
	}
	// Bounded, because this rides a push: a code plane that has stopped answering
	// must not hold a push reactor open. Indexing EMBEDS — paid inference on the
	// far side, charged to the account the push named (indexPayer).
	ictx, cancel := context.WithTimeout(ctx, indexCallTimeout)
	defer cancel()
	_, ierr := codepeer.CodeIndex(ictx, &client.IndexIn{
		Org: in.Org, Payer: in.Payer, Project: in.Project, Repo: in.Repo, Files: out,
	})
	return errors.Join(gerr, ierr)
}

// fileGraph restates what one commit's tree states into the org's graph.
//
// An empty tree is still a restatement: it ends what the last commit stated. A
// tree cut short by the read caps is not the whole of what the commit states,
// and restating it would end everything past the cut, so it is not filed.
//
// The call is bounded, as the index's is. The tenant is the repo's, stated on a
// context with no request behind it: zip reads a stated caller only there, and
// the inline path runs on the push's own. A refusal is logged and not returned:
// the same tree is refused the same way however often it is retried, and only
// an outage is worth a retry.
func fileGraph(s *cloud.Service[state], in indexInput, rev Revision, files []client.IndexFile, whole bool) error {
	switch {
	case !whole:
		s.Log.Warn("code-graph: tree over the read caps, not restated", "org", in.Org, "repo", in.Repo, "commit", rev.String())
		return nil
	case in.Pushed.IsZero():
		// Enqueued before a push carried its instant: nothing says when the tree
		// became the branch's, and the next push restates the repo.
		return nil
	}
	ctx, cancel := context.WithTimeout(zip.WithCaller(context.Background(), zip.Caller{Org: in.Org, Project: in.Project}), indexCallTimeout)
	defer cancel()
	out, err := codepeer.CodeGraph(ctx, &client.Tree{
		Repo: in.Repo, At: in.Pushed.UTC().Format(time.RFC3339), Source: commitURL(s, in.Repo, rev), Files: files,
	})
	var refused *zip.HTTPError
	if errors.As(err, &refused) && refused.Status < http.StatusInternalServerError {
		s.Log.Warn("code-graph: restatement refused", "org", in.Org, "repo", in.Repo, "commit", rev.String(), "err", err)
		return nil
	}
	if err != nil {
		return err
	}
	if out.Stale {
		s.Log.Info("code-graph: a later commit is already filed", "org", in.Org, "repo", in.Repo, "commit", rev.String())
	}
	if len(out.Refused) > 0 {
		s.Log.Warn("code-graph: statements refused", "org", in.Org, "repo", in.Repo, "commit", rev.String(),
			"refused", len(out.Refused), "first", out.Refused[0])
	}
	return nil
}

// commitURL is where this host answers a commit: the repo's commit log from it,
// the commit first (browseCommits), read by the org it belongs to. It names the
// source of everything the commit states.
func commitURL(s *cloud.Service[state], repo string, rev Revision) string {
	return origin(s) + "/v1/git/repos/" + url.PathEscape(repo) + "/commits?ref=" + rev.String()
}

// indexCallTimeout bounds one reconcile. Generous because a first index of a
// large repo embeds every file; short enough that a push reactor is never held
// open by a code plane that has stopped answering.
var indexCallTimeout = 2 * time.Minute

// isDefaultBranch reports whether branch is the repo's default (its symbolic HEAD
// target). initBare points HEAD at the default branch, so this holds for a bare repo
// with no checkout.
func isDefaultBranch(ctx context.Context, repo Repository, branch string) bool {
	def, err := repo.DefaultBranch(ctx)
	if err != nil {
		return false
	}
	return def == branch
}

// treeTextFiles returns a commit's text files, skipping binaries and oversize
// blobs, bounded by the read caps, and whether the caps left the tree whole. The
// per-file cap is handed to the model so an oversize blob is skipped without
// ever being read.
func treeTextFiles(ctx context.Context, repo Repository, rev Revision) (files []IndexedFile, whole bool, err error) {
	var out []IndexedFile
	var total int
	whole = true
	err = repo.WalkText(ctx, rev, maxIndexFileBytes, func(path, content string) error {
		if len(out) >= maxIndexTreeFiles || total >= maxIndexTotalBytes {
			whole = false
			return StopWalk
		}
		total += len(content)
		out = append(out, IndexedFile{Path: path, Content: content})
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, whole, nil
}
