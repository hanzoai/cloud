// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

// masquerade.go — the operator line through the event plane.
//
// A platform operator acts as someone else in two ways, and both leave the same
// mark on every event written under them:
//
//   - an impersonation token: IAM mints the TARGET's claims and names the operator
//     in `act` (`imp: true`). The identity boundary reads the verified `act` and
//     mints it as zip.HeaderActedBy, which zip forwards across every plane hop.
//   - an org switch: a SuperAdmin selects another tenant with X-Org-Id and acts
//     there as themselves (crossOrgHome, audit_middleware.go, is the same predicate).
//
// THE MARK IS THE SERVER'S. Two attributes, impersonated=true and
// impersonated_by=<operator>, are written from the request's verified identity and
// from nothing in the body: mark drops both keys from whatever a caller sent before
// it writes them, so a client can neither forge the mark nor strip it. A third,
// $subject, is the IAM subject the boundary verified behind the write — the one
// attribution of a person that a key holder cannot state for someone else.
//
// OPERATOR TRAFFIC IS OURS. Like a test account's, it stays in our stream and never
// reaches a sink an integration forwards to an ad platform (fanOutEvents): an
// operator reproducing a checkout is not the customer's conversion.
//
// THE READS DEFAULT TO THE CUSTOMER. Every product-event lens counts the customer's
// own traffic unless asked otherwise (traffic below), so an operator reproducing a
// bug inside a tenant does not show up as that tenant's visitor, pageview or order.

package event

import (
	"cmp"
	"strings"

	"github.com/zap-proto/zip"
)

// The attributes only the server writes.
const (
	attrImpersonated   = "impersonated"
	attrImpersonatedBy = "impersonated_by"
	// attrSubject is the IAM subject the boundary verified for the request that
	// wrote the event. Absent on a write a key alone admitted, which names no person.
	attrSubject = "$subject"
)

// signer is who the boundary verified behind a write: the subject the credential
// names, and the operator acting as them when someone is.
type signer struct {
	subject  string
	operator string
}

// signerOf reads the signer off the identity the boundary minted — the request's,
// or the one a peer forwarded over the plane.
func signerOf(who zip.Caller) signer {
	return signer{subject: strings.TrimSpace(who.User), operator: operatorOf(who)}
}

// mark writes the server's attributes onto an event's properties and is the only
// writer of them: whatever the caller sent under those keys is dropped first, and
// they are written back from the signer alone. It returns a copy and never touches
// the caller's map, on withSource's terms. ingestEvents applies it to every event
// before the row is built and before the batch fans out, so the stored row, the
// webhook envelope and every sink read one mark.
func mark(p map[string]any, s signer) map[string]any {
	_, a := p[attrImpersonated]
	_, b := p[attrImpersonatedBy]
	_, c := p[attrSubject]
	if s == (signer{}) && !a && !b && !c {
		return p
	}
	out := make(map[string]any, len(p)+3)
	for k, v := range p {
		if k != attrImpersonated && k != attrImpersonatedBy && k != attrSubject {
			out[k] = v
		}
	}
	if s.subject != "" {
		out[attrSubject] = s.subject
	}
	if s.operator != "" {
		out[attrImpersonated] = "true"
		out[attrImpersonatedBy] = s.operator
	}
	return out
}

// operated reports whether mark put the operator mark on these properties.
func operated(p map[string]any) bool { v, _ := p[attrImpersonated].(string); return v == "true" }

// operatorOf names the platform operator behind a call made as somebody else, and
// is empty for everyone acting as themselves. who is the identity the boundary
// minted (the request's, or the one a peer forwarded over the plane), so every
// field here is a verified claim rather than a statement in the body.
//
// The operator is spelled `<owner>/<name>`, IAM's own spelling of an actor — the
// form `act.sub` carries — so both ways of acting as someone else name the same
// person the same way.
func operatorOf(who zip.Caller) string {
	if strings.TrimSpace(who.User) == "" {
		return "" // no validated principal: nobody to name
	}
	if by := strings.TrimSpace(who.ActedBy); by != "" {
		return by
	}
	home, org := strings.TrimSpace(who.Owner), strings.TrimSpace(who.Org)
	if who.Admin && home != "" && org != "" && org != home {
		return home + "/" + cmp.Or(strings.TrimSpace(who.Name), strings.TrimSpace(who.User))
	}
	return ""
}

// traffic is which side of the operator mark a lens counts.
type traffic string

const (
	// customers is the default: every impersonated row left out, so a metric is
	// the tenant's own.
	customers traffic = "exclude"
	// everyone counts both sides.
	everyone traffic = "include"
	// operators counts only what operators did while acting as someone else.
	operators traffic = "only"
)

// trafficOf reads the `impersonated` query value. Empty is the default; anything
// it does not name is the caller's mistake and a 400, never a silent default.
func trafficOf(s string) (traffic, error) {
	switch t := traffic(strings.ToLower(strings.TrimSpace(s))); t {
	case "", customers:
		return customers, nil
	case everyone, operators:
		return t, nil
	}
	return "", zip.ErrBadRequest("impersonated must be exclude, include or only")
}

// clause is the predicate a lens appends after its tenant, signal and time bounds.
// A server constant: nothing a caller sends reaches the SQL.
func (t traffic) clause() string {
	switch t {
	case everyone:
		return ""
	case operators:
		return " AND attributes['" + attrImpersonated + "'] = 'true'"
	}
	return " AND attributes['" + attrImpersonated + "'] != 'true'"
}
