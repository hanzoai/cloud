package catalog

// split_test.go — the catalog as it is actually DEPLOYED: its own process, with
// the index in another one.
//
// The rest of this suite mounts the index and the lens on ONE app (catalog_test.go
// `mount`). That was the whole fleet once, and it is the topology this deployment
// stopped having. In it index.Ready() is true, so every read and every write takes
// the in-process leg and the plane leg — the ONLY leg production runs — was never
// executed by a test at all.
//
// That gap is the entire bug. catalog's reconcile called index.Reconcile, which
// serves out of the index's own process-level global; in the catalog process that
// global is nil and always will be. So the hourly sync assembled the corpus
// correctly and then dropped it on the floor with "index: not mounted", the
// published catalog was never written a single time, and GET /v1/catalog answered
// 200 with {"data":[],"total":0} — a page that reads as a platform on which
// nobody has built anything. The suite stayed green throughout, because the suite
// was the fused binary.
//
// So these tests refuse the in-process leg on purpose and go over a real unix
// socket to a real peer, which is the only arrangement that can prove the corpus
// reaches the store.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/index"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// runDir points this test's plane at a directory of its own, and it is
// deliberately not t.TempDir(): that name carries the TEST's name, and a unix
// socket path is capped near 104 bytes. Over the cap the failure is
// "connect: invalid argument" — an errno that reads like a broken call rather
// than a long name, and it arrives identically whether a peer is there or not.
// The plane's own suite keeps a short dir for exactly this reason.
func runDir(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("", "cx")
	if err != nil {
		t.Fatalf("run dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	plane.Unbind()
	t.Cleanup(plane.Unbind)
}

// standIn is the index peer as another PROCESS presents it: one socket, the
// reconcile op, and a record of what arrived. It records the TENANT too, because
// the org rides the caller and never the argument — so capturing it here is what
// proves the corpus was published as the public org rather than as nobody.
type standIn struct {
	mu   sync.Mutex
	org  string
	uid  string
	pk   string
	docs []map[string]any
}

func (s *standIn) seen() (string, string, string, []map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.org, s.uid, s.pk, s.docs
}

// peer serves the index's reconcile op on the index's canonical socket, and
// first insists that this process has NO index of its own — without that the
// write takes the in-process leg and the test silently checks nothing, which is
// exactly how the original defect survived a green suite.
func peer(t *testing.T) *standIn {
	t.Helper()
	if index.Ready() {
		t.Fatal("split test: an index is mounted in this process, so the plane leg cannot be reached")
	}
	runDir(t)
	s := &standIn{}
	app := zip.New(zip.Config{AppName: "index", DisableStartupMessage: true})
	zip.Post[plane.IndexReconcileIn, plane.IndexReconcileOut](app, "/index/reconcile",
		func(ctx context.Context, in *plane.IndexReconcileIn) (*plane.IndexReconcileOut, error) {
			s.mu.Lock()
			defer s.mu.Unlock()
			s.org, s.uid, s.pk = cloud.Who(ctx).Org, in.UID, in.PrimaryKey
			for _, raw := range in.Docs {
				var d map[string]any
				if err := json.Unmarshal(raw, &d); err != nil {
					return nil, err
				}
				s.docs = append(s.docs, d)
			}
			return &plane.IndexReconcileOut{Kept: len(in.Docs), Removed: 0}, nil
		}, zip.WithOperationID(plane.IndexReconcile))
	go func() { _ = app.Listen(zip.SocketPath("index")) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	for i := 0; i < 200; i++ {
		if c, err := net.Dial("unix", zip.SocketPath("index")); err == nil {
			_ = c.Close()
			return s
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("index stand-in never began listening at %s", zip.SocketPath("index"))
	return nil
}

// TestWriteReachesTheIndexProcess is the regression, and it fails on the code
// this replaces with "index: not mounted" — the error the deployed fleet returned
// every hour while the catalog read as empty.
func TestWriteReachesTheIndexProcess(t *testing.T) {
	s := peer(t)

	kept, removed, err := reconcile(context.Background(), PublicOrg, []Entry{
		{ID: "hanzo/ui", Org: "hanzo", Name: "ui", Kind: "repo", Origin: OriginProduct, Forkable: true},
		{ID: "zoo/gym", Org: "zoo", Name: "gym", Kind: "site", Origin: OriginCommunity},
	})
	if err != nil {
		t.Fatalf("reconcile over the plane: %v", err)
	}
	if kept != 2 || removed != 0 {
		t.Fatalf("reconcile reported kept=%d removed=%d, want 2 and 0", kept, removed)
	}

	org, uid, pk, docs := s.seen()
	// The corpus is published as the public org, which is the whole tenancy rule:
	// a name no principal can mint, stated by a background job that has no request
	// behind it to be overridden by.
	if org != PublicOrg {
		t.Errorf("the index was asked as %q, want %q", org, PublicOrg)
	}
	if uid != "catalog" || pk != "id" {
		t.Errorf("index addressed as uid=%q pk=%q, want catalog and id", uid, pk)
	}
	if len(docs) != 2 {
		t.Fatalf("the peer received %d documents, want 2", len(docs))
	}
	if got := docs[0]["id"]; got != "hanzo/ui" {
		t.Errorf("first document id %v, want hanzo/ui", got)
	}
	// Provenance is stamped on READ, so a stored row must not CLAIM one: a row
	// frozen as "public" would keep saying so after it was read out of an org's
	// private corpus. The key itself survives the wire — Scope is not omitempty,
	// because the read contract has it present on every row it returns — so the
	// invariant to hold is that it crosses empty.
	if got := docs[0]["scope"]; got != "" {
		t.Errorf("scope crossed as %q; provenance belongs to the read, not the corpus", got)
	}
	// forkable=false is an ANSWER, so it has to survive the wire. Dropped, a
	// client cannot tell "you may not fork this" from "nobody said".
	if got, ok := docs[1]["forkable"]; !ok || got != false {
		t.Errorf("second document forkable=%v (present=%v), want false and present", got, ok)
	}
}

// TestUnkeyedRowsNeverReachTheIndex holds the one filter the write applies. A row
// with no id could never be pruned by a later swap, so it would sit in the corpus
// forever — the one way this reconcile could leak rows it can no longer see.
func TestUnkeyedRowsNeverReachTheIndex(t *testing.T) {
	s := peer(t)

	if _, _, err := reconcile(context.Background(), PublicOrg, []Entry{
		{ID: "", Org: "hanzo", Name: "nameless"},
		{ID: "hanzo/real", Org: "", Name: "orgless"},
		{ID: "hanzo/keeper", Org: "hanzo", Name: "keeper"},
	}); err != nil {
		t.Fatalf("reconcile over the plane: %v", err)
	}

	_, _, _, docs := s.seen()
	if len(docs) != 1 {
		t.Fatalf("the peer received %d documents, want only the keyed one", len(docs))
	}
	if got := docs[0]["id"]; got != "hanzo/keeper" {
		t.Errorf("document id %v, want hanzo/keeper", got)
	}
}

// TestNoIndexAnywhereIsAPeerFault separates the two failures a write can have, so
// an operator reading a log can act on it. "There is no index in this deployment"
// is ErrNoPeer; it is NOT the in-process ErrNotMounted, which in the split fleet
// was never a real fact about the deployment at all — only about the wrong half
// of it being asked.
func TestNoIndexAnywhereIsAPeerFault(t *testing.T) {
	if index.Ready() {
		t.Fatal("split test: an index is mounted in this process")
	}
	runDir(t) // an empty run dir: no peer serves here

	_, _, err := reconcile(context.Background(), PublicOrg,
		[]Entry{{ID: "hanzo/ui", Org: "hanzo", Name: "ui"}})
	if err == nil {
		t.Fatal("a write with no index anywhere reported success")
	}
	if !errors.Is(err, plane.ErrNoPeer) {
		t.Errorf("write failed with %v, want ErrNoPeer", err)
	}
	if errors.Is(err, index.ErrNotMounted) {
		t.Error("write reported the in-process ErrNotMounted, so it never took the plane leg")
	}
}
