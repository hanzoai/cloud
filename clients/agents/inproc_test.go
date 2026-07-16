package agents

import (
	"context"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
)

// mountInproc points the package `mounted` singleton at a throwaway store so the
// in-process session API (inproc.go) exercises the SAME store the HTTP control
// plane uses. Restores the previous singleton on cleanup.
func mountInproc(t *testing.T) {
	t.Helper()
	prev := mounted
	mounted = &cloud.Service[state]{
		Base:  cloud.Base{Log: luxlog.New("test")},
		State: state{store: testSessionStore(t)},
	}
	t.Cleanup(func() { mounted = prev })
}

func TestInproc_OpenLogClose_Lifecycle(t *testing.T) {
	mountInproc(t)
	ctx := context.Background()

	id, err := OpenSession(ctx, SessionOpen{Org: "acme", Actor: "acme/u1", Agent: "hanzo", Title: "code: api — fix bug"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	got, err := mounted.State.store.GetSession(ctx, "acme", id)
	if err != nil || got.Status != StatusRunning || got.RootID != id {
		t.Fatalf("session not running root: %+v (%v)", got, err)
	}

	if err := LogSessionEvent(ctx, "acme", id, KindStatus, "acme/u1", []byte(`{"status":"started"}`)); err != nil {
		t.Fatalf("log status: %v", err)
	}
	if err := LogSessionEvent(ctx, "acme", id, KindToolCall, "acme/u1", []byte(`{"step":"clone"}`)); err != nil {
		t.Fatalf("log tool: %v", err)
	}
	if n, _ := mounted.State.store.CountEvents(ctx, "acme", id); n != 2 {
		t.Fatalf("want 2 events, got %d", n)
	}

	if err := CloseSession(ctx, "acme", id, StatusDone); err != nil {
		t.Fatalf("close: %v", err)
	}
	got, _ = mounted.State.store.GetSession(ctx, "acme", id)
	if got.Status != StatusDone || got.EndedAt == 0 {
		t.Fatalf("want terminal done with ended_at, got %+v", got)
	}
	// Double-close is a monotonic no-op.
	if err := CloseSession(ctx, "acme", id, StatusError); err != nil {
		t.Fatalf("double close should be a no-op, got %v", err)
	}
	got, _ = mounted.State.store.GetSession(ctx, "acme", id)
	if got.Status != StatusDone {
		t.Fatalf("terminal state must stay done, got %q", got.Status)
	}
}

func TestInproc_TenantIsolation_ForeignOrgCannotTouchSession(t *testing.T) {
	mountInproc(t)
	ctx := context.Background()
	id, err := OpenSession(ctx, SessionOpen{Org: "acme", Actor: "acme/u1", Agent: "hanzo", Title: "t"})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// A different org can neither append to nor close acme's session.
	if err := LogSessionEvent(ctx, "evil", id, KindLog, "evil/u", []byte(`{}`)); err == nil {
		t.Fatal("foreign org must NOT append to another org's session")
	}
	if err := CloseSession(ctx, "evil", id, StatusDone); err == nil {
		t.Fatal("foreign org must NOT close another org's session")
	}
	// acme's session is untouched (no stray events, still running).
	if n, _ := mounted.State.store.CountEvents(ctx, "acme", id); n != 0 {
		t.Fatalf("foreign writes leaked in: %d events", n)
	}
	got, _ := mounted.State.store.GetSession(ctx, "acme", id)
	if got.Status != StatusRunning {
		t.Fatalf("session should still be running, got %q", got.Status)
	}
}

func TestInproc_Validation_FailClosed(t *testing.T) {
	mountInproc(t)
	ctx := context.Background()
	if _, err := OpenSession(ctx, SessionOpen{Org: "", Actor: "a", Agent: "hanzo", Title: "t"}); err == nil {
		t.Fatal("empty org must fail")
	}
	if _, err := OpenSession(ctx, SessionOpen{Org: "acme", Actor: "a", Agent: "", Title: "t"}); err == nil {
		t.Fatal("empty agent must fail")
	}
	id, _ := OpenSession(ctx, SessionOpen{Org: "acme", Actor: "a", Agent: "hanzo", Title: "t"})
	if err := LogSessionEvent(ctx, "acme", id, "not-a-kind", "a", []byte(`{}`)); err == nil {
		t.Fatal("invalid kind must fail")
	}
	if err := LogSessionEvent(ctx, "acme", id, KindLog, "a", []byte(`{not json`)); err == nil {
		t.Fatal("malformed JSON payload must fail")
	}
	if err := CloseSession(ctx, "acme", id, StatusRunning); err == nil {
		t.Fatal("close to a non-terminal status must fail")
	}
}

func TestInproc_NotMounted_FailsClosed(t *testing.T) {
	prev := mounted
	mounted = nil
	t.Cleanup(func() { mounted = prev })
	if _, err := OpenSession(context.Background(), SessionOpen{Org: "acme", Actor: "a", Agent: "hanzo", Title: "t"}); err == nil {
		t.Fatal("unmounted OpenSession must fail closed")
	}
}
