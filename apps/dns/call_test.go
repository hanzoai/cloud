package dns

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestEveryAddressIsTheSameCall is the fact one send rests on: whatever the route
// table names — a zone list, a record write, a zone create, the plane's own sync
// and health — reaches the plane as the caller's own method and path, and the head
// tells none of them apart.
//
// It is worth asserting because the shape it replaced looked like it did tell them
// apart: five interface methods over an operation read off the address, all five
// forwarding the original path unchanged. Anything that starts distinguishing these
// addresses again has to make this test say so.
func TestEveryAddressIsTheSameCall(t *testing.T) {
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/v1/dns/zones"},
		{http.MethodPost, "/v1/dns/zones"},
		{http.MethodGet, "/v1/dns/zones/example.com"},
		{http.MethodDelete, "/v1/dns/zones/example.com"},
		{http.MethodGet, "/v1/dns/zones/example.com/records"},
		{http.MethodPost, "/v1/dns/zones/example.com/records"},
		{http.MethodPut, "/v1/dns/zones/example.com/records/r1"},
		{http.MethodPatch, "/v1/dns/zones/example.com/records/r1"},
		{http.MethodDelete, "/v1/dns/zones/example.com/records/r1"},
		{http.MethodGet, "/v1/dns/health"},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			up := newStubDNS()
			defer up.Close()
			app := dnsApp(t, up.URL)

			res, _ := do(t, app, as(httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{}`)),
				"orgA", "userA", "tokenA"))
			if res.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want the plane's own 200", res.StatusCode)
			}
			up.mu.Lock()
			defer up.mu.Unlock()
			if up.hits != 1 {
				t.Fatalf("plane saw %d calls, want 1", up.hits)
			}
			if up.method != tc.method || up.path != tc.path {
				t.Fatalf("plane saw %s %s, want %s %s", up.method, up.path, tc.method, tc.path)
			}
			if up.orgHdr != "orgA" || up.auth != "Bearer tokenA" {
				t.Fatalf("plane saw org=%q auth=%q, want the validated tenant and the caller's own bearer",
					up.orgHdr, up.auth)
			}
		})
	}
}

// TestWhereThePlaneIsIsConfiguration pins the one thing about the plane a
// deployment chooses. A base that is not set at all leaves the in-cluster default,
// which is why a standard deployment forwards with no configuration of its own.
func TestWhereThePlaneIsIsConfiguration(t *testing.T) {
	up := newStubDNS()
	defer up.Close()
	t.Setenv("HANZO_DNS_URL", up.URL+"/")
	if got := newHanzoPlane().base; got != up.URL {
		t.Fatalf("base = %q, want %q with the trailing slash trimmed", got, up.URL)
	}
	t.Setenv("HANZO_DNS_URL", "")
	if got := newHanzoPlane().base; got != endpoint {
		t.Fatalf("base = %q, want the in-cluster default %q", got, endpoint)
	}
}
