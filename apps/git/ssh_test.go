package git

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"github.com/zap-proto/zip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"golang.org/x/crypto/ssh"
)

// sshKeypair makes a key, writes its private half where the git CLI can read it,
// and answers the public line.
func sshKeypair(t *testing.T, dir, name string) (priv, pub string) {
	t.Helper()
	pk, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	blk, err := ssh.MarshalPrivateKey(sk, "")
	if err != nil {
		t.Fatal(err)
	}
	priv = filepath.Join(dir, name)
	if err := os.WriteFile(priv, pem.EncodeToMemory(blk), 0o600); err != nil {
		t.Fatal(err)
	}
	spk, err := ssh.NewPublicKey(pk)
	if err != nil {
		t.Fatal(err)
	}
	return priv, strings.TrimSpace(string(ssh.MarshalAuthorizedKey(spk)))
}

func runGit(t *testing.T, dir, key, port string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_SSH_COMMAND=ssh -i "+key+" -p "+port+" -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o BatchMode=yes",
		"GIT_TERMINAL_PROMPT=0", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestKeyRegistry: keys register, list, refuse a second registration of the same
// key anywhere, and are removed by their owner or an admin only.
func TestKeyRegistry(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		pushTo(t, app, "acme", "code", "main", "README.md", "hi")
		_, pub := sshKeypair(t, t.TempDir(), "k")

		code, b := do(t, app, http.MethodPost, "/v1/git/keys", "acme", map[string]any{"title": "laptop", "key": pub})
		if code != http.StatusCreated {
			t.Fatalf("add: %d %s", code, b)
		}
		var v keyView
		_ = json.Unmarshal(b, &v)
		if v.Kind != kindUser || !strings.HasPrefix(v.Fingerprint, "SHA256:") || v.Creator != "u_acme" {
			t.Fatalf("view: %+v", v)
		}
		if code, _ = do(t, app, http.MethodPost, "/v1/git/keys", "acme", map[string]any{"title": "again", "key": pub}); code != http.StatusConflict {
			t.Fatalf("same key twice: %d", code)
		}
		if code, _ = do(t, app, http.MethodPost, "/v1/git/keys", "other", map[string]any{"title": "theirs", "key": pub}); code != http.StatusConflict {
			t.Fatalf("same key in another org: %d", code)
		}
		for _, bad := range []string{"not a key", "command=\"x\" " + pub, pub + "\n" + pub} {
			if code, _ = do(t, app, http.MethodPost, "/v1/git/keys", "acme", map[string]any{"title": "x", "key": bad}); code != http.StatusBadRequest {
				t.Fatalf("bad key %q: %d", bad, code)
			}
		}
		_, b = do(t, app, http.MethodGet, "/v1/git/keys", "acme", nil)
		var l keyList
		_ = json.Unmarshal(b, &l)
		if len(l.Data) != 1 {
			t.Fatalf("list: %s", b)
		}
		_, b = do(t, app, http.MethodGet, "/v1/git/keys", "other", nil)
		_ = json.Unmarshal(b, &l)
		if len(l.Data) != 0 {
			t.Fatalf("another org lists %s", b)
		}
		if code, _ = do(t, app, http.MethodDelete, "/v1/git/keys/"+v.ID, "other", nil); code != http.StatusNotFound {
			t.Fatalf("another org deletes: %d", code)
		}
		if code, _ = do(t, app, http.MethodDelete, "/v1/git/keys/"+v.ID, "acme", nil); code != http.StatusNoContent {
			t.Fatalf("delete: %d", code)
		}
		// Deploy keys are bound to a repository that exists in the caller's scope.
		_, pub2 := sshKeypair(t, t.TempDir(), "k2")
		if code, _ = postAs(t, app, "/v1/git/keys", "acme", orgAdmin, map[string]any{"title": "d", "key": pub2, "repo": "nope"}); code != http.StatusNotFound {
			t.Fatalf("deploy key for a missing repo: %d", code)
		}
	})
}

// TestSSHCloneAndPush drives the real git CLI over SSH against the listener: a
// user key clones, pushes and fetches; a read-only deploy key fetches and is
// refused a push; a key of another org and an unknown key reach nothing.
func TestSSHCloneAndPush(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh")
	}
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app := mountApp(t)
		pushTo(t, app, "acme", "code", "main", "README.md", "hello")
		pem, err := newHostKey()
		if err != nil {
			t.Fatal(err)
		}
		host, err := ssh.ParsePrivateKey(pem)
		if err != nil {
			t.Fatal(err)
		}
		srv, err := startSSH(mounted.Load(), "127.0.0.1:0", host)
		if err != nil {
			t.Fatal(err)
		}
		defer srv.stop()
		port := srv.addr()[strings.LastIndex(srv.addr(), ":")+1:]

		keys := t.TempDir()
		userPriv, userPub := sshKeypair(t, keys, "user")
		roPriv, roPub := sshKeypair(t, keys, "ro")
		strangerPriv, _ := sshKeypair(t, keys, "stranger")
		otherPriv, otherPub := sshKeypair(t, keys, "other")
		for _, k := range []map[string]any{
			{"title": "user", "key": userPub},
			{"title": "ro", "key": roPub, "repo": "code", "readOnly": true},
		} {
			if code, b := postAs(t, app, "/v1/git/keys", "acme", orgAdmin, k); code != http.StatusCreated {
				t.Fatalf("register: %d %s", code, b)
			}
		}
		if code, b := do(t, app, http.MethodPost, "/v1/git/keys", "other", map[string]any{"title": "o", "key": otherPub}); code != http.StatusCreated {
			t.Fatalf("register other: %d %s", code, b)
		}

		work := t.TempDir()
		remote := "ssh://git@127.0.0.1:" + port + "/acme/code.git"
		if out, err := runGit(t, work, userPriv, port, "clone", "-q", remote, "a"); err != nil {
			t.Fatalf("clone: %v\n%s", err, out)
		}
		a := filepath.Join(work, "a")
		if b, err := os.ReadFile(filepath.Join(a, "README.md")); err != nil || string(b) != "hello" {
			t.Fatalf("cloned README: %q %v", b, err)
		}
		if err := os.WriteFile(filepath.Join(a, "new.txt"), []byte("pushed over ssh"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, args := range [][]string{{"add", "."}, {"commit", "-q", "-m", "over ssh"}} {
			if out, err := runGit(t, a, userPriv, port, args...); err != nil {
				t.Fatalf("%v: %v\n%s", args, err, out)
			}
		}
		if out, err := runGit(t, a, userPriv, port, "push", "-q", "origin", "HEAD:refs/heads/main"); err != nil {
			t.Fatalf("push: %v\n%s", err, out)
		}
		// The push is the same fact a smart-HTTP push is: the ref moved in the store.
		head, _ := runGit(t, a, userPriv, port, "rev-parse", "HEAD")
		if got := branchAt(t, "acme", "code", "main"); got != strings.TrimSpace(head) {
			t.Fatalf("main is %s after the push, want %s", got, strings.TrimSpace(head))
		}
		// A second clone with the read-only deploy key sees it; its push is refused.
		if out, err := runGit(t, work, roPriv, port, "clone", "-q", remote, "b"); err != nil {
			t.Fatalf("deploy clone: %v\n%s", err, out)
		}
		if _, err := os.Stat(filepath.Join(work, "b", "new.txt")); err != nil {
			t.Fatalf("deploy clone missed the push: %v", err)
		}
		b := filepath.Join(work, "b")
		_ = os.WriteFile(filepath.Join(b, "x.txt"), []byte("x"), 0o644)
		runGit(t, b, roPriv, port, "add", ".")
		runGit(t, b, roPriv, port, "commit", "-q", "-m", "x")
		if out, err := runGit(t, b, roPriv, port, "push", "origin", "HEAD:refs/heads/side"); err == nil {
			t.Fatalf("read-only deploy key pushed:\n%s", out)
		}
		if got := branchAt(t, "acme", "code", "side"); got != "" {
			t.Fatalf("refused push created a branch: %s", got)
		}
		// Another org's key, and a key nobody registered, reach nothing.
		if out, err := runGit(t, work, otherPriv, port, "clone", "-q", remote, "c"); err == nil {
			t.Fatalf("another org cloned:\n%s", out)
		}
		if out, err := runGit(t, work, strangerPriv, port, "clone", "-q", remote, "d"); err == nil {
			t.Fatalf("unknown key cloned:\n%s", out)
		}
		// Removing a key ends its access.
		_, lb := do(t, app, http.MethodGet, "/v1/git/keys", "acme", nil)
		var l keyList
		_ = json.Unmarshal(lb, &l)
		for _, k := range l.Data {
			if k.Kind == kindUser {
				do(t, app, http.MethodDelete, "/v1/git/keys/"+k.ID, "acme", nil)
			}
		}
		if out, err := runGit(t, work, userPriv, port, "clone", "-q", remote, "e"); err == nil {
			t.Fatalf("removed key cloned:\n%s", out)
		}
	})
}

func TestParseExec(t *testing.T) {
	for cmd, want := range map[string]target{
		"git-upload-pack '/acme/code.git'":    {svcUploadPack, "acme", "", "code"},
		"git-receive-pack 'acme/p1/code.git'": {svcReceivePack, "acme", "p1", "code"},
	} {
		if got, err := parseExec(cmd); err != nil || got != want {
			t.Errorf("%q = %+v %v, want %+v", cmd, got, err, want)
		}
	}
	for _, cmd := range []string{"ls", "git-upload-pack", "git-upload-pack 'a'", "git-upload-pack 'a/b/c/d'", "git-upload-pack '../x/y'", "scp -t /x"} {
		if _, err := parseExec(cmd); err == nil {
			t.Errorf("%q accepted", cmd)
		}
	}
}

// postAs posts a JSON body with extra identity headers.
func postAs(t *testing.T, app *zip.App, path, org string, head map[string]string, body any) (int, []byte) {
	t.Helper()
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u_"+org)
	for k, v := range head {
		req.Header.Set(k, v)
	}
	resp, err := app.Test(req, testCfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}
