package crm

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// A form of the kind program, as the website files it.
func formOfKind(ref, address, program string) map[string]any {
	return map[string]any{"email": address, "contact_name": "Someone", "source": "hanzo.ai/" + program, "program": program, "public_ref": ref}
}

// The applicants are the leads that applied to a program, whoever they are: the
// ones that asked something else, and the ones that took themselves off the mail,
// applied to nothing.
func TestApplicantsAreTheLeadsThatAppliedToAProgram(t *testing.T) {
	ctx := open(t)
	at := func(h int) time.Time { return filedAt.Add(time.Duration(h) * time.Hour) }
	for _, f := range []struct {
		ref, address, program string
		h                     int
	}{
		{"lead_aaaaaaaaaaaaaaaaaaaaaaaa", "ada@example.com", "beta", 3},
		{"lead_bbbbbbbbbbbbbbbbbbbbbbbb", "ada@example.com", "research", 5},
		{"lead_cccccccccccccccccccccccc", "bob@example.com", "partner", 1},
		{"lead_dddddddddddddddddddddddd", "cy@example.com", "sales", 2},
		{"lead_eeeeeeeeeeeeeeeeeeeeeeee", "dee@example.com", "unsubscribe", 4},
		{"lead_ffffffffffffffffffffffff", "eve@example.com", "newsletter", 6},
		{"lead_000000000000000000000000", "", "beta", 7},
		// dee took themselves off the mail and also applied: the application stands.
		{"lead_111111111111111111111111", "dee@example.com", "beta", 8},
	} {
		if err := FileLead(ctx, org, "application", formOfKind(f.ref, f.address, f.program), at(f.h)); err != nil {
			t.Fatalf("file %s: %v", f.ref, err)
		}
	}
	got, err := Applicants(ctx, org)
	if err != nil {
		t.Fatalf("applicants: %v", err)
	}
	var refs []string
	for _, a := range got {
		refs = append(refs, a.Ref[:6])
	}
	// Oldest filed first: bob's partner form, ada's beta, ada's research, and dee's
	// beta, whose address is on the unsubscribe list: that list is the mail's, and
	// the board holds the application, anonymous, and writes to no one.
	if want := []string{"lead_c", "lead_a", "lead_b", "lead_1"}; !slices.Equal(refs, want) {
		t.Fatalf("applicants = %v, want %v", refs, want)
	}
	if got[1].Person != personID("ada@example.com") || got[2].Person != got[1].Person {
		t.Fatalf("one address is one person: %+v", got)
	}
	if !strings.HasPrefix(got[1].At, "2026-10-19 00:30:00") {
		t.Fatalf("at = %q, when the form was filed", got[1].At)
	}
}

// A waitlist record names the person its account's address is, so a lead filed
// from the same address is the same person's.
func TestAWaitlistRecordNamesItsPerson(t *testing.T) {
	ctx := open(t)
	if err := Stand(ctx, org, "Ada@Example.com", "sub-ada", map[string]any{"user": "sub-ada", "program": "beta"}, filedAt); err != nil {
		t.Fatalf("stand: %v", err)
	}
	got, err := Waiting(ctx, org)
	if err != nil || len(got) != 1 {
		t.Fatalf("waiting: %v %v", got, err)
	}
	if got[0].Person != personID("ada@example.com") {
		t.Fatalf("person = %q, want the address's person", got[0].Person)
	}
}
