package email

import (
	"cmp"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/crm"
)

// inbound.go — a reply the org's SES receipt rule took in, filed on its person.
//
// The receipt rule publishes each message it receives to the SNS topic the org's
// events arrive on, so it reaches sesWebhook and is verified like every delivery
// event. A message from an address the org holds a person for is filed inbound on
// that person through crm.FileMail, which also marks the person replied. Anything
// else is answered 200 and filed nowhere: SNS would only send it again.

// maxText bounds the text read from one received message.
const maxText = 64 << 10

// verdict is what SES concluded of a received message.
type verdict struct {
	Status string `json:"status"`
}

// sesReceipt is the notification SES publishes for a message its receipt rule took in.
type sesReceipt struct {
	Mail struct {
		MessageID   string   `json:"messageId"`
		Destination []string `json:"destination"`
	} `json:"mail"`
	Receipt struct {
		Timestamp string  `json:"timestamp"`
		Spam      verdict `json:"spamVerdict"`
		Virus     verdict `json:"virusVerdict"`
		DMARC     verdict `json:"dmarcVerdict"`
		Action    struct {
			Encoding string `json:"encoding"`
		} `json:"action"`
	} `json:"receipt"`
	// Content is the message as received, in the encoding the rule's action names.
	Content string `json:"content"`
}

// isReceipt reports whether an SNS message is a receipt, not a delivery event.
func isReceipt(message string) bool {
	var n struct {
		NotificationType string `json:"notificationType"`
	}
	return json.Unmarshal([]byte(message), &n) == nil && n.NotificationType == "Received"
}

// received files the reply a verified receipt carries and answers what became of it.
func (h *Handlers) received(org, message string, at time.Time) (string, error) {
	var n sesReceipt
	if err := json.Unmarshal([]byte(message), &n); err != nil {
		return "", zip.ErrBadRequest("the SNS message is not an SES receipt")
	}
	for _, v := range []verdict{n.Receipt.Spam, n.Receipt.Virus, n.Receipt.DMARC} {
		if v.Status == "FAIL" {
			return "failed a verdict", nil
		}
	}
	raw := n.Content
	if strings.EqualFold(n.Receipt.Action.Encoding, "BASE64") {
		b, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return "", zip.ErrBadRequest("the received message is not base64")
		}
		raw = string(b)
	}
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		return "", zip.ErrBadRequest("the received message does not read")
	}
	from, err := mail.ParseAddress(msg.Header.Get("From"))
	if err != nil {
		return "", zip.ErrBadRequest("the received message has no sender")
	}
	if automatic(msg.Header, from.Address) {
		return "automatic", nil
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil {
		subject = msg.Header.Get("Subject")
	}
	ctx, cancel := acting(org)
	defer cancel()
	filed, err := fileFn(ctx, org, crm.Mail{
		ID: "in:" + n.Mail.MessageID, From: from.Address, To: strings.Join(n.Mail.Destination, ", "),
		Subject: clip(subject, 998), Body: textOf(msg.Header.Get, msg.Body, 0),
		MessageID:  clip(msg.Header.Get("Message-Id"), 998),
		References: clip(cmp.Or(msg.Header.Get("References"), msg.Header.Get("In-Reply-To")), 998),
		At:         when(n.Receipt.Timestamp, at), Rail: "ses",
	})
	if err != nil {
		return "", h.unfiled(org, err)
	}
	if !filed {
		return "no such person", nil
	}
	return "filed", nil
}

// automatic reports whether a message is a machine's answer (an out-of-office, a
// bounce, a list's mail), which is no one replying.
func automatic(h mail.Header, from string) bool {
	if v := strings.ToLower(strings.TrimSpace(h.Get("Auto-Submitted"))); v != "" && v != "no" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(h.Get("Precedence"))) {
	case "bulk", "junk", "list", "auto_reply":
		return true
	}
	if strings.TrimSpace(h.Get("Return-Path")) == "<>" || h.Get("X-Autoreply") != "" || h.Get("X-Autorespond") != "" {
		return true
	}
	local, _, _ := strings.Cut(strings.ToLower(from), "@")
	return local == "mailer-daemon" || local == "postmaster"
}

// textOf is the plain text of a message or one of its parts, decoded: its
// text/plain part, the first of a multipart. A message with no text part has none.
func textOf(header func(string) string, body io.Reader, depth int) string {
	kind, params, err := mime.ParseMediaType(header("Content-Type"))
	if err != nil {
		kind = "text/plain"
	}
	switch {
	case strings.HasPrefix(kind, "multipart/") && depth < 4:
		parts := multipart.NewReader(body, params["boundary"])
		for {
			p, err := parts.NextPart()
			if err != nil {
				return ""
			}
			if t := textOf(p.Header.Get, p, depth+1); t != "" {
				return t
			}
		}
	case kind == "text/plain":
		switch strings.ToLower(strings.TrimSpace(header("Content-Transfer-Encoding"))) {
		case "quoted-printable":
			body = quotedprintable.NewReader(body)
		case "base64":
			body = base64.NewDecoder(base64.StdEncoding, body)
		}
		b, _ := io.ReadAll(io.LimitReader(body, maxText))
		return strings.TrimSpace(strings.ToValidUTF8(string(b), ""))
	}
	return ""
}
