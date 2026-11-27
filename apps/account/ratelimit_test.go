package account

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/zap-proto/zip"
)

// spend drives requests carrying these headers through capped on rl, and answers the
// status of each, through a real zip request so the peer and header plumbing are
// exercised. The harness peer is the unspecified address, which is one of our hops —
// the ingress's position.
func spend(t *testing.T, rl *rateLimiter, h map[string]string, n int) []int {
	t.Helper()
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Raw(http.MethodPost, "/probe", func(c *zip.Ctx) error {
		if err := capped(rl, c); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	var out []int
	for range n {
		req := httptest.NewRequest(http.MethodPost, "/probe", nil)
		for k, v := range h {
			req.Header.Set(k, v)
		}
		res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, res.StatusCode)
	}
	return out
}

// AN ANONYMOUS CALLER IS ITS ADDRESS, NOT THE POD'S SOCKET. Keyed on c.Fiber().IP(),
// which in this subsystem is the host's socket, every anonymous caller was one key and
// any one of them could spend the money-write ceiling for the rest. A person is still
// their principal, wherever they call from.
func TestRateKeyIsTheCaller(t *testing.T) {
	spent := func(xff string) bool {
		t.Helper()
		return spend(t, newRateLimiter(1), map[string]string{"X-Forwarded-For": xff}, 2)[1] == http.StatusTooManyRequests
	}
	rl := newRateLimiter(1)
	at := func(h map[string]string) int { t.Helper(); return spend(t, rl, h, 1)[0] }
	if !spent("203.0.113.7") {
		t.Fatal("one anonymous caller was served twice against a ceiling of one")
	}
	if at(map[string]string{"X-Forwarded-For": "203.0.113.7"}) != http.StatusNoContent ||
		at(map[string]string{"X-Forwarded-For": "198.51.100.4"}) != http.StatusNoContent {
		t.Fatal("two anonymous callers share a key")
	}
	// One IPv6 host rotating inside its /64 is one key; the next /64 is another.
	if at(map[string]string{"X-Forwarded-For": "2001:db8:1:2::1"}) != http.StatusNoContent ||
		at(map[string]string{"X-Forwarded-For": "2001:db8:1:2:ffff::9"}) != http.StatusTooManyRequests {
		t.Fatal("two addresses in one /64 are two keys")
	}
	if at(map[string]string{"X-Forwarded-For": "2001:db8:1:3::1"}) != http.StatusNoContent {
		t.Fatal("the neighbouring /64 shares the key")
	}
	// A forged left-most entry is not the caller.
	if at(map[string]string{"X-Forwarded-For": "1.2.3.4, 203.0.113.7"}) != http.StatusTooManyRequests {
		t.Fatal("a forged chain prefix moved the key")
	}
	// The person, whichever address.
	if at(map[string]string{"X-User-Id": "u-1", "X-Forwarded-For": "203.0.113.7"}) != http.StatusNoContent {
		t.Fatal("a signed-in person was counted by the address they share")
	}
}

// ANONYMOUS CALLERS NEVER SPEND A PERSON'S ALLOWANCE. Addresses are keys a caller
// mints, and an IPv6 prefix mints them for free; they are counted on their own
// window, so fifty of them leave a signed-in person's minute untouched.
func TestAnonymousCallersNeverSpendAPerson(t *testing.T) {
	rl := newRateLimiter(1)
	for i := range 50 {
		spend(t, rl, map[string]string{"X-Forwarded-For": "2001:db8:" + strconv.Itoa(i+1) + "::1"}, 1)
	}
	if got := spend(t, rl, map[string]string{"X-User-Id": "u-1"}, 2); got[0] != http.StatusNoContent || got[1] != http.StatusTooManyRequests {
		t.Fatalf("a person after 50 anonymous callers: %v, want 204 then 429", got)
	}
}

// And through the decision: one anonymous caller spending the ceiling leaves another
// theirs.
func TestCappedCountsEachAnonymousCaller(t *testing.T) {
	rl := newRateLimiter(1)
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Raw(http.MethodPost, "/probe", func(c *zip.Ctx) error {
		if err := capped(rl, c); err != nil {
			return err
		}
		return c.NoContent(http.StatusNoContent)
	})
	ask := func(xff string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/probe", nil)
		req.Header.Set("X-Forwarded-For", xff)
		res, err := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
		if err != nil {
			t.Fatal(err)
		}
		return res.StatusCode
	}
	if code := ask("203.0.113.7"); code != http.StatusNoContent {
		t.Fatalf("first request: %d, want 204", code)
	}
	if code := ask("203.0.113.7"); code != http.StatusTooManyRequests {
		t.Fatalf("second request from one caller: %d, want 429", code)
	}
	if code := ask("198.51.100.4"); code != http.StatusNoContent {
		t.Fatalf("another caller was refused (%d) after the first spent its ceiling", code)
	}
}
