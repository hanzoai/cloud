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
// Config.Version — getenv("CLOUD_VERSION", Version) — resolves from the
// CLOUD_VERSION env, falling back to the link-time cloud.Version default. It
// exercises the sourcing directly rather than LoadConfig, which registers
// process-global flags and must not be called twice (see
// config_controlplane_test.go).
func TestConfig_VersionSourcing(t *testing.T) {
	t.Setenv("CLOUD_VERSION", "v9.9.9")
	if got := getenv("CLOUD_VERSION", Version); got != "v9.9.9" {
		t.Errorf("Version sourcing=%q want v9.9.9 (from CLOUD_VERSION)", got)
	}
	t.Setenv("CLOUD_VERSION", "")
	if got := getenv("CLOUD_VERSION", Version); got != Version {
		t.Errorf("Version sourcing=%q want %q (cloud.Version default)", got, Version)
	}
}
