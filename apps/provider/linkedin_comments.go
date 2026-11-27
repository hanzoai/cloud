package provider

// linkedin_comments.go — LinkedIn Community Management API (v202611) comment
// reading and responding for connected LinkedIn member profiles and business
// company pages (urn:li:organization:<id>).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const linkedinSocialVersion = "202611"

// LinkedInComment is one comment (or nested reply) on a LinkedIn post or share.
type LinkedInComment struct {
	CommentURN    string `json:"comment_urn"`
	ObjectURN     string `json:"object_urn"`
	Actor         string `json:"actor"`
	Text          string `json:"text"`
	ParentComment string `json:"parent_comment,omitempty"`
	CreatedAt     int64  `json:"created_at"`
}

// ListLinkedInComments lists comments on a LinkedIn post URN (e.g.
// urn:li:share:... or urn:li:ugcPost:...) using the org's connected LinkedIn
// account token custodied in KMS.
func ListLinkedInComments(ctx context.Context, org, accountID, postURN string) ([]LinkedInComment, error) {
	postURN = strings.TrimSpace(postURN)
	if postURN == "" {
		return nil, fmt.Errorf("linkedin comments: post URN is required")
	}
	if mounted == nil {
		return nil, fmt.Errorf("linkedin comments: integrations plane is not mounted")
	}
	tok, _, err := tokenAndConn(mounted, ctx, org, "linkedin", accountID)
	if err != nil || len(tok) == 0 {
		return nil, fmt.Errorf("linkedin comments: org has no connected linkedin account")
	}
	return listLinkedInCommentsWithToken(ctx, string(tok), postURN)
}

func listLinkedInCommentsWithToken(ctx context.Context, token, postURN string) ([]LinkedInComment, error) {
	u := fmt.Sprintf("%s/rest/socialActions/%s/comments", strings.TrimRight(linkedinAPI, "/"), url.PathEscape(postURN))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("LinkedIn-Version", linkedinSocialVersion)
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	resp, err := postClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("linkedin comments rejected (%d): %s", resp.StatusCode, postSaid(body))
	}
	var parsed struct {
		Elements []struct {
			CommentURN    string `json:"commentUrn"`
			ID            string `json:"id"`
			Object        string `json:"object"`
			Actor         string `json:"actor"`
			ParentComment string `json:"parentComment"`
			Message       struct {
				Text string `json:"text"`
			} `json:"message"`
			Created struct {
				Time int64 `json:"time"`
			} `json:"created"`
		} `json:"elements"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("linkedin comments decode: %w", err)
	}
	out := make([]LinkedInComment, 0, len(parsed.Elements))
	for _, e := range parsed.Elements {
		urn := e.CommentURN
		if urn == "" {
			urn = e.ID
		}
		out = append(out, LinkedInComment{
			CommentURN:    urn,
			ObjectURN:     e.Object,
			Actor:         e.Actor,
			Text:          e.Message.Text,
			ParentComment: e.ParentComment,
			CreatedAt:     e.Created.Time,
		})
	}
	return out, nil
}

// ReplyLinkedInComment posts a comment or threaded reply on a LinkedIn post as
// the connected Company Page (when Kind == "page") or member profile. Passing
// a non-empty parentCommentURN creates a nested reply to that comment.
func ReplyLinkedInComment(ctx context.Context, org, accountID, postURN, parentCommentURN, text string) (string, error) {
	postURN = strings.TrimSpace(postURN)
	text = strings.TrimSpace(text)
	if postURN == "" || text == "" {
		return "", fmt.Errorf("linkedin reply: post URN and text are required")
	}
	if mounted == nil {
		return "", fmt.Errorf("linkedin reply: integrations plane is not mounted")
	}
	tok, conn, err := tokenAndConn(mounted, ctx, org, "linkedin", accountID)
	if err != nil || len(tok) == 0 {
		return "", fmt.Errorf("linkedin reply: org has no connected linkedin account")
	}
	actor := linkedinActorURN(conn.BotUserID, conn.ExternalID)
	if actor == "" {
		return "", fmt.Errorf("linkedin reply: connected account has no member or organization id")
	}
	return replyLinkedInCommentWithToken(ctx, string(tok), actor, postURN, strings.TrimSpace(parentCommentURN), text)
}

func linkedinActorURN(kind, externalID string) string {
	id := strings.TrimSpace(externalID)
	if id == "" {
		return ""
	}
	if strings.HasPrefix(id, "urn:li:") {
		return id
	}
	if strings.EqualFold(strings.TrimSpace(kind), "page") {
		return "urn:li:organization:" + id
	}
	return "urn:li:person:" + id
}

func replyLinkedInCommentWithToken(ctx context.Context, token, actorURN, postURN, parentCommentURN, text string) (string, error) {
	payload := map[string]any{
		"actor":   actorURN,
		"object":  postURN,
		"message": map[string]string{"text": text},
	}
	if parentCommentURN != "" {
		payload["parentComment"] = parentCommentURN
	}
	raw, _ := json.Marshal(payload)
	u := fmt.Sprintf("%s/rest/socialActions/%s/comments", strings.TrimRight(linkedinAPI, "/"), url.PathEscape(postURN))
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("LinkedIn-Version", linkedinSocialVersion)
	req.Header.Set("X-Restli-Protocol-Version", "2.0.0")
	resp, err := postClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("linkedin reply rejected (%d): %s", resp.StatusCode, postSaid(body))
	}
	var out struct {
		CommentURN string `json:"commentUrn"`
		ID         string `json:"id"`
	}
	_ = json.Unmarshal(body, &out)
	if out.CommentURN != "" {
		return out.CommentURN, nil
	}
	if out.ID != "" {
		return out.ID, nil
	}
	if hdr := resp.Header.Get("X-Restli-Id"); hdr != "" {
		return hdr, nil
	}
	return "", nil
}
