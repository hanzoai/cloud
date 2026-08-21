package cloud

import "testing"

// WHAT EMBEDDED IAM NEEDS ABOVE ONE REPLICA IS A SHARED STORE, NOT ONE REPLICA.
//
// The rule this locks used to be "iam enabled ⇒ replicas must be 1". It is now
// the condition that was actually behind it: an iam-enabled cloud may run at any
// replica count once IAM_STORE_BACKEND names a store every replica reaches, and
// may not while that store is the per-pod file.
//
// The distinction matters because the two are not the same shape. The old rule
// had no way to become satisfied — a deployment could do everything right and
// still be pinned to one pod — so the fix for it was to edit the rule, which is
// how a stale claim survives. This one is a condition an operator can meet, and
// meeting it is checked rather than asserted.
func TestValidateIAMNeedsASharedStoreAboveOneReplica(t *testing.T) {
	base := func() *Config {
		return &Config{Brand: "hanzo", Domain: "api.hanzo.ai", DataDir: "/var/lib/cloud"}
	}
	cases := []struct {
		name     string
		enable   []string
		replicas int
		store    string
		wantErr  bool
	}{
		// The per-pod file, above one replica: N pods, N identity stores.
		{"file store, 2 replicas -> refuse", []string{"iam", "kms"}, 2, "", true},
		{"file store named explicitly, 3 replicas -> refuse", []string{"iam"}, 3, "sqlite", true},
		// iam is NOT staged, so the empty-Enable "mount everything" default mounts
		// it — and that empty list is the production posture (CLOUD_ENABLE unset),
		// so it is the case that must not slip through. Disabling iam takes a
		// non-empty list that omits it, two cases below.
		{"empty list is iam-enabled, 4 replicas on the file -> refuse", nil, 4, "", true},

		// A shared backend is the whole condition. Any count is then sound.
		{"shared store, 3 replicas -> ok", []string{"iam"}, 3, "sql", false},
		{"shared store, empty list, 12 replicas -> ok", nil, 12, "sql", false},
		{"shared store, 1 replica -> ok", []string{"iam"}, 1, "sql", false},

		// One replica cannot diverge from itself, and 0 is the unmanaged/dev case.
		{"file store, 1 replica -> ok", []string{"iam"}, 1, "", false},
		{"file store, replicas unset -> ok", []string{"iam"}, 0, "", false},

		// No embedded iam, no embedded identity store, nothing to diverge.
		{"iam disabled, 5 replicas -> ok", []string{"kms", "o11y"}, 5, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base()
			c.Enable, c.Replicas, c.IAMStore = tc.enable, tc.replicas, tc.store
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error (iam, replicas=%d, store=%q)", tc.replicas, tc.store)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil (iam, replicas=%d, store=%q)", err, tc.replicas, tc.store)
			}
		})
	}
}

// The shard-routing guard asks the SAME question of the same variable, because
// it is the same fact: a store local to one pod cannot serve a request the ring
// routed to another. It had no test at all, which is how it kept a reason the
// replica check had already outgrown.
func TestValidateIAMShardsOnlyOnASharedStore(t *testing.T) {
	base := func() *Config {
		return &Config{
			Brand: "hanzo", Domain: "api.hanzo.ai", DataDir: "/var/lib/cloud",
			Enable:     []string{"iam"},
			ShardPeers: "cloud-0@cloud-0:8080,cloud-1@cloud-1:8080",
			ShardSelf:  "cloud-0",
		}
	}
	if err := base().Validate(); err == nil {
		t.Fatal("Validate() = nil for iam sharded over the per-pod file store, want error")
	}
	shared := base()
	shared.IAMStore = "sql"
	if err := shared.Validate(); err != nil {
		t.Fatalf("Validate() = %v for iam sharded over a shared store, want nil", err)
	}
}

// IAMStoreShared is the ONE definition of the distinction, so both the boot check
// and apps/iam's opener land on the same set of backends. The failure it prevents
// is asymmetric and silent: an opener that thought "sql" was local would open a
// file nobody asked for, and a boot check that thought it was shared would let N
// replicas run on N of them.
func TestIAMStoreSharedNamesTheServerBackends(t *testing.T) {
	for _, local := range []string{"", "  ", "sqlite"} {
		if IAMStoreShared(local) {
			t.Fatalf("IAMStoreShared(%q) = true, want false — that is the per-pod file", local)
		}
	}
	for _, shared := range []string{"sql", "datastore"} {
		if !IAMStoreShared(shared) {
			t.Fatalf("IAMStoreShared(%q) = false, want true — that is a server every replica reaches", shared)
		}
	}
}
