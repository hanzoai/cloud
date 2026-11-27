package forge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/brand"
	"github.com/hanzoai/cloud/secret"
)

// iamToken discovers readiness before exchanging a credential. auto is a staged
// migration: only the old server's explicit 404 permits its existing credential.
// Once IAM has answered, this process never downgrades. Production finishes the
// cutover with mode=iam, which has no legacy path, including after a restart.
func (s *Source) iamToken(ctx context.Context, kms Secrets, domain, mode string) (string, time.Duration, bool, error) {
	issuer := strings.TrimRight(strings.TrimSpace(os.Getenv("HANZO_IAM_ISSUER")), "/")
	if issuer == "" {
		issuer = "https://hanzo.id"
	}
	if _, err := New(issuer, "validation"); err != nil {
		return "", 0, false, fmt.Errorf("forge: invalid IAM issuer")
	}
	base, err := New(s.host(domain), "discovery")
	if err != nil {
		return "", 0, false, err
	}
	h := s.HTTPClient
	if h == nil {
		h = &http.Client{Timeout: 15 * time.Second}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.base+"/identity-authority", nil)
	if err != nil {
		return "", 0, false, err
	}
	res, err := h.Do(req)
	if err != nil {
		return "", 0, false, fmt.Errorf("forge: native authority is unreachable")
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound && mode == "auto" && !s.iamSeen {
		b, err := kms.GetSecret(ctx, TokenRef)
		if err != nil {
			return "", 0, false, fmt.Errorf("read %s: %w", TokenRef, err)
		}
		token := strings.TrimSpace(string(b))
		if token == "" {
			return "", 0, false, fmt.Errorf("%s is empty", TokenRef)
		}
		return token, 5 * time.Second, false, nil
	}
	if res.StatusCode != http.StatusOK {
		return "", 0, false, fmt.Errorf("forge: native authority answered HTTP %d", res.StatusCode)
	}
	var authority struct {
		Authority  string `json:"authority"`
		Issuer     string `json:"issuer"`
		MachineAPI bool   `json:"machineAPI"`
	}
	if json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&authority) != nil || authority.Authority != "hanzo-iam" || !brand.Trusts(issuer, strings.TrimRight(authority.Issuer, "/")) || !authority.MachineAPI {
		return "", 0, false, fmt.Errorf("forge: native machine authority is not ready or does not match IAM")
	}
	s.iamSeen = true // an observed native authority may never fall back
	mint := s.WorkloadToken
	if mint == nil {
		mint = secret.WorkloadToken
	}
	token, err := mint(ctx)
	if err != nil {
		return "", 0, false, fmt.Errorf("forge: IAM did not issue a machine access token")
	}
	if token == nil || !strings.HasPrefix(token.AccessToken, "ey") || strings.Count(token.AccessToken, ".") != 2 || token.Expiry.Before(time.Now().Add(30*time.Second)) {
		return "", 0, false, fmt.Errorf("forge: IAM returned no usable short-lived JWT")
	}
	// A native token must actually hold the machine API grant before it is lent
	// to a caller. Merely minting a token does not prove the account is linked.
	check, err := http.NewRequestWithContext(ctx, http.MethodGet, base.base+"/user", nil)
	if err != nil {
		return "", 0, false, err
	}
	check.Header.Set("Authorization", "Bearer "+token.AccessToken)
	admitted, err := h.Do(check)
	if err != nil {
		return "", 0, false, fmt.Errorf("forge: IAM machine admission is unreachable")
	}
	defer admitted.Body.Close()
	var user struct {
		IsAdmin bool `json:"is_admin"`
	}
	if admitted.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(admitted.Body, 65536)).Decode(&user) != nil || !user.IsAdmin {
		return "", 0, false, fmt.Errorf("forge: IAM machine identity is not admitted as an administrator")
	}
	roleReq, _ := http.NewRequestWithContext(ctx, http.MethodGet, base.base+"/identity-authority", nil)
	roleReq.Header.Set("Authorization", "Bearer "+token.AccessToken)
	roleRes, err := h.Do(roleReq)
	if err != nil {
		return "", 0, false, fmt.Errorf("forge: IAM role admission is unreachable")
	}
	defer roleRes.Body.Close()
	var role struct {
		CredentialRole string `json:"credentialRole"`
	}
	if roleRes.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(roleRes.Body, 65536)).Decode(&role) != nil || role.CredentialRole != "machine" {
		return "", 0, false, fmt.Errorf("forge: IAM identity lacks the machine API grant")
	}
	ttl := min(fresh, time.Until(token.Expiry)-30*time.Second)
	return token.AccessToken, ttl, true, nil
}

// Native IAM JWTs ride as Bearer. The token scheme is retained solely for the
// explicitly selected pre-cutover client; the IAM-only server accepts none of it.
func authorization(token string) string {
	if strings.HasPrefix(token, "ey") && strings.Count(token, ".") == 2 {
		return "Bearer " + token
	}
	return "token " + token
}
