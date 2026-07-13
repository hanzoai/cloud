// device.go — RFC 8628 Device Authorization Grant for `hanzo login`: the ONE
// way any machine signs in. The CLI asks IAM for a device+user code, shows the
// verification link as text AND a terminal QR (scan with a phone), and polls
// the token endpoint until the user approves in any browser session. No
// password ever touches this terminal; works headless (GPU boxes, ssh, CI).
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/spf13/cobra"
)

// deviceAuthResp is the RFC 8628 device authorization response.
type deviceAuthResp struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
	Error                   string `json:"error"`
	ErrorDesc               string `json:"error_description"`
}

const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// defaultDeviceClientID is the first-party client the device grant runs as.
// hanzo-app is the one Hanzo client IAM seeds with device_code enabled
// (iam cmd/iam/cli/init_apps.go brandGrantTypes); hanzo-console stays the
// password-grant client. hanzo-dev's live device flow uses the same pair of
// endpoints and this client, so the two CLIs share one server-side config.
const defaultDeviceClientID = "hanzo-app"

// rawForm posts a form to an oauth endpoint and returns the decoded token
// response WITHOUT mapping OAuth errors to Go errors — the device poll must
// inspect authorization_pending / slow_down itself.
func (c *iamClient) rawForm(ctx context.Context, endpoint string, form url.Values) (*tokenResp, error) {
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
	return &tr, nil
}

// deviceAuth starts the device flow: mints a device_code + user_code pair.
// Query-param shape matches hanzo-dev's verified client (oidc_device_auth.rs).
func (c *iamClient) deviceAuth(ctx context.Context, scope string) (*deviceAuthResp, error) {
	q := url.Values{"client_id": {c.clientID}, "scope": {scope}, "response_type": {"device_code"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.issuer+"/v1/iam/oauth/device?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "hanzo-cli/"+Version)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var da deviceAuthResp
	if err := json.Unmarshal(body, &da); err != nil {
		return nil, fmt.Errorf("iam device auth: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if da.Error != "" {
		return nil, fmt.Errorf("iam device auth: %s: %s", da.Error, da.ErrorDesc)
	}
	if da.DeviceCode == "" || da.UserCode == "" {
		return nil, fmt.Errorf("iam device auth: HTTP %d: no device_code in response", resp.StatusCode)
	}
	return &da, nil
}

// pollDeviceToken polls the token endpoint until the user approves, the code
// expires, or ctx ends. authorization_pending keeps polling; slow_down backs
// off per RFC 8628 §3.5.
func (c *iamClient) pollDeviceToken(ctx context.Context, da *deviceAuthResp) (*tokenResp, error) {
	interval := time.Duration(max64(da.Interval, 1)) * time.Second
	deadline := time.Now().Add(time.Duration(da.ExpiresIn) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("device code expired before approval — run `hanzo login` again")
		}
		tr, err := c.rawForm(ctx, "/v1/iam/oauth/token", url.Values{
			"grant_type":  {deviceGrantType},
			"client_id":   {c.clientID},
			"device_code": {da.DeviceCode},
		})
		if err != nil {
			return nil, err
		}
		switch tr.Error {
		case "":
			if tr.AccessToken == "" {
				return nil, fmt.Errorf("iam device token: empty access_token")
			}
			return tr, nil
		case "authorization_pending":
			// approved not yet — keep waiting
		case "slow_down":
			interval += 5 * time.Second
		case "expired_token":
			return nil, fmt.Errorf("device code expired before approval — run `hanzo login` again")
		default:
			return nil, fmt.Errorf("iam device token: %s: %s", tr.Error, tr.ErrorDesc)
		}
	}
}

// runDeviceLogin drives the interactive device sign-in and returns credentials.
func runDeviceLogin(cmd *cobra.Command, env *Env, scope string) (*Credentials, error) {
	out := cmd.OutOrStdout()
	// An explicit --client-id/HANZO_CLIENT_ID override wins; otherwise the
	// device grant runs as hanzo-app (see defaultDeviceClientID).
	clientID := env.ClientID
	if clientID == "" || clientID == defaultClientID {
		clientID = defaultDeviceClientID
	}
	iam := newIAMClient(env.IAMIssuer, clientID)
	da, err := iam.deviceAuth(cmd.Context(), scope)
	if err != nil {
		return nil, err
	}
	link := firstNonEmpty(da.VerificationURIComplete, da.VerificationURI)
	fmt.Fprintf(out, "\nSign in on any device:\n\n")
	printQR(out, link)
	fmt.Fprintf(out, "\n  %s\n", link)
	if da.VerificationURI != "" && da.UserCode != "" {
		fmt.Fprintf(out, "  or open %s and enter code %s\n", da.VerificationURI, da.UserCode)
	}
	fmt.Fprintf(out, "\nWaiting for approval…\n")
	tr, err := iam.pollDeviceToken(cmd.Context(), da)
	if err != nil {
		return nil, err
	}
	return credsFromToken(tr), nil
}

// printQR renders a scannable QR of the verification link using half blocks
// (compact enough for a default 80×24 terminal).
func printQR(w io.Writer, link string) {
	qrterminal.GenerateWithConfig(link, qrterminal.Config{
		Level:      qrterminal.L,
		Writer:     w,
		HalfBlocks: true,
		QuietZone:  1,
	})
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
