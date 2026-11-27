package deploy

// pin.go — the image a service is declared to run.
//
// CD holds the universe commit it applied and the files it renders a service
// from (spec.source.helm.valueFiles), but not the image: that is a value in those
// files at that commit, and in the status.history commits before it. So the pin is
// a file read at a commit, as the machine, found through what CD says and nowhere
// else. Commits are immutable, so each file is read once (memo.go).

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/hanzoai/cloud/forge"
	"github.com/hanzoai/cloud/sdk"
)

// sources are the repositories a pin is read from, as owner/name. An Application
// names its repository in cluster state and the read is made as the machine, so
// without this list an Application could point the read at any repository the
// forge holds. The host in repoURL is ignored: github.com and git.hanzo.ai hold
// the same commits, and the forge is the one this plane dials.
var sources = []string{"hanzo-inc/universe"}

// track is where one service's pin is written down.
type track struct {
	repo  string   // owner/name
	files []string // repository paths, in the order Helm layers them
	rev   string   // the service's Revision
	past  []string // the commits CD deployed, newest first
}

// trackOf reads where an Application keeps its pin. A zero track is a service
// with no values files — CD renders it from plain manifests — and so no pin.
func trackOf(g GitOpsApp, cr *unstructured.Unstructured) track {
	u, err := url.Parse(g.RepoURL)
	if err != nil {
		return track{}
	}
	repo := strings.Trim(strings.TrimSuffix(u.Path, ".git"), "/")
	if !slices.Contains(sources, repo) {
		return track{}
	}
	files, _, _ := unstructured.NestedStringSlice(cr.Object, "spec", "source", "helm", "valueFiles")
	t := track{repo: repo, rev: g.Revision}
	for _, f := range files {
		t.files = append(t.files, path.Join(g.Path, f))
	}
	for _, h := range g.History {
		t.past = append(t.past, h.Revision)
	}
	return t
}

// layer is what one values file says of the image.
type layer struct {
	Repository string `yaml:"repository"`
	Tag        string `yaml:"tag"`
	Digest     string `yaml:"digest"`
	Absent     bool   `yaml:"-"` // the file is not at that commit
}

// fileLayer reads one values file at a commit. A variable so a test can answer
// without a forge.
var fileLayer = func(ctx context.Context, repo, rev, name string) (layer, error) {
	owner, short, _ := strings.Cut(repo, "/")
	c, err := forge.Dial(ctx, cloud.KMSPeer{})
	if err != nil {
		return layer{}, err
	}
	f, err := c.Machine().Read(ctx, owner, short, rev, name)
	switch {
	case errors.Is(err, forge.ErrNotFound):
		return layer{Absent: true}, nil
	case err != nil:
		return layer{}, err
	case f.Truncated:
		return layer{}, fmt.Errorf("%s at %s is past the read limit", name, rev)
	}
	var v struct {
		Image layer `yaml:"image"`
	}
	if err := yaml.Unmarshal(f.Data, &v); err != nil {
		return layer{}, fmt.Errorf("%s at %s: %w", name, rev, err)
	}
	return v.Image, nil
}

// pins is every values file read, by repository, commit and path.
var pins = newMemo[layer]()

// declare is the image the files pin at rev, layered as Helm layers values: a
// later file's keys win. Nil with true is a service that pins none. False is a
// pin that could not be read — including a missing first file, which is the one
// CD cannot render without and so means the coordinate is wrong, where a missing
// overlay is the ignoreMissingValueFiles the fleet declares.
func declare(ctx context.Context, repo, rev string, files []string) (*Image, bool) {
	var m layer
	for i, f := range files {
		l, ok := pins.get(ctx, repo+"@"+rev+":"+f, func(ctx context.Context) (layer, error) {
			return fileLayer(ctx, repo, rev, f)
		})
		if !ok || (l.Absent && i == 0) {
			return nil, false
		}
		m.Repository, m.Tag, m.Digest = over(l.Repository, m.Repository), over(l.Tag, m.Tag), over(l.Digest, m.Digest)
	}
	if m.Repository == "" {
		return nil, true
	}
	ref := m.Repository
	if m.Tag != "" {
		ref += ":" + m.Tag
	}
	if m.Digest != "" {
		ref += "@" + m.Digest
	}
	img := imageOf(ref)
	return &img, true
}

func over(next, prev string) string {
	if next != "" {
		return next
	}
	return prev
}

// pin reads what the service's files declare at its Revision and the image CD
// deployed before the one it deployed last. It reports false
// when the declaration could not be read; an unread past only leaves Previous out.
func pin(ctx context.Context, r *Running, t track) bool {
	if len(t.files) == 0 || t.rev == "" {
		return true
	}
	img, ok := declare(ctx, t.repo, t.rev, t.files)
	if !ok {
		return false
	}
	r.Declared, r.Previous = img, past(ctx, t)
	return true
}

// past is the newest earlier deploy whose pin differs from the one CD deployed
// last: the image a rollback returns to. Deploys that changed something else are
// passed over. Nil when the history holds none, or a pin in it could not be read.
func past(ctx context.Context, t track) *Image {
	var last *Image
	for i, rev := range t.past {
		img, ok := declare(ctx, t.repo, rev, t.files)
		if !ok || img == nil {
			return nil
		}
		if i == 0 {
			last = img
		} else if img.Tag != last.Tag || img.Digest != last.Digest {
			return img
		}
	}
	return nil
}

// same reports whether a running image is the declared one: the same image name,
// tag and, when the pin has one, digest. The registry host and organisation are
// left out because the nodes serve oci.hanzo.ai from ghcr.io, so one pin can run
// under either; the digest says the bytes are the same either way.
func same(got, want Image) bool {
	return path.Base(got.Repository) == path.Base(want.Repository) && got.Tag == want.Tag &&
		(want.Digest == "" || got.Digest == want.Digest)
}

// drift names what disagrees about a service: image, the pods do not run the pin;
// sync and health, CD's verdicts are not Synced and Healthy; unread, the pin could
// not be read, so agreement is unproven. Empty is converged.
func drift(r Running, read bool) []string {
	d := []string{}
	if !read {
		d = append(d, "unread")
	}
	if r.Declared != nil && !slices.ContainsFunc(r.Images, func(im Image) bool { return same(im, *r.Declared) }) {
		d = append(d, "image")
	}
	if r.Sync != "Synced" {
		d = append(d, "sync")
	}
	if r.Health != "Healthy" {
		d = append(d, "health")
	}
	return d
}
