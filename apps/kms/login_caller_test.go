package kms_test

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/geo"
	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// THE LOGIN CEILING IS PER CALLER, NOT PER POD. Keyed on c.Fiber().IP(), which in
// this subsystem is the host's socket, every caller shared one bucket: one stranger
// spending sixty logins a minute locked the kms-operator and everyone else out. Keyed
// on geo.IP, a spent stranger leaves the next stranger and the in-cluster
// caller their logins — and an IPv6 stranger cannot rotate inside its /64, or across
// the /64s of its /48, for a fresh bucket.
func TestLoginBrokerCountsEachCaller(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "iam-token", "expires_in": 3600, "token_type": "Bearer",
			})
		}))
		defer iam.Close()

		cfg := baseCfg(t, masterKeyB64(t))
		cfg.IAMIssuer = iam.URL
		app, _ := newApp(t, cfg)

		const body = `{"clientId":"maxpower-platform-kms","clientSecret":"shh"}`
		login := func(xff string) int {
			t.Helper()
			var extra map[string]string
			if xff != "" {
				extra = map[string]string{"X-Forwarded-For": xff}
			}
			resp := do(t, app, "POST", "/v1/kms/auth/login", "", body, false, extra)
			_ = resp.Body.Close()
			return resp.StatusCode
		}
		// spend drives addresses from next until one is refused, and says how many were
		// served first. A ceiling that never arrives within 200 is no ceiling.
		spend := func(next func(i int) string) int {
			t.Helper()
			for i := range 200 {
				switch code := login(next(i)); code {
				case http.StatusOK:
				case http.StatusTooManyRequests:
					return i
				default:
					t.Fatalf("login %d: status %d, want 200 or 429", i, code)
				}
			}
			t.Fatal("200 logins were served without a refusal: the broker has no ceiling")
			return 0
		}

		served := spend(func(int) string { return "203.0.113.7" })
		if served == 0 {
			t.Fatal("the first login was refused: the limiter blocks legitimate traffic")
		}
		if code := login("198.51.100.4"); code != http.StatusOK {
			t.Fatalf("a second stranger was refused (%d) after the first spent its window: one bucket for everyone", code)
		}
		if code := login(""); code != http.StatusOK {
			t.Fatalf("the in-cluster caller was refused (%d) after a stranger spent its window", code)
		}

		// One IPv6 host rotating through its /64 is one caller.
		if got := spend(func(i int) string { return "2001:db8:1:2::" + strconv.FormatInt(int64(i+1), 16) }); got != served {
			t.Fatalf("an IPv6 host rotating inside its /64 was served %d logins, want the same %d as one address", got, served)
		}
		if code := login("2001:db8:1:3::1"); code != http.StatusOK {
			t.Fatalf("the neighbouring /64 was refused (%d): it is somebody else", code)
		}

		// A site rotating through the /64s of one /48 is sixteen callers, not 65,536: the
		// sixteen /64s spend its window, and the seventeenth is refused on arrival.
		for i := range 16 {
			if got := spend(func(j int) string { return fmt.Sprintf("2001:db8:7:%x::%x", i, j+1) }); got != served {
				t.Fatalf("/64 %d of its /48 was served %d logins, want %d", i+1, got, served)
			}
		}
		if code := login("2001:db8:7:ffff::1"); code != http.StatusTooManyRequests {
			t.Fatalf("a 17th /64 of a /48 that spent sixteen windows was answered %d, want 429", code)
		}
		if code := login("2001:db8:8::1"); code != http.StatusOK {
			t.Fatalf("the neighbouring /48 was refused (%d): it is somebody else", code)
		}
	})
}

// A STRANGER WHO REACHED THE HOST DIRECTLY IS NOT THE KMS-OPERATOR.
//
// kms runs as its own process behind the host, and in-cluster callers — the operator
// among them — share the "" bucket. A caller that reached the host without a proxy of
// ours in between is the host's peer, so the host names it; the child used to re-read
// the request's chain instead, found none, and counted the stranger in "". Sixty such
// logins locked the operator out. Served here as it ships: host, socket, child.
func TestADirectStrangerCannotSpendTheOperatorsLogins(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "iam-token", "expires_in": 3600, "token_type": "Bearer",
			})
		}))
		defer iam.Close()

		cfg := baseCfg(t, masterKeyB64(t))
		cfg.IAMIssuer = iam.URL
		child, _ := newApp(t, cfg)
		dir, err := os.MkdirTemp("", "zk")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(dir) })
		sock := filepath.Join(dir, "k.sock")
		go func() { _ = child.Listen(sock) }()
		t.Cleanup(func() { _ = child.Shutdown() })
		for i := 0; ; i++ {
			if c, err := net.Dial("unix", sock); err == nil {
				_ = c.Close()
				break
			}
			if i > 100 {
				t.Fatalf("kms never listened on %s", sock)
			}
			time.Sleep(20 * time.Millisecond)
		}
		remote, err := zip.Proxy("/v1", sock)
		if err != nil {
			t.Fatal(err)
		}
		stranger := &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 41000}
		host := zip.New(zip.Config{DisableStartupMessage: true})
		host.Use(zip.H(func(c *zip.Ctx) error {
			// Which connection reached the host is the test's to choose: a stranger
			// on a public address, or — with no header — the harness's own peer, which is
			// in-cluster space.
			if c.Header("X-Test-Direct") != "" {
				c.Fiber().RequestCtx().SetRemoteAddr(stranger)
				c.Fiber().Request().Header.Del("X-Test-Direct")
			}
			return c.Continue()
		}))
		host.Use(zip.H(geo.Stamp))
		host.Use(remote)

		login := func(direct bool) int {
			t.Helper()
			req := httptest.NewRequest(http.MethodPost, "/v1/kms/auth/login",
				strings.NewReader(`{"clientId":"maxpower-platform-kms","clientSecret":"shh"}`))
			req.Header.Set("Content-Type", "application/json")
			if direct {
				req.Header.Set("X-Test-Direct", "1")
			}
			resp, err := host.Fiber().Test(req, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			return resp.StatusCode
		}

		refused := false
		for i := range 200 {
			code := login(true)
			if code == http.StatusTooManyRequests {
				refused = true
				break
			}
			if code != http.StatusOK {
				t.Fatalf("stranger login %d: status %d, want 200 or 429", i, code)
			}
		}
		if !refused {
			t.Fatal("200 logins from one stranger were served without a refusal")
		}
		if code := login(false); code != http.StatusOK {
			t.Fatalf("the in-cluster caller was refused (%d) after a stranger who reached the host directly "+
				"spent its own window: the stranger was counted in the operator's bucket", code)
		}
	})
}
