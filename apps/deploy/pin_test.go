package deploy

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"sync/atomic"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

const (
	universeURL = "https://github.com/hanzo-inc/universe"
	nextDigest  = "sha256:3d0d98a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d"
)

// stubPins answers values-file reads from files, keyed <commit>:<path>, counting
// them. A file not in the table is a forge that did not answer.
func stubPins(t *testing.T, files map[string]layer) *atomic.Int64 {
	t.Helper()
	var n atomic.Int64
	prev, prevMemo := fileLayer, pins
	fileLayer = func(_ context.Context, _, rev, name string) (layer, error) {
		n.Add(1)
		if l, ok := files[rev+":"+name]; ok {
			return l, nil
		}
		return layer{}, errors.New("forge unavailable")
	}
	pins = newMemo[layer]()
	t.Cleanup(func() { fileLayer, pins = prev, prevMemo })
	return &n
}

// file and overlay are the two values files the fleet's ApplicationSet gives a
// service: its declaration, and the AWS overlay that layers over it.
func file(name string) string    { return "charts/app/values/hanzo/" + name + ".yaml" }
func overlay(name string) string { return "infra/aws/values/hanzo/" + name + ".yaml" }

// pinnedApp is a service CD renders from universe's chart: its Application names the
// values files the pin is in, and CD's verdicts are the ones given.
func pinnedApp(name, revision, sync, health string, images, history []any) *unstructured.Unstructured {
	cr := liveApp(name, "hanzo", revision, images, history)
	cr.Object["spec"].(map[string]any)["source"] = map[string]any{
		"repoURL": universeURL, "path": "charts/app", "targetRevision": "main",
		"helm": map[string]any{"valueFiles": []any{
			"values/hanzo/" + name + ".yaml",
			"../../infra/aws/values/hanzo/" + name + ".yaml",
		}},
	}
	status := cr.Object["status"].(map[string]any)
	status["sync"] = map[string]any{"status": sync, "revision": revision}
	status["health"] = map[string]any{"status": health}
	return cr
}

func cloudPin(tag, digest string) layer {
	return layer{Repository: "ghcr.io/hanzoai/cloud", Tag: tag, Digest: digest}
}

func serviceNamed(t *testing.T, out Live, name string) Running {
	t.Helper()
	for _, r := range out.Services {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("no service %q in %+v", name, out.Services)
	return Running{}
}

// A converged rollout: the pods run the pin, CD says Synced and Healthy. The pin
// is read at the commit CD compared against, over the overlay's absence; the
// previous image is the newest earlier deploy that pinned something else, so the
// deploy that only changed configuration is passed over; and a registry the nodes
// serve under another host does not read as drift.
func TestLiveConvergedRolloutNamesWhatItRollsBackTo(t *testing.T) {
	stubLabels(t, nil)
	// The pods run it from ghcr.io; the pin is written against oci.hanzo.ai.
	mirror := cloudPin("v2", cloudDigest)
	mirror.Repository = "oci.hanzo.ai/hanzoai/cloud"
	stubPins(t, map[string]layer{
		"r3:" + file("hanzo-cloud"):    mirror,
		"r3:" + overlay("hanzo-cloud"): {Absent: true},
		"r2:" + file("hanzo-cloud"):    mirror,
		"r2:" + overlay("hanzo-cloud"): {Absent: true},
		"r1:" + file("hanzo-cloud"):    cloudPin("v1", nextDigest),
		"r1:" + overlay("hanzo-cloud"): {Absent: true},
	})
	s := fakeService(pinnedApp("hanzo-cloud", "r3", "Synced", "Healthy",
		[]any{"ghcr.io/hanzoai/cloud:v2@" + cloudDigest},
		[]any{
			cdHistory(1, "r1", "2026-11-06T00:00:00Z"),
			cdHistory(2, "r2", "2026-11-07T00:00:00Z"),
			cdHistory(3, "r3", "2026-11-08T00:00:00Z"),
		}))

	row := serviceNamed(t, decodeLive(t, getAs(t, s, "/v1/deploy/live", map[string]string{"X-User-IsAdmin": "true"})), "hanzo-cloud")
	if row.Declared == nil || row.Declared.Tag != "v2" || row.Declared.Digest != cloudDigest {
		t.Fatalf("declared = %+v", row.Declared)
	}
	if !row.Converged || len(row.Drift) != 0 {
		t.Fatalf("converged = %v drift = %v, want converged", row.Converged, row.Drift)
	}
	if row.Previous == nil || row.Previous.Tag != "v1" || row.Previous.Digest != nextDigest {
		t.Fatalf("previous = %+v, want v1@%s (r2 only changed configuration)", row.Previous, nextDigest)
	}
}

// A rollout in progress: universe pins v3, CD has not synced it, the pods still
// run v2 and some are coming up. All three disagreements are named.
func TestLiveRolloutInProgressNamesEveryDisagreement(t *testing.T) {
	stubLabels(t, nil)
	stubPins(t, map[string]layer{
		"r2:" + file("hanzo-cloud"):    cloudPin("v3", nextDigest),
		"r2:" + overlay("hanzo-cloud"): {Absent: true},
		"r1:" + file("hanzo-cloud"):    cloudPin("v2", cloudDigest),
		"r1:" + overlay("hanzo-cloud"): {Absent: true},
	})
	s := fakeService(pinnedApp("hanzo-cloud", "r2", "OutOfSync", "Progressing",
		[]any{"ghcr.io/hanzoai/cloud:v2@" + cloudDigest},
		[]any{cdHistory(1, "r1", "2026-11-07T00:00:00Z")}))

	row := serviceNamed(t, decodeLive(t, getAs(t, s, "/v1/deploy/live", map[string]string{"X-User-IsAdmin": "true"})), "hanzo-cloud")
	if row.Converged || !reflect.DeepEqual(row.Drift, []string{"image", "sync", "health"}) {
		t.Fatalf("converged = %v drift = %v, want image, sync, health", row.Converged, row.Drift)
	}
	if row.Declared == nil || row.Declared.Tag != "v3" {
		t.Fatalf("declared = %+v, want v3", row.Declared)
	}
	if row.Previous != nil {
		t.Fatalf("previous = %+v, want none: CD's history holds one deploy", row.Previous)
	}
}

// A failed rollout: the pods run the pin and CD is Synced, but the objects are
// Degraded. Only health is named.
func TestLiveFailedRolloutIsDegradedAlone(t *testing.T) {
	stubLabels(t, nil)
	stubPins(t, map[string]layer{
		"r1:" + file("hanzo-cloud"):    cloudPin("v2", cloudDigest),
		"r1:" + overlay("hanzo-cloud"): {Absent: true},
	})
	s := fakeService(pinnedApp("hanzo-cloud", "r1", "Synced", "Degraded",
		[]any{"ghcr.io/hanzoai/cloud:v2@" + cloudDigest}, nil))

	row := serviceNamed(t, decodeLive(t, getAs(t, s, "/v1/deploy/live", map[string]string{"X-User-IsAdmin": "true"})), "hanzo-cloud")
	if row.Converged || !reflect.DeepEqual(row.Drift, []string{"health"}) {
		t.Fatalf("converged = %v drift = %v, want health alone", row.Converged, row.Drift)
	}
}

// Drift between the pin and the pods: CD says Synced and Healthy — it applied
// what git says — but the pods run another tag. Nothing else disagrees, and an
// overlay layered over the pin is the pin that counts.
func TestLiveDriftBetweenPinAndPods(t *testing.T) {
	stubLabels(t, nil)
	stubPins(t, map[string]layer{
		"r1:" + file("hanzo-cloud"):    cloudPin("v1", cloudDigest),
		"r1:" + overlay("hanzo-cloud"): {Tag: "v2", Digest: nextDigest},
	})
	s := fakeService(pinnedApp("hanzo-cloud", "r1", "Synced", "Healthy",
		[]any{"ghcr.io/hanzoai/cloud:v1@" + cloudDigest}, nil))

	row := serviceNamed(t, decodeLive(t, getAs(t, s, "/v1/deploy/live", map[string]string{"X-User-IsAdmin": "true"})), "hanzo-cloud")
	if row.Converged || !reflect.DeepEqual(row.Drift, []string{"image"}) {
		t.Fatalf("converged = %v drift = %v, want image alone", row.Converged, row.Drift)
	}
	if row.Declared == nil || row.Declared.Tag != "v2" || row.Declared.Repository != "ghcr.io/hanzoai/cloud" {
		t.Fatalf("declared = %+v, want the overlay's tag over the file's repository", row.Declared)
	}
}

// A pin the forge did not answer is not agreement: the row says unread and is not
// converged, whatever CD's verdicts are. A service CD renders from plain manifests
// has no pin to read, and one whose Application names a repository outside the
// allowlist is not read at all.
func TestLiveUnreadAndUnpinnedServices(t *testing.T) {
	stubLabels(t, nil)
	reads := stubPins(t, nil)
	stray := pinnedApp("hanzo-stray", "r1", "Synced", "Healthy", nil, nil)
	stray.Object["spec"].(map[string]any)["source"].(map[string]any)["repoURL"] = "https://github.com/acme/elsewhere"
	s := fakeService(
		pinnedApp("hanzo-cloud", "r1", "Synced", "Healthy", []any{"ghcr.io/hanzoai/cloud:v1@" + cloudDigest}, nil),
		liveApp("hanzo-edge", "hanzo", "r1", nil, nil),
		stray,
	)

	out := decodeLive(t, getAs(t, s, "/v1/deploy/live", map[string]string{"X-User-IsAdmin": "true"}))
	if cloud := serviceNamed(t, out, "hanzo-cloud"); cloud.Converged || cloud.Declared != nil || !reflect.DeepEqual(cloud.Drift, []string{"unread"}) {
		t.Fatalf("unread row = %+v", cloud)
	}
	for _, name := range []string{"hanzo-edge", "hanzo-stray"} {
		if r := serviceNamed(t, out, name); !r.Converged || r.Declared != nil || len(r.Drift) != 0 {
			t.Fatalf("%s = %+v, want converged with nothing declared", name, r)
		}
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("%d forge reads, want 1 (the first file of hanzo-cloud, which failed)", n)
	}
}

// Overlay files layer over the file CD renders first, key by key; and the file CD
// cannot render without being absent is a wrong coordinate, not an empty pin.
func TestDeclareLayersFilesAndRefusesAMissingBase(t *testing.T) {
	stubPins(t, map[string]layer{
		"r1:a.yaml": {Repository: "ghcr.io/hanzoai/x", Tag: "1", Digest: "sha256:aa"},
		"r1:b.yaml": {Tag: "2"},
		"r1:c.yaml": {Absent: true},
		"r1:d.yaml": {},
	})
	img, ok := declare(context.Background(), "hanzo-inc/universe", "r1", []string{"a.yaml", "b.yaml", "c.yaml"})
	if !ok || img == nil || img.Ref != "ghcr.io/hanzoai/x:2@sha256:aa" {
		t.Fatalf("declare = %+v, %v", img, ok)
	}
	if img, ok := declare(context.Background(), "hanzo-inc/universe", "r1", []string{"d.yaml"}); !ok || img != nil {
		t.Fatalf("a file with no image block declared %+v, %v; want nothing, read", img, ok)
	}
	if _, ok := declare(context.Background(), "hanzo-inc/universe", "r1", []string{"c.yaml", "a.yaml"}); ok {
		t.Fatalf("a missing first file read as an empty pin")
	}
}

// The commit query: a prefix of seven or more hex characters of a build commit,
// across every listed service or the one asked for. An image the registry did not
// answer could be the one asked about, so without a hit the answer is absent.
func TestLiveCommitQuery(t *testing.T) {
	stubPins(t, nil)
	stubLabels(t, map[string]label{
		"ghcr.io/hanzoai/cloud@" + cloudDigest: {Commit: "b37f1d7014bc834b8441bff121a23c15339c0294", Repo: "https://github.com/hanzoai/cloud"},
		"ghcr.io/hanzoai/kms@" + kmsDigest:     {Commit: "88302a82e1c4b7d6f0a9e8c7b6a5f4e3d2c1b0a9"},
	})
	admin := map[string]string{"X-User-IsAdmin": "true"}
	cloud := liveApp("hanzo-cloud", "hanzo", "r1", []any{"ghcr.io/hanzoai/cloud:v1@" + cloudDigest}, nil)
	kms := liveApp("hanzo-kms", "hanzo", "r1", []any{"ghcr.io/hanzoai/kms:v1@" + kmsDigest}, nil)
	private := liveApp("hanzo-dark", "hanzo", "r1", []any{"ghcr.io/hanzo-inc/dark:v1@" + nextDigest}, nil)

	ask := func(q string, apps ...*unstructured.Unstructured) Live {
		t.Helper()
		objs := make([]runtime.Object, len(apps))
		for i, a := range apps {
			objs[i] = a
		}
		return decodeLive(t, getAs(t, fakeService(objs...), "/v1/deploy/live?"+q, admin))
	}
	want := func(got Live, live *bool) {
		t.Helper()
		if (got.Live == nil) != (live == nil) || (live != nil && *got.Live != *live) {
			t.Fatalf("live = %v, want %v", got.Live, live)
		}
	}
	yes, no := true, false

	want(ask("commit=b37f1d7", cloud, kms), &yes)
	want(ask("commit=B37F1D7014BC", cloud, kms), &yes)
	want(ask("commit=88302a8", cloud, kms), &yes)
	want(ask("commit=deadbeef", cloud, kms), &no)
	want(ask("", cloud, kms), nil)
	// Scoped to a service, another service's commit is not live there.
	want(ask("service=hanzo-kms&commit=b37f1d7", cloud, kms), &no)
	want(ask("service=hanzo-cloud&commit=b37f1d7", cloud, kms), &yes)
	if got := ask("service=hanzo-kms&commit=88302a8", cloud, kms); len(got.Services) != 1 || got.Services[0].Name != "hanzo-kms" {
		t.Fatalf("service filter kept %+v", got.Services)
	}
	// A private image the registry would not answer might be built from it.
	want(ask("commit=deadbeef", cloud, private), nil)
	want(ask("commit=b37f1d7", cloud, private), &yes)

	for _, bad := range []string{"commit=b37f1d", "commit=nothex12", "commit=b37f1d7%20"} {
		resp := getAs(t, fakeService(cloud), "/v1/deploy/live?"+bad, admin)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("GET ?%s = %d, want 400", bad, resp.StatusCode)
		}
	}
}

// A service query reads only that service: the filter comes before the reads.
func TestLiveServiceQueryReadsOnlyThatService(t *testing.T) {
	stubLabels(t, nil)
	reads := stubPins(t, map[string]layer{
		"r1:" + file("hanzo-cloud"):    cloudPin("v1", cloudDigest),
		"r1:" + overlay("hanzo-cloud"): {Absent: true},
	})
	s := fakeService(
		pinnedApp("hanzo-cloud", "r1", "Synced", "Healthy", []any{"ghcr.io/hanzoai/cloud:v1@" + cloudDigest}, nil),
		pinnedApp("hanzo-kms", "r1", "Synced", "Healthy", nil, nil),
	)
	out := decodeLive(t, getAs(t, s, "/v1/deploy/live?service=hanzo-cloud", map[string]string{"X-User-IsAdmin": "true"}))
	if len(out.Services) != 1 || !out.Services[0].Converged {
		t.Fatalf("live = %+v", out)
	}
	if n := reads.Load(); n != 2 {
		t.Fatalf("%d forge reads, want 2 (the file and its overlay)", n)
	}
}
