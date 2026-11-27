package allowance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/client"
)

// planned is a service over a real store whose tier is whatever *plan says now, and
// whose pool answers p (or err), counting how often it was asked.
func planned(t *testing.T, plan *string, p *client.Pool, err error, asked *int) *service {
	t.Helper()
	return &service{
		store: testStore(t),
		tier:  func(context.Context, string, string) (string, error) { return *plan, nil },
		pool: func(context.Context, string) (*client.Pool, error) {
			*asked++
			return p, err
		},
		log: luxlog.Default().New("subsystem", "allowance"),
	}
}

// THE FREE PLAN SAYS WHAT IT IS. A free caller's answer is pooled — their calls are
// served from the pool every free user shares — and carries that pool's standing, so
// the usage page can say "limited usage, from a pool shared by all free users" with
// the caller's own remaining allowance beside the pool's state. Nothing is invented:
// the pool is what the ai process said.
func TestAFreeCallerIsPooledAndSeesThePool(t *testing.T) {
	plan, asked := "free", 0
	resets := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC).Unix()
	pool := &client.Pool{State: "exhausted", Resets: resets}
	s := planned(t, &plan, pool, nil, &asked)
	now := time.Date(2026, 9, 28, 15, 20, 0, 0, time.UTC)

	for range 3 {
		if _, err := s.take(context.Background(), calls, "acme", "acme/ann", "acme/ann", now); err != nil {
			t.Fatal(err)
		}
	}
	out, err := s.read(context.Background(), calls, "acme", "acme/ann", "acme/ann", now)
	if err != nil {
		t.Fatal(err)
	}
	s.pooled(context.Background(), "acme", out)

	if !out.Pooled {
		t.Fatal("a free caller is not marked pooled")
	}
	if out.Pool == nil || *out.Pool != *pool {
		t.Fatalf("pool = %+v, want what ai said: %+v", out.Pool, pool)
	}
	if out.Plan != "free" || out.Window != "session" || out.Limit != free || out.Used != 3 || out.Resets != now.Add(fiveHours).Unix() {
		t.Fatalf("standing = %+v, want free, 3 of the session's %d, resetting five hours after the first", out, free)
	}

	// THE WIRE SHAPE the usage page reads.
	raw, _ := json.Marshal(out)
	var wire map[string]any
	_ = json.Unmarshal(raw, &wire)
	for _, k := range []string{"plan", "limit", "used", "window", "resets", "pooled", "pool"} {
		if _, ok := wire[k]; !ok {
			t.Errorf("GET /v1/allowance carries no %q: %s", k, raw)
		}
	}
	p, _ := wire["pool"].(map[string]any)
	if p["state"] != "exhausted" || p["resets"] != float64(resets) {
		t.Errorf("pool on the wire = %v, want exhausted until %d", wire["pool"], resets)
	}
	for _, k := range []string{"keys", "ready"} {
		if _, ok := p[k]; ok {
			t.Errorf("pool on the wire carries %q — how many accounts stand behind it is ours", k)
		}
	}
}

// A PAID PLAN IS NOT POOLED. Its usage is metered in money, so it carries no pool and
// the pool is not even asked.
func TestAPaidCallerIsNotPooled(t *testing.T) {
	plan, asked := "pro", 0
	s := planned(t, &plan, &client.Pool{State: "available"}, nil, &asked)
	out, err := s.read(context.Background(), calls, "acme", "acme/pat", "acme/pat", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.pooled(context.Background(), "acme", out)
	if out.Pooled || out.Pool != nil || asked != 0 {
		t.Fatalf("pro: pooled=%v pool=%+v asked=%d, want not pooled and the pool never asked", out.Pooled, out.Pool, asked)
	}
}

// A POOL THAT DOES NOT ANSWER IS ABSENT, NEVER GUESSED. The caller is still pooled —
// that is a fact about their plan — and the allowance still answers.
func TestAnUnreadablePoolIsAbsent(t *testing.T) {
	plan, asked := "free", 0
	s := planned(t, &plan, nil, errors.New("ai is rolling"), &asked)
	out, err := s.read(context.Background(), calls, "acme", "acme/ann", "acme/ann", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	s.pooled(context.Background(), "acme", out)
	if !out.Pooled || out.Pool != nil || asked != 1 {
		t.Fatalf("pooled=%v pool=%+v asked=%d, want pooled with no pool", out.Pooled, out.Pool, asked)
	}
}

// A FREE CALLER AT THE CAP WHO PAYS IS SERVED ON THE NEXT CALL. The ceiling is read
// through commerce on every ask — nothing here holds a plan — so the moment the
// subscription is recorded, the caller is on the paid plan's terms: unbounded, not
// pooled, not spent.
func TestAFreeCallerAtTheCapWhoPaysPassesAtOnce(t *testing.T) {
	plan, asked := "free", 0
	s := planned(t, &plan, &client.Pool{State: "available"}, nil, &asked)
	now := time.Date(2026, 9, 28, 15, 20, 0, 0, time.UTC)
	ctx := context.Background()

	var out *client.Allowance
	for range free + 1 {
		var err error
		if out, err = s.take(ctx, calls, "acme", "acme/ann", "acme/ann", now); err != nil {
			t.Fatal(err)
		}
	}
	if !out.Spent || out.Window != "session" {
		t.Fatalf("after %d: %+v, want spent on the session", free+1, out)
	}

	plan = "pro" // commerce records the paid subscription

	got, err := s.read(ctx, calls, "acme", "acme/ann", "acme/ann", now)
	if err != nil {
		t.Fatal(err)
	}
	s.pooled(ctx, "acme", got)
	if got.Spent || got.Limit != 0 || got.Plan != "pro" || got.Pooled {
		t.Fatalf("the read after paying: %+v, want pro, unbounded, not pooled, not spent", got)
	}
}
