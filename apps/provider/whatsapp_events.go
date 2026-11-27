package provider

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/sdk/env"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
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
func whatsappAppSecret() string { return env.Or("WHATSAPP_APP_SECRET", "") }

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

// whatsappInbound is the one shape this endpoint reads out of Meta's envelope.
type whatsappInbound struct {
	// Account is the phone number id that RECEIVED this, which is the connection's
	// ExternalID and therefore the isolation root.
	Account string
	// From is the sender's number. In a direct message it is also the reply
	// address; in a group the reply address is GroupID and From stays the person.
	From      string
	Text      string
	MessageID string
	// GroupID is set when the message arrived in a group. Meta puts it on the
	// message or on the message's context.
	GroupID string
	// Media is set for an image, document, audio, video or sticker. Text is the
	// caption when the message carried one.
	Media *whatsappMediaRef
}

// whatsappMediaRef is the media object on one message, before the bytes are
// fetched. Kind is Meta's type: image, document, audio, video or sticker.
type whatsappMediaRef struct {
	Kind     string
	ID       string
	MIME     string
	Caption  string
	Filename string
}

// whatsappGroupFields are the Cloud API webhook fields that describe a group
// changing rather than someone speaking. Each is acknowledged, never turned
// into a conversation.
var whatsappGroupFields = map[string]bool{
	"group_lifecycle_update":    true,
	"group_participants_update": true,
	"group_settings_update":     true,
	"group_status_update":       true,
}

// whatsappGroupUpdate is one of those fields on a delivery.
type whatsappGroupUpdate struct {
	Field   string
	GroupID string
}

// whatsappGroupUpdates lists the group-change fields on a delivery. A messages
// field is not one of them. A body that does not parse lists nothing.
func whatsappGroupUpdates(raw []byte) []whatsappGroupUpdate {
	var env struct {
		Entry []struct {
			Changes []struct {
				Field string `json:"field"`
				Value struct {
					GroupID string `json:"group_id"`
					Groups  []struct {
						ID string `json:"id"`
					} `json:"groups"`
				} `json:"value"`
			} `json:"changes"`
		} `json:"entry"`
	}
	if json.Unmarshal(raw, &env) != nil {
		return nil
	}
	var out []whatsappGroupUpdate
	for _, e := range env.Entry {
		for _, ch := range e.Changes {
			if !whatsappGroupFields[ch.Field] {
				continue
			}
			id := ch.Value.GroupID
			if id == "" && len(ch.Value.Groups) > 0 {
				id = ch.Value.Groups[0].ID
			}
			out = append(out, whatsappGroupUpdate{Field: ch.Field, GroupID: id})
		}
	}
	return out
}

// parseWhatsAppEvent lifts the first message this endpoint can answer.
//
// Meta batches: entry[] × changes[] × messages[]. A text message contributes
// its body. An image, document, audio, video or sticker contributes its media
// id, and its caption when it has one. A message of either kind that names a
// group_id — on the message or on its context — is a group message. Status
// callbacks carry no messages and are not turns.
func parseWhatsAppEvent(raw []byte) (whatsappInbound, bool) {
	var env struct {
		Entry []struct {
			Changes []struct {
				Value struct {
					GroupID  string `json:"group_id"`
					Metadata struct {
						PhoneNumberID string `json:"phone_number_id"`
					} `json:"metadata"`
					Messages []struct {
						From    string `json:"from"`
						ID      string `json:"id"`
						Type    string `json:"type"`
						GroupID string `json:"group_id"`
						Context struct {
							GroupID string `json:"group_id"`
						} `json:"context"`
						Text struct {
							Body string `json:"body"`
						} `json:"text"`
						Image    whatsappMediaObject `json:"image"`
						Document whatsappMediaObject `json:"document"`
						Audio    whatsappMediaObject `json:"audio"`
						Video    whatsappMediaObject `json:"video"`
						Sticker  whatsappMediaObject `json:"sticker"`
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
				in := whatsappInbound{
					Account:   ch.Value.Metadata.PhoneNumberID,
					From:      m.From,
					MessageID: m.ID,
					GroupID:   m.GroupID,
				}
				if in.GroupID == "" {
					in.GroupID = m.Context.GroupID
				}
				if in.GroupID == "" {
					in.GroupID = ch.Value.GroupID
				}
				switch m.Type {
				case "text":
					if strings.TrimSpace(m.Text.Body) == "" {
						continue
					}
					in.Text = m.Text.Body
				case "image", "document", "audio", "video", "sticker":
					obj := m.Image
					switch m.Type {
					case "document":
						obj = m.Document
					case "audio":
						obj = m.Audio
					case "video":
						obj = m.Video
					case "sticker":
						obj = m.Sticker
					}
					if strings.TrimSpace(obj.ID) == "" {
						continue
					}
					in.Text = obj.Caption
					in.Media = &whatsappMediaRef{
						Kind: m.Type, ID: obj.ID, MIME: obj.MIMEType,
						Caption: obj.Caption, Filename: obj.Filename,
					}
				default:
					continue
				}
				return in, true
			}
		}
	}
	return whatsappInbound{}, false
}

// whatsappMediaObject is the media object Meta nests under the message's type.
type whatsappMediaObject struct {
	ID       string `json:"id"`
	MIMEType string `json:"mime_type"`
	Caption  string `json:"caption"`
	Filename string `json:"filename"`
}

// whatsappEnvelopeKind maps Meta's media type onto the chat envelope's closed
// set. A sticker is a picture.
func whatsappEnvelopeKind(kind string) string {
	switch kind {
	case "image", "sticker":
		return "image"
	case "audio":
		return "audio"
	case "video":
		return "video"
	default:
		return "file"
	}
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
	for _, u := range whatsappGroupUpdates(body) {
		// The field and the group, never the body: a participants update names
		// phone numbers, and those do not belong in the log.
		s.Log.Info("whatsapp: group update", "field", u.Field, "group", u.GroupID)
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
	text, atts := whatsappForAgent(c.Context(), s, org, m)
	channel := m.From
	if m.GroupID != "" {
		channel = m.GroupID
	}
	// Meta redelivers on any non-2xx, so the same message id arrives more than
	// once as a matter of course, and the dedupe inside emitIngress is what makes
	// a redelivery free. An event it could not take answers a retriable non-2xx
	// for the same reason: nothing was recorded, so the redelivery is the recovery.
	if !emitIngress(c.Context(), s, org, Inbound{
		Provider:    "whatsapp",
		ExternalID:  m.Account,
		User:        m.From,
		Channel:     channel,
		Text:        text,
		DedupeKey:   m.MessageID,
		Group:       m.GroupID != "",
		Attachments: atts,
	}, "") {
		return zip.Errorf(http.StatusTooManyRequests, "whatsapp message not taken; please redeliver")
	}
	return c.NoContent(http.StatusOK)
}

// whatsappForAgent fetches the message's media, if it has any, and folds it
// into the text the agent reads. A fetch that fails still names the file: the
// person sent something, and dropping the turn would hide it.
func whatsappForAgent(ctx context.Context, s *cloud.Service[state], org string, m whatsappInbound) (string, []client.IngestAttachment) {
	if m.Media == nil {
		return m.Text, nil
	}
	ref := m.Media
	d := &whatsappDownloaded{
		ID: ref.ID, Name: ref.Filename, MIME: ref.MIME, Kind: whatsappEnvelopeKind(ref.Kind),
	}
	tok, err := TokenFor(ctx, org, "whatsapp", apiKeySecret)
	if err != nil {
		s.Log.Warn("whatsapp: media not fetched", "id", ref.ID, "err", err)
	} else if got, ferr := fetchWhatsAppMedia(ctx, string(tok), ref.ID, ref.Filename); ferr != nil {
		s.Log.Warn("whatsapp: media not fetched", "id", ref.ID, "err", ferr)
	} else {
		got.Kind = d.Kind
		if got.Name == "" {
			got.Name = ref.Filename
		}
		d = &got
	}
	return whatsappTurnText(m.Text, d), []client.IngestAttachment{{
		Kind: d.Kind,
		URL:  "whatsapp-media:" + ref.ID,
		MIME: d.MIME,
		Name: d.Name,
	}}
}
