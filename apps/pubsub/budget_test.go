package pubsub

import (
	"errors"
	"strconv"
	"testing"

	natsio "github.com/nats-io/nats.go"

	"github.com/hanzoai/cloud/sdk/bus"
)

const gib = int64(1) << 30

func TestTheLimitTheAccountIsTold(t *testing.T) {
	for _, c := range []struct {
		name                  string
		store, reserved, want int64
		got                   int64
	}{
		{"a node with room for the ceilings is left unlimited", 18 * gib, 0, 0, 0},
		{"and so is its restart, with the ceilings restored", 17 * gib, 9 * gib, 0, 0},
		{"a small node is told what it holds", 4 * gib, 0, 0, (4*gib - 1) / 160 * 100},
		{"a declared budget is the limit", 20 * gib, 0, gib, gib},
		{"a declared budget on a small node is still the limit", 4 * gib, 0, gib, gib},
		{"a budget larger than the node holds is what the node holds", 2 * gib, 0, 8 * gib, (2*gib - 1) / 160 * 100},
		{"a full store states nothing", gib, gib, 0, 0},
		{"a full store states nothing, declared or not", gib, 2 * gib, gib, 0},
		{"no store, no limit", 0, 0, gib, 0},
	} {
		if got := limit(c.store, c.reserved, c.want); got != c.got {
			t.Errorf("%s: limit(%d, %d, %d) = %d, want %d", c.name, c.store, c.reserved, c.want, got, c.got)
		}
	}
}

// The limit a boot is given is the limit the next boot is given, with the streams
// the first one declared restored under it: nothing shrinks across a restart.
func TestTheLimitHoldsAcrossARestart(t *testing.T) {
	for _, store := range []int64{gib, 4 * gib, 8 * gib} {
		first := limit(store, 0, 0)
		var reserved int64
		for _, r := range bus.Reservations {
			reserved += r.Of(first)
		}
		if again := limit(store, reserved, 0); again != first {
			t.Errorf("a %d store: %d at first boot, %d with %d restored", store, first, again, reserved)
		}
		if first+reserved+1 > store {
			t.Errorf("a %d store: a limit of %d beside %d reserved is one the server refuses", store, first, reserved)
		}
	}
}

func TestABadBudgetStopsBoot(t *testing.T) {
	for _, v := range []string{"lots", "-1", "0", "2Gi"} {
		t.Setenv(storeEnv, v)
		if _, err := declared(); err == nil {
			t.Errorf("%s=%q was read as a budget", storeEnv, v)
		}
	}
	t.Setenv(storeEnv, "")
	if n, err := declared(); n != 0 || err != nil {
		t.Errorf("no budget read as %d, %v", n, err)
	}
}

// The defect, end to end: on a plane with a budget, the account says what it holds,
// every reserving stream fits at its share, and the production ceiling that used to
// be asked for is the one declaration refused.
func TestADeclaredBudgetIsWhatTheAccountReports(t *testing.T) {
	const store = 256 << 20
	t.Setenv(storeEnv, strconv.Itoa(store))
	url := mountForTest(t)

	nc, err := natsio.Connect(url)
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	acct, err := js.AccountInfo()
	if err != nil {
		t.Fatalf("account info: %v", err)
	}
	if acct.Limits.MaxStore != store {
		t.Fatalf("the account reports a file store of %d, want the declared %d", acct.Limits.MaxStore, store)
	}

	for i, r := range bus.Reservations {
		name := "RESERVING" + strconv.Itoa(i)
		if _, err := js.AddStream(&natsio.StreamConfig{
			Name: name, Subjects: []string{name + ".>"}, Storage: natsio.FileStorage,
			MaxBytes: r.Of(acct.Limits.MaxStore),
		}); err != nil {
			t.Fatalf("a stream at its share (%d of %d) was refused: %v", r.Of(acct.Limits.MaxStore), store, err)
		}
	}
	// A stream that states no ceiling reserves nothing and is always taken.
	if _, err := js.AddStream(&natsio.StreamConfig{Name: "OPEN", Subjects: []string{"open.>"}}); err != nil {
		t.Fatalf("a stream with no ceiling was refused: %v", err)
	}

	_, err = js.AddStream(&natsio.StreamConfig{
		Name: "CEILING", Subjects: []string{"ceiling.>"}, Storage: natsio.FileStorage,
		MaxBytes: bus.Events.Ceiling,
	})
	var api *natsio.APIError
	if !errors.As(err, &api) || api.ErrorCode != 10047 {
		t.Fatalf("a production ceiling on a %d store: %v, want JetStream's 10047", store, err)
	}
}
