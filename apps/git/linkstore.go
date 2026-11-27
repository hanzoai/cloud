package git

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
	gogitstorage "github.com/go-git/go-git/v5/storage"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/hanzoai/cloud/apps/git/objects"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// linkstore.go — a linked repository is filled from its host INTO the objects store, and
// a commit built here goes up to the host FROM the store. Neither touches a disk.
//
// go-git does both ends. Its Remote reads the storer it is given to negotiate (the
// commits the store already has are the haves, so a refresh downloads what is new and
// nothing more), writes what arrives through SetEncodedObject, and moves refs through
// SetReference; the store's Tx is that storer, so one fetch is one transaction: the
// objects arrive as packs of at most 64 MiB in the bucket, the refs move when the
// transaction commits, and a fetch that fails leaves the previous copy as it was. A push
// runs the other way: go-git walks from the new commit down to what the host has, reading
// the store, and sends the pack.
//
// Every request goes through [upstream], hanzoai/outbound's client: the address is judged
// where it is dialled, no redirect is followed, and the credential is sent over https
// only.

func init() {
	c := githttp.NewClient(&http.Client{
		Transport:     hostTransport{},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	})
	client.InstallProtocol("https", c)
	client.InstallProtocol("http", c)
}

// hostTransport sends go-git's requests through [upstream]. A variable client is read per
// request so a test that points upstream at its own server is obeyed. No overall timeout:
// a pack is minutes of body; the context bounds it.
type hostTransport struct{}

func (hostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return upstream(0).Do(req)
}

// hostAuth is the credential a request to clone goes under: basic, over https only, and
// nothing at all otherwise.
func hostAuth(clone string, cred gitCred) transport.AuthMethod {
	if u, err := url.Parse(clone); err == nil && u.Scheme == "https" && cred.Token != "" {
		return &githttp.BasicAuth{Username: cred.User, Password: cred.Token}
	}
	return nil
}

// hostRemote is a remote named origin for k, over storer.
func hostRemote(st gogitstorage.Storer, k *link) *gogit.Remote {
	return gogit.NewRemote(st, &config.RemoteConfig{Name: "origin", URLs: []string{k.CloneURL}})
}

// hostRefs filters a listing to what a copy holds: branches and tags, not peeled.
func hostRefs(refs []*plumbing.Reference) (heads map[plumbing.ReferenceName]plumbing.Hash, head plumbing.ReferenceName) {
	heads = map[plumbing.ReferenceName]plumbing.Hash{}
	var headHash plumbing.Hash
	for _, ref := range refs {
		switch {
		case ref.Name() == plumbing.HEAD && ref.Type() == plumbing.SymbolicReference:
			head = ref.Target()
		case ref.Name() == plumbing.HEAD:
			headHash = ref.Hash()
		case ref.Type() == plumbing.HashReference && (ref.Name().IsBranch() || ref.Name().IsTag()) &&
			!strings.HasSuffix(ref.Name().String(), "^{}"):
			heads[ref.Name()] = ref.Hash()
		}
	}
	if head == "" && !headHash.IsZero() {
		// A host that names no symref: the branch at HEAD's commit, the default first.
		for name, h := range heads {
			if h == headHash && name.IsBranch() && (head == "" || name.Short() == defaultBranchName) {
				head = name
			}
		}
	}
	return heads, head
}

// syncFromHost brings r's copy in the objects store up to date with its host: every
// branch and tag as the host has it (forced, pruned), HEAD at the host's default branch.
// The host is asked for its refs first, and when the copy already equals them that is the
// whole cost.
func syncFromHost(ctx context.Context, s *cloud.Service[state], r Repo, k *link, cred gitCred) error {
	u, err := url.Parse(k.CloneURL)
	if err != nil || u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("git: %s is not an http address", host(k.CloneURL))
	}
	if err := admit(ctx, u.Hostname()); err != nil {
		return fmt.Errorf("git: %s is not reachable: %w", host(k.CloneURL), err)
	}
	objs, err := objectsFor(s, r.Org)
	if err != nil {
		return err
	}
	if ok, err := objs.Has(ctx, r.ID); err != nil {
		return err
	} else if !ok {
		if err := objs.Create(ctx, r.ID, cmp.Or(r.DefaultBranch, defaultBranchName)); err != nil && !errors.Is(err, objects.ErrExists) {
			return err
		}
	}
	auth := hostAuth(k.CloneURL, cred)
	listed, err := hostRemote(memory.NewStorage(), k).ListContext(ctx, &gogit.ListOptions{Auth: auth})
	empty := errors.Is(err, transport.ErrEmptyRemoteRepository)
	if err != nil && !empty {
		return fmt.Errorf("list %s: %w", host(k.CloneURL), err)
	}
	want, head := hostRefs(listed)
	if head != "" && checkHeadRef(head.String()) != nil {
		head = "" // the host chooses this, so it is checked: HEAD is not under refs/
	}

	if current(objs.Repo(ctx, r.ID), want, head) {
		return nil
	}
	return withPackSlot(ctx, func() error {
		tx, err := objs.Begin(ctx, r.ID)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback() }()
		if len(want) > 0 {
			err := hostRemote(tx, k).FetchContext(ctx, &gogit.FetchOptions{
				RemoteName: "origin",
				RefSpecs:   []config.RefSpec(refSpecs()),
				Auth:       auth,
				// --no-tags: tags are fetched as themselves by the refspec, not followed by
				// short name onto a branch of the same one.
				Tags:  gogit.NoTags,
				Force: true,
				Prune: true,
			})
			if err != nil && !errors.Is(err, gogit.NoErrAlreadyUpToDate) {
				return fmt.Errorf("fetch %s: %w", host(k.CloneURL), err)
			}
		} else if err := dropRefs(tx.Repo); err != nil {
			return err
		}
		if head != "" {
			if err := tx.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, head)); err != nil {
				return err
			}
		}
		return tx.Commit()
	})
}

func refSpecs() []config.RefSpec {
	out := make([]config.RefSpec, len(fillRefSpecs))
	for i, p := range fillRefSpecs {
		out[i] = config.RefSpec(p)
	}
	return out
}

// current is whether the copy's branches, tags and HEAD are exactly want and head.
func current(repo *objects.Repo, want map[plumbing.ReferenceName]plumbing.Hash, head plumbing.ReferenceName) bool {
	refs, err := repo.References()
	if err != nil {
		return false
	}
	have := map[plumbing.ReferenceName]plumbing.Hash{}
	var at plumbing.ReferenceName
	for _, ref := range refs {
		switch {
		case ref.Name() == plumbing.HEAD && ref.Type() == plumbing.SymbolicReference:
			at = ref.Target()
		case ref.Type() == plumbing.HashReference && (ref.Name().IsBranch() || ref.Name().IsTag()):
			have[ref.Name()] = ref.Hash()
		}
	}
	if len(have) != len(want) || head != "" && at != head {
		return false
	}
	for n, h := range want {
		if have[n] != h {
			return false
		}
	}
	return true
}

// dropRefs removes every branch and tag: the host holds none.
func dropRefs(repo *objects.Repo) error {
	refs, err := repo.References()
	if err != nil {
		return err
	}
	for _, ref := range refs {
		if ref.Type() == plumbing.HashReference && (ref.Name().IsBranch() || ref.Name().IsTag()) {
			if err := repo.RemoveReference(ref.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

// send moves ref on k's host to commit, pushed from the store (st holds the commit and the
// history under it) under the caller's write credential and without force: a branch that
// moved on the host since the copy read it refuses the push, and nothing moves anywhere.
func send(ctx context.Context, org string, k *link, st gogitstorage.Storer, commit, ref string) error {
	if u, err := url.Parse(k.CloneURL); err != nil || u.Scheme != "https" {
		return zip.ErrForbidden(fmt.Sprintf("this repository's history is on %s: push there directly", host(k.CloneURL)))
	}
	cred, release, err := writer(ctx, org, k)
	if err != nil {
		return err
	}
	defer release()
	err = withPackSlot(ctx, func() error {
		return hostRemote(st, k).PushContext(ctx, &gogit.PushOptions{
			RemoteName: "origin",
			RefSpecs:   []config.RefSpec{config.RefSpec(commit + ":" + ref)},
			Auth:       hostAuth(k.CloneURL, cred),
		})
	})
	if err == nil || errors.Is(err, gogit.NoErrAlreadyUpToDate) {
		return nil
	}
	detail := sanitizeGitErr(err.Error())
	if errors.Is(err, gogit.ErrNonFastForwardUpdate) ||
		strings.Contains(detail, "rejected") || strings.Contains(detail, "non-fast-forward") || strings.Contains(detail, "fetch first") {
		return zip.ErrConflict(fmt.Sprintf("%s moved on %s since it was read; push again", ref, host(k.CloneURL)))
	}
	return zip.Errorf(http.StatusBadGateway, "%s refused the push: %s", host(k.CloneURL), detail)
}
