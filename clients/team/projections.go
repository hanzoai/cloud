package team

// This file is the member→Person/Employee projection — the bots-as-members
// vocabulary. It is ported from github.com/hanzoai/team-go/pkg/transactor's
// ingest.go projection layer, scoped to Phase 1 (member roster). The chat
// Channel/Message projections and the Base-collection mirror hooks are Phase 2
// (chat/slack/files/mirror) and are intentionally NOT ported here.

import (
	"strings"
	"time"
)

// Contact class ids the projection materializes. Kept in one place so the
// Huly-model wire identity lives with the code that builds it.
const (
	clPerson         = "contact:class:Person"
	mixinEmployee    = "contact:mixin:Employee"
	clSocialIdentity = "contact:class:SocialIdentity"
	spaceContacts    = "contact:space:Contacts"
)

// ── tx builders (the Huly-model knowledge, one place) ────────────────────────

func createTx(objectID, objectClass, space, modifiedBy string, attrs map[string]any) map[string]any {
	now := time.Now().UnixMilli()
	return map[string]any{
		"_class": clTxCreate, "objectId": objectID, "objectClass": objectClass,
		"objectSpace": space, "modifiedBy": modifiedBy, "modifiedOn": now,
		"createdBy": modifiedBy, "createdOn": now, "attributes": attrs,
	}
}

// updateTx sets only the given operations on an existing doc (get→apply→put),
// preserving every field it does not name — the anti-clobber primitive.
func updateTx(objectID, objectClass, space, modifiedBy string, ops map[string]any) map[string]any {
	now := time.Now().UnixMilli()
	return map[string]any{
		"_class": clTxUpdate, "objectId": objectID, "objectClass": objectClass,
		"objectSpace": space, "modifiedBy": modifiedBy, "modifiedOn": now,
		"operations": ops,
	}
}

func attachedCreateTx(objectID, objectClass, space, attachedTo, attachedToClass, collection, modifiedBy string, attrs map[string]any) map[string]any {
	t := createTx(objectID, objectClass, space, modifiedBy, attrs)
	t["attachedTo"] = attachedTo
	t["attachedToClass"] = attachedToClass
	t["collection"] = collection
	return t
}

func mixinTx(objectID, objectClass, space, mixin, modifiedBy string, attrs map[string]any) map[string]any {
	now := time.Now().UnixMilli()
	return map[string]any{
		"_class": clTxMixin, "objectId": objectID, "objectClass": objectClass,
		"objectSpace": space, "mixin": mixin, "modifiedBy": modifiedBy, "modifiedOn": now,
		"attributes": attrs,
	}
}

// ── member projection ─────────────────────────────────────────────────────────

// Member is the projection of a team member (human or bot) rendered as a Person +
// Employee in the SPA directory.
type Member struct {
	UserID string // account uuid (also the Person.personUuid + social key)
	Name   string // display name
	Role   string // owner/admin/member — surfaced on the Employee mixin
	IsBot  bool
	Active bool // Employee.active — drives Team/Employee-list membership
}

// PersonRef is the deterministic Person _id for a member account — stable so
// re-syncs upsert in place (never duplicate a member).
func PersonRef(userID string) string { return "person-" + userID }

// MemberTxes builds the txes to project a member. When the Person already EXISTS
// it only UPDATES the mirror-owned name + refreshes the Employee mixin (which
// merges, never replaces) — so avatar/city/birthday/profile and any SPA-set mixin
// survive. On first projection it creates the Person + the hanzo social identity.
// The Employee mixin (active) is what puts the member in the Team list and fires
// the PersonSpace trigger.
func MemberTxes(m Member, exists bool) []map[string]any {
	pid := PersonRef(m.UserID)
	name := hulyName(pick(m.Name, m.UserID))
	role := strings.ToUpper(pick(m.Role, "member"))
	position := ""
	if m.IsBot {
		position = "Agent"
	}
	var txes []map[string]any
	if exists {
		// Refresh the mirror-owned display fields (name + avatar placeholder) in
		// place; SPA-owned profile fields (city, avatar upload, …) are untouched.
		txes = append(txes, updateTx(pid, clPerson, spaceContacts, acctSystem, map[string]any{
			"name": name, "avatarType": "color",
		}))
	} else {
		txes = append(txes, createTx(pid, clPerson, spaceContacts, acctSystem, map[string]any{
			"name": name, "personUuid": m.UserID, "city": "", "avatarType": "color",
		}))
	}
	txes = append(txes, mixinTx(pid, clPerson, spaceContacts, mixinEmployee, acctSystem, map[string]any{
		"active": m.Active, "role": role, "position": position,
	}))
	if !exists {
		socialKey := "hanzo:" + m.UserID
		txes = append(txes, attachedCreateTx(socialKey, clSocialIdentity, spaceContacts, pid, clPerson, "socialIds", acctSystem, map[string]any{
			"key": socialKey, "type": "hanzo", "value": m.UserID, "verifiedOn": time.Now().UnixMilli(),
		}))
	}
	return txes
}

// ── helpers ──────────────────────────────────────────────────────────────────

// hulyName formats a display name into Huly's canonical Person.name convention
// "last,first" (the SPA renders it "first last"; a native SPA person stores ","
// for an empty name). A single-token name (most bots) has no last name, so it
// becomes the first name (",token") — rendered verbatim. Empty stays ",".
func hulyName(display string) string {
	display = strings.TrimSpace(display)
	if display == "" {
		return ","
	}
	if i := strings.LastIndex(display, " "); i > 0 {
		return strings.TrimSpace(display[i+1:]) + "," + strings.TrimSpace(display[:i])
	}
	return "," + display
}

// pick returns a when non-empty, else b.
func pick(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
