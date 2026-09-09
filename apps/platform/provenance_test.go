package platform

import "testing"

// An image published by this door names the version it was published under and
// the commit it was built from, WITHOUT the repository's cooperation.
//
// Passing the two as build-args reaches the image only where the Dockerfile
// declares the matching ARG and stamps a LABEL from it. Measured on what the door
// actually published: ghcr.io/hanzoai/ci:v1.0.95 and both of its per-architecture
// halves carry no labels at all, because ci's Dockerfile declares neither an ARG
// nor a LABEL, while v1.0.94 from the GitHub-Actions lane carries revision,
// source and version. Two builders, two answers to "which commit is this", and
// the digest-pinned lane rests on that answer.
func TestAnImageNamesItsVersionAndCommitWithoutTheDockerfile(t *testing.T) {
	rev := "7d62d13c0a1b2c3d4e5f60718293a4b5c6d7e8f9"
	cmd := buildFrontendCmdRev("https://git.example/x.git#"+rev, "Dockerfile",
		"ghcr.io/hanzoai/x:v1.0.95", "ghcr.io/hanzoai/x:v1.0.95", rev, nil)

	if v, ok := argvOpt(cmd, "label:org.opencontainers.image.version="); !ok || v != "v1.0.95" {
		t.Errorf("version label = %q (present %v), want v1.0.95", v, ok)
	}
	if v, ok := argvOpt(cmd, "label:org.opencontainers.image.revision="); !ok || v != rev {
		t.Errorf("revision label = %q (present %v), want %s", v, ok, rev)
	}
}

// One half of a fan-out publishes at the release tag plus its architecture, and
// the label follows the version the RELEASE claims — `v1.0.95-arm64` is a place
// to push, never a version anybody released.
func TestAFanOutHalfLabelsTheReleaseVersionNotItsOwnTag(t *testing.T) {
	cmd := buildFrontendCmdRev("https://git.example/x.git#main", "Dockerfile",
		"ghcr.io/hanzoai/x:v1.0.95", "ghcr.io/hanzoai/x:v1.0.95-arm64", "", []string{"linux/arm64"})
	if v, _ := argvOpt(cmd, "label:org.opencontainers.image.version="); v != "v1.0.95" {
		t.Errorf("version label = %q, want v1.0.95", v)
	}
}

// A branch is not a revision and a digest names no version, so neither is
// claimed. A label that says "main" reads as populated and answers nothing.
func TestNoReceiptIsInventedWhereThereIsNone(t *testing.T) {
	cmd := buildFrontendCmdRev("https://git.example/x.git#main", "Dockerfile",
		"ghcr.io/hanzoai/x@sha256:"+"ab"+"cd"+"ef01234567890123456789012345678901234567890123456789012345",
		"ghcr.io/hanzoai/x@sha256:"+"ab"+"cd"+"ef01234567890123456789012345678901234567890123456789012345",
		"main", nil)
	if v, ok := argvOpt(cmd, "label:org.opencontainers.image.version="); ok {
		t.Errorf("a digest-pinned ref claimed version %q", v)
	}
	if v, ok := argvOpt(cmd, "label:org.opencontainers.image.revision="); ok {
		t.Errorf("a branch was stamped as revision %q", v)
	}
}
