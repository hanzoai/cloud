package git

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/contract"
	"github.com/hanzoai/namespace"
	"gopkg.in/yaml.v3"
)

// journal.go makes CI work durable BEFORE a push is answered.
//
// THE ORDER IS THE DESIGN. A push updates refs, writes ONE journal row in the
// org's own database, and only then answers the client. Everything after that —
// reading the workflows at the commit, resolving a pool, opening the run — is
// delivery, and delivery happens off the push. So a CI subsystem that is slow,
// wedged or absent cannot fail a push, and a process that dies between the ref
// write and the run still owes the run: the row is on disk and the next drain
// finds it.
//
// The row's id is the FACT, not the moment: a digest of org, project, repo, ref
// and commit. A redelivered push writes the id it already wrote and the insert
// is ignored; a redelivered entry opens the run it already opened, because Open
// is unique on (repo, commit, workflow). Two independent idempotencies, so a
// retry can never make a second run at either layer.
//
// Delivery is retried until it is acknowledged. An entry that names labels no
// declared pool carries stays pending with the reason on the row — visible, and
// delivered the moment somebody declares the pool. That is the acceptance rule
// stated as code: capacity is declared state, and work waits for a declaration
// rather than falling back to whatever daemon happens to be up.

const journalDDL = `
CREATE TABLE IF NOT EXISTS journal (
  id         TEXT PRIMARY KEY,
  org        TEXT NOT NULL,
  project    TEXT NOT NULL DEFAULT '',
  repo       TEXT NOT NULL,
  ref        TEXT NOT NULL,
  commit_sha TEXT NOT NULL,
  pusher     TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  taken_at   INTEGER NOT NULL DEFAULT 0,
  attempts   INTEGER NOT NULL DEFAULT 0,
  fault      TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ix_journal_open ON journal(taken_at, created_at);
`

// Note is one push that owes a run.
type Note struct {
	ID        string
	Org       string
	Project   string
	Repo      string
	Ref       string
	Commit    string
	Pusher    string
	CreatedAt int64
	TakenAt   int64
	Attempts  int64
	Fault     string
}

// fact is a note's identity: what happened, never when it was noticed. Two
// reports of one push therefore carry one id and the second insert is ignored.
func fact(org, project, repo, ref, commit string) string {
	h := sha256.Sum256([]byte(strings.Join([]string{org, project, repo, ref, commit}, "\x00")))
	return "push_" + hex.EncodeToString(h[:16])
}

// Owe records that a push owes a run. It is the write that must land before the
// push is answered, so it is one INSERT and nothing else.
func (s *Store) Owe(ctx context.Context, e Note) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO journal (id,org,project,repo,ref,commit_sha,pusher,created_at)
		 VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(id) DO NOTHING`,
		e.ID, e.Org, e.Project, e.Repo, e.Ref, e.Commit, e.Pusher, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("note push: %w", err)
	}
	return nil
}

// Owed lists the notes still to deliver, oldest first.
func (s *Store) Owed(ctx context.Context, limit int) ([]Note, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id,org,project,repo,ref,commit_sha,pusher,created_at,taken_at,attempts,fault
		 FROM journal WHERE taken_at=0 ORDER BY created_at LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list owed: %w", err)
	}
	defer rows.Close()
	var out []Note
	for rows.Next() {
		var e Note
		if err := rows.Scan(&e.ID, &e.Org, &e.Project, &e.Repo, &e.Ref, &e.Commit,
			&e.Pusher, &e.CreatedAt, &e.TakenAt, &e.Attempts, &e.Fault); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Took marks a note delivered.
func (s *Store) Took(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE journal SET taken_at=?, fault='' WHERE id=?`, time.Now().Unix(), id)
	return err
}

// Faulted records why a note is still owed, and that another attempt was made.
func (s *Store) Faulted(ctx context.Context, id, why string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE journal SET attempts=attempts+1, fault=? WHERE id=?`, why, id)
	return err
}

// note writes the journal row for one advanced branch. It runs INSIDE the push,
// before the client is answered, and it is the only part of CI that does.
func note(s *cloud.Service[state], ctx context.Context, org, project, repo, branch, commit, pusher string) {
	st, err := storeFor(s, org)
	if err != nil {
		s.Log.Error("git: journal store", "org", org, "repo", repo, "err", err)
		return
	}
	ref := "refs/heads/" + branch
	e := Note{ID: fact(org, project, repo, ref, commit), Org: org, Project: project,
		Repo: repo, Ref: ref, Commit: commit, Pusher: pusher}
	if err := st.Owe(ctx, e); err != nil {
		s.Log.Error("git: journal push", "org", org, "repo", repo, "ref", ref, "err", err)
	}
}

// deliver turns one owed entry into a run: read the workflows at the commit,
// resolve each job's pool from the capacity this org has DECLARED, and open the
// run and its tasks. Reports the reason it could not, which the caller records
// on the row so the entry stays owed and visible.
func deliver(s *cloud.Service[state], ctx context.Context, st *Store, e Note) error {
	repo, err := openRepository(s, Repo{Org: e.Org, Project: e.Project, Name: e.Repo})
	if err != nil {
		return fmt.Errorf("open repo: %w", err)
	}
	docs, err := workflows(ctx, repo, e.Commit)
	if err != nil {
		return err
	}
	if len(docs) == 0 {
		return nil // this repository declares no workflows: nothing is owed
	}
	pools, err := st.Pools(ctx, e.Org)
	if err != nil {
		return err
	}
	for _, d := range docs {
		jobs, err := assign(d.body, pools)
		if err != nil {
			return fmt.Errorf("%s: %w", d.name, err)
		}
		run := Run{Org: e.Org, Project: e.Project, Repo: e.Repo, Ref: e.Ref,
			Commit: e.Commit, Event: "push", Workflow: d.name, Doc: d.body, Actor: e.Pusher}
		out, fresh, err := st.Open(ctx, run, jobs)
		if err != nil {
			return err
		}
		if fresh {
			s.Log.Info("git: run opened", "org", e.Org, "repo", e.Repo, "workflow", d.name,
				"run", out.ID, "number", out.Number, "jobs", len(jobs), "commit", shortSHA(e.Commit))
		}
	}
	return nil
}

// document is one workflow file at a commit.
type document struct {
	name string
	body []byte
}

// workflows reads every workflow declared at a commit. `.hanzo/workflows/` is
// the native location; a repository that carries `.github/workflows/` is read
// too, because the document is the same document and a repository should not
// have to move its files to be built here.
func workflows(ctx context.Context, repo Repository, commit string) ([]document, error) {
	rev, _, err := repo.Resolve(ctx, commit)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", shortSHA(commit), err)
	}
	var out []document
	for _, dir := range []string{".hanzo/workflows", ".github/workflows"} {
		entries, err := repo.Tree(ctx, rev, dir)
		if err != nil {
			continue // a repository need not carry either directory
		}
		for _, en := range entries {
			name := strings.ToLower(en.Name)
			if en.Dir || (!strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml")) {
				continue
			}
			b, err := repo.Blob(ctx, rev, en.Path, contract.Max)
			if err != nil || b.Link || b.Binary || b.Truncated {
				continue
			}
			out = append(out, document{name: en.Path, body: b.Content})
		}
		if len(out) > 0 {
			return out, nil // native first: one location decides, never both
		}
	}
	return out, nil
}

// declared is the slice of a workflow document this plugin reads: which jobs it
// has, and which capacity each one asks for. Nothing else is read, because
// nothing else is this plugin's to decide — act, on the runner, is what executes
// a job, and it reads the same document from the run.
type declared struct {
	Jobs map[string]struct {
		RunsOn yaml.Node `yaml:"runs-on"`
	} `yaml:"jobs"`
}

// assign resolves each job in a document to a DECLARED pool. A job that asks for
// capacity nobody declared fails the whole document: half a workflow is not a
// workflow, and a run whose other half can never start is worse than a push that
// stays visibly owed.
func assign(doc []byte, pools []Pool) ([]Job, error) {
	var d declared
	if err := yaml.Unmarshal(doc, &d); err != nil {
		return nil, fmt.Errorf("read workflow: %w", err)
	}
	if len(d.Jobs) == 0 {
		return nil, errors.New("declares no jobs")
	}
	out := make([]Job, 0, len(d.Jobs))
	for name := range d.Jobs {
		want := labels(d.Jobs[name].RunsOn)
		p, ok := carries(pools, want)
		if !ok {
			return nil, fmt.Errorf("job %q asks for %s and no pool declares it",
				name, strings.Join(want, "+"))
		}
		out = append(out, Job{Name: name, Pool: p.Name})
	}
	// Deterministic, because a map is not: two deliveries of one push must
	// produce the same tasks in the same order.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Name < out[j-1].Name; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

// labels reads a `runs-on:` as the set it is, whether it was written as one
// scalar or a sequence.
func labels(n yaml.Node) []string {
	switch n.Kind {
	case yaml.ScalarNode:
		if n.Value == "" {
			return nil
		}
		return []string{n.Value}
	case yaml.SequenceNode:
		out := make([]string, 0, len(n.Content))
		for _, c := range n.Content {
			if c.Kind == yaml.ScalarNode && c.Value != "" {
				out = append(out, c.Value)
			}
		}
		return out
	}
	return nil
}

// carries finds a declared pool holding every label a job asked for. A job that
// asks for nothing matches nothing: capacity is chosen, never defaulted.
func carries(pools []Pool, want []string) (Pool, bool) {
	if len(want) == 0 {
		return Pool{}, false
	}
	for _, p := range pools {
		if holds(p.Labels, want) {
			return p, true
		}
	}
	return Pool{}, false
}

func holds(have, want []string) bool {
	for _, w := range want {
		found := false
		for _, h := range have {
			if h == w {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// drainLimit bounds one pass so a long backlog is worked through in batches
// rather than held in memory at once.
const drainLimit = 128

// drain delivers what one org owes. Every entry is independent: one that cannot
// be delivered records its reason and the pass moves on.
func drain(s *cloud.Service[state], ctx context.Context, org string) {
	st, err := storeFor(s, org)
	if err != nil {
		s.Log.Warn("git: drain store", "org", org, "err", err)
		return
	}
	owed, err := st.Owed(ctx, drainLimit)
	if err != nil {
		s.Log.Warn("git: drain read", "org", org, "err", err)
		return
	}
	for _, e := range owed {
		if err := deliver(s, ctx, st, e); err != nil {
			if ferr := st.Faulted(ctx, e.ID, err.Error()); ferr != nil {
				s.Log.Warn("git: drain record fault", "org", org, "entry", e.ID, "err", ferr)
			}
			s.Log.Warn("git: run not opened", "org", org, "repo", e.Repo,
				"commit", shortSHA(e.Commit), "err", err)
			continue
		}
		if err := st.Took(ctx, e.ID); err != nil {
			s.Log.Warn("git: drain acknowledge", "org", org, "entry", e.ID, "err", err)
		}
	}
}

// sweepEvery is how often every org's journal is re-read. A push kicks its own
// org immediately, so this is what catches an entry whose delivery failed and
// what picks up the backlog after a restart.
const sweepEvery = 30 * time.Second

// dispatcher is the process's one journal drainer.
var dispatcher struct {
	once sync.Once
	stop chan struct{}
}

// dispatch starts the drainer. It sweeps every org on the disk, because after a
// restart the process knows of no push and the rows are the only record that
// anything is owed.
func dispatch(s *cloud.Service[state]) {
	dispatcher.once.Do(func() {
		dispatcher.stop = make(chan struct{})
		go func() {
			t := time.NewTicker(sweepEvery)
			defer t.Stop()
			for {
				sweep(s)
				select {
				case <-dispatcher.stop:
					return
				case <-t.C:
				}
			}
		}()
	})
}

// sweep drains every org that has a git store on this host.
func sweep(s *cloud.Service[state]) {
	ctx, cancel := context.WithTimeout(context.Background(), sweepEvery)
	defer cancel()
	err := s.State.stores.Each(func(ns namespace.Namespace, _ *Store, err error) {
		if err != nil || ns.ID() == "" {
			return
		}
		drain(s, ctx, ns.ID())
	})
	if err != nil && !errors.Is(err, sql.ErrConnDone) {
		s.Log.Debug("git: journal sweep", "err", err)
	}
}

// inspect reports what a document declares and where each of its jobs would run,
// reading it the same way assign does so the report and the assignment can never
// disagree. A job whose labels no declared pool carries reports an empty pool,
// which is the visible form of "nobody declared this capacity".
func inspect(d document, pools []Pool) workflowView {
	v := workflowView{Name: d.name}
	var decl declared
	if err := yaml.Unmarshal(d.body, &decl); err != nil {
		v.Fault = err.Error()
		return v
	}
	names := make([]string, 0, len(decl.Jobs))
	for name := range decl.Jobs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		want := labels(decl.Jobs[name].RunsOn)
		j := jobView{Name: name, RunsOn: want}
		if p, ok := carries(pools, want); ok {
			j.Pool = p.Name
		}
		v.Jobs = append(v.Jobs, j)
	}
	if len(v.Jobs) == 0 {
		v.Fault = "declares no jobs"
	}
	return v
}
