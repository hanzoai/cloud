package commercesvc

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hanzoai/cloud"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// TestRegisteredAtOrder100 proves the subsystem wired itself into cloud's registry
// via init() — name "commerce", order 100 (after kms=10/iam=50/base=60 and the
// billing/licensing tier). This guards the one-line blank import in subsystems.go:
// before this fold commerce was blank-imported but its init() sat behind
// //go:build cloud and never fired, so cloud never registered it and proxied to a
// remote pod instead.
func TestRegisteredAtOrder100(t *testing.T) {
	var found *cloud.MountSpec
	for i := range cloud.Registry {
		if cloud.Registry[i].Name == "commerce" {
			found = &cloud.Registry[i]
			break
		}
	}
	if found == nil {
		t.Fatal("commerce not registered in cloud.Registry")
	}
	if found.Order != 100 {
		t.Fatalf("commerce registry order = %d, want 100", found.Order)
	}
	if found.Mount == nil {
		t.Fatal("commerce registry Mount is nil")
	}
}

// TestMountHandlerPreservesFullPath verifies the routing plumbing the embedded
// commerce gin handler relies on: zip.App.Mount(prefix, h) must dispatch prefix/*
// to h with the ORIGINAL request path intact. gin routes on the full path
// (/v1/commerce/tenant, not a stripped /tenant), so a prefix-stripping mount would
// 404 every checkout/tenant call. This drives the exact mountHandler call Mount
// uses, with a stub handler, so it runs without the full commerce runtime.
func TestMountHandlerPreservesFullPath(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})

	var gotPath string
	stub := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	})
	mountHandler(app, stub)

	for _, want := range []string{
		"/v1/commerce/tenant",
		"/v1/commerce/catalog",
		"/v1/commerce/deposits",
		"/_/commerce/admin",
	} {
		gotPath = ""
		resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, want, nil))
		if err != nil {
			t.Fatalf("Test(%s): %v", want, err)
		}
		_ = resp.Body.Close()
		if gotPath != want {
			t.Errorf("embedded handler saw path %q, want full %q (prefix stripped?)", gotPath, want)
		}
	}
}

// TestMountFailClosed503 proves the fail-soft path: when commerce.Embed cannot boot,
// mountFailClosed serves an honest JSON 503 on every commerce prefix instead of
// letting /v1/commerce/* fall through to another subsystem's catch-all. cloud and
// every co-resident subsystem stay up — the blast-radius isolation the
// consolidation exists for.
func TestMountFailClosed503(t *testing.T) {
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	mountFailClosed(app)
	for _, p := range []string{
		"/v1/commerce/tenant",
		"/v1/commerce/anything",
		"/_/commerce/admin",
	} {
		resp, err := app.Fiber().Test(httptest.NewRequest(http.MethodGet, p, nil))
		if err != nil {
			t.Fatalf("Test(%s): %v", p, err)
		}
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("%s = %d, want 503 (fail-closed)", p, resp.StatusCode)
		}
		_ = resp.Body.Close()
	}
}

// TestPrefixesCoverPublicSurface guards that the mount set never loses the public
// checkout/tenant surface commerce serves in-process.
func TestPrefixesCoverPublicSurface(t *testing.T) {
	have := map[string]bool{}
	for _, p := range commercePrefixes {
		have[p] = true
	}
	for _, n := range []string{"/v1/commerce", "/_/commerce"} {
		if !have[n] {
			t.Errorf("commercePrefixes missing %q", n)
		}
	}
}
