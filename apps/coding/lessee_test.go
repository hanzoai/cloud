package coding

// lessee_test.go — a coding run's sandbox is its PERSON'S. apps/sandbox records
// the caller of a lease as the sandbox's lessee and lets nobody else in the org
// act on it but an admin; so every call the runner makes has to state the run's
// person, from the lease to the goodbye, or the run could not end its own lease
// and a colleague's turn could reach the write key armed into it.

import (
	"context"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	sandboxpeer "github.com/hanzoai/cloud/client/sandbox"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/kit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// TestSandboxRun_EverySandboxCallIsTheRunsPerson runs on the context a real run
// gets — the tenant stated and nobody else (runContext) — and reads back the caller
// every sandbox op was reached as.
func TestSandboxRun_EverySandboxCallIsTheRunsPerson(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", kit.Sockets(t))
	var (
		mu  sync.Mutex
		saw = map[string][]zip.Caller{}
	)
	note := func(op string, ctx context.Context) {
		mu.Lock()
		defer mu.Unlock()
		saw[op] = append(saw[op], zip.CallerOf(ctx))
	}
	sandboxes := zip.New(zip.Config{AppName: sandboxpeer.App, DisableStartupMessage: true})
	sandboxes.Post("/sandbox/lease", func(ctx context.Context, in *client.LeaseIn) (*client.Leased, error) {
		note("lease", ctx)
		return &client.Leased{ID: "sbx_1", Class: in.Class, Status: "running", Workdir: "/work"}, nil
	}, zip.WithOperationID(client.SandboxLease))
	sandboxes.Post("/sandbox/run", func(ctx context.Context, _ *client.RunIn) (*client.Ran, error) {
		note("run", ctx)
		return &client.Ran{Stdout: theSHA + "\n"}, nil
	}, zip.WithOperationID(client.SandboxRun))
	sandboxes.Post("/sandbox/end", func(ctx context.Context, _ *client.EndIn) (*struct{}, error) {
		note("end", ctx)
		return &struct{}{}, nil
	}, zip.WithOperationID(client.SandboxEnd))
	commerce := zip.New(zip.Config{AppName: "commerce", DisableStartupMessage: true})
	commerce.Post("/commerce/authorize", func(context.Context, *client.AuthorizeIn) (*client.Verdict, error) {
		return &client.Verdict{OK: true}, nil
	}, zip.WithOperationID(client.FinanceAuthorize))
	for name, app := range map[string]*zip.App{sandboxpeer.App: sandboxes, "commerce": commerce} {
		plane.Bind()
		go func(path string) { _ = app.Listen(path) }(zip.SocketPath(name))
		t.Cleanup(func() { _ = app.Shutdown() })
		waitListening(t, name)
	}

	if _, err := (sandboxRunner{}).Run(cloud.For(context.Background(), "acme"), "acme", "u_1", aRun(), nil); err != nil {
		t.Fatalf("run: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(saw["end"]) != 0 {
		t.Fatal("the run ended its sandbox; a run's sandbox is kept for its person")
	}
	for _, op := range []string{"lease", "run"} {
		if len(saw[op]) == 0 {
			t.Fatalf("the run never called %s", op)
		}
		for _, who := range saw[op] {
			if who.Org != "acme" || who.User != "u_1" {
				t.Fatalf("%s was called as %+v — a sandbox op of this run must be the run's person in its org", op, who)
			}
		}
	}
}
