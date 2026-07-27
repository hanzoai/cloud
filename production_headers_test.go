package cloud

import (
	"net/http"
	"testing"

	"github.com/zap-proto/zip"
	"github.com/zap-proto/zip/middleware"
)

// TestProductionHeaders_WiredWithBrandRegistry proves the serve.go wiring end to
// end against cloud's REAL white-label registry (brand.go): the Server header is
// the brand of the request Host, an unmatched Host falls back to THIS
// deployment's brand (never a framework name, never a hardcoded single brand),
// and X-Api-Version carries the build version. This is the cloud-level
// white-label isolation contract — a lux/zoo caller must never see "hanzo".
func TestProductionHeaders_WiredWithBrandRegistry(t *testing.T) {
	const deployBrand = "zoo" // a zoo deployment (CLOUD_BRAND=zoo)
	app := zip.New(zip.Config{DisableStartupMessage: true, ServerHeader: "zip"})
	app.Use(
		middleware.RequestID(),
		// Byte-for-byte the serve.go wiring.
		middleware.ProductionHeaders(middleware.ProductionHeadersConfig{
			Brand:   func(host string) string { b, _ := BrandForHostOK(host); return b },
			Neutral: deployBrand,
			Version: "v1.786.207",
			HSTS:    true,
		}),
	)
	app.Get("/x", func(c *zip.Ctx) error { return c.JSON(200, map[string]int{"ok": 1}) })

	cases := map[string]string{
		"api.hanzo.ai":      "hanzo",
		"hanzo.cloud":       "hanzo", // AltDomain
		"api.lux.network":   "lux",
		"api.lux.network.":  "lux", // trailing FQDN dot still resolves (not neutral)
		"api.lux.cloud":     "lux", // AltDomain
		"zoo.ngo":           "zoo",
		"console.zoo.cloud": "zoo", // AltDomain
		"api.pars.network":  "pars",
		"weird.example.com": "zoo", // unmatched -> deployment brand
		"localhost":         "zoo",
	}
	for host, want := range cases {
		req, _ := http.NewRequest("GET", "http://"+host+"/x", nil)
		resp, err := app.Fiber().Test(req)
		if err != nil {
			t.Fatalf("Test(%s): %v", host, err)
		}
		if got := resp.Header.Get("Server"); got != want {
			t.Errorf("Host %q: Server=%q want %q", host, got, want)
		}
		if got := resp.Header.Get("X-Api-Version"); got != "v1.786.207" {
			t.Errorf("Host %q: X-Api-Version=%q want v1.786.207", host, got)
		}
		if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("Host %q: missing nosniff", host)
		}
		// No Host ever leaks the framework name or X-Powered-By.
		for _, bad := range []string{"fasthttp", "fiber", "zip"} {
			if resp.Header.Get("Server") == bad {
				t.Errorf("Host %q: Server leaked framework %q", host, bad)
			}
		}
		if got := resp.Header.Get("X-Powered-By"); got != "" {
			t.Errorf("Host %q: leaked X-Powered-By=%q", host, got)
		}
	}
	// A non-hanzo deployment NEVER serves "hanzo" on an unmatched Host.
	for _, host := range []string{"weird.example.com", "localhost"} {
		req, _ := http.NewRequest("GET", "http://"+host+"/x", nil)
		resp, _ := app.Fiber().Test(req)
		if resp.Header.Get("Server") == "hanzo" {
			t.Errorf("zoo deployment leaked hanzo on Host %q", host)
		}
	}
}

// TestConfig_VersionSourcing proves the exact expression LoadConfig uses for
// Config.Version — getenv("CLOUD_VERSION", getenv("HANZO_VERSION", Version)) —
// resolves CLOUD_VERSION first, then the operator-set HANZO_VERSION, then the
// link-time cloud.Version default. It exercises the sourcing directly rather
// than LoadConfig, which registers process-global flags and must not be called
// twice (see config_controlplane_test.go).
//
// The HANZO_VERSION rung is what makes a rollout verifiable: the operator sets
// it on every container from the image tag it rendered, so a pod reports the
// build it actually is instead of the link-time "dev".
func TestConfig_VersionSourcing(t *testing.T) {
	// resolveVersion is the same function the boot path calls, so a regression
	// there fails here rather than passing against a copy of the expression.
	sourced := resolveVersion

	t.Setenv("HANZO_VERSION", "")
	t.Setenv("CLOUD_VERSION", "v9.9.9")
	if got := sourced(); got != "v9.9.9" {
		t.Errorf("Version sourcing=%q want v9.9.9 (from CLOUD_VERSION)", got)
	}

	// The operator's value carries the deployed tag when no explicit override is set.
	t.Setenv("CLOUD_VERSION", "")
	t.Setenv("HANZO_VERSION", "v1.801.233")
	if got := sourced(); got != "v1.801.233" {
		t.Errorf("Version sourcing=%q want v1.801.233 (from HANZO_VERSION)", got)
	}

	// An explicit CLOUD_VERSION still wins over the operator's value.
	t.Setenv("CLOUD_VERSION", "v9.9.9")
	if got := sourced(); got != "v9.9.9" {
		t.Errorf("Version sourcing=%q want v9.9.9 (CLOUD_VERSION overrides HANZO_VERSION)", got)
	}

	// Neither set: the link-time default, so an un-deployed binary is never
	// mistaken for a released one.
	t.Setenv("CLOUD_VERSION", "")
	t.Setenv("HANZO_VERSION", "")
	if got := sourced(); got != Version {
		t.Errorf("Version sourcing=%q want %q (cloud.Version default)", got, Version)
	}
}
