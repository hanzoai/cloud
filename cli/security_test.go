package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runSecurity executes `security <args>` through the real root command with an
// isolated $HOME, capturing stdout. Returns output + the Execute error (the
// non-zero-exit signal).
func runSecurity(t *testing.T, args ...string) (string, error) {
	t.Helper()
	sandbox(t)
	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(append([]string{"security"}, args...))
	err := root.Execute()
	return out.String(), err
}

// TestSecurityScanFindsAndFails proves scan detects a planted secret, exits
// non-zero at the default fail threshold, and never prints the raw secret.
func TestSecurityScanFindsAndFails(t *testing.T) {
	dir := t.TempDir()
	secret := "AKIAIOSFODNN7EXAMPLE"
	must(t, os.WriteFile(filepath.Join(dir, "config.py"),
		[]byte("aws_key = \""+secret+"\"\nok = 1\n"), 0o644))
	// a clean file that must NOT trip anything
	must(t, os.WriteFile(filepath.Join(dir, "clean.go"),
		[]byte("package main\nfunc main() {}\n"), 0o644))

	out, err := runSecurity(t, "scan", dir)
	if err == nil {
		t.Fatalf("expected non-zero exit on a found secret; out:\n%s", out)
	}
	if !strings.Contains(err.Error(), "at or above") {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("output leaked the raw secret:\n%s", out)
	}
	if !strings.Contains(out, "aws-access-key-id") {
		t.Fatalf("expected the aws rule id in output:\n%s", out)
	}
}

// TestSecurityScanCleanPasses proves a clean tree exits zero with the ok line.
func TestSecurityScanCleanPasses(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "app.go"),
		[]byte("package main\nfunc main() { println(\"hi\") }\n"), 0o644))

	out, err := runSecurity(t, "scan", dir)
	if err != nil {
		t.Fatalf("clean tree should exit zero, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "no secrets found") {
		t.Fatalf("expected the clean message:\n%s", out)
	}
}

// TestSecurityScanFailOnNone proves --fail-on=none reports findings but exits
// zero (report-only mode).
func TestSecurityScanFailOnNone(t *testing.T) {
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "s.py"),
		[]byte(`k = "AKIAIOSFODNN7EXAMPLE"`), 0o644))

	out, err := runSecurity(t, "scan", "--fail-on", "none", dir)
	if err != nil {
		t.Fatalf("--fail-on=none should exit zero, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "aws-access-key-id") {
		t.Fatalf("report-only should still list the finding:\n%s", out)
	}
}

// TestSecurityScanFailOnThreshold proves a medium finding does NOT fail when
// --fail-on=critical, but a critical one does.
func TestSecurityScanFailOnThreshold(t *testing.T) {
	dir := t.TempDir()
	// a jwt is medium severity
	must(t, os.WriteFile(filepath.Join(dir, "t.txt"),
		[]byte("tok = eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJhYmMifQ.SGVsbG9TaWduYXR1cmU\n"), 0o644))

	if out, err := runSecurity(t, "scan", "--fail-on", "critical", dir); err != nil {
		t.Fatalf("medium finding must not fail at --fail-on=critical, got %v\n%s", err, out)
	}
	// now add a critical
	must(t, os.WriteFile(filepath.Join(dir, "k.py"),
		[]byte(`k = "AKIAIOSFODNN7EXAMPLE"`), 0o644))
	if _, err := runSecurity(t, "scan", "--fail-on", "critical", dir); err == nil {
		t.Fatal("a critical finding must fail at --fail-on=critical")
	}
}

// TestSecurityScanJSON proves -o json emits a machine-readable result with the
// summary and no raw secret.
func TestSecurityScanJSON(t *testing.T) {
	dir := t.TempDir()
	secret := "AKIAIOSFODNN7EXAMPLE"
	must(t, os.WriteFile(filepath.Join(dir, "s.py"), []byte(`k = "`+secret+`"`), 0o644))

	out, _ := runSecurity(t, "scan", "-o", "json", "--fail-on", "none", dir)
	var res scanResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("json parse: %v\n%s", err, out)
	}
	if res.Summary.Critical < 1 || res.Summary.Total < 1 {
		t.Fatalf("summary missing the critical: %+v", res.Summary)
	}
	if strings.Contains(out, secret) {
		t.Fatalf("json leaked the secret:\n%s", out)
	}
}

// TestSecurityScanSkipsVendorAndBinary proves the walker skips skipDirs and
// binary files (a secret inside node_modules or a NUL-laden blob is ignored).
func TestSecurityScanSkipsVendorAndBinary(t *testing.T) {
	dir := t.TempDir()
	nm := filepath.Join(dir, "node_modules")
	must(t, os.MkdirAll(nm, 0o755))
	must(t, os.WriteFile(filepath.Join(nm, "dep.js"),
		[]byte(`k = "AKIAIOSFODNN7EXAMPLE"`), 0o644))
	must(t, os.WriteFile(filepath.Join(dir, "blob.bin"),
		append([]byte{0, 1, 2}, []byte(`AKIAIOSFODNN7EXAMPLE`)...), 0o644))

	out, err := runSecurity(t, "scan", dir)
	if err != nil {
		t.Fatalf("vendored + binary secrets should be skipped → clean exit, got %v\n%s", err, out)
	}
	if !strings.Contains(out, "no secrets found") {
		t.Fatalf("expected clean (skipped) result:\n%s", out)
	}
}

// TestSecurityScanBadFailOn proves an invalid --fail-on is a clean error.
func TestSecurityScanBadFailOn(t *testing.T) {
	dir := t.TempDir()
	_, err := runSecurity(t, "scan", "--fail-on", "nope", dir)
	if err == nil || !strings.Contains(err.Error(), "invalid --fail-on") {
		t.Fatalf("want invalid --fail-on error, got %v", err)
	}
}

// TestSecurityRules proves the rules subcommand lists the catalog.
func TestSecurityRules(t *testing.T) {
	out, err := runSecurity(t, "rules")
	if err != nil {
		t.Fatalf("rules failed: %v", err)
	}
	if !strings.Contains(out, "aws-access-key-id") || !strings.Contains(out, "rules") {
		t.Fatalf("rules output missing content:\n%s", out)
	}
}

// TestSecurityIsControlVerb proves `security` routes to the CLI, not the server
// dispatcher.
func TestSecurityIsControlVerb(t *testing.T) {
	if !IsControlVerb("security") {
		t.Fatal("security must be a control verb")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
