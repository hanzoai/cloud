// Package cli is the Hanzo cloud-control CLI — the gcloud/doctl-class client
// half of the `hanzo` binary.
//
// `hanzo <subsystem>` SERVES a subsystem (server mode, cmd/hanzo dispatch);
// `hanzo <verb>` CONTROLS the live estate (client mode, this package):
//
//	hanzo login | auth        identity against hanzo.id (IAM)
//	hanzo apps  list|get      the platform apps board (declared/running/drift)
//	hanzo deploy              drive a platform redeploy (rolling, zero-downtime)
//	hanzo clusters …          provision/list/select dedicated DOKS clusters
//	hanzo build               enqueue a platform-native build (runner fabric)
//	hanzo k8s …               current deploy target helpers
//	hanzo config …            ~/.hanzo/config preferences
//
// It is a THIN client over surfaces that already exist — Hanzo IAM
// (hanzo.id /v1/iam/oauth/*), the platform REST control plane
// (platform.hanzo.ai /v1/*), and the cloud /v1 API. It invents no parallel
// API and holds no business logic; every command is one HTTP call shaped by
// resolved configuration. Secrets live only in ~/.hanzo (0600) or the
// environment — never in source, never logged.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Version is the binary version, set by cmd/hanzo from its -ldflags value so
// the CLI and the server report one string. Used in the User-Agent.
var Version = "dev"

// Default endpoints. Overridable per-field via config / env / flag.
const (
	defaultIAMIssuer   = "https://hanzo.id"
	defaultPlatformURL = "https://platform.hanzo.ai"
	defaultCloudURL    = "https://api.hanzo.ai"
	// hanzo-console is the only live IAM client that accepts the password
	// grant today; a dedicated `hanzo-cli` client is a one-line IAM seed
	// follow-up. Override with `--client-id` / HANZO_CLIENT_ID / config.
	defaultClientID = "hanzo-console"
)

// controlCommands maps every client-mode verb to its one-line help. cmd/hanzo
// reads this both to ROUTE (a first token in here means client mode) and to
// list the commands in `hanzo help`, so the verb set is defined exactly once.
var controlCommands = map[string]string{
	"login":    "authenticate against Hanzo IAM (hanzo.id) and store a token",
	"logout":   "remove stored credentials",
	"whoami":   "show the current identity from the stored token",
	"auth":     "manage authentication (login, logout, whoami, token)",
	"apps":     "list/get the platform apps board (declared/running/drift)",
	"deploy":   "drive a platform redeploy (rolling restart, zero-downtime)",
	"clusters": "provision/list/select dedicated DOKS clusters",
	"build":    "enqueue a platform-native build (runner fabric)",
	"k8s":      "deploy-target helpers (current target)",
	"config":   "view/edit ~/.hanzo/config preferences",
	"security": "scan files for hardcoded secrets (local guardrail; no server/auth)",
	"gpu":      "connect this machine's GPU to the Hanzo cloud fleet (connect/status/disconnect)",
	"engine":   "run a local hanzo-engine (OpenAI + Anthropic model server)",
	"code":     "launch a coding agent (claude, codex, dev) on a Hanzo cloud model",
	"runner":   "run this machine as a JIT CI runner for your org (GitHub Actions)",
	"run":      "launch a workload on Hanzo compute (container or function)",
	"agent":    "invoke a managed Hanzo agent to run a task (headless)",
	"bot":      "launch a computer-using agent (booted desktop or terminal)",
}

// IsControlVerb reports whether sub is a client-mode command (and therefore
// must be routed to this package, not the server dispatcher).
func IsControlVerb(sub string) bool {
	_, ok := controlCommands[sub]
	return ok
}

// ControlCommands returns the verb→description map for `hanzo help`.
func ControlCommands() map[string]string { return controlCommands }

// Execute runs the control CLI with args (already stripped of "hanzo"). It is
// the single entrypoint cmd/hanzo calls for client-mode verbs.
func Execute(args []string) error {
	root := newRootCmd()
	root.SetArgs(args)
	return root.Execute()
}

// ---------------------------------------------------------------------------
// Config — non-secret preferences, ~/.hanzo/config (JSON).
// ---------------------------------------------------------------------------

// Config holds non-secret CLI preferences. Every field is optional; empty
// fields fall back to the built-in defaults at resolution time.
type Config struct {
	Org         string `json:"org,omitempty"`
	Output      string `json:"output,omitempty"` // "table" (default) | "json"
	IAMIssuer   string `json:"iam_issuer,omitempty"`
	PlatformURL string `json:"platform_url,omitempty"`
	CloudURL    string `json:"cloud_url,omitempty"`
	ClientID    string `json:"client_id,omitempty"`
	APIKey      string `json:"apiKey,omitempty"` // hk-… key; what `hanzo code` hands the agents
}

// Credentials holds secret material, ~/.hanzo/credentials.json, mode 0600.
// AccessToken/RefreshToken are the IAM user identity (from `hanzo login`);
// PlatformToken/BuildToken are the machine-to-machine tokens the platform
// REST control plane requires (it cannot validate IAM user tokens).
type Credentials struct {
	AccessToken   string `json:"access_token,omitempty"`
	RefreshToken  string `json:"refresh_token,omitempty"`
	TokenType     string `json:"token_type,omitempty"`
	Expiry        int64  `json:"expiry,omitempty"` // unix seconds
	Subject       string `json:"subject,omitempty"`
	Owner         string `json:"owner,omitempty"` // org slug from the token
	PlatformToken string `json:"platform_token,omitempty"`
	BuildToken    string `json:"build_token,omitempty"`
}

// hanzoDir is ~/.hanzo, created 0700 if missing. Overridable with HANZO_HOME
// (used by tests to sandbox the credential store).
func hanzoDir() (string, error) {
	if h := os.Getenv("HANZO_HOME"); h != "" {
		return h, os.MkdirAll(h, 0o700)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".hanzo")
	return dir, os.MkdirAll(dir, 0o700)
}

func configPath() (string, error) {
	dir, err := hanzoDir()
	if err != nil {
		return "", err
	}
	if p := os.Getenv("HANZO_CONFIG"); p != "" {
		return p, nil
	}
	return filepath.Join(dir, "config"), nil
}

func credentialsPath() (string, error) {
	dir, err := hanzoDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "credentials.json"), nil
}

// loadJSON reads a JSON file into v; a missing file is not an error (v is left
// at its zero value) so first-run with no config/credentials just works.
func loadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, v)
}

// writeJSON writes v as indented JSON at path with the given mode, via a
// temp-file rename so a crash mid-write never truncates the store.
func writeJSON(path string, v any, mode os.FileMode) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LoadConfig reads ~/.hanzo/config (or HANZO_CONFIG).
func LoadConfig() (*Config, error) {
	p, err := configPath()
	if err != nil {
		return nil, err
	}
	c := &Config{}
	return c, loadJSON(p, c)
}

// Save persists the config (mode 0644 — non-secret).
func (c *Config) Save() error {
	p, err := configPath()
	if err != nil {
		return err
	}
	return writeJSON(p, c, 0o644)
}

// LoadCredentials reads ~/.hanzo/credentials.json.
func LoadCredentials() (*Credentials, error) {
	p, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	c := &Credentials{}
	return c, loadJSON(p, c)
}

// Save persists credentials with mode 0600 (owner read/write only).
func (c *Credentials) Save() error {
	p, err := credentialsPath()
	if err != nil {
		return err
	}
	return writeJSON(p, c, 0o600)
}

// DeleteCredentials removes the credential store (used by logout).
func DeleteCredentials() error {
	p, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// Env — the effective, resolved settings a command operates with.
// ---------------------------------------------------------------------------

// Env is the fully-resolved runtime context for a command: config + creds
// merged with environment and the global flags. Built once in the root's
// PersistentPreRunE and read by every subcommand.
type Env struct {
	cfg   *Config
	creds *Credentials

	Org         string
	Output      string
	IAMIssuer   string
	PlatformURL string
	CloudURL    string
	ClientID    string

	out io.Writer
}

// flag values bound by the persistent flags (empty == unset, fall through).
type globalFlags struct {
	org, output, platformURL, iamIssuer, cloudURL, clientID, platformToken string
}

// firstNonEmpty returns the first non-empty argument, or "".
func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// resolve merges flags > env > config > built-in defaults into an Env. It is
// pure given its inputs (config/creds are loaded by the caller) so it is
// directly unit-testable.
func resolve(cfg *Config, creds *Credentials, f globalFlags) *Env {
	e := &Env{cfg: cfg, creds: creds, out: os.Stdout}
	e.Output = firstNonEmpty(f.output, os.Getenv("HANZO_OUTPUT"), cfg.Output, "table")
	e.IAMIssuer = strings.TrimRight(firstNonEmpty(f.iamIssuer, os.Getenv("HANZO_IAM_ISSUER"), cfg.IAMIssuer, defaultIAMIssuer), "/")
	e.PlatformURL = strings.TrimRight(firstNonEmpty(f.platformURL, os.Getenv("HANZO_PLATFORM_URL"), cfg.PlatformURL, defaultPlatformURL), "/")
	e.CloudURL = strings.TrimRight(firstNonEmpty(f.cloudURL, os.Getenv("HANZO_CLOUD_URL"), cfg.CloudURL, defaultCloudURL), "/")
	e.ClientID = firstNonEmpty(f.clientID, os.Getenv("HANZO_CLIENT_ID"), cfg.ClientID, defaultClientID)
	// Org for platform calls is the platform organization id (a distinct
	// namespace from the IAM token's `owner` slug), so it comes only from
	// flag/env/config — never silently from the token.
	e.Org = firstNonEmpty(f.org, os.Getenv("HANZO_ORG"), cfg.Org)
	return e
}

// accessToken is the IAM user token (identity / cloud calls).
func (e *Env) accessToken() string {
	return firstNonEmpty(os.Getenv("HANZO_TOKEN"), e.creds.AccessToken)
}

// platformToken resolves the platform control-plane service token. The
// platform REST surface is machine-to-machine (it cannot validate IAM user
// tokens), so apps/clusters/redeploy authenticate with this, sourced from
// (in precedence) the bound --platform-token flag, the environment, then the
// credential store. Never hardcoded.
func (e *Env) platformToken(flagVal string) string {
	return firstNonEmpty(
		flagVal,
		os.Getenv("HANZO_PLATFORM_TOKEN"),
		os.Getenv("PLATFORM_SERVICE_TOKEN"),
		os.Getenv("PAAS_SERVICE_TOKEN"),
		e.creds.PlatformToken,
	)
}

// buildToken resolves the platform build-enqueue token (a distinct credential
// from the service token — see /v1/runner).
func (e *Env) buildToken(flagVal string) string {
	return firstNonEmpty(
		flagVal,
		os.Getenv("HANZO_BUILD_TOKEN"),
		os.Getenv("PLATFORM_BUILD_CALLBACK_TOKEN"),
		e.creds.BuildToken,
	)
}

// requireOrg returns the resolved org or a clear error telling the user how to
// set it.
func (e *Env) requireOrg() (string, error) {
	if e.Org == "" {
		return "", fmt.Errorf("no org set: pass --org, set HANZO_ORG, or run `hanzo config set org <org>`")
	}
	return e.Org, nil
}

// ---------------------------------------------------------------------------
// Output helpers — one place decides JSON vs human-readable tables.
// ---------------------------------------------------------------------------

// emit prints v as JSON when --output=json, otherwise calls table to render a
// human view. This is the single output branch for every command.
func (e *Env) emit(v any, table func(w io.Writer)) error {
	if e.Output == "json" {
		enc := json.NewEncoder(e.out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	table(e.out)
	return nil
}

// ---------------------------------------------------------------------------
// Root command + global flags.
// ---------------------------------------------------------------------------

func newRootCmd() *cobra.Command {
	var f globalFlags
	var env *Env

	root := &cobra.Command{
		Use:           "hanzo",
		Short:         "Hanzo cloud control — manage the live Hanzo estate",
		Long:          "hanzo — gcloud/doctl-class control for the Hanzo platform (IAM, apps, deploys, clusters, builds).",
		SilenceUsage:  true,
		SilenceErrors: false,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			creds, err := LoadCredentials()
			if err != nil {
				return fmt.Errorf("load credentials: %w", err)
			}
			env = resolve(cfg, creds, f)
			env.out = cmd.OutOrStdout()
			return nil
		},
	}

	pf := root.PersistentFlags()
	pf.StringVar(&f.org, "org", "", "organization (overrides config / HANZO_ORG)")
	pf.StringVarP(&f.output, "output", "o", "", "output format: table|json")
	pf.StringVar(&f.platformURL, "platform-url", "", "platform base URL (default "+defaultPlatformURL+")")
	pf.StringVar(&f.iamIssuer, "iam-issuer", "", "IAM issuer (default "+defaultIAMIssuer+")")
	pf.StringVar(&f.cloudURL, "cloud-url", "", "cloud API base URL (default "+defaultCloudURL+")")
	pf.StringVar(&f.clientID, "client-id", "", "IAM OAuth client id (default "+defaultClientID+")")
	pf.StringVar(&f.platformToken, "platform-token", "", "platform control-plane service token (else env/credential store)")

	// envOf returns the resolved Env for a command's RunE (always non-nil after
	// PersistentPreRunE).
	envOf := func() *Env { return env }

	root.AddCommand(
		newVersionCmd(),
		newAuthCmd(envOf, &f),
		newLoginCmd(envOf, &f),
		newLogoutCmd(),
		newWhoamiCmd(envOf),
		newAppsCmd(envOf, &f),
		newDeployCmd(envOf, &f),
		newClustersCmd(envOf, &f),
		newBuildCmd(envOf, &f),
		newK8sCmd(envOf, &f),
		newConfigCmd(),
		newSecurityCmd(envOf),
		newGPUCmd(envOf, &f),
		newEngineCmd(envOf, &f),
		newCodeCmd(envOf, &f),
		newRunnerCmd(envOf, &f),
		newRunCmd(envOf, &f),
		newAgentCmd(envOf, &f),
		newBotCmd(envOf, &f),
	)
	return root
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "version",
		Short:             "Print the hanzo version",
		Args:              cobra.NoArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintf(cmd.OutOrStdout(), "hanzo %s\n", Version)
			return nil
		},
	}
}

// ---------------------------------------------------------------------------
// config command — view/edit the non-secret preference file.
// ---------------------------------------------------------------------------

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "config",
		Short:             "View/edit ~/.hanzo/config preferences",
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
	}

	configKeys := []string{"org", "output", "iam_issuer", "platform_url", "cloud_url", "client_id"}

	get := &cobra.Command{
		Use:   "get <key>",
		Short: "Print one config value",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			v, err := cfg.field(args[0])
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), v)
			return nil
		},
	}

	set := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set one config value (keys: " + strings.Join(configKeys, ", ") + ")",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			if err := cfg.setField(args[0], args[1]); err != nil {
				return err
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "set %s = %s\n", args[0], args[1])
			return nil
		},
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "Print the full config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := LoadConfig()
			if err != nil {
				return err
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(cfg)
		},
	}

	path := &cobra.Command{
		Use:   "path",
		Short: "Print the config file path",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, err := configPath()
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), p)
			return nil
		},
	}

	cmd.AddCommand(get, set, list, path)
	return cmd
}

// field returns the named config value as a string.
func (c *Config) field(key string) (string, error) {
	switch key {
	case "org":
		return c.Org, nil
	case "output":
		return c.Output, nil
	case "iam_issuer":
		return c.IAMIssuer, nil
	case "platform_url":
		return c.PlatformURL, nil
	case "cloud_url":
		return c.CloudURL, nil
	case "client_id":
		return c.ClientID, nil
	default:
		return "", fmt.Errorf("unknown config key %q", key)
	}
}

// setField sets the named config value.
func (c *Config) setField(key, val string) error {
	switch key {
	case "org":
		c.Org = val
	case "output":
		if val != "table" && val != "json" {
			return fmt.Errorf("output must be table|json")
		}
		c.Output = val
	case "iam_issuer":
		c.IAMIssuer = val
	case "platform_url":
		c.PlatformURL = val
	case "cloud_url":
		c.CloudURL = val
	case "client_id":
		c.ClientID = val
	default:
		return fmt.Errorf("unknown config key %q", key)
	}
	return nil
}

// shortTime renders a unix timestamp for human tables; "" for zero.
func shortTime(unix int64) string {
	if unix == 0 {
		return ""
	}
	return time.Unix(unix, 0).Format(time.RFC3339)
}

// sortedKeys returns the keys of m, sorted — for deterministic help output.
func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
