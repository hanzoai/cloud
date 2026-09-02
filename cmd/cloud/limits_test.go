package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/clientip"
)

// fourMiB is fasthttp's default request-body ceiling. It is the number this test
// exists to keep the listener away from: the listener terminates public HTTP, so
// if it falls back to the framework default it refuses a body BEFORE the program
// behind it can accept one, and no downstream setting can be reached past it.
const fourMiB = 4 << 20

// TestTheEndpointAcceptsABodyLargerThanTheFrameworkDefault is the assertion that was
// missing. cmd/cloud built its app as zip.New(zip.Config{AppName: "cloud", ...})
// with no BodyLimit while cloud.App() configured the program correctly, so
// GATEWAY_BODY_LIMIT=104857600 sat in the pod's environment and 4,194,305 bytes
// still answered 400. Nothing was red. The config said 100 MiB and the socket
// enforced 4 MiB.
//
// It sends a real body over a real listener rather than reading the config back,
// because reading the config back is what the old code would also have passed:
// the defect was that the value never reached the transport, not that it was
// computed wrong.
func TestTheEndpointAcceptsABodyLargerThanTheFrameworkDefault(t *testing.T) {
	app := zip.New(endpointConfig())
	app.Post("/probe", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"ok": "1"}) })

	body := bytes.Repeat([]byte("a"), fourMiB+1024)
	req := httptest.NewRequest(http.MethodPost, "/probe", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := app.Test(req, deadline)
	if err != nil {
		t.Fatalf("listener refused a %d-byte body at the transport: %v", len(body), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusBadRequest {
		t.Fatalf("the listener answered 400 to a %d-byte body — that is fasthttp refusing "+
			"it before any handler ran, which means endpointConfig() is not carrying BodyLimit. "+
			"This is the exact failure that made 1M-context models unreachable while "+
			"GATEWAY_BODY_LIMIT read 100 MiB in the environment.", len(body))
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("handler never ran cleanly: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

// TestTheEndpointCarriesTheHeaderCeilingToo pins the sibling half. ReadBufferSize was
// omitted from the same struct literal, so the listener also sat on fiber's 4 KiB
// header default — the ceiling a multi-domain SSO session (Domain=.hanzo.ai
// cookies on every subdomain) overruns, answering 431 to a legitimate request.
func TestTheEndpointCarriesTheHeaderCeilingToo(t *testing.T) {
	cfg := endpointConfig()
	if cfg.ReadBufferSize <= 4096 {
		t.Fatalf("listener ReadBufferSize=%d is at or below the 4 KiB framework default — "+
			"a multi-domain SSO session 431s here", cfg.ReadBufferSize)
	}
	if cfg.BodyLimit <= fourMiB {
		t.Fatalf("listener BodyLimit=%d is at or below the %d framework default", cfg.BodyLimit, fourMiB)
	}
}

// TestTheEndpointReportsTheCallerAndNotTheIngress holds the listener to the one fact
// every per-caller rule is keyed on.
//
// zip resolves the caller ONCE at the client and believes a forwarded header only
// where the app names its own hops. Unnamed, its answer is the socket peer — which
// behind the bar is one in-cluster address for every visitor on earth, so the free
// lane's per-visitor ceiling becomes a single global bucket that whoever arrives
// first each day spends for everybody.
//
// It drives a real request rather than reading the config back, for the reason the
// test above states: the defect is that the value never reaches the transport.
// `app.Test` reports the unspecified address as the peer, which the shipped trust
// set names, so a forwarded header is honoured here exactly as it is from the bar.
func TestTheEndpointReportsTheCallerAndNotTheIngress(t *testing.T) {
	const visitor = "203.0.113.7" // TEST-NET-3: never a real peer, so never a false pass

	var seen string
	app := zip.New(endpointConfig())
	app.Get("/probe", func(c *zip.Ctx) error {
		seen = clientip.ClientIP(c)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})

	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	req.Header.Set("X-Forwarded-For", visitor)
	if _, err := app.Test(req, deadline); err != nil {
		t.Fatalf("probe did not reach the handler: %v", err)
	}
	if seen != visitor {
		t.Fatalf("the listener reported %q; the caller is %q. The listener names no "+
			"trusted proxies, so every visitor arrives as the same in-cluster address", seen, visitor)
	}
}
