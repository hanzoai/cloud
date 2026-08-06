package exec

// sandboxpeer_test.go — a sandbox peer, on the REAL plane.
//
// The double answers the same five ops apps/sandbox publishes, registered on
// cloud.Plane() and served under the sandboxes app name, so every assertion in this
// package goes through the actual composition: cloud.Ask resolves the peer, zip
// dispatches the op, the reply decodes into the declared type. What is faked is the
// POD — a map instead of a gVisor container — and nothing else.
//
// It is a fake of the pod and not of the plane on purpose. A double at the Ask
// boundary would have proved that exec calls something; this proves exec composes
// over the ops sandbox actually declares, so an op renamed or a field moved fails
// here instead of in production.

import (
	"context"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// fakePod is one sandbox's filesystem and the shell semantics the two commands exec
// actually sends depend on: the marker write, and `find -newer`.
type fakePod struct {
	files map[string][]byte
	mtime map[string]time.Time
	marks map[string]time.Time
}

type peerState struct {
	mu   sync.Mutex
	pods map[string]*fakePod
	// runs records every argv the peer was asked to run, so a test can assert on
	// what exec sent rather than only on what came back.
	runs []([]string)
	// program is what a run "produces": files the fake writes on the caller's
	// behalf, keyed by path relative to the workdir.
	program func(id string, argv []string) (stdout, stderr string, code int, wrote map[string][]byte)
}

var peers = &peerState{pods: map[string]*fakePod{}}

const fakeWorkdir = "/mnt/data"

// servePeer registers the five sandbox ops and binds the sandboxes socket, which is
// what apps/sandbox's Mount does in a real process.
func servePeer(t *testing.T) *peerState {
	t.Helper()
	// A run dir this test owns. The default is /run/hanzo, which a test process has
	// no business creating and no permission to.
	dir, err := os.MkdirTemp("", "sbpeer")
	if err != nil {
		t.Fatalf("run dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", "")
	t.Setenv("CLOUD_RUN_DIR", dir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)

	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)
	peers = &peerState{pods: map[string]*fakePod{}}

	p := cloud.Plane()
	zip.Post[plane.LeaseIn, plane.Leased](p, "/sandbox/lease", peers.lease,
		zip.WithOperationID(plane.SandboxLease))
	zip.Post[plane.RunIn, plane.Ran](p, "/sandbox/run", peers.run,
		zip.WithOperationID(plane.SandboxRun))
	zip.Post[plane.PathIn, plane.Blob](p, "/sandbox/read", peers.read,
		zip.WithOperationID(plane.SandboxRead))
	zip.Post[plane.WriteIn, plane.Wrote](p, "/sandbox/write", peers.write,
		zip.WithOperationID(plane.SandboxWrite))
	zip.Post[plane.EndIn, struct{}](p, "/sandbox/end", peers.end,
		zip.WithOperationID(plane.SandboxEnd))

	stop, serr := cloud.ServePlane(peer, luxlog.NewNoOpLogger())
	if serr != nil {
		t.Fatalf("serve sandboxes plane: %v", serr)
	}
	t.Cleanup(func() { _ = stop() })
	return peers
}

func (s *peerState) get(id string) *fakePod {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pods[id]
}

func (s *peerState) lease(ctx context.Context, in *plane.LeaseIn) (*plane.Leased, error) {
	if cloud.Who(ctx).Org == "" {
		return nil, zip.ErrForbidden("org required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := in.ID
	if id == "" || s.pods[id] == nil {
		id = "m_" + strings.Repeat("a", 4) + string(rune('0'+len(s.pods)))
		s.pods[id] = &fakePod{files: map[string][]byte{}, mtime: map[string]time.Time{},
			marks: map[string]time.Time{}}
	}
	return &plane.Leased{ID: id, Class: "exec", Status: "running", Workdir: fakeWorkdir}, nil
}

func (s *peerState) write(ctx context.Context, in *plane.WriteIn) (*plane.Wrote, error) {
	pod := s.get(in.ID)
	if pod == nil {
		return nil, zip.ErrNotFound("sandbox not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rel := strings.TrimPrefix(strings.TrimPrefix(in.Path, fakeWorkdir), "/")
	pod.files[rel] = in.Data
	pod.mtime[rel] = time.Now()
	return &plane.Wrote{Path: fakeWorkdir + "/" + rel, Bytes: len(in.Data)}, nil
}

func (s *peerState) read(ctx context.Context, in *plane.PathIn) (*plane.Blob, error) {
	pod := s.get(in.ID)
	if pod == nil {
		return nil, zip.ErrNotFound("sandbox not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rel := strings.TrimPrefix(strings.TrimPrefix(in.Path, fakeWorkdir), "/")
	if rel == "" {
		names := make([]string, 0, len(pod.files))
		for n := range pod.files {
			names = append(names, n)
		}
		sort.Strings(names)
		return &plane.Blob{Path: fakeWorkdir, Dir: true, Entries: names}, nil
	}
	b, ok := pod.files[rel]
	if !ok {
		return nil, zip.ErrNotFound("no such path")
	}
	return &plane.Blob{Path: fakeWorkdir + "/" + rel, Data: b}, nil
}

// run interprets the two shapes exec actually sends: the marker-plus-program line,
// and the `find -newer` sweep. Anything else is recorded and answers empty, which is
// how a test notices exec started sending a third.
func (s *peerState) run(ctx context.Context, in *plane.RunIn) (*plane.Ran, error) {
	pod := s.get(in.ID)
	if pod == nil {
		return nil, zip.ErrNotFound("sandbox not found")
	}
	s.mu.Lock()
	s.runs = append(s.runs, in.Argv)
	s.mu.Unlock()

	line := ""
	if len(in.Argv) >= 3 {
		line = in.Argv[2]
	}
	switch {
	case strings.HasPrefix(line, ": > "+marker):
		s.mu.Lock()
		pod.marks[marker] = time.Now()
		s.mu.Unlock()
		time.Sleep(time.Millisecond)
		out, errout, code, wrote := "", "", 0, map[string][]byte(nil)
		if s.program != nil {
			out, errout, code, wrote = s.program(in.ID, in.Argv)
		}
		s.mu.Lock()
		for p, b := range wrote {
			pod.files[p] = b
			pod.mtime[p] = time.Now()
		}
		s.mu.Unlock()
		return &plane.Ran{ExitCode: code, Stdout: out, Stderr: errout}, nil

	case strings.Contains(line, "-newer "+marker):
		s.mu.Lock()
		defer s.mu.Unlock()
		var names []string
		for n, mt := range pod.mtime {
			if mt.After(pod.marks[marker]) {
				names = append(names, "./"+n)
			}
		}
		sort.Strings(names)
		return &plane.Ran{Stdout: strings.Join(names, "\n")}, nil

	case strings.Contains(line, "date -u -r"):
		s.mu.Lock()
		defer s.mu.Unlock()
		var rows []string
		names := make([]string, 0, len(pod.files))
		for n := range pod.files {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			rows = append(rows, pod.mtime[n].UTC().Format("2006-01-02T15:04:05Z"), "./"+n)
		}
		return &plane.Ran{Stdout: strings.Join(rows, "\n")}, nil
	}
	return &plane.Ran{}, nil
}

func (s *peerState) end(ctx context.Context, in *plane.EndIn) (*struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pods, in.ID)
	return &struct{}{}, nil
}

// ranLines is every shell line the peer was asked to run, for assertions about what
// exec sent.
func (s *peerState) ranLines() []string {
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

func (s *peerState) lastArgs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.runs) - 1; i >= 0; i-- {
		if len(s.runs[i]) >= 3 && strings.HasPrefix(s.runs[i][2], ": > "+marker) {
			return s.runs[i][4:]
		}
	}
	return nil
}
