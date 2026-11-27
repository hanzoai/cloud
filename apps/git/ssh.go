package git

// ssh.go serves git over SSH, for the repositories whose history is in the
// objects store: `git clone git@git.hanzo.ai:<org>/<repo>.git`, and the deploy key
// a coding run pushes with.
//
// It is stateless in the way the rest of this app is. The host key arrives from KMS
// (GIT_SSH_HOST_KEY, a PEM), keys are rows on hanzo-sql (sshkeys.go), and a session
// reads and writes only the objects store, so any replica serves any connection and
// nothing lives on a disk. With no GIT_SSH_ADDR the listener is not started: a
// process that serves other apps' routes must not bind a port nobody asked for.
//
// The wire is the SAME code the HTTP transport runs. An SSH client speaks git's
// stateful protocol where HTTP is stateless-rpc, so a session adapts one to the
// other: fetch buffers the wants and accumulates the haves, answers each batch with
// NAK, and on `done` hands the whole request to serveFetch — exactly the request a
// stateless client would have sent in one POST. Push reads the commands, then walks
// the pack's own framing to find where it ends (git does not close the stream until
// it has the report), and hands commands and pack to receive.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/go-git/go-git/v5/plumbing/format/packfile"
	"github.com/hanzoai/cloud/apps/git/objects"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"golang.org/x/crypto/ssh"
)

const (
	sshHandshake = 30 * time.Second
	sshIdle      = 10 * time.Minute
	maxCommand   = 1 << 20
)

// keyHolder opens the key registry on first use and keeps it.
type keyHolder struct {
	mu  sync.Mutex
	idx *keyIndex
}

func (h *keyHolder) get(dataDir string) (*keyIndex, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.idx != nil {
		return h.idx, nil
	}
	idx, err := openKeyIndex(dataDir)
	if err != nil {
		return nil, err
	}
	h.idx = idx
	return idx, nil
}

func (h *keyHolder) close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.idx == nil {
		return nil
	}
	err := h.idx.Close()
	h.idx = nil
	return err
}

// sshServer is the listener and its sessions.
type sshServer struct {
	s    *cloud.Service[state]
	cfg  *ssh.ServerConfig
	ln   net.Listener
	wg   sync.WaitGroup
	mu   sync.Mutex
	live map[net.Conn]struct{}
}

// hostSigner reads the host key from GIT_SSH_HOST_KEY. A key generated here would
// differ on every replica and on every restart, and clients would be told the host
// changed, so there is none: no key, no listener.
func hostSigner() (ssh.Signer, error) {
	pem := env.Or("GIT_SSH_HOST_KEY", "")
	if pem == "" {
		return nil, errors.New("GIT_SSH_HOST_KEY is not set (the host key is a KMS value)")
	}
	return ssh.ParsePrivateKey([]byte(pem))
}

// startSSH binds addr and serves until the service ends. It returns the server so
// a test can read the address it bound.
func startSSH(s *cloud.Service[state], addr string, host ssh.Signer) (*sshServer, error) {
	srv := &sshServer{s: s, live: map[net.Conn]struct{}{}}
	srv.cfg = &ssh.ServerConfig{
		// Public keys only: no password, keyboard-interactive or none. An unknown key
		// is refused before any session exists.
		PublicKeyCallback: srv.authenticate,
		MaxAuthTries:      4,
	}
	srv.cfg.AddHostKey(host)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("git ssh: listen %s: %w", addr, err)
	}
	srv.ln = ln
	srv.wg.Add(1)
	go srv.accept()
	return srv, nil
}

func (v *sshServer) addr() string { return v.ln.Addr().String() }

// stop closes the listener and every connection, then waits for the sessions.
func (v *sshServer) stop() {
	_ = v.ln.Close()
	v.mu.Lock()
	for c := range v.live {
		_ = c.Close()
	}
	v.mu.Unlock()
	v.wg.Wait()
}

func (v *sshServer) accept() {
	defer v.wg.Done()
	for {
		c, err := v.ln.Accept()
		if err != nil {
			return
		}
		v.mu.Lock()
		v.live[c] = struct{}{}
		v.mu.Unlock()
		v.wg.Add(1)
		go func() {
			defer v.wg.Done()
			defer func() {
				_ = c.Close()
				v.mu.Lock()
				delete(v.live, c)
				v.mu.Unlock()
			}()
			v.serve(c)
		}()
	}
}

// authenticate resolves a presented key to the row that registered it, and carries
// that row to the session in the connection's permissions.
func (v *sshServer) authenticate(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	ctx, cancel := context.WithTimeout(v.s.State.life, 10*time.Second)
	defer cancel()
	idx, err := keyIndexFor(v.s)
	if err != nil {
		return nil, errors.New("unavailable")
	}
	k, err := idx.lookup(ctx, ssh.FingerprintSHA256(key))
	if err != nil {
		return nil, errors.New("unknown key")
	}
	return &ssh.Permissions{Extensions: map[string]string{
		"id": k.ID, "kind": k.Kind, "org": k.Org, "project": k.Project, "repo": k.Repo,
		"creator": k.Creator, "readonly": fmt.Sprint(k.ReadOnly),
	}}, nil
}

func (v *sshServer) serve(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(sshHandshake))
	sc, chans, reqs, err := ssh.NewServerConn(c, v.cfg)
	if err != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	defer func() { _ = sc.Close() }()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			_ = nc.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, requests, err := nc.Accept()
		if err != nil {
			return
		}
		go v.session(sc.Permissions, ch, requests)
	}
}

// session runs the one exec command a git client sends.
func (v *sshServer) session(p *ssh.Permissions, ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer func() { _ = ch.Close() }()
	for req := range reqs {
		switch req.Type {
		case "exec":
			var msg struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &msg); err != nil {
				_ = req.Reply(false, nil)
				return
			}
			_ = req.Reply(true, nil)
			status := uint32(0)
			if err := v.exec(p, ch, msg.Command); err != nil {
				fmt.Fprintf(ch.Stderr(), "%s\n", err)
				status = 1
			}
			_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{status}))
			return
		case "env", "pty-req", "shell":
			// git sends GIT_PROTOCOL as env to ask for protocol v2; the answer is v1,
			// which every client falls back to. A shell is never offered.
			ok := req.Type == "env"
			_ = req.Reply(ok, nil)
		default:
			_ = req.Reply(false, nil)
		}
	}
}

// target is what an exec command names.
type target struct {
	service            string
	org, project, name string
}

// parseExec reads `git-upload-pack '<org>/[<project>/]<repo>.git'`.
func parseExec(cmd string) (target, error) {
	f := strings.SplitN(strings.TrimSpace(cmd), " ", 2)
	if len(f) != 2 || (f[0] != svcUploadPack && f[0] != svcReceivePack) {
		return target{}, errors.New("only git-upload-pack and git-receive-pack are served")
	}
	path := strings.Trim(strings.TrimSpace(f[1]), `'"`)
	path = strings.Trim(path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.Split(path, "/")
	t := target{service: f[0]}
	switch len(parts) {
	case 2:
		t.org, t.name = parts[0], parts[1]
	case 3:
		t.org, t.project, t.name = parts[0], parts[1], parts[2]
	default:
		return target{}, errors.New("repository path is <org>/<repo>.git or <org>/<project>/<repo>.git")
	}
	if !orgRE.MatchString(t.org) || !nameRE.MatchString(t.name) || (t.project != "" && !projectRE.MatchString(t.project)) {
		return target{}, errors.New("invalid repository path")
	}
	return t, nil
}

// exec authorizes the command and serves it.
func (v *sshServer) exec(p *ssh.Permissions, ch ssh.Channel, cmd string) error {
	t, err := parseExec(cmd)
	if err != nil {
		return err
	}
	x := p.Extensions
	// A key reaches its own org only; a deploy key reaches its one repository, and
	// only to fetch when it is read-only. Every refusal reads the same to the caller:
	// whether a repository exists is not something a key outside it may learn.
	missing := errors.New("repository not found or access denied")
	if x["org"] != t.org {
		return missing
	}
	if x["kind"] == kindDeploy && (x["repo"] != t.name || x["project"] != t.project) {
		return missing
	}
	write := t.service == svcReceivePack
	if write && x["kind"] == kindDeploy && x["readonly"] == "true" {
		return errors.New("this deploy key is read-only")
	}
	ctx, cancel := context.WithCancel(v.s.State.life)
	defer cancel()
	// The timeout is on idleness, not on the transfer: a clone may take as long as
	// it takes.
	idle := time.AfterFunc(sshIdle, cancel)
	defer idle.Stop()

	store, err := storeFor(v.s, t.org)
	if err != nil {
		return missing
	}
	row, err := store.Get(ctx, t.org, t.project, normalizeName(t.name))
	if err != nil {
		return missing
	}
	if _, linked := linkOf(row); linked {
		return errors.New("this repository is linked to another host; use that host's address")
	}
	objs, err := v.s.State.vault.home(ctx, v.s, row)
	if err != nil || objs == nil {
		return errors.New("this repository's history is not in the object store")
	}
	repo := objs.Repo(ctx, row.ID)
	refs, err := advertisement(repo, t.service)
	if err != nil {
		return fmt.Errorf("advertise refs: %w", err)
	}
	if _, err := ch.Write(refs); err != nil {
		return err
	}
	in := bufio.NewReader(ch)
	pusher := x["creator"]
	if pusher == "" {
		pusher = "deploy:" + x["id"]
	}
	rctx := cloud.For(ctx, t.org)
	if write {
		return v.receivePack(rctx, ch, in, objs, row, pusher)
	}
	return v.uploadPack(rctx, ch, in, objs, row)
}

// readPkt reads one pkt-line and answers its raw bytes; flush is a 0000 packet.
func readPkt(r *bufio.Reader) (raw []byte, flush bool, err error) {
	var h [4]byte
	if _, err = io.ReadFull(r, h[:]); err != nil {
		return nil, false, err
	}
	n, err := pktLen(h[:])
	if err != nil {
		return nil, false, err
	}
	if n == 0 {
		return h[:], true, nil
	}
	if n < 4 || n > 65520 {
		return nil, false, fmt.Errorf("pkt-line length %d", n)
	}
	raw = make([]byte, n)
	copy(raw, h[:])
	if _, err = io.ReadFull(r, raw[4:]); err != nil {
		return nil, false, err
	}
	return raw, false, nil
}

// uploadPack runs a fetch: wants, then rounds of haves answered NAK, then done.
func (v *sshServer) uploadPack(ctx context.Context, ch ssh.Channel, in *bufio.Reader, objs *objects.Store, row Repo) error {
	var body bytes.Buffer
	sawWant := false
	for {
		raw, flush, err := readPkt(in)
		if err != nil {
			if errors.Is(err, io.EOF) && !sawWant {
				return nil // ls-remote: advertisement read, nothing asked
			}
			return err
		}
		body.Write(raw)
		if flush {
			break
		}
		sawWant = true
		if body.Len() > maxCommand {
			return errors.New("request too large")
		}
	}
	if !sawWant {
		return nil
	}
	for {
		raw, flush, err := readPkt(in)
		if err != nil {
			return err
		}
		if flush {
			// A round of haves with no done: nothing is common yet as far as the
			// server says, and the client sends more or finishes with done.
			if _, err := ch.Write([]byte("0008NAK\n")); err != nil {
				return err
			}
			continue
		}
		body.Write(raw)
		if strings.TrimSuffix(string(raw[4:]), "\n") == "done" {
			break
		}
		if body.Len() > maxCommand {
			return errors.New("request too large")
		}
	}
	if err := acquirePackSlot(ctx); err != nil {
		return err
	}
	rc, err := serveFetch(ctx, objs, row.ID, body.Bytes())
	if err != nil {
		releasePackSlot()
		return fmt.Errorf("upload-pack: %w", err)
	}
	defer func() {
		_ = rc.Close()
		if st, ok := rc.(*stream); !ok || st.onEnd == nil {
			releasePackSlot()
		}
	}()
	_, err = io.Copy(ch, rc)
	return err
}

// receivePack runs a push: commands, the pack, then the report.
func (v *sshServer) receivePack(ctx context.Context, ch ssh.Channel, in *bufio.Reader, objs *objects.Store, row Repo, pusher string) error {
	var raw bytes.Buffer
	for {
		pkt, flush, err := readPkt(in)
		if err != nil {
			if errors.Is(err, io.EOF) && raw.Len() == 0 {
				return nil
			}
			return err
		}
		raw.Write(pkt)
		if flush {
			break
		}
		if raw.Len() > maxCommand {
			return errors.New("commands too large")
		}
	}
	if raw.Len() == 4 {
		return nil // a flush alone: the client had nothing to push
	}
	cmds, n, caps, err := parseRefCommandsCaps(raw.Bytes())
	if err != nil {
		return fmt.Errorf("unreadable push: %w", err)
	}
	writes := false
	for _, c := range cmds {
		if !c.Deletes() {
			writes = true
		}
	}
	if writes {
		if err := readPack(in, &raw); err != nil {
			return fmt.Errorf("unreadable pack: %w", err)
		}
	}
	org, project, name := row.Org, row.Project, row.Name
	if verr := checkRefPolicy(cmds, headBranch(objs.Repo(ctx, row.ID)), ""); verr != nil {
		v.s.Log.Warn("git push refused by ref policy", "org", org, "repo", name, "reason", verr.Error())
		_, err := ch.Write(refusalReport(cmds, caps, verr.Error()))
		return err
	}
	var report []byte
	var landed []refCommand
	if err := withPackSlot(ctx, func() (err error) {
		report, landed, err = receive(ctx, objs, row.ID, raw.Bytes(), cmds, n, caps)
		return err
	}); err != nil {
		v.s.Log.Warn("git receive-pack failed", "org", org, "repo", name, "err", err)
		return errors.New("receive-pack failed")
	}
	afterReceive(v.s, context.WithoutCancel(ctx), org, project, name, pusher, landed)
	_, err = ch.Write(report)
	return err
}

// readPack copies one pack from r to dst and stops at its own end. A pack carries
// its object count and a trailing checksum, so its end is known without waiting for
// the client to close; walking the objects is how the end is found.
func readPack(r io.Reader, dst *bytes.Buffer) error {
	tee := io.TeeReader(r, dst)
	sc := packfile.NewScanner(tee)
	_, count, err := sc.Header()
	if err != nil {
		return err
	}
	for i := uint32(0); i < count; i++ {
		if _, err := sc.NextObjectHeader(); err != nil {
			return err
		}
		if _, _, err := sc.NextObject(io.Discard); err != nil {
			return err
		}
	}
	_, err = sc.Checksum()
	return err
}

// newHostKey makes an ed25519 host key as a PEM, for tests and for the operator who
// seeds KMS.
func newHostKey() ([]byte, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	blk, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(blk), nil
}
