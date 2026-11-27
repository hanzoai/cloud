package email

import (
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	netmail "net/mail"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/lock"
	"github.com/hanzoai/cloud/sdk/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

var (
	ErrNotFound       = errors.New("email: message not found")
	ErrInvalidRequest = errors.New("email: invalid send request")
)

// SendFunc dispatches one message through a resolved provider with the extra MIME
// headers given and the tags the provider carries back on every event about it (an
// SES message tag, a SendGrid custom arg), and answers the provider's id for the
// message, which its events name it by.
type SendFunc func(ctx context.Context, provider string, creds map[string]string, from string, to []string, subject, htmlBody, textBody string, headers, tags map[string]string) (string, error)

// Service sends the org's commercial mail through SES or SendGrid and keeps the
// ledger of it in the org's Base (ledger.go).
type Service struct {
	resolver CredentialResolver
	sendFunc SendFunc
	log      luxlog.Logger
	now      func() time.Time
	// filing orders, in this process, the filers of one org's messages and counts; across
	// replicas the lock is the database's (hold).
	filing lock.Keyed
}

// NewDeliveryService creates the email delivery service.
func NewDeliveryService(resolver CredentialResolver, customSend SendFunc, log luxlog.Logger) *Service {
	s := &Service{resolver: resolver, sendFunc: customSend, log: log, now: time.Now}
	if s.sendFunc == nil {
		s.sendFunc = defaultSend
	}
	return s
}

// defaultSend delivers through Amazon SES (ses) or SendGrid (sendgrid), the two
// commercial rails. The transactional rails (Twilio, SMTP) are notify's and never
// this capability's.
func defaultSend(ctx context.Context, provider string, creds map[string]string, from string, to []string, subject, htmlBody, textBody string, headers, tags map[string]string) (string, error) {
	body := cmp.Or(htmlBody, textBody)
	merged := maps.Clone(creds)
	if merged == nil {
		merged = map[string]string{}
	}
	if from != "" {
		// "Name <address>" names the sender: each provider takes the two apart.
		if a, err := netmail.ParseAddress(from); err == nil && a.Name != "" {
			merged["from-email"], merged["from-name"] = a.Address, a.Name
		} else {
			merged["from-email"] = from
		}
	}
	switch provider {
	case "ses":
		c := NewSES(merged, to)
		if textBody != "" && htmlBody != "" {
			c.Alternative(textBody)
		}
		c.SetHeaders(headers)
		c.SetTags(tags)
		return c.SendWithID(ctx, subject, body)
	case "sendgrid":
		c := NewSendGrid(merged, to)
		if textBody != "" && htmlBody != "" {
			c.Alternative(textBody)
		}
		c.SetHeaders(headers)
		c.SetTags(tags)
		return c.SendWithID(ctx, subject, body)
	}
	return "", fmt.Errorf("email: %q is not a commercial rail; /v1/email sends by ses or sendgrid", provider)
}

// Outgoing is one message to one recipient, ready to leave: its body already
// carries its tagged links and its opt-out footer.
type Outgoing struct {
	ID, Provider          string
	Creds                 map[string]string
	From, To, Subject     string
	Text, HTML            string
	Headers               map[string]string
	Campaign, Idempotency string
}

// Route is the provider and credentials org's mail leaves by: the one pinned, else
// the org's own default (PUT /v1/email/provider), else ses, else sendgrid,
// whichever the org holds credentials for.
func (s *Service) Route(ctx context.Context, org, pinned string) (string, map[string]string, error) {
	if s.resolver == nil {
		return "", nil, ErrNoCredentials
	}
	return s.resolver.ResolveEmailCredentials(ctx, org, pinned)
}

// MessageID is the id a message to one recipient goes out under: derived from the
// request's idempotency key when it has one, so a retry names the same message,
// and fresh otherwise.
func MessageID(org, idempotency string) string {
	if idempotency == "" {
		return newID("msg")
	}
	sum := sha256.Sum256([]byte("idempotency\x00" + org + "\x00" + idempotency))
	return "msg_" + hex.EncodeToString(sum[:])[:24]
}

// Send sends one message and files it in the org's ledger. A message whose id the
// ledger already holds is not sent again: the record is answered as it stands, so a
// retried request sends once. The record is written before the provider is asked
// (queued), and again with what the provider answered, so the ledger holds a
// message that was cut off between the two as queued rather than not at all. A
// message the provider accepted is counted as sent and stated on the event stream.
func (s *Service) Send(ctx context.Context, org string, o *Outgoing) (*MessageRecord, error) {
	if err := ensure(ctx, org); err != nil {
		return nil, err
	}
	id := rid("message", o.ID)
	now := s.now().UTC()
	if o.Idempotency != "" {
		held, err := fetch(ctx, org, id)
		if err != nil {
			return nil, err
		}
		if held != nil && !(held.Status == string(StatusQueued) && now.Sub(unstamp(held.At)) > stalled) {
			return held.record(org), nil
		}
		if held != nil {
			// A send cut off between its queued record and the provider long ago:
			// the provider never had it, so the retry sends it, the record
			// stamped now so a second retry meanwhile finds it fresh.
			held.At = stamp(now)
			if _, err := upsert(ctx, org, "email", messages, []map[string]any{{"id": id, "at": held.At}}); err != nil {
				return nil, err
			}
			return s.dispatch(ctx, org, o, held, now)
		}
	}
	m := &stored{
		ID: id, Message: o.ID, Origin: originAPI, Provider: o.Provider, Status: string(StatusQueued),
		From: o.From, To: []string{o.To}, Recipient: digest(o.To), Subject: o.Subject, Campaign: o.Campaign,
		Tags: []string{}, Idempotency: o.Idempotency, At: stamp(now),
	}
	created, err := upsert(ctx, org, "email", messages, []map[string]any{{
		"id": id, "message": m.Message, "origin": m.Origin, "provider": m.Provider, "status": m.Status,
		"from": m.From, "to": m.To, "recipient": m.Recipient, "subject": m.Subject, "campaign": m.Campaign,
		"tags": m.Tags, "idempotency": m.Idempotency, "at": m.At,
	}})
	if err != nil {
		return nil, err
	}
	if !created[0] {
		// Another request with this key filed it first, and sends it.
		held, err := fetch(ctx, org, id)
		if err != nil || held == nil {
			return nil, cmp.Or(err, fmt.Errorf("%w: %s", ErrNotFound, o.ID))
		}
		return held.record(org), nil
	}
	return s.dispatch(ctx, org, o, m, now)
}

// stalled is how long a message may stay queued before a retry of its
// idempotency key takes it as cut off and sends it: far longer than any send
// takes, so a send still running is never sent twice.
const stalled = 10 * time.Minute

// dispatch hands the filed message m to its provider and records what it answered.
func (s *Service) dispatch(ctx context.Context, org string, o *Outgoing, m *stored, now time.Time) (*MessageRecord, error) {
	id := m.ID
	tags := map[string]string{"message": o.ID}
	if o.Campaign != "" {
		tags["campaign"] = o.Campaign
	}
	pid, sendErr := s.sendFunc(ctx, o.Provider, o.Creds, o.From, []string{o.To}, o.Subject, o.HTML, o.Text, o.Headers, tags)
	update := map[string]any{"id": id, "status": string(StatusSent)}
	m.Status = string(StatusSent)
	if pid != "" {
		m.Key = o.Provider + ":" + pid
		update["key"] = m.Key
	}
	if sendErr != nil {
		m.Status, m.Error = string(StatusFailed), clip(sendErr.Error(), 1000)
		update["status"], update["error"] = m.Status, m.Error
	}
	if _, err := upsert(ctx, org, "email", messages, []map[string]any{update}); err != nil && s.log != nil {
		// The provider has it; the ledger holds it as queued. The caller is told
		// what happened to the message, which is what it must not send twice.
		s.log.Warn("email: a message left and its record was not updated", "org", org, "message", o.ID, "err", err)
	}
	if sendErr != nil {
		return m.record(org), fmt.Errorf("email: delivery failed: %w", sendErr)
	}
	tally := counts{}
	tally.add(KindSent, o.Provider, o.Campaign, now, 1)
	if err := tally.write(ctx, org); err != nil && s.log != nil {
		s.log.Warn("email: a sent message was not counted", "org", org, "message", o.ID, "err", err)
	}
	e := &Event{ID: "sent:" + o.ID, Provider: o.Provider, Kind: KindSent, PID: pid, Message: o.ID, At: now, To: []string{o.To}, Campaign: o.Campaign}
	if err := state(ctx, org, []*client.EventIn{statement(e, m)}); err != nil && s.log != nil {
		s.log.Warn("email: a sent message was not stated on the event stream", "org", org, "message", o.ID, "err", err)
	}
	return m.record(org), nil
}

// GetMessage reads one of org's messages by our id for it.
func (s *Service) GetMessage(ctx context.Context, org, id string) (*MessageRecord, error) {
	id = strings.TrimSpace(id)
	if !ourID.MatchString(id) {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if err := ensure(ctx, org); err != nil {
		return nil, err
	}
	m, err := fetch(ctx, org, rid("message", id))
	if err != nil {
		return nil, err
	}
	if m == nil {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return m.record(org), nil
}

// The statuses a message may be listed by, and the providers.
var (
	statuses  = []string{string(StatusQueued), string(StatusSent), string(StatusFailed), string(StatusDelivered), string(StatusOpened), string(StatusClicked), string(StatusBounced), string(StatusComplained)}
	providers = []string{"ses", "sendgrid"}
)

// ListMessages reads a page of org's messages, newest first, narrowed as in asks.
// Every narrowing is an index of email_messages, and the page is read from where
// the cursor says the last one stopped, so the thousandth page costs what the
// first does.
func (s *Service) ListMessages(ctx context.Context, org string, in *ListMessagesInput) (*ListMessagesResult, error) {
	limit := 50
	if in.Limit > 0 {
		limit = min(in.Limit, 200)
	}
	var where []string
	if in.Status != "" {
		if !slices.Contains(statuses, in.Status) {
			return nil, zip.ErrBadRequest("email: status is one of " + strings.Join(statuses, ", "))
		}
		where = append(where, `status = "`+in.Status+`"`)
	}
	if in.Provider != "" {
		if !slices.Contains(providers, in.Provider) {
			return nil, zip.ErrBadRequest("email: provider is ses or sendgrid")
		}
		where = append(where, `provider = "`+in.Provider+`"`)
	}
	if in.Recipient != "" {
		where = append(where, `recipient = "`+digest(in.Recipient)+`"`)
	}
	for _, b := range []struct{ raw, op string }{{in.Since, ">="}, {in.Until, "<"}} {
		if b.raw == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, b.raw)
		if err != nil {
			return nil, zip.ErrBadRequest("email: since and until are RFC 3339 times")
		}
		where = append(where, `at `+b.op+` "`+stamp(t)+`"`)
	}
	if in.Cursor != "" {
		at, id, ok := cursorOf(in.Cursor)
		if !ok {
			return nil, zip.ErrBadRequest("email: cursor is the next of an earlier page")
		}
		where = append(where, `at <= "`+at+`" && (at < "`+at+`" || id < "`+id+`")`)
	}
	if err := ensure(ctx, org); err != nil {
		return nil, err
	}
	page, err := listFn(plane.For(ctx, org), &client.BaseListIn{
		Collection: messages, Filter: strings.Join(where, " && "), Sort: "-at,-id", PerPage: limit, SkipTotal: true,
	})
	if err != nil {
		return nil, fmt.Errorf("email: list messages: %w", err)
	}
	out := &ListMessagesResult{Messages: make([]*MessageRecord, 0, len(page.Items))}
	var last *stored
	for _, raw := range page.Items {
		m := &stored{}
		if err := json.Unmarshal(raw, m); err != nil {
			return nil, err
		}
		out.Messages = append(out.Messages, m.record(org))
		last = m
	}
	if last != nil && len(page.Items) == limit {
		out.Next = hex.EncodeToString([]byte(last.At)) + "." + last.ID
	}
	return out, nil
}

// cursorOf reads a page's next: the hex of the last message's date, a dot, and its
// record id. Both are checked to the letter, since they are spelled into a filter.
func cursorOf(c string) (string, string, bool) {
	h, id, ok := strings.Cut(c, ".")
	b, err := hex.DecodeString(h)
	if !ok || err != nil || len(id) != 15 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", "", false
	}
	at := string(b)
	if unstamp(at).IsZero() || stamp(unstamp(at)) != at {
		return "", "", false
	}
	return at, id, true
}

// The longest range a stats read covers, by interval.
const (
	maxDays  = 400
	maxHours = 31 * 24
)

// Stats counts org's mail over a range, from the tallies: in all, by provider, by
// campaign and by day or hour.
func (s *Service) Stats(ctx context.Context, org string, in *StatsInput) (*Stats, error) {
	span := cmp.Or(in.Interval, spanDay)
	step := 24 * time.Hour
	switch span {
	case spanDay:
	case spanHour:
		step = time.Hour
	default:
		return nil, zip.ErrBadRequest("email: interval is day or hour")
	}
	floor := func(t time.Time) time.Time {
		t = t.UTC()
		if span == spanDay {
			return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		}
		return t.Truncate(time.Hour)
	}
	until := s.now()
	if in.Until != "" {
		t, err := time.Parse(time.RFC3339, in.Until)
		if err != nil {
			return nil, zip.ErrBadRequest("email: until is an RFC 3339 time")
		}
		until = t
	}
	end := floor(until)
	if !end.Equal(until.UTC()) {
		end = end.Add(step)
	}
	start := end.Add(-30 * 24 * time.Hour)
	if span == spanHour {
		start = end.Add(-48 * time.Hour)
	}
	if in.Since != "" {
		t, err := time.Parse(time.RFC3339, in.Since)
		if err != nil {
			return nil, zip.ErrBadRequest("email: since is an RFC 3339 time")
		}
		start = floor(t)
	}
	if !start.Before(end) {
		return nil, zip.ErrBadRequest("email: since is before until")
	}
	if n := int(end.Sub(start) / step); (span == spanDay && n > maxDays) || (span == spanHour && n > maxHours) {
		return nil, zip.ErrBadRequest(fmt.Sprintf("email: at most %d days by day, %d hours by hour", maxDays, maxHours))
	}
	if in.Provider != "" && !slices.Contains(providers, in.Provider) {
		return nil, zip.ErrBadRequest("email: provider is ses or sendgrid")
	}
	campaign := strings.TrimSpace(in.Campaign)
	if campaign != "" && !campaignShape.MatchString(campaign) {
		return nil, zip.ErrBadRequest("email: campaign is up to 128 letters, digits, '-', '_', '.' or ' '")
	}
	if err := ensure(ctx, org); err != nil {
		return nil, err
	}

	where := `span = "` + span + `" && at >= "` + stamp(start) + `" && at < "` + stamp(end) + `"`
	if in.Provider != "" {
		where += ` && provider = "` + in.Provider + `"`
	}
	rows, err := readTallies(ctx, org, where+` && campaign = "`+campaign+`"`)
	if err != nil {
		return nil, err
	}
	out := &Stats{Since: start, Until: end, Interval: span, Provider: in.Provider, Campaign: campaign,
		Providers: []Part{}, Campaigns: []Part{}, Series: []Point{}}
	byProvider := map[string]*Tally{}
	byAt := map[string]*Tally{}
	for _, r := range rows {
		out.Totals.add(r)
		if byProvider[r.Provider] == nil {
			byProvider[r.Provider] = &Tally{}
		}
		byProvider[r.Provider].add(r)
		if byAt[r.At] == nil {
			byAt[r.At] = &Tally{}
		}
		byAt[r.At].add(r)
	}
	for _, p := range slices.Sorted(maps.Keys(byProvider)) {
		out.Providers = append(out.Providers, Part{Name: p, Tally: *byProvider[p]})
	}
	for t := start; t.Before(end); t = t.Add(step) {
		p := Point{At: t}
		if c := byAt[stamp(t)]; c != nil {
			p.Tally = *c
		}
		out.Series = append(out.Series, p)
	}
	if campaign == "" {
		rows, err := readTallies(ctx, org, where+` && campaign != ""`)
		if err != nil {
			return nil, err
		}
		byCampaign := map[string]*Tally{}
		for _, r := range rows {
			if byCampaign[r.Campaign] == nil {
				byCampaign[r.Campaign] = &Tally{}
			}
			byCampaign[r.Campaign].add(r)
		}
		for c, t := range byCampaign {
			out.Campaigns = append(out.Campaigns, Part{Name: c, Tally: *t})
		}
		slices.SortFunc(out.Campaigns, func(a, b Part) int {
			return cmp.Or(cmp.Compare(b.Tally.Sent, a.Tally.Sent), strings.Compare(a.Name, b.Name))
		})
		out.Campaigns = out.Campaigns[:min(len(out.Campaigns), maxCampaigns)]
	}
	return out, nil
}

// maxCampaigns is the most campaigns one stats answer names, the most sent first.
const maxCampaigns = 200

// sentSince is how many messages org sent by provider in the hours from since,
// from the ledger's hourly tallies.
func sentSince(ctx context.Context, org, provider string, since time.Time) (int, error) {
	if err := ensure(ctx, org); err != nil {
		return 0, err
	}
	rows, err := readTallies(ctx, org, `span = "hour" && campaign = "" && provider = "`+provider+`" && at >= "`+stamp(since.UTC().Truncate(time.Hour))+`"`)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, r := range rows {
		n += int(r.Sent)
	}
	return n, nil
}

// GetSESStatus reports the org's Amazon SES configuration: whether it holds
// credentials, the account's sending limits as SES answers them, what the ledger
// counted sent by SES in the last 24 hours, and the DNS records of the domain.
func (s *Service) GetSESStatus(ctx context.Context, org, domain string) (*SESStatusResult, error) {
	out := &SESStatusResult{Org: org, Region: "us-east-1"}
	if p, _, err := s.Route(ctx, org, ""); err == nil {
		out.ActiveProvider = p
	}
	creds, held := s.held(ctx, org, "ses")
	if held {
		out.Configured = true
		out.Region = cmp.Or(strings.TrimSpace(creds["region"]), out.Region)
		out.FromEmail = strings.TrimSpace(creds["from-email"])
		out.FromName = strings.TrimSpace(creds["from-name"])
		out.ConfigurationSet = NewSES(creds, nil).configurationSet
		acct, err := NewSES(creds, nil).Account(ctx)
		if err != nil {
			out.QuotaError = s.quiet(org, "ses", err)
		} else {
			out.Sandbox = !acct.ProductionAccessEnabled
			out.SendingEnabled = acct.SendingEnabled
			out.Max24HourSend = acct.SendQuota.Max24HourSend
			out.MaxSendRate = acct.SendQuota.MaxSendRate
			out.ProviderSentLast24Hours = acct.SendQuota.SentLast24Hours
		}
	}
	out.Domain = domainOf(domain, out.FromEmail, "hanzo.ai")
	out.MailFromDomain = "bounce." + out.Domain
	out.DNSRecords = BuildSESDNSRecords(out.Domain, out.Region)
	n, err := sentSince(ctx, org, "ses", s.now().Add(-23*time.Hour))
	if err != nil {
		return nil, err
	}
	out.SentLast24Hours = n
	return out, nil
}

// GetSendGridStatus reports the org's SendGrid configuration: whether it holds
// credentials, the account's credits as SendGrid answers them, what the ledger
// counted sent by SendGrid in the last 24 hours, and the DNS records of the domain.
func (s *Service) GetSendGridStatus(ctx context.Context, org, domain string) (*SendGridStatusResult, error) {
	out := &SendGridStatusResult{Org: org, SeparateFromTwilioNotify: true}
	if p, _, err := s.Route(ctx, org, ""); err == nil {
		out.ActiveProvider = p
	}
	creds, held := s.held(ctx, org, "sendgrid")
	if held {
		out.Configured = true
		out.FromEmail = strings.TrimSpace(creds["from-email"])
		out.FromName = strings.TrimSpace(creds["from-name"])
		out.ReplyTo = strings.TrimSpace(creds["reply-to"])
		out.IPPool = strings.TrimSpace(creds["ip-pool"])
		out.ASMGroupID = strings.TrimSpace(creds["asm-group-id"])
		credit, err := NewSendGrid(creds, nil).Credits(ctx)
		if err != nil {
			out.QuotaError = s.quiet(org, "sendgrid", err)
		} else {
			out.CreditsTotal, out.CreditsRemaining, out.CreditsUsed = credit.Total, credit.Remain, credit.Used
		}
	}
	out.Domain = domainOf(domain, out.FromEmail, "mail.hanzo.ai")
	out.DNSRecords = BuildSendGridDNSRecords(out.Domain)
	n, err := sentSince(ctx, org, "sendgrid", s.now().Add(-23*time.Hour))
	if err != nil {
		return nil, err
	}
	out.SentLast24Hours = n
	return out, nil
}

// quiet is what a member is told of a provider read that failed: that it failed and
// to check the credentials, never the provider's own words, which name the account
// and its keys. The words go to the log.
func (s *Service) quiet(org, provider string, err error) string {
	if s.log != nil {
		s.log.Warn("email: a provider read failed", "org", org, "provider", provider, "err", err)
	}
	return provider + ": the account could not be read; check the org's " + provider + " credentials"
}

// held is org's credentials for provider, and whether it holds every one the
// provider cannot send without.
func (s *Service) held(ctx context.Context, org, provider string) (map[string]string, bool) {
	if s.resolver == nil {
		return nil, false
	}
	_, creds, err := s.resolver.ResolveEmailCredentials(ctx, org, provider)
	return creds, err == nil
}

// domainOf is the domain asked for, else the sender's, else fallback.
func domainOf(asked, from, fallback string) string {
	d := strings.ToLower(strings.TrimSpace(asked))
	if d == "" {
		if at := strings.LastIndexByte(from, '@'); at != -1 {
			d = strings.ToLower(from[at+1:])
		}
	}
	if at := strings.LastIndexByte(d, '@'); at != -1 {
		d = d[at+1:]
	}
	return cmp.Or(d, fallback)
}

// WebhookCredentials reads the credentials a provider's webhook is verified under.
func (s *Service) WebhookCredentials(ctx context.Context, org, provider string) (map[string]string, error) {
	if s.resolver == nil {
		return nil, ErrNoCredentials
	}
	_, creds, err := s.resolver.ResolveEmailCredentials(ctx, org, provider)
	return creds, err
}

// SetDefault sets the provider org's mail leaves by when a request pins none.
func (s *Service) SetDefault(ctx context.Context, org, provider string) error {
	if !slices.Contains(providers, provider) {
		return zip.ErrBadRequest("email: provider is ses or sendgrid")
	}
	if s.resolver == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "email: credentials are not available on this deployment")
	}
	return s.resolver.SetDefault(ctx, org, provider)
}

func newID(prefix string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return prefix + "_" + hex.EncodeToString(b)
}
