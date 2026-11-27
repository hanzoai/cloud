package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/hanzoai/cloud/sdk/env"
)

// whatsapp_send.go is the WhatsApp Cloud API egress. Token custody stays here,
// with slack's and telegram's, so channels never holds a credential.

// The same origin the connect-time verify uses, from the same env override, so a
// test or a staging tenant cannot end up verifying against one host and sending
// to another.
func whatsappOrigin() string {
	if v := env.Or("WHATSAPP_API_BASE", ""); v != "" {
		return v
	}
	return "https://graph.facebook.com"
}

// The credential and the number both come from the connection whatsapp.go
// already establishes: the System User token sealed under apiKeySecret, and the
// phone number id recorded as the connection's ExternalID when /connect verified
// it live against the Graph API. Reading them from anywhere else would be a
// second source for one fact.

// SendWhatsApp sends one text message to a person, as the org's own business
// number.
//
// `to` is that person's number in E.164 without the leading +, or a group id
// when group is set. A group is addressed with recipient_type "group" and the
// group id as `to`. The two ids are both opaque, so the flag is the only
// thing that tells them apart.
//
// replyTo, when present, is the inbound message id this answers. Meta calls it
// a context and renders it as a quoted reply; it is optional, and an unknown or
// expired id is refused by the API rather than silently dropped, so it is only
// set when there is one.
//
// WHAT THIS CANNOT DO, deliberately: free-form text reaches a person only
// inside 24 hours of their last inbound message. Outside it Meta answers 131047
// and accepts a pre-approved template instead. That window is not visible from
// here — it belongs to the conversation, not the process — so the API's refusal
// is returned as-is rather than guessed at, and the caller sees why.
func SendWhatsApp(ctx context.Context, org, to, replyTo, text string, group bool) (string, error) {
	if strings.TrimSpace(to) == "" {
		return "", fmt.Errorf("whatsapp: a message needs someone to send it to")
	}
	tok, number, err := whatsappCreds(ctx, org)
	if err != nil {
		return "", err
	}
	return postWhatsApp(ctx, tok, number, to, replyTo, text, group)
}

// whatsappCreds is the org's system-user token and the phone number id it was
// verified against. The org owns the connection (whatsapp.go registers it
// AdminOnly), so the owner is "" — a member-owned row would be a different number.
func whatsappCreds(ctx context.Context, org string) (token, number string, err error) {
	tok, err := TokenFor(ctx, org, "whatsapp", apiKeySecret)
	if err != nil {
		return "", "", err
	}
	conn, ok := ConnectionFor(org, "whatsapp", "")
	if !ok || conn.ExternalID == "" {
		return "", "", fmt.Errorf("whatsapp: this org has no connected number")
	}
	return string(tok), conn.ExternalID, nil
}

// postWhatsApp sends one text message as the given number. group selects
// recipient_type.
func postWhatsApp(ctx context.Context, token, phoneNumberID, to, replyTo, text string, group bool) (string, error) {
	kind := "individual"
	if group {
		kind = "group"
	}
	body := map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    kind,
		"to":                to,
		"type":              "text",
		// preview_url off: a link in an agent's reply should render as the text
		// it is, not as an unfurled card the sender never chose to attach.
		"text": map[string]any{"body": text, "preview_url": false},
	}
	if replyTo != "" {
		body["context"] = map[string]string{"message_id": replyTo}
	}
	endpoint := whatsappOrigin() + "/v21.0/" + url.PathEscape(phoneNumberID) + "/messages"
	raw, err := whatsappPost(ctx, token, endpoint, body)
	if err != nil {
		return "", err
	}
	var out struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || len(out.Messages) == 0 {
		// Accepted, but nothing to correlate a delivery receipt against later.
		return "", nil
	}
	return out.Messages[0].ID, nil
}

// CreateWhatsAppGroup creates a group on the org's business number.
//
// Meta answers the group id. The invite link is a second read
// (WhatsAppInviteLink): creating a group and handing out its join link are
// different acts, and a link that failed to be read does not uncreate the group.
func CreateWhatsAppGroup(ctx context.Context, org, subject, description string) (string, error) {
	tok, number, err := whatsappCreds(ctx, org)
	if err != nil {
		return "", err
	}
	return createWhatsAppGroup(ctx, tok, number, subject, description)
}

func createWhatsAppGroup(ctx context.Context, token, phoneNumberID, subject, description string) (string, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return "", fmt.Errorf("whatsapp: a group needs a subject")
	}
	if len(subject) > 128 {
		return "", fmt.Errorf("whatsapp: a group subject is at most 128 characters")
	}
	description = strings.TrimSpace(description)
	if len(description) > 2048 {
		return "", fmt.Errorf("whatsapp: a group description is at most 2048 characters")
	}
	body := map[string]any{
		"messaging_product": "whatsapp",
		"subject":           subject,
	}
	if description != "" {
		body["description"] = description
	}
	endpoint := whatsappOrigin() + "/v21.0/" + url.PathEscape(phoneNumberID) + "/groups"
	raw, err := whatsappPost(ctx, token, endpoint, body)
	if err != nil {
		return "", err
	}
	var out struct {
		ID      string `json:"id"`
		GroupID string `json:"group_id"`
	}
	if json.Unmarshal(raw, &out) != nil || (out.ID == "" && out.GroupID == "") {
		return "", fmt.Errorf("whatsapp: group created with no id")
	}
	if out.ID != "" {
		return out.ID, nil
	}
	return out.GroupID, nil
}

// WhatsAppInviteLink reads the join link for a group this number owns.
func WhatsAppInviteLink(ctx context.Context, org, groupID string) (string, error) {
	tok, _, err := whatsappCreds(ctx, org)
	if err != nil {
		return "", err
	}
	return whatsappInviteLink(ctx, tok, groupID)
}

func whatsappInviteLink(ctx context.Context, token, groupID string) (string, error) {
	groupID = strings.TrimSpace(groupID)
	if groupID == "" || strings.ContainsAny(groupID, " ?#") {
		return "", fmt.Errorf("whatsapp: a group id is required")
	}
	endpoint := whatsappOrigin() + "/v21.0/" + url.PathEscape(groupID) + "/invite_link"
	var out struct {
		InviteLink string `json:"invite_link"`
		Link       string `json:"link"`
	}
	if err := whatsappGet(ctx, token, endpoint, &out); err != nil {
		return "", err
	}
	if out.InviteLink != "" {
		return out.InviteLink, nil
	}
	if out.Link != "" {
		return out.Link, nil
	}
	return "", fmt.Errorf("whatsapp: group has no invite link")
}

func whatsappPost(ctx context.Context, token, endpoint string, body any) ([]byte, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return nil, fmt.Errorf("whatsapp: request")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := whatsappHTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("whatsapp: request failed")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, whatsappStatus(resp.StatusCode, raw)
	}
	return raw, nil
}

// whatsappStatus is Meta's own words when it gave them, and only the status
// when it did not. The body is never logged and the bearer is never in it.
func whatsappStatus(status int, raw []byte) error {
	var e struct {
		Error struct {
			Message string `json:"message"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		return fmt.Errorf("whatsapp: %s (code %d)", e.Error.Message, e.Error.Code)
	}
	return fmt.Errorf("whatsapp: request returned %d", status)
}

// whatsappVerifyToken is the string Meta echoes back when a webhook is first
// subscribed. It is a shared secret, not a credential for anything else.
func whatsappVerifyToken() string { return env.Or("WHATSAPP_VERIFY_TOKEN", "") }
