package crm

// form.go — a completed response to one of the org's forms (/v1/form), on the
// timeline, and an event the org's site stated, on the timeline.
//
// A response is an activity of kind "form" titled by its form, keyed by
// "form:<response id>" so filing it twice is one entry. It is on the person the
// response is from when the forms service knew them (their CRM person id), and on
// the org's own timeline when it did not: an activity with no person, which the
// timeline holds as it holds any other. The person's address fills the person and
// never the entry: the person holds it.

import (
	"context"
	"errors"
	"strings"
	"time"
)

// PersonID is the CRM person an address is: one person per address, however many
// forms, lists, mails and events name it. It is the join key every surface that
// speaks of a person carries instead of the address.
func PersonID(address string) string { return personID(address) }

// FormFiling is one completed response as the timeline files it.
type FormFiling struct {
	// Ref is the entry's key, "form:<response id>".
	Ref string
	// Title is the form's title, Form its id, Public its public id, Kind its kind.
	Title, Form, Public, Kind string
	// Person is the CRM person; empty files on the org's own timeline.
	Person string
	// Email, Name and Company fill the person: created when missing, given only
	// the fields still empty when held.
	Email, Name, Company string
	// List is the list the person joins, by name; empty joins none.
	List string
	// Answers are each question and what was said.
	Answers []map[string]any
	// Hidden are the values the page passed in.
	Hidden map[string]string
	// Visitor is the page's visitor id, Subject the signed-in person's subject,
	// Path the page.
	Visitor, Subject, Path string
	// Verified is whether the person is the signed-in respondent. An unverified
	// filing is a claim someone typed: the entry says so, it is on the person
	// only when the CRM already holds them, and it changes nothing about them,
	// joins no list and records no consent.
	Verified bool
}

// FileForm files one completed response at the moment it was completed: the
// person (and their place on the list) when a signed-in respondent names one,
// then the entry. A claim is filed on the person it names only when the CRM
// already holds them, marked unverified, and on the org's own timeline otherwise.
func FileForm(ctx context.Context, org string, d FormFiling, at time.Time) error {
	on := d.Person
	if !d.Verified && on != "" {
		held, err := get(ctx, org, People, on)
		if err != nil {
			return err
		}
		if held == nil {
			on = ""
		}
	}
	if d.Verified && d.Person != "" && d.Email != "" {
		p := map[string]any{"id": d.Person, "emails": email(d.Email), "lifecycle": "lead", "source": "form:" + d.Public}
		if n := strings.TrimSpace(d.Name); n != "" {
			p["name"] = name(n)
		}
		if err := person(ctx, org, "application", p); err != nil {
			return err
		}
		if l := strings.TrimSpace(d.List); l != "" {
			list := map[string]any{"id": recordID("list", strings.ToLower(l)), "name": l, "object": "people", "purpose": "People who joined it through a form"}
			if err := record(ctx, org, "application", Lists, list); err != nil {
				return err
			}
			if err := record(ctx, org, "application", ListMembers, map[string]any{
				"id": recordID("member", list["id"].(string)+d.Person), "list": list["id"], "person": d.Person,
				"source": "api", "consent_at": stamp(at),
			}); err != nil {
				return err
			}
		}
	}
	data := map[string]any{"kind": d.Kind, "form": d.Form, "public_id": d.Public, "verified": d.Verified}
	set(data, "company", strings.TrimSpace(d.Company))
	set(data, "visitor", d.Visitor)
	set(data, "account", d.Subject)
	set(data, "path", d.Path)
	if d.Path != "" {
		set(data, "source", d.Path)
	} else if d.Public != "" {
		set(data, "source", "form:"+d.Public)
	}
	if p := strings.TrimSpace(d.Hidden["program"]); p != "" {
		set(data, "program", p)
	} else {
		pub := strings.ToLower(d.Public)
		switch {
		case strings.HasSuffix(pub, ".help-chat") || strings.HasSuffix(pub, ".support"):
			data["program"] = "support"
		case strings.HasSuffix(pub, ".sales-chat"):
			data["program"] = "chat"
		case strings.HasSuffix(pub, "-feedback"):
			data["program"] = "feedback"
		}
	}
	if len(d.Answers) > 0 {
		data["answers"] = d.Answers
	}
	if len(d.Hidden) > 0 {
		data["hidden"] = d.Hidden
	}
	subject := d.Title
	if !d.Verified && d.Person != "" {
		subject = "Unverified: " + d.Title
	}
	a := map[string]any{
		"source_id": d.Ref,
		"kind":      "form",
		"direction": "in",
		"channel":   "web",
		"subject":   subject,
		"at":        stamp(at),
		"data":      data,
	}
	set(a, "person", on)
	if len(d.Answers) > 0 {
		v, _ := d.Answers[0]["answer"].(string)
		set(a, "snippet", snippet(v))
	}
	return form(ctx, org, "application", a)
}

// EventFiling is one event the org's site stated, as the timeline files it.
type EventFiling struct {
	// Ref is the entry's key, "event:<event id>".
	Ref string
	// Person is the CRM person the event is from. Required.
	Person string
	// Email fills the person when it is the address the event named.
	Email string
	// Kind is the activity's kind, one of the timeline's kinds; Title its subject.
	Kind, Title string
	// Data is what of the event the entry keeps.
	Data map[string]any
}

// FileEvent files one event on its person's timeline, once.
func FileEvent(ctx context.Context, org string, d EventFiling, at time.Time) error {
	if d.Email != "" {
		if err := person(ctx, org, "application", map[string]any{"id": d.Person, "emails": email(d.Email), "lifecycle": "lead", "source": "event"}); err != nil {
			return err
		}
	} else if held, err := get(ctx, org, People, d.Person); err != nil || held == nil {
		return errNoPerson(err)
	}
	return form(ctx, org, "application", map[string]any{
		"source_id": d.Ref,
		"kind":      d.Kind,
		"direction": "in",
		"channel":   "web",
		"subject":   d.Title,
		"at":        stamp(at),
		"person":    d.Person,
		"data":      d.Data,
	})
}

// ErrNoPerson is an event naming a person the CRM does not hold.
var ErrNoPerson = errors.New("crm: the event names no person the CRM holds")

// errNoPerson is a read that failed, or else the person missing.
func errNoPerson(err error) error {
	if err != nil {
		return err
	}
	return ErrNoPerson
}

// Kinds are the kinds an activity on the timeline may be, as module crm
// declares them.
var Kinds = []string{"email", "sms", "whatsapp", "chat", "call", "meeting", "note", "form", "order", "task", "booking", "system"}
