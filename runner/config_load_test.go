package runner

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCfg writes a config.yaml into a temp dir alongside a fake app key and
// returns the config path. The app key file must exist because validate() stats
// app_private_key_path.
func writeCfg(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	key := filepath.Join(dir, "app.pem")
	if err := os.WriteFile(key, []byte("-----BEGIN KEY-----\nx\n-----END KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(p, []byte(body+"\napp_private_key_path: "+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadConfig_valid(t *testing.T) {
	p := writeCfg(t, `
host_name: evo
labels: [self-hosted, evo, linux, amd64]
runner_dir: /tmp/runner
work_dir: /tmp/work
orgs: [hanzoai, luxfi]
app_id: 1164624
poll_interval: 90s
parallelism: 4
max_concurrent_runners: 12`)

	cfg, err := LoadConfig(p)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.HostName != "evo" {
		t.Errorf("host_name = %q, want evo", cfg.HostName)
	}
	if got, want := len(cfg.Orgs), 2; got != want {
		t.Errorf("orgs = %d, want %d", got, want)
	}
	if cfg.PollInterval != 90*time.Second {
		t.Errorf("poll_interval = %v, want 90s", cfg.PollInterval)
	}
	if cfg.AppID != 1164624 {
		t.Errorf("app_id = %d", cfg.AppID)
	}
}

func TestLoadConfig_rejectsIncomplete(t *testing.T) {
	cases := map[string]string{
		"no host_name": `labels: [x]
runner_dir: /r
work_dir: /w
orgs: [o]
app_id: 1`,
		"no orgs": `host_name: h
labels: [x]
runner_dir: /r
work_dir: /w
app_id: 1`,
		"no auth": `host_name: h
labels: [x]
runner_dir: /r
work_dir: /w
orgs: [o]`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			// write WITHOUT an app key file so app-only configs also fail closed
			dir := t.TempDir()
			p := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadConfig(p); err == nil {
				t.Fatalf("LoadConfig(%s): want error, got nil", name)
			}
		})
	}
}

func TestLoadConfig_missingFile(t *testing.T) {
	if _, err := LoadConfig("/no/such/config.yaml"); err == nil {
		t.Fatal("want error for missing file")
	}
}
