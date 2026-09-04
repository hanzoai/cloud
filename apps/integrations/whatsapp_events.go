package integrations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/hanzoai/cloud/internal/environ"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// whatsapp_events.go is the inbound leg: Meta's Cloud API webhook, verified,
// deduped and handed to the shared ingress the other channels use.
//
// Without it the transport can only answer routes that never come into being —
// a channel_route row is written from an ALLOWED inbound, so egress with no
// inbound is a capability an org can never reach.

const (
	whatsappSignatureHeader = "X-Hub-Signature-256"
	whatsappSignaturePrefix = "sha256="
)

// The app secret Meta signs each delivery with, and the string it echoes when a
// webhook is first subscribed. Both are shared secrets for THIS endpoint, not
// credentials for anything a tenant owns.
func whatsappAppSecret() string { return environ.Or("WHATSAPP_APP_SECRET", "") }

// whatsappVerify answers Meta's subscription challenge.
//
// Meta sends a GET with a mode, the verify token we gave it, and a challenge to
// echo. Answering the challenge without checking the token would let anyone
// point their own app at this endpoint and have it confirm the subscription, so
// the token is compared before the echo — and in constant time, because it is a
// secret and a timing oracle on it is a way to learn it.
func whatsappVerify(s *cloud.Service[state], c *zip.Ctx) error {
	want := whatsappVerifyToken()
	if want == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "whatsapp webhook not configured")
	}
	if c.Query("hub.mode") != "subscribe" || !hmac.Equal([]byte(c.Query("hub.verify_token")), []byte(want)) {
		return zip.ErrUnauthorized("bad whatsapp verify token")
	}
	return c.String(http.StatusOK, c.Query("hub.challenge"))
}

// whatsappSigned reports whether a body carries Meta's HMAC for our app secret.
//
// This is the whole authenticity of an inbound. A message that reaches the
// ingress creates the reply route that authorises egress, so an unsigned
// delivery accepted here would let anyone hand this org a conversation to answer
// — and the org would answer it under its own number.
func whatsappSigned(header string, body []byte) bool {
	secret := whatsappAppSecret()
	if secret == "" || !strings.HasPrefix(header, whatsappSignaturePrefix) {
		return false
	}
	sum, err := hex.DecodeString(strings.TrimPrefix(header, whatsappSignaturePrefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sum, mac.Sum(nil))
}

// whatsappInbound is the one shape this endpoint reads out of Meta's envelope: a
// text message, the number it came from, and the business number it arrived on.
type whatsappInbound struct {
	// Account is the phone number id that RECEIVED this, which is the connection's
	// ExternalID and therefore the isolation root.
	Account string
	// From is the sender's number, which is also the only address a reply can go
	// to — WhatsApp has no room apart from the person.
	From      string
	Text      string
	MessageID string
}

// parseWhatsAppEvent lifts the first text message out of a Cloud API delivery.
//
// Meta batches: entry[] × changes[] × messages[]. It also delivers status
// callbacks (sent/delivered/read) through this same endpoint with no messages at
// all, which are not turns and are dropped rather than reported as a parse
// failure.
func parseWhatsAppEvent(raw []byte) (whatsappInbound, bool) {
	var env struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					Metadata struct {
						PhoneNumberID string `json:"phone_number_id"`
					} `json:"metadata"`
					Messages []struct {
						From string `json:"from"`
						ID   string `json:"id"`
						Type string `json:"type"`
						Text struct {
							Body string `json:"body"`
						} `json:"text"`
					} `json:"messages"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return whatsappInbound{}, false
	}
	for _, e := range env.Entry {
		for _, ch := range e.Changes {
			for _, m := range ch.Value.Messages {
				// Text only for now. An image or a location is a real message this
				// cannot answer, and pretending it was empty text would put an agent
				// in a conversation it cannot see.
				if m.Type != "text" || strings.TrimSpace(m.Text.Body) == "" {
					continue
				}
				return whatsappInbound{
					Account:   ch.Value.Metadata.PhoneNumberID,
					From:      m.From,
					Text:      m.Text.Body,
					MessageID: m.ID,
				}, true
			}
		}
	}
	return whatsappInbound{}, false
}

// whatsappWebhook takes one delivery from Meta.
//
// It answers 200 for anything it cannot act on. Meta retries a non-2xx with
// backoff and eventually disables the subscription, so a status callback or an
// unbound number must not read as a delivery failure. It refuses an unconfigured
// endpoint and a bad signature, which are ours to fix rather than Meta's to
// retry, and a message it could not take, which is exactly what a redelivery
// repairs.
func whatsappWebhook(s *cloud.Service[state], c *zip.Ctx) error {
	channelReady()
	if whatsappAppSecret() == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "whatsapp webhook not configured")
	}
	body := slackReadBody(c)
	if !whatsappSigned(c.Header(whatsappSignatureHeader), body) {
		return zip.ErrUnauthorized("bad whatsapp signature")
	}
	m, ok := parseWhatsAppEvent(body)
	if !ok {
		return c.NoContent(http.StatusOK)
	}

	org, ok := OrgForExternalID("whatsapp", m.Account)
	if !ok {
		s.Log.Warn("whatsapp: message to an unbound number", "account", m.Account)
		return c.NoContent(http.StatusOK)
	}
	// Meta redelivers on any non-2xx, so the same message id arrives more than
	// once as a matter of course, and the dedupe inside emitIngress is what makes
	// a redelivery free. An event it could not take answers a retriable non-2xx
	// for the same reason: nothing was recorded, so the redelivery is the recovery.
	if !emitIngress(c.Context(), s, org, Inbound{
		Provider:   "whatsapp",
		ExternalID: m.Account,
		User:       m.From,
		// The sender IS the room: a reply goes to the person, and there is nowhere
		// else for it to go.
		Channel:   m.From,
		Text:      m.Text,
		DedupeKey: m.MessageID,
	}, "") {
		return zip.Errorf(http.StatusTooManyRequests, "whatsapp message not taken; please redeliver")
	}
	return c.NoContent(http.StatusOK)
}
