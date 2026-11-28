package pubsub

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"testing"
	"time"

	natsio "github.com/nats-io/nats.go"

	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/pubsub/server"
)

const gib = int64(1) << 30

func TestTheLimitTheAccountIsTold(t *testing.T) {
	for _, c := range []struct {
		name                  string
		store, reserved, want int64
		replicas              int
		got                   int64
	}{
		{"a node with room for the ceilings is left unlimited", 18 * gib, 0, 0, 1, 0},
		{"and so is its restart, with the ceilings restored", 17 * gib, 9 * gib, 0, 1, 0},
		{"a small node is told what it holds", 4 * gib, 0, 0, 1, (4*gib - 1) / 160 * 100},
		{"three replicas need three ceilings before leaving unlimited", 18 * gib, 0, 0, 3, (18*gib - 1) / 160 * 100},
		{"three replicas with room for all three ceilings stay unlimited", 30 * gib, 0, 0, 3, 0},
		{"a declared budget is the limit", 20 * gib, 0, gib, 1, gib},
		{"a declared budget on a small node is still the limit", 4 * gib, 0, gib, 1, gib},
		{"a budget larger than the node holds is what the node holds", 2 * gib, 0, 8 * gib, 1, (2*gib - 1) / 160 * 100},
		{"a full store states nothing", gib, gib, 0, 1, 0},
		{"a full store has zero room even when a budget is declared", gib, 2 * gib, gib, 1, 0},
		{"no store, no limit", 0, 0, gib, 1, 0},
	} {
		if got := limit(c.store, c.reserved, c.want, c.replicas); got != c.got {
			t.Errorf("%s: limit(%d, %d, %d, %d) = %d, want %d", c.name, c.store, c.reserved, c.want, c.replicas, got, c.got)
		}
	}
}

// The limit a boot is given is the limit the next boot is given, with the streams
// the first one declared restored under it: nothing shrinks across a restart.
func TestTheLimitHoldsAcrossARestart(t *testing.T) {
	for _, replicas := range []int{1, 3} {
		for _, store := range []int64{gib, 4 * gib, 8 * gib} {
			first := limit(store, 0, 0, replicas)
			var perNode, accountWide int64
			for _, r := range bus.Reservations {
				b := r.Of(first, replicas)
				perNode += b
				accountWide += b * int64(replicas)
			}
			for _, reserved := range []int64{perNode, accountWide} {
				if again := limit(store, reserved, 0, replicas); again != first {
					t.Errorf("replicas=%d, a %d store: %d at first boot, %d with %d restored", replicas, store, first, again, reserved)
				}
				if first+reserved+1 > store {
					t.Errorf("replicas=%d, a %d store: a limit of %d beside %d reserved is one the server refuses", replicas, store, first, reserved)
				}
			}
		}
	}
}

func TestExhaustedDeclaredBudgetFailsClosed(t *testing.T) {
	const store = 64 << 20
	ns, err := server.NewServer(&server.Options{
		Host:              "127.0.0.1",
		Port:              -1,
		JetStream:         true,
		JetStreamMaxStore: store,
		StoreDir:          t.TempDir(),
		NoSigs:            true,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	ns.Start()
	t.Cleanup(func() { ns.Shutdown(); ns.WaitForShutdown() })
	if !ns.ReadyForConnections(5 * time.Second) {
		t.Fatal("server not ready")
	}

	nc, err := natsio.Connect(ns.ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	// Exhaust the server's store so limit(cfg.MaxStore, reserved, want, 1) == 0.
	if _, err := js.AddStream(&natsio.StreamConfig{
		Name: "FULL", Subjects: []string{"full.>"}, Storage: natsio.FileStorage, MaxBytes: store,
	}); err != nil {
		t.Fatalf("seed full stream: %v", err)
	}

	// Undeclared budget (want == 0) still leaves the account untouched.
	if held, err := budget(ns, 0); err != nil || held != 0 {
		t.Fatalf("undeclared budget on full store = (%d, %v), want (0, nil)", held, err)
	}
	// Declared budget (want > 0) must fail explicitly instead of leaving MaxStore: -1.
	if held, err := budget(ns, 32<<20); err == nil {
		t.Fatalf("declared budget on exhausted store succeeded with held=%d; want explicit error", held)
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
			MaxBytes: r.Of(acct.Limits.MaxStore, 1),
		}); err != nil {
			t.Fatalf("a stream at its share (%d of %d) was refused: %v", r.Of(acct.Limits.MaxStore, 1), store, err)
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

// On an un-tiered account, JetStream charges Replicas * MaxBytes against MaxStore.
// Verify against a real 3-node JetStream cluster that unadjusted shares fail with
// 10047 while replica-aware Of/Within reservations fit cleanly at Replicas=3.
func TestReplicatedStreamsFitTheAccountBudget(t *testing.T) {
	const (
		replicas = 3
		store    = 300 << 20
	)
	ports := make([]int, replicas)
	lns := make([]net.Listener, replicas)
	var routes []*url.URL
	for i := range replicas {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("reserve route port %d: %v", i, err)
		}
		lns[i] = ln
		ports[i] = ln.Addr().(*net.TCPAddr).Port
		u, err := url.Parse("nats-route://127.0.0.1:" + strconv.Itoa(ports[i]))
		if err != nil {
			t.Fatalf("parse route %d: %v", i, err)
		}
		routes = append(routes, u)
	}
	for _, ln := range lns {
		_ = ln.Close()
	}

	var nodes []*server.Server
	for i := range replicas {
		ns, err := server.NewServer(&server.Options{
			ServerName: "n" + strconv.Itoa(i),
			Host:       "127.0.0.1",
			Port:       -1,
			JetStream:  true,
			StoreDir:   t.TempDir(),
			NoSigs:     true,
			Cluster: server.ClusterOpts{
				Name: "test-js-r3",
				Host: "127.0.0.1",
				Port: ports[i],
			},
			Routes: routes,
		})
		if err != nil {
			t.Fatalf("new server %d: %v", i, err)
		}
		nodes = append(nodes, ns)
	}
	for _, ns := range nodes {
		go ns.Start()
	}
	t.Cleanup(func() {
		for _, ns := range nodes {
			ns.Shutdown()
		}
		for _, ns := range nodes {
			ns.WaitForShutdown()
		}
	})
	for i, ns := range nodes {
		if !ns.ReadyForConnections(10 * time.Second) {
			t.Fatalf("server %d not ready", i)
		}
	}

	nc, err := natsio.Connect(nodes[0].ClientURL())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}

	// Wait until all 3 JetStream peers are online in the meta group (leader election
	// succeeds at quorum=2, before the 3rd peer's heartbeat arrives).
	deadline := time.Now().Add(15 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if _, err := js.AddStream(&natsio.StreamConfig{
			Name: "READY", Subjects: []string{"ready"}, Replicas: replicas,
		}); err == nil {
			_ = js.DeleteStream("READY")
			ready = true
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !ready {
		t.Fatal("3-node JetStream cluster did not reach 3-peer placement readiness")
	}

	for _, ns := range nodes {
		if err := ns.GlobalAccount().UpdateJetStreamLimits(map[string]server.JetStreamAccountLimits{"": {
			MaxMemory: -1, MaxStore: store, MaxStreams: -1, MaxConsumers: -1, MaxAckPending: -1,
			MemoryMaxStreamBytes: -1, StoreMaxStreamBytes: -1,
		}}); err != nil {
			t.Fatalf("update account limits: %v", err)
		}
	}

	// Prove the bug: unadjusted single-replica share (50% * 3 = 150% of store) is rejected with 10047.
	_, err = js.AddStream(&natsio.StreamConfig{
		Name: "UNADJUSTED", Subjects: []string{"unadj.>"}, Storage: natsio.FileStorage,
		Replicas: replicas, MaxBytes: bus.Events.Of(store, 1),
	})
	var api *natsio.APIError
	if !errors.As(err, &api) || api.ErrorCode != bus.StorageRefused {
		t.Fatalf("unadjusted R=3 share = %v, want 10047", err)
	}

	// Replica-aware shares (50%/3 + 10%/3 => 60% of MaxStore total) both fit cleanly.
	for i, r := range bus.Reservations {
		acct, err := js.AccountInfo()
		if err != nil {
			t.Fatalf("account info: %v", err)
		}
		name := "R3_" + strconv.Itoa(i)
		want := r.Within(acct.Limits.MaxStore, int64(acct.ReservedStore), 0, replicas)
		if _, err := js.AddStream(&natsio.StreamConfig{
			Name: name, Subjects: []string{name + ".>"}, Storage: natsio.FileStorage,
			Replicas: replicas, MaxBytes: want,
		}); err != nil {
			t.Fatalf("R=3 stream %s at %d bytes refused: %v", name, want, err)
		}
	}
}
