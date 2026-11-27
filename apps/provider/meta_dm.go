package provider

// meta_dm.go — Facebook Page Messenger & Instagram Direct messaging egress and
// inbound webhook parser on the Meta Graph API (v21.0), alongside WhatsApp Cloud
// API in whatsapp_send.go and whatsapp_events.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// MetaInboundDM is one normalized inbound Direct Message from Facebook Page
// Messenger or Instagram Direct delivered via Meta Webhooks.
type MetaInboundDM struct {
	Surface     string `json:"surface"`      // "facebook" | "instagram"
	RecipientID string `json:"recipient_id"` // Page ID or IG Business Account ID
	SenderID    string `json:"sender_id"`    // PSID or IGSID
	MessageID   string `json:"message_id"`
	Text        string `json:"text"`
	Timestamp   int64  `json:"timestamp"`
}

// SendFacebookDM sends a direct message from an org's connected Facebook Page
// to a recipient PSID via POST /{page-id}/messages.
func SendFacebookDM(ctx context.Context, org, pageID, recipientPSID, text string) (string, error) {
	return sendMetaGraphDM(ctx, org, "facebook", pageID, recipientPSID, text)
}

// SendInstagramDM sends a direct message from an org's connected Instagram
// Professional account to a recipient IGSID via POST /{ig-id}/messages.
func SendInstagramDM(ctx context.Context, org, igID, recipientIGSID, text string) (string, error) {
	return sendMetaGraphDM(ctx, org, "instagram", igID, recipientIGSID, text)
}

func sendMetaGraphDM(ctx context.Context, org, prov, accountID, recipientID, text string) (string, error) {
	if strings.TrimSpace(recipientID) == "" || strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s dm: recipient and text are required", prov)
	}
	if mounted == nil {
		return "", fmt.Errorf("%s dm: integrations plane is not mounted", prov)
	}
	tok, conn, err := tokenAndConn(mounted, ctx, org, prov, accountID)
	if err != nil || len(tok) == 0 {
		return "", fmt.Errorf("%s dm: org has no connected %s account", prov, prov)
	}
	payload := map[string]any{
		"recipient":      map[string]string{"id": recipientID},
		"messaging_type": "RESPONSE",
		"message":        map[string]string{"text": text},
	}
	raw, _ := json.Marshal(payload)
	u := fmt.Sprintf("%s/%s/messages", strings.TrimRight(graphAPI, "/"), conn.ExternalID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+string(tok))
	req.Header.Set("Content-Type", "application/json")
	resp, err := postClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("%s dm rejected (%d)", prov, resp.StatusCode)
	}
	var out struct {
		MessageID string `json:"message_id"`
	}
	_ = json.Unmarshal(b, &out)
	return out.MessageID, nil
}

// ParseMetaMessagingWebhook extracts inbound Facebook Page ("page") and
// Instagram ("instagram") Direct Messages from a Meta Graph webhook payload.
func ParseMetaMessagingWebhook(body []byte) ([]MetaInboundDM, error) {
	var envelope struct {
		Object string `json:"object"`
		Entry  []struct {
			ID        string `json:"id"`
			Messaging []struct {
				Sender    struct{ ID string `json:"id"` } `json:"sender"`
				Recipient struct{ ID string `json:"id"` } `json:"recipient"`
				Timestamp int64                           `json:"timestamp"`
				Message   *struct {
					MID    string `json:"mid"`
					Text   string `json:"text"`
					IsEcho bool   `json:"is_echo"`
				} `json:"message"`
			} `json:"messaging"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	surface := "facebook"
	if envelope.Object == "instagram" {
		surface = "instagram"
	}
	var out []MetaInboundDM
	for _, entry := range envelope.Entry {
		for _, m := range entry.Messaging {
			if m.Message == nil || m.Message.IsEcho || strings.TrimSpace(m.Message.Text) == "" {
				continue
			}
			out = append(out, MetaInboundDM{
				Surface:     surface,
				RecipientID: entry.ID,
				SenderID:    m.Sender.ID,
				MessageID:   m.Message.MID,
				Text:        m.Message.Text,
				Timestamp:   m.Timestamp,
			})
		}
	}
	return out, nil
}
