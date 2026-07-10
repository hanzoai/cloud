// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package sbom

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/v1/empty"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/static"
	"github.com/google/go-containerregistry/pkg/v1/types"
	aiobject "github.com/hanzoai/ai/object"
)

// TestPullOnMissLiveEndToEnd is the FULL consumer-pull proof against real infra:
// a real OCI registry (registry:2) serving a real CycloneDX SBOM (cyclonedx-gomod
// output) cosign-attached to an image digest, materialized through the REAL
// production path — GET /v1/sbom/{ref} → pull-on-miss → production pullSBOM →
// go-containerregistry → parseComponents → INSERT into a real ClickHouse → reread.
//
// It exercises production pullSBOM (DefaultKeychain, no test-only insecure opt) —
// go-containerregistry auto-selects http for a localhost registry, so the SAME code
// that runs in prod runs here. Env-gated so `go test` stays hermetic:
//
//	SBOM_LIVE_REGISTRY  host:port of a writable OCI registry (e.g. localhost:5555)
//	SBOM_LIVE_DOC       path to a CycloneDX JSON document to attach
//	DATASTORE_ADDR      host:port of a ClickHouse native port (e.g. localhost:19000)
func TestPullOnMissLiveEndToEnd(t *testing.T) {
	reg := os.Getenv("SBOM_LIVE_REGISTRY")
	doc := os.Getenv("SBOM_LIVE_DOC")
	if reg == "" || doc == "" || os.Getenv("DATASTORE_ADDR") == "" {
		t.Skip("set SBOM_LIVE_REGISTRY, SBOM_LIVE_DOC, DATASTORE_ADDR to run the live end-to-end pull")
	}
	sbomBytes, err := os.ReadFile(doc)
	if err != nil {
		t.Fatalf("read SBOM_LIVE_DOC: %v", err)
	}
	want, err := parseComponents(sbomBytes)
	if err != nil || len(want) == 0 {
		t.Fatalf("SBOM_LIVE_DOC is not a non-empty CycloneDX document: %v (%d comps)", err, len(want))
	}

	// Push a real subject image + the cosign SBOM-tag attachment to the real registry.
	subjectRef := reg + "/hanzoai/cloud:live"
	tag, err := name.NewTag(subjectRef)
	if err != nil {
		t.Fatalf("tag: %v", err)
	}
	subj, err := mutate.AppendLayers(empty.Image, static.NewLayer([]byte("subject"), types.OCILayer))
	if err != nil {
		t.Fatalf("subject: %v", err)
	}
	if err := remote.Write(tag, subj); err != nil {
		t.Fatalf("push subject: %v", err)
	}
	dig, err := subj.Digest()
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	sbomTag, err := name.NewTag(tag.Context().Name() + ":" + sbomTagFor(dig.String()))
	if err != nil {
		t.Fatalf("sbom tag: %v", err)
	}
	sbomImg, err := mutate.AppendLayers(empty.Image, static.NewLayer(sbomBytes, "application/vnd.cyclonedx+json"))
	if err != nil {
		t.Fatalf("sbom image: %v", err)
	}
	if err := remote.Write(sbomTag, sbomImg); err != nil {
		t.Fatalf("push sbom attachment: %v", err)
	}
	t.Logf("pushed subject %s @ %s + SBOM attachment %s (%d real components)", subjectRef, dig, sbomTag, len(want))

	// Connect the real ClickHouse and wait for the async datastore to latch ready.
	aiobject.InitDatastore()
	deadline := time.Now().Add(20 * time.Second)
	for !aiobject.DatastoreEnabled() {
		if time.Now().After(deadline) {
			t.Fatal("datastore did not become ready (is ClickHouse at DATASTORE_ADDR up?)")
		}
		time.Sleep(300 * time.Millisecond)
	}

	app := mountApp(t)

	// First GET: not in ClickHouse yet → pull-on-miss materializes it → 200 real comps.
	code, body := do(t, app, http.MethodGet, "/v1/sbom/"+subjectRef, "", false)
	if code != http.StatusOK {
		t.Fatalf("pull-on-miss GET want 200, got %d (%s)", code, body)
	}
	var view SbomView
	if err := json.Unmarshal(body, &view); err != nil {
		t.Fatalf("unmarshal view: %v (%s)", err, body)
	}
	if view.ComponentCount != len(want) {
		t.Fatalf("component count: got %d want %d", view.ComponentCount, len(want))
	}
	if view.ImageDigest != dig.String() {
		t.Fatalf("resolved digest: got %q want %q", view.ImageDigest, dig.String())
	}
	assertHasComponent(t, view.Components, want[0].Name, want[0].Version)
	t.Logf("pull-on-miss materialized %d components; digest=%s", view.ComponentCount, view.ImageDigest)

	// Second GET: now a ClickHouse cache HIT (no pull) — same real components.
	code2, body2 := do(t, app, http.MethodGet, "/v1/sbom/"+subjectRef, "", false)
	if code2 != http.StatusOK {
		t.Fatalf("cache-hit GET want 200, got %d (%s)", code2, body2)
	}
	var view2 SbomView
	if err := json.Unmarshal(body2, &view2); err != nil {
		t.Fatalf("unmarshal cache-hit view: %v", err)
	}
	if view2.ComponentCount != len(want) {
		t.Fatalf("cache-hit component count: got %d want %d", view2.ComponentCount, len(want))
	}

	// Resolve by DIGEST too (the console can hold a digest ref).
	code3, body3 := do(t, app, http.MethodGet, "/v1/sbom/"+reg+"/hanzoai/cloud@"+dig.String(), "", false)
	if code3 != http.StatusOK {
		t.Fatalf("digest-ref GET want 200, got %d (%s)", code3, body3)
	}
}

func assertHasComponent(t *testing.T, comps []SbomComponent, name, version string) {
	t.Helper()
	for _, c := range comps {
		if c.Name == name && c.Version == version {
			return
		}
	}
	t.Fatalf("expected component %s@%s not found in %d rows", name, version, len(comps))
}
