package platform

import (
	"strings"
	"testing"
)

// VERSION and GIT_VERSION come from one splitImageRef call, so they agree by
// construction: same tag, and GIT_VERSION without the leading v because that is
// how a Makefile writes a version it would otherwise get from `git describe`.
// latest names no release and a digest names no version, so both are suppressed
// rather than stamped as one.
func TestBuildArgsFromImageTag(t *testing.T) {
	argOf := func(cmd []any, key string) string {
		for i := 0; i+1 < len(cmd); i++ {
			if s, ok := cmd[i+1].(string); ok && strings.HasPrefix(s, key+"=") {
				return strings.TrimPrefix(s, key+"=")
			}
		}
		return ""
	}
	for _, c := range []struct{ image, version, gitVersion string }{
		{"ghcr.io/hanzoai/git:v1.26.25", "v1.26.25", "1.26.25"},
		{"ghcr.io/hanzoai/console:v8.5.29", "v8.5.29", "8.5.29"},
		{"ghcr.io/hanzoai/git:1.26.25", "1.26.25", "1.26.25"},
		{"registry.example.com:5000/team/app:v2.0.0", "v2.0.0", "2.0.0"},
		{"registry.example.com:5000/team/app", "", ""},                    // implicit latest
		{"ghcr.io/hanzoai/git:latest", "", ""},                            // names no release
		{"ghcr.io/hanzoai/git@sha256:" + strings.Repeat("a", 64), "", ""}, // names no version
	} {
		cmd := buildFrontendCmd("ctx", "Dockerfile", c.image)
		if got := argOf(cmd, "build-arg:VERSION"); got != c.version {
			t.Errorf("%s: VERSION = %q, want %q", c.image, got, c.version)
		}
		if got := argOf(cmd, "build-arg:GIT_VERSION"); got != c.gitVersion {
			t.Errorf("%s: GIT_VERSION = %q, want %q", c.image, got, c.gitVersion)
		}
	}
}

// Every build job is a fresh pod with an empty local cache, so without a shared
// cache each build re-downloads its whole dependency set (for studio: the entire
// torch stack plus requirements, on every push).
//
// The cache lives in the OBJECT STORE, not the registry. A registry cache is
// pulled whole onto the node before any of it can be read and pushed back after,
// so its size lands on the same 105GB disk that holds the images, the snapshots
// and the build's working set — which is what filled the runner pool and evicted
// builds fifteen minutes in. S3 is read ranged and per-blob, so the node holds
// the working set and nothing else.
func TestBuildFrontendCmdCarriesASharedCache(t *testing.T) {
	join := func(cmd []any) string {
		out := ""
		for _, a := range cmd {
			out += " " + a.(string)
		}
		return out
	}
	got := join(buildFrontendCmd("ctx", "Dockerfile", "ghcr.io/hanzoai/studio:v1.2.3"))
	for _, want := range []string{
		"--import-cache type=registry,ref=ghcr.io/hanzoai/studio:buildcache",
		"--export-cache type=registry,ref=ghcr.io/hanzoai/studio:buildcache,mode=min,compression=zstd",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("build command missing %q\ngot:%s", want, got)
		}
	}
	// A digest-pinned ref names no tag to hang a cache off — no cache flags, no crash.
	if d := join(buildFrontendCmd("ctx", "Dockerfile", "ghcr.io/hanzoai/studio@sha256:abc")); strings.Contains(d, "-cache") {
		t.Errorf("digest ref must carry no cache flags, got:%s", d)
	}
}

// The layer compression is three settings that only work together, and each one
// fails QUIETLY on its own — which is why they are asserted rather than trusted.
// Measured on cloud's 1.63GB layer: gzip 245.5s/256.1s against zstd 58.1s/53.3s.
//
//   - compression=zstd alone: buildkit reuses any blob it already has in the
//     requested-or-not format, so it can publish gzip layers having done no work,
//     and the only symptom is that the build is as slow as it was before.
//   - without oci-mediatypes: a zstd layer has no Docker schema2 media type to be
//     named by, so the manifest cannot describe what was pushed.
//   - and the cache export must agree with the image, or mode=min re-compresses
//     the very layers the image export just wrote and hands the saving back.
func TestTheImageAndItsCacheAgreeOnCompression(t *testing.T) {
	got := ""
	for _, a := range buildFrontendCmd("ctx", "Dockerfile", "ghcr.io/hanzoai/studio:v1.2.3") {
		got += " " + a.(string)
	}
	for _, want := range []string{
		"compression=zstd",
		"force-compression=true",
		"oci-mediatypes=true",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("image output missing %q — the layer export silently falls back to single-core gzip\ngot:%s", want, got)
		}
	}
	if !strings.Contains(got, "--export-cache") || !strings.Contains(got, "mode=min,compression=zstd") {
		t.Errorf("the cache export must be min and must match the image's compression\ngot:%s", got)
	}
}
