package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJITHasLabels(t *testing.T) {
	c := &Config{
		Labels: []string{"self-hosted", "dbc", "macos", "arm64", "m4max", "metal", "vulkan-moltenvk"},
	}
	tests := []struct {
		name     string
		required []string
		want     bool
	}{
		{"exact subset", []string{"self-hosted", "dbc", "metal"}, true},
		{"case insensitive", []string{"Self-Hosted", "DBC", "METAL"}, true},
		{"empty required", []string{}, false},
		{"missing one", []string{"self-hosted", "dbc", "cuda"}, false},
		{"full set", c.Labels, true},
		{"unknown label", []string{"hanzoai-cuda"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := c.HasLabels(tt.required); got != tt.want {
				t.Errorf("HasLabels(%v) = %v, want %v", tt.required, got, tt.want)
			}
		})
	}
}

func TestLoadJITConfig_AppAuth(t *testing.T) {
	dir := t.TempDir()
	pemPath := filepath.Join(dir, "app.pem")
	if err := os.WriteFile(pemPath, []byte("-----BEGIN RSA PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := `host_name: testhost
labels: [self-hosted, testhost]
runner_dir: /tmp/agent
work_dir: /tmp/work
poll_interval: 30s
max_concurrent_runners: 3
app_id: 12345
app_private_key_path: ` + pemPath + `
orgs:
  - org1
  - org2
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.HostName != "testhost" {
		t.Errorf("HostName = %q", c.HostName)
	}
	if c.PollInterval != 30*time.Second {
		t.Errorf("PollInterval = %v", c.PollInterval)
	}
	if c.Parallelism != 4 {
		t.Errorf("Parallelism default = %d", c.Parallelism)
	}
	if c.HealthAddr != "127.0.0.1:7777" {
		t.Errorf("HealthAddr default = %q", c.HealthAddr)
	}
	if c.MaxConcurrentRunners != 3 {
		t.Errorf("MaxConcurrentRunners = %d, want 3", c.MaxConcurrentRunners)
	}
	if len(c.Orgs) != 2 {
		t.Errorf("Orgs = %v", c.Orgs)
	}
}

func TestLoadJITConfig_PATAuth(t *testing.T) {
	dir := t.TempDir()
	patPath := filepath.Join(dir, "token")
	if err := os.WriteFile(patPath, []byte("gho_xxxxxxxxxxxxx\ngho_yyyyyyyyyyy\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := `host_name: pat-host
labels: [self-hosted, pat-host]
runner_dir: /tmp/agent
work_dir: /tmp/work
pat_file: ` + patPath + `
orgs:
  - org1
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.PATFile != patPath {
		t.Errorf("PATFile = %q", c.PATFile)
	}
}

func TestLoadJITConfig_MissingAuth(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	yaml := `host_name: no-auth
labels: [self-hosted]
runner_dir: /tmp/agent
work_dir: /tmp/work
orgs: [a]
`
	_ = os.WriteFile(cfgPath, []byte(yaml), 0o644)
	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for missing auth")
	}
	if !strings.Contains(err.Error(), "pat_file") {
		t.Errorf("expected auth error, got %v", err)
	}
}

// TestNewJITDaemon_MaxConcurrentRunnersSemaphore proves the
// max_concurrent_runners knob is wired: a positive value installs a
// bounding semaphore, zero (the default) leaves spawn uncapped.
func TestNewJITDaemon_MaxConcurrentRunnersSemaphore(t *testing.T) {
	dir := t.TempDir()
	patPath := filepath.Join(dir, "token")
	if err := os.WriteFile(patPath, []byte("ghp_test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	capped := &Config{PATFile: patPath, MaxConcurrentRunners: 2, GitHubAPIBase: "https://api.github.com"}
	d, err := NewJITDaemon(capped, nil)
	if err != nil {
		t.Fatalf("NewJITDaemon(capped): %v", err)
	}
	if d.runSem == nil {
		t.Fatal("MaxConcurrentRunners=2 should install a bounding semaphore")
	}
	// The semaphore must admit exactly the configured weight.
	if !d.runSem.TryAcquire(2) {
		t.Fatal("semaphore should admit 2 concurrent runners")
	}
	if d.runSem.TryAcquire(1) {
		t.Fatal("semaphore must reject a 3rd runner past the cap of 2")
	}
	d.runSem.Release(2)

	uncapped := &Config{PATFile: patPath, GitHubAPIBase: "https://api.github.com"}
	du, err := NewJITDaemon(uncapped, nil)
	if err != nil {
		t.Fatalf("NewJITDaemon(uncapped): %v", err)
	}
	if du.runSem != nil {
		t.Fatal("MaxConcurrentRunners=0 must leave spawn uncapped (nil semaphore)")
	}
}
