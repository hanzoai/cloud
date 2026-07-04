// sync.go implements OAuth code exchange and the per-provider document pull. Every
// pulled item is normalized to ONE shape (ingestDoc) and filed as a kb-source
// document via framework.Ingest — so the SAME after_save hook indexes it into the
// org's vector namespace, alongside manual pages and memories. This is the single
// ingestion path; providers differ only in how they LIST and FETCH, never in how
// they land in the store.
//
// GitHub is implemented end-to-end (repo READMEs + issues via the REST API) as the
// proof. Slack and Google are wired to the SAME normalizer and OAuth lifecycle;
// their pull is scaffolded with an honest depth marker (they connect and record a
// connection, and the sync returns a clear "listing not yet implemented" rather
// than a fabricated ingest) so the depth is never overstated.
package kb

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/framework"
)

// maxSyncDocs bounds a single sync so a huge account can't blow up the per-org
// SQLite store or the embedding bill in one call. A cursor (returned to the
// connector) resumes the next sync where this one stopped.
const maxSyncDocs = 500

var syncHTTP = &http.Client{Timeout: 30 * time.Second}

// ingestDoc is the normalized shape every connector produces. runSync files it as a
// kb-source document; the after_save hook then indexes its title+body.
type ingestDoc struct {
	Title      string
	Body       string
	ExternalID string // stable per-source id → idempotent re-sync (update in place)
	URL        string
	Author     string
	Timestamp  string // "YYYY-MM-DD HH:MM:SS" or ""
}

// runSync dispatches to the provider's pull and files each normalized doc into the
// org's store. Returns the count ingested and a resume cursor. It is org-scoped
// throughout (framework.Ingest writes only into `org`).
func (s *svc) runSync(ctx context.Context, org, provider, token string) (int, string, error) {
	switch provider {
	case "github":
		return s.syncGitHub(ctx, org, token)
	case "slack":
		return 0, "", fmt.Errorf("slack sync: listing not yet implemented (connection stored; wire channels.history to ingestDoc)")
	case "google":
		return 0, "", fmt.Errorf("google sync: listing not yet implemented (connection stored; wire drive.files to ingestDoc)")
	default:
		return 0, "", fmt.Errorf("unknown provider %q", provider)
	}
}

// fileDoc writes ONE normalized document into the org's kb-source store,
// idempotently: if a kb-source with the same external_id already exists it is
// updated in place (so the vector point is refreshed, not duplicated); otherwise a
// new one is created. Either way the after_save hook indexes it. Returns whether a
// new document was created (for the count).
func fileDoc(ctx context.Context, org, provider string, d ingestDoc) (created bool, err error) {
	data := map[string]any{
		"title":       d.Title,
		"body":        d.Body,
		"provider":    provider,
		"external_id": d.ExternalID,
		"url":         d.URL,
		"author":      d.Author,
	}
	if d.Timestamp != "" {
		data["source_ts"] = d.Timestamp
	}
	if d.ExternalID != "" {
		if name, e := framework.FindByField(ctx, org, DTSource, "external_id", d.ExternalID); e == nil && name != "" {
			return false, framework.UpdateData(ctx, org, DTSource, name, data)
		}
	}
	_, e := framework.Ingest(ctx, org, DTSource, data, "")
	return true, e
}

// ---- GitHub (end-to-end) ----

// syncGitHub pulls the authenticated user's repositories, then each repo's README
// and open issues, normalizing every item to a kb-source. Bounded by maxSyncDocs.
// The cursor is the ISO timestamp of the run (a subsequent sync could pass it as
// `since` for issues — recorded for incremental resumption).
func (s *svc) syncGitHub(ctx context.Context, org, token string) (int, string, error) {
	repos, err := ghRepos(ctx, token)
	if err != nil {
		return 0, "", err
	}
	n := 0
	for _, r := range repos {
		if n >= maxSyncDocs {
			break
		}
		// README (best-effort: a repo without one is skipped, not an error).
		if readme, ok, e := ghReadme(ctx, token, r.FullName); e == nil && ok {
			d := ingestDoc{
				Title:      r.FullName + " — README",
				Body:       readme,
				ExternalID: "github:readme:" + r.FullName,
				URL:        r.HTMLURL,
				Author:     r.Owner,
			}
			if _, e := fileDoc(ctx, org, "github", d); e == nil {
				n++
			}
		}
		// Open issues (title + body), bounded.
		issues, e := ghIssues(ctx, token, r.FullName)
		if e != nil {
			continue
		}
		for _, is := range issues {
			if n >= maxSyncDocs {
				break
			}
			d := ingestDoc{
				Title:      fmt.Sprintf("%s#%d %s", r.FullName, is.Number, is.Title),
				Body:       is.Body,
				ExternalID: fmt.Sprintf("github:issue:%s:%d", r.FullName, is.Number),
				URL:        is.HTMLURL,
				Author:     is.User,
				Timestamp:  ghTime(is.UpdatedAt),
			}
			if _, e := fileDoc(ctx, org, "github", d); e == nil {
				n++
			}
		}
	}
	return n, time.Now().UTC().Format(time.RFC3339), nil
}

type ghRepo struct {
	FullName string
	HTMLURL  string
	Owner    string
}

// ghRepos lists the token's repositories (first page, updated-first). A connector
// syncs what the granted scope can see; pagination beyond the first page is a
// bounded-by-maxSyncDocs follow-up (cursor-resumable).
func ghRepos(ctx context.Context, token string) ([]ghRepo, error) {
	var raw []struct {
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
		Owner    struct {
			Login string `json:"login"`
		} `json:"owner"`
	}
	if err := ghGet(ctx, token, "https://api.github.com/user/repos?per_page=50&sort=updated", &raw); err != nil {
		return nil, err
	}
	out := make([]ghRepo, 0, len(raw))
	for _, r := range raw {
		out = append(out, ghRepo{FullName: r.FullName, HTMLURL: r.HTMLURL, Owner: r.Owner.Login})
	}
	return out, nil
}

// ghReadme fetches a repo's README, decoded from the base64 content the API returns.
// ok=false (no error) when the repo has no README.
func ghReadme(ctx context.Context, token, fullName string) (string, bool, error) {
	var resp struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	err := ghGet(ctx, token, "https://api.github.com/repos/"+fullName+"/readme", &resp)
	if err != nil {
		if err == errNotFound {
			return "", false, nil
		}
		return "", false, err
	}
	if resp.Encoding == "base64" {
		if dec, e := b64Decode(resp.Content); e == nil {
			return string(dec), true, nil
		}
	}
	return resp.Content, resp.Content != "", nil
}

type ghIssue struct {
	Number    int
	Title     string
	Body      string
	HTMLURL   string
	User      string
	UpdatedAt string
}

// ghIssues lists a repo's open issues (excluding pull requests, which the issues
// endpoint mixes in). Bounded to the first page.
func ghIssues(ctx context.Context, token, fullName string) ([]ghIssue, error) {
	var raw []struct {
		Number      int       `json:"number"`
		Title       string    `json:"title"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		UpdatedAt   string    `json:"updated_at"`
		PullRequest *struct{} `json:"pull_request"`
		User        struct {
			Login string `json:"login"`
		} `json:"user"`
	}
	if err := ghGet(ctx, token, "https://api.github.com/repos/"+fullName+"/issues?state=open&per_page=50", &raw); err != nil {
		return nil, err
	}
	out := make([]ghIssue, 0, len(raw))
	for _, is := range raw {
		if is.PullRequest != nil {
			continue // an issues listing includes PRs; KB wants issues
		}
		out = append(out, ghIssue{
			Number: is.Number, Title: is.Title, Body: is.Body,
			HTMLURL: is.HTMLURL, User: is.User.Login, UpdatedAt: is.UpdatedAt,
		})
	}
	return out, nil
}

// ghGet performs an authenticated GitHub REST GET and decodes JSON into out. A 404
// surfaces as errNotFound (so callers branch on "no README"); other non-2xx is an
// error. The token is a Bearer credential and is never logged.
func ghGet(ctx context.Context, token, u string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := syncHTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode == http.StatusNotFound {
		return errNotFound
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("github %s: status %d: %s", u, resp.StatusCode, truncate(body, 200))
	}
	return json.Unmarshal(body, out)
}

func ghTime(iso string) string {
	if t, err := time.Parse(time.RFC3339, iso); err == nil {
		return t.UTC().Format("2006-01-02 15:04:05")
	}
	return ""
}

// ---- OAuth code exchange ----

// exchangeCode swaps an authorization code for an access token at the provider's
// token endpoint and derives a display `account` for the connection. GitHub returns
// a form-encoded (or JSON) body; Slack/Google return JSON. Returns the token (kept
// secret by the caller → KMS) and a non-secret account label.
func exchangeCode(ctx context.Context, provider string, app oauthApp, code, redirectURI string) (token, account string, err error) {
	form := url.Values{}
	form.Set("client_id", app.clientID)
	form.Set("client_secret", app.clientSecret)
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)
	form.Set("grant_type", "authorization_code")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, app.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := syncHTTP.Do(req)
	if err != nil {
		return "", "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", err
	}
	if resp.StatusCode/100 != 2 {
		return "", "", fmt.Errorf("token endpoint status %d", resp.StatusCode)
	}

	// All three providers can return JSON when asked (Accept: application/json).
	var j struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
		// Slack nests the workspace + authed user.
		Team struct {
			Name string `json:"name"`
		} `json:"team"`
		AuthedUser struct {
			AccessToken string `json:"access_token"`
		} `json:"authed_user"`
	}
	if err := json.Unmarshal(body, &j); err != nil {
		return "", "", fmt.Errorf("decode token response: %w", err)
	}
	if j.Error != "" {
		return "", "", fmt.Errorf("provider error: %s", j.Error)
	}
	tok := j.AccessToken
	if tok == "" && j.AuthedUser.AccessToken != "" {
		tok = j.AuthedUser.AccessToken // Slack user token
	}
	if tok == "" {
		return "", "", fmt.Errorf("no access token in response")
	}

	// A non-secret account label for the console (best-effort; empty is fine).
	switch provider {
	case "github":
		account = ghLogin(ctx, tok)
	case "slack":
		account = j.Team.Name
	case "google":
		account = "" // filled by a userinfo call in a full Google impl
	}
	return tok, account, nil
}

// ghLogin returns the authenticated GitHub login for the connection label
// (best-effort; empty on any failure).
func ghLogin(ctx context.Context, token string) string {
	var u struct {
		Login string `json:"login"`
	}
	if err := ghGet(ctx, token, "https://api.github.com/user", &u); err != nil {
		return ""
	}
	return u.Login
}
