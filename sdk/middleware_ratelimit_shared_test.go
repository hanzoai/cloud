package cloud

// ONE CEILING FOR THE DEPLOYMENT.
//
// ScopeRateLimit kept its minutes in a map in each process, so an org held to N a
// minute was held to N by every process that served it — N times the replicas,
// times the apps. These drive two limiters as two processes do: separate limiter
// instances, separate Coords with separate local counts, and nothing in common but
// the deployment's KV. And they take the KV away, which must loosen the ceiling to
// one per process and never refuse a request for it.

import (
	"bytes"
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/gateway/edge"
	"github.com/hanzoai/cloud/sdk/coord"
	"github.com/zap-proto/zip"
)

// process is one process's ScopeRateLimit and the Coord it counts through.
type process struct {
	app *zip.App
	co  *coord.Coord
}

// send is one request through p, counted through p's Coord.
func (p process) send(t *testing.T, org, key, rate string) *http.Response {
	t.Helper()
	was := coord.Default()
	coord.SetDefault(p.co)
	defer coord.SetDefault(was)
	r := httptest.NewRequest(http.MethodGet, "/v1/train/jobs", nil)
	r.Header.Set("X-Org-Id", org)
	r.Header.Set("X-User-Id", org+"/ann")
	if key != "" {
		r.Header.Set("X-Test-Key", key)
		r.Header.Set("X-Test-Rate", rate)
	}
	resp, err := p.app.Test(r, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	_ = resp.Body.Close()
	return resp
}

// orgCeiling is a policy store holding every org to rpm a minute.
func orgCeiling(t *testing.T, rpm int) *edge.Store {
	t.Helper()
	return staticPol(t, edge.Policy{OrgRPM: rpm})
}

// deadKV answers a Coord per pod whose KV_URL names a port nothing listens on.
func deadKV(t *testing.T) func(id string) *coord.Coord {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	url := "kv://" + l.Addr().String()
	_ = l.Close()
	return func(id string) *coord.Coord {
		co, err := coord.New(coord.Config{
			URL:        url,
			Deployment: "test",
			Self:       coord.Member{ID: id, Addr: id + ":8443"},
			Instance:   id,
			Secret:     bytes.Repeat([]byte{9}, 32),
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { co.Close(context.Background()) })
		return co
	}
}
