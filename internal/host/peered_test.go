package host

import (
	"testing"
	"time"

	cloud "github.com/hanzoai/cloud/sdk"

	"github.com/hanzoai/cloud/internal/driver/kv/kvtest"
	"github.com/hanzoai/cloud/sdk/coord"
)

// TestPeeredIsWhatTheDeploymentDeclares. A deployment may run more than one writer
// when its Deployment declares more than one replica (CLOUD_REPLICAS) or CLOUD_PEERS
// names more than one pod. Neither a KV nor the live set says so: one pod with a KV
// is one writer, and the live set empties within a heartbeat of a KV outage, so a
// pod that read it as proof of being alone would act beside a peer acting the same.
//
// MUTATION: make Peered read c.Shared() and the second row goes red; make it read
// the live set and the third does.
func TestPeeredIsWhatTheDeploymentDeclares(t *testing.T) {
	url := kvtest.KV(t)
	one := kvtest.Pod(t, url, "cloud-0")
	kvtest.Pod(t, url, "cloud-1")
	deadline := time.Now().Add(5 * time.Second)
	for len(one.Members()) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("the first pod never saw the second join")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for _, c := range []struct {
		name string
		base cloud.Base
		want bool
	}{
		{"a lone pod, no KV", cloud.Base{Coord: coord.Alone()}, false},
		{"one pod with a KV", cloud.Base{Coord: kvtest.Pod(t, kvtest.KV(t), "cloud-0")}, false},
		{"two live pods nothing declared", cloud.Base{Coord: one}, false},
		{"one named peer is this pod itself", cloud.Base{Peers: peered(&cloud.Config{ShardPeers: "a@a:1"})}, false},
		{"two named peers", cloud.Base{Peers: peered(&cloud.Config{ShardPeers: "a@a:1,b@b:1"})}, true},
		{"two declared replicas", cloud.Base{Peers: peered(&cloud.Config{Replicas: 2})}, true},
		{"one declared replica", cloud.Base{Peers: peered(&cloud.Config{Replicas: 1})}, false},
	} {
		if got := c.base.Peered(); got != c.want {
			t.Fatalf("%s: Peered() = %v, want %v", c.name, got, c.want)
		}
	}
}
