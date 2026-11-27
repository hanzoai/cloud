package client

// planlink.go — the plan link: an org that holds no paid plan of its own uses one a
// person it trusts already pays for on another account.
//
// A link names the account whose recorded subscriptions the org's plan is read from. It
// moves no money and mints no credit: the org keeps its own wallet and its own included
// allotment, and the lent plan is never the org's to cancel. One plan is lent to at most
// one org, and an org borrows at most one plan.
//
// An account is written as the money core writes it: "<org>/<name>" for a person's
// wallet, "<org>" for an org's account (account.Account.Subject). The people who may
// link, and who may take a plan back, are decided at /v1/billing/link from the caller's
// own credential; the plane op trusts the Held list it is handed, as BillingSubscription
// trusts its Privileged flag.

// The ops, on the commerce plane.
const (
	BillingLink     = "billing_link"
	BillingLinkPut  = "billing_link_put"
	BillingLinkDrop = "billing_link_drop"
)

// The codes a refused link or unlink carries beside its status.
const (
	// LinkNoPlan: the account named is not one the caller may lend, or holds no live paid
	// plan (400).
	LinkNoPlan = "no_plan_to_link"
	// LinkOrgHasPlan: the org already holds a live paid plan of its own (409).
	LinkOrgHasPlan = "org_has_plan"
	// LinkPlanTaken: the plan is already lent to an org (409).
	LinkPlanTaken = "plan_already_linked"
	// LinkOrgTaken: the org already borrows a plan (409).
	LinkOrgTaken = "org_already_linked"
	// LinkOrgRefused: the org is one that cannot borrow a plan — the shared signup org, a
	// held or reserved org — or is the account's own (400).
	LinkOrgRefused = "org_cannot_link"
)

// PlanLink is the plan the org the caller acts in borrows.
type PlanLink struct {
	// Org is the org that borrows the plan.
	Org string `json:"org"`
	// From is the account the plan is read from: "<org>/<name>" or "<org>".
	From string `json:"from"`
	// Plan is the catalog rung the source serves now, "" when the source's plan has
	// lapsed.
	Plan string `json:"plan"`
	// PlanName is that rung's display name, "" when it has lapsed.
	PlanName string `json:"planName"`
	// By is the IAM row ("<owner>/<name>") of the person who linked it.
	By string `json:"by"`
	// At is when it was linked, RFC 3339.
	At string `json:"at"`
	// Live is false when the source's plan has lapsed: the link then confers nothing,
	// and stays until it is removed.
	Live bool `json:"live"`
}

// LendablePlan is a live paid plan the caller holds and could lend.
type LendablePlan struct {
	// From is the account that holds it.
	From string `json:"from"`
	// Plan is the catalog rung it serves.
	Plan string `json:"plan"`
	// PlanName is that rung's display name.
	PlanName string `json:"planName"`
	// Org is the org it is already lent to, omitted when it is free to link.
	Org string `json:"org,omitempty"`
}

// PlanLinkState is the plan link of the org the caller acts in, and the plans the caller
// could lend it.
type PlanLinkState struct {
	// Link is the plan the org borrows, omitted when it borrows none.
	Link *PlanLink `json:"link,omitempty"`
	// Plans is every live paid plan the CALLER holds, always an array.
	Plans []LendablePlan `json:"plans"`
	// CanLink is true only when the caller may link a plan to the org they act in right
	// now: they are its owner or admin, the org may borrow, it holds no live paid plan
	// of its own and borrows none. It says nothing of whether the caller holds a plan to
	// lend; Plans does.
	CanLink bool `json:"canLink"`
	// CanUnlink is true only when the org borrows a plan and the caller may end it: they
	// are an owner or admin of the org, or hold the account the plan is lent from.
	CanUnlink bool `json:"canUnlink"`
}

// PlanLinkIn asks the state of the acting org's link. Held is the accounts the caller
// holds, decided from the caller's credential.
type PlanLinkIn struct {
	Held []string `json:"held"`
	// Privileged is an owner or admin of the acting org, decided from the caller's
	// credential.
	Privileged bool `json:"privileged,omitempty"`
}

// PlanLinkPutIn links From to the acting org.
type PlanLinkPutIn struct {
	// From is the account to lend, one of Held.
	From string `json:"from"`
	// Held is the accounts the caller holds, decided from the caller's credential.
	Held []string `json:"held"`
	// By is the caller's IAM row, kept as who linked it.
	By string `json:"by"`
	// Privileged is an owner or admin of the acting org, decided from the caller's
	// credential.
	Privileged bool `json:"privileged,omitempty"`
}

// PlanLinkDropIn ends a link: the acting org's when From is empty, else the one From's plan
// is lent through.
type PlanLinkDropIn struct {
	From       string   `json:"from,omitempty"`
	Held       []string `json:"held"`
	Privileged bool     `json:"privileged,omitempty"`
}
