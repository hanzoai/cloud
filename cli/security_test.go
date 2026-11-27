package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/apps/security/detect"
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

func gitExec(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Test",
		"GIT_AUTHOR_EMAIL=test@hanzo.ai",
		"GIT_COMMITTER_NAME=Test",
		"GIT_COMMITTER_EMAIL=test@hanzo.ai",
		"GIT_AUTHOR_DATE=2026-11-22T12:00:00Z",
		"GIT_COMMITTER_DATE=2026-11-22T12:00:00Z",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestSecurityScanGitHistoryAndIgnoreFile proves `hanzo security scan --git --since-commit <init> --branch HEAD`
// catches a secret committed in Commit 1 and deleted in Commit 2 (invisible to working-tree scan),
// stamps the exact Commit SHA + masked preview + SHA-256 fingerprint without ever printing the raw secret,
// handles all-zero --since-commit, emits GitHub Actions ::error annotations, and honors .hanzo-security-ignore.
func TestSecurityScanGitHistoryAndIgnoreFile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	gitExec(t, repo, "init", "-b", "main")
	must(t, os.WriteFile(filepath.Join(repo, "README.md"), []byte("# test 2026-11-22\n"), 0o644))
	gitExec(t, repo, "add", "README.md")
	gitExec(t, repo, "commit", "-m", "init")
	initSHA := gitExec(t, repo, "rev-parse", "HEAD")

	// Commit 1: commit a fake Hanzo API secret.
	leakedSecret := "hk-" + "LiveTestSecretKey20261122AbCdEfGh"
	must(t, os.WriteFile(filepath.Join(repo, "deploy.py"), []byte("TOKEN = \""+leakedSecret+"\"\n"), 0o644))
	gitExec(t, repo, "add", "deploy.py")
	gitExec(t, repo, "commit", "-m", "oops commit secret")
	leakCommitSHA := gitExec(t, repo, "rev-parse", "HEAD")

	// Commit 2: delete the secret from working tree.
	must(t, os.WriteFile(filepath.Join(repo, "deploy.py"), []byte("TOKEN = os.environ['TOKEN']\n"), 0o644))
	gitExec(t, repo, "add", "deploy.py")
	gitExec(t, repo, "commit", "-m", "remove secret from head")

	// Working-tree scan is clean (secret was deleted in Commit 2).
	if out, err := runSecurity(t, "scan", repo); err != nil {
		t.Fatalf("working tree should be clean after deletion, got %v:\n%s", err, out)
	}

	// Git history scan (--since-commit <init> --branch HEAD) catches Commit 1!
	out, err := runSecurity(t, "scan", "-o", "json", "--git", "--since-commit", initSHA, "--branch", "HEAD", "--fail-on", "none", repo)
	if err != nil {
		t.Fatalf("git scan json failed: %v\n%s", err, out)
	}
	if strings.Contains(out, leakedSecret) {
		t.Fatalf("git scan leaked raw secret:\n%s", out)
	}
	var res scanResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode git scan json: %v\n%s", err, out)
	}
	if res.CommitsScanned != 2 || len(res.Findings) != 1 {
		t.Fatalf("expected 2 commits scanned and 1 historical finding, got commits=%d findings=%+v", res.CommitsScanned, res.Findings)
	}
	got := res.Findings[0]
	wantFP := detect.Fingerprint(leakedSecret)
	if got.RuleID != "hanzo-api-key" || got.Commit != leakCommitSHA || got.Fingerprint != wantFP || got.Line != 1 {
		t.Fatalf("unexpected git finding: %+v (want commit=%s fp=%s)", got, leakCommitSHA, wantFP)
	}

	// All-zero --since-commit (GitHub Actions new-branch push) + --github-actions annotations.
	gaOut, gaErr := runSecurity(t, "scan", "--git", "--since-commit", strings.Repeat("0", 40), "--branch", "HEAD", "--github-actions", repo)
	if gaErr == nil {
		t.Fatalf("expected non-zero policy exit on historical leak; out:\n%s", gaOut)
	}
	if strings.Contains(gaOut, leakedSecret) {
		t.Fatalf("github-actions annotation leaked raw secret:\n%s", gaOut)
	}
	if !strings.Contains(gaOut, "::error file=deploy.py,line=1,title=Hanzo Security (hanzo-api-key)::") || !strings.Contains(gaOut, leakCommitSHA) {
		t.Fatalf("missing GitHub Actions annotation in output:\n%s", gaOut)
	}

	// Allowlisting by SHA-256 fingerprint in .hanzo-security-ignore suppresses the triaged fixture cleanly.
	ignoreContent := wantFP + " # rotated & triaged 2026-11-22\n"
	must(t, os.WriteFile(filepath.Join(repo, ".hanzo-security-ignore"), []byte(ignoreContent), 0o644))
	cleanOut, cleanErr := runSecurity(t, "scan", "--git", "--since-commit", initSHA, "--branch", "HEAD", repo)
	if cleanErr != nil {
		t.Fatalf("allowlisted fingerprint should pass cleanly, got %v:\n%s", cleanErr, cleanOut)
	}
}

func TestSecurityScanVerifyAndInsights(t *testing.T) {
	var capturedBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/iam/userinfo":
			if strings.Contains(r.Header.Get("Authorization"), "LIVEKEY") {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"sub":"u1"}`))
				return
			}
			w.WriteHeader(http.StatusUnauthorized)
		case "/v1/capture":
			capturedBody, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":1}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	origHanzo := verifyEndpoints.hanzo
	verifyEndpoints.hanzo = srv.URL + "/v1/iam/userinfo"
	t.Cleanup(func() { verifyEndpoints.hanzo = origHanzo })

	t.Setenv("HANZO_INSIGHTS_HOST", srv.URL)
	t.Setenv("HANZO_INSIGHTS_API_KEY", "phc_test_insights_20261122")

	dir := t.TempDir()
	liveSecret := "hk-" + "LIVEKEY20261122ValidTokenAbCdEf"
	deadSecret := "hk-" + "DEADKEY20261122RevokedTokenXyZw"
	must(t, os.WriteFile(filepath.Join(dir, "keys.env"),
		[]byte("A="+liveSecret+"\nB="+deadSecret+"\n"), 0o644))

	out, err := runSecurity(t, "scan", "-o", "json", "--only-verified", "--insights", "--fail-on", "none", dir)
	if err != nil {
		t.Fatalf("verified scan failed: %v\n%s", err, out)
	}
	if strings.Contains(out, liveSecret) || strings.Contains(out, deadSecret) {
		t.Fatalf("scan output leaked raw secret:\n%s", out)
	}
	var res scanResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("decode json: %v\n%s", err, out)
	}
	if len(res.Findings) != 1 || res.Summary.Verified != 1 || res.Findings[0].Fingerprint != detect.Fingerprint(liveSecret) {
		t.Fatalf("expected exactly 1 verified finding, got %+v", res)
	}
	if len(capturedBody) == 0 || strings.Contains(string(capturedBody), liveSecret) || !strings.Contains(string(capturedBody), `"security.scan.completed"`) {
		t.Fatalf("unexpected Insights capture payload: %s", capturedBody)
	}
}
