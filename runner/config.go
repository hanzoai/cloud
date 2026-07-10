// Config for the JIT runner daemon. One YAML per host, loaded once at
// startup, validated, then immutable for the process lifetime.
package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v2"
)

// Config is the host-role daemon configuration, loaded from a single
// YAML file (see LoadConfig). It is the exported entrypoint config the
// cloud CLI passes to RunHost.
type Config struct {
	HostName     string        `yaml:"host_name"`
	Labels       []string      `yaml:"labels"`
	RunnerDir    string        `yaml:"runner_dir"`
	WorkDir      string        `yaml:"work_dir"`
	PollInterval time.Duration `yaml:"poll_interval"`
	Parallelism  int           `yaml:"parallelism"`
	HealthAddr   string        `yaml:"health_addr"`
	AppID        int64         `yaml:"app_id"`
	AppKeyPath   string        `yaml:"app_private_key_path"`
	PATFile      string        `yaml:"pat_file"`
	Orgs         []string      `yaml:"orgs"`

	// AllowForks lets the daemon serve jobs from FORK repositories the org owns.
	// Default false skips them as defense-in-depth. This is NOT the primary
	// fork-PR protection (see daemon.go): a self-hosted runner runs workflow code
	// as this OS user and can read the on-disk App key/PAT, so the operational
	// rule is to point the daemon at PRIVATE/INTERNAL orgs only. Enable only for
	// an org whose forks you fully trust.
	AllowForks    bool   `yaml:"allow_forks"`
	RunnerBinary  string `yaml:"runner_binary"`   // optional, defaults to runner_dir/run.sh
	RunnerVersion string `yaml:"runner_version"`  // optional, sanity log only
	GitHubAPIBase string `yaml:"github_api_base"` // optional, defaults to https://api.github.com

	// MaxConcurrentRunners caps the number of actions-runner subprocesses
	// this host runs at once, across all orgs/repos. Parallelism bounds
	// the org-scan fan-out (how fast we discover work); this bounds the
	// spawn (how much work runs at once) so a burst of queued jobs can't
	// fork-bomb the box. Default 0 means unlimited (legacy behavior).
	MaxConcurrentRunners int `yaml:"max_concurrent_runners"`

	// RepoListTTL bounds how often the daemon re-fetches an org's repo
	// list. The repo set changes rarely (new repo, archive, transfer) so
	// re-listing on every poll cycle is wasted budget — for orgs with
	// hundreds of repos the paginated /orgs/{org}/repos calls dominate
	// the per-cycle API budget and starve the actually-useful
	// /repos/.../actions/runs?status=queued polls. Default is 1 hour.
	// Set to 0 to disable caching (re-fetch every tick).
	RepoListTTL time.Duration `yaml:"repo_list_ttl"`

	// ControlPlaneAddr and Dialer are the OPTIONAL control channel to the
	// in-cluster controller. They are set programmatically by the CLI,
	// never from YAML — a host with neither runs standalone on the local
	// config, which is the documented offline fallback. See transport.go.
	ControlPlaneAddr string `yaml:"-"`
	Dialer           Dialer `yaml:"-"`
}

// LoadConfig reads and validates the host config from path. An empty
// path defaults to ~/.arcd/config.yaml.
func LoadConfig(path string) (Config, error) {
	if path == "" {
		path = filepath.Join(os.Getenv("HOME"), ".arcd", "config.yaml")
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	c := Config{}
	if err := yaml.UnmarshalStrict(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	if err := c.validate(); err != nil {
		return Config{}, fmt.Errorf("validate config %q: %w", path, err)
	}
	c.applyDefaults()
	return c, nil
}

func (c *Config) validate() error {
	if c.HostName == "" {
		return errors.New("host_name is required")
	}
	if len(c.Labels) == 0 {
		return errors.New("labels must be non-empty")
	}
	if c.RunnerDir == "" {
		return errors.New("runner_dir is required")
	}
	if c.WorkDir == "" {
		return errors.New("work_dir is required")
	}
	if len(c.Orgs) == 0 {
		return errors.New("orgs must be non-empty")
	}
	hasApp := c.AppID != 0 && c.AppKeyPath != ""
	hasPAT := c.PATFile != ""
	if !hasApp && !hasPAT {
		return errors.New("either (app_id + app_private_key_path) or pat_file must be set")
	}
	if hasApp {
		if _, err := os.Stat(c.AppKeyPath); err != nil {
			return fmt.Errorf("app_private_key_path: %w", err)
		}
	}
	if hasPAT {
		if _, err := os.Stat(c.PATFile); err != nil {
			return fmt.Errorf("pat_file: %w", err)
		}
	}
	return nil
}

func (c *Config) applyDefaults() {
	if c.PollInterval == 0 {
		c.PollInterval = 15 * time.Second
	}
	if c.Parallelism <= 0 {
		c.Parallelism = 4
	}
	if c.RepoListTTL == 0 {
		c.RepoListTTL = time.Hour
	}
	if c.HealthAddr == "" {
		// Loopback by default: the /v1 control surface (pause/resume/status) is
		// unauthenticated and reachability-is-auth. Binding all interfaces (":7777")
		// would expose it to the LAN; localGuard (http.go) additionally defeats the
		// browser bypasses of a loopback bind. Operators can still opt into a wider
		// bind explicitly via health_addr.
		c.HealthAddr = "127.0.0.1:7777"
	}
	if c.GitHubAPIBase == "" {
		c.GitHubAPIBase = "https://api.github.com"
	}
	if c.RunnerBinary == "" {
		c.RunnerBinary = filepath.Join(c.RunnerDir, "run.sh")
	}
	// Auto-append the `wsl` label when running inside WSL2. Idempotent —
	// `wsl` is a no-op if the operator already declared it in config.yaml.
	// This lets the same config.yaml work on native Linux and inside WSL2
	// without a per-environment fork; only the label set changes.
	c.Labels = AugmentLabelsWithWSL(c.Labels)
}

// HasLabels returns true if the given set of required labels is a subset
// of this host's labels (case-insensitive). GitHub does the same set
// matching when dispatching jobs.
func (c *Config) HasLabels(required []string) bool {
	if len(required) == 0 {
		return false
	}
	set := make(map[string]struct{}, len(c.Labels))
	for _, l := range c.Labels {
		set[strings.ToLower(l)] = struct{}{}
	}
	for _, r := range required {
		if _, ok := set[strings.ToLower(r)]; !ok {
			return false
		}
	}
	return true
}
