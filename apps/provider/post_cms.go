package provider

// post_cms.go — Native Go connectors and publishers for Dev.to, Medium, Hashnode,
// and Hanzo CMS (cms.hanzo.ai / Ghost Admin & Payload REST). Credentials are sealed
// per-org in Hanzo KMS under accessSecret and never leave the provider plane.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var (
	devtoAPI    = "https://dev.to/api"
	mediumAPI   = "https://api.medium.com/v1"
	hashnodeAPI = "https://gql.hashnode.com"
	hanzoCMSAPI = "https://cms.hanzo.ai"
)

func init() {
	register(&Provider{
		ID:           "devto",
		Name:         "Dev.to",
		Description:  "Publish technical articles to Dev.to with an API key.",
		Category:     "Social",
		Kind:         apiKeyKind,
		AdminOnly:    true,
		Secrets:      []string{accessSecret},
		MultiAccount: true,
		PerTarget:    true,
		Configured:   func() bool { return true },
		Creds:        func() OAuthConfig { return OAuthConfig{} },
		Verify:       verifyDevTo,
		Fields: []Field{
			{Name: "api_key", Label: "API Key", Secret: true, Required: true, Help: "Generate a Forem/Dev.to API key in Settings -> Extensions."},
		},
	})
	register(&Provider{
		ID:           "medium",
		Name:         "Medium",
		Description:  "Publish articles to Medium with an integration token.",
		Category:     "Social",
		Kind:         apiKeyKind,
		AdminOnly:    true,
		Secrets:      []string{accessSecret},
		MultiAccount: true,
		PerTarget:    true,
		Configured:   func() bool { return true },
		Creds:        func() OAuthConfig { return OAuthConfig{} },
		Verify:       verifyMedium,
		Fields: []Field{
			{Name: "token", Label: "Integration Token", Secret: true, Required: true, Help: "Generate a Medium Integration Token in Settings -> Security and apps."},
		},
	})
	register(&Provider{
		ID:           "hashnode",
		Name:         "Hashnode",
		Description:  "Publish articles to a Hashnode publication via GraphQL Personal Access Token.",
		Category:     "Social",
		Kind:         apiKeyKind,
		AdminOnly:    true,
		Secrets:      []string{accessSecret, "publication_id"},
		MultiAccount: true,
		PerTarget:    true,
		Configured:   func() bool { return true },
		Creds:        func() OAuthConfig { return OAuthConfig{} },
		Verify:       verifyHashnode,
		Fields: []Field{
			{Name: "token", Label: "Personal Access Token", Secret: true, Required: true, Help: "Hashnode developer Personal Access Token."},
			{Name: "publication_id", Label: "Publication ID", Required: true, Help: "Target Hashnode Publication ObjectID."},
		},
	})
	register(&Provider{
		ID:           "hanzo-cms",
		Name:         "Hanzo CMS",
		Description:  "Publish articles and changelogs to Hanzo CMS (cms.hanzo.ai, hanzo.blog, zenlm.org).",
		Category:     "Social",
		Kind:         apiKeyKind,
		AdminOnly:    true,
		Secrets:      []string{accessSecret, "site_url"},
		MultiAccount: true,
		PerTarget:    true,
		Configured:   func() bool { return true },
		Creds:        func() OAuthConfig { return OAuthConfig{} },
		Verify:       verifyHanzoCMS,
		Fields: []Field{
			{Name: "api_key", Label: "CMS API Key", Secret: true, Required: true, Help: "Hanzo CMS / Ghost Admin API key or bearer token."},
			{Name: "site_url", Label: "Site URL", Help: "Target CMS origin (defaults to https://cms.hanzo.ai).", Default: hanzoCMSAPI},
		},
	})
}

func splitTitleBody(text string) (string, string) {
	trimmed := strings.TrimSpace(text)
	if i := strings.IndexByte(trimmed, '\n'); i > 0 {
		title := strings.TrimSpace(strings.TrimLeft(trimmed[:i], "# "))
		body := strings.TrimSpace(trimmed[i+1:])
		if title != "" && body != "" {
			return title, body
		}
	}
	if len(trimmed) > 72 {
		return trimmed[:72] + "…", trimmed
	}
	return trimmed, trimmed
}

func verifyDevTo(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
	key := strings.TrimSpace(in.Fields["api_key"])
	if key == "" {
		return nil, verifyRefused("Enter your Dev.to API key.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, devtoAPI+"/users/me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("api-key", key)
	resp, err := postClient.Do(req)
	if err != nil {
		return nil, verifyRefused("Dev.to could not be reached.")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, verifyRefused("Dev.to did not accept that API key.")
	}
	var me struct {
		ID       int    `json:"id"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&me); err != nil || me.ID == 0 {
		return nil, verifyRefused("Dev.to returned an unreadable profile.")
	}
	id := fmt.Sprintf("%d", me.ID)
	return &ExchangeResult{
		Tokens:       map[string]string{accessSecret: key},
		ExternalID:   id,
		Key:          keyOf(id),
		AccountLabel: "@" + me.Username,
	}, nil
}

func postDevTo(ctx context.Context, to postTo) (string, error) {
	title, body := splitTitleBody(to.Text)
	payload := map[string]any{
		"article": map[string]any{
			"title":         title,
			"body_markdown": body,
			"published":     true,
		},
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, devtoAPI+"/articles", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("api-key", to.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := postClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Dev.to rejected the article (%d)", resp.StatusCode)
	}
	var out struct {
		URL string `json:"url"`
	}
	_ = json.Unmarshal(b, &out)
	return out.URL, nil
}

func verifyMedium(ctx context.Context, in VerifyInput) (*ExchangeResult, error) {
	tok := strings.TrimSpace(in.Fields["token"])
	if tok == "" {
		return nil, verifyRefused("Enter your Medium Integration Token.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mediumAPI+"/me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := postClient.Do(req)
	if err != nil {
		return nil, verifyRefused("Medium could not be reached.")
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, verifyRefused("Medium did not accept that token.")
	}
	var out struct {
		Data struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Data.ID == "" {
		return nil, verifyRefused("Medium returned an unreadable profile.")
	}
	return &ExchangeResult{
		Tokens:       map[string]string{accessSecret: tok},
		ExternalID:   out.Data.ID,
		Key:          keyOf(out.Data.ID),
		AccountLabel: "@" + out.Data.Username,
	}, nil
}

func postMedium(ctx context.Context, to postTo) (string, error) {
	title, body := splitTitleBody(to.Text)
	payload := map[string]any{
		"title":         title,
		"contentFormat": "markdown",
		"content":       body,
		"publishStatus": "public",
	}
	raw, _ := json.Marshal(payload)
	u := fmt.Sprintf("%s/users/%s/posts", strings.TrimRight(mediumAPI, "/"), to.Account)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+to.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := postClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Medium rejected the article (%d)", resp.StatusCode)
	}
	var out struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	_ = json.Unmarshal(b, &out)
	return out.Data.URL, nil
}

func verifyHashnode(_ context.Context, in VerifyInput) (*ExchangeResult, error) {
	tok := strings.TrimSpace(in.Fields["token"])
	pub := strings.TrimSpace(in.Fields["publication_id"])
	if tok == "" || pub == "" {
		return nil, verifyRefused("Enter both your Hashnode token and publication ID.")
	}
	return &ExchangeResult{
		Tokens:       map[string]string{accessSecret: tok, "publication_id": pub},
		ExternalID:   pub,
		Key:          keyOf(pub),
		AccountLabel: "hashnode:" + pub,
	}, nil
}

func postHashnode(ctx context.Context, to postTo) (string, error) {
	title, body := splitTitleBody(to.Text)
	pubID := to.Sealed["publication_id"]
	if pubID == "" {
		pubID = to.Account
	}
	gql := map[string]any{
		"query": `mutation PublishPost($input: PublishPostInput!) { publishPost(input: $input) { post { url } } }`,
		"variables": map[string]any{
			"input": map[string]any{
				"publicationId":   pubID,
				"title":           title,
				"contentMarkdown": body,
			},
		},
	}
	raw, _ := json.Marshal(gql)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, hashnodeAPI, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", to.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := postClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Hashnode rejected the article (%d)", resp.StatusCode)
	}
	var out struct {
		Data struct {
			PublishPost struct {
				Post struct {
					URL string `json:"url"`
				} `json:"post"`
			} `json:"publishPost"`
		} `json:"data"`
	}
	_ = json.Unmarshal(b, &out)
	return out.Data.PublishPost.Post.URL, nil
}

func verifyHanzoCMS(_ context.Context, in VerifyInput) (*ExchangeResult, error) {
	key := strings.TrimSpace(in.Fields["api_key"])
	if key == "" {
		return nil, verifyRefused("Enter your Hanzo CMS API key.")
	}
	site := strings.TrimSpace(in.Fields["site_url"])
	if site == "" {
		site = hanzoCMSAPI
	}
	site = strings.TrimRight(site, "/")
	return &ExchangeResult{
		Tokens:       map[string]string{accessSecret: key, "site_url": site},
		ExternalID:   site,
		Key:          keyOf(site),
		AccountLabel: site,
	}, nil
}

func postHanzoCMS(ctx context.Context, to postTo) (string, error) {
	title, body := splitTitleBody(to.Text)
	site := to.Sealed["site_url"]
	if site == "" {
		site = hanzoCMSAPI
	}
	site = strings.TrimRight(site, "/")
	payload := map[string]any{
		"title":   title,
		"content": body,
		"status":  "published",
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, site+"/api/posts", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+to.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := postClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Hanzo CMS rejected the post (%d)", resp.StatusCode)
	}
	var out struct {
		URL  string `json:"url"`
		Slug string `json:"slug"`
	}
	_ = json.Unmarshal(b, &out)
	if out.URL != "" {
		return out.URL, nil
	}
	if out.Slug != "" {
		return site + "/" + strings.TrimLeft(out.Slug, "/"), nil
	}
	return site, nil
}
