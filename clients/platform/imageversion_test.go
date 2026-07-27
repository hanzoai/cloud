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
