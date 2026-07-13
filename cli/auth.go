package cli

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"
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

// passwordGrant exchanges username+password for a token (the live IAM client
// supports password grant; device_code is hard-rejected server-side).
func (c *iamClient) passwordGrant(ctx context.Context, username, password, scope string) (*tokenResp, error) {
	return c.postForm(ctx, "/v1/iam/oauth/access_token", url.Values{
		"grant_type": {"password"},
		"client_id":  {c.clientID},
		"username":   {username},
		"password":   {password},
		"scope":      {scope},
	})
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
		TokenType:    firstNonEmpty(tr.TokenType, "Bearer"),
	}
	if claims, err := decodeJWTClaims(tr.AccessToken); err == nil {
		c.Subject = firstNonEmpty(claimString(claims, "email"), claimString(claims, "sub"))
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
	username      string
	passwordStdin bool
	token         string
	platformToken string
	buildToken    string
	scope         string
}

func runLogin(env *Env, lf *loginFlags, cmd *cobra.Command) error {
	creds, err := LoadCredentials()
	if err != nil {
		return err
	}

	switch {
	case lf.token != "":
		// Paste an externally-minted token. Decode claims for identity.
		tr := &tokenResp{AccessToken: lf.token, TokenType: "Bearer"}
		creds = credsFromToken(tr)
	case lf.username != "" || lf.passwordStdin:
		// Password grant — kept for automation (--username/--password-stdin).
		username := lf.username
		if username == "" {
			username, err = prompt(cmd, "Email: ")
			if err != nil {
				return err
			}
		}
		password, err := readPassword(cmd, lf.passwordStdin)
		if err != nil {
			return err
		}
		iam := newIAMClient(env.IAMIssuer, env.ClientID)
		tr, err := iam.passwordGrant(cmd.Context(), username, password, lf.scope)
		if err != nil {
			return err
		}
		creds = credsFromToken(tr)
	default:
		// The ONE interactive way: RFC 8628 device flow — link + QR + code,
		// approve from any signed-in browser or phone. Headless-safe.
		creds, err = runDeviceLogin(cmd, env, lf.scope)
		if err != nil {
			return err
		}
	}

	// Optional machine-to-machine tokens for the platform control plane,
	// stored alongside the identity so apps/deploy work post-login.
	if lf.platformToken != "" {
		creds.PlatformToken = lf.platformToken
	}
	if lf.buildToken != "" {
		creds.BuildToken = lf.buildToken
	}

	if err := creds.Save(); err != nil {
		return err
	}
	who := firstNonEmpty(creds.Subject, "(unknown)")
	if creds.Owner != "" {
		who += " @ " + creds.Owner
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Logged in as %s (token expires %s)\n", who, shortTime(creds.Expiry))
	return nil
}

func newLoginCmd(envOf func() *Env, _ *globalFlags) *cobra.Command {
	lf := &loginFlags{}
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate against Hanzo IAM and store a token",
		Long: "Authenticate against Hanzo IAM (hanzo.id) and store the token in\n" +
			"~/.hanzo/credentials.json (mode 0600). Default is the device flow: scan the\n" +
			"QR (or open the link) from any signed-in device and approve — no password\n" +
			"touches this terminal, works over ssh/headless. --username/--password-stdin\n" +
			"keep the password grant for automation; --token stores an externally-minted\n" +
			"token; --platform-token stores the platform control-plane service token\n" +
			"needed by apps/deploy/clusters.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runLogin(envOf(), lf, cmd) },
	}
	bindLoginFlags(cmd, lf)
	return cmd
}

func bindLoginFlags(cmd *cobra.Command, lf *loginFlags) {
	f := cmd.Flags()
	f.StringVarP(&lf.username, "username", "u", "", "IAM username/email")
	f.BoolVar(&lf.passwordStdin, "password-stdin", false, "read the password from stdin (for automation)")
	f.StringVar(&lf.token, "token", "", "store this access token directly (skip the password grant)")
	f.StringVar(&lf.platformToken, "platform-token", "", "also store the platform control-plane service token")
	f.StringVar(&lf.buildToken, "build-token", "", "also store the platform build-enqueue token")
	f.StringVar(&lf.scope, "scope", "openid profile email", "OAuth scope")
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "logout",
		Short:             "Remove stored credentials",
		Args:              cobra.NoArgs,
		PersistentPreRunE: func(*cobra.Command, []string) error { return nil },
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := DeleteCredentials(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Logged out.")
			return nil
		},
	}
}

func newWhoamiCmd(envOf func() *Env) *cobra.Command {
	var verify bool
	cmd := &cobra.Command{
		Use:   "whoami",
		Short: "Show the current identity from the stored token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			env := envOf()
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
				fmt.Fprintf(w, "name:    %s\n", firstNonEmpty(claimString(claims, "displayName"), claimString(claims, "name")))
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

// newAuthCmd is the `auth` group: login/logout/whoami plus `token` (print the
// stored access token, for piping into other tools).
func newAuthCmd(envOf func() *Env, gf *globalFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Manage authentication",
	}
	tokenCmd := &cobra.Command{
		Use:   "token",
		Short: "Print the stored access token",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			tok := envOf().accessToken()
			if tok == "" {
				return fmt.Errorf("not logged in: run `hanzo login`")
			}
			fmt.Fprintln(cmd.OutOrStdout(), tok)
			return nil
		},
	}
	cmd.AddCommand(newLoginCmd(envOf, gf), newLogoutCmd(), newWhoamiCmd(envOf), tokenCmd)
	return cmd
}

// ---------------------------------------------------------------------------
// Terminal helpers.
// ---------------------------------------------------------------------------

// prompt writes a prompt to stderr and reads a trimmed line from stdin.
func prompt(cmd *cobra.Command, label string) (string, error) {
	fmt.Fprint(cmd.ErrOrStderr(), label)
	r := bufio.NewReader(cmd.InOrStdin())
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// readPassword reads a password without echo from the terminal, or as a plain
// line from stdin when --password-stdin is set (automation) or stdin is not a
// terminal.
func readPassword(cmd *cobra.Command, fromStdin bool) (string, error) {
	if fromStdin {
		r := bufio.NewReader(cmd.InOrStdin())
		line, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		fmt.Fprint(cmd.ErrOrStderr(), "Password: ")
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(cmd.ErrOrStderr())
		return string(b), err
	}
	// Non-terminal stdin without --password-stdin: read a line so piped input
	// still works, but nudge toward the explicit flag.
	r := bufio.NewReader(cmd.InOrStdin())
	line, err := r.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
