package provider

// google_send.go sends one email from a person's own connected Gmail
// (google_user.go), published on the internal plane for the app that composes the
// message. The token never crosses: this process holds it, so this process sends.
//
// THE SENDER IS THE CALLER. The person and the org are read from the plane
// context, never from the request: a person a caller could name is a mailbox a
// caller could send from. One recipient per message, and every header value is
// refused if it carries a line break, so a recipient or a subject cannot add a
// header (a Bcc) of its own.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// gmailSendURL is Gmail's send endpoint, a var so a test can point it at a mock.
var gmailSendURL = "https://gmail.googleapis.com/gmail/v1/users/me/messages/send"

// The bounds of one message.
const (
	maxSubject = 200
	maxText    = 64 << 10
	maxHTML    = 256 << 10
)

// Why a message did not go (client.GoogleSendOut.Reason).
const (
	sendNotConnected = "not_connected"
	sendNotAllowed   = "not_allowed"
	sendLimited      = "limited"
	sendRejected     = "rejected"
	sendUnavailable  = "unavailable"
)

// exposeGoogleSend publishes the send on the internal plane. Mount calls it.
func exposeGoogleSend() {
	cloud.Plane().Post("/integrations/google/send", planeGoogleSend,
		zip.WithOperationID(client.IntegrationsGoogleSend),
		zip.WithSummary("Send one email from the caller's own connected Gmail"))
}

// planeGoogleSend sends one message from the caller's own Gmail, as them: the
// From is the address their connection holds, whatever the request says. A token
// near its end is rotated first; a token Google refuses is rotated once and the
// send tried once more; a grant Google no longer honours is dropped, and the
// answer says to connect again. Whatever happens at Google is an answer
// (GoogleSendOut.Reason), never a transport error, so the caller keeps the reason
// on what it sent. A request that is not one message to one recipient is 400.
//
// A named function, not a closure, so zipdoc can lift this prose into the registry.
func planeGoogleSend(ctx context.Context, in *client.GoogleSendIn) (*client.GoogleSendOut, error) {
	who := cloud.Who(ctx)
	org, user := strings.TrimSpace(who.Org), strings.TrimSpace(who.User)
	if org == "" || user == "" {
		return nil, zip.ErrForbidden("google send: no person on the call")
	}
	if mounted == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "google send: integrations is not serving")
	}
	if in == nil {
		return nil, zip.ErrBadRequest("google send: a message is required")
	}
	to, err := oneAddress(in.To)
	if err != nil {
		return nil, err
	}
	if err := checkMessage(in); err != nil {
		return nil, err
	}
	s := mounted
	conn, found, err := s.State.store.Get(ctx, org, user, googleUserID, defaultLabel)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "lookup: %v", err)
	}
	if !found {
		return notConnected(), nil
	}
	if !slices.Contains(conn.Scopes, gmailSendScope) {
		return &client.GoogleSendOut{Reason: sendNotAllowed, Error: "Your Google connection does not allow sending mail; connect Google again and allow it."}, nil
	}
	for attempt := 0; attempt < 2; attempt++ {
		tok, from, ok, err := personGoogle(ctx, s, org, user, attempt > 0)
		if err != nil {
			var he *zip.HTTPError
			if errors.As(err, &he) && he.Status == http.StatusBadGateway {
				return &client.GoogleSendOut{Reason: sendUnavailable, Error: "Google did not answer; try again in a moment."}, nil
			}
			return nil, err
		}
		if !ok {
			return notConnected(), nil
		}
		raw, err := rfc5322(from, to, in)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "message: %v", err)
		}
		id, status, reason := gmailSend(ctx, tok, raw)
		switch {
		case status == http.StatusOK:
			return &client.GoogleSendOut{ID: id, From: from}, nil
		case status == http.StatusUnauthorized && attempt == 0:
			continue // a token Google refuses: rotate it whatever its expiry, and try once more
		case status == http.StatusUnauthorized:
			forgetGoogle(ctx, s, org, user)
			return notConnected(), nil
		}
		return refusal(status, reason), nil
	}
	return notConnected(), nil
}

// notConnected is the answer for a person with no live Google connection.
func notConnected() *client.GoogleSendOut {
	return &client.GoogleSendOut{Reason: sendNotConnected, Error: "Connect your Google account to send from your Gmail."}
}

// refusal maps a send Gmail did not take to its reason. 0 is no answer at all.
func refusal(status int, reason string) *client.GoogleSendOut {
	switch {
	case status == http.StatusTooManyRequests ||
		(status == http.StatusForbidden && strings.Contains(strings.ToLower(reason), "limit")):
		return &client.GoogleSendOut{Reason: sendLimited, Error: "Gmail's sending limit for your account is reached; try again later."}
	case status == http.StatusForbidden:
		return &client.GoogleSendOut{Reason: sendNotAllowed, Error: "Google did not allow this account to send; connect Google again and allow sending."}
	case status == http.StatusBadRequest:
		return &client.GoogleSendOut{Reason: sendRejected, Error: "Gmail refused the message or its recipient."}
	}
	return &client.GoogleSendOut{Reason: sendUnavailable, Error: "Google did not answer; try again in a moment."}
}

// oneAddress is the one recipient, parsed, with no line break anywhere in it.
func oneAddress(raw string) (*mail.Address, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, "\r\n") {
		return nil, zip.ErrBadRequest("google send: one recipient address is required")
	}
	a, err := mail.ParseAddress(raw)
	if err != nil || strings.Count(a.Address, "@") != 1 {
		return nil, zip.ErrBadRequest("google send: the recipient is not one email address")
	}
	return a, nil
}

// checkMessage bounds a message and refuses a header value that could start a
// header of its own.
func checkMessage(in *client.GoogleSendIn) error {
	subject := strings.TrimSpace(in.Subject)
	switch {
	case subject == "" || len([]rune(subject)) > maxSubject || strings.ContainsAny(subject, "\r\n"):
		return zip.ErrBadRequest(fmt.Sprintf("google send: a subject of one line and at most %d characters is required", maxSubject))
	case strings.TrimSpace(in.Text) == "" || len(in.Text) > maxText:
		return zip.ErrBadRequest("google send: a text body of at most 64 KiB is required")
	case len(in.HTML) > maxHTML:
		return zip.ErrBadRequest("google send: the HTML body is larger than 256 KiB")
	}
	if u := strings.TrimSpace(in.Unsubscribe); u != "" {
		p, err := url.Parse(u)
		if err != nil || strings.ContainsAny(u, "\r\n<> ") || (p.Scheme != "https" && p.Scheme != "mailto") || (p.Scheme == "https" && p.Host == "") {
			return zip.ErrBadRequest("google send: unsubscribe is one https or mailto address")
		}
	}
	return nil
}

// rfc5322 writes the message: From the person, To the one recipient,
// the subject encoded for any language, the text and (when given) the HTML as its
// alternative, each base64 so no body line can be read as a header.
func rfc5322(from string, to *mail.Address, in *client.GoogleSendIn) ([]byte, error) {
	var b bytes.Buffer
	header := func(k, v string) { b.WriteString(k + ": " + v + "\r\n") }
	header("From", (&mail.Address{Address: from}).String())
	header("To", to.String())
	header("Subject", mime.QEncoding.Encode("utf-8", strings.TrimSpace(in.Subject)))
	header("Date", time.Now().UTC().Format(time.RFC1123Z))
	header("MIME-Version", "1.0")
	if u := strings.TrimSpace(in.Unsubscribe); u != "" {
		header("List-Unsubscribe", "<"+u+">")
		if strings.HasPrefix(u, "https:") {
			header("List-Unsubscribe-Post", "List-Unsubscribe=One-Click")
		}
	}
	if in.HTML == "" {
		header("Content-Type", `text/plain; charset="utf-8"`)
		header("Content-Transfer-Encoding", "base64")
		b.WriteString("\r\n")
		wrap64(&b, in.Text)
		return b.Bytes(), nil
	}
	n := make([]byte, 12)
	if _, err := rand.Read(n); err != nil {
		return nil, err
	}
	boundary := "hz-" + hex.EncodeToString(n)
	header("Content-Type", `multipart/alternative; boundary="`+boundary+`"`)
	b.WriteString("\r\n")
	for _, part := range []struct{ kind, body string }{{"text/plain", in.Text}, {"text/html", in.HTML}} {
		b.WriteString("--" + boundary + "\r\n")
		header("Content-Type", part.kind+`; charset="utf-8"`)
		header("Content-Transfer-Encoding", "base64")
		b.WriteString("\r\n")
		wrap64(&b, part.body)
	}
	b.WriteString("--" + boundary + "--\r\n")
	return b.Bytes(), nil
}

// wrap64 writes body as base64 in 76-character lines.
func wrap64(b *bytes.Buffer, body string) {
	enc := base64.StdEncoding.EncodeToString([]byte(body))
	for len(enc) > 76 {
		b.WriteString(enc[:76] + "\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc + "\r\n")
}

// gmailSend is one call to Gmail's send endpoint with the person's token. It
// answers Gmail's message id on 200, else the status and Gmail's own reason code
// (never a body, which could carry the message back into a log). Status 0 is no
// answer at all.
func gmailSend(ctx context.Context, token string, raw []byte) (id string, status int, reason string) {
	body, _ := json.Marshal(map[string]string{"raw": base64.RawURLEncoding.EncodeToString(raw)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, gmailSendURL, bytes.NewReader(body))
	if err != nil {
		return "", 0, ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := googleHTTP.Do(req)
	if err != nil {
		return "", 0, ""
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		ID    string `json:"id"`
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
			Status string `json:"status"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&out)
	if resp.StatusCode == http.StatusOK && out.ID != "" {
		return sanitizeMeta(out.ID), http.StatusOK, ""
	}
	if resp.StatusCode == http.StatusOK {
		return "", 0, "" // a 200 with no id is no answer this can stand behind
	}
	reason = out.Error.Status
	if len(out.Error.Errors) > 0 {
		reason = out.Error.Errors[0].Reason
	}
	return "", resp.StatusCode, truncate(sanitizeMeta(reason), 64)
}
