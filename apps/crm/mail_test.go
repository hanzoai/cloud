package crm

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/plane"
)

// mailOf reads the activity a send's id was filed under, nil for none.
func mailOf(t *testing.T, ctx context.Context, id string) map[string]any {
	t.Helper()
	page, err := listFn(plane.For(ctx, org), &client.BaseListIn{Collection: Activities, Filter: fmt.Sprintf("source_id = %q", mailRef(id)), PerPage: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) == 0 {
		return nil
	}
	if len(page.Items) > 1 {
		t.Fatalf("%d activities under %s, want one", len(page.Items), id)
	}
	var a map[string]any
	if err := json.Unmarshal(page.Items[0], &a); err != nil {
		t.Fatal(err)
	}
	return a
}

// A mail to an address the org holds a person for is on that person's timeline;
// one to an address it holds no one for is not filed at all.
func TestAMailIsFiledForAKnownAddressAndNotForAnUnknownOne(t *testing.T) {
	ctx := open(t)
	if err := FileLead(ctx, org, "application", betaLead(), filedAt); err != nil {
		t.Fatal(err)
	}
	filed, err := FileMail(ctx, org, Mail{ID: "ses:m-1", To: "Ada@Example.com", By: "program:beta", Subject: "Your code", Body: "Hi Ada.", At: filedAt, Rail: "ses"})
	if err != nil || !filed {
		t.Fatalf("a mail to a known address: filed %v, %v", filed, err)
	}
	a := mailOf(t, ctx, "ses:m-1")
	if a == nil || a["person"] != personID("ada@example.com") || a["kind"] != "email" || a["direction"] != "out" || a["subject"] != "Your code" {
		t.Fatalf("filed %v", a)
	}
	d, _ := a["data"].(map[string]any)
	if d["status"] != "sent" || d["to"] != "ada@example.com" || d["rail"] != "ses" {
		t.Fatalf("filed data %v", d)
	}

	filed, err = FileMail(ctx, org, Mail{ID: "ses:m-2", To: "stranger@example.com", Subject: "Hello", Body: "x", At: filedAt})
	if err != nil || filed {
		t.Fatalf("a mail to an unknown address: filed %v, %v, want not filed", filed, err)
	}
	if a := mailOf(t, ctx, "ses:m-2"); a != nil {
		t.Fatalf("a mail to an unknown address is on a timeline: %v", a)
	}
}

// Know makes the person an unmet address is, once, and leaves a person the org
// holds as it is; a mail to the address is then on their timeline.
func TestKnowMakesTheOnePersonAnAddressIs(t *testing.T) {
	ctx := open(t)
	id, err := Know(ctx, org, "beta invite", " Grace@Example.com ")
	if err != nil || id != personID("grace@example.com") {
		t.Fatalf("know: %q, %v", id, err)
	}
	if again, err := Know(ctx, org, "beta invite", "grace@example.com"); err != nil || again != id {
		t.Fatalf("know again: %q, %v", again, err)
	}
	filed, err := FileMail(ctx, org, Mail{ID: "invite:inv_1", Person: id, To: "grace@example.com", By: "ada@example.com", Subject: "Invitation", Body: "Hi.", At: filedAt, Rail: "gmail"})
	if err != nil || !filed {
		t.Fatalf("a mail to a known person: filed %v, %v", filed, err)
	}
	if a := mailOf(t, ctx, "invite:inv_1"); a == nil || a["person"] != id {
		t.Fatalf("filed %v", a)
	}
	if err := FileLead(ctx, org, "application", betaLead(), filedAt); err != nil {
		t.Fatal(err)
	}
	held := personID("ada@example.com")
	if got, err := Know(ctx, org, "beta invite", "ada@example.com"); err != nil || got != held {
		t.Fatalf("a held person is %q, %v, want %q", got, err, held)
	}
	if _, err := Know(ctx, org, "beta invite", " "); err == nil {
		t.Fatal("an empty address was known")
	}
}

// A person the team made, with an id of Base's choosing, is found by the
// addresses their record names.
func TestAMailFindsAPersonTheTeamMade(t *testing.T) {
	ctx := open(t)
	if err := upsert(ctx, org, "import", People, "", map[string]any{
		"id": "teammade0000001", "emails": map[string]any{"primaryEmail": "grace@navy.example", "additionalEmails": []string{"g@home.example"}},
	}); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"grace@navy.example", "g@home.example"} {
		if p, err := PersonOf(ctx, org, to); err != nil || p != "teammade0000001" {
			t.Fatalf("%s is person %q, %v", to, p, err)
		}
	}
}

// A refused mail is filed suppressed with its reason, and delivery events move a
// mail forward and never back.
func TestMailEventsMoveAMailForward(t *testing.T) {
	ctx := open(t)
	if err := FileLead(ctx, org, "application", betaLead(), filedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := FileMail(ctx, org, Mail{ID: "x", To: "ada@example.com", Subject: "s", Status: "suppressed", Reason: "unsubscribed", At: filedAt}); err != nil {
		t.Fatal(err)
	}
	if d := mailOf(t, ctx, "x")["data"].(map[string]any); d["status"] != "suppressed" || d["reason"] != "unsubscribed" {
		t.Fatalf("a refused mail is filed %v", d)
	}

	if _, err := FileMail(ctx, org, Mail{ID: "sendgrid:sg-1", To: "ada@example.com", Subject: "s", At: filedAt}); err != nil {
		t.Fatal(err)
	}
	at := filedAt
	for _, step := range []struct{ event, want string }{
		{"delivered", "delivered"},
		{"clicked", "clicked"},
		{"opened", "clicked"}, // an open arriving after its click does not move it back
		{"bounced", "bounced"},
		{"delivered", "bounced"},
	} {
		at = at.Add(time.Minute)
		ok, err := MailEvent(ctx, org, "sendgrid:sg-1", "", step.event, at)
		if err != nil || !ok {
			t.Fatalf("%s: %v %v", step.event, ok, err)
		}
		if got := mailOf(t, ctx, "sendgrid:sg-1")["data"].(map[string]any)["status"]; got != step.want {
			t.Fatalf("after %s the mail is %v, want %s", step.event, got, step.want)
		}
	}
	events := mailOf(t, ctx, "sendgrid:sg-1")["data"].(map[string]any)["events"].([]any)
	if len(events) != 6 {
		t.Fatalf("%d events on the mail, want the send and five", len(events))
	}
	if ok, err := MailEvent(ctx, org, "sendgrid:nobody", "", "delivered", at); err != nil || ok {
		t.Fatalf("an event for a mail never filed: %v %v, want not found and no error", ok, err)
	}
}

// The same provider event told twice (a retry, a delivery two replicas filed)
// joins the mail's events once.
func TestAMailEventIsKeptOncePerEvent(t *testing.T) {
	ctx := open(t)
	if err := FileLead(ctx, org, "application", betaLead(), filedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := FileMail(ctx, org, Mail{ID: "ses:9", To: "ada@example.com", Subject: "s", At: filedAt}); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if ok, err := MailEvent(ctx, org, "ses:9", "ses:n-open", "opened", filedAt.Add(time.Minute)); err != nil || !ok {
			t.Fatalf("%v %v", ok, err)
		}
	}
	if _, err := MailEvent(ctx, org, "ses:9", "ses:n-click", "clicked", filedAt.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	d := mailOf(t, ctx, "ses:9")["data"].(map[string]any)
	if events := d["events"].([]any); len(events) != 3 || d["status"] != "clicked" {
		t.Fatalf("events %v status %v, want the send, one open and one click", events, d["status"])
	}
}

// A person's outreach follows the mail sent them, forward only, and a send moves
// their last contact: the CRM's tables filter people by both without reading a
// timeline.
func TestOutreachFollowsTheMail(t *testing.T) {
	ctx := open(t)
	if err := FileLead(ctx, org, "application", betaLead(), filedAt); err != nil {
		t.Fatal(err)
	}
	ada := personID("ada@example.com")
	stands := func(want, contacted string) {
		t.Helper()
		p := must(t, ctx, People, ada)
		if p["outreach"] != want || p["contacted"] != contacted {
			t.Fatalf("outreach %v, contacted %v; want %s, %s", p["outreach"], p["contacted"], want, contacted)
		}
	}

	// A refused mail reaches no one.
	if _, err := FileMail(ctx, org, Mail{ID: "r", To: "ada@example.com", Subject: "s", Status: "suppressed", At: filedAt}); err != nil {
		t.Fatal(err)
	}
	stands("", "")

	if _, err := FileMail(ctx, org, Mail{ID: "ses:1", To: "ada@example.com", Subject: "s", At: filedAt}); err != nil {
		t.Fatal(err)
	}
	stands("sent", stamp(filedAt))

	at := filedAt
	for _, step := range []struct{ event, want string }{
		{"delivered", "sent"},
		{"clicked", "clicked"},
		{"opened", "clicked"}, // an open after the click does not move them back
		{"complained", "unsubscribed"},
		{"delivered", "unsubscribed"},
	} {
		at = at.Add(time.Minute)
		if _, err := MailEvent(ctx, org, "ses:1", "", step.event, at); err != nil {
			t.Fatal(err)
		}
		stands(step.want, stamp(filedAt))
	}

	// An older send never makes the last contact older.
	if _, err := FileMail(ctx, org, Mail{ID: "ses:0", To: "ada@example.com", Subject: "s", At: filedAt.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	stands("unsubscribed", stamp(filedAt))
	later := filedAt.Add(24 * time.Hour)
	if _, err := FileMail(ctx, org, Mail{ID: "ses:2", To: "ada@example.com", Subject: "s", At: later}); err != nil {
		t.Fatal(err)
	}
	stands("unsubscribed", stamp(later))
}

// A reply the org receives is filed inbound on the person who wrote it, in the
// thread it answers, and that person has replied; the send's own contact time stays.
func TestAReplyIsFiledInboundAndMarksThePersonReplied(t *testing.T) {
	ctx := open(t)
	if err := FileLead(ctx, org, "application", betaLead(), filedAt); err != nil {
		t.Fatal(err)
	}
	if _, err := FileMail(ctx, org, Mail{ID: "ses:1", To: "ada@example.com", Subject: "Your code", At: filedAt}); err != nil {
		t.Fatal(err)
	}
	reply := Mail{
		ID: "in:r-1", From: "Ada@Example.com", To: "growth@hanzo.ai", Subject: "Re: Your code", Body: "Thanks, got it.",
		MessageID: "<r1@mail.example.com>", References: "<ses-1@email.amazonses.com> <r0@mail.example.com>", At: filedAt.Add(time.Hour),
	}
	for range 2 {
		if filed, err := FileMail(ctx, org, reply); err != nil || !filed {
			t.Fatalf("a reply from a known address: filed %v, %v", filed, err)
		}
	}
	a := mailOf(t, ctx, "in:r-1")
	if a == nil || a["person"] != personID("ada@example.com") || a["direction"] != "in" || a["subject"] != "Re: Your code" {
		t.Fatalf("filed %v", a)
	}
	d, _ := a["data"].(map[string]any)
	if d["status"] != "received" || d["from"] != "ada@example.com" || d["to"] != "growth@hanzo.ai" || d["message_id"] != "<r1@mail.example.com>" || d["thread"] != "<ses-1@email.amazonses.com>" {
		t.Fatalf("filed data %v", d)
	}
	p := must(t, ctx, People, personID("ada@example.com"))
	if p["outreach"] != "replied" || p["contacted"] != stamp(filedAt) {
		t.Fatalf("outreach %v, contacted %v; want replied, %s", p["outreach"], p["contacted"], stamp(filedAt))
	}

	stranger := reply
	stranger.ID, stranger.From = "in:r-2", "stranger@example.com"
	if filed, err := FileMail(ctx, org, stranger); err != nil || filed {
		t.Fatalf("a reply from an unknown address: filed %v, %v, want not filed", filed, err)
	}
}
