package integrations

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// linear.go is what the Linear connector does once a person has connected it.
// The connector itself is the user-scoped API-key provider in saas.go: a person
// enrols their own Linear key under /v1/integrations/connectors/linear, and every
// call here — the claim, the comment, the backfill — spends that person's key, so
// what it writes in Linear carries their name.
//
// The one org-level fact is the webhook. Linear delivers to one address per Linear
// organization, signed with a secret the org chose, so `claim` proves which Linear
// organization the caller belongs to (their own key answers `organization { id }`),
// seals the chosen secret under the org, and records organization id → org. The
// webhook then resolves the tenant from the signed `organizationId` and verifies
// with that org's own secret — the GitHub installation shape, per organization.

const (
	// The todo team every mirrored Linear issue files under; the Linear team key
	// is the per-issue discriminator within it (IssueUpsert.Repo).
	linearTodoProjectKey  = "LINEAR"
	linearTodoProjectName = "Linear"
	// linearWebhookSecret is the KMS name of the org's webhook secret, under
	// kmsPath(org, "linear").
	linearWebhookSecret = "webhook_secret"
)

// linearAPI is a var so a test can point it at a mock.
var (
	linearAPI  = "https://api.linear.app/graphql"
	linearHTTP = &http.Client{Timeout: 30 * time.Second}
)

// linearQuery runs one GraphQL document and decodes `data` into out. A GraphQL
// error is an error here: Linear answers 200 with an `errors` array, and a caller
// that read only the status would take an empty `data` for an empty result. The
// key rides in Authorization bare, the way Linear reads a personal API key and the
// way saas.go verified it at connect.
func linearQuery(ctx context.Context, token, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearAPI, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := linearHTTP.Do(req)
	if err != nil {
		return fmt.Errorf("linear call: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("linear read: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("linear http %d: %s", resp.StatusCode, truncateBody(b))
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(b, &env); err != nil {
		return fmt.Errorf("linear decode: %w", err)
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("linear: %s", env.Errors[0].Message)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// linearMention is the text a comment carries to address the agent.
const linearMention = "@hanzo"

// linearKey is a person's own Linear key: their connector row (the one labelled
// "" when they hold several) opened through the same custody path tokenConn uses.
func linearKey(ctx context.Context, s *cloud.Service[state], org, user string) (string, error) {
	p, ok := userProvider(s, "linear")
	if !ok {
		return "", zip.ErrNotFound("unknown provider")
	}
	conns, err := s.State.store.List(ctx, org, user)
	if err != nil {
		return "", zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	var conn Connection
	found := false
	for _, c := range conns {
		if c.Provider != "linear" {
			continue
		}
		if !found || c.Label == "" {
			conn, found = c, true
		}
	}
	if !found {
		return "", zip.Errorf(http.StatusFailedDependency, "connect Linear first: hanzo connector add linear")
	}
	if !kmsReady(s) {
		return "", kmsUnavailable()
	}
	_, tok, err := fresh(ctx, s, p, conn, false)
	if err != nil {
		if he, ok := httpErr(err); ok {
			return "", he
		}
		return "", zip.Errorf(http.StatusBadGateway, "token unavailable")
	}
	return string(tok), nil
}

// ── claim: bind the caller's Linear organization and its webhook secret ──────

// linearClaimIn carries the secret the org set on its Linear webhook.
type linearClaimIn struct {
	// Secret is the signing secret configured on the webhook in Linear. Claiming
	// again with a new value rotates it.
	Secret string `json:"secret"`
}

// linearClaimOut names the organization that was bound and where Linear should deliver.
type linearClaimOut struct {
	// Organization is the Linear organization id now bound to this org.
	Organization string `json:"organization"`
	// Name is the organization's URL key.
	Name string `json:"name"`
	// Path is the address to configure in Linear, on this deployment's origin.
	Path string `json:"path"`
}

// linearClaim binds the caller's Linear organization to the org and seals the
// webhook secret. The organization is READ from the caller's own key, never taken
// from the body: a person can only bind an organization they are a member of. An
// organization another org already holds is refused.
//
// Example: {"secret":"whsec_…"}
// Response: {"organization":"6f0b…","name":"acme","path":"/v1/integrations/linear/webhook"}
func (o ops) linearClaim(ctx context.Context, in *linearClaimIn) (*linearClaimOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	secret := strings.TrimSpace(in.Secret)
	if len(secret) < 16 {
		return nil, zip.ErrBadRequest("secret must be at least 16 characters")
	}
	tok, err := linearKey(ctx, o.s, org, user)
	if err != nil {
		return nil, err
	}
	var d struct {
		Organization struct {
			ID     string `json:"id"`
			URLKey string `json:"urlKey"`
		} `json:"organization"`
	}
	if err := linearQuery(ctx, tok, `{ organization { id urlKey } }`, nil, &d); err != nil || d.Organization.ID == "" {
		return nil, zip.Errorf(http.StatusBadGateway, "linear organization: %v", err)
	}
	if err := o.s.State.store.Upsert(ctx, Connection{Org: org, Provider: "linear", ExternalID: d.Organization.ID, AccountLabel: d.Organization.URLKey}); err != nil {
		if errors.Is(err, errBound) {
			return nil, zip.Errorf(http.StatusConflict, "that Linear organization is bound to another org")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "persist failed")
	}
	if err := sealTokens(o.s, kmsPath(org, "linear"), map[string]string{linearWebhookSecret: secret}); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "seal secret: %v", err)
	}
	// The claimer is who a Linear turn runs as until the commenter has linked
	// their own account: the person who bound the organization, and whose key
	// posts the reply. The same rule Slack applies to its installer before pairing.
	if err := putUserLink(o.s, org, "linear", defaultSubjectKey, userLink{Subject: user}); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "bind claimer: %v", err)
	}
	return &linearClaimOut{Organization: d.Organization.ID, Name: d.Organization.URLKey, Path: "/v1/integrations/linear/webhook"}, nil
}

// linearIssue is the issue as the webhook delivers it and as the backfill reads
// it. Labels is flattened at decode time because the two wires disagree: a
// webhook carries an array of label objects, the GraphQL API a connection with
// `nodes`.
type linearIssue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"`
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	State       struct {
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"state"`
	Team struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"team"`
	Assignee *struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"assignee"`
	Labels linearLabels `json:"labels"`
}

type linearLabels []string

func (l *linearLabels) UnmarshalJSON(b []byte) error {
	var arr []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(b, &arr); err == nil {
		*l = labelNames(arr)
		return nil
	}
	var conn struct {
		Nodes []struct {
			Name string `json:"name"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(b, &conn); err != nil {
		return err
	}
	*l = labelNames(conn.Nodes)
	return nil
}

func labelNames(xs []struct {
	Name string `json:"name"`
}) []string {
	out := make([]string, 0, len(xs))
	for _, x := range xs {
		if n := strings.TrimSpace(x.Name); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// linearState folds Linear's workflow state types onto the todo's two. Linear has
// six (triage, backlog, unstarted, started, completed, canceled); the todo asks
// only whether the work is done.
func linearState(t string) string {
	switch t {
	case "completed", "canceled":
		return "closed"
	}
	return "open"
}

// mirrorLinearIssue upserts one issue into org's todo. ExtRef "linear:ENG-123"
// anchors idempotency across webhook redeliveries and backfill re-runs.
func mirrorLinearIssue(ctx context.Context, org string, is linearIssue) (bool, error) {
	var assignee string
	if is.Assignee != nil {
		assignee = firstNonEmpty(is.Assignee.Name, is.Assignee.Email)
	}
	res, err := cloud.UpsertIssue(ctx, cloud.IssueUpsert{
		Org:         org,
		ProjectKey:  linearTodoProjectKey,
		ProjectName: linearTodoProjectName,
		Repo:        is.Team.Key,
		ExtRef:      "linear:" + is.Identifier,
		Kind:        "issue",
		Source:      "linear",
		Title:       is.Title,
		Description: is.Description,
		State:       linearState(is.State.Type),
		Assignee:    assignee,
		Labels:      is.Labels,
	})
	if err != nil {
		return false, err
	}
	return res.Created, nil
}

// ── comment ──────────────────────────────────────────────────────────────────

// linearCommentIn names the issue and the comment.
type linearCommentIn struct {
	// Issue is the issue's identifier (ENG-123) or its id.
	Issue string `json:"issue"`
	// Body is the comment, Markdown.
	Body string `json:"body"`
}

// linearCommentOut is the created comment.
type linearCommentOut struct {
	// ID is the comment's id in Linear.
	ID string `json:"id"`
	// URL is the comment's address in Linear.
	URL string `json:"url"`
}

// linearComment posts a comment on a Linear issue with the caller's own key, so it
// carries their name. This is the op an agent is offered when it should answer in
// Linear rather than in chat.
//
// Example: {"issue":"ENG-123","body":"Reproduced on main; fix in #482."}
// Response: {"id":"c0f1…","url":"https://linear.app/acme/issue/ENG-123#comment-c0f1"}
func (o ops) linearComment(ctx context.Context, in *linearCommentIn) (*linearCommentOut, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ref := strings.TrimSpace(in.Issue)
	body := strings.TrimSpace(in.Body)
	if ref == "" || body == "" {
		return nil, zip.ErrBadRequest("issue and body are required")
	}
	tok, err := linearKey(ctx, o.s, org, user)
	if err != nil {
		return nil, err
	}
	return linearCommentWith(ctx, tok, &linearCommentIn{Issue: ref, Body: body})
}

// linearCommentWith is the comment op below the token: the identifier lookup
// and the mutation, with nothing of the principal in it.
func linearCommentWith(ctx context.Context, tok string, in *linearCommentIn) (*linearCommentOut, error) {
	ref, body := in.Issue, in.Body
	// Linear's `issue` query resolves an identifier as well as an id, so one
	// lookup turns whatever the caller named into the id the mutation wants.
	var found struct {
		Issue struct {
			ID string `json:"id"`
		} `json:"issue"`
	}
	if err := linearQuery(ctx, tok, `query($id: String!) { issue(id: $id) { id } }`, map[string]any{"id": ref}, &found); err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "linear issue %s: %v", ref, err)
	}
	var out struct {
		CommentCreate struct {
			Success bool `json:"success"`
			Comment struct {
				ID  string `json:"id"`
				URL string `json:"url"`
			} `json:"comment"`
		} `json:"commentCreate"`
	}
	err := linearQuery(ctx, tok,
		`mutation($issueId: String!, $body: String!) { commentCreate(input: {issueId: $issueId, body: $body}) { success comment { id url } } }`,
		map[string]any{"issueId": found.Issue.ID, "body": body}, &out)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "linear comment: %v", err)
	}
	if !out.CommentCreate.Success {
		return nil, zip.Errorf(http.StatusBadGateway, "linear comment: not created")
	}
	return &linearCommentOut{ID: out.CommentCreate.Comment.ID, URL: out.CommentCreate.Comment.URL}, nil
}

// ── backfill ─────────────────────────────────────────────────────────────────

// linearBackfillIn selects which issues to mirror.
type linearBackfillIn struct {
	// State is the set of issues to walk: "open" (the default), "closed" or "all".
	State string `json:"state"`
}

// linearBackfillResult is the count the operator asked for.
type linearBackfillResult struct {
	// Issues is how many Linear issues were seen.
	Issues int `json:"issues"`
	// Created is how many native issues this pass created.
	Created int `json:"created"`
	// Updated is how many existing native issues this pass refreshed.
	Updated int `json:"updated"`
	// Failed is how many issues errored; the pass continues past each.
	Failed int `json:"failed"`
	// Truncated is set when the time budget or the issue cap stopped the pass early.
	// Re-run to continue — the mirror is idempotent by ExtRef, so nothing duplicates.
	Truncated bool `json:"truncated,omitempty"`
}

// linearStateTypes is the GraphQL filter for each requested state.
func linearStateTypes(state string) ([]string, error) {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "open":
		return []string{"triage", "backlog", "unstarted", "started"}, nil
	case "closed":
		return []string{"completed", "canceled"}, nil
	case "all":
		return nil, nil
	}
	return nil, zip.ErrBadRequest("state must be open|closed|all")
}

const linearIssuesQuery = `query($after: String, $filter: IssueFilter) {
  issues(first: 100, after: $after, filter: $filter, orderBy: updatedAt) {
    nodes { id identifier title description url state { name type } team { key name } assignee { name email } labels { nodes { name } } }
    pageInfo { hasNextPage endCursor }
  }
}`

// linearIssuesBackfill seeds the native todo with the EXISTING Linear issues the
// caller's key can see (default state=open); the webhook keeps them live
// thereafter. Synchronous and bounded, idempotent by ExtRef.
//
// Example: {"state":"all"}
// Response: {"issues":430,"created":410,"updated":20,"failed":0}
func (o ops) linearIssuesBackfill(ctx context.Context, in *linearBackfillIn) (*linearBackfillResult, error) {
	org, user, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	types, err := linearStateTypes(in.State)
	if err != nil {
		return nil, err
	}
	tok, err := linearKey(ctx, o.s, org, user)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, backfillBudget)
	defer cancel()

	vars := map[string]any{}
	if types != nil {
		vars["filter"] = map[string]any{"state": map[string]any{"type": map[string]any{"in": types}}}
	}
	var out linearBackfillResult
	for {
		var page struct {
			Issues struct {
				Nodes    []linearIssue `json:"nodes"`
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
			} `json:"issues"`
		}
		if err := linearQuery(ctx, tok, linearIssuesQuery, vars, &page); err != nil {
			return nil, zip.Errorf(http.StatusBadGateway, "linear issues: %v", err)
		}
		for _, is := range page.Issues.Nodes {
			if ctx.Err() != nil || out.Issues >= backfillMaxIssues {
				out.Truncated = true
				break
			}
			out.Issues++
			created, uerr := mirrorLinearIssue(ctx, org, is)
			if uerr != nil {
				out.Failed++
				o.s.Log.Warn("linear backfill: mirror", "org", org, "issue", is.Identifier, "err", uerr)
				continue
			}
			if created {
				out.Created++
			} else {
				out.Updated++
			}
		}
		if out.Truncated || !page.Issues.PageInfo.HasNextPage {
			break
		}
		vars["after"] = page.Issues.PageInfo.EndCursor
	}
	o.s.Log.Info("linear issues backfill", "org", org, "issues", out.Issues,
		"created", out.Created, "updated", out.Updated, "failed", out.Failed, "truncated", out.Truncated)
	return &out, nil
}

// linearIssueComment posts one comment on an issue as the organization's default
// subject — the claimer — whose own key is the one that posts.
func linearIssueComment(ctx context.Context, s *cloud.Service[state], org, issue, text string) (string, error) {
	if s == nil {
		return "", fmt.Errorf("integrations: not mounted")
	}
	link, ok, err := getUserLink(s, org, "linear", defaultSubjectKey)
	if err != nil {
		return "", err
	}
	if !ok || link.Subject == "" {
		return "", fmt.Errorf("integrations: linear is not claimed for org %s", org)
	}
	tok, err := linearKey(ctx, s, org, link.Subject)
	if err != nil {
		return "", err
	}
	out, err := linearCommentWith(ctx, tok, &linearCommentIn{Issue: issue, Body: text})
	if err != nil {
		return "", err
	}
	return out.ID, nil
}
