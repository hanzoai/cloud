package git

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/hanzoai/cloud/sdk"
)

// audit.go — which bare directories on the volume the store no longer needs.
//
// Before the objects store a repository was a directory under
// <data>/git/<org>/<project|_>/<name>.git, and a linked repository's cache was one too.
// Both now live in the store. This sweep walks the directories and says, for each,
// whether it can be deleted: it never deletes anything. The report is a JSON document in
// the objects bucket (auditKey) and a log line, so whoever decides to delete reads a
// list and not a guess.
//
//   - a directory of a linked repository is a cache the host refills: safe.
//   - a directory of a repository whose history is here is safe once the store holds it:
//     every ref of the directory is a ref of the store at the same commit, or at a commit
//     the store has an object for (a push since), and HEAD names the same branch.
//   - a directory with no metadata row, or one the store does not hold, is not safe: a
//     person looks at it.

// auditKey is where the report is kept in the objects bucket.
const auditKey = "audit/directories.json"

// dirAudit is one directory's verdict.
type dirAudit struct {
	Org     string `json:"org"`
	Project string `json:"project,omitempty"`
	Name    string `json:"name"`
	Path    string `json:"path"`
	Bytes   int64  `json:"bytes"`
	Kind    string `json:"kind"` // cache, adopted, unadopted, orphan, differs
	Safe    bool   `json:"safe"`
	Why     string `json:"why"`
}

// auditReport is the whole sweep.
type auditReport struct {
	At        time.Time  `json:"at"`
	Dirs      int        `json:"dirs"`
	Safe      int        `json:"safe"`
	SafeBytes int64      `json:"safeBytes"`
	Unsafe    []dirAudit `json:"unsafe"`
	Deletable []dirAudit `json:"deletable"`
}

// audit walks the volume and writes the report. It reads; it deletes nothing.
func (v *vault) audit(ctx context.Context, s *cloud.Service[state]) (*auditReport, error) {
	root := s.State.storage.rootDir
	rep := &auditReport{At: time.Now().UTC()}
	orgs, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	for _, o := range orgs {
		if !o.IsDir() || !orgRE.MatchString(o.Name()) {
			continue
		}
		projects, _ := os.ReadDir(filepath.Join(root, o.Name()))
		for _, p := range projects {
			if !p.IsDir() {
				continue
			}
			repos, _ := os.ReadDir(filepath.Join(root, o.Name(), p.Name()))
			for _, d := range repos {
				name, ok := strings.CutSuffix(d.Name(), ".git")
				if !d.IsDir() || !ok {
					continue
				}
				if ctx.Err() != nil {
					return rep, ctx.Err()
				}
				project := p.Name()
				if project == "_" {
					project = ""
				}
				a := v.auditOne(ctx, s, o.Name(), project, name)
				rep.Dirs++
				if a.Safe {
					rep.Safe++
					rep.SafeBytes += a.Bytes
					rep.Deletable = append(rep.Deletable, a)
				} else {
					rep.Unsafe = append(rep.Unsafe, a)
				}
			}
		}
	}
	sort.Slice(rep.Deletable, func(i, j int) bool { return rep.Deletable[i].Bytes > rep.Deletable[j].Bytes })
	return rep, nil
}

func (v *vault) auditOne(ctx context.Context, s *cloud.Service[state], org, project, name string) dirAudit {
	a := dirAudit{Org: org, Project: project, Name: name, Path: s.State.storage.absRepoPath(org, project, name)}
	a.Bytes, _ = s.State.storage.sizeBytes(org, project, name)
	store, err := storeFor(s, org)
	if err != nil {
		a.Kind, a.Why = "orphan", "the org's metadata cannot be opened: "+err.Error()
		return a
	}
	r, err := store.Get(ctx, org, project, name)
	if err != nil {
		a.Kind, a.Why = "orphan", "no metadata row names this directory"
		return a
	}
	if r.Origin != "" {
		a.Kind, a.Safe, a.Why = "cache", true, "a cache of "+r.Origin+", which the store fills its own copy of from the host"
		return a
	}
	objs, err := objectsFor(s, org)
	if err != nil {
		a.Kind, a.Why = "unadopted", err.Error()
		return a
	}
	if ok, err := objs.Has(ctx, r.ID); err != nil || !ok {
		a.Kind, a.Why = "unadopted", "the store holds no rows for this repository"
		return a
	}
	src, err := s.State.storage.storer(org, project, name)
	if err != nil {
		a.Kind, a.Why = "differs", "the directory cannot be read: "+err.Error()
		return a
	}
	repo := objs.Repo(ctx, r.ID)
	refs, err := src.IterReferences()
	if err != nil {
		a.Kind, a.Why = "differs", "the directory's refs cannot be read: "+err.Error()
		return a
	}
	var bad []string
	_ = refs.ForEach(func(ref *plumbing.Reference) error {
		if ref.Type() == plumbing.SymbolicReference {
			got, err := repo.Reference(ref.Name())
			if err != nil || got.Target() != ref.Target() {
				bad = append(bad, ref.Name().String()+" names "+ref.Target().String()+" and the store's does not")
			}
			return nil
		}
		got, err := repo.Reference(ref.Name())
		if err == nil && got.Hash() == ref.Hash() {
			return nil
		}
		// A push since the copy moved the ref on: the store must still hold the commit.
		if err := repo.HasEncodedObject(ref.Hash()); err != nil {
			bad = append(bad, ref.Name().String()+" at "+ref.Hash().String()+" is not in the store")
		}
		return nil
	})
	if len(bad) > 0 {
		sort.Strings(bad)
		a.Kind, a.Why = "differs", strings.Join(bad, "; ")
		return a
	}
	a.Kind, a.Safe, a.Why = "adopted", true, "every ref of the directory is in the store"
	return a
}

// auditOnce runs the audit once for this process and keeps the report in the bucket.
func (v *vault) auditOnce(s *cloud.Service[state]) {
	v.audited.Do(func() {
		ctx := s.State.life
		rep, err := v.audit(ctx, s)
		if err != nil {
			s.Log.Warn("git.vault audit failed", "err", err)
			return
		}
		s.Log.Info("git.vault audit", "directories", rep.Dirs, "safe", rep.Safe, "safeBytes", rep.SafeBytes, "notSafe", len(rep.Unsafe))
		if b := packBlobs(); b != nil {
			body, _ := json.MarshalIndent(rep, "", " ")
			if err := b.Put(ctx, auditKey, bytes.NewReader(body), int64(len(body))); err != nil {
				s.Log.Warn("git.vault audit: report not kept", "err", err)
			}
		}
	})
}
