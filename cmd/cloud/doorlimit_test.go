package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// fourMiB is fasthttp's default request-body ceiling. It is the number this test
// exists to keep the door away from: the door terminates public HTTP, so if it
// falls back to the framework default it refuses a body BEFORE the program
// behind it can accept one, and no downstream setting can be reached past it.
const fourMiB = 4 << 20

// TestTheDoorAcceptsABodyLargerThanTheFrameworkDefault is the assertion that was
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
func TestTheDoorAcceptsABodyLargerThanTheFrameworkDefault(t *testing.T) {
	app := zip.New(doorConfig())
	app.Post("/probe", func(c *zip.Ctx) error { return c.JSON(http.StatusOK, map[string]string{"ok": "1"}) })

	body := bytes.Repeat([]byte("a"), fourMiB+1024)
	req := httptest.NewRequest(http.MethodPost, "/probe", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/octet-stream")

	resp, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("door refused a %d-byte body at the transport: %v", len(body), err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusBadRequest {
		t.Fatalf("the door answered 400 to a %d-byte body — that is fasthttp refusing it "+
			"before any handler ran, which means doorConfig() is not carrying BodyLimit. "+
			"This is the exact failure that made 1M-context models unreachable while "+
			"GATEWAY_BODY_LIMIT read 100 MiB in the environment.", len(body))
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("handler never ran cleanly: got %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

// TestTheDoorCarriesTheHeaderCeilingToo pins the sibling half. ReadBufferSize was
// omitted from the same struct literal, so the door also sat on fiber's 4 KiB
// header default — the ceiling a multi-domain SSO session (Domain=.hanzo.ai
// cookies on every subdomain) overruns, answering 431 to a legitimate request.
func TestTheDoorCarriesTheHeaderCeilingToo(t *testing.T) {
	cfg := doorConfig()
	if cfg.ReadBufferSize <= 4096 {
		t.Fatalf("door ReadBufferSize=%d is at or below the 4 KiB framework default — "+
			"a multi-domain SSO session 431s here", cfg.ReadBufferSize)
	}
	if cfg.BodyLimit <= fourMiB {
		t.Fatalf("door BodyLimit=%d is at or below the %d framework default", cfg.BodyLimit, fourMiB)
	}
}
