package client

// economic.go is the economic event: one payment between two orgs, whatever rail
// moved it, stated once on the event plane and read back by both parties to it.
//
// THE ORG IS THE PRINCIPAL. Buyer and Seller are IAM org ids. There is no second
// registry of economic identities, because an org already is one: it holds the
// ledger that paid or was paid, and it is what a tax form names.
//
// THIS FILE IS THE WHOLE SCHEMA, AND IT IS VERSIONED. V names the shape an event
// was stated in. A field is added by raising EconomicVersion, never by
// reinterpreting one that exists, because an event is never rewritten and a
// reader of a year-old event must read what was stated then.
//
// ONLY THE RAIL THAT MOVED THE MONEY SAYS WHAT IT WAS. A caller of the plane names
// a payment — its rail and its proof (EconomicNotice) — and nothing else; the event
// app asks the app that owns that rail what the proof records, and states what the
// rail answers. So the orgs, the amount and the time come from the rail's own
// records and never from whoever called, and a rail no app in the fleet owns
// cannot be stated at all.
//
// IDEMPOTENT ON (RAIL, PROOF). The id is derived from the rail and the proof and
// from nothing else (EconomicID), so a rail that states one payment twice — a
// retry, a reconcile, a redelivery — has stated one event.
//
// IMMUTABLE. A correction is a NEW event, with a proof of its own on the SAME rail,
// whose Corrects names the event it restates, between the same two orgs and at the
// same time. Only the rail states one, as it states everything; a party that
// disagrees with a payment files a dispute (EconomicDispute), which is a record
// beside the event and changes nothing about it. A reader that applies corrections
// reads the correction in the original's place; the original stays readable.
//
// THE WIRE SAYS WHAT HAPPENED AND NEVER WHAT IT MEANS FOR TAX. The rail says how
// the money moved. Whether a card payment is reported by the settlement entity,
// or a digital asset at its fair market value, is the reader's rule
// (apps/tax/rules.go) — an emitter that pre-classified would be a second tax
// engine, and the two would disagree the first time a rule changed.

import (
	"crypto/sha256"
	"encoding/hex"
)

// The two plane ops of the economic event, both answered by the event app.
const (
	// EventEconomic states one payment a rail recorded onto the event plane.
	EventEconomic = "event_economic"
	// EventEconomics lists the economic events the caller's org is a party to.
	EventEconomics = "event_economics"
)

// EconomicVersion is the schema version this file describes. Version 2 added
// Category.
const EconomicVersion = 2

// The rails. The set is closed so a reader can switch on it without a default
// that silently guesses.
const (
	// RailX402 is an x402 authorization settled between two orgs' balances.
	RailX402 = "x402"
	// RailLedger is a transfer between two orgs' balances on Hanzo's prepaid USD
	// ledger.
	RailLedger = "ledger"
	// RailChain is an on-chain transfer of a digital asset; Chain and Tx name it.
	RailChain = "chain"
	// RailACH is an ACH transfer.
	RailACH = "ach"
	// RailWire is a wire transfer.
	RailWire = "wire"
	// RailCard is a payment card charge, settled by a card network.
	RailCard = "card"
	// RailNetwork is a payment settled by a third-party settlement organization.
	RailNetwork = "network"
	// RailOther is any rail not named above.
	RailOther = "other"
)

// Rails is every rail, in the order above.
var Rails = []string{RailX402, RailLedger, RailChain, RailACH, RailWire, RailCard, RailNetwork, RailOther}

// The categories: what a payment was for, as its two parties agreed it on the
// rail that moved it. A category is what the parties said, not what it means for
// tax: a royalty here is a payment the buyer declared and the seller accepted as
// one, and apps/tax still decides what a year of them owes.
const (
	// CategoryService is a payment for work done.
	CategoryService = "service"
	// CategoryGoods is a payment for something delivered and owned.
	CategoryGoods = "goods"
	// CategoryTransfer moves value between two orgs with nothing bought.
	CategoryTransfer = "transfer"
	// CategoryRefund returns a payment, or the part of one a dispute gave back.
	CategoryRefund = "refund"
	// CategoryRoyalty is a payment for the use of something the seller owns.
	CategoryRoyalty = "royalty"
)

// Categories is every category, in the order above.
var Categories = []string{CategoryService, CategoryGoods, CategoryTransfer, CategoryRefund, CategoryRoyalty}

// EconomicEvent is one payment between two orgs.
type EconomicEvent struct {
	// V is the schema version the event was stated in.
	V int `json:"v"`
	// ID is the event's identity, derived from its rail and proof.
	ID string `json:"id"`
	// Buyer is the org that paid.
	Buyer string `json:"buyer"`
	// Seller is the org that was paid.
	Seller string `json:"seller"`
	// Agent is the principal that acted for the buyer, as IAM names it. Only the
	// buyer's copy carries it: it may name a person, and who inside the buyer
	// acted is none of the seller's records.
	Agent string `json:"agent,omitempty"`
	// Parent is the agent that spawned Agent, when one did. Only the buyer's copy
	// carries it, for Agent's reason.
	Parent string `json:"parent,omitempty"`
	// Service is what was bought, in the rail's own words.
	Service string `json:"service,omitempty"`
	// Currency is what the amounts are in: an ISO 4217 code or an asset symbol.
	Currency string `json:"currency"`
	// Gross is what the buyer paid, an exact decimal in Currency.
	Gross string `json:"gross"`
	// Fee is what the rail kept of Gross, an exact decimal in Currency.
	Fee string `json:"fee,omitempty"`
	// Tax is the tax collected within Gross, an exact decimal in Currency.
	Tax string `json:"tax,omitempty"`
	// FMV is Gross's fair market value in U.S. dollars when it was paid, an exact
	// decimal.
	FMV string `json:"fmv"`
	// Jurisdictions are the ISO 3166 codes the payment is subject to, when the
	// rail knows them.
	Jurisdictions []string `json:"jurisdictions,omitempty"`
	// Rail is how the money moved: x402, ledger, chain, ach, wire, card, network
	// or other.
	Rail string `json:"rail"`
	// Chain is the CAIP-2 network an on-chain payment moved on.
	Chain string `json:"chain,omitempty"`
	// Tx is the on-chain transaction hash of an on-chain payment.
	Tx string `json:"tx,omitempty"`
	// Proof is the rail's own reference for the payment: a settlement id, a chain
	// transaction hash or a processor reference.
	Proof string `json:"proof"`
	// At is when the payment was made, unix seconds, as the rail recorded it.
	At int64 `json:"at"`
	// Corrects is the id of the event this one restates, when it is a correction.
	Corrects string `json:"corrects,omitempty"`
	// Category is what the payment was for — service, goods, transfer, refund or
	// royalty — as the rail recorded the parties agreeing it. Empty on an event
	// stated before version 2, and on a rail that records no purpose.
	Category string `json:"category,omitempty"`
}

// EconomicID is the id of the event a rail states with this proof: the same rail
// and proof are always the same event, whatever else is said about it.
func EconomicID(rail, proof string) string {
	h := sha256.Sum256([]byte(rail + "\x00" + proof))
	return "econ_" + hex.EncodeToString(h[:])
}

// OnChain reports whether the payment moved on a chain, which makes it a payment
// in a digital asset.
func (e EconomicEvent) OnChain() bool { return e.Rail == RailChain || e.Tx != "" }

// EconomicNotice names one payment a rail recorded: the whole of what a caller of
// the event plane says, and what the plane asks the rail's owner about.
type EconomicNotice struct {
	// Rail is the rail that moved the payment.
	Rail string `json:"rail" validate:"required"`
	// Proof is the rail's own reference for it.
	Proof string `json:"proof" validate:"required"`
}

// EconomicStated is the receipt for a stated event.
type EconomicStated struct {
	// ID is the event's id.
	ID string `json:"id"`
	// Landed is whether both parties' rows are already in the warehouse. Until it
	// is true the rail still owes the statement and states it again.
	Landed bool `json:"landed"`
}

// EconomicsIn bounds a read of the caller org's economic events. There is no org
// field: the org is the caller's.
type EconomicsIn struct {
	// From is the inclusive start of the window, unix seconds.
	From int64 `json:"from"`
	// To is the exclusive end of the window, unix seconds.
	To int64 `json:"to"`
	// Counterparty keeps only events with this org on the other side.
	Counterparty string `json:"counterparty,omitempty"`
	// Rail keeps only events that moved on this rail.
	Rail string `json:"rail,omitempty"`
	// Cursor continues a read from the Next of the page before it.
	Cursor string `json:"cursor,omitempty"`
	// Limit is the most events to answer; zero takes the default.
	Limit int `json:"limit,omitempty"`
}

// Economics is one page of economic events, oldest first.
type Economics struct {
	// Rows are the events.
	Rows []EconomicEvent `json:"rows"`
	// Superseded are the ids in Rows that a correction restates.
	Superseded []string `json:"superseded,omitempty"`
	// Disputes are what either party has disputed about the events in Rows. A
	// dispute changes nothing about the event it names.
	Disputes []EconomicDispute `json:"disputes,omitempty"`
	// Next continues the read; empty on the last page.
	Next string `json:"next,omitempty"`
}

// EconomicDispute is one party's statement that it disagrees with a payment. It is
// a record beside the event, never an amendment of it: only the rail restates a
// payment.
type EconomicDispute struct {
	// Event is the id of the event disputed.
	Event string `json:"event"`
	// By is the org that disputes it: one of the event's two parties.
	By string `json:"by"`
	// Reason is what the party says is wrong.
	Reason string `json:"reason"`
	// At is when the dispute was filed, unix seconds.
	At int64 `json:"at"`
}
