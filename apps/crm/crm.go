package crm

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/brand"
)

// Module is the CRM's product: the entitlement an org holds the CRM by (the beta,
// apps/entitlement Beta), and the Base module its collections come from.
const Module = "crm"

// Sizes is the closed set a lead's company size is one of, in the order a form
// offers them.
var Sizes = []string{"1-49", "50-249", "250-999", "1000-4999", "5000+"}

// Campaign is the closed set of campaign tags a lead records, in the order a
// link carries them: the utm_* keys of the address the visitor arrived on.
var Campaign = []string{"utm_source", "utm_medium", "utm_campaign", "utm_term", "utm_content"}

// Programs is the closed set of programs a lead may have applied to, in the order
// a form offers them. A lead with none is a sales inquiry.
var Programs = []string{"beta", "research", "partner"}

// Ref is a new outward reference: prefix and 96 random bits in hex. A lead's is
// "lead_…", a candidate's "cand_…"; the intake mints one for every form it files,
// and it is the form's key (an activity's source_id), never a record's id, so it
// says nothing about how many the team has.
func Ref(prefix string) (string, error) {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

// IntroCall is the brand's booking page opened on one lead: their name and
// address filled in, and the reference in the notes so the booking and the lead
// are one person. Only a sales inquiry is offered one, and only in the brand's own
// org, because only the brand's team books on the brand's page; any other org's
// lead, and any brand without a booking page, carries none. data is in the form's
// field names (contact_name, email).
func IntroCall(org string, data map[string]any, ref string) string {
	if org != cloud.Brand() || !brand.Registered(org) {
		return ""
	}
	page := brand.For(org).Intro
	if page == "" {
		return ""
	}
	q := url.Values{"notes": {"Lead " + ref}}
	for param, field := range map[string]string{"name": "contact_name", "email": "email"} {
		if v, _ := data[field].(string); v != "" {
			q.Set(param, v)
		}
	}
	return page + "?" + q.Encode()
}
