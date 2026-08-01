package main

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
)

// TestResolveVersionLadder walks every rung. The one that answers depends on
// the toolchain that built the binary, so each is pinned here rather than left
// to whichever happens to fire on the box that runs the suite.
func TestResolveVersionLadder(t *testing.T) {
	info := func(main string, settings ...debug.BuildSetting) func() (*debug.BuildInfo, bool) {
		return func() (*debug.BuildInfo, bool) {
			return &debug.BuildInfo{Main: debug.Module{Version: main}, Settings: settings}, true
		}
	}
	sha := "4d269626af4ac98f929f9d3fb3d3d681ff02c3a6"
	rev := debug.BuildSetting{Key: "vcs.revision", Value: sha}
	clean := debug.BuildSetting{Key: "vcs.modified", Value: "false"}
	dirty := debug.BuildSetting{Key: "vcs.modified", Value: "true"}

	for _, tc := range []struct {
		name  string
		stamp string
		build func() (*debug.BuildInfo, bool)
		want  string
	}{
		{"stamped tag wins outright", "v1.234.5", info("v9.9.9"), "v1.234.5"},
		{"toolchain module version", "dev", info("v1.801.351-0.20260801181131-4d269626af4a"), "v1.801.351-0.20260801181131-4d269626af4a"},
		{"vcs stamp when the module is (devel)", "dev", info("(devel)", rev, clean), "0.0.0-dev+4d269626af4a"},
		{"vcs stamp says so when the tree is not committed", "dev", info("(devel)", rev, dirty), "0.0.0-dev+4d269626af4a-dirty"},
		{"nothing known is the only dev left", "dev", info(""), "dev"},
		{"no build info at all", "dev", func() (*debug.BuildInfo, bool) { return nil, false }, "dev"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func(v string, r func() (*debug.BuildInfo, bool)) { version, readBuildInfo = v, r }(version, readBuildInfo)
			version, readBuildInfo = tc.stamp, tc.build
			if got := resolveVersion(); got != tc.want {
				t.Fatalf("resolveVersion()=%q want %q", got, tc.want)
			}
		})
	}
}

// TestBinaryNamesItsCommit is THE regression. `hanzo --version` printed "dev"
// on every build ever made, including the installed one, because the ldflag it
// documented was wired nowhere. So build it the way that shipped — plain `go
// build`, no ldflags — and require the binary to name the commit it is anyway:
// both surviving rungs (the pseudo-version the toolchain records, and the
// vcs.revision synthesis) carry the same 12-char sha, which is what makes one
// assertion cover both. Stdout is checked alone, and must be ONE line.
func TestBinaryNamesItsCommit(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "hanzo")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}

	meta, err := exec.Command("go", "version", "-m", bin).Output()
	if err != nil {
		t.Fatalf("go version -m: %v", err)
	}
	rev := ""
	for _, l := range strings.Split(string(meta), "\n") {
		if _, v, ok := strings.Cut(l, "vcs.revision="); ok {
			rev = strings.TrimSpace(v)
		}
	}
	if len(rev) < 12 {
		t.Skip("toolchain embedded no vcs.revision — nothing for the binary to name")
	}

	// PATH holds only an empty dir, so no delegate resolves and the run cannot
	// depend on what happens to be installed on the box.
	run := exec.Command(bin, "--version")
	run.Env = []string{"PATH=" + dir, "HOME=" + dir}
	var stdout bytes.Buffer
	run.Stdout = &stdout
	if err := run.Run(); err != nil {
		t.Fatalf("hanzo --version: %v", err)
	}

	got := stdout.String()
	if lines := strings.Count(got, "\n"); lines != 1 {
		t.Errorf("stdout is %d lines, want exactly 1:\n%s", lines, got)
	}
	if !strings.Contains(got, rev[:12]) {
		t.Errorf("hanzo --version = %q; want it to name commit %s", strings.TrimSpace(got), rev[:12])
	}
}
