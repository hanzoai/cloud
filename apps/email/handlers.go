package email

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	marketingpeer "github.com/hanzoai/cloud/client/marketing"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/hanzoai/cloud/sdk/principal"
	addr "github.com/hanzoai/ai/address"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// How much verifying one caller, and the deployment, may do in an hour. A probe
// is the SMTP check, which connects to the address's mail server from our
// addresses: it is held far tighter, because a mail server that sees many is a
// mail server that blocks us, and a caller who may probe freely can learn which
// mailboxes exist.
const (
	verifyWindow     = time.Hour
	verifyRate       = 120
	verifyDeployment = 6000
	probeRate        = 20
	probeDeployment  = 400
)

// verifying and probing count each caller's checks across the deployment.
var (
	verifying = cloud.NewWindow("email:verify", verifyRate, verifyWindow)
	probing   = cloud.NewWindow("email:probe", probeRate, verifyWindow)
)

// limits are the hourly allowances, a field so a test can set small ones.
type limits struct {
	verify, verifyAll, probe, probeAll int
}

// Handlers wires HTTP requests to the verifier and the delivery service.
type Handlers struct {
	verifier Verifier
	delivery *Service
	log      luxlog.Logger

	// sns verifies the messages Amazon SNS delivers to the SES webhook.
	sns *SNS
	// now is the clock a signed delivery's timestamp is read against.
	now func() time.Time

	verifying, probing *cloud.Window
	limit              limits
}

// NewHandlers creates the handlers over a verifier and a delivery service.
func NewHandlers(v Verifier, d *Service, log luxlog.Logger) *Handlers {
	return &Handlers{
		verifier:  v,
		delivery:  d,
		log:       log,
		sns:       NewSNS(nil),
		now:       time.Now,
		verifying: verifying,
		probing:   probing,
		limit:     limits{verifyRate, verifyDeployment, probeRate, probeDeployment},
	}
}

// admit charges one check to the caller and to the deployment, and says whether
// both had room.
func admit(w *cloud.Window, subject string, one, all int) bool {
	left, _ := w.Spend([]addr.Bucket{{Key: "sub:" + subject, Scale: 1}}, one, verifyWindow)
	if left < 0 {
		return false
	}
	left, _ = w.Spend([]addr.Bucket{{Key: "deployment", Scale: 1}}, all, verifyWindow)
	return left >= 0
}

// probes reports whether the checks include the SMTP probe.
func probes(in *VerificationInput) bool {
	return slices.ContainsFunc(in.Checks, func(c string) bool {
		return strings.EqualFold(strings.TrimSpace(c), string(CheckSMTP))
	})
}

// postVerification evaluates an email address using explicitly selected, composable checks.
//
// A signed-in caller only: the SMTP check connects to the address's mail server
// from ours, so an anonymous caller could probe mailboxes and get our addresses
// blocked. Each caller may run 120 checks an hour and 20 of them SMTP probes, and
// the deployment 6,000 and 400; past either is 429.
//
// POST /v1/email/verify
func (h *Handlers) postVerification(ctx context.Context, in *VerificationInput) (*VerificationResult, error) {
	if !principal.ValidatedFrom(ctx) {
		return nil, zip.ErrUnauthorized("email: authentication required")
	}
	if in == nil || strings.TrimSpace(in.Email) == "" {
		return nil, zip.Errorf(http.StatusBadRequest, "email is required")
	}
	subject, _ := principal.SubjectFrom(ctx)
	if subject == "" {
		subject, _ = principal.OrgFrom(ctx)
	}
	if !admit(h.verifying, subject, h.limit.verify, h.limit.verifyAll) {
		return nil, zip.Errorf(http.StatusTooManyRequests, "too many verifications; try again later")
	}
	if probes(in) && !admit(h.probing, subject, h.limit.probe, h.limit.probeAll) {
		return nil, zip.Errorf(http.StatusTooManyRequests, "too many mailbox probes; try again later")
	}

	res, err := h.verifier.Verify(ctx, in)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "email verification failed: %v", err)
	}
	return res, nil
}

// sendCampaign is the shape of a campaign a send names: what an SES message tag may
// hold. campaignShape is a campaign a stats read may name, which includes one a
// provider told of (SendGrid's own campaigns carry names with spaces).
var (
	sendCampaign  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
	campaignShape = regexp.MustCompile(`^[^"\\\x00-\x1f]{1,128}$`)
)

// postMessage sends commercial email from the org through SES or SendGrid: one
// message per recipient, each carrying its own signed unsubscribe link in the
// footer and in List-Unsubscribe and List-Unsubscribe-Post (RFC 8058), and every
// http(s) link tagged with utm_source=email, utm_medium=<provider>,
// utm_campaign=<campaign>, utm_content=link<n> and from=<message id>, each only
// where the link does not carry it already. Every recipient is first asked of the
// one suppression check (marketing_clearance): an address the org's list holds,
// whose company unsubscribed, or that the platform blocks is not sent to, and is
// answered in refused with status 422 and the reason; when every recipient is
// refused the request is 422. A list that cannot answer sends nothing (503).
//
// The rail is the provider the request pins (ses or sendgrid), else the org's
// default (PUT /v1/email/provider), else SES, else SendGrid, whichever the org
// holds credentials for; anything else is 400. Every message is kept in the org's
// ledger (GET /v1/email), counted (GET /v1/email/stats), stated on the event
// stream as email_sent, and put on the CRM record of the person the org holds for
// the address. The same idempotency key and recipient send once. Transactional
// mail (a code, a receipt) is not this route's: it goes by /v1/notify, which no
// opt-out stops.
//
// POST /v1/email
func (h *Handlers) postMessage(ctx context.Context, in *SendMessageInput) (*Dispatch, error) {
	if !principal.ValidatedFrom(ctx) {
		return nil, zip.ErrUnauthorized("email: authentication required")
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok || org == "" {
		return nil, zip.ErrUnauthorized("email: org scope required")
	}
	if in == nil {
		return nil, zip.Errorf(http.StatusBadRequest, "missing request body")
	}
	by, _ := principal.SubjectFrom(ctx)
	if email := strings.TrimSpace(zip.CallerOf(ctx).Email); email != "" {
		by = email
	}
	out, err := h.post(ctx, org, by, "", in)
	if errors.Is(err, errRefused) {
		var why []string
		for _, r := range out.Refused {
			why = append(why, r.Address+": "+r.Reason)
		}
		return nil, zip.Errorf(http.StatusUnprocessableEntity, "email: every recipient is suppressed (%s)", strings.Join(why, "; "))
	}
	return out, err
}

// errRefused is a send whose every recipient the suppression list held; the
// Dispatch beside it says why, recipient by recipient.
var errRefused = errors.New("email: every recipient is suppressed")

// post is the one commercial send: POST /v1/email and email_send both reach it.
// list, when set, is the marketing list the mail is for, whose own one-click
// unsubscribe link the recipient then carries.
func (h *Handlers) post(ctx context.Context, org, by, list string, in *SendMessageInput) (*Dispatch, error) {
	if in.Text == "" && in.Body != "" {
		in.Text = in.Body
	}
	if in.Subject == "" && in.Text == "" && in.HTML == "" && in.TemplateID == "" {
		return nil, zip.Errorf(http.StatusBadRequest, "%v: a subject, text or html is required", ErrInvalidRequest)
	}
	campaign := strings.TrimSpace(in.Campaign)
	if campaign != "" && !sendCampaign.MatchString(campaign) {
		return nil, zip.Errorf(http.StatusBadRequest, "%v: campaign is 1 to 128 letters, digits, '-' or '_'", ErrInvalidRequest)
	}
	provider, creds, err := h.delivery.Route(ctx, org, strings.TrimSpace(in.Provider))
	if err != nil {
		return nil, zip.Errorf(http.StatusBadRequest, "%v", err)
	}
	from := strings.TrimSpace(in.From)
	if from == "" {
		from = creds["from-email"]
	}
	// Every recipient is cleared before any message leaves, so a list that cannot
	// answer sends nothing rather than half.
	var to []string
	links, seen := map[string]string{}, map[string]bool{}
	out := &Dispatch{Messages: []*MessageRecord{}}
	for _, raw := range in.To {
		addr := strings.ToLower(strings.TrimSpace(raw))
		if addr == "" || seen[addr] {
			continue
		}
		seen[addr] = true
		c, err := clearFn(plane.For(ctx, org), &client.Addressee{Org: org, Channel: "email", Address: addr, List: list})
		if err != nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "email: the suppression list did not answer, so nothing was sent: %v", err)
		}
		if c.Refused != "" {
			out.Refused = append(out.Refused, Refused{Address: addr, Status: http.StatusUnprocessableEntity, Reason: c.Refused})
			h.file(ctx, org, crm.Mail{ID: newID("msg"), To: addr, By: by, Subject: in.Subject, Body: cmp.Or(in.Text, in.HTML), Status: "suppressed", Reason: c.Refused})
			continue
		}
		if c.Unsubscribe == "" {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "email: no unsubscribe link for %s, so nothing was sent", addr)
		}
		links[addr] = c.Unsubscribe
		to = append(to, addr)
	}
	if len(to) == 0 && len(out.Refused) == 0 {
		return nil, zip.Errorf(http.StatusBadRequest, "%v: at least one recipient address is required", ErrInvalidRequest)
	}
	if len(to) == 0 {
		return out, errRefused
	}

	var failed error
	for _, addr := range to {
		idempotency := ""
		if in.IdempotencyKey != "" {
			idempotency = in.IdempotencyKey + ":" + addr
		}
		id := MessageID(org, idempotency)
		t := tag{message: id, provider: provider, campaign: campaign}
		o := &Outgoing{
			ID: id, Provider: provider, Creds: creds, From: from, To: addr, Subject: in.Subject,
			Text: Footer(t.text(in.Text), links[addr], addr), Headers: maps.Clone(in.Headers),
			Campaign: campaign, Idempotency: idempotency,
		}
		if in.HTML != "" {
			o.HTML = Footer(t.html(in.HTML), links[addr], addr)
		}
		if o.Headers == nil {
			o.Headers = map[string]string{}
		}
		maps.Copy(o.Headers, Unsubscribe(links[addr]))
		record, err := h.delivery.Send(ctx, org, o)
		if record == nil {
			return nil, zip.Errorf(http.StatusServiceUnavailable, "email: the ledger did not answer, so %s was not sent: %v", addr, err)
		}
		if err != nil && failed == nil {
			failed = err
		}
		status, reason := "sent", ""
		if record.Status == StatusFailed {
			status, reason = "failed", record.Error
		}
		h.file(ctx, org, crm.Mail{ID: mailKey(record), To: addr, By: by, Subject: in.Subject, Body: cmp.Or(o.Text, o.HTML),
			At: record.CreatedAt, Status: status, Reason: reason, Rail: record.Provider})
		out.Messages = append(out.Messages, record)
	}
	if failed != nil && !slices.ContainsFunc(out.Messages, func(m *MessageRecord) bool { return m.Status != StatusFailed }) {
		return nil, zip.Errorf(http.StatusBadGateway, "%v", failed)
	}
	return out, nil
}

// The CRM and the marketing list, variables so a test can see what reaches them.
// Only a test reassigns them.
var (
	clearFn    = marketingpeer.MarketingClearance
	suppressFn = marketingpeer.MarketingSuppress
	fileFn     = crm.FileMail
	eventFn    = crm.MailEvent
)

// mailKey is the key a message is filed under and its delivery events find it
// by: the provider and its own id for the message, else ours.
func mailKey(r *MessageRecord) string {
	if r.ProviderID != "" {
		return r.Provider + ":" + r.ProviderID
	}
	return r.ID
}

// file puts a message on the CRM record of the person org holds for its address.
// A CRM that cannot answer costs the timeline one entry, never the send.
func (h *Handlers) file(ctx context.Context, org string, m crm.Mail) {
	if m.At.IsZero() {
		m.At = time.Now()
	}
	if _, err := fileFn(ctx, org, m); err != nil && h.log != nil {
		h.log.Warn("email: file a message on the crm", "org", org, "err", err)
	}
}

// member is the org a signed-in caller acts in.
func member(ctx context.Context) (string, error) {
	if !principal.ValidatedFrom(ctx) {
		return "", zip.ErrUnauthorized("email: authentication required")
	}
	org, ok := principal.OrgFrom(ctx)
	if !ok || org == "" {
		return "", zip.ErrUnauthorized("email: org scope required")
	}
	return org, nil
}

// failure is how a ledger error answers: a refusal the ledger made as it stands,
// anything else 503.
func failure(err error) error {
	var he *zip.HTTPError
	if errors.As(err, &he) {
		return err
	}
	return zip.Errorf(http.StatusServiceUnavailable, "%v", err)
}

// getMessage reads one message of the org's ledger, sent through this API or told of
// by its provider, with what became of it.
//
// GET /v1/email/:id
func (h *Handlers) getMessage(ctx context.Context, in *GetMessageInput) (*MessageRecord, error) {
	org, err := member(ctx)
	if err != nil {
		return nil, err
	}
	if in == nil || strings.TrimSpace(in.ID) == "" {
		return nil, zip.Errorf(http.StatusBadRequest, "id is required")
	}
	record, err := h.delivery.GetMessage(ctx, org, in.ID)
	if errors.Is(err, ErrNotFound) {
		return nil, zip.ErrNotFound(err.Error())
	}
	if err != nil {
		return nil, failure(err)
	}
	return record, nil
}

// listMessages reads a page of the org's ledger, newest first: every message sent
// through POST /v1/email, and every message SES or SendGrid told of by its events,
// sent from anywhere on the org's account. It narrows by status, provider, a range
// of send time (since, until) and one recipient, and pages by cursor: pass the
// answer's next to read the page after.
//
// GET /v1/email
func (h *Handlers) listMessages(ctx context.Context, in *ListMessagesInput) (*ListMessagesResult, error) {
	org, err := member(ctx)
	if err != nil {
		return nil, err
	}
	if in == nil {
		in = &ListMessagesInput{}
	}
	out, err := h.delivery.ListMessages(ctx, org, in)
	if err != nil {
		return nil, failure(err)
	}
	return out, nil
}

// stats counts the org's mail over a range: sent, delivered, opened, clicked,
// bounced, complained and unsubscribed, each a count of messages, in all, by
// provider, by campaign and by day or hour (UTC). Mail sent around this API, from
// the org's own tools on its SES or SendGrid account, is counted from the
// providers' events like mail sent through it.
//
// GET /v1/email/stats
func (h *Handlers) stats(ctx context.Context, in *StatsInput) (*Stats, error) {
	org, err := member(ctx)
	if err != nil {
		return nil, err
	}
	if in == nil {
		in = &StatsInput{}
	}
	out, err := h.delivery.Stats(ctx, org, in)
	if err != nil {
		return nil, failure(err)
	}
	return out, nil
}

// trail is the audit trail a change of the org's default provider is recorded on
// before it is made; with none, nothing is changed.
var trail *audit.Recorder

// setProvider sets the provider the org's mail leaves by when a send pins none:
// ses or sendgrid. With none set it is SES, else SendGrid, whichever the org holds
// credentials for; a send that pins one goes by it. Org admin only, and on the
// audit trail (email.provider.set) before it takes effect.
//
// PUT /v1/email/provider
func (h *Handlers) setProvider(ctx context.Context, in *Choice) (*Choice, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	if !cloud.Admin.Admits(cloud.AuthorityIn(ctx)) {
		return nil, cloud.Admin.Refusal()
	}
	provider := strings.TrimSpace(in.Provider)
	if !slices.Contains(providers, provider) {
		return nil, zip.ErrBadRequest("email: provider is ses or sendgrid")
	}
	if trail == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "email: the audit trail is not open, so nothing was changed")
	}
	who := cloud.Who(ctx)
	after, _ := json.Marshal(map[string]string{"provider": provider})
	if _, err := trail.Append(ctx, audit.Record{
		Actor:     audit.Actor{Org: cmp.Or(who.Owner, who.Org), Sub: who.User, Email: who.Email},
		Action:    "email.provider.set",
		Resource:  audit.Resource{Type: "org", ID: org},
		Auth:      audit.AuthContext{Method: "attested", IsAdmin: who.Admin},
		Outcome:   audit.Outcome{Result: "success", Status: http.StatusOK},
		SourceIP:  who.IP,
		RequestID: who.RequestID,
		After:     after,
	}); err != nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "email: the audit trail did not take the change, so nothing was changed: %v", err)
	}
	if err := h.delivery.SetDefault(ctx, org, provider); err != nil {
		return nil, failure(err)
	}
	return &Choice{Provider: provider}, nil
}

// getSESStatus reports the org's Amazon SES configuration: whether it holds the
// credentials a send needs, the account's limits as SES answers them (sandbox,
// sending enabled, 24-hour quota, rate, sent in the last 24 hours), what the org's
// ledger counted sent by SES in the last 24 hours, and the DNS records of the
// sending domain.
//
// GET /v1/email/ses/status
func (h *Handlers) getSESStatus(ctx context.Context, in *SESStatusInput) (*SESStatusResult, error) {
	org, err := member(ctx)
	if err != nil {
		return nil, err
	}
	domain := ""
	if in != nil {
		domain = in.Domain
	}
	res, err := h.delivery.GetSESStatus(ctx, org, domain)
	if err != nil {
		return nil, failure(err)
	}
	return res, nil
}

// getSendGridStatus reports the org's SendGrid configuration: whether it holds the
// credentials a send needs, the account's credits as SendGrid answers them, what
// the org's ledger counted sent by SendGrid in the last 24 hours, and the DNS
// records of the sending domain.
//
// GET /v1/email/sendgrid/status
func (h *Handlers) getSendGridStatus(ctx context.Context, in *SendGridStatusInput) (*SendGridStatusResult, error) {
	org, err := member(ctx)
	if err != nil {
		return nil, err
	}
	domain := ""
	if in != nil {
		domain = in.Domain
	}
	res, err := h.delivery.GetSendGridStatus(ctx, org, domain)
	if err != nil {
		return nil, failure(err)
	}
	return res, nil
}
