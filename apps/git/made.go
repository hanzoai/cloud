package git

// made.go — a repository in the org's own code workspace on the forge.
//
// An org the estate's table does not name keeps its code on the forge, in a
// namespace the platform made for it and recorded under the org (forge
// workspace.go). This app's store holds none of it, so the browse reads answer
// it from the forge as the MACHINE — and only there: the namespace is the one
// the org's own record names, read for the caller's validated org, and nothing a
// request sends picks another. A repository this app holds answers first, at a
// ref it holds: the copy a project's visibility makes here for every project is
// empty (community.go), and the branch a run pushes goes to the forge, so a held
// copy that cannot resolve the ref a read asks for yields to the workspace
// ([yields]).
//
// An estate org's repositories are rows here, linked to their history on GitHub
// (link.go) and read through the cache as GitHub lets the caller read them.

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hanzoai/cloud/client"
	projectpeer "github.com/hanzoai/cloud/client/project"
	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// madeCall bounds the read of the org's workspace record, and madeMake the
// making of one, which forge.Provision bounds at 90 seconds.
const (
	madeCall = 10 * time.Second
	madeMake = 100 * time.Second
)

// madeSource reads the forge for this app; a test stands in for it.
var madeSource = func(ctx context.Context) (*forge.Client, error) { return forge.Dial(ctx, cloud.KMSPeer{}) }

// madeWorkspace answers the caller's org's made workspace, making it first when
// create is set and the org has none; a test stands in for it.
var madeWorkspace = func(org string, create bool) (forge.Workspace, error) {
	budget := madeCall
	if create {
		budget = madeMake
	}
	ctx, cancel := context.WithTimeout(plane.For(context.Background(), org), budget)
	defer cancel()
	out, err := projectpeer.ProjectsWorkspace(ctx, &client.WorkspaceIn{Make: create})
	if err != nil || out == nil {
		return forge.Workspace{}, err
	}
	return forge.Answered(out.Owner, out.Made)
}

// made is the forge, the namespace and the repository a name not held here
// addresses, and the client to read it with; ok is false for every other caller
// and name.
//
// A name with an owner, hanzo-inc/cloud, is the forge's repository at that
// coordinate, read as the caller in a namespace the caller's org owns
// ([forgeFor]). A bare name is one in the workspace the platform made for an org
// the estate's table does not name, read as the machine.
func made(s *cloud.Service[state], ctx context.Context, t tenant, name string) (m *forge.Client, owner, repo string, ok bool, err error) {
	if owner, repo, at := place(name); at {
		m, err := forgeFor(ctx, t, owner)
		if err != nil {
			return nil, "", "", false, err
		}
		return m, owner, repo, true, nil
	}
	if _, found := findRepo(s, ctx, t.org, name); found {
		return nil, "", "", false, nil
	}
	return shelved(ctx, t, name)
}

// yields is the workspace's repository when the copy of a bare name held here
// cannot resolve the ref a read asked for (miss is that read's error, unmapped):
// the empty copy a project's visibility makes for every project (community.go),
// or one without the branch a run pushed to the forge. ok is false for any other
// miss, and wherever [shelved] reads nothing.
func yields(ctx context.Context, t tenant, name string, miss error) (m *forge.Client, owner, repo string, ok bool, err error) {
	if !errors.Is(miss, ErrNoRevision) {
		return nil, "", "", false, nil
	}
	if _, _, at := place(name); at {
		return nil, "", "", false, nil
	}
	return shelved(ctx, t, name)
}

// shelved is a bare name in the workspace the platform made for the caller's
// org, and the machine to read it as; ok is false for an estate org, an org with
// no made workspace, and a deployment that keeps no record. Whether this app
// holds the name is its caller's to ask first ([made], [yields]).
func shelved(ctx context.Context, t tenant, name string) (m *forge.Client, owner, repo string, ok bool, err error) {
	if _, err := forge.Owner(t.org); err == nil {
		return nil, "", "", false, nil
	}
	ws, err := madeWorkspace(t.org, false)
	switch {
	case errors.Is(err, plane.ErrNoPeer):
		// A deployment that does not run the project app keeps no workspace record.
		return nil, "", "", false, nil
	case err != nil:
		return nil, "", "", false, zip.Errorf(http.StatusBadGateway, "this organization's code workspace could not be read: %v", err)
	}
	if !ws.Made {
		return nil, "", "", false, nil
	}
	c, err := madeSource(ctx)
	if err != nil {
		return nil, "", "", false, zip.Errorf(http.StatusBadGateway, "the code forge is unreachable: %v", err)
	}
	return c.Machine(), ws.Owner, name, true, nil
}

// madeTree is one directory of owner/name on the forge, in the browse shape.
func madeTree(ctx context.Context, m *forge.Client, owner, name, ref, dir string) (*treeJSON, error) {
	entries, err := m.List(ctx, owner, name, ref, dir)
	if err != nil {
		return nil, madeRefusal(err, "no such directory")
	}
	out := make([]treeEntryJSON, 0, len(entries))
	for _, e := range entries {
		kind, mode := "blob", "100644"
		if e.Dir {
			kind, mode = "tree", "040000"
		}
		out = append(out, treeEntryJSON{Name: e.Name, Path: e.Path, Type: kind, Size: e.Size, Mode: mode})
	}
	return &treeJSON{Entries: out}, nil
}

// madeBlob is one file of owner/name on the forge, in the browse shape: text
// verbatim, anything else base64, past the view cap truncated with no content.
func madeBlob(ctx context.Context, m *forge.Client, owner, name, ref, file string) (*blobJSON, error) {
	f, err := m.Read(ctx, owner, name, ref, file)
	if err != nil {
		return nil, madeRefusal(err, "no such file")
	}
	out := blobJSON{Path: file, Size: f.Size, Encoding: "utf8", Truncated: f.Truncated}
	switch {
	case f.Truncated:
	case bytes.IndexByte(f.Data[:min(len(f.Data), 8000)], 0) >= 0 || !utf8.Valid(f.Data):
		out.Binary, out.Encoding, out.Content = true, "base64", base64.StdEncoding.EncodeToString(f.Data)
	default:
		out.Content = string(f.Data)
	}
	return &out, nil
}

// madeRefusal is a forge read's failure as the status the browse surface
// answers: absent and not-a-directory are one 404 — and on a read made as the
// caller, so is a repository they may not see, which the forge does not tell
// apart — the forge's own refusal of the caller is 403, a name that is not a
// path is the caller's to fix, and anything else is the forge failing.
func madeRefusal(err error, missing string) error {
	switch {
	case errors.Is(err, forge.ErrBadName):
		return zip.ErrBadRequest(strings.TrimPrefix(err.Error(), "forge: "))
	case errors.Is(err, forge.ErrNotFound), errors.Is(err, forge.ErrUnknownActor),
		errors.Is(err, forge.ErrNotDir), errors.Is(err, forge.ErrNotFile):
		return zip.ErrNotFound(missing)
	case errors.Is(err, forge.ErrRefused):
		return zip.ErrForbidden("the forge refused this read for you")
	}
	return zip.Errorf(http.StatusBadGateway, "the code forge could not be read: %v", err)
}
