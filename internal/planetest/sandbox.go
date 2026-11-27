// Copyright © 2026 Hanzo AI. MIT License.

package planetest

// sandbox.go — the sandboxes peer a compute test runs against: apps/sandbox's half
// of the internal plane, on a real socket, with a map where the pod would be.
//
// It answers the ops apps/sandbox publishes, so a caller reaches it exactly the
// way it reaches the real one — cloud.Ask resolves the app, zip dispatches the op,
// the reply decodes into the declared type. What is faked is the POD and nothing
// else, so an op renamed or a field moved fails in a test instead of in production.
//
// It lives HERE and not beside its callers for the reason this package's own doc
// gives: two subsystems run snippets in a sandbox — apps/exec serves the code
// interpreter and apps/functions invokes a customer function — and a copy per caller
// is how nine packages ended up counting an endpoint nothing calls any more.

import (
	"context"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/zap-proto/zip"
)

// Workdir is where a fake sandbox keeps its files — the same directory the real
// `exec` class uses, because the code tool tells the model to persist artifacts
// there and a test against a different one would prove nothing about the contract.
const Workdir = "/mnt/data"

// Pod is one fake sandbox: its files, and the two clocks the shell commands a caller
// actually sends depend on — a file's mtime, and the marker a run stamps for itself.
type Pod struct {
	Files map[string][]byte
	mtime map[string]time.Time
	marks map[string]time.Time
}

// Program is what a run DOES: what it printed, whether it failed, and the files it
// left behind (keyed by path relative to [Workdir]). Nil means a run that printed
// nothing and wrote nothing.
type Program func(id string, argv []string) (stdout, stderr string, exit int, wrote map[string][]byte)

// Sandboxes is the peer. Set Run to give the fake pods a program; leave it nil and
// every run is a silent success.
type Sandboxes struct {
	mu     sync.Mutex
	pods   map[string]*Pod
	parked map[string]*Pod
	held   map[string]string // project -> sandbox id
	minted int               // sandboxes ever leased: an id is never reused
	runs   [][]string
	n      atomic.Int32

	// Run is the program every leased sandbox executes. Assign it before the call
	// under test; it is read under the peer's own lock.
	Run Program

	// OnLease observes the ORG each lease was taken for — the caller's plane
	// identity, which is the only thing a cross-tenant test can assert on. A
	// sandbox id says nothing about whose it is; this says exactly that.
	OnLease func(org string)

	// LeasedBy observes the whole caller each lease was taken as: who it is for and
	// who pays (the username, home org and billing account the payer is resolved
	// from), which OnLease's org alone cannot show.
	LeasedBy func(who zip.Caller)

	// LeaseIns observes each lease exactly as it crossed the plane, Bearer included:
	// whether a sibling relayed the person's own token into the lease, which only the
	// request can show.
	LeaseIns func(in client.LeaseIn)

	// Other answers a line none of the shapes run interprets — a start script, say.
	// Nil answers it empty with exit 0.
	Other func(id string, argv []string) client.Ran

	// FailLeases refuses that many leases with 503 before it answers any.
	FailLeases int

	// Stops counts the stop ops the peer was asked, by sandbox id.
	Stops map[string]int

	// Stream answers a stream op: it is told the org, the subject, the sandbox and the
	// socket asked for, and returns the unix socket the stream is at.
	Stream func(org, subject, id, path string) string
}

// ServeSandboxes binds the sandboxes peer's socket and returns it. It shares the
// test's ONE runtime directory (see runtimeDir), so a test may serve this and the
// commerce peer together.
func ServeSandboxes(t *testing.T) *Sandboxes {
	t.Helper()
	s := &Sandboxes{pods: map[string]*Pod{}, parked: map[string]*Pod{}, held: map[string]string{}}
	runtimeDir(t)

	app := zip.New(zip.Config{AppName: sandboxpeer.App})
	app.Post("/sandbox/lease", s.lease,
		zip.WithOperationID(client.SandboxLease))
	app.Post("/sandbox/run", s.run,
		zip.WithOperationID(client.SandboxRun))
	app.Post("/sandbox/read", s.read,
		zip.WithOperationID(client.SandboxRead))
	app.Post("/sandbox/write", s.write,
		zip.WithOperationID(client.SandboxWrite))
	app.Post("/sandbox/end", s.end,
		zip.WithOperationID(client.SandboxEnd))
	app.Post("/sandbox/park", s.park,
		zip.WithOperationID(client.SandboxPark))
	app.Post("/sandbox/stop", s.stop,
		zip.WithOperationID(client.SandboxStop))
	app.Post("/sandbox/stream", s.stream,
		zip.WithOperationID(client.SandboxStream))
	app.Post("/sandbox/attach", s.attach,
		zip.WithOperationID(client.SandboxAttach))

	listen(t, app, sandboxpeer.App)
	return s
}

// Pod is the fake sandbox with this id, or nil.
func (s *Sandboxes) Pod(id string) *Pod {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pods[id]
}

// Live is how many leases the peer still holds. A code-interpreter session
// legitimately outlives its run; a function invoke must leave none.
func (s *Sandboxes) Live() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pods)
}

// Ran counts PROGRAMS, not calls. Every test that asks "did compute happen" means
// this: a lease nothing ran in is not compute, and the artifact sweep that follows a
// run is bookkeeping — counting either would double the answer.
func (s *Sandboxes) Ran() int32 { return s.n.Load() }

// Lines is every shell line the peer was asked to run, in order, for a test that
// asserts on what was SENT rather than only on what came back.
func (s *Sandboxes) Lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.runs))
	for _, a := range s.runs {
		if len(a) >= 3 {
			out = append(out, a[2])
		}
	}
	return out
}

// Args is the argument vector the most recent PROGRAM was given — what followed
// `sh -c <line> sh`. It is how a test checks that a caller's args reached the
// program and not, say, the compiler that built it.
func (s *Sandboxes) Args() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range slices.Backward(s.runs) {
		if len(v) >= 4 && isProgram(v[2]) {
			return v[4:]
		}
	}
	return nil
}

// isProgram tells a caller's run line from its artifact sweep. The run stamps a
// marker first — that is what makes the sweep's `-newer` meaningful — so the leading
// redirect is the signature, and it is the ONE place this file decides which is
// which.
func isProgram(line string) bool { return strings.HasPrefix(line, ": > ") }

func (s *Sandboxes) lease(ctx context.Context, in *client.LeaseIn) (*client.Leased, error) {
	// The tenant rides the CALLER. Refusing an empty one is what makes a test that
	// asserts tenancy mean something — and it is what caught cloud.For being read
	// only on a context with no request behind it.
	if zip.CallerOf(ctx).Org == "" {
		return nil, zip.ErrForbidden("sandbox: org required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.FailLeases > 0 {
		s.FailLeases--
		return nil, zip.Errorf(503, "sandbox: no node")
	}
	if s.OnLease != nil {
		s.OnLease(zip.CallerOf(ctx).Org)
	}
	if s.LeasedBy != nil {
		s.LeasedBy(zip.CallerOf(ctx))
	}
	if s.LeaseIns != nil {
		s.LeaseIns(*in)
	}
	id := strings.TrimSpace(in.ID)
	if id != "" && s.parked[id] != nil {
		s.pods[id] = s.parked[id]
		delete(s.parked, id)
	} else if id == "" || s.pods[id] == nil {
		s.minted++
		id = "m_" + strings.Repeat("0", 4) + string(rune('a'+s.minted-1))
		s.pods[id] = &Pod{Files: map[string][]byte{}, mtime: map[string]time.Time{},
			marks: map[string]time.Time{}}
		if in.Project != "" {
			s.held[in.Project] = id
		}
	}
	return &client.Leased{ID: id, Class: "exec", Status: "running", Workdir: Workdir}, nil
}

func (s *Sandboxes) write(ctx context.Context, in *client.WriteIn) (*client.Wrote, error) {
	p := s.Pod(in.ID)
	if p == nil {
		return nil, zip.ErrNotFound("sandbox not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rel := rel(in.Path)
	p.Files[rel] = in.Data
	p.mtime[rel] = time.Now()
	return &client.Wrote{Path: Workdir + "/" + rel, Bytes: len(in.Data)}, nil
}

func (s *Sandboxes) read(ctx context.Context, in *client.PathIn) (*client.Blob, error) {
	p := s.Pod(in.ID)
	if p == nil {
		return nil, zip.ErrNotFound("sandbox not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := rel(in.Path)
	if r == "" {
		return &client.Blob{Path: Workdir, Dir: true, Entries: slices.Sorted(maps.Keys(p.Files))}, nil
	}
	b, ok := p.Files[r]
	if !ok {
		return nil, zip.ErrNotFound("no such path")
	}
	return &client.Blob{Path: Workdir + "/" + r, Data: b}, nil
}

// run interprets the three shell shapes a caller actually sends — the marker-plus-
// program line, the `-newer` artifact sweep, and the mtime listing. Anything else is
// recorded and answers empty, which is how a test notices a caller started sending a
// fourth.
func (s *Sandboxes) run(ctx context.Context, in *client.RunIn) (*client.Ran, error) {
	p := s.Pod(in.ID)
	if p == nil {
		return nil, zip.ErrNotFound("sandbox not found")
	}
	s.mu.Lock()
	s.runs = append(s.runs, in.Argv)
	prog, other := s.Run, s.Other
	s.mu.Unlock()

	line := ""
	if len(in.Argv) >= 3 {
		line = in.Argv[2]
	}
	switch {
	case isProgram(line):
		s.n.Add(1)
		mark := time.Now()
		s.mu.Lock()
		p.marks["run"] = mark
		s.mu.Unlock()
		// The fake's clock has to be able to tell "before the mark" from "after" it,
		// and a monotonic clock read twice in the same instruction stream can return
		// the same instant. One millisecond is invisible to a test suite and is the
		// difference between an artifact sweep that works and one that reports the
		// source file it just wrote.
		time.Sleep(time.Millisecond)
		out, errout, code, wrote := "", "", 0, map[string][]byte(nil)
		if prog != nil {
			out, errout, code, wrote = prog(in.ID, in.Argv)
		}
		s.mu.Lock()
		for path, b := range wrote {
			p.Files[path] = b
			p.mtime[path] = time.Now()
		}
		s.mu.Unlock()
		return &client.Ran{ExitCode: code, Stdout: out, Stderr: errout}, nil

	case strings.Contains(line, "-newer "):
		s.mu.Lock()
		defer s.mu.Unlock()
		var names []string
		for n, mt := range p.mtime {
			if mt.After(p.marks["run"]) {
				names = append(names, "./"+n)
			}
		}
		sort.Strings(names)
		return &client.Ran{Stdout: strings.Join(names, "\n")}, nil

	case strings.Contains(line, "date -u -r"):
		// The RECURSIVE listing: stamp then path, one pair per file, at any depth.
		// The fake keeps paths flat-keyed with separators in them, which is exactly
		// what `find . -type f` reports, so a nested artifact appears here the same
		// way it appears in the artifact sweep above — the two used to disagree.
		s.mu.Lock()
		defer s.mu.Unlock()
		var rows []string
		for _, n := range slices.Sorted(maps.Keys(p.Files)) {
			// `-maxdepth 1` means top level only, which is what the fake must honour
			// for a test of nested artifacts to be able to fail.
			if strings.Contains(line, "-maxdepth 1") && strings.Contains(n, "/") {
				continue
			}
			rows = append(rows, p.mtime[n].UTC().Format("2006-01-02T15:04:05Z"), "./"+n)
		}
		return &client.Ran{Stdout: strings.Join(rows, "\n")}, nil
	}
	if other != nil {
		r := other(in.ID, in.Argv)
		return &r, nil
	}
	return &client.Ran{}, nil
}

// stop records an interrupt; the fake runs nothing long enough to need one.
func (s *Sandboxes) stop(_ context.Context, in *client.StopIn) (*client.Stopped, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pods[in.ID] == nil {
		return nil, zip.ErrNotFound("sandbox not found")
	}
	if s.Stops == nil {
		s.Stops = map[string]int{}
	}
	s.Stops[in.ID]++
	return &client.Stopped{}, nil
}

// stream hands out the socket the test says the stream is at.
func (s *Sandboxes) stream(ctx context.Context, in *client.StreamIn) (*client.Streamed, error) {
	who := zip.CallerOf(ctx)
	if s.Stream == nil || who.Org == "" {
		return nil, zip.ErrForbidden("sandbox: org required")
	}
	return &client.Streamed{Socket: s.Stream(who.Org, who.User, in.ID, in.Path), ExpiresIn: 30}, nil
}

// attach answers the sandbox a project holds, or an empty id.
func (s *Sandboxes) attach(_ context.Context, in *client.AttachIn) (*client.Attached, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.held[in.Project]
	if s.pods[id] == nil && s.parked[id] == nil {
		id = ""
	}
	return &client.Attached{ID: id}, nil
}

// park stops the pod and keeps its disk so a later lease naming its id brings it back.
func (s *Sandboxes) park(ctx context.Context, in *client.ParkIn) (*struct{}, error) {
	if zip.CallerOf(ctx).Org == "" {
		return nil, zip.ErrForbidden("sandbox: org required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := strings.TrimSpace(in.ID)
	if p := s.pods[id]; p != nil {
		s.parked[id] = p
		delete(s.pods, id)
		return &struct{}{}, nil
	}
	if s.parked[id] != nil {
		return &struct{}{}, nil
	}
	return nil, zip.ErrNotFound("sandbox not found")
}

func (s *Sandboxes) end(ctx context.Context, in *client.EndIn) (*struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pods, in.ID)
	delete(s.parked, in.ID)
	return &struct{}{}, nil
}

// rel reads a caller's path as one relative to the workdir. Callers send both forms
// — the real sandbox confines an absolute path and accepts a relative one — so the
// fake has to mean the same file either way.
func rel(p string) string {
	return strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(p), Workdir), "/")
}
