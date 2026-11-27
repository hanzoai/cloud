package cloud

import (
	"bytes"
	"context"
	"testing"

	_ "github.com/hanzoai/cloud/internal/driver/kv"

	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/hanzoai/cloud/sdk/tenure"
)

// TestMembershipSourceStaticFallback: with no shared KV (dev / native-Go) the source
// IS the static set — the exact wiring runs everywhere, no KV required.
func TestMembershipSourceStaticFallback(t *testing.T) {
	peers := []tenure.Member{{ID: "cloud-0", Addr: "cloud-0:8080"}, {ID: "cloud-1", Addr: "cloud-1:8080"}}
	src := MembershipSource(peers, coord.Alone())
	got, err := src(context.Background())
	if err != nil {
		t.Fatalf("static source: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("static fallback want 2 members, got %d", len(got))
	}
}

// A KV THAT CANNOT BE READ NEVER EMPTIES THE WRITER SET. An empty set has no owner,
// so the fence refuses every org write; with the KV down at boot that was every org
// read-only on every pod.
func TestMembershipSourceHoldsThisPodWithTheKVDown(t *testing.T) {
	c, err := coord.New(coord.Config{
		URL:    "kv://127.0.0.1:1",
		Self:   coord.Member{ID: "cloud-0", Addr: "10.0.0.1:8443"},
		Secret: bytes.Repeat([]byte{1}, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Start(context.Background())
	defer c.Close(context.Background())
	got, err := MembershipSource(nil, c)(context.Background())
	if err != nil || len(got) != 1 || got[0].ID != "cloud-0" {
		t.Fatalf("with the KV unreachable the writer set is %v (err %v); want this pod", got, err)
	}
}
