package cli

import (
	"bufio"
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// iamClient is a thin client over the Hanzo IAM OAuth2 surface at
// {issuer}/v1/iam/oauth/*. It speaks only the standard token + userinfo
// endpoints; it holds no IAM business logic.
type iamClient struct {
	issuer   string
	clientID string
	http     *http.Client
}

func newIAMClient(issuer, clientID string) *iamClient {
	return &iamClient{
		issuer:   strings.TrimRight(issuer, "/"),
		clientID: clientID,
		http:     &http.Client{Timeout: 30 * time.Second},
	}
}

// tokenResp is the OAuth2 token endpoint response (success or RFC-6749 error).
type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// postForm performs an x-www-form-urlencoded POST to an oauth endpoint and
// decodes the token response, surfacing OAuth errors as Go errors.
func (c *iamClient) postForm(ctx context.Context, endpoint string, form url.Values) (*tokenResp, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.issuer+endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hanzo-cli/"+Version)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var tr tokenResp
	if err := json.Unmarshal(body, &tr); err != nil {
		return nil, fmt.Errorf("iam %s: HTTP %d: %s", endpoint, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if tr.Error != "" {
		return nil, fmt.Errorf("iam %s: %s: %s", endpoint, tr.Error, tr.ErrorDesc)
	}
	if tr.AccessToken == "" {
		return nil, fmt.Errorf("iam %s: HTTP %d: no access_token in response", endpoint, resp.StatusCode)
	}
	return &tr, nil
}

// refreshGrant exchanges a refresh token for a fresh access token.
func (c *iamClient) refreshGrant(ctx context.Context, refreshToken string) (*tokenResp, error) {
	return c.postForm(ctx, "/v1/iam/oauth/refresh_token", url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {c.clientID},
		"refresh_token": {refreshToken},
	})
}

// decodeJWTClaims base64url-decodes a JWT's payload segment WITHOUT verifying
// the signature — used only to display the user's own token claims locally.
func decodeJWTClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("not a JWT (need 3 dot-separated segments)")
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return nil, fmt.Errorf("decode JWT payload: %w", err)
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, fmt.Errorf("parse JWT claims: %w", err)
	}
	return claims, nil
}

// claimString reads a string claim, tolerating absence.
func claimString(claims map[string]any, key string) string {
	if v, ok := claims[key].(string); ok {
		return v
	}
	return ""
}

// credsFromToken builds a Credentials carrying the token plus the identity
// fields decoded from its claims. expiresIn (token endpoint) wins for expiry;
// otherwise the JWT `exp` claim is used.
func credsFromToken(tr *tokenResp) *Credentials {
	c := &Credentials{
		AccessToken:  tr.AccessToken,
		RefreshToken: tr.RefreshToken,
		TokenType:    cmp.Or(tr.TokenType, "Bearer"),
	}
	if claims, err := decodeJWTClaims(tr.AccessToken); err == nil {
		c.Subject = cmp.Or(claimString(claims, "email"), claimString(claims, "sub"))
		c.Owner = claimString(claims, "owner")
		if exp, ok := claims["exp"].(float64); ok {
			c.Expiry = int64(exp)
		}
	}
	if tr.ExpiresIn > 0 {
		c.Expiry = time.Now().Add(time.Duration(tr.ExpiresIn) * time.Second).Unix()
	}
	return c
}

// ---------------------------------------------------------------------------
// Commands: login / logout / whoami, grouped under `auth`.
// ---------------------------------------------------------------------------

// loginFlags are shared by `hanzo login` and `hanzo auth login`.
type loginFlags struct {
	token         string
	platformToken string
	scope         string
}

func runLogin(env *Env, lf *loginFlags, cmd *cobra.Command) error {
	var creds *Credentials
	// want is the stored identity this sign-in is for, when one was picked: the
	// device grant signs in as whoever the approving page is signed in as, so an
	// approval as anyone else is filed beside it, never made active.
	want := ""

	switch {
	case lf.token != "":
		// Paste an externally-minted token. Decode claims for identity.
		creds = credsFromToken(&tokenResp{AccessToken: lf.token, TokenType: "Bearer"})
	default:
		// On a terminal holding identities, ask which account this is for first.
		// A stored one whose session still renews is simply switched to; one that
		// has ended is signed in again, by name.
		if isTerminal(cmd.InOrStdin(), cmd.OutOrStdout()) {
			store, err := LoadIdentities()
			if err != nil {
				return err
			}
			if len(store.Identities) > 0 {
				key, err := pickIdentity(cmd.InOrStdin(), cmd.OutOrStdout(), store)
				if err != nil {
					return err
				}
				if key != "" {
					if _, err := env.freshToken(cmd.Context(), store, key); err == nil {
						return switchTo(cmd.OutOrStdout(), key)
					}
					fmt.Fprintf(cmd.OutOrStdout(), "The %s session has ended; sign in to it again.\n", key)
					want = key
				}
			}
		}
		// The ONE interactive way: RFC 8628 device flow — link + QR + code,
		// approve from any signed-in browser or phone. Headless-safe.
		c, err := runDeviceLogin(cmd, env, lf.scope, want)
		if err != nil {
			return err
		}
		creds = c
	}

	// Optional machine-to-machine tokens for the platform control plane,
	// stored with this identity so apps/deploy work post-login.
	if lf.platformToken != "" {
		creds.PlatformToken = lf.platformToken
	}

	// Persist under this identity's stable key and make it active. A second
	// login as a different owner (e.g. admin vs hanzo for the same email, via a
	// different --client-id) is stored beside the first, never over it;
	// credentials.json mirrors whichever is active for legacy readers.
	store, err := LoadIdentities()
	if err != nil {
		return err
	}
	if got := creds.key(); want != "" && got != want {
		store.Identities[got] = creds
		if err := store.Save(); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "You asked for %s, and the page approved %s: it is stored beside, and %s is still active.\n"+
			"Sign out on the page and run `hanzo login` again, or `hanzo auth switch %s` to use it.\n", want, got, store.Active, got)
		return nil
	}
	key := store.Put(creds)
	if err := store.Save(); err != nil {
		return err
	}
	who := cmp.Or(creds.Subject, "(unknown)")
	if creds.Owner != "" {
		who += " @ " + creds.Owner
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s [%s] (token expires %s)\n", who, key, shortTime(creds.Expiry))
	return nil
}

// isTerminal reports whether login can ask a question: both ends are a terminal.
// A variable so a test can stand in for one.
var isTerminal = func(in io.Reader, out io.Writer) bool {
	return charDevice(in) && charDevice(out)
}

func charDevice(v any) bool {
	f, ok := v.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

// pickIdentity asks which stored identity a sign-in is for: its key, or "" for
// another account. Enter takes the active one; end of input cancels.
func pickIdentity(in io.Reader, out io.Writer, store *IdentityStore) (string, error) {
	keys := store.keys()
	def := len(keys) + 1
	fmt.Fprintln(out, "Sign in as:")
	for i, k := range keys {
		line := k
		if s := store.Identities[k].Subject; s != "" {
			line += "  " + s
		}
		if k == store.Active {
			line += " (active)"
			def = i + 1
		}
		fmt.Fprintf(out, "  %d) %s\n", i+1, line)
	}
	fmt.Fprintf(out, "  %d) Another account\n", len(keys)+1)
	fmt.Fprintf(out, "Choose [%d]: ", def)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return "", fmt.Errorf("sign-in cancelled")
	}
	n := def
	if t := strings.TrimSpace(line); t != "" {
		if n, err = strconv.Atoi(t); err != nil || n < 1 || n > len(keys)+1 {
			return "", fmt.Errorf("no choice %q: pick 1 to %d", t, len(keys)+1)
		}
	}
	if n == len(keys)+1 {
		return "", nil
	}
	return keys[n-1], nil
}

// switchTo makes the stored identity sel names active: `hanzo auth switch`, and
// a login that picked an identity whose session still renews.
func switchTo(out io.Writer, sel string) error {
	store, err := LoadIdentities()
	if err != nil {
		return err
	}
	key, err := store.resolve(sel)
	if err != nil {
		return err
	}
	store.Active = key
	if err := store.Save(); err != nil {
		return err
	}
	c := store.Identities[key]
	who := cmp.Or(c.Subject, "(unknown)")
	if c.Owner != "" {
		who += " @ " + c.Owner
	}
	fmt.Fprintf(out, "Switched to %s [%s] (token expires %s)\n", who, key, shortTime(c.Expiry))
	return nil
}

func newLoginCmd(envOf func() *Env, _ *globalFlags) *cobra.Command {
	lf := &loginFlags{}
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate against Hanzo IAM and store a token",
		Long: "Authenticate against Hanzo IAM (hanzo.id) and store the token in\n" +
			"~/.hanzo/credentials.json (mode 0600). The sign-in is the device flow: scan\n" +
			"the QR (or open the link) from any signed-in device and approve — no password\n" +
			"touches this terminal, works over ssh/headless. --token stores an\n" +
			"externally-minted token; --platform-token stores the platform control-plane\n" +
			"service token needed by apps/deploy/clusters.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runLogin(envOf(), lf, cmd) },
	}
	bindLoginFlags(cmd, lf)
	return cmd
}

func bindLoginFlags(cmd *cobra.Command, lf *loginFlags) {
	f := cmd.Flags()
	f.StringVar(&lf.token, "token", "", "store this access token directly (skip the device flow)")
	f.StringVar(&lf.platformToken, "platform-token", "", "also store the platform control-plane service token")
	f.StringVar(&lf.scope, "scope", "openid profile email", "OAuth scope")
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "logout [<owner>]",
		Short:             "Remove a stored identity (the active one, or the named owner)",
		Args:              cobra.MaximumNArgs(1),
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := LoadIdentities()
			if err != nil {
				return err
			}
			if len(store.Identities) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "Not logged in.")
				return nil
			}
			target := store.Active
			if len(args) == 1 {
				if target, err = store.resolve(args[0]); err != nil {
					return err
				}
			}
			store.Remove(target)
			if err := store.Save(); err != nil {
				return err
			}
			msg := "Logged out of " + target + "."
			if store.Active != "" {
				msg += " Active is now " + store.Active + "."
			}
			fmt.Fprintln(cmd.OutOrStdout(), msg)
			return nil
		},
	}
}

// identityRow is the JSON/table projection of one stored identity.
type identityRow struct {
	Key     string `json:"key"`
	Owner   string `json:"owner"`
	Subject string `json:"subject"`
	Expiry  int64  `json:"expiry,omitempty"`
	Active  bool   `json:"active"`
}

// listIdentities renders every stored identity (active marked with *) — the
// shared body of `hanzo auth list` and `hanzo whoami --all`.
func listIdentities(env *Env, _ *cobra.Command) error {
	store, err := LoadIdentities()
	if err != nil {
		return err
	}
	rows := make([]identityRow, 0, len(store.Identities))
	for _, k := range store.keys() {
		c := store.Identities[k]
		rows = append(rows, identityRow{
			Key: k, Owner: c.Owner, Subject: c.Subject, Expiry: c.Expiry,
			Active: k == store.Active,
		})
	}
	return env.emit(rows, func(w io.Writer) {
		if len(rows) == 0 {
			fmt.Fprintln(w, "No stored identities. Run `hanzo login`.")
			return
		}
		tw := newTab(w)
		fmt.Fprintln(tw, "ACTIVE\tKEY\tOWNER\tSUBJECT\tEXPIRES")
		for _, r := range rows {
			active := ""
			if r.Active {
				active = "*"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", active, r.Key, r.Owner, r.Subject, shortTime(r.Expiry))
		}
		tw.Flush()
	})
}

func newWhoamiCmd(envOf func() *Env) *cobra.Command {
	var verify, all bool
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show the active identity from the stored token (--all lists every stored identity)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env := envOf()
			if all {
				return listIdentities(env, cmd)
			}
			tok := env.accessToken()
			if tok == "" {
				return fmt.Errorf("not logged in: run `hanzo login`")
			}
			claims, err := decodeJWTClaims(tok)
			if err != nil {
				return err
			}
			if verify {
				if err := verifyUserInfo(cmd.Context(), env, tok); err != nil {
					return fmt.Errorf("token rejected by IAM: %w", err)
				}
			}
			return env.emit(claims, func(w io.Writer) {
				fmt.Fprintf(w, "email:   %s\n", claimString(claims, "email"))
				fmt.Fprintf(w, "name:    %s\n", cmp.Or(claimString(claims, "displayName"), claimString(claims, "name")))
				fmt.Fprintf(w, "org:     %s\n", claimString(claims, "owner"))
				fmt.Fprintf(w, "subject: %s\n", claimString(claims, "sub"))
				fmt.Fprintf(w, "issuer:  %s\n", claimString(claims, "iss"))
				if exp, ok := claims["exp"].(float64); ok {
					fmt.Fprintf(w, "expires: %s\n", shortTime(int64(exp)))
				}
				if verify {
					fmt.Fprintln(w, "verified: yes (IAM userinfo accepted the token)")
				}
			})
		},
	}
	cmd.Flags().BoolVar(&verify, "verify", false, "verify the token against the IAM userinfo endpoint")
	cmd.Flags().BoolVar(&all, "all", false, "list every stored identity (active marked with *)")
	return cmd
}

// verifyUserInfo calls the IAM userinfo endpoint with the bearer token; a 2xx
// means IAM accepts the token as live.
func verifyUserInfo(ctx context.Context, env *Env, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, env.IAMIssuer+"/v1/iam/oauth/userinfo", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "hanzo-cli/"+Version)
	resp, err := (&http.Client{Timeout: 20 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// refreshWindow is how near its expiry a token is refreshed rather than printed.
const refreshWindow = 90 * time.Second

func execTokenHelper(ctx context.Context, cmdLine string, shell bool) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var cmd *exec.Cmd
	if shell {
		cmd = exec.CommandContext(cctx, "sh", "-c", cmdLine)
	} else {
		cmd = exec.CommandContext(cctx, cmdLine)
	}
	cmd.Env = append(os.Environ(), "HANZO_TOKEN_COMMAND=")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	tok := strings.TrimSpace(string(out))
	if tok == "" {
		return "", fmt.Errorf("token helper returned empty output")
	}
	return tok, nil
}

func machineLinkTokenFallback(ctx context.Context) (string, bool) {
	dir, err := hanzoDir()
	if err != nil || dir == "" {
		return "", false
	}
	helper := filepath.Join(dir, "link", "token")
	fi, err := os.Stat(helper)
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm()&0o077 != 0 {
		return "", false
	}
	tok, err := execTokenHelper(ctx, helper, false)
	if err != nil {
		return "", false
	}
	return tok, true
}

// authToken is the access token `hanzo auth token` prints: HANZO_TOKEN when it
// is set and no identity is named, else HANZO_TOKEN_COMMAND, else a live token
// for the active identity (falling back to ~/.hanzo/link/token when an
// unattended machine's interactive session is absent or has ended), or for the
// stored identity `as` names. Naming one never makes it active or falls back.
func (e *Env) authToken(ctx context.Context, as string) (string, error) {
	if as == "" {
		if t := os.Getenv("HANZO_TOKEN"); t != "" {
			return t, nil
		}
		if cmdLine := strings.TrimSpace(os.Getenv("HANZO_TOKEN_COMMAND")); cmdLine != "" {
			return execTokenHelper(ctx, cmdLine, true)
		}
	}
	store, err := LoadIdentities()
	if err != nil {
		return "", err
	}
	key := store.Active
	if as != "" {
		if key, err = store.resolve(as); err != nil {
			return "", err
		}
	}
	if store.Identities[key] == nil {
		if as == "" {
			if tok, ok := machineLinkTokenFallback(ctx); ok {
				return tok, nil
			}
		}
		return "", fmt.Errorf("not logged in: run `hanzo login`")
	}
	tok, ferr := e.freshToken(ctx, store, key)
	if ferr != nil && as == "" {
		if mtok, ok := machineLinkTokenFallback(ctx); ok {
			return mtok, nil
		}
	}
	return tok, ferr
}

// freshToken returns a live access token for the stored identity key. One with
// more than refreshWindow left is returned as stored. Otherwise it is refreshed
// and filed back under the same key, so a long-lived caller never holds a token
// past its expiry, and the active identity is left as it was. A session IAM
// refuses to renew, or a refresh that comes back as someone else, is an error
// saying how to sign in again; it is never answered with an expired token.
func (e *Env) freshToken(ctx context.Context, store *IdentityStore, key string) (string, error) {
	c := store.Identities[key]
	if c.AccessToken == "" {
		return "", fmt.Errorf("the %s session holds no token: run `hanzo login`", key)
	}
	if c.Expiry == 0 || time.Now().Add(refreshWindow).Unix() < c.Expiry {
		return c.AccessToken, nil
	}
	stillValid := time.Now().Unix() < c.Expiry
	if c.RefreshToken == "" {
		if stillValid {
			return c.AccessToken, nil
		}
		return "", fmt.Errorf("the %s session has ended: run `hanzo login`", key)
	}
	tr, err := newIAMClient(e.IAMIssuer, e.ClientID).refreshGrant(ctx, c.RefreshToken)
	if err != nil {
		if stillValid {
			return c.AccessToken, nil
		}
		return "", fmt.Errorf("the %s session has ended (%v): run `hanzo login`", key, err)
	}
	nc := credsFromToken(tr)
	if nc.key() != key {
		return "", fmt.Errorf("the refreshed credential identifies as %s, not %s: refusing to file it; run `hanzo login`", nc.key(), key)
	}
	nc.PlatformToken = c.PlatformToken
	nc.RefreshToken = cmp.Or(nc.RefreshToken, c.RefreshToken)
	// File it into the store as it is now, not as it was read before the network:
	// another process may have signed in or out meanwhile.
	latest, err := LoadIdentities()
	if err != nil {
		return "", err
	}
	if latest.Identities[key] != nil {
		latest.Identities[key] = nc
		if err := latest.Save(); err != nil {
			return "", fmt.Errorf("refreshed the %s token but could not file it: %w", key, err)
		}
	}
	return nc.AccessToken, nil
}

// newAuthCmd is the `auth` group: login/logout/whoami plus `token` (print the
// stored access token, for piping into other tools).
func newAuthCmd(envOf func() *Env, gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication and stored identities",
	}
	var as string
	tokenCmd := &cobra.Command{
		Use:   "token",
		Short: "Print the active access token (--as <owner|owner/name> prints another identity's)",
		Long: `Print a live access token, refreshing it through IAM when it is about to lapse.

With no flag it is the active identity's. --as names another stored identity —
owner/name, or a bare owner when only one is held there — and prints its token
without changing which identity is active. Other tools take their login from this.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tok, err := envOf().authToken(cmd.Context(), as)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), tok)
			return nil
		},
	}
	tokenCmd.Flags().StringVar(&as, "as", "", "print this stored identity's token: owner/name, or an owner (see `hanzo auth list`)")
	listCmd := &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls", "identities"},
		Short:   "List stored identities (active marked with *)",
		Args:    cobra.NoArgs,
		RunE:    func(cmd *cobra.Command, _ []string) error { return listIdentities(envOf(), cmd) },
	}
	switchCmd := &cobra.Command{
		Use:     "switch <owner>",
		Aliases: []string{"use"},
		Short:   "Make a stored identity active (accepts an owner, or a full owner/name key)",
		Args:    cobra.ExactArgs(1),
		RunE:    func(cmd *cobra.Command, args []string) error { return switchTo(cmd.OutOrStdout(), args[0]) },
	}
	cmd.AddCommand(newLoginCmd(envOf, gf), newLogoutCmd(), newWhoamiCmd(envOf), tokenCmd, listCmd, switchCmd)
	return cmd
}
