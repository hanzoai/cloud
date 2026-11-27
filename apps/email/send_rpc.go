package email

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// Commercial mail from another process, on the internal plane.
//
// A marketing broadcast sends from marketing's process, one recipient at a time,
// and its mail is commercial: it must be asked of the one suppression list, carry
// the signed unsubscribe link and be filed on the CRM, which is exactly what POST
// /v1/email does. So the plane op is that route's own core with the org as an
// argument, as every plane op takes it, and commercial mail keeps one sender.

// exposeSend publishes the send op. Mount calls it.
func exposeSend(h *Handlers) {
	cloud.Plane().Post("/email/send", h.planeSend,
		zip.WithOperationID(client.EmailSend),
		zip.WithSummary("Send one commercial email for an org through its provider"))
}

// planeSend sends one letter for the org it names through POST /v1/email's own
// core, and answers the message's id as its delivery events name it, or why the
// suppression list held the recipient. A letter for a marketing list carries that
// list's one-click unsubscribe link. The sender's name rides the from address
// ("Name <address>"), and a reply address the Reply-To header.
func (h *Handlers) planeSend(ctx context.Context, in *client.EmailLetter) (*client.EmailPosted, error) {
	if in == nil {
		return nil, zip.ErrBadRequest("email: no letter")
	}
	org, to := strings.TrimSpace(in.Org), strings.ToLower(strings.TrimSpace(in.To))
	if org == "" || to == "" {
		return nil, zip.ErrBadRequest("email: org and to are required")
	}
	from := strings.TrimSpace(in.From)
	if from != "" && strings.TrimSpace(in.Name) != "" {
		from = (&mail.Address{Name: strings.TrimSpace(in.Name), Address: from}).String()
	}
	msg := &SendMessageInput{From: from, To: []string{to}, Subject: in.Subject, Text: in.Text, HTML: in.HTML,
		Provider: in.Provider, IdempotencyKey: in.Key}
	if r := strings.TrimSpace(in.ReplyTo); r != "" {
		msg.Headers = map[string]string{"Reply-To": r}
	}
	out, err := h.post(ctx, org, "marketing", strings.TrimSpace(in.List), msg)
	if errors.Is(err, errRefused) {
		return &client.EmailPosted{Refused: out.Refused[0].Reason}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(out.Messages) == 0 {
		return nil, zip.Errorf(http.StatusBadGateway, "email: nothing was sent")
	}
	return &client.EmailPosted{Message: mailKey(out.Messages[0])}, nil
}
