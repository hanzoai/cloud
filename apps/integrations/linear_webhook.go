package integrations

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

// linear_webhook.go is Linear's delivery address: /v1/integrations/linear/webhook.
// PUBLIC at the JWT layer (Linear has no Hanzo session) — auth is the signature,
// verified fail-closed over the raw bytes with the secret the org sealed at claim
// time (linear.go). The tenant is the organization the delivery names, and the
// signature is what proves the delivery is that organization's.
//
// An Issue event is mirrored into the native todo (one row per issue, idempotent
// by identifier) and, like a Comment event, handed to the automations engine as a
// verified trigger — which is how an org wires "assigned to the bot" or "mentioned
// in a comment" to an agent run.

// linearMaxWebhookBody bounds the payload read and signed over.
const linearMaxWebhookBody = 8 << 20

// linearWebhookSkew is how old a delivery may be. The body carries its own
// `webhookTimestamp`, and a signature that verifies is still a replay if the
// delivery is stale; Linear's own guidance is one minute.
var linearWebhookSkew = time.Minute

// verifyLinearSignature reports whether sig is Linear's signature over body under
// secret: "Linear-Signature: <hex(HMAC_SHA256(secret, body))>". Constant-time;
// fail-closed on an empty secret, header or malformed hex.
func verifyLinearSignature(secret, sig string, body []byte) bool {
	if secret == "" || sig == "" {
		return false
	}
	got, err := hex.DecodeString(strings.TrimSpace(sig))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}

// linearEvent is the envelope every delivery shares; Data is decoded per type.
type linearEvent struct {
	Action           string          `json:"action"` // create | update | remove
	Type             string          `json:"type"`   // Issue | Comment | …
	OrganizationID   string          `json:"organizationId"`
	WebhookTimestamp int64           `json:"webhookTimestamp"` // unix milliseconds
	URL              string          `json:"url"`
	Data             json.RawMessage `json:"data"`
}

// linearWebhook verifies and processes one delivery. It answers a benign 200 for
// everything it does not act on (unknown organization, other types, a remove) so
// Linear does not retry-storm; a bad or stale signature is 401, a sink failure 502.
func linearWebhook(s *cloud.Service[state], c *zip.Ctx) error {
	body := c.Body()
	if len(body) > linearMaxWebhookBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "payload too large")
	}
	// The envelope is read BEFORE it is trusted, for one field only: which
	// organization's secret to verify with. Nothing else in it is acted on until
	// the signature over these exact bytes has been checked with that secret.
	var ev linearEvent
	if err := json.Unmarshal(body, &ev); err != nil {
		return zip.ErrBadRequest("invalid payload")
	}
	org, ok := OrgForExternalID("linear", ev.OrganizationID)
	if !ok {
		return c.JSON(http.StatusOK, map[string]any{"ignored": "unknown organization"})
	}
	secret, err := kmsGet(s, kmsPath(org, "linear"), linearWebhookSecret)
	if err != nil || !verifyLinearSignature(string(secret), c.Header("Linear-Signature"), body) {
		return zip.Errorf(http.StatusUnauthorized, "invalid signature")
	}
	sent := time.UnixMilli(ev.WebhookTimestamp)
	if ev.WebhookTimestamp == 0 || time.Since(sent) > linearWebhookSkew || time.Until(sent) > linearWebhookSkew {
		return zip.Errorf(http.StatusUnauthorized, "stale delivery")
	}

	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	dedupe := firstNonEmpty(c.Header("Linear-Delivery"), ev.Type+":"+ev.Action+":"+c.Header("Linear-Event"))
	name := strings.ToLower(ev.Type) + "." + ev.Action

	switch ev.Type {
	case "Issue":
		if ev.Action == "remove" {
			// Native is canonical; an inbound delete never removes a native row.
			fireTrigger(c.Context(), org, "linear", name, dedupe, 0, payload)
			return c.JSON(http.StatusOK, map[string]any{"ignored": "remove"})
		}
		var is linearIssue
		if err := json.Unmarshal(ev.Data, &is); err != nil || is.Identifier == "" {
			return zip.ErrBadRequest("invalid issue payload")
		}
		created, err := mirrorLinearIssue(c.Context(), org, is)
		if err != nil {
			return zip.Errorf(http.StatusBadGateway, "mirror issue: %v", err)
		}
		fireTrigger(c.Context(), org, "linear", name, dedupe, 0, payload)
		return c.JSON(http.StatusOK, map[string]any{"mirrored": true, "created": created, "action": ev.Action})
	case "Comment":
		fireTrigger(c.Context(), org, "linear", name, dedupe, 0, payload)
		if in, ok := linearMentionInbound(ev); ok {
			if !emitIngress(c.Context(), s, org, in, "") {
				return zip.Errorf(http.StatusTooManyRequests, "linear comment not taken; please redeliver")
			}
			return c.JSON(http.StatusOK, map[string]any{"triggered": true, "turn": true, "action": ev.Action})
		}
		return c.JSON(http.StatusOK, map[string]any{"triggered": true, "action": ev.Action})
	}
	return c.JSON(http.StatusOK, map[string]any{"ignored": "event"})
}

// linearComment is the slice of a Comment delivery a turn needs.
type linearComment struct {
	ID      string `json:"id"`
	Body    string `json:"body"`
	IssueID string `json:"issueId"`
	UserID  string `json:"userId"`
	User    struct {
		ID string `json:"id"`
	} `json:"user"`
}

// linearMentionInbound turns a created comment that addresses @hanzo into the
// normalized inbound the channels transport reads: the issue is the room, the
// commenter is the sender, the organization is the account. A reply this
// platform posts never carries the mention, so it never re-enters here.
func linearMentionInbound(ev linearEvent) (Inbound, bool) {
	if ev.Action != "create" {
		return Inbound{}, false
	}
	var cm linearComment
	if err := json.Unmarshal(ev.Data, &cm); err != nil || cm.ID == "" || cm.IssueID == "" {
		return Inbound{}, false
	}
	text, ok := stripMention(cm.Body, linearMention)
	if !ok {
		return Inbound{}, false
	}
	return Inbound{
		Provider: "linear", ExternalID: ev.OrganizationID,
		User:    firstNonEmpty(cm.UserID, cm.User.ID),
		Channel: cm.IssueID, Text: text, DedupeKey: "comment:" + cm.ID,
	}, true
}

// stripMention reports whether text addresses mention (case-insensitively, as
// a whole word) and returns the text with the first occurrence removed.
func stripMention(text, mention string) (string, bool) {
	lower := strings.ToLower(text)
	m := strings.ToLower(mention)
	i := strings.Index(lower, m)
	for i >= 0 {
		end := i + len(m)
		before := i == 0 || !isWordByte(lower[i-1])
		after := end == len(lower) || !isWordByte(lower[end])
		if before && after {
			return strings.TrimSpace(text[:i] + text[end:]), true
		}
		j := strings.Index(lower[end:], m)
		if j < 0 {
			break
		}
		i = end + j
	}
	return "", false
}

func isWordByte(b byte) bool {
	return b == '_' || b == '-' || (b >= '0' && b <= '9') || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}
