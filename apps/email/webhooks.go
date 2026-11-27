package email

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// The provider event webhooks. The provider is the caller, so a signature is the
// only credential: an unsigned or badly signed delivery is answered 401 and
// records nothing, and the answer says nothing of which check it failed.

const (
	// maxWebhookBody bounds a delivery; SNS sends at most 256 KiB and a SendGrid
	// batch is a few thousand events.
	maxWebhookBody = 4 << 20
	// sendGridSkew is how far a SendGrid delivery's signed timestamp may be from now.
	sendGridSkew = 10 * time.Minute

	// The signature and the time SendGrid signed it, as it names its headers.
	sendGridSignature = "X-Twilio-Email-Event-Webhook-Signature"
	sendGridTimestamp = "X-Twilio-Email-Event-Webhook-Timestamp"

	// topicKey is the SES credential naming the SNS topic the org's events arrive on.
	topicKey = "sns-topic-arn"
	// publicKey is the SendGrid credential holding the key its webhook is signed with.
	publicKey = "webhook-public-key"
)

// errWebhook is every refusal of a delivery.
var errWebhook = errors.New("email: webhook not verified")

// refused is the one answer to a delivery that is not verified.
func refused() error { return zip.ErrUnauthorized("webhook not verified") }

// sesWebhook receives Amazon SES events as an SNS HTTPS subscription delivers
// them. The organization is named in the address (?org=) and holds the SNS topic
// its events arrive on (the sns-topic-arn credential): a message is accepted when
// it is signed by SNS, is from that topic, and its certificate comes from that
// topic's region. A subscription confirmation is completed the same way and
// records nothing.
//
// POST /v1/email/webhooks/ses?org=<org>
func (h *Handlers) sesWebhook(c *zip.Ctx) error {
	body := c.Body()
	if len(body) > maxWebhookBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "payload too large")
	}
	org := strings.TrimSpace(c.Query("org"))
	ctx := c.Context()
	creds, err := h.delivery.WebhookCredentials(ctx, org, "ses")
	if err != nil || strings.TrimSpace(creds[topicKey]) == "" {
		return refused()
	}
	topic := strings.TrimSpace(creds[topicKey])
	var env snsEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return refused()
	}
	if err := h.sns.Verify(ctx, &env, topic); err != nil {
		return refused()
	}
	switch env.Type {
	case "SubscriptionConfirmation":
		if err := h.sns.Confirm(ctx, &env, topic); err != nil {
			return refused()
		}
		return c.JSON(http.StatusOK, map[string]string{"subscription": "confirmed"})
	case "Notification":
		if isReceipt(env.Message) {
			what, err := h.received(org, env.Message, when(env.Timestamp, h.now().UTC()))
			if err != nil {
				return err
			}
			return c.JSON(http.StatusOK, map[string]string{"inbound": what})
		}
		e, err := sesEvent(env.MessageID, env.Message, when(env.Timestamp, h.now().UTC()))
		if errors.Is(err, errIgnored) {
			return c.JSON(http.StatusOK, map[string]int{"recorded": 0})
		}
		if err != nil {
			return zip.Errorf(http.StatusBadRequest, "%v", err)
		}
		fctx, cancel := acting(org)
		defer cancel()
		n, err := h.delivery.Record(fctx, org, []*Event{e})
		if err != nil {
			return h.unfiled(org, err)
		}
		return c.JSON(http.StatusOK, map[string]int{"recorded": n})
	}
	return c.JSON(http.StatusOK, map[string]string{"ignored": env.Type})
}

// sendGridWebhook receives SendGrid's Signed Event Webhook: the delivery is an
// array of events, signed (ECDSA P-256, SHA-256) over its timestamp header
// followed by the body, under the public key the organization holds
// (the webhook-public-key credential). A delivery signed more than ten minutes
// from now is refused, so a captured one cannot be replayed.
//
// POST /v1/email/webhooks/sendgrid?org=<org>
func (h *Handlers) sendGridWebhook(c *zip.Ctx) error {
	body := c.Body()
	if len(body) > maxWebhookBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "payload too large")
	}
	org := strings.TrimSpace(c.Query("org"))
	ctx := c.Context()
	creds, err := h.delivery.WebhookCredentials(ctx, org, "sendgrid")
	if err != nil {
		return refused()
	}
	if err := verifySendGrid(creds[publicKey], c.Header(sendGridTimestamp), c.Header(sendGridSignature), body, h.now()); err != nil {
		return refused()
	}
	raw, err := sendGridEvents(body)
	if err != nil {
		return zip.Errorf(http.StatusBadRequest, "%v", err)
	}
	// The delivery is signed whole, so every event in it is SendGrid's: one that
	// does not read is skipped and counted, never a reason to refuse the rest,
	// which SendGrid would only send again with that event still in it.
	evs := make([]*Event, 0, len(raw))
	skipped := 0
	for _, r := range raw {
		e, err := sendGridEvent(r)
		if errors.Is(err, errIgnored) {
			continue
		}
		if err != nil {
			skipped++
			if h.log != nil {
				h.log.Warn("email: a SendGrid event that does not read was skipped", "org", org, "err", err)
			}
			continue
		}
		evs = append(evs, e)
	}
	fctx, cancel := acting(org)
	defer cancel()
	n, err := h.delivery.Record(fctx, org, evs)
	if err != nil {
		return h.unfiled(org, err)
	}
	return c.JSON(http.StatusOK, map[string]int{"recorded": n, "skipped": skipped})
}

// fileBudget bounds filing one delivery. It is not the request's: a provider that
// stops waiting sends the delivery again, and finds filed what this finished.
const fileBudget = 2 * time.Minute

// acting is the context a verified delivery is filed in, acting for org. The
// request behind a webhook names no one, and on a context derived from a request
// the request's own assertion wins over a stated tenant, so the plane is called
// from a fresh one.
func acting(org string) (context.Context, context.CancelFunc) {
	return context.WithTimeout(plane.For(context.Background(), org), fileBudget)
}

// unfiled answers a verified delivery the ledger could not file 503, so the
// provider sends it again: what was filed is filed once, and what was not is taken
// up where it stopped.
func (h *Handlers) unfiled(org string, err error) error {
	if h.log != nil {
		h.log.Warn("email: a verified delivery was not filed; the provider will send it again", "org", org, "err", err)
	}
	return zip.Errorf(http.StatusServiceUnavailable, "email: the delivery was not filed; send it again")
}

// verifySendGrid checks sig, base64 of the DER ECDSA signature, over timestamp
// and body under key, base64 of the DER public key SendGrid shows for the webhook.
func verifySendGrid(key, timestamp, sig string, body []byte, now time.Time) error {
	key, sig, timestamp = strings.TrimSpace(key), strings.TrimSpace(sig), strings.TrimSpace(timestamp)
	if key == "" || sig == "" || timestamp == "" {
		return errWebhook
	}
	unix, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return errWebhook
	}
	if d := now.Sub(time.Unix(unix, 0)); d > sendGridSkew || d < -sendGridSkew {
		return errWebhook
	}
	der, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return errWebhook
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	pub, ok := parsed.(*ecdsa.PublicKey)
	if err != nil || !ok || pub.Curve != elliptic.P256() {
		return errWebhook
	}
	raw, err := base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return errWebhook
	}
	sum := sha256.Sum256(append([]byte(timestamp), body...))
	if !ecdsa.VerifyASN1(pub, sum[:], raw) {
		return errWebhook
	}
	return nil
}

// sendGridEvents reads a delivery: SendGrid posts an array of events. An object,
// alone or holding an events array, is read too.
func sendGridEvents(body []byte) ([]json.RawMessage, error) {
	var list []json.RawMessage
	if err := json.Unmarshal(body, &list); err == nil {
		return list, nil
	}
	var one struct {
		Events []json.RawMessage `json:"events"`
		Event  string            `json:"event"`
	}
	if err := json.Unmarshal(body, &one); err != nil {
		return nil, errors.New("email: SendGrid webhook body is not events")
	}
	if len(one.Events) > 0 {
		return one.Events, nil
	}
	if one.Event != "" {
		return []json.RawMessage{body}, nil
	}
	return nil, errors.New("email: SendGrid webhook body holds no event")
}
