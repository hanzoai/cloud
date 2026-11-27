package crm

// newsletter.go — a newsletter subscriber, once they have confirmed.
//
// Nothing is filed when a person asks for the newsletter: their address is only
// mailed a link (apps/marketing newsletter.go). Pressing the link files three
// records in the org's Base. The person, as a later form finds them: created when
// missing, given only the fields still empty when held. Their place on the
// "Newsletter" list (lists, list_members), which is who the newsletter is sent
// to. And the confirmation itself, a form on their timeline whose data is the
// consent as given: the address, the page and language it came from, when it was
// asked for and when and from which address it was confirmed.
//
// The form is keyed by the link's own reference, so one link files one form
// however often it is pressed, and a second link (the person subscribed again)
// is a second form: each confirmation is its own fact.

import (
	"context"
	"time"
)

// Newsletter is the kind a confirmed subscription is filed as: the form's
// data.kind, never a program.
const Newsletter = "newsletter"

// NewsletterList is the newsletter list's name: the one list this deployment
// runs a double opt-in for.
const NewsletterList = "Newsletter"

// newsletter is the list a person who confirmed the newsletter is on.
var newsletter = map[string]any{
	"id": recordID("list", Newsletter), "name": NewsletterList, "object": "people",
	"purpose": "The newsletter, for people who confirmed they want it",
}

// Subscribed reports whether the confirmation ref names was already filed.
func Subscribed(ctx context.Context, org, ref string) (bool, error) {
	a, err := formOf(ctx, org, ref)
	return a != nil, err
}

// FileSubscriber files one confirmed subscription: the person, their place on the
// newsletter list, then the confirmation on their timeline, last, so a filing cut
// short is filed whole when it is asked again. d is in the subscription's field
// names (email, source, locale, requested_at, confirmed_at, confirmed_ip,
// consent, public_ref); at is when it was confirmed.
func FileSubscriber(ctx context.Context, org string, d map[string]any, at time.Time) error {
	p := map[string]any{"id": personID(text(d, "email")), "emails": email(text(d, "email"))}
	set(p, "source", text(d, "source"))
	if err := person(ctx, org, "application", p); err != nil {
		return err
	}
	if err := record(ctx, org, "application", Lists, newsletter); err != nil {
		return err
	}
	if err := record(ctx, org, "application", ListMembers, map[string]any{
		"id": recordID("member", newsletter["id"].(string)+p["id"].(string)), "list": newsletter["id"], "person": p["id"],
		"source": "api", "consent_at": stamp(at),
	}); err != nil {
		return err
	}
	data := filed(d)
	data["kind"] = Newsletter
	return form(ctx, org, "application", map[string]any{
		"source_id": text(d, "public_ref"),
		"kind":      "form",
		"direction": "in",
		"channel":   "web",
		"subject":   titles[Newsletter],
		"at":        stamp(at),
		"person":    p["id"],
		"data":      data,
	})
}
