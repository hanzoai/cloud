package digitalocean

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestMutationWireShape pins the request each mutation actually sends. A wrong verb or
// path here fails silently against the real API — a PUT to the wrong node-pool URL just
// does nothing — so the shape is asserted rather than assumed.
func TestMutationWireShape(t *testing.T) {
	var gotMethod, gotPath, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotMethod, gotPath, gotBody = r.Method, r.URL.Path, string(b)
		fmt.Fprint(w, `{"action":{"id":77,"status":"in-progress"}}`)
	}))
	defer srv.Close()
	c := NewWithBase(srv.URL, "test-token")

	for _, tc := range []struct {
		name         string
		call         func() error
		method, path string
		body         string
	}{
		{"delete droplet", func() error { return c.DeleteDroplet(context.Background(), 101) },
			http.MethodDelete, "/v2/droplets/101", ""},
		{"resize droplet", func() error {
			_, err := c.ResizeDroplet(context.Background(), 101, "s-4vcpu-8gb", false)
			return err
		}, http.MethodPost, "/v2/droplets/101/actions", `{"disk":false,"size":"s-4vcpu-8gb","type":"resize"}`},
		{"permanent resize", func() error {
			_, err := c.ResizeDroplet(context.Background(), 101, "s-4vcpu-8gb", true)
			return err
		}, http.MethodPost, "/v2/droplets/101/actions", `{"disk":true,"size":"s-4vcpu-8gb","type":"resize"}`},
		{"delete load balancer", func() error { return c.DeleteLoadBalancer(context.Background(), "lb-1") },
			http.MethodDelete, "/v2/load_balancers/lb-1", ""},
		{"scale node pool", func() error { return c.ScaleNodePool(context.Background(), "k8s-1", "pool-1", "workers", 3) },
			http.MethodPut, "/v2/kubernetes/clusters/k8s-1/node_pools/pool-1", `{"count":3,"name":"workers"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotMethod, gotPath, gotBody = "", "", ""
			if err := tc.call(); err != nil {
				t.Fatalf("call: %v", err)
			}
			if gotMethod != tc.method || gotPath != tc.path {
				t.Errorf("%s %s, want %s %s", gotMethod, gotPath, tc.method, tc.path)
			}
			if tc.body != "" && !sameJSON(gotBody, tc.body) {
				t.Errorf("body = %s, want %s", gotBody, tc.body)
			}
		})
	}

	// An unconfigured client must never reach the network.
	blank := New("")
	for name, err := range map[string]error{
		"droplet": blank.DeleteDroplet(context.Background(), 1),
		"lb":      blank.DeleteLoadBalancer(context.Background(), "x"),
		"pool":    blank.ScaleNodePool(context.Background(), "c", "p", "n", 1),
	} {
		if err == nil {
			t.Errorf("%s: no error without a token", name)
		}
	}
}

func sameJSON(a, b string) bool {
	var x, y any
	return json.Unmarshal([]byte(a), &x) == nil && json.Unmarshal([]byte(b), &y) == nil &&
		fmt.Sprint(x) == fmt.Sprint(y)
}

func TestDollarsToCents(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"23.44", 2344},
		{"-40000.00", -4_000_000}, // promo credit held (negative account_balance)
		{"12.23", 1223},
		{"0", 0},
		{"", 0},
		{"  5.5 ", 550},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := int64(dollarsToCents(c.in)); got != c.want {
			t.Errorf("dollarsToCents(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
