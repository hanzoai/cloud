package audit

import (
	"context"
	"testing"
	"time"
)

func TestToWire_MapsEveryField(t *testing.T) {
	r := Record{
		Seq:      7,
		Time:     time.Date(2026, 7, 4, 12, 30, 0, 0, time.UTC),
		Actor:    Actor{Org: "maxpower", Sub: "dave", Email: "dave@maxpower.ai"},
		Action:   "machine.create",
		Resource: Resource{Type: "machine", ID: "m-1"},
		Auth:     AuthContext{Method: "jwt", IsAdmin: true},
		Outcome:  Outcome{Result: "success", Status: 201, Reason: "ok"},
		SourceIP: "1.2.3.4", UserAgent: "test-agent", RequestID: "req-1",
		Method: "POST", Path: "/v1/machines",
		PrevHash: "aa", Hash: "bb",
	}
	w := r.ToWire()
	if w.Seq != 7 || w.Time != "2026-07-04T12:30:00Z" {
		t.Fatalf("seq/time: %+v", w)
	}
	if w.Org != "maxpower" || w.Sub != "dave" || w.Email != "dave@maxpower.ai" {
		t.Fatalf("actor: %+v", w)
	}
	if w.Action != "machine.create" || w.Resource != "machine" || w.ResourceID != "m-1" {
		t.Fatalf("action/resource: %+v", w)
	}
	if w.Auth != "jwt" || !w.IsAdmin {
		t.Fatalf("auth: %+v", w)
	}
	if w.Result != "success" || w.Status != 201 || w.Reason != "ok" {
		t.Fatalf("outcome: %+v", w)
	}
	if w.Method != "POST" || w.Path != "/v1/machines" || w.SourceIP != "1.2.3.4" {
		t.Fatalf("request: %+v", w)
	}
	if w.Hash != "bb" || w.PrevHash != "aa" {
		t.Fatalf("chain: %+v", w)
	}
}

func TestQuery_ResourceIDFilter(t *testing.T) {
	rec, err := Open(":memory:", nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = rec.Close() }()
	ctx := context.Background()
	for _, id := range []string{"m-1", "m-1", "m-2"} {
		if _, err := rec.Append(ctx, Record{
			Actor: Actor{Org: "o"}, Action: "machine.op",
			Resource: Resource{Type: "machine", ID: id}, Outcome: Outcome{Result: "success"},
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	rows, total, err := rec.Query(ctx, Filter{ResourceID: "m-1"})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("resourceId filter: want 2 rows on m-1, got %d rows total %d", len(rows), total)
	}
	for _, r := range rows {
		if r.Resource.ID != "m-1" {
			t.Fatalf("filter leaked %q", r.Resource.ID)
		}
	}
}
