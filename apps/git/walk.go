package git

import (
	"container/heap"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/hanzoai/cloud/apps/git/objects"
)

// walk.go — which objects a fetch is owed.
//
// go-git's revlist reads the whole history of every commit the client says it has, so
// a fetch of one commit into a repository of a hundred thousand costs a hundred
// thousand reads. What a server owes is what git's rev-list --objects owes: walk the
// wanted commits' history newest first, stop where the client's history begins, and
// send what the commits in between add to the trees the client already has.
//
//  1. Commits. A wanted commit is interesting, a commit the client has is not, and
//     the walk takes commits newest first, handing each one's colour to its parents,
//     until no interesting commit is waiting (and a little longer: commit dates are a
//     person's clock). The commits it took and did not later find uninteresting are
//     the ones to send.
//  2. Trees. A commit's tree is compared with its parents' trees, and only what is not
//     the same at the same path is sent, recursively. A parent that is itself being sent
//     is a base (what it adds is sent on its own turn); a parent the client has is a base
//     (it has it); a parent that is neither, the far side of a shallow cut, is not.
//  3. Blobs ride on the trees; the filter may drop them.

// plan is what a fetch is sent.
type plan struct {
	oids []plumbing.Hash
	// shallow are commits the client must record as having no parents; unshallow are
	// shallow commits of its own that now have them.
	shallow, unshallow []plumbing.Hash
}

// filterBlob reports whether a blob of this size is dropped by the filter, which is
// "blob:none" or "blob:limit=<n>" or empty. Anything else is refused before a fetch is
// planned.
func filterBlob(filter string, size func() (int64, error)) (bool, error) {
	switch {
	case filter == "":
		return false, nil
	case filter == "blob:none":
		return true, nil
	}
	limit, _ := strings.CutPrefix(filter, "blob:limit=")
	n, err := parseSize(limit)
	if err != nil {
		return false, fmt.Errorf("unsupported filter %q", filter)
	}
	sz, err := size()
	return sz >= n, err
}

func checkFilter(filter string) error {
	if filter == "" || filter == "blob:none" {
		return nil
	}
	if limit, ok := strings.CutPrefix(filter, "blob:limit="); ok {
		if _, err := parseSize(limit); err == nil {
			return nil
		}
	}
	return fmt.Errorf("unsupported filter %q", filter)
}

// parseSize reads git's size syntax: a number with an optional k, m or g.
func parseSize(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1<<10, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult, s = 1<<20, strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "g"):
		mult, s = 1<<30, strings.TrimSuffix(s, "g")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not a size", s)
	}
	return n * mult, nil
}

// node is a commit in the walk.
type node struct {
	c       *object.Commit
	unint   bool // reachable from something the client has
	queued  bool
	sending bool
}

type dated []*node

func (q dated) Len() int           { return len(q) }
func (q dated) Less(i, j int) bool { return q[i].c.Committer.When.After(q[j].c.Committer.When) }
func (q dated) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *dated) Push(x any)        { *q = append(*q, x.(*node)) }
func (q *dated) Pop() any          { old := *q; n := old[len(old)-1]; *q = old[:len(old)-1]; return n }
func (q dated) interesting() bool {
	for _, n := range q {
		if !n.unint {
			return true
		}
	}
	return false
}

// slop is how many more commits the walk takes after the last interesting one has
// been taken, because a commit's date is whatever its author's clock said.
const slop = 5

type walker struct {
	repo  *objects.Repo
	nodes map[plumbing.Hash]*node
}

func (w *walker) node(h plumbing.Hash) (*node, error) {
	if n, ok := w.nodes[h]; ok {
		return n, nil
	}
	c, err := object.GetCommit(w.repo, h)
	if err != nil {
		return nil, fmt.Errorf("commit %s: %w", h, err)
	}
	n := &node{c: c}
	w.nodes[h] = n
	return n, nil
}

// cut marks n, and every commit already seen below it, as one the client has.
func (w *walker) cut(n *node) {
	for stack := []*node{n}; len(stack) > 0; {
		x := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if x.unint {
			continue
		}
		x.unint = true
		for _, p := range x.c.ParentHashes {
			if pn, ok := w.nodes[p]; ok {
				stack = append(stack, pn)
			}
		}
	}
}

// commits are the commits owed for wants, given haves, newest first.
func (w *walker) commits(wants, haves []plumbing.Hash) ([]*node, error) {
	q := &dated{}
	push := func(n *node) {
		if !n.queued {
			n.queued = true
			heap.Push(q, n)
		}
	}
	for _, h := range wants {
		n, err := w.node(h)
		if err != nil {
			return nil, err
		}
		push(n)
	}
	for _, h := range haves {
		n, err := w.node(h)
		if err != nil {
			return nil, err
		}
		w.cut(n)
		push(n)
	}
	var taken []*node
	rest := slop
	for q.Len() > 0 {
		if !q.interesting() {
			if rest == 0 {
				break
			}
			rest--
		}
		n := heap.Pop(q).(*node)
		for _, ph := range n.c.ParentHashes {
			p, err := w.node(ph)
			if err != nil {
				return nil, err
			}
			if n.unint {
				w.cut(p)
			}
			push(p)
		}
		if !n.unint {
			taken = append(taken, n)
		}
	}
	out := taken[:0]
	for _, n := range taken {
		if !n.unint {
			n.sending = true
			out = append(out, n)
		}
	}
	return out, nil
}

// shallowCommits are the commits within depth of wants (a want is depth one), newest
// first, the ones at the cut that still have parents (shallow), and the client's own
// shallow commits that the walk went past (unshallow).
func (w *walker) shallowCommits(wants, clientShallow []plumbing.Hash, depth int) (taken []*node, cut, past []plumbing.Hash, err error) {
	theirs := map[plumbing.Hash]bool{}
	for _, h := range clientShallow {
		theirs[h] = true
	}
	level := map[plumbing.Hash]int{}
	var frontier []*node
	for _, h := range wants {
		n, err := w.node(h)
		if err != nil {
			return nil, nil, nil, err
		}
		if _, seen := level[h]; !seen {
			level[h] = 1
			frontier = append(frontier, n)
		}
	}
	for d := 1; len(frontier) > 0; d++ {
		var next []*node
		for _, n := range frontier {
			n.sending = true
			taken = append(taken, n)
			if len(n.c.ParentHashes) == 0 {
				continue
			}
			if d == depth {
				cut = append(cut, n.c.Hash)
				continue
			}
			if theirs[n.c.Hash] {
				past = append(past, n.c.Hash)
			}
			for _, ph := range n.c.ParentHashes {
				if _, seen := level[ph]; seen {
					continue
				}
				p, err := w.node(ph)
				if err != nil {
					return nil, nil, nil, err
				}
				level[ph] = d + 1
				next = append(next, p)
			}
		}
		frontier = next
	}
	return taken, cut, past, nil
}

// emitter collects the objects to send, each once.
type emitter struct {
	repo   *objects.Repo
	seen   map[plumbing.Hash]struct{}
	known  map[plumbing.Hash]struct{} // objects the client has
	filter string
	list   []plumbing.Hash
}

func (e *emitter) add(h plumbing.Hash) bool {
	if _, ok := e.seen[h]; ok {
		return false
	}
	if _, ok := e.known[h]; ok {
		return false
	}
	e.seen[h] = struct{}{}
	e.list = append(e.list, h)
	return true
}

func (e *emitter) blob(h plumbing.Hash) error {
	if _, ok := e.seen[h]; ok {
		return nil
	}
	if _, ok := e.known[h]; ok {
		return nil
	}
	drop, err := filterBlob(e.filter, func() (int64, error) { return e.repo.EncodedObjectSize(h) })
	if err != nil || drop {
		return err
	}
	e.add(h)
	return nil
}

// tree sends t and what it holds that the bases do not hold at the same path.
func (e *emitter) tree(t plumbing.Hash, bases []plumbing.Hash) error {
	if !e.add(t) {
		return nil
	}
	tr, err := object.GetTree(e.repo, t)
	if err != nil {
		return fmt.Errorf("tree %s: %w", t, err)
	}
	var under []map[string]object.TreeEntry
	for _, b := range bases {
		bt, err := object.GetTree(e.repo, b)
		if err != nil {
			return fmt.Errorf("tree %s: %w", b, err)
		}
		m := make(map[string]object.TreeEntry, len(bt.Entries))
		for _, be := range bt.Entries {
			m[be.Name] = be
		}
		under = append(under, m)
	}
	// Every subtree is read next: one round trip names where they all are.
	var kids []plumbing.Hash
	for _, ent := range tr.Entries {
		if ent.Mode == filemode.Dir {
			kids = append(kids, ent.Hash)
		}
	}
	if err := e.repo.Prefetch(kids); err != nil {
		return err
	}
	for _, ent := range tr.Entries {
		if ent.Mode == filemode.Submodule {
			continue
		}
		same := false
		var sub []plumbing.Hash
		for _, m := range under {
			be, ok := m[ent.Name]
			if !ok {
				continue
			}
			if be.Hash == ent.Hash {
				same = true
				break
			}
			if be.Mode == filemode.Dir && ent.Mode == filemode.Dir {
				sub = append(sub, be.Hash)
			}
		}
		switch {
		case same:
		case ent.Mode == filemode.Dir:
			if err := e.tree(ent.Hash, sub); err != nil {
				return err
			}
		default:
			if err := e.blob(ent.Hash); err != nil {
				return err
			}
		}
	}
	return nil
}

// plan works out what to send for r, given the repository.
func (r *request) plan(repo *objects.Repo) (*plan, error) {
	wk := &walker{repo: repo, nodes: map[plumbing.Hash]*node{}}
	e := &emitter{repo: repo, seen: map[plumbing.Hash]struct{}{}, known: map[plumbing.Hash]struct{}{}, filter: r.filter}

	var wantCommits, haveCommits []plumbing.Hash
	var wantTrees []plumbing.Hash
	for _, h := range r.wants {
		for {
			o, err := repo.EncodedObject(plumbing.AnyObject, h)
			if err != nil {
				return nil, fmt.Errorf("want %s: %w", h, err)
			}
			switch o.Type() {
			case plumbing.CommitObject:
				wantCommits = append(wantCommits, h)
			case plumbing.TreeObject:
				wantTrees = append(wantTrees, h)
			case plumbing.BlobObject:
				// A blob asked for by name is sent whatever the filter: that is how a
				// partial clone fetches the one it reads.
				e.add(h)
			case plumbing.TagObject:
				e.add(h)
				t, err := object.DecodeTag(repo, o)
				if err != nil {
					return nil, err
				}
				h = t.Target
				continue
			}
			break
		}
	}
	for _, h := range r.haves {
		for {
			o, err := repo.EncodedObject(plumbing.AnyObject, h)
			if err != nil {
				break
			}
			switch o.Type() {
			case plumbing.CommitObject:
				haveCommits = append(haveCommits, h)
			case plumbing.TagObject:
				if t, err := object.DecodeTag(repo, o); err == nil {
					h = t.Target
					continue
				}
			default:
				e.known[h] = struct{}{}
			}
			break
		}
	}

	out := &plan{}
	var taken []*node
	var err error
	if r.depth > 0 {
		taken, out.shallow, out.unshallow, err = wk.shallowCommits(wantCommits, r.shallows, r.depth)
	} else {
		taken, err = wk.commits(wantCommits, haveCommits)
	}
	if err != nil {
		return nil, err
	}
	have := map[plumbing.Hash]struct{}{}
	for _, h := range haveCommits {
		have[h] = struct{}{}
	}
	for _, n := range taken {
		e.add(n.c.Hash)
	}
	for _, n := range taken {
		var bases []plumbing.Hash
		for _, ph := range n.c.ParentHashes {
			p := wk.nodes[ph]
			_, theirs := have[ph]
			if (p != nil && (p.sending || p.unint)) || theirs {
				pc, err := object.GetCommit(repo, ph)
				if err != nil {
					return nil, err
				}
				bases = append(bases, pc.TreeHash)
			}
		}
		if err := e.tree(n.c.TreeHash, bases); err != nil {
			return nil, err
		}
	}
	for _, t := range wantTrees {
		if err := e.tree(t, nil); err != nil {
			return nil, err
		}
	}
	if r.tags {
		if err := e.tagsFor(); err != nil {
			return nil, err
		}
	}
	out.oids = e.list
	return out, nil
}

// tagsFor adds the annotated tags that point at something being sent, so a fetch of a
// branch brings the tags on it (include-tag).
func (e *emitter) tagsFor() error {
	refs, err := e.repo.References()
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if ref.Type() != plumbing.HashReference || !ref.Name().IsTag() {
			continue
		}
		if _, ok := e.seen[ref.Hash()]; ok {
			continue
		}
		chain := []plumbing.Hash{}
		h := ref.Hash()
		for range 16 {
			o, err := e.repo.EncodedObject(plumbing.AnyObject, h)
			if err != nil || o.Type() != plumbing.TagObject {
				break
			}
			chain = append(chain, h)
			t, err := object.DecodeTag(e.repo, o)
			if err != nil {
				chain = nil
				break
			}
			h = t.Target
			if _, ok := e.seen[h]; ok {
				for _, c := range chain {
					e.add(c)
				}
				chain = nil
				break
			}
		}
	}
	return nil
}

// errNotOurs is a want the repository does not hold.
var errNotOurs = errors.New("not our ref")
