package forge

// grant_test.go asserts what a run's credential IS, on the wire.
//
// Every property this package claims about a grant is a property of the requests
// it emits and of the bytes it hands back, so the assertions are about those and
// not about a returned struct: a Grant that names the right repository while
// registering a read-only key, or while handing out a private key that does not
// match the public one it registered, has still failed.

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// repoStub is a forge that answers the repository, deploy-key and pull-request
// routes, recording every request so a test can ask what actually travelled.
type repoStub struct {
	*httptest.Server

	mu     sync.Mutex
	reqs   []*http.Request
	bodies []string

	// repo is what GET /repos/{o}/{r} answers; nil is 404.
	repo *Repo
	// keys is the deploy-key list, and posted records what was registered.
	keys    []map[string]any
	posted  map[string]any
	deleted []string
	nextID  int64
}

func newRepoStub(t *testing.T) *repoStub {
	t.Helper()
	s := &repoStub{nextID: 41, repo: &Repo{
		Name: "api", FullName: "acme/api", Branch: "main",
		SSH: "git@git.test:acme/api.git", Perm: &Perm{Pull: true, Push: true},
	}}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		if r.ContentLength > 0 {
			_, _ = r.Body.Read(body)
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, r.Clone(context.Background()))
		s.bodies = append(s.bodies, string(body))
		s.mu.Unlock()

		switch {
		case strings.HasSuffix(r.URL.Path, "/keys") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(s.keys)
		case strings.HasSuffix(r.URL.Path, "/keys") && r.Method == http.MethodPost:
			var in map[string]any
			_ = json.Unmarshal(body, &in)
			s.mu.Lock()
			s.posted = in
			s.nextID++
			id := s.nextID
			s.mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"id": id})
		case strings.Contains(r.URL.Path, "/keys/") && r.Method == http.MethodDelete:
			s.mu.Lock()
			s.deleted = append(s.deleted, r.URL.Path)
			s.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/repos"):
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "api"})
		default:
			if s.repo == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(s.repo)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *repoStub) client(t *testing.T) *Client {
	t.Helper()
	c, err := New(s.URL, "machine-token-value")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

// sudos returns the Sudo header of every request, "" for a machine call.
func (s *repoStub) sudos() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.reqs))
	for _, r := range s.reqs {
		out = append(out, r.Header.Get("Sudo"))
	}
	return out
}

// pinned seeds the host-key cache so a test of the HTTP paths is not also a test
// of the SSH handshake. TestKnown_LearnsTheHostKeyFromTheForge covers that.
func pinned(t *testing.T, c *Client) {
	t.Helper()
	host := c.host
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "22")
	}
	hostKeys.Lock()
	if hostKeys.m == nil {
		hostKeys.m = map[string]string{}
	}
	hostKeys.m[host] = "git.test ssh-ed25519 AAAAPINNEDHOSTKEY"
	hostKeys.Unlock()
	t.Cleanup(func() {
		hostKeys.Lock()
		delete(hostKeys.m, host)
		hostKeys.Unlock()
	})
}

// A GRANT IS A WRITE KEY ON ONE REPOSITORY, AND THE KEY IT HANDS OUT IS THE ONE
// IT REGISTERED.
//
// The last clause is the one a reader should not take on trust: if the private
// key returned to the run did not correspond to the public key put on the
// repository, every run would fail to push and the failure would look like a
// forge problem.
func TestGrant_RegistersAWriteKeyForExactlyThatRepo(t *testing.T) {
	s := newRepoStub(t)
	c := s.client(t)
	pinned(t, c)

	g, err := c.Machine().Grant(context.Background(), "acme", "api", "sess_abc")
	if err != nil {
		t.Fatalf("Grant: %v", err)
	}

	s.mu.Lock()
	posted := s.posted
	s.mu.Unlock()
	if posted == nil {
		t.Fatal("no key was registered")
	}
	// A RUN PUSHES. read_only true would clone and then fail at the one step the
	// whole run exists to reach.
	if ro, _ := posted["read_only"].(bool); ro {
		t.Fatal("the run's key is read-only and could never push")
	}
	// The title carries the run, so a live credential can be traced back to what
	// is holding it — and so a sweep can tell a run's key from a real deploy key.
	title, _ := posted["title"].(string)
	if !strings.HasPrefix(title, grantTitle) || !strings.Contains(title, "sess_abc") {
		t.Fatalf("the key does not name the run holding it: %q", title)
	}

	// THE KEY MATCHES. Parse the private half we handed out, derive its public
	// half, and compare with what was put on the repository.
	signer, err := ssh.ParsePrivateKey([]byte(g.Key))
	if err != nil {
		t.Fatalf("the run was handed an unusable private key: %v", err)
	}
	if _, ok := signer.PublicKey().(ssh.PublicKey); !ok || signer.PublicKey().Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("want an ed25519 key, got %q", signer.PublicKey().Type())
	}
	registered, _ := posted["key"].(string)
	want := string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	if strings.TrimSpace(registered) != strings.TrimSpace(want) {
		t.Fatalf("the registered public key is not the private key's own:\n registered %q\n derived    %q", registered, want)
	}

	// It addresses ONE repository, at the address the FORGE gave, and it carries
	// the pin the sandbox will verify the host against.
	if g.Owner != "acme" || g.Repo != "api" {
		t.Fatalf("the grant addresses %s/%s", g.Owner, g.Repo)
	}
	if g.Remote != "git@git.test:acme/api.git" {
		t.Fatalf("the remote was re-derived rather than taken from the forge: %q", g.Remote)
	}
	if !strings.Contains(g.Known, "ssh-ed25519") {
		t.Fatalf("the grant carries no host key to pin: %q", g.Known)
	}
	// The machine credential is not among what the run receives.
	if strings.Contains(g.Key+g.Remote+g.Known, "machine-token-value") {
		t.Fatal("the machine token reached the run")
	}
}

// A GRANT IS REFUSED WHEN THE ACTOR MAY NOT PUSH, and nothing is left behind on
// the repository. The forge's own ACL decides, applied to the human.
func TestGrant_RefusedWhenTheActorCannotPush(t *testing.T) {
	s := newRepoStub(t)
	s.repo = &Repo{Name: "api", FullName: "acme/api", Perm: &Perm{Pull: true}} // read only
	c := s.client(t)
	pinned(t, c)

	if _, err := c.As("someone").Grant(context.Background(), "acme", "api", "sess_x"); err == nil {
		t.Fatal("a reader was given a push credential")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.posted != nil {
		t.Fatal("a refused grant still registered a key")
	}
}

// An ARCHIVED repository takes no writes, so it takes no grant either — a run
// that got one would fail at the push having leased a sandbox and burned a
// budget.
func TestGrant_RefusedOnAnArchivedRepo(t *testing.T) {
	s := newRepoStub(t)
	s.repo = &Repo{Name: "api", FullName: "acme/api", Archived: true, Perm: &Perm{Push: true}}
	c := s.client(t)
	pinned(t, c)

	if _, err := c.Machine().Grant(context.Background(), "acme", "api", "sess_x"); err == nil {
		t.Fatal("an archived repository accepted a grant")
	}
}

// AN UNSCOPED CLIENT REFUSES. It is the whole point of As(): a call that forgot
// to say who it acts for must not quietly become the machine, which can do
// anything on this forge.
func TestGrant_UnscopedRefusesRatherThanActingAsTheMachine(t *testing.T) {
	s := newRepoStub(t)
	c := s.client(t)
	pinned(t, c)

	if _, err := c.Grant(context.Background(), "acme", "api", "sess_x"); err == nil {
		t.Fatal("an unscoped client minted a credential on the machine's authority")
	}
	if len(s.sudos()) != 0 {
		t.Fatal("an unscoped call reached the forge")
	}
}

// Machine() sends NO Sudo; As() sends the actor. The forge's ACL is applied to
// whoever the header names, so getting this wrong is a permissions bug rather
// than a cosmetic one.
func TestMachineAndAsAreDistinguishableOnTheWire(t *testing.T) {
	s := newRepoStub(t)
	c := s.client(t)

	if _, err := c.Machine().Repo(context.Background(), "acme", "api"); err != nil {
		t.Fatalf("machine read: %v", err)
	}
	if _, err := c.As("zoe").Repo(context.Background(), "acme", "api"); err != nil {
		t.Fatalf("sudoed read: %v", err)
	}
	got := s.sudos()
	if len(got) != 2 || got[0] != "" || got[1] != "zoe" {
		t.Fatalf("Sudo headers were %q, want [\"\" \"zoe\"]", got)
	}
}

// REVOKE DELETES THE ONE KEY, AND IS IDEMPOTENT. A run that already lost its
// credential must not fail trying to give it back twice, and a key id that is
// gone is the state the caller wanted.
func TestRevoke_DeletesTheKeyAndToleratesAnAbsentOne(t *testing.T) {
	s := newRepoStub(t)
	c := s.client(t)

	if err := c.Machine().Revoke(context.Background(), "acme", "api", 42); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	s.mu.Lock()
	deleted := append([]string(nil), s.deleted...)
	s.mu.Unlock()
	if len(deleted) != 1 || !strings.HasSuffix(deleted[0], "/repos/acme/api/keys/42") {
		t.Fatalf("revoke addressed %v", deleted)
	}

	// A zero id is not a request; it must not become DELETE .../keys/0.
	if err := c.Machine().Revoke(context.Background(), "acme", "api", 0); err != nil {
		t.Fatalf("revoking nothing is not an error: %v", err)
	}
	s.mu.Lock()
	n := len(s.deleted)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("a zero id still issued a delete (%d total)", n)
	}
}

// THE SWEEP IS THE BACKSTOP A TTL USED TO BE.
//
// Nothing on the forge expires a key, so a process that died between minting and
// withdrawing leaves a live push credential. Minting the NEXT grant takes back
// the ones that outlived any possible run — and touches neither a live run's key
// nor a deploy key somebody added deliberately.
func TestGrant_SweepsAbandonedKeysButNotLiveOnesOrRealDeployKeys(t *testing.T) {
	s := newRepoStub(t)
	now := time.Now()
	s.keys = []map[string]any{
		{"id": 1, "title": grantTitle + "sess_old", "created_at": now.Add(-3 * time.Hour).Format(time.RFC3339)},
		{"id": 2, "title": grantTitle + "sess_live", "created_at": now.Add(-2 * time.Minute).Format(time.RFC3339)},
		{"id": 3, "title": "ci deploy key", "created_at": now.Add(-90 * 24 * time.Hour).Format(time.RFC3339)},
	}
	c := s.client(t)
	pinned(t, c)

	if _, err := c.Machine().Grant(context.Background(), "acme", "api", "sess_new"); err != nil {
		t.Fatalf("Grant: %v", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.deleted) != 1 {
		t.Fatalf("the sweep deleted %v, want exactly the abandoned key", s.deleted)
	}
	if !strings.HasSuffix(s.deleted[0], "/keys/1") {
		t.Fatalf("the sweep deleted %q", s.deleted[0])
	}
}

// Tip tells "the branch is not there" apart from "the read failed". Both must
// fail the integrity gate, but only one of them is a fact about the branch.
func TestTip_AbsentBranchIsAnAnswerAndAFailureIsNot(t *testing.T) {
	var code int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code != 0 {
			w.WriteHeader(code)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name": "agent/x", "commit": map[string]any{"id": "abc123"},
		})
	}))
	defer srv.Close()
	c, err := New(srv.URL, "machine-token-value")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sha, found, err := c.Machine().Tip(context.Background(), "acme", "api", "agent/x")
	if err != nil || !found || sha != "abc123" {
		t.Fatalf("present branch: sha=%q found=%v err=%v", sha, found, err)
	}

	code = http.StatusNotFound
	sha, found, err = c.Machine().Tip(context.Background(), "acme", "api", "agent/x")
	if err != nil || found || sha != "" {
		t.Fatalf("an absent branch is an answer, not an error: sha=%q found=%v err=%v", sha, found, err)
	}

	code = http.StatusInternalServerError
	if _, found, err = c.Machine().Tip(context.Background(), "acme", "api", "agent/x"); err == nil || found {
		t.Fatalf("a broken forge must not read as an absent branch: found=%v err=%v", found, err)
	}
}

// A slashed branch reaches the forge WHOLE. The route is a wildcard, so
// escaping the separator would ask about a branch nobody has — and every agent
// branch is slashed.
func TestTip_SlashedBranchIsNotEscaped(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
		_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]any{"id": "abc"}})
	}))
	defer srv.Close()
	c, _ := New(srv.URL, "machine-token-value")
	if _, _, err := c.Machine().Tip(context.Background(), "acme", "api", "agent/abc123"); err != nil {
		t.Fatalf("Tip: %v", err)
	}
	if !strings.HasSuffix(path, "/branches/agent/abc123") {
		t.Fatalf("the branch was mangled on the way out: %q", path)
	}
}

// Ensure creates ONLY when the repository is absent. A repository that is there
// and that the actor may not write stays a refusal — creating past it would let
// a caller make a repository whose name is already taken by one they cannot see.
func TestEnsure_CreatesWhenAbsentAndRefusesWhenForbidden(t *testing.T) {
	s := newRepoStub(t)
	c := s.client(t)

	if _, err := c.Machine().Ensure(context.Background(), "acme", "api", "d"); err != nil {
		t.Fatalf("an existing repo needs no creation: %v", err)
	}
	s.mu.Lock()
	for _, r := range s.reqs {
		if r.Method == http.MethodPost {
			s.mu.Unlock()
			t.Fatalf("an existing repository was re-created (%s %s)", r.Method, r.URL.Path)
		}
	}
	s.mu.Unlock()

	s.repo = &Repo{Name: "api", FullName: "acme/api", Perm: &Perm{Pull: true}} // visible, not writable
	if _, err := c.As("zoe").Ensure(context.Background(), "acme", "api", "d"); err == nil {
		t.Fatal("a repository the actor cannot write was created past")
	}
}

// Known LEARNS THE HOST KEY FROM THE FORGE ITSELF, over a handshake that is
// abandoned before authentication — so it needs no credential and the value it
// returns is what an SSH client would have to accept.
func TestKnown_LearnsTheHostKeyFromTheForge(t *testing.T) {
	_, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	// A server that offers its host key and then refuses every login, which is
	// exactly what the forge does to anyone who is not pushing. It must declare
	// SOME auth method or it aborts before the key exchange — and the host key is
	// sent during the key exchange, which is what makes this a read of a public
	// fact rather than a login.
	cfg := &ssh.ServerConfig{
		PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
			return nil, fmt.Errorf("no")
		},
	}
	cfg.AddHostKey(signer)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_, _, _, _ = ssh.NewServerConn(conn, cfg)
		_ = conn.Close()
	}()

	c, err := New("https://git.test", "machine-token-value")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	c.host = ln.Addr().String()

	line, err := c.Known(context.Background())
	if err != nil {
		t.Fatalf("Known: %v", err)
	}
	// A known_hosts line is "<host> <algo> <base64>", and a non-default port is
	// bracketed — the form OpenSSH itself writes, or ssh will not match it.
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	if !strings.HasPrefix(line, "["+h+"]:"+p+" ") {
		t.Fatalf("the pin does not name the host in known_hosts form: %q", line)
	}
	want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if !strings.Contains(line, want) {
		t.Fatalf("the pin is not the key the server offered:\n got  %q\n want %q", line, want)
	}
	// Learned ONCE: a per-run handshake in front of every dispatch buys no new
	// fact.
	before := fmt.Sprint(hostKeys.m[ln.Addr().String()])
	again, err := c.Known(context.Background())
	if err != nil || again != line || fmt.Sprint(hostKeys.m[ln.Addr().String()]) != before {
		t.Fatalf("the host key was re-learned: %v", err)
	}
}
