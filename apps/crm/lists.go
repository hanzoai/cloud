package crm

// lists.go — a marketing list's members, as the CRM keeps them.
//
// Every subscriber of a marketing list is a person: created when the CRM does not
// hold them, given only the fields still empty when it does. The list is a CRM
// list, their place on it a list member, and every change to their membership
// (subscribed, confirmed, imported, unsubscribed, bounced, complained, cleaned,
// moved) an activity on their timeline that carries the consent evidence as
// recorded. Writes go in batches, a few hundred members a call, so an import of
// millions reaches the CRM at the rate the CRM takes it.
//
// IDEMPOTENT BY CONSTRUCTION, as every other record here: a person's id is their
// address, a list's is its marketing id, a member's is the pair, and an
// activity's reference is the change itself, so a batch written twice is one.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/plane"
)

// Member is one change to one person's place on one marketing list.
type Member struct {
	// List is the marketing list's id, and ListName its name.
	List, ListName string
	// Email is the address; the person is filed under it.
	Email string
	// Name is the person's full name, when known.
	Name string
	// Event is what happened: subscribed, confirmed, imported, unsubscribed,
	// bounced, complained, cleaned or moved.
	Event string
	// Status is the membership's status after it.
	Status string
	// Source is where the membership came from (form, page, api, import,
	// commerce) and Ref the form, page or import batch.
	Source, Ref string
	// Consent is the evidence, by field: basis, method, at (RFC 3339), page,
	// language, ip, gpc, dnt.
	Consent map[string]any
	// At is when it happened.
	At time.Time
}

// events is how each change reads on the timeline.
var events = map[string]string{
	"subscribed": "Subscribed", "confirmed": "Confirmed", "imported": "Imported",
	"unsubscribed": "Unsubscribed", "bounced": "Bounced", "complained": "Complained of spam",
	"cleaned": "Cleaned", "moved": "Moved",
}

// listRecord is the CRM list a marketing list is.
func listRecord(id, name string) map[string]any {
	return map[string]any{"id": recordID("list", "marketing:"+id), "name": name, "object": "people",
		"purpose": "Mailing list"}
}

// FileMembers puts a batch of membership changes on the org's CRM: each person,
// each list, each person's place on it, and each change on their timeline.
func FileMembers(ctx context.Context, org string, ms []Member) error {
	if len(ms) == 0 {
		return nil
	}
	// People: the ones held are read in one call, so only the empty fields of a
	// held person are written and nobody's name is overwritten.
	ids := map[string]Member{}
	for _, m := range ms {
		ids[personID(m.Email)] = m
	}
	held, err := heldPeople(ctx, org, ids)
	if err != nil {
		return err
	}
	var people []map[string]any
	for id, m := range ids {
		p := map[string]any{"id": id}
		h := held[id]
		if h == nil {
			p["emails"] = email(strings.ToLower(strings.TrimSpace(m.Email)))
			set(p, "source", m.Source)
		}
		if m.Name != "" && (h == nil || empty(h["name"])) {
			p["name"] = name(m.Name)
		}
		if h != nil && empty(h["source"]) && m.Source != "" {
			p["source"] = m.Source
		}
		if len(p) > 1 {
			people = append(people, p)
		}
	}
	if err := upsertAll(ctx, org, People, "", people); err != nil {
		return err
	}
	// Lists, created when missing and left as they are when there.
	lists := map[string]map[string]any{}
	for _, m := range ms {
		r := listRecord(m.List, m.ListName)
		lists[r["id"].(string)] = r
	}
	for _, r := range lists {
		if err := record(ctx, org, "import", Lists, r); err != nil {
			return err
		}
	}
	// Members and the timeline.
	members, acts := make([]map[string]any, 0, len(ms)), make([]map[string]any, 0, len(ms))
	for _, m := range ms {
		pid := personID(m.Email)
		list := listRecord(m.List, m.ListName)["id"].(string)
		source := "api"
		if m.Source == "import" || m.Source == "commerce" {
			source = "import"
		}
		member := map[string]any{"id": recordID("member", list+pid), "list": list, "person": pid, "source": source}
		if at, _ := m.Consent["at"].(string); at != "" {
			if t := datetime(at); t != "" {
				member["consent_at"] = t
			}
		}
		members = append(members, member)
		data := map[string]any{"kind": "list", "list": m.List, "list_name": m.ListName, "event": m.Event,
			"status": m.Status, "source": m.Source}
		set(data, "ref", m.Ref)
		if len(m.Consent) > 0 {
			data["consent"] = m.Consent
		}
		subject := events[m.Event]
		if subject == "" {
			subject = m.Event
		}
		acts = append(acts, map[string]any{
			"source_id": recordID("membership", fmt.Sprintf("%s|%s|%s|%d", m.List, pid, m.Event, m.At.Unix())),
			"kind":      "system",
			"direction": "in",
			"channel":   "list",
			"subject":   subject + ": " + m.ListName,
			"at":        stamp(m.At),
			"person":    pid,
			"data":      data,
		})
	}
	if err := upsertAll(ctx, org, ListMembers, "", members); err != nil {
		return err
	}
	return upsertAll(ctx, org, Activities, "source_id", acts)
}

// heldPeople reads which of the ids the CRM already holds, a page at a time.
func heldPeople(ctx context.Context, org string, ids map[string]Member) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	all := make([]string, 0, len(ids))
	for id := range ids {
		all = append(all, id)
	}
	for i := 0; i < len(all); i += 100 {
		part := all[i:min(i+100, len(all))]
		terms := make([]string, len(part))
		for k, id := range part {
			terms[k] = fmt.Sprintf("id = %q", id)
		}
		page, err := listFn(plane.For(ctx, org), &client.BaseListIn{
			Collection: People, Filter: strings.Join(terms, " || "), PerPage: 1000, SkipTotal: true,
		})
		if err != nil {
			return nil, err
		}
		for _, raw := range page.Items {
			var r map[string]any
			if json.Unmarshal(raw, &r) == nil {
				if id, _ := r["id"].(string); id != "" {
					out[id] = r
				}
			}
		}
	}
	return out, nil
}

// upsertAll writes records in calls of at most two hundred.
func upsertAll(ctx context.Context, org, collection, key string, rs []map[string]any) error {
	for i := 0; i < len(rs); i += 200 {
		part := rs[i:min(i+200, len(rs))]
		raws := make([]json.RawMessage, 0, len(part))
		for _, r := range part {
			b, err := json.Marshal(r)
			if err != nil {
				return err
			}
			raws = append(raws, b)
		}
		if _, err := upsertFn(plane.For(ctx, org), &client.BaseUpsertIn{
			Collection: collection, Key: key, Source: "import", Records: raws,
		}); err != nil {
			return err
		}
	}
	return nil
}
