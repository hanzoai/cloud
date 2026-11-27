package email

// events.go — what a provider says became of a message, made one kind of thing.
//
// SES (through SNS) and SendGrid each describe a delivery, an open, a click, a
// bounce, a complaint and an unsubscribe in their own words. Each is read here into
// one Event, and Record files a delivery of them: one record per event however often
// the provider sends it, the message it names moved forward, the counts the stats
// read, the event stream, the suppression list and the CRM.
//
// The provider is the caller, so a message we never sent is accepted too: an event
// for a message the org sent from its own tools, straight through its SES or
// SendGrid account, files that message under the provider's id for it, so mail sent
// around this API is counted with mail sent through it.

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/crm"
	"github.com/hanzoai/cloud/client"
	eventpeer "github.com/hanzoai/cloud/client/event"
	"github.com/hanzoai/cloud/sdk/plane"
	"golang.org/x/sync/errgroup"
)

// The kinds of event, in the ledger's words. The first seven are what the stats
// count and the event stream carries, as "email_<kind>".
const (
	KindSent         = "sent"
	KindDelivered    = "delivered"
	KindOpened       = "opened"
	KindClicked      = "clicked"
	KindBounced      = "bounced"
	KindComplained   = "complained"
	KindUnsubscribed = "unsubscribed"
	// KindDeferred is a delivery the receiving server put off; the provider tries again.
	KindDeferred = "deferred"
	// KindRejected is a message the provider refused to send (SES Reject, RenderingFailure).
	KindRejected = "rejected"
)

// Event is one provider event, normalized.
type Event struct {
	// ID is the provider and its own id for the event: "ses:<SNS MessageId>",
	// "sendgrid:<sg_event_id>". A delivery replayed carries the same one.
	ID string
	// Provider is ses or sendgrid.
	Provider string
	// Kind is what happened (KindSent…KindRejected).
	Kind string
	// PID is the provider's id for the message the event is about.
	PID string
	// Message is our id for the message, when it carried one (an SES message tag or
	// a SendGrid custom arg named "message", which every message we send carries).
	Message string
	// At is when it happened, and Sent when the message was sent, if the event says.
	At, Sent time.Time
	// To is the recipients the event is about. It is used and never kept here: the
	// event record holds its digest, the stream the person it is.
	To []string
	// From and Subject are the message's, when the event carries them (SES does).
	From, Subject string
	// Campaign is the campaign the sender tagged the message with, and Tags its
	// other tags: SendGrid's categories, SES's own message tags as name=value.
	Campaign string
	Tags     []string
	// Link is the address a click went to, as the sender wrote it.
	Link string
	// Bounce is hard or soft, for a bounce; Reason says why it bounced, was
	// complained of, deferred or rejected.
	Bounce, Reason string
	// Suppress says the recipient goes on the org's suppression list: a hard
	// bounce, a complaint, an unsubscribe.
	Suppress bool
}

// key is the message's provider and the provider's id for it.
func (e *Event) key() string {
	if e.PID == "" {
		return ""
	}
	return e.Provider + ":" + e.PID
}

// ref is our id for the message the event is about: the one it carried, else the
// one its provider id is filed under. "" for an event that names no message.
func (e *Event) ref() string {
	if e.Message != "" {
		return e.Message
	}
	if e.PID != "" {
		return outsideID(e.key())
	}
	return ""
}

// ourID is the shape of the ids we give messages.
var ourID = regexp.MustCompile(`^msg_[0-9a-f]{24}$`)

// outsideID is the id a message we did not send is filed under: derived from the
// provider's id for it, so every event about it finds the one record.
func outsideID(key string) string {
	sum := sha256.Sum256([]byte("provider\x00" + key))
	return "msg_" + hex.EncodeToString(sum[:])[:24]
}

// clip cuts s to n runes.
func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// ---- SES, through SNS ----

// sesNote is an SES event as its configuration set publishes it to SNS, and as the
// older identity notifications send a bounce, a complaint or a delivery.
type sesNote struct {
	EventType        string `json:"eventType"`
	NotificationType string `json:"notificationType"`
	Mail             struct {
		MessageID     string              `json:"messageId"`
		Timestamp     string              `json:"timestamp"`
		Source        string              `json:"source"`
		Destination   []string            `json:"destination"`
		Tags          map[string][]string `json:"tags"`
		CommonHeaders struct {
			Subject string `json:"subject"`
		} `json:"commonHeaders"`
	} `json:"mail"`
	Bounce *struct {
		BounceType        string    `json:"bounceType"`
		BounceSubType     string    `json:"bounceSubType"`
		BouncedRecipients []sesWhom `json:"bouncedRecipients"`
		Timestamp         string    `json:"timestamp"`
	} `json:"bounce"`
	Complaint *struct {
		ComplaintFeedbackType string    `json:"complaintFeedbackType"`
		ComplainedRecipients  []sesWhom `json:"complainedRecipients"`
		Timestamp             string    `json:"timestamp"`
	} `json:"complaint"`
	Delivery *struct {
		Timestamp  string   `json:"timestamp"`
		Recipients []string `json:"recipients"`
	} `json:"delivery"`
	Open *struct {
		Timestamp string `json:"timestamp"`
	} `json:"open"`
	Click *struct {
		Timestamp string `json:"timestamp"`
		Link      string `json:"link"`
	} `json:"click"`
	Reject *struct {
		Reason string `json:"reason"`
	} `json:"reject"`
	Failure *struct {
		ErrorMessage string `json:"errorMessage"`
	} `json:"failure"`
	DeliveryDelay *struct {
		Timestamp string `json:"timestamp"`
		DelayType string `json:"delayType"`
	} `json:"deliveryDelay"`
}

type sesWhom struct {
	EmailAddress string `json:"emailAddress"`
}

// errIgnored is an event of a kind the ledger does not keep (an SES Subscription
// event, a SendGrid resubscribe); the delivery is answered and nothing is recorded.
var errIgnored = errors.New("email: an event the ledger does not keep")

// sesEvent reads the SES event a verified SNS notification carries. id is the
// notification's own MessageId, which SNS keeps across its retries, and at its
// Timestamp, read when the event states no time of its own.
func sesEvent(id, message string, at time.Time) (*Event, error) {
	var n sesNote
	if err := json.Unmarshal([]byte(message), &n); err != nil {
		return nil, errors.New("email: the SNS message is not an SES event")
	}
	e := &Event{
		ID: "ses:" + id, Provider: "ses", PID: clip(n.Mail.MessageID, 256), To: n.Mail.Destination,
		From: clip(n.Mail.Source, 320), Subject: clip(n.Mail.CommonHeaders.Subject, 998),
	}
	if id == "" || e.PID == "" {
		return nil, errors.New("email: an SES event without its ids")
	}
	e.Sent = when(n.Mail.Timestamp, time.Time{})
	stamp := ""
	switch cmp.Or(n.EventType, n.NotificationType) {
	case "Send":
		e.Kind = KindSent
	case "Delivery":
		e.Kind = KindDelivered
		if d := n.Delivery; d != nil {
			stamp = d.Timestamp
			if len(d.Recipients) > 0 {
				e.To = d.Recipients
			}
		}
	case "Open":
		e.Kind = KindOpened
		if n.Open != nil {
			stamp = n.Open.Timestamp
		}
	case "Click":
		e.Kind = KindClicked
		if n.Click != nil {
			stamp, e.Link = n.Click.Timestamp, n.Click.Link
		}
	case "Bounce":
		e.Kind, e.Bounce = KindBounced, "soft"
		if b := n.Bounce; b != nil {
			stamp = b.Timestamp
			if b.BounceType == "Permanent" {
				e.Bounce = "hard"
			}
			e.Reason = strings.TrimSpace(b.BounceType + " " + b.BounceSubType)
			if to := whom(b.BouncedRecipients); len(to) > 0 {
				e.To = to
			}
		}
		e.Suppress = e.Bounce == "hard"
	case "Complaint":
		e.Kind, e.Suppress = KindComplained, true
		if c := n.Complaint; c != nil {
			stamp, e.Reason = c.Timestamp, c.ComplaintFeedbackType
			if to := whom(c.ComplainedRecipients); len(to) > 0 {
				e.To = to
			}
		}
	case "DeliveryDelay":
		e.Kind = KindDeferred
		if d := n.DeliveryDelay; d != nil {
			stamp, e.Reason = d.Timestamp, d.DelayType
		}
	case "Reject":
		e.Kind = KindRejected
		if n.Reject != nil {
			e.Reason = n.Reject.Reason
		}
	case "Rendering Failure", "RenderingFailure":
		e.Kind = KindRejected
		if n.Failure != nil {
			e.Reason = n.Failure.ErrorMessage
		}
	default:
		return nil, errIgnored
	}
	e.At = when(stamp, cmp.Or(e.Sent, at))
	e.Reason = clip(e.Reason, 256)
	// Our own tags say which message and campaign; the configuration set and every
	// tag the sender set are kept as the message's tags. SES's own (ses:source-ip,
	// ses:caller-identity …) say how it was sent, not what it was.
	for name, vals := range n.Mail.Tags {
		if len(vals) == 0 {
			continue
		}
		switch {
		case name == "message":
			if ourID.MatchString(vals[0]) {
				e.Message = vals[0]
			}
		case name == "campaign":
			e.Campaign = clip(vals[0], 128)
		case name == "ses:configuration-set" || !strings.HasPrefix(name, "ses:"):
			e.Tags = append(e.Tags, clip(name+"="+vals[0], 128))
		}
	}
	slices.Sort(e.Tags)
	e.Tags = e.Tags[:min(len(e.Tags), 10)]
	return e, nil
}

func whom(rs []sesWhom) []string {
	var out []string
	for _, r := range rs {
		if r.EmailAddress != "" {
			out = append(out, r.EmailAddress)
		}
	}
	return out
}

// when reads an RFC 3339 time, else fallback.
func when(s string, fallback time.Time) time.Time {
	if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(s)); err == nil {
		return t.UTC()
	}
	return fallback
}

// ---- SendGrid ----

// categories is SendGrid's category field, which is a string when the message had
// one category and an array when it had several.
type categories []string

func (c *categories) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		if one != "" {
			*c = categories{one}
		}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*c = many
	return nil
}

// sgEvent is one event of a SendGrid Event Webhook delivery. A message's custom args
// arrive as fields of the event beside SendGrid's own: message and campaign are the
// two every message we send carries.
type sgEvent struct {
	Email       string     `json:"email"`
	Timestamp   int64      `json:"timestamp"`
	Event       string     `json:"event"`
	SGMessageID string     `json:"sg_message_id"`
	SGEventID   string     `json:"sg_event_id"`
	Reason      string     `json:"reason"`
	Status      string     `json:"status"`
	Type        string     `json:"type"`
	URL         string     `json:"url"`
	Category    categories `json:"category"`
	Message     arg        `json:"message"`
	Campaign    arg        `json:"campaign"`
	// SendGrid's own campaigns name the message's campaign here.
	SingleSend arg `json:"singlesend_name"`
	Marketing  arg `json:"marketing_campaign_name"`
}

// arg is a custom arg as text, whatever JSON the sender gave it: a campaign tagged
// 2024 is the campaign "2024", and an object is nothing, not a refused event.
type arg string

func (a *arg) UnmarshalJSON(b []byte) error {
	var s string
	if json.Unmarshal(b, &s) == nil {
		*a = arg(s)
		return nil
	}
	if t := strings.TrimSpace(string(b)); t != "" && (t[0] == '-' || (t[0] >= '0' && t[0] <= '9') || t == "true" || t == "false") {
		*a = arg(t)
	}
	return nil
}

// defaultCategory is the category a message we send through SendGrid carries when
// the org named none (sendgrid.go); it names no campaign.
const defaultCategory = "hanzo-crm-outreach"

// sendGridEvent reads one event of a verified delivery. raw is the event as it
// arrived, whose digest is its id when SendGrid gave it none.
func sendGridEvent(raw json.RawMessage) (*Event, error) {
	var g sgEvent
	if err := json.Unmarshal(raw, &g); err != nil {
		return nil, errors.New("email: a SendGrid event that does not read")
	}
	id := strings.TrimSpace(g.SGEventID)
	if id == "" {
		sum := sha256.Sum256(raw)
		id = hex.EncodeToString(sum[:16])
	}
	pid, _, _ := strings.Cut(strings.TrimSpace(g.SGMessageID), ".")
	e := &Event{ID: "sendgrid:" + clip(id, 128), Provider: "sendgrid", PID: clip(pid, 256), Link: g.URL}
	if g.Email != "" {
		e.To = []string{g.Email}
	}
	if g.Timestamp > 0 {
		e.At = time.Unix(g.Timestamp, 0).UTC()
	} else {
		e.At = time.Now().UTC()
	}
	switch strings.ToLower(strings.TrimSpace(g.Event)) {
	case "processed":
		e.Kind, e.Sent = KindSent, e.At
	case "delivered":
		e.Kind = KindDelivered
	case "open":
		e.Kind = KindOpened
	case "click":
		e.Kind = KindClicked
	case "bounce":
		// A "blocked" bounce is the receiving server refusing for now, not an
		// address that does not exist: soft, and it puts no one on the list.
		e.Kind, e.Bounce = KindBounced, "hard"
		if g.Type == "blocked" {
			e.Bounce = "soft"
		}
		e.Reason = cmp.Or(g.Reason, g.Status)
		e.Suppress = e.Bounce == "hard"
	case "dropped":
		// SendGrid would not send. Only a drop about the ADDRESS is a hard bounce
		// that puts it on the list: one SendGrid holds as bounced, unsubscribed or
		// reported, or one that is not an address. Any other (a plan's quota, the
		// content's spam score, a bad header) is about the send, and suppresses no one.
		e.Reason = "dropped: " + g.Reason
		if addressDrop(g.Reason) {
			e.Kind, e.Bounce, e.Suppress = KindBounced, "hard", true
		} else {
			e.Kind = KindRejected
		}
	case "spamreport":
		e.Kind, e.Suppress, e.Reason = KindComplained, true, "abuse"
	case "unsubscribe", "group_unsubscribe":
		e.Kind, e.Suppress = KindUnsubscribed, true
	case "deferred":
		e.Kind, e.Reason = KindDeferred, g.Reason
	default:
		return nil, errIgnored
	}
	e.Reason = clip(e.Reason, 256)
	if ourID.MatchString(string(g.Message)) {
		e.Message = string(g.Message)
	}
	for _, c := range g.Category {
		if c = clip(c, 128); c != "" && c != defaultCategory && len(e.Tags) < 10 {
			e.Tags = append(e.Tags, c)
		}
	}
	e.Campaign = clip(string(cmp.Or(g.Campaign, g.SingleSend, g.Marketing)), 128)
	if e.Campaign == "" && len(e.Tags) > 0 {
		e.Campaign = e.Tags[0]
	}
	return e, nil
}

// addressDrops are SendGrid's drop reasons that are about the address itself.
var addressDrops = []string{"bounced address", "unsubscribed address", "spam reporting address", "invalid"}

// addressDrop is whether a SendGrid drop reason is about the address.
func addressDrop(reason string) bool {
	return slices.Contains(addressDrops, strings.ToLower(strings.TrimSpace(reason)))
}

// ---- filing ----

// The plane beyond the Base, variables so a test can see what reaches it. Only a
// test reassigns them.
var captureFn = eventpeer.EventCapture

// hold takes org's filing lock, or answers why it could not before ctx ended; the
// func it answers releases it.
//
// It makes the read-and-write of an org's messages and counts one at a time on every
// replica: two deliveries about one message, to two pods, must not each move it from the
// same read. The records are Base's and this package has no store of its own, so the
// lock is held on the process's lock pool (locus.Locks) under the org's name. It is
// held for that alone, never across the stream, the suppression list or the CRM, and a
// delivery whose context ends stops waiting for it.
func (s *Service) hold(ctx context.Context, org string) (func(), error) {
	return s.filing.Across(ctx, nil, "filing:"+org)
}

// Record files a delivery of provider events for org and answers how many it holds.
//
// Each event is one record, keyed by the provider's own id for it, so a delivery
// the provider sends twice is filed once. The first time an event is filed its
// effects follow. A hard bounce, a complaint or an unsubscribe puts the recipient
// on the org's suppression list FIRST, before anything else can fail: putting an
// address there twice is putting it there once, and a bounce must never be lost to
// a stream or a ledger that did not answer. Then the message it names moves
// forward (and is filed, if we never sent it), what the message reached for the
// first time is counted, it is stated on the event stream, and the CRM is told
// (once per event). Only then is the event settled. A delivery that fails part way
// is answered with the error, so the provider sends it again and the events not
// yet settled are taken up where they stopped; the stream collapses a restatement
// on the event's id.
func (s *Service) Record(ctx context.Context, org string, evs []*Event) (int, error) {
	if len(evs) == 0 {
		return 0, nil
	}
	if err := ensure(ctx, org); err != nil {
		return 0, err
	}

	seen := map[string]bool{}
	var batch []*Event
	for _, e := range evs {
		if !seen[e.ID] {
			seen[e.ID] = true
			batch = append(batch, e)
		}
	}
	work, fresh, err := s.unsettled(ctx, org, batch)
	if err != nil || len(work) == 0 {
		return len(batch), err
	}
	slices.SortStableFunc(work, func(a, b *Event) int { return a.At.Compare(b.At) })
	for _, e := range work {
		if err := suppress(ctx, org, e); err != nil {
			return 0, err
		}
	}

	stated, err := s.file(ctx, org, work, fresh)
	if err != nil {
		return 0, err
	}
	if err := state(ctx, org, stated); err != nil {
		return 0, err
	}
	for _, e := range work {
		s.tell(ctx, org, e)
	}
	settle := make([]map[string]any, 0, len(work))
	for _, e := range work {
		settle = append(settle, map[string]any{"id": rid("event", e.ID), "settled": true})
	}
	if _, err := upsert(ctx, org, "webhook", events, settle); err != nil {
		return 0, err
	}
	return len(batch), nil
}

// file moves the messages work names and counts what each reached for the first
// time, under org's filing lock, and answers what the stream is to be told. A
// retry after the messages moved finds nothing left to count, so a count is never
// made twice; an event that names no message is counted only when fresh, the
// first time it was filed.
func (s *Service) file(ctx context.Context, org string, work []*Event, fresh map[string]bool) ([]*client.EventIn, error) {
	release, err := s.hold(ctx, org)
	if err != nil {
		return nil, err
	}
	defer release()

	refs := make([]string, 0, len(work))
	for _, e := range work {
		if r := e.ref(); r != "" {
			refs = append(refs, rid("message", r))
		}
	}
	held, err := load[stored](ctx, org, messages, "", slices.Compact(slices.Sorted(slices.Values(refs))))
	if err != nil {
		return nil, err
	}
	moved := map[string]map[string]any{}
	tally := counts{}
	var stated []*client.EventIn
	for _, e := range work {
		m := s.move(e, held, moved, tally, fresh[e.ID])
		if e.Kind == KindDeferred || e.Kind == KindRejected || (e.Kind == KindSent && m != nil && m.Origin == originAPI) {
			continue
		}
		stated = append(stated, statement(e, m))
	}
	records := make([]map[string]any, 0, len(moved))
	for _, r := range moved {
		records = append(records, r)
	}
	if _, err := upsert(ctx, org, "webhook", messages, records); err != nil {
		return nil, err
	}
	return stated, tally.write(ctx, org)
}

// unsettled files every event of the batch and answers those whose effects are
// still to follow: each one just filed (fresh), and each one filed before whose
// effects a failed delivery left unfinished.
func (s *Service) unsettled(ctx context.Context, org string, batch []*Event) ([]*Event, map[string]bool, error) {
	records := make([]map[string]any, 0, len(batch))
	for _, e := range batch {
		r := map[string]any{
			"id": rid("event", e.ID), "event": e.Kind, "provider": e.Provider, "key": e.key(),
			"message": e.ref(), "campaign": e.Campaign, "tags": orEmpty(e.Tags), "link": clip(trueLink(e.Link), 2000),
			"bounce": e.Bounce, "reason": scrub(e.Reason), "at": stamp(e.At),
		}
		if len(e.To) > 0 {
			r["recipient"], r["person"] = digest(e.To[0]), crm.PersonID(e.To[0])
		}
		records = append(records, r)
	}
	created, err := upsert(ctx, org, "webhook", events, records)
	if err != nil {
		return nil, nil, err
	}
	fresh := map[string]bool{}
	var work, again []*Event
	for i, e := range batch {
		if created[i] {
			work = append(work, e)
			fresh[e.ID] = true
		} else {
			again = append(again, e)
		}
	}
	if len(again) == 0 {
		return work, fresh, nil
	}
	ids := make([]string, len(again))
	for i, e := range again {
		ids[i] = rid("event", e.ID)
	}
	open, err := load[struct{}](ctx, org, events, "settled = false", ids)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range again {
		if _, ok := open[rid("event", e.ID)]; ok {
			work = append(work, e)
		}
	}
	return work, fresh, nil
}

// The origins of a message.
const (
	originAPI      = "api"      // sent through POST /v1/email
	originProvider = "provider" // known only from its provider's events
)

// move applies e to the message it names, held as read or as moved by an earlier
// event of the batch, and answers the message. A message we hold no record of is
// filed now, from what the event says of it, and counted as sent. Every first time
// it reaches a state is counted. An event that names no message is counted as it
// stands, the first time it is filed (fresh) and never again on a retry.
func (s *Service) move(e *Event, held map[string]*stored, moved map[string]map[string]any, tally counts, fresh bool) *stored {
	ref := e.ref()
	if ref == "" {
		if c := counter(e.Kind); c != "" && fresh {
			tally.add(c, e.Provider, e.Campaign, e.At, 1)
		}
		return nil
	}
	id := rid("message", ref)
	m, ok := held[id]
	change := moved[id]
	if !ok {
		sent := cmp.Or(e.Sent, e.At)
		m = &stored{
			ID: id, Message: ref, Key: e.key(), Origin: originProvider, Provider: e.Provider, Status: string(StatusSent),
			From: e.From, To: orEmpty(e.To), Subject: e.Subject, Campaign: e.Campaign, Tags: orEmpty(e.Tags), At: stamp(sent),
		}
		if len(e.To) > 0 {
			m.Recipient = digest(e.To[0])
		}
		held[id] = m
		change = map[string]any{
			"id": id, "message": m.Message, "key": m.Key, "origin": m.Origin, "provider": m.Provider, "status": m.Status,
			"from": m.From, "to": m.To, "recipient": m.Recipient, "subject": m.Subject, "campaign": m.Campaign,
			"tags": m.Tags, "at": m.At,
		}
		moved[id] = change
		tally.add(KindSent, m.Provider, m.Campaign, sent, 1)
	}
	if change == nil {
		change = map[string]any{"id": id}
	}
	if m.Campaign == "" && e.Campaign != "" {
		m.Campaign, change["campaign"] = e.Campaign, e.Campaign
	}
	if m.Subject == "" && e.Subject != "" {
		m.Subject, change["subject"] = e.Subject, e.Subject
	}
	at := stamp(e.At)
	first := func(field *string, name string) bool {
		if *field != "" {
			return false
		}
		*field, change[name] = at, at
		return true
	}
	var reached bool
	switch e.Kind {
	case KindSent:
		if e.Sent.Before(unstamp(m.At)) && m.Origin == originProvider {
			m.At, change["at"] = stamp(e.Sent), stamp(e.Sent)
		}
	case KindDelivered:
		reached = first(&m.Delivered, "delivered")
	case KindOpened:
		reached = first(&m.Opened, "opened")
	case KindClicked:
		reached = first(&m.Clicked, "clicked")
	case KindBounced:
		if reached = first(&m.Bounced, "bounced"); reached {
			m.Bounce, change["bounce"] = e.Bounce, e.Bounce
			m.Error, change["error"] = e.Reason, e.Reason
		}
	case KindComplained:
		reached = first(&m.Complained, "complained")
	case KindUnsubscribed:
		reached = first(&m.Unsubscribed, "unsubscribed")
	case KindDeferred, KindRejected:
		m.Error, change["error"] = clip(e.Kind+": "+e.Reason, 256), clip(e.Kind+": "+e.Reason, 256)
	}
	if reached {
		tally.add(e.Kind, m.Provider, m.Campaign, e.At, 1)
	}
	if status := string(m.status()); status != m.Status {
		m.Status, change["status"] = status, status
	}
	if len(change) > 1 {
		moved[id] = change
	}
	return m
}

// counter is the tally an event of kind counts in, "" for one no tally keeps.
func counter(kind string) string {
	if slices.Contains(counters, kind) {
		return kind
	}
	return ""
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// statement is what the event stream is told of e: its kind as email_<kind>, the
// person it concerned as the CRM names them (never the address), and the facts a
// funnel splits on. m is the message it is about, nil when it names none.
func statement(e *Event, m *stored) *client.EventIn {
	in := &client.EventIn{ID: rid("event", e.ID), Name: "email_" + e.Kind, Product: "email", At: e.At.Format(time.RFC3339Nano)}
	if len(e.To) > 0 {
		in.Subject = crm.PersonID(e.To[0])
	}
	campaign := e.Campaign
	if m != nil {
		campaign = cmp.Or(m.Campaign, campaign)
	}
	for _, a := range []client.Signal{
		{Name: "provider", Value: e.Provider},
		{Name: "message", Value: e.ref()},
		{Name: "provider_message_id", Value: e.PID},
		{Name: "campaign", Value: campaign},
		{Name: "category", Value: strings.Join(e.Tags, ",")},
		{Name: "link", Value: clip(trueLink(e.Link), 2000)},
		{Name: "bounce", Value: e.Bounce},
		{Name: "reason", Value: e.Reason},
	} {
		// The stream is read by every lens the org has: no address rides it, in a
		// diagnostic, a campaign name or anywhere else a sender put one.
		if a.Value = scrub(a.Value); a.Value != "" {
			in.Attributes = append(in.Attributes, a)
		}
	}
	return in
}

// addresses is anything shaped like an email address.
var addresses = regexp.MustCompile(`[^\s<>()\[\],;:"'@]+@[^\s<>()\[\],;:"'@]+\.[A-Za-z]{2,}`)

// scrub replaces every address in s with "<address>".
func scrub(s string) string { return addresses.ReplaceAllString(s, "<address>") }

// stating bounds the statements in flight for one delivery, and stateBudget each.
const (
	stating     = 16
	stateBudget = 5 * time.Second
)

// state puts every statement on org's event stream, a few at a time, and answers
// the first that did not land.
func state(ctx context.Context, org string, ins []*client.EventIn) error {
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(stating)
	for _, in := range ins {
		g.Go(func() error {
			c, cancel := context.WithTimeout(plane.For(gctx, org), stateBudget)
			defer cancel()
			_, err := captureFn(c, in)
			return err
		})
	}
	return g.Wait()
}

// suppress puts the recipients of a hard bounce, a complaint or an unsubscribe on
// the org's suppression list. A list that cannot answer fails the delivery, so the
// provider sends it again: an address that bounced is never mailed again because a
// write was lost, and putting it on the list twice is putting it there once.
func suppress(ctx context.Context, org string, e *Event) error {
	if !e.Suppress {
		return nil
	}
	reason := e.Kind
	switch e.Kind {
	case KindBounced:
		reason = strings.TrimSpace("bounce: " + e.Reason)
	case KindComplained:
		reason = strings.TrimSpace("complaint: " + e.Reason)
	case KindUnsubscribed:
		reason = "unsubscribed at " + e.Provider
	}
	for _, addr := range e.To {
		if _, err := suppressFn(plane.For(ctx, org), &client.Addressee{Org: org, Channel: "email", Address: addr, Reason: reason}); err != nil {
			return fmt.Errorf("email: the suppression list did not take a %s: %w", e.Kind, err)
		}
	}
	return nil
}

// tell moves the CRM's record of the mail an event names to the event's status. A
// CRM that cannot answer costs the timeline an entry, never the event.
func (s *Service) tell(ctx context.Context, org string, e *Event) {
	if status := crmStatus(e.Kind); status != "" && e.PID != "" {
		if _, err := eventFn(ctx, org, e.key(), e.ID, status, e.At); err != nil && s.log != nil {
			s.log.Warn("email: a delivery event did not reach the crm", "org", org, "err", err)
		}
	}
}

// crmStatus is the CRM's word for an event, "" for one it does not keep.
func crmStatus(kind string) string {
	switch kind {
	case KindDelivered, KindOpened, KindClicked, KindBounced, KindComplained:
		return kind
	}
	return ""
}
