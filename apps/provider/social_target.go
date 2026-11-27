package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// A social login is one consent and several accounts. Facebook's publishable
// credential is the Page token, not the user token the code exchanged for.
// LinkedIn's company pages keep the member token and are keyed by the
// organization id, which is a different key from the member, so saving a page
// cannot replace the personal row.
//
// Nothing here writes a temporary row. Each grant is a finished account, and
// its key is its own platform id (or org-<id> for a LinkedIn company, which
// cannot equal the member id).

// socialGrant is one account to seal from a single consent.
type socialGrant struct {
	// ExternalID is the platform id posts are addressed to: a Page id, a member
	// id, or a company id.
	ExternalID string
	// Label is the connection key when it must differ from ExternalID. Empty
	// means the key is ExternalID.
	Label string
	// AccountLabel is the name a person recognizes.
	AccountLabel string
	// Token is what gets sealed. A Facebook Page's own token, or the member
	// token a LinkedIn company post is made with.
	Token string
	// Refresh is what renews Token when that is not the consent's own refresh
	// token. Threads renews a long-lived token with itself.
	Refresh string
	// ExpiresAt is when Token stops working, unix seconds. Zero never does, which
	// is true of a Facebook Page token. It applies to a grant that brings its own
	// token; one that posts with the consent's token expires when that does.
	ExpiresAt int64
	// Kind is "personal" or "page".
	Kind string
	// Author is the provider user who consented.
	Author string
}

var (
	facebookGraph = "https://graph.facebook.com/v21.0"
	linkedinAPIOf = "https://api.linkedin.com"
)

func expandFacebook(ctx context.Context, creds OAuthConfig, token string, who identity) ([]socialGrant, error) {
	long := facebookLongLived(ctx, creds, token)
	pages, err := facebookPages(ctx, long)
	if err != nil {
		return nil, err
	}
	out := make([]socialGrant, 0, len(pages))
	for _, p := range pages {
		if p.ID == "" || p.Token == "" || p.ID == who.ExternalID {
			// A page listed without its own token is not publishable, and the
			// user id is never a page's key.
			continue
		}
		name := p.Name
		if name == "" {
			name = p.ID
		}
		out = append(out, socialGrant{
			ExternalID: p.ID, AccountLabel: name, Token: p.Token,
			Kind: "page", Author: who.ExternalID,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("facebook: the account manages no Page with its own token")
	}
	return out, nil
}

func facebookLongLived(ctx context.Context, creds OAuthConfig, token string) string {
	if creds.ClientID == "" || creds.ClientSecret == "" || token == "" {
		return token
	}
	q := url.Values{
		"grant_type":        {"fb_exchange_token"},
		"client_id":         {creds.ClientID},
		"client_secret":     {creds.ClientSecret},
		"fb_exchange_token": {token},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, facebookGraph+"/oauth/access_token?"+q.Encode(), nil)
	if err != nil {
		return token
	}
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return token
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	var out struct {
		AccessToken string `json:"access_token"`
	}
	if json.Unmarshal(body, &out) != nil || out.AccessToken == "" {
		return token
	}
	return out.AccessToken
}

type fbPage struct {
	ID, Name, Token string
}

func facebookPages(ctx context.Context, token string) ([]fbPage, error) {
	var out struct {
		Data []struct {
			ID    string `json:"id"`
			Name  string `json:"name"`
			Token string `json:"access_token"`
		} `json:"data"`
	}
	u := facebookGraph + "/me/accounts?fields=id,name,access_token&limit=100"
	if err := getJSON(ctx, u, token, &out); err != nil {
		return nil, fmt.Errorf("facebook pages: %w", err)
	}
	pages := make([]fbPage, 0, len(out.Data))
	for _, p := range out.Data {
		pages = append(pages, fbPage{ID: p.ID, Name: p.Name, Token: p.Token})
	}
	return pages, nil
}

func expandLinkedIn(ctx context.Context, _ OAuthConfig, token string, who identity) ([]socialGrant, error) {
	var out []socialGrant
	if who.ExternalID != "" {
		name := who.Label
		if name == "" {
			name = who.ExternalID
		}
		out = append(out, socialGrant{
			ExternalID: who.ExternalID, AccountLabel: name, Token: token,
			Kind: "personal", Author: who.ExternalID,
		})
	}
	companies, err := linkedinCompanies(ctx, token)
	if err != nil {
		// The member can still post. A company scope the app has not been
		// granted is the provider's refusal, not a reason to drop the profile.
		if len(out) == 0 {
			return nil, err
		}
		return out, nil
	}
	for _, c := range companies {
		if c.ID == "" || c.ID == who.ExternalID {
			continue
		}
		name := c.Name
		if name == "" {
			name = c.ID
		}
		out = append(out, socialGrant{
			ExternalID: c.ID, Label: "org-" + c.ID, AccountLabel: name, Token: token,
			Kind: "page", Author: who.ExternalID,
		})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("linkedin: the login named no account")
	}
	return out, nil
}

type liCompany struct{ ID, Name string }

func linkedinCompanies(ctx context.Context, token string) ([]liCompany, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, linkedinAPIOf+
		"/v2/organizationalEntityAcls?q=roleAssignee&role=ADMINISTRATOR&projection=(elements*(organizationalTarget~(localizedName)))", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	req.Header.Set("LinkedIn-Version", "202611")
	resp, err := oauthHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("linkedin companies: %d", resp.StatusCode)
	}
	var parsed struct {
		Elements []struct {
			Target string `json:"organizationalTarget"`
			Org    struct {
				Name string `json:"localizedName"`
			} `json:"organizationalTarget~"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	out := make([]liCompany, 0, len(parsed.Elements))
	for _, e := range parsed.Elements {
		id := e.Target
		if i := strings.LastIndex(id, ":"); i >= 0 {
			id = id[i+1:]
		}
		out = append(out, liCompany{ID: id, Name: e.Org.Name})
	}
	return out, nil
}

// grantKey is the connection key. A LinkedIn company uses org-<id>, which
// cannot equal the member id the personal row is stored under.
func grantKey(g socialGrant) string {
	if g.Label != "" && validLabel(g.Label) {
		return g.Label
	}
	if validLabel(g.ExternalID) {
		return g.ExternalID
	}
	return ""
}

func targetLabel(id string) string {
	if validLabel(id) {
		return id
	}
	return ""
}
