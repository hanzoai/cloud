package platform

import (
	"testing"

	"github.com/hanzoai/cloud/clients/cd"
)

// The property that matters: when a digest is known, the artifact must reference
// it, because a tag can be moved and a digest cannot. A rollback that resolves a
// moved tag deploys different bytes than the ones that were verified — silently,
// and precisely when someone is trying to recover.
func TestArtifactPrefersTheImmutableReference(t *testing.T) {
	const dg = "sha256:abc123"
	for _, tc := range []struct {
		name  string
		image string
		want  string
	}{
		{"tagged image", "ghcr.io/hanzoai/cloud:v1.801.246", "ghcr.io/hanzoai/cloud@" + dg},
		{"untagged image", "ghcr.io/hanzoai/cloud", "ghcr.io/hanzoai/cloud@" + dg},
		{"already digested", "ghcr.io/hanzoai/cloud@sha256:old", "ghcr.io/hanzoai/cloud@" + dg},
		// A port in the registry host must not be mistaken for a tag separator.
		{"registry with port", "registry.hanzo.ai:5000/hanzoai/cloud:v1", "registry.hanzo.ai:5000/hanzoai/cloud@" + dg},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Artifact(tc.image, dg)
			if got.Ref != tc.want {
				t.Errorf("Ref = %q, want %q", got.Ref, tc.want)
			}
			if got.Kind != cd.KindImage {
				t.Errorf("Kind = %q, want image", got.Kind)
			}
			if got.Digest != dg {
				t.Errorf("Digest = %q, want %q", got.Digest, dg)
			}
		})
	}
}

// With no digest, record the tag honestly rather than fabricating a
// stronger-looking identity.
func TestArtifactWithoutDigestKeepsTheTag(t *testing.T) {
	got := Artifact("ghcr.io/hanzoai/cloud:v1.801.246", "")
	if got.Ref != "ghcr.io/hanzoai/cloud:v1.801.246" {
		t.Errorf("Ref = %q, want the tag unchanged", got.Ref)
	}
	if got.Digest != "" {
		t.Errorf("Digest = %q, want empty — do not invent one", got.Digest)
	}
}

func TestArtifactTrimsWhitespace(t *testing.T) {
	if got := Artifact("  ghcr.io/x/y:v1  ", "  "); got.Ref != "ghcr.io/x/y:v1" {
		t.Errorf("Ref = %q, want trimmed", got.Ref)
	}
}

// It must be placeable by the lifecycle, or the seam is decorative.
func TestArtifactIsAcceptedByAWorkloadTarget(t *testing.T) {
	a := Artifact("ghcr.io/hanzoai/cloud:v1", "sha256:d")
	if a.Kind != cd.KindImage {
		t.Fatalf("Kind = %q; a workload target would reject it", a.Kind)
	}
}
