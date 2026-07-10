// iam_admin.go — admin-scope IAM helpers used by the commerce-grant CLI to
// resolve an IAM user by email. These helpers call IAM admin
// endpoints (hanzo.id) using client-credentials (clientId/clientSecret) query
// auth — the same mechanism Cloud-API uses for /api/add-usage-record.
//
// These are kept separate from the OIDC-focused IAMClient so that ordinary
// request-path code does not accidentally depend on admin surface area.
package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// IAMAdminClient is a thin HTTP client for IAM admin endpoints on hanzo.id.
// All calls authenticate via clientId/clientSecret query params — no session.
type IAMAdminClient struct {
	BaseURL      string       // e.g. https://hanzo.id
	ClientID     string
	ClientSecret string
	HTTPClient   *http.Client
}

// NewIAMAdminClient constructs an admin client. BaseURL must be the IAM origin
// without trailing slash; ClientID/ClientSecret are the service credentials.
func NewIAMAdminClient(baseURL, clientID, clientSecret string, httpClient *http.Client) *IAMAdminClient {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &IAMAdminClient{
		BaseURL:      strings.TrimRight(baseURL, "/"),
		ClientID:     clientID,
		ClientSecret: clientSecret,
		HTTPClient:   httpClient,
	}
}

// AdminUser is the subset of the IAM user record we care about.
type AdminUser struct {
	Owner       string `json:"owner"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	DisplayName string `json:"displayName,omitempty"`
	ID          string `json:"id,omitempty"`
}

// Subject returns the canonical "owner/name" identifier.
func (u AdminUser) Subject() string {
	if u.Owner == "" || u.Name == "" {
		return ""
	}
	return u.Owner + "/" + u.Name
}

// GetUserByEmail finds a user by email across all organizations. It uses
// The IAM /api/get-global-users search endpoint with field=email filter.
func (c *IAMAdminClient) GetUserByEmail(ctx context.Context, email string) (*AdminUser, error) {
	if email == "" {
		return nil, fmt.Errorf("iam admin: email required")
	}
	if c.BaseURL == "" || c.ClientID == "" || c.ClientSecret == "" {
		return nil, fmt.Errorf("iam admin: BaseURL/ClientID/ClientSecret required")
	}

	params := url.Values{}
	params.Set("clientId", c.ClientID)
	params.Set("clientSecret", c.ClientSecret)
	params.Set("field", "email")
	params.Set("value", email)
	params.Set("pageSize", "5")
	params.Set("p", "1")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.BaseURL+"/api/get-global-users?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("iam admin: get-global-users: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("iam admin: get-global-users: status %d: %s", resp.StatusCode, string(body))
	}

	// IAM wraps responses as { status, msg, data, data2 }.
	var envelope struct {
		Status string          `json:"status"`
		Msg    string          `json:"msg"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, fmt.Errorf("iam admin: decode envelope: %w", err)
	}
	if envelope.Status != "" && envelope.Status != "ok" {
		return nil, fmt.Errorf("iam admin: %s", envelope.Msg)
	}

	var users []AdminUser
	if len(envelope.Data) > 0 && envelope.Data[0] == '[' {
		if err := json.Unmarshal(envelope.Data, &users); err != nil {
			return nil, fmt.Errorf("iam admin: decode users: %w", err)
		}
	} else {
		// Some endpoints return a single object.
		var single AdminUser
		if err := json.Unmarshal(envelope.Data, &single); err == nil && single.Email != "" {
			users = []AdminUser{single}
		}
	}

	// Exact-match the email case-insensitively (IAM search is a LIKE).
	emailLC := strings.ToLower(email)
	for i := range users {
		if strings.ToLower(users[i].Email) == emailLC {
			return &users[i], nil
		}
	}
	if len(users) == 1 {
		return &users[0], nil
	}
	return nil, nil
}
