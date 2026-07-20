package main

import (
	"context"
	"testing"
)

func TestVerify_MismatchDetected(t *testing.T) {
	app, _, _ := newCloudApp(t)
	src := newKMSClient("http://kms.hanzo.svc", newFakeStandalone())
	fs := src.do.(*fakeStandalone)
	dst := newKMSClient("http://cloud.hanzo.svc", cloudDoer{app})

	// Migrate value A, then the source diverges to value B → verify must catch it.
	fs.seed("hanzo", "p", "prod", "K", "value-A")
	inv := Inventory{Targets: []Target{{Org: "hanzo", Path: "p", Env: "prod", Key: "K"}}}
	if r := reseal(context.Background(), inv, src, dst, injectToken, injectToken, false); r.Migrated != 1 {
		t.Fatalf("seed migrate failed: %+v", r.Results)
	}
	fs.seed("hanzo", "p", "prod", "K", "value-B-DIFFERENT")

	vrep := verify(context.Background(), inv, src, dst, injectToken, injectToken)
	if vrep.Mismatch != 1 {
		t.Fatalf("verify mismatch=%d, want 1 (src diverged from cloud): %+v", vrep.Mismatch, vrep.Results)
	}
	// The mismatch detail must be hash prefixes, never the values.
	for _, r := range vrep.Results {
		if r.Outcome == vMismatch {
			if contains(r.Detail, "value-A") || contains(r.Detail, "value-B") {
				t.Errorf("mismatch detail leaked a value: %q", r.Detail)
			}
		}
	}
}

func TestVerify_AbsentOnCloud(t *testing.T) {
	app, _, _ := newCloudApp(t)
	src := newKMSClient("http://kms.hanzo.svc", newFakeStandalone())
	src.do.(*fakeStandalone).seed("hanzo", "p", "prod", "NOT_MIGRATED", "v")
	dst := newKMSClient("http://cloud.hanzo.svc", cloudDoer{app})

	inv := Inventory{Targets: []Target{{Org: "hanzo", Path: "p", Env: "prod", Key: "NOT_MIGRATED"}}}
	vrep := verify(context.Background(), inv, src, dst, injectToken, injectToken)
	if vrep.AbsentD != 1 {
		t.Fatalf("verify absent-dst=%d, want 1 (never migrated): %+v", vrep.AbsentD, vrep.Results)
	}
}

func TestVerify_EmptyFolderIsNonGreen(t *testing.T) {
	app, _, _ := newCloudApp(t)
	src := newKMSClient("http://kms.hanzo.svc", newFakeStandalone()) // seed NOTHING
	dst := newKMSClient("http://cloud.hanzo.svc", cloudDoer{app})

	// A folder-sync CR whose source path holds no keys (billing-kms-sync shape).
	inv := Inventory{Folders: []Target{{Org: "hanzo", Path: "billing-secrets", Env: "prod", Folder: true}}}
	vrep := verify(context.Background(), inv, src, dst, injectToken, injectToken)
	// MED-1: the GREEN gate must count an empty folder as NON-green (not silently pass).
	if vrep.Unseeded != 1 {
		t.Fatalf("empty folder: Unseeded=%d, want 1 (must be non-green): %+v", vrep.Unseeded, vrep.Results)
	}
	if vrep.Match != 0 {
		t.Fatalf("empty folder produced %d matches, want 0", vrep.Match)
	}
}

func TestVerify_OrgIsolationMatrixOnCloud(t *testing.T) {
	app, _, _ := newCloudApp(t)
	dst := newKMSClient("http://cloud.hanzo.svc", cloudDoer{app})

	// org "hanzo" token reading org "acme"'s coordinate → 403; no-principal → 403.
	iso := isolationProbe(context.Background(), dst, "org:hanzo", "hanzo", "acme", "p", "prod", "K")
	if len(iso) != 2 {
		t.Fatalf("isolation probes=%d, want 2", len(iso))
	}
	for _, r := range iso {
		if !r.Passed {
			t.Errorf("isolation %q: got %d want %d (guard must deny)", r.Name, r.Got, r.Want)
		}
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (indexOf(s, sub) >= 0) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
