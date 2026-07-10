// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Registry pull — the CONSUMER half of the SBOM lane. The registry is the source
// of truth: CI produces the CycloneDX SBOM and `cosign attach`es it to the image
// DIGEST. This file reads that attached artifact back with go-containerregistry
// (pure-Go, no binary deps) and hands the raw CycloneDX document to the SAME
// server-side parser the POST /v1/sbom path uses (parseComponents), so ingest and
// pull share one component-flattening code path.
//
// Two attachment conventions are honored, in order:
//
//  1. cosign SBOM tag — `cosign attach sbom` writes an image tagged
//     `sha256-<hex>.sbom` in the SAME repo as the subject. Deterministic, no
//     referrers API needed. Tried first.
//  2. OCI 1.1 referrers — the registry's /referrers response for the digest lists
//     manifests that declare the subject; we pick the CycloneDX one by its
//     artifactType/mediaType. Fallback for registries/producers that use referrers.
//
// A pull failure is NON-FATAL to the caller: pull-on-miss simply falls back to the
// honest 404. Nothing here fabricates components — a document must parse as
// CycloneDX or it is ignored.
package sbom

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

// maxSBOMBytes bounds a single attached document read so a hostile/broken artifact
// can't exhaust memory. CycloneDX SBOMs for our images are well under this.
const maxSBOMBytes = 64 << 20 // 64 MiB

// pulled is the result of a successful registry pull: the resolved digest, the ref
// we pulled through, and the parsed CycloneDX components ready to persist.
type pulled struct {
	Digest     string
	Ref        string
	Components []SbomComponent
}

// pullSBOM resolves ref → digest, locates the attached CycloneDX SBOM in the
// registry, and parses it into components. ref MUST be a full image reference with
// a repository (a bare `sha256:…` digest has no repo to pull from → error). Pure
// production entry: DefaultKeychain auth (in-cluster/registry creds when present,
// anonymous for public images) + the caller's context for cancellation.
func pullSBOM(ctx context.Context, ref string) (*pulled, error) {
	return pullSBOMWith(ctx, ref, nil, remote.WithAuthFromKeychain(authn.DefaultKeychain))
}

// pullSBOMWith is the testable core: nameOpts (e.g. name.Insecure) tune reference
// parsing; remoteOpts carry auth/transport. It is split out so a fake in-memory
// registry can exercise the exact same locate logic as production.
func pullSBOMWith(ctx context.Context, ref string, nameOpts []name.Option, remoteOpts ...remote.Option) (*pulled, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, fmt.Errorf("empty image reference")
	}
	parsed, err := name.ParseReference(ref, nameOpts...)
	if err != nil {
		return nil, fmt.Errorf("parse reference %q: %w", ref, err)
	}
	remoteOpts = append([]remote.Option{remote.WithContext(ctx)}, remoteOpts...)

	// Resolve the subject digest. A digest ref is already resolved; a tag ref needs a
	// HEAD. The SBOM is content-addressed to this digest by both conventions.
	repo := parsed.Context()
	var digest string
	if d, ok := parsed.(name.Digest); ok {
		digest = d.DigestStr()
	} else {
		desc, herr := remote.Head(parsed, remoteOpts...)
		if herr != nil {
			return nil, fmt.Errorf("resolve digest for %q: %w", ref, herr)
		}
		digest = desc.Digest.String()
	}

	doc, err := locateSBOM(repo, digest, nameOpts, remoteOpts...)
	if err != nil {
		return nil, err
	}
	comps, err := parseComponents(doc)
	if err != nil {
		return nil, fmt.Errorf("parse attached SBOM for %s: %w", digest, err)
	}
	if len(comps) == 0 {
		return nil, fmt.Errorf("attached SBOM for %s has no components", digest)
	}
	return &pulled{Digest: digest, Ref: ref, Components: comps}, nil
}

// locateSBOM finds the raw CycloneDX document for a subject digest: cosign SBOM tag
// first, OCI referrers fallback. Returns the first document that yields CycloneDX
// components.
func locateSBOM(repo name.Repository, digest string, nameOpts []name.Option, remoteOpts ...remote.Option) ([]byte, error) {
	if doc, err := fromSBOMTag(repo, digest, nameOpts, remoteOpts...); err == nil && doc != nil {
		return doc, nil
	}
	if doc, err := fromReferrers(repo, digest, remoteOpts...); err == nil && doc != nil {
		return doc, nil
	}
	return nil, fmt.Errorf("no CycloneDX SBOM attached to %s (checked cosign sbom tag and OCI referrers)", digest)
}

// sbomTagFor derives the cosign SBOM tag for a digest: `sha256:<hex>` →
// `sha256-<hex>.sbom` (cosign replaces ':' with '-' and appends ".sbom").
func sbomTagFor(digest string) string {
	return strings.ReplaceAll(digest, ":", "-") + ".sbom"
}

// fromSBOMTag pulls the cosign SBOM-tag image and returns its CycloneDX layer.
func fromSBOMTag(repo name.Repository, digest string, nameOpts []name.Option, remoteOpts ...remote.Option) ([]byte, error) {
	tag, err := name.NewTag(repo.Name()+":"+sbomTagFor(digest), nameOpts...)
	if err != nil {
		return nil, err
	}
	img, err := remote.Image(tag, remoteOpts...)
	if err != nil {
		return nil, err // typically manifest-unknown when no SBOM tag exists → fall through
	}
	return cyclonedxFromImage(img)
}

// fromReferrers walks the OCI referrers of the subject digest and returns the
// CycloneDX layer of the first manifest that declares a CycloneDX artifact/media
// type.
func fromReferrers(repo name.Repository, digest string, remoteOpts ...remote.Option) ([]byte, error) {
	subject := repo.Digest(digest)
	idx, err := remote.Referrers(subject, remoteOpts...)
	if err != nil {
		return nil, err
	}
	manifest, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	for _, m := range manifest.Manifests {
		if !isCycloneDX(string(m.MediaType)) && !isCycloneDX(m.ArtifactType) {
			continue
		}
		img, err := remote.Image(repo.Digest(m.Digest.String()), remoteOpts...)
		if err != nil {
			continue
		}
		if doc, err := cyclonedxFromImage(img); err == nil && doc != nil {
			return doc, nil
		}
	}
	return nil, fmt.Errorf("no CycloneDX referrer for %s", digest)
}

// cyclonedxFromImage extracts the SBOM document from an attachment image: the layer
// whose media type is CycloneDX, else (single-layer cosign attachments don't always
// set a distinctive type) the first layer. The bytes are size-bounded.
func cyclonedxFromImage(img v1.Image) ([]byte, error) {
	layers, err := img.Layers()
	if err != nil {
		return nil, err
	}
	if len(layers) == 0 {
		return nil, fmt.Errorf("attachment has no layers")
	}
	chosen := layers[0]
	for _, l := range layers {
		mt, merr := l.MediaType()
		if merr == nil && isCycloneDX(string(mt)) {
			chosen = l
			break
		}
	}
	return readLayer(chosen)
}

// readLayer reads an uncompressed layer with a hard size cap.
func readLayer(l v1.Layer) ([]byte, error) {
	rc, err := l.Uncompressed()
	if err != nil {
		return nil, err
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, maxSBOMBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxSBOMBytes {
		return nil, fmt.Errorf("attached SBOM exceeds %d bytes", maxSBOMBytes)
	}
	return b, nil
}

// isCycloneDX matches the CycloneDX media/artifact types cosign and OCI referrers
// use for an SBOM: `application/vnd.cyclonedx+json`, `…+json;version=…`, or the bare
// cyclonedx marker. Case-insensitive, substring — tolerant of version suffixes.
func isCycloneDX(mt string) bool {
	return strings.Contains(strings.ToLower(mt), "cyclonedx")
}
