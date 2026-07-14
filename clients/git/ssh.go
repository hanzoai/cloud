package git

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/hanzoai/cloud"
	"golang.org/x/crypto/ssh"
)

// ssh.go serves the Git SSH transport so `git clone git@<sshHost>:<org>/<repo>.git`
// works alongside smart-HTTP. It uses golang.org/x/crypto/ssh (already a direct
// dependency — no new heavy dep) directly rather than a wrapper.
//
// Auth is per-user public key. The PublicKeyCallback fingerprints the presented
// key and resolves it, via the global key registry (keystore.go), to the org +
// user that registered it — failing CLOSED on any unknown key. The resolved
// identity is stashed in ssh.Permissions.Extensions and read back by the session
// handler, which sets the SAME (org) scope the HTTP path derives from X-Org-Id.
// So an SSH request and an HTTP request converge on the ONE pack code path
// (pack.go) with the ONE org scope.
//
// The session handler accepts exactly two exec commands —
// `git-upload-pack '<org>/<repo>.git'` (clone/fetch) and
// `git-receive-pack '<org>/<repo>.git'` (push) — parses the org/repo, enforces
// that the path org equals the key-bound org (no cross-org), confirms the
// repo exists, and drives the shared pack driver on the channel's stdin/stdout.

// sshConf carries the SSH server's runtime configuration, resolved from deps/env
// in sshConfig. Kept small: the listen address, the host-key source, and a
// back-reference to the git service the sessions operate on.
type sshConf struct {
	addr        string // listen address, e.g. ":2222"
	hostKeyPEM  []byte // KMS/env-provided host private key (PEM), or nil
	hostKeyPath string // on-disk host key path; generated + persisted if absent
}

// sshServer owns the SSH listener and its lifecycle.
type sshServer struct {
	svc    *cloud.Service[state]
	cfg    *ssh.ServerConfig
	listen string

	mu       sync.Mutex
	ln       net.Listener
	closed   bool
	wg       sync.WaitGroup
	boundTCP string // actual bound address (resolves :0 to the ephemeral port for tests)
}

// gitSSHHost resolves the SSH host advertised in sshUrl. GIT_SSH_HOST wins;
// else it is derived from the deployment domain (api.hanzo.ai → git.hanzo.ai);
// else "git.hanzo.ai".
func gitSSHHost(domain string) string {
	if h := strings.TrimSpace(os.Getenv("GIT_SSH_HOST")); h != "" {
		return h
	}
	return defaultSSHHost(domain)
}

// defaultSSHHost derives the git SSH host from the primary domain: the registered
// domain with a "git." prefix (api.hanzo.ai → git.hanzo.ai). Falls back to
// "git.hanzo.ai" when the domain is empty or unparseable.
func defaultSSHHost(domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return "git.hanzo.ai"
	}
	// Strip a leading "api." (the common cloud host) and prefix "git.".
	base := strings.TrimPrefix(domain, "api.")
	return "git." + base
}

// sshConfig resolves the SSH runtime config from deps/env. The host key is
// sourced KMS-first (GIT_SSH_HOST_KEY, a PEM the operator syncs from KMS),
// falling back to an on-disk key under the git data root that is generated +
// persisted 0600 on first boot. The listen address is GIT_SSH_ADDR
// (default :2222 — real :22 is fronted by a k8s TCP LoadBalancer).
func sshConfig(deps cloud.Deps, gitRoot string) sshConf {
	addr := strings.TrimSpace(os.Getenv("GIT_SSH_ADDR"))
	if addr == "" {
		addr = ":2222"
	}
	var pemKey []byte
	if p := strings.TrimSpace(os.Getenv("GIT_SSH_HOST_KEY")); p != "" {
		pemKey = []byte(p)
	}
	return sshConf{
		addr:        addr,
		hostKeyPEM:  pemKey,
		hostKeyPath: filepath.Join(gitRoot, "ssh_host_ed25519_key"),
	}
}

// newSSHServer builds the SSH server: it resolves the host key, wires the
// public-key auth callback (fail-closed key→org resolution), and prepares the
// ServerConfig. It does NOT yet listen — call start.
func newSSHServer(s *cloud.Service[state], conf sshConf) (*sshServer, error) {
	signer, err := loadOrCreateHostKey(conf)
	if err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}
	srv := &sshServer{svc: s, listen: conf.addr}
	cfg := &ssh.ServerConfig{
		// PublicKeyCallback is the ONLY accepted auth. It fingerprints the
		// presented key and resolves it to an org; an unknown key returns an
		// error, so auth fails CLOSED — there is no password / keyboard-interactive
		// / none fallback.
		PublicKeyCallback: srv.authPublicKey,
	}
	cfg.AddHostKey(signer)
	srv.cfg = cfg
	return srv, nil
}

// authPublicKey resolves a presented SSH key to its owning (org, user) via the
// global key registry, keyed by the SHA256 fingerprint. Unknown key → error →
// auth rejected (fail closed). On success the resolved identity rides in
// Permissions.Extensions so the session handler can scope the org WITHOUT a
// second lookup.
func (srv *sshServer) authPublicKey(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	fp := ssh.FingerprintSHA256(key)
	row, err := srv.svc.State.keys.ByFingerprint(context.Background(), fp)
	if err != nil {
		// Unknown or errored key: reject. Never leak whether the fingerprint
		// exists — a single opaque "unknown key" for every failure.
		return nil, fmt.Errorf("ssh: unknown key")
	}
	return &ssh.Permissions{
		Extensions: map[string]string{
			"git-org":     row.Org,
			"git-user-id": row.UserID,
			"git-key-id":  row.ID,
		},
	}, nil
}

// start binds the listener and accepts connections in a background goroutine.
func (srv *sshServer) start() error {
	ln, err := net.Listen("tcp", srv.listen)
	if err != nil {
		return fmt.Errorf("ssh listen %q: %w", srv.listen, err)
	}
	srv.mu.Lock()
	srv.ln = ln
	srv.boundTCP = ln.Addr().String()
	srv.mu.Unlock()

	srv.wg.Add(1)
	go srv.acceptLoop(ln)
	return nil
}

// addr returns the bound listen address (the ephemeral port resolved, for tests).
func (srv *sshServer) addr() string {
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.boundTCP != "" {
		return srv.boundTCP
	}
	return srv.listen
}

// stop closes the listener and waits for in-flight connections to drain.
// Idempotent.
func (srv *sshServer) stop() {
	srv.mu.Lock()
	if srv.closed {
		srv.mu.Unlock()
		return
	}
	srv.closed = true
	ln := srv.ln
	srv.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	srv.wg.Wait()
}

func (srv *sshServer) acceptLoop(ln net.Listener) {
	defer srv.wg.Done()
	for {
		conn, err := ln.Accept()
		if err != nil {
			srv.mu.Lock()
			closed := srv.closed
			srv.mu.Unlock()
			if closed {
				return // listener closed by stop() — clean shutdown
			}
			// Transient accept error; keep serving.
			srv.svc.Log.Warn("git ssh accept failed", "err", err)
			continue
		}
		srv.wg.Add(1)
		go func() {
			defer srv.wg.Done()
			srv.handleConn(conn)
		}()
	}
}

// handleConn performs the SSH handshake (auth via authPublicKey) and services
// the connection's session channels.
func (srv *sshServer) handleConn(nConn net.Conn) {
	defer func() { _ = nConn.Close() }()
	sconn, chans, reqs, err := ssh.NewServerConn(nConn, srv.cfg)
	if err != nil {
		// Handshake / auth failure — normal for probes + rejected keys.
		return
	}
	defer func() { _ = sconn.Close() }()
	go ssh.DiscardRequests(reqs) // reject global out-of-band requests

	org := sconn.Permissions.Extensions["git-org"]
	for newChan := range chans {
		if newChan.ChannelType() != "session" {
			_ = newChan.Reject(ssh.UnknownChannelType, "only session channels are supported")
			continue
		}
		ch, chReqs, err := newChan.Accept()
		if err != nil {
			continue
		}
		go srv.handleSession(org, ch, chReqs)
	}
}

// execPayload is the wire shape of an SSH "exec" request: a single
// length-prefixed command string (RFC 4254 §6.5).
type execPayload struct {
	Command string
}

// envPayload is the wire shape of an SSH "env" request: a name/value pair
// (RFC 4254 §6.4). git clients send GIT_PROTOCOL here to negotiate protocol v2.
type envPayload struct {
	Name  string
	Value string
}

// handleSession services one session channel: it waits for the "exec" request
// carrying the git command, runs it, and closes the channel. A preceding "env"
// request carrying GIT_PROTOCOL is captured so the git subprocess negotiates the
// requested wire protocol (v2 = cheaper on large repos). Any other request type
// (shell, pty) is rejected — this is a git-only endpoint.
func (srv *sshServer) handleSession(org string, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer func() { _ = ch.Close() }()
	var gitProtocol string
	for req := range reqs {
		switch req.Type {
		case "exec":
			var p execPayload
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				_ = req.Reply(false, nil)
				srv.exit(ch, 1)
				return
			}
			_ = req.Reply(true, nil)
			code := srv.runGitCommand(org, p.Command, gitProtocol, ch)
			srv.exit(ch, code)
			return
		case "env":
			// Capture GIT_PROTOCOL (validated before use in gitProtocolEnv); ack
			// silently. Other env vars are accepted-and-ignored.
			var e envPayload
			if err := ssh.Unmarshal(req.Payload, &e); err == nil && e.Name == "GIT_PROTOCOL" {
				gitProtocol = e.Value
			}
			_ = req.Reply(true, nil)
		case "shell", "pty-req":
			// A bare `ssh git@host` (shell) or pty setup: git-only endpoint.
			_ = req.Reply(false, nil)
			if req.Type == "shell" {
				_, _ = io.WriteString(ch.Stderr(), "Hi! This is the Hanzo git SSH endpoint; interactive shells are not available.\n")
				srv.exit(ch, 1)
				return
			}
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// gitCmdRE parses `git-upload-pack '<path>'` / `git-receive-pack '<path>'`,
// accepting single-quoted or bare paths (git quotes; some clients don't).
var gitCmdRE = regexp.MustCompile(`^(git-upload-pack|git-receive-pack) '?([^']+?)'?$`)

// runGitCommand parses the exec command, enforces org scoping (path org == key
// org), confirms the repo exists, and drives the shared pack code path on the
// channel's stdin/stdout. Returns the exit code the session reports to the client.
func (srv *sshServer) runGitCommand(keyOrg, command, gitProtocol string, ch ssh.Channel) int {
	m := gitCmdRE.FindStringSubmatch(strings.TrimSpace(command))
	if m == nil {
		_, _ = io.WriteString(ch.Stderr(), "unsupported command; only git-upload-pack / git-receive-pack are allowed\n")
		return 1
	}
	service, path := m[1], m[2]

	pathOrg, name, err := parseRepoPath(path)
	if err != nil {
		_, _ = io.WriteString(ch.Stderr(), "invalid repo path: "+err.Error()+"\n")
		return 1
	}
	// Org scope: the key's bound org is authoritative; the path org must match it.
	// A key for org A can never reach org B's namespace even if it crafts the path.
	if keyOrg == "" || pathOrg != keyOrg {
		_, _ = io.WriteString(ch.Stderr(), "access denied: repository is outside your organization\n")
		return 1
	}
	// SSH has no project sub-scope (no X-Project-Id) — org-level repos only.
	const project = ""

	store, err := storeFor(srv.svc, keyOrg)
	if err != nil {
		_, _ = io.WriteString(ch.Stderr(), "internal error\n")
		return 1
	}
	if _, err := store.Get(context.Background(), keyOrg, project, name); err != nil {
		_, _ = io.WriteString(ch.Stderr(), "repository not found\n")
		return 1
	}

	ctx := context.Background()
	switch service {
	case svcUploadPack:
		if err := sshUploadPack(srv.svc, ctx, keyOrg, project, name, gitProtocol, ch); err != nil {
			srv.svc.Log.Warn("git ssh upload-pack failed", "org", keyOrg, "repo", name, "err", err)
			return 1
		}
	case svcReceivePack:
		if err := sshReceivePack(srv.svc, ctx, keyOrg, project, name, gitProtocol, ch); err != nil {
			srv.svc.Log.Warn("git ssh receive-pack failed", "org", keyOrg, "repo", name, "err", err)
			return 1
		}
	}
	return 0
}

// exit sends the exit-status request and closes the write side, mirroring what a
// real git server does so the client sees a clean end.
func (srv *sshServer) exit(ch ssh.Channel, code int) {
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(code)}))
}

// repoPathRE validates the "<org>/<repo>" tail of a git SSH path. Both segments
// are safe identifiers (mirrors nameRE); a leading slash is tolerated (git may
// send an absolute-looking path).
var repoPathRE = regexp.MustCompile(`^/?([A-Za-z0-9][A-Za-z0-9._-]{0,63})/([A-Za-z0-9][A-Za-z0-9._-]{0,63})$`)

// parseRepoPath extracts (org, repo) from a git SSH path like "acme/code.git"
// or "/acme/code.git". The trailing ".git" is stripped, and both segments are
// validated as safe identifiers so the path can never traverse storage.
func parseRepoPath(path string) (org, repo string, err error) {
	path = strings.TrimSpace(path)
	path = strings.TrimSuffix(path, ".git")
	m := repoPathRE.FindStringSubmatch(path)
	if m == nil {
		return "", "", errors.New("path must be <org>/<repo>.git")
	}
	return m[1], m[2], nil
}

// loadOrCreateHostKey resolves the SSH host key signer: an operator-provided PEM
// (KMS/env) wins; else an on-disk key is loaded, or generated (ed25519) and
// persisted 0600 on first boot. NEVER hardcoded.
func loadOrCreateHostKey(conf sshConf) (ssh.Signer, error) {
	if len(conf.hostKeyPEM) > 0 {
		signer, err := ssh.ParsePrivateKey(conf.hostKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("parse GIT_SSH_HOST_KEY: %w", err)
		}
		return signer, nil
	}
	if b, err := os.ReadFile(conf.hostKeyPath); err == nil {
		signer, err := ssh.ParsePrivateKey(b)
		if err != nil {
			return nil, fmt.Errorf("parse host key %q: %w", conf.hostKeyPath, err)
		}
		return signer, nil
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read host key %q: %w", conf.hostKeyPath, err)
	}
	// First boot: generate an ed25519 host key and persist it 0600.
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generate host key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "hanzo-git-host")
	if err != nil {
		return nil, fmt.Errorf("marshal host key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(conf.hostKeyPath), 0o700); err != nil {
		return nil, fmt.Errorf("mkdir host key dir: %w", err)
	}
	if err := os.WriteFile(conf.hostKeyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, fmt.Errorf("persist host key %q: %w", conf.hostKeyPath, err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, fmt.Errorf("signer from host key: %w", err)
	}
	return signer, nil
}
