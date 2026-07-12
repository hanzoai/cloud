package team

import (
	"fmt"
	"testing"
)

// TestDefaultIsFrontVersion pins the code default to the front SPA's model version
// (0.6.0), so a fresh binary is in sync with the front with no env override.
func TestDefaultIsFrontVersion(t *testing.T) {
	if defaultModelVersion != "0.6.0" {
		t.Fatalf("defaultModelVersion = %q, want 0.6.0", defaultModelVersion)
	}
	if got := modelVersion(); got != "0.6.0" {
		t.Fatalf("modelVersion() = %q, want 0.6.0", got)
	}
}

// TestParseDefault proves the numeric triple the workspace handshake needs matches
// the default string — 0.6.0 → 0/6/0.
func TestParseDefault(t *testing.T) {
	if modelMajor() != 0 || modelMinor() != 6 || modelPatch() != 0 {
		t.Fatalf("parse = %d.%d.%d, want 0.6.0", modelMajor(), modelMinor(), modelPatch())
	}
}

// TestEnvOverride proves MODEL_VERSION overrides both the string and the
// parsed triple — one source, both surfaces track it.
func TestEnvOverride(t *testing.T) {
	t.Setenv("MODEL_VERSION", "1.2.3")
	if got := modelVersion(); got != "1.2.3" {
		t.Fatalf("modelVersion() = %q, want 1.2.3", got)
	}
	if modelMajor() != 1 || modelMinor() != 2 || modelPatch() != 3 {
		t.Fatalf("parse = %d.%d.%d, want 1.2.3", modelMajor(), modelMinor(), modelPatch())
	}
}

// TestNoDrift is the core invariant: the parsed triple always re-serializes to
// modelVersion() — the workspace-model version and the string server version are
// the SAME number, never drifting.
func TestNoDrift(t *testing.T) {
	for _, v := range []string{"0.6.0", "0.7.0", "1.10.5"} {
		t.Setenv("MODEL_VERSION", v)
		if got := fmt.Sprintf("%d.%d.%d", modelMajor(), modelMinor(), modelPatch()); got != v {
			t.Fatalf("triple %q != modelVersion() %q", got, v)
		}
	}
}

// TestMalformedDegrades proves a bad override degrades to 0 components rather than
// panicking.
func TestMalformedDegrades(t *testing.T) {
	t.Setenv("MODEL_VERSION", "not-a-version")
	if modelMajor() != 0 || modelMinor() != 0 || modelPatch() != 0 {
		t.Fatalf("malformed parse = %d.%d.%d, want 0.0.0", modelMajor(), modelMinor(), modelPatch())
	}
}
