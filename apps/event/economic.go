// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// economic.go — A PAYMENT BETWEEN TWO ORGS, AS A FACT ON THE PLANE.
//
// The schema is client.EconomicEvent and it is written down there, once. This file
// is how one reaches the plane and comes back off it: a rail names a payment over
// the plane (planeEconomic), this app asks the rail's owner what it records, the
// bus carries it on event.economic, the sink lands it in event.fact, and both
// parties read it back (economics).
//
// THE RAIL SAYS WHAT HAPPENED, AND NOBODY ELSE. A caller names a rail and a proof,
// and that is all it says. The payment itself — both orgs, the amount, the time — is
// what the rail's OWNER answers for that proof, asked on the owner's own socket
// (owners). So a caller cannot put a payment into anyone's books: it can only ask
// that a payment the rail already recorded be stated as the rail recorded it, which
// is idempotent. A rail no app owns cannot be stated at all. The address is the
// authority here as it is everywhere on the plane: only the x402 app answers on
// x402's socket.
//
// ONE ROW PER PARTY. The plane's tenant is a column and every read binds it, so a
// payment between two orgs is written twice — once into the buyer's partition and
// once into the seller's — and each reads its own. A third org has no row to read,
// which is the whole of the isolation: it is not a filter a read could forget. The
// two rows are the same payment but not the same bytes: the seller's copy carries
// no principal of the buyer's (copyFor).
//
// A ROW'S TIME IS THE RAIL'S. event.fact is keyed (org, time, id), and a row's time
// is when the payment was made AS THE RAIL RECORDED IT — never a clock of this
// server's, which a restart, a replica or a skew could move. Every statement of one
// payment therefore lands at the same time, and its id is the digest of the party's
// whole copy, so a restatement is the same row and collapses into it on merge. A
// statement that says something DIFFERENT about a payment already landed is refused
// (landed), and if one ever races in, a read takes each event at its least (time,
// id): a function of what the rail said, the same on every replica and every read.
//
// A PAGE COSTS A PAGE. Because the time is the payment's, a window is a range of the
// sort key and a cursor is a point in it: a read walks (time, event) forward from the
// cursor and stops at the page (economicSQL), whatever the org has stated before.
//
// A CORRECTION IS THE RAIL'S, AND A DISPUTE IS THE PARTY'S. A correction is a new
// event whose Corrects names the event it restates. It is stated like any other —
// by the rail's owner — and it is refused unless the event it names has landed on
// the SAME rail, between the SAME two orgs, at the SAME time, so it can restate a
// payment and never move one into another year or onto another rail. A party that
// disagrees files a dispute (dispute): a record both parties read beside the event,
// which amends nothing.
//
// NEVER ABANDONED. The sink gives up on an ordinary fact after maxDeliver failed
// inserts. An economic fact is a payment that already moved, so its writer is kept
// (warehouse.go): it waits on the stream until the warehouse takes it. And the
// stream is not what makes it durable: the rail keeps owing a statement until this
// app answers that both rows have LANDED, so a warehouse down for longer than the
// stream holds a fact loses nothing — the rail states it again.
//
// KEPT TEN YEARS. Once landed, a row is kept for the longest period a tax year may
// still be asked about, and no longer: event.fact's TTL for the signal, declared
// with the table in hanzoai/o11y (deploy/datastore/migrations/0004_event_economic.sql).

package event

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/hanzoai/cloud/client"
	x402peer "github.com/hanzoai/cloud/client/x402"
	"github.com/hanzoai/cloud/money"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/decimal"
	"github.com/zap-proto/zip"
)

// exposeEconomic publishes the two plane ops. build calls it.
func exposeEconomic() {
	cloud.Plane().Post("/event/economic", planeEconomic,
		zip.WithOperationID(client.EventEconomic),
		zip.WithSummary("State one payment a rail recorded onto the event plane"))
	cloud.Plane().Post("/event/economics", planeEconomics,
		zip.WithOperationID(client.EventEconomics),
		zip.WithSummary("List the economic events the caller's org is a party to"))
}

// owners is, per rail, the one app that may say what a payment on it was: the app
// that moved the money, asked on its own socket. A rail with no owner here has no
// app in this fleet that moves money on it, and nothing states a payment on it.
var owners = map[string]func(context.Context, *client.EconomicNotice) (*client.EconomicEvent, error){
	client.RailX402: x402peer.X402Economic,
}

// States one payment a rail recorded onto both parties' event plane, as the rail's
// owner answers it, and answers whether both parties' rows have landed.
func planeEconomic(ctx context.Context, in *client.EconomicNotice) (*client.EconomicStated, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("economic: no org on the call, so the payment is stated for no one")
	}
	ask, ok := owners[in.Rail]
	if !ok {
		return nil, zip.ErrForbidden(fmt.Sprintf("economic: no app in this fleet owns rail %q, so nothing states a payment on it", in.Rail))
	}
	if in.Proof == "" || !plain(in.Proof, maxEconomicText) {
		return nil, zip.ErrBadRequest(fmt.Sprintf("economic: proof must be 1 to %d printable characters", maxEconomicText))
	}
	said, err := ask(ctx, in)
	if err != nil {
		return nil, err // the rail's own answer: an unknown or unsettled proof is its to refuse
	}
	if said == nil || said.Rail != in.Rail || said.Proof != in.Proof {
		return nil, zip.Errorf(http.StatusBadGateway, "economic: rail %s answered for a payment it was not asked about", in.Rail)
	}
	e, err := canonical(*said)
	if err != nil {
		return nil, zip.Errorf(http.StatusBadGateway, "economic: rail %s: %v", in.Rail, err)
	}
	if org != e.Buyer && org != e.Seller {
		return nil, zip.ErrForbidden("economic: the call acts for " + org + ", which is party to neither side")
	}
	facts := economicFacts(e)
	if err := requireDatastore(ctx); err != nil {
		if e.Corrects != "" {
			return nil, err // a correction is stated only against the event it names
		}
		// No warehouse to ask: put it on the stream, and the rail asks again.
		if err := publish(ctx, facts); err != nil {
			return nil, err
		}
		return &client.EconomicStated{ID: e.ID}, nil
	}
	if e.Corrects != "" {
		if err := restates(ctx, e); err != nil {
			return nil, err
		}
	}
	done, err := landed(ctx, facts)
	if err != nil {
		return nil, err
	}
	if !done {
		if err := publish(ctx, facts); err != nil {
			return nil, err
		}
	}
	return &client.EconomicStated{ID: e.ID, Landed: done}, nil
}

// Lists, oldest first, the economic events the caller's org is a party to in a window.
func planeEconomics(ctx context.Context, in *client.EconomicsIn) (*client.Economics, error) {
	org, ok := cloud.Tenant(ctx)
	if !ok {
		return nil, zip.ErrForbidden("economics: no org on the call")
	}
	return economics(ctx, org, *in)
}

// economicQuery is the per-org read's window and filters.
type economicQuery struct {
	// Year is the calendar year to read, UTC.
	Year int `json:"year" validate:"required"`
	// Counterparty keeps only payments with this org on the other side.
	Counterparty string `json:"counterparty"`
	// Rail keeps only payments that moved on this rail.
	Rail string `json:"rail"`
	// Cursor continues from the next of the page before; empty starts the year.
	Cursor string `json:"cursor"`
	// Limit is how many events to answer, default 100 and at most 1000.
	Limit int `json:"limit"`
}

// Returns, oldest first and paged, the payments the caller's org made or received in
// a year, with the ones a correction restates named and what either party disputes
// — for the org's admins.
//
// Example: {"year": 2026, "rail": "x402"}
func (o readOps) economic(ctx context.Context, in *economicQuery) (*client.Economics, error) {
	org, err := economicAdmin(ctx)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Year()
	if in.Year < economicEpoch.Year() || in.Year > now {
		return nil, zip.ErrBadRequest(fmt.Sprintf("economic: year must be %d through %d", economicEpoch.Year(), now))
	}
	from := time.Date(in.Year, time.January, 1, 0, 0, 0, 0, time.UTC)
	out, err := economics(ctx, org, client.EconomicsIn{
		From: from.Unix(), To: from.AddDate(1, 0, 0).Unix(),
		Counterparty: in.Counterparty, Rail: in.Rail, Cursor: in.Cursor, Limit: in.Limit,
	})
	noStore(ctx, err)
	return out, err
}

// disputeIn names the payment a party disputes, and why.
type disputeIn struct {
	// Event is the id of the economic event disputed.
	Event string `json:"event" validate:"required"`
	// Reason is what the caller's org says is wrong, at most 1024 characters.
	Reason string `json:"reason" validate:"required"`
}

// Records that the caller's org disputes one payment it is party to — for the org's
// admins. A dispute is a record both parties read beside the payment, and it changes
// nothing about it: the amount, the parties and the tax year stay as the rail stated
// them, because only the rail that moved the money restates a payment.
//
// Example: {"event": "econ_4f1c…", "reason": "the service was never delivered"}
func (o readOps) dispute(ctx context.Context, in *disputeIn) (*client.EconomicDispute, error) {
	org, err := economicAdmin(ctx)
	if err != nil {
		return nil, err
	}
	if !idShape.MatchString(in.Event) {
		return nil, zip.ErrBadRequest("economic: event is not an economic event's id")
	}
	reason := strings.TrimSpace(in.Reason)
	if reason == "" || !plain(reason, maxDisputeText) {
		return nil, zip.ErrBadRequest(fmt.Sprintf("economic: reason must be 1 to %d printable characters", maxDisputeText))
	}
	if err := requireDatastore(ctx); err != nil {
		return nil, err
	}
	q, args := eventSQL(org, in.Event)
	rows, err := warehouseQuery(ctx, q, args...)
	if err != nil {
		return nil, warehouseErr("economic", err)
	}
	found, err := economicRows(rows)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	if len(found) == 0 {
		return nil, zip.ErrNotFound("economic: " + org + " is party to no event " + in.Event)
	}
	d := client.EconomicDispute{Event: in.Event, By: org, Reason: scrubEconomic(reason), At: time.Now().Unix()}
	if err := publish(ctx, disputeFacts(found[0], d)); err != nil {
		return nil, err
	}
	return &d, nil
}

// economicAdmin is the caller's org, when the caller administers it: an org's
// payments are its admins' to read and to dispute.
func economicAdmin(ctx context.Context) (string, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return "", err
	}
	if !cloud.Admin.Admits(cloud.AuthorityIn(ctx)) {
		return "", zip.ErrForbidden("economic: an org's payments are its admins'")
	}
	return org, nil
}

// noStore keeps an org's books out of every cache on the way back.
func noStore(ctx context.Context, err error) {
	if c, ok := cloud.Request(ctx); ok && err == nil {
		c.SetHeader("Cache-Control", "no-store")
	}
}

// ── the statement ────────────────────────────────────────────────────────────

// economicEpoch is the earliest payment a rail may state: before any rail here
// existed, so an earlier time is a unit error (milliseconds, a zero) and never a
// payment. It also bounds the key range an economic part can span.
var economicEpoch = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)

// Bounds on what one event carries. Every value lands in a row both parties read.
const (
	maxEconomicText   = 256
	maxEconomicPlaces = 16
	maxDisputeText    = 1024
)

var (
	// currencyShape is an ISO 4217 code or an asset symbol.
	currencyShape = regexp.MustCompile(`^[A-Z0-9]{2,12}$`)
	// chainShape is CAIP-2: a namespace and a reference.
	chainShape = regexp.MustCompile(`^[-a-z0-9]{3,8}:[-_a-zA-Z0-9]{1,32}$`)
	// txShape is a transaction hash in any chain's alphabet.
	txShape = regexp.MustCompile(`^[A-Za-z0-9]{1,128}$`)
	// placeShape is ISO 3166-1 alpha-2, or an ISO 3166-2 subdivision.
	placeShape = regexp.MustCompile(`^[A-Z]{2}(-[A-Z0-9]{1,3})?$`)
	// idShape is what client.EconomicID answers.
	idShape = regexp.MustCompile(`^econ_[0-9a-f]{64}$`)
	// uuidShape is the org the warehouse refuses (tenant_is_a_slug): a uuid is an
	// id that was never resolved to its org.
	uuidShape = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	// tinShape is a taxpayer identification number as a person types one: an SSN
	// or ITIN (123-45-6789), an EIN (12-3456789), or nine bare digits.
	tinShape = regexp.MustCompile(`\b(?:\d{3}-\d{2}-\d{4}|\d{2}-\d{7}|\d{9})\b`)
)

// scrubEconomic is the one scrub for the free text an economic row carries — what
// was bought, and why a party disputes it: every row lands in both parties' books
// for years, so no credential, address or taxpayer number rides in one.
func scrubEconomic(s string) string {
	return tinShape.ReplaceAllString(scrubText(s), "[redacted]")
}

// canonical checks what a rail answered and returns it in its one canonical form:
// the version and id stamped, every amount in its exact shortest spelling, the
// jurisdictions sorted. The canonical form is what is stored and what the row id
// digests, so two statements that mean the same thing are the same row.
func canonical(e client.EconomicEvent) (client.EconomicEvent, error) {
	bad := func(format string, a ...any) (client.EconomicEvent, error) {
		return client.EconomicEvent{}, fmt.Errorf(format, a...)
	}
	if e.V != 0 && e.V != client.EconomicVersion {
		return bad("version %d is not %d", e.V, client.EconomicVersion)
	}
	e.V = client.EconomicVersion
	for _, org := range []string{e.Buyer, e.Seller} {
		if !principal.NamesOrg(org) || uuidShape.MatchString(org) {
			return bad("%q is not an org", org)
		}
	}
	if e.Buyer == e.Seller {
		return bad("the buyer and the seller are one org")
	}
	if !slices.Contains(client.Rails, e.Rail) {
		return bad("rail %q is not one of %v", e.Rail, client.Rails)
	}
	if e.Proof == "" || !plain(e.Proof, maxEconomicText) {
		return bad("proof must be 1 to %d printable characters", maxEconomicText)
	}
	id := client.EconomicID(e.Rail, e.Proof)
	if e.ID != "" && e.ID != id {
		return bad("id %q is not the id of rail %s and this proof", e.ID, e.Rail)
	}
	e.ID = id
	for _, s := range []string{e.Agent, e.Parent, e.Service} {
		if !plain(s, maxEconomicText) {
			return bad("agent, parent and service are at most %d printable characters", maxEconomicText)
		}
	}
	e.Service = scrubEconomic(e.Service)
	if e.Category != "" && !slices.Contains(client.Categories, e.Category) {
		return bad("category %q is not one of %v", e.Category, client.Categories)
	}
	if !currencyShape.MatchString(e.Currency) {
		return bad("currency %q is not an ISO 4217 code or an asset symbol", e.Currency)
	}
	gross, err := amount(e.Gross)
	if err != nil {
		return bad("gross: %v", err)
	}
	fee, err := amount(e.Fee)
	if err != nil {
		return bad("fee: %v", err)
	}
	tax, err := amount(e.Tax)
	if err != nil {
		return bad("tax: %v", err)
	}
	fmv, err := amount(e.FMV)
	if err != nil {
		return bad("fmv: %v", err)
	}
	if e.Corrects == "" && (gross.Sign() <= 0 || fmv.Sign() <= 0) {
		return bad("a payment moves more than nothing; only a correction restates one to zero")
	}
	if fee.Add(tax).Cmp(gross) > 0 {
		return bad("fee and tax are within gross")
	}
	if e.Currency == "USD" && fmv.Cmp(gross) != 0 {
		return bad("a U.S. dollar payment's fair market value is its gross")
	}
	e.Gross, e.Fee, e.Tax, e.FMV = gross.String(), nonzero(fee), nonzero(tax), fmv.String()
	switch {
	case e.Rail == client.RailChain && e.Tx == "":
		return bad("a chain payment names its transaction")
	case (e.Chain == "") != (e.Tx == ""):
		return bad("an on-chain payment names both its chain and its transaction")
	case e.Chain != "" && !chainShape.MatchString(e.Chain):
		return bad("chain %q is not CAIP-2", e.Chain)
	case e.Tx != "" && !txShape.MatchString(e.Tx):
		return bad("tx %q is not a transaction hash", e.Tx)
	}
	if len(e.Jurisdictions) > maxEconomicPlaces {
		return bad("at most %d jurisdictions", maxEconomicPlaces)
	}
	for _, j := range e.Jurisdictions {
		if !placeShape.MatchString(j) {
			return bad("jurisdiction %q is not an ISO 3166 code", j)
		}
	}
	e.Jurisdictions = slices.Compact(slices.Sorted(slices.Values(e.Jurisdictions)))
	if len(e.Jurisdictions) == 0 {
		e.Jurisdictions = nil
	}
	at := time.Unix(e.At, 0)
	if at.Before(economicEpoch) || at.After(time.Now().Add(maxClockSkew)) {
		return bad("at %d is not a time a payment was made", e.At)
	}
	if e.Corrects != "" && (!idShape.MatchString(e.Corrects) || e.Corrects == e.ID) {
		return bad("corrects %q is not another event's id", e.Corrects)
	}
	return e, nil
}

// amount reads one exact, non-negative decimal, at most as fine as the ledger's
// own unit. Empty is zero.
func amount(s string) (decimal.Decimal, error) {
	d, err := decimal.Parse(s)
	switch {
	case err != nil:
		return decimal.Decimal{}, err
	case d.Sign() < 0:
		return decimal.Decimal{}, fmt.Errorf("%q is negative", s)
	case d.Scale() > money.Decimals:
		return decimal.Decimal{}, fmt.Errorf("%q has more than %d fractional digits", s, money.Decimals)
	}
	return d, nil
}

// nonzero spells an amount, and zero as nothing, so an absent fee and a zero one
// are one statement.
func nonzero(d decimal.Decimal) string {
	if d.IsZero() {
		return ""
	}
	return d.String()
}

// plain reports whether s is at most n runes, none of them a control character.
func plain(s string, n int) bool {
	if len(s) > n*4 || strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return false
	}
	return len([]rune(s)) <= n
}

// ── the rows ─────────────────────────────────────────────────────────────────

// The row's name says which kind of record it is. A payment and a correction are
// events; a dispute is a record about one.
const (
	economicPayment    = "payment"
	economicCorrection = "correction"
	economicDispute    = "dispute"
)

// copyFor is the copy of an event a party keeps. The buyer's is the whole event.
// The seller's leaves out who inside the buyer acted: an IAM principal may be a
// person, the event cannot tell a person's id from an agent's, and the seller's
// books need the org that paid — which is what a tax form names — and never the
// people in it.
func copyFor(e client.EconomicEvent, org string) client.EconomicEvent {
	if org != e.Buyer {
		e.Agent, e.Parent = "", ""
	}
	return e
}

// economicFacts is a canonical event as its two rows: the buyer's copy in the
// buyer's partition and the seller's in the seller's. Each row's time is the
// payment's, as the rail recorded it, and its id is the digest of that party's
// copy, so a restatement of the same copy is the same row. The rail rides kind and
// every other field is an attribute under its json name.
func economicFacts(e client.EconomicEvent) []fact {
	name := economicPayment
	if e.Corrects != "" {
		name = economicCorrection
	}
	out := make([]fact, 0, 2)
	for _, org := range []string{e.Buyer, e.Seller} {
		c := copyFor(e, org)
		out = append(out, fact{
			signal: signalEconomic, org: org, time: time.Unix(e.At, 0).UTC(), id: digest(c),
			name: name, kind: e.Rail, attributes: economicAttrs(c),
		})
	}
	return out
}

// disputeFacts is one party's dispute of an event, as a row in each party's
// partition. Its time is when it was filed: nothing is ordered by it.
func disputeFacts(e client.EconomicEvent, d client.EconomicDispute) []fact {
	out := make([]fact, 0, 2)
	for _, org := range []string{e.Buyer, e.Seller} {
		out = append(out, fact{
			signal: signalEconomic, org: org, time: time.Unix(d.At, 0).UTC(), id: digest(d),
			name: economicDispute, kind: e.Rail,
			attributes: map[string]string{"disputes": d.Event, "by": d.By, "reason": d.Reason, "at": strconv.FormatInt(d.At, 10)},
		})
	}
	return out
}

// digest is the hex SHA-256 of a value's JSON.
func digest(v any) string {
	body, _ := json.Marshal(v) // strings, integers and a []string: it cannot fail
	h := sha256.Sum256(body)
	return hex.EncodeToString(h[:])
}

// economicAttrs is the event's attributes. An empty field is absent, as it is on
// every other fact.
func economicAttrs(e client.EconomicEvent) map[string]string {
	out := map[string]string{}
	for k, v := range map[string]string{
		"v": strconv.Itoa(e.V), "event": e.ID, "buyer": e.Buyer, "seller": e.Seller,
		"agent": e.Agent, "parent": e.Parent, "service": e.Service, "currency": e.Currency,
		"gross": e.Gross, "fee": e.Fee, "tax": e.Tax, "fmv": e.FMV,
		"jurisdictions": strings.Join(e.Jurisdictions, ","),
		"chain":         e.Chain, "tx": e.Tx, "proof": e.Proof, "at": strconv.FormatInt(e.At, 10),
		"corrects": e.Corrects, "category": e.Category,
	} {
		if v != "" {
			out[k] = v
		}
	}
	return out
}

// economicOf reads one event back from its row: the inverse of economicFacts.
func economicOf(rail string, attrs map[string]string) (client.EconomicEvent, error) {
	v, err := strconv.Atoi(attrs["v"])
	if err != nil {
		return client.EconomicEvent{}, fmt.Errorf("economic row %q: version %q", attrs["event"], attrs["v"])
	}
	at, err := strconv.ParseInt(attrs["at"], 10, 64)
	if err != nil {
		return client.EconomicEvent{}, fmt.Errorf("economic row %q: time %q", attrs["event"], attrs["at"])
	}
	e := client.EconomicEvent{
		V: v, ID: attrs["event"], Buyer: attrs["buyer"], Seller: attrs["seller"],
		Agent: attrs["agent"], Parent: attrs["parent"], Service: attrs["service"],
		Currency: attrs["currency"], Gross: attrs["gross"], Fee: attrs["fee"], Tax: attrs["tax"],
		FMV: attrs["fmv"], Rail: rail, Chain: attrs["chain"], Tx: attrs["tx"],
		Proof: attrs["proof"], At: at, Corrects: attrs["corrects"], Category: attrs["category"],
	}
	if j := attrs["jurisdictions"]; j != "" {
		e.Jurisdictions = strings.Split(j, ",")
	}
	return e, nil
}

// disputeOf reads one dispute back from its row: the inverse of disputeFacts.
func disputeOf(attrs map[string]string) client.EconomicDispute {
	at, _ := strconv.ParseInt(attrs["at"], 10, 64)
	return client.EconomicDispute{Event: attrs["disputes"], By: attrs["by"], Reason: attrs["reason"], At: at}
}

// ── what has landed ──────────────────────────────────────────────────────────

// errRestated is a statement of a payment that says something other than what has
// already landed for it. The rail owns its records and they do not change, so this
// is the rail's fault, and the first statement stands.
var errRestated = errors.New("a different statement of this payment has already landed")

// landed reports whether both parties' rows of a statement are in the warehouse. A
// row there for the same payment that is not one of these is a different statement
// of it, and is refused: the first one stands.
func landed(ctx context.Context, facts []fact) (bool, error) {
	e := facts[0]
	q, args := landedSQL(facts)
	rows, err := warehouseQuery(ctx, q, args...)
	if err != nil {
		return false, warehouseErr("economic", err)
	}
	want := map[string]string{}
	for _, f := range facts {
		want[f.org] = f.id
	}
	have := map[string]bool{}
	for _, r := range rows {
		org, id := asStr(r["org"]), asStr(r["id"])
		if want[org] != id {
			return false, zip.Errorf(http.StatusConflict, "economic: %s: %v", e.attributes["event"], errRestated)
		}
		have[org] = true
	}
	return len(have) == len(want), nil
}

// isEvent keeps the two kinds of row that are events; a dispute is not one.
const isEvent = `name IN ('` + economicPayment + `', '` + economicCorrection + `')`

// landedSQL is where a statement's rows would be: a point on the sort key in each
// party's partition, because a row's time is the payment's.
func landedSQL(facts []fact) (string, []any) {
	f := facts[0]
	return `SELECT org, id FROM ` + factTable + `
	WHERE org IN (?, ?) AND signal = ? AND time = toDateTime64(?, 9, 'UTC') AND ` + isEvent + `
	  AND attributes['event'] = ?`,
		[]any{facts[0].org, facts[1].org, string(signalEconomic), f.time.Unix(), f.attributes["event"]}
}

// restates holds a correction to the event it names: landed, on the same rail,
// between the same two orgs, at the same time. Anything else would not restate a
// payment — it would move one — and a correction that arrives before its event has
// landed is refused until it has, which the rail's retry reaches.
func restates(ctx context.Context, c client.EconomicEvent) error {
	q, args := restatesSQL(c)
	rows, err := warehouseQuery(ctx, q, args...)
	if err != nil {
		return warehouseErr("economic", err)
	}
	orig, err := economicRows(rows)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	if len(orig) == 0 || orig[0].Rail != c.Rail || orig[0].Buyer != c.Buyer || orig[0].Seller != c.Seller {
		return zip.Errorf(http.StatusConflict,
			"economic: %s corrects %s, which has not landed as a payment on rail %s from %s to %s at %d",
			c.ID, c.Corrects, c.Rail, c.Buyer, c.Seller, c.At)
	}
	return nil
}

// restatesSQL is the event a correction names, where it must be: in the buyer's
// partition at the correction's own time.
func restatesSQL(c client.EconomicEvent) (string, []any) {
	where, args := scope(c.Buyer, signalEconomic)
	return `SELECT kind AS rail, attributes AS attrs FROM ` + factTable + `
	WHERE ` + where + ` AND time = toDateTime64(?, 9, 'UTC') AND ` + isEvent + ` AND attributes['event'] = ?
	LIMIT 1`, append(args, c.At, c.Corrects)
}

// eventSQL is one event as it landed in the org's partition.
func eventSQL(org, event string) (string, []any) {
	where, args := scope(org, signalEconomic)
	return `SELECT kind AS rail, attributes AS attrs FROM ` + factTable + `
	WHERE ` + where + ` AND ` + isEvent + ` AND attributes['event'] = ?
	ORDER BY time, id
	LIMIT 1`, append(args, event)
}

// ── the read ─────────────────────────────────────────────────────────────────

// Page bounds of a read.
const (
	economicPage    = 100
	maxEconomicPage = 1000
)

// economics is THE read of the signal, for both the plane op and the org's own op:
// the org's events in [From, To), each at its first statement, oldest first, one
// page at a time, with the ones a correction restates named and the disputes of
// them beside.
func economics(ctx context.Context, org string, in client.EconomicsIn) (*client.Economics, error) {
	if in.To <= in.From {
		return nil, zip.ErrBadRequest("economic: the window closes before it opens")
	}
	if in.Rail != "" && !slices.Contains(client.Rails, in.Rail) {
		return nil, zip.ErrBadRequest(fmt.Sprintf("economic: rail %q is not one of %v", in.Rail, client.Rails))
	}
	if in.Counterparty == org {
		return nil, zip.ErrBadRequest("economic: an org is never its own counterparty")
	}
	after, err := decodeCursor(in.Cursor)
	if err != nil {
		return nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = economicPage
	}
	limit = min(limit, maxEconomicPage)
	if err := requireDatastore(ctx); err != nil {
		return nil, err
	}
	q, args := economicSQL(org, in, after, limit)
	rows, err := warehouseQuery(ctx, q, args...)
	if err != nil {
		return nil, warehouseErr("economic", err)
	}
	events, err := economicRows(rows)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	out := &client.Economics{Rows: events}
	if len(events) > limit {
		out.Rows = events[:limit]
		out.Next = encodeCursor(out.Rows[limit-1])
	}
	if len(out.Rows) == 0 {
		return out, nil
	}
	q, args = besideSQL(org, out.Rows)
	rows, err = warehouseQuery(ctx, q, args...)
	if err != nil {
		return nil, warehouseErr("economic", err)
	}
	var fixes []client.EconomicEvent
	for _, r := range rows {
		attrs := aStrMap(r["attrs"])
		if asStr(r["name"]) == economicDispute {
			out.Disputes = append(out.Disputes, disputeOf(attrs))
			continue
		}
		c, err := economicOf(asStr(r["rail"]), attrs)
		if err != nil {
			return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
		}
		fixes = append(fixes, c)
	}
	out.Superseded = superseded(out.Rows, fixes)
	return out, nil
}

// economicSQL is one page of the org's events. The org and the signal lead, bound,
// through scope; the window and the cursor are ranges of the sort key's time, which
// is the payment's, so the read starts at the cursor and stops at the page: rows are
// taken in (time, event, id) order, the least of each event's rows is its statement,
// and one row past the limit says whether there is a next page.
func economicSQL(org string, in client.EconomicsIn, after cursor, limit int) (string, []any) {
	where, args := scope(org, signalEconomic)
	q := `SELECT attributes['event'] AS event, kind AS rail, attributes AS attrs
	FROM ` + factTable + `
	WHERE ` + where + ` AND ` + isEvent + `
	  AND time >= toDateTime64(?, 9, 'UTC') AND time < toDateTime64(?, 9, 'UTC')`
	args = append(args, in.From, in.To)
	if after.event != "" {
		q += `
	  AND (time > toDateTime64(?, 9, 'UTC') OR (time = toDateTime64(?, 9, 'UTC') AND attributes['event'] > ?))`
		args = append(args, after.at, after.at, after.event)
	}
	if in.Rail != "" {
		q += ` AND kind = ?`
		args = append(args, in.Rail)
	}
	if cp := in.Counterparty; cp != "" {
		q += ` AND ((attributes['buyer'] = ? AND attributes['seller'] = ?) OR (attributes['seller'] = ? AND attributes['buyer'] = ?))`
		args = append(args, cp, org, cp, org)
	}
	q += `
	ORDER BY time, event, id
	LIMIT 1 BY event
	LIMIT ?`
	return q, append(args, limit+1)
}

// besideSQL is what stands beside a page's events: the corrections that restate
// them — which share their time, so they are read over the page's own range of the
// key — and the disputes of them, which were filed whenever they were. Both are
// few, and both are named by the events they are about. The page's ids are bound
// once: a full page of them is a quarter of the statement size the warehouse
// parses, and twice would be half.
func besideSQL(org string, page []client.EconomicEvent) (string, []any) {
	ids := make([]string, len(page))
	for i, e := range page {
		ids[i] = e.ID
	}
	where, args := scope(org, signalEconomic)
	return `WITH ? AS page
	SELECT name, kind AS rail, attributes AS attrs FROM ` + factTable + `
	WHERE ` + where + `
	  AND ((name = '` + economicCorrection + `' AND time >= toDateTime64(?, 9, 'UTC') AND time <= toDateTime64(?, 9, 'UTC')
	        AND has(page, attributes['corrects']))
	    OR (name = '` + economicDispute + `' AND has(page, attributes['disputes'])))
	ORDER BY time, id
	LIMIT 1 BY id`, append(append([]any{ids}, args...), page[0].At, page[len(page)-1].At)
}

// economicRows reads the warehouse's rows back into events.
func economicRows(rows []map[string]any) ([]client.EconomicEvent, error) {
	out := make([]client.EconomicEvent, 0, len(rows))
	for _, r := range rows {
		e, err := economicOf(asStr(r["rail"]), aStrMap(r["attrs"]))
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// superseded names the events a correction restates. A correction counts only when
// it moved on the same rail between the same two orgs at the same time as the event
// it names: one that changes who paid whom, or when, is not a correction of that
// payment.
func superseded(rows, fixes []client.EconomicEvent) []string {
	var out []string
	for _, e := range rows {
		for _, c := range fixes {
			if c.Corrects == e.ID && c.Rail == e.Rail && c.Buyer == e.Buyer && c.Seller == e.Seller && c.At == e.At {
				out = append(out, e.ID)
				break
			}
		}
	}
	return out
}

// cursor is where a page ended: the last event's time and id.
type cursor struct {
	at    int64
	event string
}

func encodeCursor(e client.EconomicEvent) string {
	return strconv.FormatInt(e.At, 10) + "." + e.ID
}

func decodeCursor(s string) (cursor, error) {
	if s == "" {
		return cursor{}, nil
	}
	at, event, ok := strings.Cut(s, ".")
	sec, err := strconv.ParseInt(at, 10, 64)
	if !ok || err != nil || !idShape.MatchString(event) {
		return cursor{}, zip.ErrBadRequest("economic: cursor is not one this read answered")
	}
	return cursor{at: sec, event: event}, nil
}
