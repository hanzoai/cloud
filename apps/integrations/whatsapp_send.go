package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/hanzoai/cloud/internal/environ"
	"io"
	"net/http"
	"strings"
)

// whatsapp_send.go is the WhatsApp Cloud API egress. Token custody stays here,
// with slack's and telegram's, so channels never holds a credential.

// The same origin the connect-time verify uses, from the same env override, so a
// test or a staging tenant cannot end up verifying against one host and sending
// to another.
func whatsappOrigin() string {
	if v := environ.Or("WHATSAPP_API_BASE", ""); v != "" {
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
// `to` is that person's number in E.164 without the leading +, which is the
// only address the Cloud API accepts and also the room id channels carries.
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
func SendWhatsApp(ctx context.Context, org, to, replyTo, text string) (string, error) {
	if strings.TrimSpace(to) == "" {
		return "", fmt.Errorf("whatsapp: a message needs someone to send it to")
	}
	tok, err := TokenFor(ctx, org, "whatsapp", apiKeySecret)
	if err != nil {
		return "", err
	}
	// The ORG owns this connection (whatsapp.go registers it AdminOnly), so the
	// owner is "" — a member-owned row would be a different number.
	conn, ok := ConnectionFor(org, "whatsapp", "")
	if !ok || conn.ExternalID == "" {
		return "", fmt.Errorf("whatsapp: this org has no connected number")
	}
	number := conn.ExternalID

	body := map[string]any{
		"messaging_product": "whatsapp",
		"recipient_type":    "individual",
		"to":                to,
		"type":              "text",
		// preview_url off: a link in an agent's reply should render as the text
		// it is, not as an unfurled card the sender never chose to attach.
		"text": map[string]any{"body": text, "preview_url": false},
	}
	if replyTo != "" {
		body["context"] = map[string]string{"message_id": replyTo}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	endpoint := whatsappOrigin() + "/v21.0/" + number + "/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(payload)))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+string(tok))
	req.Header.Set("Content-Type", "application/json")

	resp, err := channelHTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// Meta's own words. Its errors are the specific ones an operator has to
		// act on — a closed 24-hour window, an unverified number, a revoked
		// token — and each needs a different repair, so flattening them to
		// "send failed" would cost the reader the whole diagnosis.
		var e struct {
			Error struct {
				Message string `json:"message"`
				Code    int    `json:"code"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
			return "", fmt.Errorf("whatsapp: %s (code %d)", e.Error.Message, e.Error.Code)
		}
		return "", fmt.Errorf("whatsapp: send returned %d", resp.StatusCode)
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

// whatsappVerifyToken is the string Meta echoes back when a webhook is first
// subscribed. It is a shared secret, not a credential for anything else.
func whatsappVerifyToken() string { return environ.Or("WHATSAPP_VERIFY_TOKEN", "") }
