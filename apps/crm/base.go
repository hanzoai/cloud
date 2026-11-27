package crm

// base.go — the CRM's records, in the org's own Base.
//
// Every record the CRM keeps is a record of the org's Base (hanzoai/base module
// crm, HIP-1120 as amended): people, companies, opportunities, candidates,
// applications and the activity timeline. Only the base app opens a Base, so this
// package writes over the plane (base_import, base_upsert, base_get), acting for
// the org the call names, and the base process stays the single writer.
//
// A public form is two records: the person it came from and the form itself, an
// activity of kind "form" that holds every answer as filed. A job application is
// a candidate, kept apart from the people sales works, and its form. The shape is
// the one written here, from the field names the form has always used
// (apps/marketing leads.go).
//
// IDEMPOTENT BY CONSTRUCTION. A record's id is derived from what it is — a person
// from their address, a candidate from its reference — and a form is keyed by its
// reference (the activity's source_id). Writing the same thing twice is one record,
// so a replay or a retried intake changes nothing.
//
// NOTHING IS OVERWRITTEN. A person the CRM already holds keeps what the team has
// given them: a later form fills only the fields still empty and is itself on the
// timeline. Every other record is created when it is missing and left alone when
// it is there, so a stage the team moved stays moved.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	basepeer "github.com/hanzoai/cloud/client/base"
	"github.com/hanzoai/cloud/internal/lock"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// The collections the CRM writes, by name, as module crm declares them.
const (
	People      = "people"
	Candidates  = "candidates"
	Activities  = "activities"
	Lists       = "lists"
	ListMembers = "list_members"
)

// careers is the list an applicant who asked to hear about roles and news joins.
var careers = map[string]any{
	"id": recordID("list", "careers"), "name": "Careers news", "object": "people",
	"purpose": "Roles and news, for applicants who asked to be kept posted",
}

// leadLists maps each lead program/kind to its CRM marketing list.
var leadLists = map[string]struct{ name, purpose string }{
	"beta":       {"Beta", "Developers and teams in the Hanzo Beta program"},
	"research":   {"Research", "Researchers and labs evaluating Hanzo models"},
	"partner":    {"Design partners", "Design partners building with the Hanzo team"},
	"investor":   {"Investors", "Investors and venture partners"},
	"enterprise": {"Enterprise", "Enterprise buyers and inquiries"},
	"alpha":      {"Alpha", "Early adopters in the Hanzo Alpha program"},
	"support":    {"Support", "Customer support inquiries and help center chats"},
	"feedback":   {"Feedback", "Product and documentation feedback"},
	"chat":       {"Sales leads", "Sales chat and buying specialist inquiries"},
}

// The plane, variables so a test can stand a Base of its own in their place. Only a
// test reassigns them.
var (
	importFn = basepeer.BaseImport
	upsertFn = basepeer.BaseUpsert
	getFn    = basepeer.BaseGet
	listFn   = basepeer.BaseList
)

// titles is how each kind of form reads on the timeline: a sales inquiry, an
// application to a program, an investor's subscription, an application to a job
// and a confirmed newsletter subscription. A lead's kind is the program it names,
// or sales when it names none.
var titles = map[string]string{
	"sales":      "Sales inquiry",
	"beta":       "Beta tester application",
	"research":   "Research access application",
	"partner":    "Design partner application",
	"investor":   "Investor",
	"careers":    "Job application",
	"newsletter": "Newsletter subscription",
	"enterprise": "Enterprise inquiry",
	"support":    "Support inquiry",
	"feedback":   "Product feedback",
	"chat":       "Sales chat inquiry",
}

// ── installing ────────────────────────────────────────────────────────────────

// Install grows org's Base by module crm: what is missing is created, and nothing
// that exists is dropped or changed, so it is safe to ask on every form and a field
// the org added survives it. The intake asks before filing, so the brand's CRM opens
// on its first form; a grant of the beta asks before the org holds it
// (apps/entitlement install).
func Install(ctx context.Context, org string) error {
	_, err := importFn(plane.For(ctx, org), &client.BaseImportIn{Module: Module})
	return err
}

// ── ids ───────────────────────────────────────────────────────────────────────

// recordID is a Base record id derived from what the record is: 15 lowercase hex
// characters of the SHA-256 of its kind and key, inside Base's id shape.
func recordID(kind, key string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + key))
	return hex.EncodeToString(sum[:])[:15]
}

// personID is the person an address is: one person per address, however many
// forms they file.
func personID(email string) string {
	return recordID("person", strings.ToLower(strings.TrimSpace(email)))
}

// candidateID is the candidate a reference is.
func candidateID(ref string) string { return recordID("candidate", ref) }

// ── the records ───────────────────────────────────────────────────────────────

// text reads one field of a form or a document as trimmed text.
func text(d map[string]any, field string) string {
	v, _ := d[field].(string)
	return strings.TrimSpace(v)
}

// name splits a full name at its first space: "Ada Lovelace King" is Ada and
// Lovelace King. A one-word name is a first name.
func name(full string) map[string]any {
	first, last, _ := strings.Cut(strings.Join(strings.Fields(full), " "), " ")
	return map[string]any{"firstName": first, "lastName": last}
}

// email is a one-address emails value, or nil for none.
func email(address string) any {
	if address == "" {
		return nil
	}
	return map[string]any{"primaryEmail": address}
}

// link is a one-address links value, or nil for none. An address with no scheme
// is read as https, the way a person types a domain.
func link(address, label string) any {
	address = strings.TrimSpace(address)
	if address == "" {
		return nil
	}
	if !strings.Contains(address, "://") {
		address = "https://" + address
	}
	return map[string]any{"primaryLinkUrl": address, "primaryLinkLabel": label}
}

// stamp is a time as Base stores a date.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.000Z") }

// datetime reads a form's date or datetime ("2006-01-02 15:04:05", UTC) as Base
// stores it, or "" when it is none.
func datetime(s string) string {
	for _, layout := range []string{time.DateTime, time.DateOnly, time.RFC3339} {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return stamp(t)
		}
	}
	return ""
}

// paragraphs is a person's plain text as an editor field holds it: escaped, a blank
// line a new paragraph and a line break a break, so what they wrote reads as they
// wrote it and nothing they typed is markup.
func paragraphs(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\r", ""))
	if s == "" {
		return ""
	}
	var b strings.Builder
	for _, p := range strings.Split(s, "\n\n") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		lines := strings.Split(p, "\n")
		for i, l := range lines {
			lines[i] = html.EscapeString(strings.TrimSpace(l))
		}
		b.WriteString("<p>" + strings.Join(lines, "<br>") + "</p>")
	}
	return b.String()
}

// snippet is the first line of s, at most 200 characters.
func snippet(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	if r := []rune(strings.TrimSpace(s)); len(r) > 200 {
		return string(r[:199]) + "…"
	}
	return strings.TrimSpace(s)
}

// lifecycle is where a lead's stage puts its person: qualified and disqualified are
// themselves, and a lead not yet judged (new, contacted) is a lead.
func lifecycle(stage string) string {
	switch stage {
	case "qualified", "disqualified":
		return stage
	}
	return "lead"
}

// kindOf is a form's kind: the program it applied to, or sales.
func kindOf(d map[string]any) string {
	if p := text(d, "program"); p != "" {
		return p
	}
	return "sales"
}

// filed is the form's own fields, every empty one dropped: what the activity holds
// as the record of what was asked and answered.
func filed(d map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range d {
		switch x := v.(type) {
		case nil:
			continue
		case string:
			if strings.TrimSpace(x) == "" {
				continue
			}
		case []any:
			if len(x) == 0 {
				continue
			}
		case []map[string]any:
			if len(x) == 0 {
				continue
			}
		case []string:
			if len(x) == 0 {
				continue
			}
		}
		out[k] = v
	}
	return out
}

// set puts v at k when it is a value, and leaves k out when it is none.
func set(r map[string]any, k string, v any) {
	switch x := v.(type) {
	case nil:
		return
	case string:
		if x == "" {
			return
		}
	}
	r[k] = v
}

// leadPerson is the person a lead is, from the lead's own fields.
func leadPerson(d map[string]any) map[string]any {
	p := map[string]any{
		"id":        personID(text(d, "email")),
		"emails":    email(text(d, "email")),
		"lifecycle": lifecycle(text(d, "stage")),
	}
	if n := text(d, "contact_name"); n != "" {
		p["name"] = name(n)
	}
	set(p, "job_title", text(d, "role"))
	set(p, "source", text(d, "source"))
	if a, ok := d["address"].(map[string]any); ok {
		p["address"] = map[string]any{
			"addressStreet1": text(a, "street"), "addressCity": text(a, "city"), "addressState": text(a, "region"),
			"addressPostcode": text(a, "postcode"), "addressCountry": text(a, "country"),
		}
	} else if c := text(d, "country"); c != "" {
		p["address"] = map[string]any{"addressCountry": c}
	}
	if n := text(d, "phone"); n != "" {
		p["phones"] = map[string]any{"primaryPhoneNumber": n}
	}
	return p
}

// leadForm is the activity a lead is: the form, every answer in its data, on the
// timeline of the person who filed it, at the moment it was filed.
func leadForm(d map[string]any, at time.Time) map[string]any {
	kind := kindOf(d)
	data := filed(d)
	data["kind"] = kind
	a := map[string]any{
		"source_id": text(d, "public_ref"),
		"kind":      "form",
		"direction": "in",
		"channel":   "web",
		"subject":   titles[kind],
		"at":        stamp(at),
		"person":    personID(text(d, "email")),
		"data":      data,
	}
	if a["subject"] == "" || a["subject"] == nil {
		a["subject"] = "Form"
	}
	set(a, "snippet", snippet(text(d, "need")))
	return a
}

// candidateRecord is the candidate a job application is.
func candidateRecord(d map[string]any, at time.Time) map[string]any {
	ref := text(d, "public_ref")
	c := map[string]any{
		"id":         candidateID(ref),
		"public_ref": ref,
		"emails":     email(text(d, "email")),
		"stage":      text(d, "stage"),
		"consent":    text(d, "consent"),
	}
	if c["stage"] == "" {
		c["stage"] = "new"
	}
	if c["consent"] == "" {
		c["consent"] = "unasked"
	}
	if n := text(d, "contact_name"); n != "" {
		c["name"] = name(n)
	}
	for _, f := range []string{"role", "job", "department", "location", "source"} {
		set(c, f, text(d, f))
	}
	resume := text(d, "resume_file")
	if resume == "" {
		resume = text(d, "resume")
	}
	set(c, "links", links(resume, d["links"]))
	set(c, "background", paragraphs(text(d, "background")))
	if answers := d["answers"]; answers != nil {
		c["answers"] = answers
	}
	applied := datetime(text(d, "applied"))
	if applied == "" {
		applied = stamp(at)
	}
	c["applied"] = applied
	return c
}

// links is a candidate's addresses as one links value: the resume first, named
// so, then every other address they gave. nil when they gave none.
func links(resume string, more any) any {
	var rest []string
	switch x := more.(type) {
	case []string:
		rest = x
	case []any:
		for _, l := range x {
			if s, ok := l.(string); ok && strings.TrimSpace(s) != "" {
				rest = append(rest, strings.TrimSpace(s))
			}
		}
	}
	primary, label := resume, "Resume"
	if primary == "" {
		if len(rest) == 0 {
			return nil
		}
		primary, label, rest = rest[0], "", rest[1:]
	}
	secondary := make([]map[string]any, 0, len(rest))
	for _, l := range rest {
		secondary = append(secondary, map[string]any{"url": l, "label": ""})
	}
	return map[string]any{"primaryLinkUrl": primary, "primaryLinkLabel": label, "secondaryLinks": secondary}
}

// candidateForm is the activity a job application is, on its candidate's timeline.
func candidateForm(d map[string]any, at time.Time) map[string]any {
	data := map[string]any{"kind": "careers"}
	for _, f := range []string{"job", "role", "department", "source", "public_ref"} {
		set(data, f, text(d, f))
	}
	subject := titles["careers"]
	if role := text(d, "role"); role != "" {
		subject += ": " + role
	}
	a := map[string]any{
		"source_id": text(d, "public_ref"),
		"kind":      "form",
		"direction": "in",
		"channel":   "web",
		"subject":   subject,
		"at":        stamp(at),
		"candidate": candidateID(text(d, "public_ref")),
		"data":      data,
	}
	set(a, "snippet", snippet(text(d, "background")))
	return a
}

// ── writing ───────────────────────────────────────────────────────────────────

// FileLead files one lead as the form sent it: the person, then the form on their
// timeline. d is in the form's field names (email, contact_name, company, size,
// role, need, country, source, stage, public_ref, the utm_* tags, coupon, program,
// offer, answers, account, account_org, intro_call, gist); at is when it was filed.
func FileLead(ctx context.Context, org, source string, d map[string]any, at time.Time) error {
	p := leadPerson(d)
	if err := person(ctx, org, source, p); err != nil {
		return err
	}
	if err := form(ctx, org, source, leadForm(d, at)); err != nil {
		return err
	}
	kind := kindOf(d)
	if l, ok := leadLists[kind]; ok && text(d, "email") != "" {
		listID := recordID("list", strings.ToLower(l.name))
		list := map[string]any{"id": listID, "name": l.name, "object": "people", "purpose": l.purpose}
		if err := record(ctx, org, source, Lists, list); err != nil {
			return err
		}
		if err := record(ctx, org, source, ListMembers, map[string]any{
			"id": recordID("member", listID+p["id"].(string)), "list": listID, "person": p["id"],
			"source": "api", "consent_at": stamp(at),
		}); err != nil {
			return err
		}
	}
	return nil
}

// FileCandidate files one job application: the candidate, then the form on their
// timeline. d is in the careers form's field names (email, contact_name, role, job,
// department, location, resume, links, background, answers, source, stage, consent,
// applied, public_ref).
func FileCandidate(ctx context.Context, org, source string, d map[string]any, at time.Time) error {
	if err := record(ctx, org, source, Candidates, candidateRecord(d, at)); err != nil {
		return err
	}
	if err := form(ctx, org, source, candidateForm(d, at)); err != nil {
		return err
	}
	if text(d, "consent") != "given" {
		return nil
	}
	// Only an applicant who ticked the box is a person on the careers list.
	p := map[string]any{"id": personID(text(d, "email")), "emails": email(text(d, "email")), "source": text(d, "source")}
	if n := text(d, "contact_name"); n != "" {
		p["name"] = name(n)
	}
	if err := person(ctx, org, source, p); err != nil {
		return err
	}
	if err := record(ctx, org, source, Lists, careers); err != nil {
		return err
	}
	return record(ctx, org, source, ListMembers, map[string]any{
		"id": recordID("member", careers["id"].(string)+p["id"].(string)), "list": careers["id"], "person": p["id"],
		"source": "api", "consent_at": stamp(at),
	})
}

// person writes a person the CRM may already hold: missing, it is created; held, it
// is given the values of fields still empty and nothing else. A composite (an
// address) is filled part by part, so a full address lands beside a country.
func person(ctx context.Context, org, source string, p map[string]any) error {
	held, err := get(ctx, org, People, p["id"].(string))
	if err != nil {
		return err
	}
	if held == nil {
		return upsert(ctx, org, source, People, "", p)
	}
	fill := map[string]any{"id": p["id"]}
	for k, v := range p {
		if k == "id" {
			continue
		}
		if empty(held[k]) {
			fill[k] = v
			continue
		}
		have, ok := held[k].(map[string]any)
		parts, _ := v.(map[string]any)
		if !ok || parts == nil {
			continue
		}
		merged, grew := map[string]any{}, false
		for part, x := range have {
			merged[part] = x
		}
		for part, x := range parts {
			if empty(have[part]) && !empty(x) {
				merged[part], grew = x, true
			}
		}
		if grew {
			fill[k] = merged
		}
	}
	if len(fill) == 1 {
		return nil
	}
	return upsert(ctx, org, source, People, "", fill)
}

// record writes a record that is created when it is missing and left as it is when
// it is there.
func record(ctx context.Context, org, source, collection string, r map[string]any) error {
	held, err := get(ctx, org, collection, r["id"].(string))
	if err != nil || held != nil {
		return err
	}
	return upsert(ctx, org, source, collection, "", r)
}

// form writes a form onto the timeline, keyed by its reference: a form filed once is
// one activity however often it is written.
func form(ctx context.Context, org, source string, a map[string]any) error {
	ref, _ := a["source_id"].(string)
	if ref == "" {
		return errors.New("crm: a form without a reference")
	}
	held, err := formOf(ctx, org, ref)
	if err != nil || held != nil {
		return err
	}
	return upsert(ctx, org, source, Activities, "source_id", a)
}

// amending orders, in this process, the callers of Amend that name one form; across
// replicas the form's lock is the database's (lock.Keyed.Across).
var amending lock.Keyed

// Amend changes the data of the form ref names: change is handed the data as it
// stands and reports whether it changed it, and a change is written whole. It
// answers the data as it stands after, or nil when ref names no form.
//
// It is one read-and-write at a time PER FORM, on every replica: two events about one
// form, delivered together to two pods, must not each write over the other's read. The
// form's records are Base's and this package has no store of its own, so the lock is
// held on the process's lock pool (locus.Locks) under the form's name.
func Amend(ctx context.Context, org, source, ref string, change func(data map[string]any) bool) (map[string]any, error) {
	return amend(ctx, &amending, org, source, ref, change)
}

func amend(ctx context.Context, k *lock.Keyed, org, source, ref string, change func(data map[string]any) bool) (map[string]any, error) {
	release, err := k.Across(ctx, nil, "amend:"+org+":"+ref)
	if err != nil {
		return nil, fmt.Errorf("crm: form %s is being changed elsewhere: %w", ref, err)
	}
	defer release()
	a, err := formOf(ctx, org, ref)
	if err != nil || a == nil {
		return nil, err
	}
	data, _ := a["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	if !change(data) {
		return data, nil
	}
	return data, upsert(ctx, org, source, Activities, "", map[string]any{"id": a["id"], "data": data})
}

// Offered reports whether the person an address is was mailed a program code on an
// earlier form: a code is given once per person, whatever they apply to after.
func Offered(ctx context.Context, org, address string) (bool, error) {
	page, err := listFn(plane.For(ctx, org), &client.BaseListIn{
		Collection: Activities, Filter: fmt.Sprintf(`person = %q && kind = "form"`, personID(address)),
		PerPage: 200, SkipTotal: true,
	})
	if err != nil {
		return false, err
	}
	for _, raw := range page.Items {
		var a struct {
			Data struct {
				Offer string `json:"offer"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &a) == nil && strings.TrimSpace(a.Data.Offer) != "" {
			return true, nil
		}
	}
	return false, nil
}

// Held is what the form ref names was filed with, its data, and nil when org's CRM
// holds no such form: a form filed again under its reference is the one already
// filed.
func Held(ctx context.Context, org, ref string) (map[string]any, error) {
	held, err := formOf(ctx, org, ref)
	if held == nil || err != nil {
		return nil, err
	}
	data, _ := held["data"].(map[string]any)
	if data == nil {
		data = map[string]any{}
	}
	return data, nil
}

// formOf is the activity a reference names, or nil.
func formOf(ctx context.Context, org, ref string) (map[string]any, error) {
	page, err := listFn(plane.For(ctx, org), &client.BaseListIn{
		Collection: Activities, Filter: fmt.Sprintf("source_id = %q", ref), PerPage: 1, SkipTotal: true,
	})
	if err != nil {
		return nil, err
	}
	if len(page.Items) == 0 {
		return nil, nil
	}
	var r map[string]any
	if err := json.Unmarshal(page.Items[0], &r); err != nil {
		return nil, err
	}
	return r, nil
}

// get is the record id names, or nil when there is none.
func get(ctx context.Context, org, collection, id string) (map[string]any, error) {
	out, err := getFn(plane.For(ctx, org), &client.BaseGetIn{Collection: collection, ID: id})
	if err != nil {
		var he *zip.HTTPError
		if errors.As(err, &he) && he.Status == http.StatusNotFound && strings.Contains(he.Error(), "no record") {
			return nil, nil
		}
		return nil, err
	}
	var r map[string]any
	if err := json.Unmarshal(out.Record, &r); err != nil {
		return nil, err
	}
	return r, nil
}

// upsert writes one record.
func upsert(ctx context.Context, org, source, collection, key string, r map[string]any) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = upsertFn(plane.For(ctx, org), &client.BaseUpsertIn{
		Collection: collection, Key: key, Source: source, Records: []json.RawMessage{raw},
	})
	return err
}

// empty is whether a stored value holds nothing: no value, no text, or a composite
// whose every part is empty.
func empty(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return strings.TrimSpace(x) == ""
	case float64:
		return x == 0
	case bool:
		return !x
	case []any:
		return len(x) == 0
	case map[string]any:
		for _, part := range x {
			if !empty(part) {
				return false
			}
		}
		return true
	}
	return false
}
