package platform

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/k8s"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/meter"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// A CONTAINER DEPLOY STILL PAYS FOR ITSELF. A static site publishes free and is
// billed for its storage (apps/projects); a container is compute, and none of that
// reaches it. A run of an image is refused, with nothing written, for a wallet that
// cannot cover the run fee; a funded run is debited the fee on its own org; and a
// finished build is debited its minutes.
func TestAContainerDeployIsStillCharged(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		var available atomic.Int64
		peer := planetest.Serve(t)
		balance := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"available": available.Load()})
		}))
		t.Cleanup(balance.Close)
		m, err := meter.New(meter.Config{BaseURL: balance.URL, Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		k := fakeK8s()
		app, s := mountSvcK8s(t, k)
		s.Bill = cloud.NewMeter(cloud.Deps{Metering: m}, "platform")

		run := map[string]any{"name": "api", "image": "ghcr.io/hanzoai/nginx:1.27"}
		// The refusal is read from its body: the status this typed op answers a refusal
		// with is its own declared one, which is not what this test is about.
		if _, body := do(t, app, http.MethodPost, "/v1/platform/run", "maxpower", run); !strings.Contains(string(body), `"insufficient_balance"`) {
			t.Fatalf("an unfunded container run was not refused for its balance: %s", body)
		}
		if _, err := k.dyn.Resource(k8s.Apps).Namespace("tenant-maxpower").Get(t.Context(), "api", metav1.GetOptions{}); err == nil {
			t.Fatal("a refused run wrote its Service CR")
		}
		if n := peer.Count(); n != 0 {
			t.Fatalf("a refused run was debited %d times", n)
		}

		available.Store(100_000)
		if code, body := do(t, app, http.MethodPost, "/v1/platform/run", "maxpower", run); code != http.StatusAccepted {
			t.Fatalf("a funded container run want 202, got %d (%s)", code, body)
		}
		if !planetest.Wait(func() bool { return peer.Count() == 1 }) {
			t.Fatalf("a container run must be debited once, got %d", peer.Count())
		}
		d, _ := peer.Last()
		if d.Org != "maxpower" || d.In.Usage.Model != runKind || planetest.Cents(d.In.Amount) != cloud.DefaultResourceFeeCents {
			t.Fatalf("the run was debited %+v on %q, want %d cents of %q on maxpower", d.In, d.Org, cloud.DefaultResourceFeeCents, runKind)
		}

		// A container build that ran two minutes is two minutes of the build rate.
		meterBuild(s, Build{Org: "maxpower", CreatedAt: 1_000}, 1_120)
		if !planetest.Wait(func() bool { return peer.Count() == 2 }) {
			t.Fatalf("a finished container build must be debited, got %d debits", peer.Count())
		}
		d, _ = peer.Last()
		if d.Org != "maxpower" || d.In.Usage.Model != "build" || planetest.Cents(d.In.Amount) != 2*cloud.DefaultResourceFeeCents {
			t.Fatalf("the build was debited %+v on %q, want %d cents of build on maxpower", d.In, d.Org, 2*cloud.DefaultResourceFeeCents)
		}
	})
}
