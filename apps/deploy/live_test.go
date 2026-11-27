package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	cloudDigest = "sha256:1b8b76ed0f4c2a9e7d3b5a6c8e9f0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b"
	kmsDigest   = "sha256:2c9c87fe1a5d3b0f8e4c6b7d9f0a1b2c3d4e5f60718293a4b5c6d7e8f90a1b2c"
)

// liveApp is a CD Application whose pods run images, deployed into ns.
func liveApp(name, ns, revision string, images []any, history []any) *unstructured.Unstructured {
	cr := cdApp(name, "https://git.hanzo.ai/hanzo-inc/universe", revision, "Synced", "Healthy", history)
	cr.Object["spec"].(map[string]any)["destination"] = map[string]any{"server": "https://kubernetes.default.svc", "namespace": ns}
	cr.Object["status"].(map[string]any)["summary"] = map[string]any{"images": images}
	return cr
}

// stubLabels answers label reads from table, counting them. A digest not in the
// table is a private image this deployment holds no credential for.
func stubLabels(t *testing.T, table map[string]label) *atomic.Int64 {
	t.Helper()
	var reads atomic.Int64
	prev, prevMemo := configLabels, labels
	configLabels = func(_ context.Context, ref string) (label, error) {
		reads.Add(1)
		if l, ok := table[ref]; ok {
			return l, nil
		}
		return label{}, errors.New("UNAUTHORIZED")
	}
	labels = newMemo[label]()
	t.Cleanup(func() { configLabels, labels = prev, prevMemo })
	return &reads
}

func decodeLive(t *testing.T, resp *http.Response) Live {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var out Live
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// One row per service: the image each pod runs, split into tag and digest, the
// commit it was built from, the universe commit CD applied, its verdicts, and
// when it last synced — newest deploy, not oldest.
func TestLiveAnswersWhatEachServiceRuns(t *testing.T) {
	reads := stubLabels(t, map[string]label{
		"ghcr.io/hanzoai/cloud@" + cloudDigest: {Commit: "88302a82e1c4b7d6f0a9e8c7b6a5f4e3d2c1b0a9", Repo: "https://github.com/hanzoai/cloud"},
	})
	s := fakeService(
		liveApp("hanzo-cloud", "hanzo", "6fdba8f7b9", []any{"ghcr.io/hanzoai/cloud:v1.2.3@" + cloudDigest}, []any{
			cdHistory(7, "5b8a8c95", "2026-11-06T21:31:09Z"),
			cdHistory(8, "6fdba8f7b9", "2026-11-07T08:44:10Z"),
		}),
		liveApp("hanzo-kms", "hanzo", "6fdba8f7b9", []any{"ghcr.io/hanzoai/kms:v0.9.1@" + kmsDigest, "docker.io/library/busybox:1.36"}, nil),
	)

	out := decodeLive(t, getAs(t, s, "/v1/deploy/live", map[string]string{"X-User-IsAdmin": "true"}))
	if !out.Installed || len(out.Services) != 2 {
		t.Fatalf("live = %+v", out)
	}
	cloud := out.Services[0]
	if cloud.Name != "hanzo-cloud" || cloud.Namespace != "hanzo" || cloud.Revision != "6fdba8f7b9" ||
		cloud.Sync != "Synced" || cloud.Health != "Healthy" || cloud.SyncedAt != "2026-11-07T08:44:10Z" ||
		cloud.RepoURL != "https://git.hanzo.ai/hanzo-inc/universe" {
		t.Fatalf("cloud row = %+v", cloud)
	}
	if len(cloud.Images) != 1 {
		t.Fatalf("cloud images = %+v", cloud.Images)
	}
	img := cloud.Images[0]
	if img.Repository != "ghcr.io/hanzoai/cloud" || img.Tag != "v1.2.3" || img.Digest != cloudDigest || img.Commit != "88302a82e1c4b7d6f0a9e8c7b6a5f4e3d2c1b0a9" || img.Repo != "https://github.com/hanzoai/cloud" {
		t.Fatalf("cloud image = %+v", img)
	}

	// kms's registry read fails and busybox is not ours: both are unknown, and
	// unknown is ABSENT, never a guess. With no history the last successful
	// operation is when it synced.
	kms := out.Services[1]
	if kms.SyncedAt != "2026-07-25T21:44:10Z" || len(kms.Images) != 2 {
		t.Fatalf("kms row = %+v", kms)
	}
	for _, im := range kms.Images {
		if im.Commit != "" || im.Repo != "" {
			t.Fatalf("an unreadable image reported a revision: %+v", im)
		}
	}
	if kms.Images[0].Repository != "docker.io/library/busybox" || kms.Images[0].Tag != "1.36" || kms.Images[0].Digest != "" {
		t.Fatalf("busybox = %+v", kms.Images[0])
	}

	// Only our digest-pinned images reach the registry, each once; a second
	// request answers from what the first learned and does not retry the refusal.
	if n := reads.Load(); n != 2 {
		t.Fatalf("%d registry reads, want 2 (cloud and kms; never busybox)", n)
	}
	decodeLive(t, getAs(t, s, "/v1/deploy/live", map[string]string{"X-User-IsAdmin": "true"}))
	if n := reads.Load(); n != 2 {
		t.Fatalf("%d registry reads after a second request, want still 2", n)
	}
}

// THE ORG SCOPE is gitops's: an org member sees its own org's services and
// another tenant sees none of them, and an unvalidated caller is refused.
func TestLiveIsScopedToTheCallersOrg(t *testing.T) {
	stubLabels(t, nil)
	// cdApp files every Application under project "hanzo"; the tenant's is its own.
	acme := liveApp("acme-shop", "tenant-acme", "a1b2c3d", []any{"ghcr.io/acme/shop:1@" + kmsDigest}, nil)
	acme.Object["spec"].(map[string]any)["project"] = "tenant-acme"
	s := fakeService(
		liveApp("hanzo-cloud", "hanzo", "6fdba8f7b9", []any{"ghcr.io/hanzoai/cloud:v1.2.3@" + cloudDigest}, nil),
		acme,
	)

	names := func(out Live) []string {
		var n []string
		for _, r := range out.Services {
			n = append(n, r.Name)
		}
		return n
	}
	if got := names(decodeLive(t, getAs(t, s, "/v1/deploy/live", orgHeaders("hanzo")))); len(got) != 1 || got[0] != "hanzo-cloud" {
		t.Fatalf("hanzo saw %v, want [hanzo-cloud]", got)
	}
	if got := names(decodeLive(t, getAs(t, s, "/v1/deploy/live", orgHeaders("acme")))); len(got) != 1 || got[0] != "acme-shop" {
		t.Fatalf("acme saw %v, want [acme-shop]", got)
	}
	if got := names(decodeLive(t, getAs(t, s, "/v1/deploy/live", orgHeaders("globex")))); len(got) != 0 {
		t.Fatalf("an unrelated tenant saw %v, want none", got)
	}
	for _, h := range []map[string]string{nil, {"X-Org-Id": "hanzo"}} {
		resp := getAs(t, s, "/v1/deploy/live", h)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("GET as %v = %d, want 403", h, resp.StatusCode)
		}
	}
}

// A fleet with nothing deployed is an empty list, never null.
func TestLiveOfAnEmptyFleetIsAnEmptyList(t *testing.T) {
	stubLabels(t, nil)
	out := decodeLive(t, getAs(t, fakeService(), "/v1/deploy/live", map[string]string{"X-User-IsAdmin": "true"}))
	if !out.Installed || out.Services == nil || len(out.Services) != 0 {
		t.Fatalf("live = %+v; want installed with an empty list", out)
	}
}

// A reference splits on the LAST colon only when it follows the last slash, so a
// registry port is never read as a tag.
func TestImageOfKeepsARegistryPort(t *testing.T) {
	for ref, want := range map[string]Image{
		"oci.hanzo.ai:5000/hanzo/cloud@" + cloudDigest:    {Repository: "oci.hanzo.ai:5000/hanzo/cloud", Digest: cloudDigest},
		"oci.hanzo.ai:5000/hanzo/cloud:v2@" + cloudDigest: {Repository: "oci.hanzo.ai:5000/hanzo/cloud", Tag: "v2", Digest: cloudDigest},
		"ghcr.io/hanzoai/cloud:v1.2.3":                    {Repository: "ghcr.io/hanzoai/cloud", Tag: "v1.2.3"},
		"busybox":                                         {Repository: "busybox"},
	} {
		got := imageOf(ref)
		want.Ref = ref
		if got != want {
			t.Errorf("imageOf(%q) = %+v, want %+v", ref, got, want)
		}
	}
	if _, ok := pinned(Image{Repository: "ghcr.io/hanzoaix/cloud", Digest: cloudDigest}); ok {
		t.Errorf("a lookalike registry was treated as ours")
	}
}
