package crm

// beta.go — the beta waitlist: one record per account, on the person.
//
// An account that applies to the beta (hanzo.ai/beta) holds ONE record: an
// activity of kind "form" on channel "beta", whose source_id is "beta:" and the
// account's IAM subject, on the timeline of the person the account's address is.
// Its data is the application as it stands (the program, the answers, the betas
// asked for) and the account's standing on the waitlist (its points and the
// actions that earned them). It is written whole on every change, so the record
// is what the account holds now; Base's history keeps every write.
//
// The waitlist's order is the beta leaderboard's (apps/leaderboard beta.go),
// which reads every record through Waiting, every lead that applied before it had
// an account through Applicants, and ranks them by one score.

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk/plane"
)

// Waitlist is the channel every waitlist record is on.
const Waitlist = "beta"

// on is the filter every waitlist record answers.
const on = `kind = "form" && channel = "beta"`

// standingRef is the source_id of the record the account sub holds.
func standingRef(sub string) string { return Waitlist + ":" + sub }

// Standing is the record the account sub holds, or nil when it holds none.
func Standing(ctx context.Context, org, sub string) (map[string]any, error) {
	return formOf(ctx, org, standingRef(sub))
}

// Stand writes the record the account sub holds, whole: the person its address
// is, then the record on their timeline, keyed by the account.
func Stand(ctx context.Context, org, address, sub string, data map[string]any, at time.Time) error {
	p := map[string]any{"id": personID(address), "emails": email(address), "source": "hanzo.ai/beta"}
	if err := person(ctx, org, "application", p); err != nil {
		return err
	}
	return upsert(ctx, org, "application", Activities, "source_id", map[string]any{
		"kind": "form", "direction": "in", "channel": Waitlist, "source_id": standingRef(sub),
		"subject": "Hanzo beta", "person": p["id"], "at": stamp(at), "data": data,
	})
}

// readPage is how many records one read asks for, and readPages how many reads a
// listing makes at most.
const (
	readPage  = 500
	readPages = 100
)

// each reads every activity that answers filter, in the order of sort, a page at
// a time, and hands each to keep as its fields.
func each(ctx context.Context, org, filter, sort string, keep func(json.RawMessage)) error {
	for page := 1; page <= readPages; page++ {
		got, err := listFn(plane.For(ctx, org), &client.BaseListIn{Collection: Activities, Filter: filter, Sort: sort, Page: page, PerPage: readPage, SkipTotal: true})
		if err != nil {
			return err
		}
		for _, raw := range got.Items {
			keep(raw)
		}
		if len(got.Items) < readPage {
			return nil
		}
	}
	return nil
}

// Wait is one waitlist record: its data as the beta ops write it, the person it
// is on, and when Base created the record.
type Wait struct {
	// Data is the record's data.
	Data json.RawMessage
	// Person is the CRM person the record is on: the one its account's address is.
	Person string
	// Created is Base's creation stamp, the order records began in.
	Created string
}

// Waiting is every waitlist record, oldest first.
func Waiting(ctx context.Context, org string) ([]Wait, error) {
	var out []Wait
	err := each(ctx, org, on, "created", func(raw json.RawMessage) {
		var a struct {
			Data    json.RawMessage `json:"data"`
			Person  string          `json:"person"`
			Created string          `json:"createdAt"`
		}
		if json.Unmarshal(raw, &a) == nil {
			out = append(out, Wait{Data: a.Data, Person: a.Person, Created: a.Created})
		}
	})
	return out, err
}

// programs are the kinds of lead that apply to a program of the beta: the form's
// `program`, which is its kind. A sales inquiry, a newsletter and an unsubscribe
// are leads too, and applied to nothing.
var programs = []string{"beta", "research", "partner"}

// applied is the filter every lead that applied to a program answers.
var applied = func() string {
	var kinds []string
	for _, p := range programs {
		kinds = append(kinds, fmt.Sprintf("data.kind = %q", p))
	}
	return `kind = "form" && channel = "web" && (` + strings.Join(kinds, " || ") + `)`
}()

// Applicant is one lead that applied to a program: the form as filed, before or
// without an account.
type Applicant struct {
	// Ref is the lead's reference (lead_…), the form's own id.
	Ref string
	// Person is the CRM person the form is on: one per address, however many forms
	// that address filed.
	Person string
	// At is when the form was filed, as Base stamps a date.
	At string
}

// Applicants is every lead that applied to a program, oldest filed first. A lead
// with no address is none: the person it would name is every address-less form.
func Applicants(ctx context.Context, org string) ([]Applicant, error) {
	var out []Applicant
	err := each(ctx, org, applied, "at", func(raw json.RawMessage) {
		var a struct {
			Ref     string `json:"source_id"`
			Person  string `json:"person"`
			At      string `json:"at"`
			Created string `json:"createdAt"`
			Data    struct {
				Email string `json:"email"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &a) != nil || a.Ref == "" || a.Person == "" || strings.TrimSpace(a.Data.Email) == "" {
			return
		}
		out = append(out, Applicant{Ref: a.Ref, Person: a.Person, At: cmp.Or(a.At, a.Created)})
	})
	return out, err
}

// Claimed reports whether a record other than the account sub's names the
// GitHub account login, so one GitHub account earns for one Hanzo account.
func Claimed(ctx context.Context, org, sub, login string) (bool, error) {
	n, err := tally(ctx, org, fmt.Sprintf(`%s && data.github = %q && source_id != %q`, on, login, standingRef(sub)))
	return n > 0, err
}

// tally is how many activities answer filter.
func tally(ctx context.Context, org, filter string) (int, error) {
	page, err := listFn(plane.For(ctx, org), &client.BaseListIn{Collection: Activities, Filter: filter, PerPage: 1})
	if err != nil {
		return 0, err
	}
	return page.TotalItems, nil
}
