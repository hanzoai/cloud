package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestEngineCmdWiring asserts `hanzo engine` exposes install/serve/status with the
// documented flags — the contract install.sh and the docs promise.
func TestEngineCmdWiring(t *testing.T) {
	env := &Env{Output: "table"}
	cmd := newEngineCmd(func() *Env { return env }, &globalFlags{})

	want := map[string]bool{"install": false, "serve": false, "status": false}
	for _, sub := range cmd.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("engine subcommand %q missing", name)
		}
	}

	serve, _, _ := cmd.Find([]string{"serve"})
	if serve.Flags().Lookup("model") == nil || serve.Flags().Lookup("port") == nil {
		t.Fatalf("serve must have --model and --port")
	}
	status, _, _ := cmd.Find([]string{"status"})
	if status.Flags().Lookup("url") == nil {
		t.Fatalf("status must have --url")
	}
}

// TestEngineStatusReady probes a stub engine and reports it ready with its models.
func TestEngineStatusReady(t *testing.T) {
	engine := stubEngine(t, "default", "zen-omni-30b")
	defer engine.Close()

	var buf bytes.Buffer
	env := &Env{Output: "table", out: &buf}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	if err := runEngineStatus(cmd, env, engine.URL); err != nil {
		t.Fatalf("status: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ready") || !strings.Contains(out, "zen-omni-30b") {
		t.Fatalf("status output missing ready/model:\n%s", out)
	}
}

// TestEngineStatusUnreachable reports a down engine as unreachable (not an error).
func TestEngineStatusUnreachable(t *testing.T) {
	var buf bytes.Buffer
	env := &Env{Output: "table", out: &buf}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	// A port with nothing listening.
	if err := runEngineStatus(cmd, env, "http://127.0.0.1:1"); err != nil {
		t.Fatalf("status should not error on a down engine: %v", err)
	}
	if !strings.Contains(buf.String(), "unreachable") {
		t.Fatalf("expected 'unreachable', got:\n%s", buf.String())
	}
}

// TestFindEngineBinary finds hanzoai on PATH.
func TestFindEngineBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PATH exe probe differs on Windows")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, engineBinName)
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	got, err := findEngineBinary()
	if err != nil {
		t.Fatalf("findEngineBinary: %v", err)
	}
	if got != bin {
		t.Fatalf("found %q, want %q", got, bin)
	}
}
