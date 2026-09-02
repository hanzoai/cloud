// Package cli is the Hanzo cloud-control CLI — the client half of the `hanzo`
// binary: one command tree over the Hanzo Cloud.
//
// Every verb registered in newRootCmd MANAGES the Hanzo Cloud from here; every
// other verb cmd/hanzo hands to the Rust fabric CLI (IsControlVerb draws that
// line off this tree). What runs here:
//
//	hanzo login | auth        identity against hanzo.id (IAM)
//	hanzo apps  list|get      the platform apps board (declared/running/drift)
//	hanzo deploy              drive a platform redeploy (rolling, zero-downtime)
//	hanzo clusters …          provision/list/select dedicated DOKS clusters
//	hanzo build               enqueue a platform-native build (runner fabric)
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
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Version is the binary version. cmd/hanzo RESOLVES it once (resolveVersion:
// the -ldflags stamp, else the build metadata the toolchain embeds) and assigns
// it here — this package holds the answer and never re-derives it. Reported by
// `hanzo version` and sent as the User-Agent.
var Version = "dev"

// Default endpoints. Overridable per-field via config / env / flag.
const (
	defaultIAMIssuer   = "https://hanzo.id"
	defaultPlatformURL = "https://platform.hanzo.ai"
	defaultCloudURL    = "https://api.hanzo.ai"
	// hanzo-cli is this binary's own IAM client (<org>-<app>, HIP-0111), and the
	// ONE client id every flow here runs as — device, code exchange, refresh.
	// It is PUBLIC: a CLI ships to users' machines, so it holds no secret and
	// proves itself with PKCE (code flow) or a human's approval (device flow).
	//
	// It must stay one id across flows. Borrowing a different client per flow is
	// what broke sign-in: the device grant ran as a client registered with a
	// secret this binary could never present, so IAM answered `invalid_client`,
	// and a refresh token minted under one id was later presented under another.
	// Override with `--client-id` / HANZO_CLIENT_ID / config.
	defaultClientID = "hanzo-cli"
	// defaultOrg is the tenant a credentialed sign-in resolves the account in when
	// --org / HANZO_ORG / config say nothing: the brand the default issuer serves.
	// A white-label deployment names its issuer and its org together.
	defaultOrg = "hanzo"
)

// IsControlVerb reports whether sub names a command this binary serves, and so
// must run here rather than being handed to the fabric CLI. It asks the command
// tree itself — cobra's own name and alias resolution over newRootCmd — so the
// router and the command set are one fact and cannot drift apart.
//
// A hand-kept verb list was the previous answer, and it drifted both ways: it
// still claimed `code` and `k8s` after those commands were deleted, so cobra
// answered `unknown command` for verbs the fabric CLI implements, and it never
// listed `completion`, so a command registered right here was handed away.
func IsControlVerb(sub string) bool {
	// The shell-completion request commands are cobra's own, registered during
	// Execute: the scripts `hanzo completion <shell>` emits invoke them, so this
	// binary serves them exactly like every command in the tree.
	if sub == cobra.ShellCompRequestCmd || sub == cobra.ShellCompNoDescRequestCmd {
		return true
	}
	root := newRootCmd()
	cmd, _, _ := root.Find([]string{sub})
	return cmd != root
}

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
	APIKey      string `json:"apiKey,omitempty"` // sk-… key, shared with the rest of the toolchain
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
// Identity store — ~/.hanzo/identities.json. Holds EVERY logged-in identity
// keyed by its stable "<owner>/<name>" key, with an Active pointer. On every
// write the active identity is mirrored into credentials.json (above), so every
// legacy single-file reader keeps seeing the current identity unchanged. This
// is the ONE credential store; credentials.json is its active-view mirror.
// ---------------------------------------------------------------------------

// IdentityStore is the on-disk shape of ~/.hanzo/identities.json.
type IdentityStore struct {
	Active     string                  `json:"active,omitempty"`
	Identities map[string]*Credentials `json:"identities,omitempty"`
}

// key is the stable per-identity store key "<owner>/<name>", where name is the
// email local-part (else the raw subject). The same identity yields the same
// key every login, so re-login updates in place; the same email under a
// different org (privilege separation) yields a distinct key (admin/z vs
// hanzo/z) and is stored side by side rather than clobbering.
func (c *Credentials) key() string {
	name := c.Subject
	if i := strings.IndexByte(name, '@'); i > 0 {
		name = name[:i]
	}
	return cmp.Or(c.Owner, "-") + "/" + cmp.Or(name, "-")
}

func identitiesPath() (string, error) {
	dir, err := hanzoDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "identities.json"), nil
}

// LoadIdentities reads the store. A pre-existing single-file credentials.json
// with no store yet is migrated in (read-only) as the sole, active identity, so
// upgrades are seamless — the first write persists it into the store.
func LoadIdentities() (*IdentityStore, error) {
	p, err := identitiesPath()
	if err != nil {
		return nil, err
	}
	s := &IdentityStore{Identities: map[string]*Credentials{}}
	if err := loadJSON(p, s); err != nil {
		return nil, err
	}
	if s.Identities == nil {
		s.Identities = map[string]*Credentials{}
	}
	if len(s.Identities) == 0 {
		if c, err := LoadCredentials(); err == nil && c.AccessToken != "" {
			k := c.key()
			s.Identities[k] = c
			s.Active = k
		}
	}
	return s, nil
}

// keys returns the identity keys, sorted, for deterministic output.
func (s *IdentityStore) keys() []string { return slices.Sorted(maps.Keys(s.Identities)) }

// Put stores c under its key and makes it active, returning the key.
func (s *IdentityStore) Put(c *Credentials) string {
	if s.Identities == nil {
		s.Identities = map[string]*Credentials{}
	}
	k := c.key()
	s.Identities[k] = c
	s.Active = k
	return k
}

// Remove deletes an identity; Save re-points Active if it was the one removed.
func (s *IdentityStore) Remove(key string) { delete(s.Identities, key) }

// resolve turns a user selector into a stored key: an exact key wins; otherwise
// a bare owner matches iff exactly one identity carries it.
func (s *IdentityStore) resolve(sel string) (string, error) {
	if _, ok := s.Identities[sel]; ok {
		return sel, nil
	}
	var match []string
	for _, k := range s.keys() {
		if s.Identities[k].Owner == sel {
			match = append(match, k)
		}
	}
	switch len(match) {
	case 1:
		return match[0], nil
	case 0:
		return "", fmt.Errorf("no stored identity for %q (see `hanzo auth list`)", sel)
	default:
		return "", fmt.Errorf("%q is ambiguous across %s — pass the full owner/name key", sel, strings.Join(match, ", "))
	}
}

// Save persists the store (0600) and mirrors the active identity into
// credentials.json for legacy single-file readers. When the store is empty it
// removes both files. Active is normalized to a real key first.
func (s *IdentityStore) Save() error {
	if _, ok := s.Identities[s.Active]; !ok {
		s.Active = ""
		if ks := s.keys(); len(ks) > 0 {
			s.Active = ks[0]
		}
	}
	if len(s.Identities) == 0 {
		return clearCredentialStore()
	}
	p, err := identitiesPath()
	if err != nil {
		return err
	}
	if err := writeJSON(p, s, 0o600); err != nil {
		return err
	}
	return s.Identities[s.Active].Save() // mirror active → credentials.json (0600)
}

// SaveActive writes c back as the active identity (store + mirror), keeping the
// two consistent after an in-place token refresh. With no store yet it falls
// back to the single-file write.
func SaveActive(c *Credentials) error {
	s, err := LoadIdentities()
	if err != nil {
		return err
	}
	if len(s.Identities) == 0 {
		return c.Save()
	}
	k := s.Active
	if k == "" || s.Identities[k] == nil {
		k = c.key()
	}
	s.Identities[k] = c
	s.Active = k
	return s.Save()
}

// clearCredentialStore removes the identity store and its credentials.json
// mirror (used by logout when the last identity is removed).
func clearCredentialStore() error {
	for _, pathOf := range []func() (string, error){credentialsPath, identitiesPath} {
		p, err := pathOf()
		if err != nil {
			return err
		}
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return err
		}
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

// resolve merges flags > env > config > built-in defaults into an Env. It is
// pure given its inputs (config/creds are loaded by the caller) so it is
// directly unit-testable.
func resolve(cfg *Config, creds *Credentials, f globalFlags) *Env {
	e := &Env{cfg: cfg, creds: creds, out: os.Stdout}
	e.Output = cmp.Or(f.output, os.Getenv("HANZO_OUTPUT"), cfg.Output, "table")
	e.IAMIssuer = strings.TrimRight(cmp.Or(f.iamIssuer, os.Getenv("HANZO_IAM_ISSUER"), cfg.IAMIssuer, defaultIAMIssuer), "/")
	e.PlatformURL = strings.TrimRight(cmp.Or(f.platformURL, os.Getenv("HANZO_PLATFORM_URL"), cfg.PlatformURL, defaultPlatformURL), "/")
	e.CloudURL = strings.TrimRight(cmp.Or(f.cloudURL, os.Getenv("HANZO_CLOUD_URL"), cfg.CloudURL, defaultCloudURL), "/")
	e.ClientID = cmp.Or(f.clientID, os.Getenv("HANZO_CLIENT_ID"), cfg.ClientID, defaultClientID)
	// Org for platform calls is the platform organization id (a distinct
	// namespace from the IAM token's `owner` slug), so it comes only from
	// flag/env/config — never silently from the token.
	e.Org = cmp.Or(f.org, os.Getenv("HANZO_ORG"), cfg.Org)
	return e
}

// accessToken is the IAM user token (identity / cloud calls).
func (e *Env) accessToken() string {
	return cmp.Or(os.Getenv("HANZO_TOKEN"), e.creds.AccessToken)
}

// platformToken resolves the bearer the platform control plane authenticates
// apps/clusters/redeploy with. ONE identity authorizes everything: after a plain
// `hanzo login` the IAM access token is the FINAL fallback, so no separate
// --platform-token is needed — the platform verifies the IAM JWT (signature,
// issuer, expiry) and org-scopes the caller. A dedicated service token still
// wins when present (flag > env > credential store > IAM login), so purpose-minted
// machine tokens keep their precedence and internal automation is unchanged.
// Never hardcoded.
func (e *Env) platformToken(flagVal string) string {
	return cmp.Or(
		flagVal,
		os.Getenv("HANZO_PLATFORM_TOKEN"),
		os.Getenv("PLATFORM_SERVICE_TOKEN"),
		os.Getenv("PAAS_SERVICE_TOKEN"),
		e.creds.PlatformToken,
		e.accessToken(), // IAM login is the one identity that authorizes control-plane ops
	)
}

// buildToken resolves the bearer `hanzo build` sends to the platform build
// enqueue (/v1/platform/runner). The organization a build is attributed to is
// read off this credential, so every source here is one that carries an
// identity: a purpose-minted build token when the caller names one, and
// otherwise the IAM login — which is why `hanzo build` needs no separate
// --build-token. A deployment's own service secrets are left to the deployment;
// they name no organization, so they cannot say who a build belongs to.
func (e *Env) buildToken(flagVal string) string {
	return cmp.Or(
		flagVal,
		os.Getenv("HANZO_BUILD_TOKEN"),
		e.creds.BuildToken,
		e.accessToken(), // IAM login is the one identity that authorizes builds
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
		Short:         "Manage the Hanzo Cloud",
		Long:          "hanzo — one command tree over the Hanzo Cloud: identities, apps, deploys, clusters and builds.",
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
	pf.StringVar(&f.platformURL, "platform-url", "", "platform base URL (default "+defaultPlatformURL+"); `hanzo build` targets --cloud-url instead")
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
		newConfigCmd(),
		newSecurityCmd(envOf),
		newLinkCmd(envOf, &f),
		newUnlinkCmd(envOf, &f),
		newEngineCmd(envOf, &f),
		newRunnerCmd(envOf, &f),
		newRunCmd(envOf, &f),
		newAgentCmd(envOf, &f),
		newBotCmd(envOf, &f),
	)

	// help and completion are part of this tree. Execute adds them anyway, at
	// which point it is too late for the router to see them; adding them here
	// means IsControlVerb answers over exactly the set a user can run. Both are
	// idempotent, so Execute's own call is a no-op.
	root.InitDefaultHelpCmd()
	root.InitDefaultCompletionCmd()
	return root
}

// delegateVersion asks the fabric CLI what it is. Empty means it would not say,
// which is reported as such rather than guessed at — a version nobody can trust
// is worse than none.
func delegateVersion(bin string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(string(out), "\n")
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	v := strings.TrimPrefix(fields[len(fields)-1], "v")
	if v == "" || v[0] < '0' || v[0] > '9' {
		return ""
	}
	return v
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "version",
		Short:             "Print the hanzo version",
		Args:              cobra.NoArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			// STDOUT is the answer to the question asked — what am I running —
			// and it is EXACTLY one line, always. It is also what a parent
			// `hanzo` reads back out of a delegate (delegateVersion takes the
			// first line's last token), so one line keeps that contract honest
			// in both directions.
			fmt.Fprintf(cmd.OutOrStdout(), "hanzo %s\n", Version)

			// The delegate is a SEPARATE artifact with its own version, and a
			// stale one silently answers every verb this binary hands over. That
			// is worth saying — as a WARNING on stderr, where it cannot compete
			// with the answer, and ONLY when there is something to act on.
			// Agreement is the healthy case and says nothing at all.
			p := fabricCLI()
			if p == "" {
				return nil
			}
			v, ours := delegateVersion(p), strings.TrimPrefix(Version, "v")
			errOut := cmd.ErrOrStderr()
			switch {
			case v == "":
				fmt.Fprintf(errOut, "warning: delegate %s is installed but would not report a version\n", p)
			case v != ours:
				self, err := os.Executable()
				if err != nil {
					self = "hanzo"
				}
				fmt.Fprintf(errOut, "warning: stale delegate — %s is %s, %s is %s;\n"+
					"         verbs handed to `hanzo-node` run THAT build, not this one\n", p, v, self, ours)
			}
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
