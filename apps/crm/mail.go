package crm

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/plane"
)

// Mail is one email the org sent to an address, or refused to send.
type Mail struct {
	// ID is the send's own key, the one its delivery events name it by:
	// "<provider>:<the provider's message id>" when the provider answered one,
	// else the sender's id for the message.
	ID string
	// Person is the CRM person the mail was written to. Empty, it is the person
	// the org holds for the address, and a mail to an address the org holds no
	// one for is not filed.
	Person string
	// To is the address it went to; for a mail the org received, the org's own.
	To string
	// From is who a mail the org received came from. Set, the mail is a reply: it is
	// filed inbound on the person the org holds for From, and that person has replied.
	From string
	// MessageID is the Message-ID header of a mail the org received, and References
	// its References header (In-Reply-To when it has none): the ids a reply to it is
	// threaded by, and the first of them, or its own, names the thread.
	MessageID, References string
	// By is who sent it: their email, their IAM subject, or the program that did.
	By      string
	Subject string
	// Body is the mail as it left, plain text or HTML.
	Body string
	// At is when it left, or was refused.
	At time.Time
	// Status is sent, suppressed or failed.
	Status string
	// Reason says why a mail was suppressed or failed.
	Reason string
	// Rail is what carried it: a provider (ses, sendgrid, twilio_email, mail) or
	// the org's mailbox.
	Rail string
}

// The statuses a mail moves through. A later event never undoes a final one: a
// bounce or a complaint stays, and an open that arrives before its delivery does
// not move a click back.
var rank = map[string]int{
	"suppressed": 0, "failed": 0, "sent": 1, "delivered": 2, "opened": 3, "clicked": 4,
	"bounced": 9, "complained": 9,
}

// FileMail is the ONE place an email reaches the CRM: every sender in the fleet
// files through it, sent or refused, and so does every reply the org receives. It
// puts the mail on the timeline of the person the org holds for the address (To
// for a send, From for a reply), an activity of kind email keyed by the mail's id,
// so the same mail filed twice is one activity. It reports whether it filed: an
// address the org holds no person for is not a record, and is left alone.
func FileMail(ctx context.Context, org string, m Mail) (bool, error) {
	inbound := m.From != ""
	address, direction := m.To, "out"
	if inbound {
		address, direction = m.From, "in"
	}
	if m.ID == "" || address == "" {
		return false, errors.New("crm: a mail without its id or its address")
	}
	person := m.Person
	if person == "" {
		var err error
		if person, err = PersonOf(ctx, org, address); err != nil || person == "" {
			return false, err
		}
	}
	at := m.At
	if at.IsZero() {
		at = time.Now()
	}
	status := m.Status
	if status == "" {
		status = "sent"
		if inbound {
			status = "received"
		}
	}
	data := map[string]any{"to": strings.ToLower(strings.TrimSpace(m.To)), "status": status}
	if inbound {
		data["from"] = strings.ToLower(strings.TrimSpace(m.From))
		set(data, "message_id", m.MessageID)
		set(data, "references", m.References)
		set(data, "thread", cmp.Or(strings.Fields(m.References+" "+m.MessageID)...))
	} else {
		data["by"] = m.By
		data["events"] = []any{map[string]any{"status": status, "at": stamp(at)}}
	}
	set(data, "reason", m.Reason)
	set(data, "rail", m.Rail)
	a := map[string]any{
		"source_id": mailRef(m.ID),
		"kind":      "email",
		"direction": direction,
		"channel":   "email",
		"subject":   m.Subject,
		"at":        stamp(at),
		"person":    person,
		"data":      data,
	}
	set(a, "snippet", snippet(m.Body))
	set(a, "body", paragraphs(m.Body))
	if err := upsert(ctx, org, "email", Activities, "source_id", a); err != nil {
		return true, err
	}
	switch {
	case inbound:
		return true, reach(ctx, org, person, "replied", time.Time{})
	case status == "sent":
		return true, reach(ctx, org, person, "sent", at)
	}
	return true, nil
}

// outreach is where a person stands with the org's mail, from what became of the
// last mail sent them, and how far each word is along: a person who opened is past
// one who was sent to, and a bounce or an opt-out stands until a person moves it.
var outreach = map[string]int{"sent": 1, "opened": 2, "clicked": 3, "replied": 4, "bounced": 9, "unsubscribed": 9}

// outreachOf is the person's outreach a mail's status moves them to: a delivery is
// still a send, and a complaint is an opt-out. A refused or failed mail moves none.
func outreachOf(status string) string {
	switch status {
	case "sent", "delivered":
		return "sent"
	case "opened", "clicked", "bounced":
		return status
	case "complained":
		return "unsubscribed"
	}
	return ""
}

// reach moves a person's outreach forward to word and, when sent is a send's time,
// their last contact to it, so the CRM's tables filter and sort people by where outreach to
// them stands without reading every timeline. Neither moves back: a later open
// does not undo a click, a reply stays a reply, and an older send never makes the
// last contact older. A CRM installed before the fields has none to move.
func reach(ctx context.Context, org, person, word string, sent time.Time) error {
	if person == "" || word == "" {
		return nil
	}
	p, err := get(ctx, org, People, person)
	if err != nil || p == nil {
		return err
	}
	if _, grown := p["outreach"]; !grown {
		return nil
	}
	patch := map[string]any{"id": person}
	if now, _ := p["outreach"].(string); outreach[word] > outreach[now] {
		patch["outreach"] = word
	}
	if !sent.IsZero() {
		if last, _ := p["contacted"].(string); last == "" || stamp(sent) > last {
			patch["contacted"] = stamp(sent)
		}
	}
	if len(patch) == 1 {
		return nil
	}
	return upsert(ctx, org, "email", People, "", patch)
}

// Know makes sure org holds a person for an address and answers the person: the
// one it already holds, else a new one, a lead. It writes only what
// is missing and never over what the org holds. A sender whose mail is to someone
// the org has not met calls it before FileMail, which files on a person and
// makes none. how is the person's source: how the org came to know them.
func Know(ctx context.Context, org, how, address string) (string, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" {
		return "", errors.New("crm: no address to know")
	}
	if id, err := PersonOf(ctx, org, address); err != nil || id != "" {
		return id, err
	}
	p := leadPerson(map[string]any{"email": address, "source": how})
	if err := person(ctx, org, "email", p); err != nil {
		return "", err
	}
	return p["id"].(string), nil
}

// MailEvent records what became of a mail FileMail filed under id: delivered,
// bounced, complained, opened or clicked. event is the provider's own id for the
// event: the same event told twice (a provider's retry, a delivery filed by two
// replicas) joins the mail's events once. The event moves its status forward,
// never back. It reports whether a mail was filed under id; one the CRM holds no
// record of is not an error.
func MailEvent(ctx context.Context, org, id, event, status string, at time.Time) (bool, error) {
	if _, ok := rank[status]; !ok || id == "" {
		return false, fmt.Errorf("crm: no mail event %q", status)
	}
	data, err := Amend(ctx, org, "email", mailRef(id), func(d map[string]any) bool {
		events, _ := d["events"].([]any)
		for _, x := range events {
			if m, ok := x.(map[string]any); ok && event != "" && m["event"] == event {
				return false
			}
		}
		entry := map[string]any{"status": status, "at": stamp(at)}
		set(entry, "event", event)
		d["events"] = append(events, entry)
		if now, _ := d["status"].(string); rank[status] >= rank[now] {
			d["status"] = status
		}
		return true
	})
	if err != nil || data == nil {
		return data != nil, err
	}
	a, err := formOf(ctx, org, mailRef(id))
	if err != nil || a == nil {
		return true, err
	}
	person, _ := a["person"].(string)
	// A report moves where outreach stands; only a send is a contact.
	return true, reach(ctx, org, person, outreachOf(status), time.Time{})
}

// PersonOf is the person org holds for an address, "" when it holds none: the
// person the address is, as an intake files it, else one the team made whose
// emails name it.
func PersonOf(ctx context.Context, org, address string) (string, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" {
		return "", nil
	}
	id := personID(address)
	held, err := get(ctx, org, People, id)
	if err != nil {
		return "", err
	}
	if held != nil {
		return id, nil
	}
	page, err := listFn(plane.For(ctx, org), &client.BaseListIn{
		Collection: People, PerPage: 1, SkipTotal: true,
		Filter: fmt.Sprintf(`emails.primaryEmail = %q || emails.additionalEmails ~ %q`, address, address),
	})
	if err != nil || len(page.Items) == 0 {
		return "", err
	}
	var p struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(page.Items[0], &p); err != nil {
		return "", err
	}
	return p.ID, nil
}

// mailRef is the activity reference a send's id is filed under.
func mailRef(id string) string { return "mail:" + id }
