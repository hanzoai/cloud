package hareplica

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
)

func TestIsReadMethod(t *testing.T) {
	for _, m := range []string{"GET", "HEAD", "OPTIONS"} {
		if !IsReadMethod(m) {
			t.Errorf("%s must be a read method", m)
		}
	}
	for _, m := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if IsReadMethod(m) {
			t.Errorf("%s must be a mutating method (routed to primary)", m)
		}
	}
}

func TestLoopbackOnly(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:52344": true,
		"[::1]:9090":      true,
		"10.244.1.7:5555": false,
		"192.168.1.2:80":  false,
		"garbage":         false,
	}
	for addr, want := range cases {
		if got := LoopbackOnly(addr); got != want {
			t.Errorf("LoopbackOnly(%q)=%v want %v", addr, got, want)
		}
	}
}

func TestConfigFromEnvDefaults(t *testing.T) {
	// Empty getenv → sane in-cluster SeaweedFS defaults, HA off.
	cfg := ConfigFromEnv("/var/lib/cloud", func(_, d string) string { return d })
	if cfg.Enabled {
		t.Fatal("HA must be OFF by default (byte-identical single-Deployment)")
	}
	if cfg.S3Endpoint != "http://s3.hanzo.svc:9000" {
		t.Errorf("default endpoint wrong: %q", cfg.S3Endpoint)
	}
	if !cfg.ForcePathStyle {
		t.Error("force-path-style must default true for SeaweedFS")
	}
	if cfg.LeaseTTL != 30*time.Second || cfg.RenewInterval != 10*time.Second {
		t.Errorf("lease cadence defaults wrong: ttl=%v renew=%v", cfg.LeaseTTL, cfg.RenewInterval)
	}
	if cfg.DataDir != "/var/lib/cloud" {
		t.Errorf("data dir not threaded: %q", cfg.DataDir)
	}
}

func TestConfigFromEnvOverrides(t *testing.T) {
	env := map[string]string{
		"HA_ENABLED":             "true",
		"HA_S3_ENDPOINT":         "http://s3.scratch.svc:9000",
		"HA_S3_BUCKET":           "cloud-ha-scratch",
		"HA_S3_PREFIX":           "scratch",
		"HA_S3_FORCE_PATH_STYLE": "false",
		"HA_PRIMARY_URL":         "http://cloud-ha-primary.scratch.svc:8000/",
		"POD_NAME":               "cloud-ha-0",
		"POD_NAMESPACE":          "scratch",
		"HA_SHIP_INTERVAL":       "500ms",
	}
	get := func(k, d string) string {
		if v, ok := env[k]; ok {
			return v
		}
		return d
	}
	cfg := ConfigFromEnv("/var/lib/cloud", get)
	if !cfg.Enabled {
		t.Fatal("HA must be enabled")
	}
	if cfg.ForcePathStyle {
		t.Error("force-path-style override to false not honored")
	}
	if cfg.PrimaryURL != "http://cloud-ha-primary.scratch.svc:8000" {
		t.Errorf("primary URL trailing slash not trimmed: %q", cfg.PrimaryURL)
	}
	if cfg.ShipInterval != 500*time.Millisecond {
		t.Errorf("ship interval override wrong: %v", cfg.ShipInterval)
	}
	if cfg.PodName != "cloud-ha-0" || cfg.PodNamespace != "scratch" {
		t.Error("pod identity not read")
	}
}

func TestReplicaClientPerDBPath(t *testing.T) {
	m := New(Config{S3Endpoint: "http://s3:9000", S3Bucket: "cloud-ha", S3Prefix: "cloud", ForcePathStyle: true}, luxlog.Noop())
	rc := m.replicaClient("tracker.db")
	if rc.Bucket != "cloud-ha" || rc.Path != "cloud/tracker.db" {
		t.Errorf("per-db S3 path wrong: bucket=%q path=%q", rc.Bucket, rc.Path)
	}
	if !rc.ForcePathStyle {
		t.Error("force-path-style must propagate to the replica client")
	}
}

func TestForwardWriteCopiesPrimaryResponse(t *testing.T) {
	var gotForwardedHdr, gotMethod, gotBody string
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotForwardedHdr = r.Header.Get(ForwardedHeader)
		gotMethod = r.Method
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("X-Test", "primary")
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"created":true}`))
	}))
	defer primary.Close()

	m := New(Config{PrimaryURL: primary.URL}, luxlog.Noop())
	res := m.ForwardWrite(context.Background(), "POST", "/v1/tracker/projects",
		[]byte(`{"key":"ENG"}`), func(add func(k, v string)) {
			add("Authorization", "Bearer tok")
		})

	if res.Status != 201 {
		t.Fatalf("status=%d want 201", res.Status)
	}
	if string(res.Body) != `{"created":true}` {
		t.Errorf("body not copied: %q", res.Body)
	}
	if res.Header.Get("X-Test") != "primary" {
		t.Error("primary response headers not copied")
	}
	if gotForwardedHdr != "1" {
		t.Error("forwarded marker header must be set (loop guard)")
	}
	if gotMethod != "POST" || gotBody != `{"key":"ENG"}` {
		t.Errorf("method/body not forwarded: %s %q", gotMethod, gotBody)
	}
}

func TestForwardWriteNoPrimaryRoute(t *testing.T) {
	m := New(Config{PrimaryURL: ""}, luxlog.Noop())
	res := m.ForwardWrite(context.Background(), "POST", "/v1/x", nil, func(func(k, v string)) {})
	if res.Status != http.StatusServiceUnavailable {
		t.Errorf("missing primary route must 503, got %d", res.Status)
	}
}

func TestForwardWriteStripsHopByHop(t *testing.T) {
	var sawConnection bool
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Connection") != "" {
			sawConnection = true
		}
		w.WriteHeader(200)
	}))
	defer primary.Close()
	m := New(Config{PrimaryURL: primary.URL}, luxlog.Noop())
	_ = m.ForwardWrite(context.Background(), "DELETE", "/v1/x/1", nil, func(add func(k, v string)) {
		add("Connection", "keep-alive")
		add("Authorization", "Bearer t")
	})
	if sawConnection {
		t.Error("hop-by-hop Connection header must NOT be forwarded")
	}
}

func TestNotReadyBeforeStartWhenEnabled(t *testing.T) {
	m := New(Config{Enabled: true}, luxlog.Noop())
	if m.Ready() {
		t.Error("an HA pod must not be Ready before RestoreAll+Start elect it")
	}
	if m.IsPrimary() {
		t.Error("no pod is primary before election")
	}
}

func TestDisabledManagerIsInert(t *testing.T) {
	m := New(Config{Enabled: false}, luxlog.Noop())
	if m.Enabled() {
		t.Fatal("manager must be disabled")
	}
	// Start on a disabled manager marks ready unconditionally (byte-identical).
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("disabled Start must not error: %v", err)
	}
	if !m.Ready() {
		t.Error("disabled manager must report ready (unconditional readiness)")
	}
	if err := m.RestoreAll(context.Background()); err != nil {
		t.Fatalf("disabled RestoreAll must be a no-op: %v", err)
	}
	if err := m.Drain(context.Background()); err != nil {
		t.Fatalf("disabled Drain must be a no-op: %v", err)
	}
}

func TestGlobalDBsCoverProofTarget(t *testing.T) {
	// The tracker DB (the stage-4 probe target) must be in the replicated set.
	found := false
	for _, d := range globalDBs {
		if d == "tracker.db" {
			found = true
		}
		if !strings.HasSuffix(d, ".db") {
			t.Errorf("global DB %q must be a .db file", d)
		}
	}
	if !found {
		t.Error("tracker.db must be in the replicated global set")
	}
}
