package deploy

// revision.go — the commit an image was built from.
//
// Every image the build lane publishes carries org.opencontainers.image.revision
// and .source in its config, and the registry is the only place those labels
// live: CD knows the image reference its pods run and nothing about how it was
// made. So they are read from the registry, by DIGEST, and kept (memo.go).

import (
	"context"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const (
	commitLabel = "org.opencontainers.image.revision"
	repoLabel   = "org.opencontainers.image.source"
)

// registries are the repositories this plane opens a connection to. The image
// references it reads come from cluster state, and a reference names a host, so
// without this list the pod would dial wherever an image says it lives.
var registries = []string{
	"oci.hanzo.ai",
	"ghcr.io/hanzoai",
	"ghcr.io/hanzo-inc",
	"ghcr.io/luxfi",
	"ghcr.io/zooai",
}

// label is what an image says of its own making. Either may be empty: an image
// carries only the labels its build passed.
type label struct{ Commit, Repo string }

// configLabels reads the labels off the image config the digest names. A variable
// so a test can answer without a registry.
var configLabels = func(ctx context.Context, ref string) (label, error) {
	d, err := name.NewDigest(ref)
	if err != nil {
		return label{}, err
	}
	img, err := remote.Image(d, remote.WithContext(ctx), remote.WithAuthFromKeychain(authn.DefaultKeychain))
	if err != nil {
		return label{}, err
	}
	cf, err := img.ConfigFile()
	if err != nil {
		return label{}, err
	}
	l := label{Commit: cf.Config.Labels[commitLabel], Repo: cf.Config.Labels[repoLabel]}
	if l.Commit == "unknown" { // the build lane's default when no commit was passed
		l.Commit = ""
	}
	return l, nil
}

// labels is every label read, by digest reference.
var labels = newMemo[label]()

// pinned is the reference to read labels through: repository@digest, for an image
// in one of our registries that is pinned by digest. A tag alone names no fixed
// bytes, so it has no labels to remember.
func pinned(img Image) (string, bool) {
	if img.Digest == "" {
		return "", false
	}
	for _, r := range registries {
		if img.Repository == r || strings.HasPrefix(img.Repository, r+"/") {
			return img.Repository + "@" + img.Digest, true
		}
	}
	return "", false
}

// imageOf splits an image reference into its repository, tag and digest. A
// colon after the last slash is a tag; one before it is a registry port.
func imageOf(ref string) Image {
	img := Image{Ref: ref}
	base, digest, _ := strings.Cut(ref, "@")
	img.Digest = digest
	if i := strings.LastIndex(base, ":"); i > strings.LastIndex(base, "/") {
		base, img.Tag = base[:i], base[i+1:]
	}
	img.Repository = base
	return img
}
