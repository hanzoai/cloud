package git

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	gogit "github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/object"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"
	xssh "golang.org/x/crypto/ssh"
)

// genClientKey returns a fresh ed25519 keypair as (privatePEM, authorizedKeyLine).
// The private PEM feeds the go-git SSH client; the authorized-key line is what a
// user registers via POST /v1/git/keys.
func genClientKey(t *testing.T) (privPEM []byte, authLine string) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	block, err := xssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal priv: %v", err)
	}
	pub, err := xssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("pub: %v", err)
	}
	return pem.EncodeToMemory(block), strings.TrimSpace(string(xssh.MarshalAuthorizedKey(pub)))
}

// sshClientAuth builds a go-git SSH auth method from a client private PEM,
// ignoring the host key (the in-process listener's host key is ephemeral).
func sshClientAuth(t *testing.T, privPEM []byte) *gitssh.PublicKeys {
	t.Helper()
	auth, err := gitssh.NewPublicKeys("git", privPEM, "")
	if err != nil {
		t.Fatalf("client auth: %v", err)
	}
	auth.HostKeyCallback = xssh.InsecureIgnoreHostKey()
	return auth
}

// TestSSHKeyRegistrationAndAuth proves the fingerprint→tenant resolution used by
// the PublicKeyCallback: a registered key resolves to its org; an unregistered
// key fails closed; and a key belongs to exactly one org (no cross-tenant).
func TestSSHKeyRegistrationAndAuth(t *testing.T) {
	app := mountApp(t)

	_, authLine := genClientKey(t)

	// Register the key for acme via the control plane.
	code, body := do(t, app, http.MethodPost, "/v1/git/keys", "acme",
		map[string]any{"title": "laptop", "publicKey": authLine})
	if code != http.StatusCreated {
		t.Fatalf("register key want 201, got %d (%s)", code, body)
	}
	var kv keyView
	if err := json.Unmarshal(body, &kv); err != nil {
		t.Fatalf("key view json: %v (%s)", err, body)
	}
	if kv.Fingerprint == "" || !strings.HasPrefix(kv.Fingerprint, "SHA256:") {
		t.Fatalf("unexpected fingerprint: %q", kv.Fingerprint)
	}

	// The registered key resolves to acme via the auth callback path.
	pub, _, _, _, err := xssh.ParseAuthorizedKey([]byte(authLine))
	if err != nil {
		t.Fatalf("parse authline: %v", err)
	}
	perms, err := mounted.ssh.authPublicKey(fakeConnMeta{}, pub)
	if err != nil {
		t.Fatalf("registered key must authenticate, got: %v", err)
	}
	if perms.Extensions["git-org"] != "acme" {
		t.Fatalf("resolved org = %q, want acme", perms.Extensions["git-org"])
	}

	// An UNREGISTERED key fails closed.
	otherPub, _ := genClientKeyPub(t)
	if _, err := mounted.ssh.authPublicKey(fakeConnMeta{}, otherPub); err == nil {
		t.Fatalf("unregistered key must be rejected")
	}

	// The SAME key cannot be re-registered under a DIFFERENT org (fingerprint is
	// globally unique — a key belongs to exactly one tenant).
	if code, _ := do(t, app, http.MethodPost, "/v1/git/keys", "beta",
		map[string]any{"title": "steal", "publicKey": authLine}); code != http.StatusConflict {
		t.Fatalf("cross-org re-register want 409, got %d", code)
	}

	// beta lists ZERO keys — never acme's.
	code, body = do(t, app, http.MethodGet, "/v1/git/keys", "beta", nil)
	if code != http.StatusOK {
		t.Fatalf("beta list keys want 200, got %d", code)
	}
	var lst struct {
		Data []keyView `json:"data"`
	}
	_ = json.Unmarshal(body, &lst)
	if len(lst.Data) != 0 {
		t.Fatalf("beta must see zero keys, got %+v", lst.Data)
	}

	// Delete the key, then it no longer authenticates (fail closed again).
	if code, _ := do(t, app, http.MethodDelete, "/v1/git/keys/"+kv.ID, "acme", nil); code != http.StatusNoContent {
		t.Fatalf("delete key want 204, got %d", code)
	}
	if _, err := mounted.ssh.authPublicKey(fakeConnMeta{}, pub); err == nil {
		t.Fatalf("deleted key must be rejected")
	}
}

// TestSSHClonePushRoundTrip is the end-to-end SSH proof: register a client key,
// create a repo, push over SSH, then clone over SSH in a fresh client and SEE the
// pushed commit — all through the in-process SSH listener, driving the SAME pack
// code path smart-HTTP uses.
func TestSSHClonePushRoundTrip(t *testing.T) {
	app := mountApp(t)

	privPEM, authLine := genClientKey(t)
	if code, body := do(t, app, http.MethodPost, "/v1/git/keys", "acme",
		map[string]any{"title": "ci", "publicKey": authLine}); code != http.StatusCreated {
		t.Fatalf("register key: %d %s", code, body)
	}
	if code, body := do(t, app, http.MethodPost, "/v1/git/repos", "acme",
		map[string]any{"name": "code"}); code != http.StatusCreated {
		t.Fatalf("create repo: %d %s", code, body)
	}

	sshURL := fmt.Sprintf("ssh://git@%s/acme/code.git", mounted.ssh.addr())
	auth := sshClientAuth(t, privPEM)

	// Build a local repo, commit, and push over SSH.
	fs := memfs.New()
	local, err := gogit.Init(memory.NewStorage(), fs)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	wt, _ := local.Worktree()
	f, _ := fs.Create("README.md")
	_, _ = f.Write([]byte("# ssh native\n"))
	_ = f.Close()
	if _, err := wt.Add("README.md"); err != nil {
		t.Fatalf("add: %v", err)
	}
	commit, err := wt.Commit("via ssh", &gogit.CommitOptions{
		Author: &object.Signature{Name: "hanzo-dev", Email: "dev@hanzo.ai", When: time.Now()},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := local.CreateRemote(&config.RemoteConfig{Name: "origin", URLs: []string{sshURL}}); err != nil {
		t.Fatalf("remote: %v", err)
	}
	if err := local.Push(&gogit.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []config.RefSpec{"refs/heads/master:refs/heads/main"},
		Auth:       auth,
	}); err != nil {
		t.Fatalf("ssh push: %v", err)
	}

	// Fresh clone over SSH — the pushed commit must be there.
	cloned, err := gogit.Clone(memory.NewStorage(), memfs.New(), &gogit.CloneOptions{URL: sshURL, Auth: auth})
	if err != nil {
		t.Fatalf("ssh clone: %v", err)
	}
	head, err := cloned.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head.Hash() != commit {
		t.Fatalf("cloned HEAD %s != pushed %s", head.Hash(), commit)
	}
}

// TestSSHCrossTenantRejected proves a key bound to acme cannot reach beta's
// namespace even when it crafts a beta path — the SSH tenancy guard.
func TestSSHCrossTenantRejected(t *testing.T) {
	app := mountApp(t)

	privPEM, authLine := genClientKey(t)
	if code, _ := do(t, app, http.MethodPost, "/v1/git/keys", "acme",
		map[string]any{"publicKey": authLine}); code != http.StatusCreated {
		t.Fatal("register acme key failed")
	}
	// beta owns a repo the acme key must NOT reach.
	if code, _ := do(t, app, http.MethodPost, "/v1/git/repos", "beta",
		map[string]any{"name": "secret"}); code != http.StatusCreated {
		t.Fatal("create beta repo failed")
	}

	sshURL := fmt.Sprintf("ssh://git@%s/beta/secret.git", mounted.ssh.addr())
	auth := sshClientAuth(t, privPEM)
	// acme's key authenticates, but the beta path is outside its org → the exec
	// handler denies and the clone fails.
	_, err := gogit.Clone(memory.NewStorage(), memfs.New(), &gogit.CloneOptions{URL: sshURL, Auth: auth})
	if err == nil {
		t.Fatalf("acme key cloning beta repo must fail")
	}
}

// TestSSHParseRepoPath covers the path parser's accept/reject cases directly.
func TestSSHParseRepoPath(t *testing.T) {
	cases := []struct {
		in, org, repo string
		ok            bool
	}{
		{"acme/code.git", "acme", "code", true},
		{"/acme/code.git", "acme", "code", true},
		{"acme/code", "acme", "code", true},
		{"../etc/passwd", "", "", false},
		{"acme/../beta/x.git", "", "", false},
		{"acme", "", "", false},
		{"a/b/c.git", "", "", false},
	}
	for _, tc := range cases {
		org, repo, err := parseRepoPath(tc.in)
		if tc.ok {
			if err != nil || org != tc.org || repo != tc.repo {
				t.Fatalf("parseRepoPath(%q) = (%q,%q,%v), want (%q,%q,nil)", tc.in, org, repo, err, tc.org, tc.repo)
			}
		} else if err == nil {
			t.Fatalf("parseRepoPath(%q) should have failed, got (%q,%q)", tc.in, org, repo)
		}
	}
}

// --- test doubles ---

// fakeConnMeta is a minimal ssh.ConnMetadata for exercising authPublicKey
// directly (the callback only reads the presented key, not the conn).
type fakeConnMeta struct{}

func (fakeConnMeta) User() string          { return "git" }
func (fakeConnMeta) SessionID() []byte     { return nil }
func (fakeConnMeta) ClientVersion() []byte { return []byte("SSH-2.0-test") }
func (fakeConnMeta) ServerVersion() []byte { return []byte("SSH-2.0-hanzo") }
func (fakeConnMeta) RemoteAddr() net.Addr  { return &net.TCPAddr{} }
func (fakeConnMeta) LocalAddr() net.Addr   { return &net.TCPAddr{} }

// genClientKeyPub returns just an ssh.PublicKey for the unregistered-key case.
func genClientKeyPub(t *testing.T) (xssh.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	pub, err := xssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("pub: %v", err)
	}
	return pub, priv
}
