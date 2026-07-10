// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package sbom

import (
	"context"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
)

// cycloneDXDoc is a minimal but real CycloneDX document with three components,
// including one with an SPDX license id — the exact shape parseComponents flattens.
const cycloneDXDoc = `{
  "bomFormat": "CycloneDX",
  "specVersion": "1.5",
  "components": [
    {"type":"library","name":"golang.org/x/net","version":"v0.38.0","purl":"pkg:golang/golang.org/x/net@v0.38.0","licenses":[{"license":{"id":"BSD-3-Clause"}}]},
    {"type":"library","name":"github.com/google/go-containerregistry","version":"v0.21.7","purl":"pkg:golang/github.com/google/go-containerregistry@v0.21.7","licenses":[{"license":{"id":"Apache-2.0"}}]},
    {"type":"application","name":"cloud","version":"v1.786.166"}
  ]
}`

const cyclonedxMediaType = types.MediaType("application/vnd.cyclonedx+json")

// newRegistry spins up an in-memory OCI registry on 127.0.0.1 (auto-detected as
// HTTP by go-containerregistry), returning its host and a cleanup.
func newRegistry(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(registry.New())
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatalf("parse registry url: %v", err)
	}
	return u.Host
}

// pushSubject pushes a trivial subject image and returns its ref + digest.
func pushSubject(t *testing.T, host string) (name.Digest, v1.Hash) {
	t.Helper()
	ref, err := name.NewTag(host+"/hanzoai/api:main", name.Insecure)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	img, err := mutate.AppendLayers(empty.Image, static.NewLayer([]byte("subject"), types.OCILayer))
	if err != nil {
		t.Fatalf("build subject: %v", err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatalf("push subject: %v", err)
	}
	dig, err := img.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	return ref.Context().Digest(dig.String()), dig
}

// attachSBOMTag writes the cosign SBOM-tag image (`sha256-<hex>.sbom`) carrying the
// CycloneDX document as its layer — the `cosign attach sbom` convention.
func attachSBOMTag(t *testing.T, host string, subject v1.Hash) {
	t.Helper()
	tag, err := name.NewTag(host+"/hanzoai/api:"+sbomTagFor(subject.String()), name.Insecure)
	if err != nil {
		t.Fatalf("sbom tag: %v", err)
	}
	img, err := mutate.AppendLayers(empty.Image, static.NewLayer([]byte(cycloneDXDoc), cyclonedxMediaType))
	if err != nil {
		t.Fatalf("build sbom image: %v", err)
	}
	if err := remote.Write(tag, img); err != nil {
		t.Fatalf("push sbom tag: %v", err)
	}
}

// attachReferrer writes a CycloneDX artifact that declares the subject via the OCI
// referrers Subject field — the OCI 1.1 referrers convention. The registry surfaces
// the manifest's config media type as the referrer descriptor's artifactType, so we
// set config = the CycloneDX type for the consumer to select on.
func attachReferrer(t *testing.T, host string, subjectDigest name.Digest, subject v1.Hash) {
	t.Helper()
	base, err := mutate.AppendLayers(empty.Image, static.NewLayer([]byte(cycloneDXDoc), cyclonedxMediaType))
	if err != nil {
		t.Fatalf("build referrer: %v", err)
	}
	base = mutate.ConfigMediaType(base, cyclonedxMediaType)
	base = mutate.MediaType(base, types.OCIManifestSchema1)
	withSub := mutate.Subject(base, v1.Descriptor{
		MediaType: types.OCIManifestSchema1,
		Digest:    subject,
	}).(v1.Image)
	rd, err := withSub.Digest()
	if err != nil {
		t.Fatalf("referrer digest: %v", err)
	}
	if err := remote.Write(subjectDigest.Context().Digest(rd.String()), withSub); err != nil {
		t.Fatalf("push referrer: %v", err)
	}
}

// TestPullSBOMFromCosignTag proves the primary path: a subject image with an
// attached `sha256-<hex>.sbom` cosign tag is resolved by REF (tag) → digest → SBOM,
// parsed into the expected components.
func TestPullSBOMFromCosignTag(t *testing.T) {
	host := newRegistry(t)
	subjectDigest, subjectHash := pushSubject(t, host)
	attachSBOMTag(t, host, subjectHash)

	ref := host + "/hanzoai/api:main"
	got, err := pullSBOMWith(context.Background(), ref, []name.Option{name.Insecure}, remote.WithContext(context.Background()))
	if err != nil {
		t.Fatalf("pullSBOM: %v", err)
	}
	if got.Digest != subjectDigest.DigestStr() {
		t.Fatalf("digest mismatch: got %s want %s", got.Digest, subjectDigest.DigestStr())
	}
	assertComponents(t, got.Components)
}

// TestPullSBOMFromReferrers proves the fallback path: no cosign SBOM tag exists, but
// a CycloneDX referrer declares the subject; the consumer walks referrers and finds
// it. Resolved by DIGEST ref this time.
func TestPullSBOMFromReferrers(t *testing.T) {
	host := newRegistry(t)
	subjectDigest, subjectHash := pushSubject(t, host)
	attachReferrer(t, host, subjectDigest, subjectHash)

	got, err := pullSBOMWith(context.Background(), subjectDigest.String(), []name.Option{name.Insecure}, remote.WithContext(context.Background()))
	if err != nil {
		t.Fatalf("pullSBOM (referrers): %v", err)
	}
	assertComponents(t, got.Components)
}

// TestPullSBOMNoAttachment proves the honest miss: a subject with NO attached SBOM
// yields an error (which the resolve path maps to 404), never fabricated data.
func TestPullSBOMNoAttachment(t *testing.T) {
	host := newRegistry(t)
	subjectDigest, _ := pushSubject(t, host)
	_, err := pullSBOMWith(context.Background(), subjectDigest.String(), []name.Option{name.Insecure}, remote.WithContext(context.Background()))
	if err == nil {
		t.Fatal("want error for image with no attached SBOM, got nil")
	}
	if !strings.Contains(err.Error(), "no CycloneDX SBOM") {
		t.Fatalf("want no-SBOM error, got: %v", err)
	}
}

// TestPullSBOMBareDigestUnpullable proves a bare digest (no repository) is not
// pullable — the caller treats the error as a miss, never a crash.
func TestPullSBOMBareDigestUnpullable(t *testing.T) {
	_, err := pullSBOM(context.Background(), "sha256:"+strings.Repeat("a", 64))
	if err == nil {
		t.Fatal("want error for bare digest with no repository, got nil")
	}
}

func assertComponents(t *testing.T, comps []SbomComponent) {
	t.Helper()
	if len(comps) != 3 {
		t.Fatalf("want 3 components, got %d: %+v", len(comps), comps)
	}
	byName := map[string]SbomComponent{}
	for _, c := range comps {
		byName[c.Name] = c
	}
	net, ok := byName["golang.org/x/net"]
	if !ok {
		t.Fatalf("missing golang.org/x/net component: %+v", comps)
	}
	if net.Version != "v0.38.0" || net.License != "BSD-3-Clause" || net.Type != "library" {
		t.Fatalf("golang.org/x/net flattened wrong: %+v", net)
	}
	if net.Purl != "pkg:golang/golang.org/x/net@v0.38.0" {
		t.Fatalf("purl wrong: %+v", net)
	}
}
