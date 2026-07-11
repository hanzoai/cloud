// Copyright © 2026 Hanzo AI. MIT License.

package commerce

import (
	"io"
	"net/http"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
)

// TestSweepOnceWireContract proves the loopback dispatch is byte-identical to
// what the retired CronJob sent over the wire: POST, the exact money-mint
// route, the service-token bearer, and a JSON body — so the gin middleware
// chain (TokenRequired service-token branch → PlatformOnly) sees the same
// request it always has.
func TestSweepOnceWireContract(t *testing.T) {
	var got *http.Request
	var body []byte
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"charged":2,"orgs":309}`))
	})

	status, resp := sweepOnce(h, "tok-123")

	if got == nil {
		t.Fatal("handler never invoked")
	}
	if got.Method != http.MethodPost {
		t.Errorf("method = %q, want POST", got.Method)
	}
	if got.URL.Path != "/v1/billing/auto-recharge/run-all" {
		t.Errorf("path = %q, want the run-all mint route", got.URL.Path)
	}
	if a := got.Header.Get("Authorization"); a != "Bearer tok-123" {
		t.Errorf("Authorization = %q, want service-token bearer", a)
	}
	if ct := got.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
	if string(body) != "{}" {
		t.Errorf("body = %q, want {}", body)
	}
	if status != http.StatusOK || string(resp) != `{"charged":2,"orgs":309}` {
		t.Errorf("status/resp = %d/%q — response not returned verbatim", status, resp)
	}
}

// TestSweepDisabledWithoutToken: no COMMERCE_SERVICE_TOKEN → the sweeper must
// not start (every tick would 403 at the mint gate) and the returned stop must
// be a safe no-op.
func TestSweepDisabledWithoutToken(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "")
	t.Setenv(sweepIntervalEnv, "1ms")
	called := make(chan struct{}, 1)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called <- struct{}{}
	})
	stop := startAutoRechargeSweep(h, luxlog.NewNoOpLogger())
	select {
	case <-called:
		t.Fatal("sweeper ran without a service token")
	case <-time.After(50 * time.Millisecond):
	}
	stop()
	stop() // idempotent
}

// TestSweepIntervalParsing: explicit off values and garbage disable the
// card-charging loop; valid durations are honored; empty = the CronJob's 15m.
func TestSweepIntervalParsing(t *testing.T) {
	log := luxlog.NewNoOpLogger()
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", defaultSweepInterval},
		{"0", 0},
		{"off", 0},
		{"false", 0},
		{"not-a-duration", 0},
		{"-5m", 0},
		{"30m", 30 * time.Minute},
	}
	for _, tc := range cases {
		t.Setenv(sweepIntervalEnv, tc.in)
		if got := sweepInterval(log); got != tc.want {
			t.Errorf("sweepInterval(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestSweepRunsAndStops: with a token and a tiny interval the sweeper ticks,
// and stop() halts it.
func TestSweepRunsAndStops(t *testing.T) {
	t.Setenv("COMMERCE_SERVICE_TOKEN", "tok")
	t.Setenv(sweepIntervalEnv, "5ms")
	called := make(chan struct{}, 64)
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called <- struct{}{}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"charged":0,"orgs":1}`))
	})
	stop := startAutoRechargeSweep(h, luxlog.NewNoOpLogger())
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("sweeper never ticked")
	}
	stop()
	// Drain anything in flight, then assert quiescence.
	time.Sleep(20 * time.Millisecond)
	for len(called) > 0 {
		<-called
	}
	select {
	case <-called:
		t.Error("sweeper ticked after stop")
	case <-time.After(50 * time.Millisecond):
	}
}
