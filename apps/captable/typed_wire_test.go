package captable

import (
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/openapi"
)

// This file holds the cap table's projection ledgers, and it keeps TWO because
// the surface has two different reasons for a route to be raw — and collapsing
// them would be the more damaging simplification. A reader of one list called
// "by design" would conclude this package is at its floor, which it is not.
//
//	untypedByDesign  a WIRE this stack cannot describe. Nothing to do until an
//	                 upstream capability lands, and the entry says which.
//	typingOwed       work that is OWED. The mechanism exists, the blocker is
//	                 named, and the entry is deleted when the op is written.
//
// A route in neither is a failure: the next route added here is typed by
// default, and a route that stops being served fails whichever list names it.

// untypedByDesign is EMPTY today, and that is a statement rather than an
// oversight: every raw route on this surface is raw because nobody has written
// the typed op yet, not because its wire resists one. The bundle relay itself is
// describable — apps/goja re-marshals the answer through encoding/json, so a
// typed op re-marshalling the same value is byte-identical, and a non-2xx comes
// back as goja.BundleErr under the bundle's own status and bytes (run, typed.go).
// That is exactly how the reads and the three existing writes work.
var untypedByDesign = map[string]string{}

// typingOwed is the ELEVEN cap-table writes that have no typed op yet.
//
// THE MECHANISM IS ALREADY HERE. writes.go carries `write` — refuse an oversized
// body with the relay's 413, assemble the body from declared fields, run the
// bundle route, relay a non-2xx as goja.BundleErr — and three ops already use it
// (updateCompany, updateStakeholder, closeRound). None of these eleven needs a
// capability that does not exist.
//
// WHAT EACH ONE NEEDS is the bundle route's own accepted FIELDS — and they are
// READABLE, which is the thing this comment got wrong for as long as it stood.
//
// It said the names "live in the captable bundle's source … not in this repo" and
// treated that as the blocker. github.com/hanzoai/captable is a Go module
// DEPENDENCY: its source sits in the module cache at the version go.mod pins, so
// `$(go env GOMODCACHE)/github.com/hanzoai/captable@<ver>/goja/src/routes/*.ts` is
// on disk, offline, at exactly the version this binary talks to. The names were
// never unknowable. They were unread.
//
// SO STILL DO NOT GUESS THEM — read them. A field name that does not match
// silently drops its value: goja.Body assembles only the fields declared, the
// bundle validates what it receives, and a share issuance missing a price or a
// stakeholder id is accepted as a smaller write rather than refused. Wrong equity
// data that nothing reports is worse than an untyped route, which is only
// invisible. Two things found in the first two conversions show the margin is
// real: `shares.transfer` takes a fourth field, `certificateId`, REQUIRED for a
// partial transfer — omit it from the Go type and every split answers
// "certificateId is required" with no way for a caller to supply one — and
// `rounds.investments.add` answers 201 where the transfer answers 200, because it
// MINTS a security rather than moving one.
//
// THE FIELD LISTS, transcribed from that source, so the next writer starts from
// data. `req*`/`num`/`oneOf`/`dateString` are required, `opt*` optional:
//
//	classes         createShareClass    name, initialSharesAuthorized, boardApprovalDate,
//	                                    stockholderApprovalDate, votesPerShare, parValue,
//	                                    pricePerShare, seniority, conversionRights,
//	                                    convertsToShareClassId?, liquidationPreferenceMultiple
//	classes/{id}    updateShareClass    the same list — a full REPLACE, not a merge
//	plans           createEquityPlan    7 fields
//	shares          addShare            stakeholderId, shareClassId, certificateId, quantity,
//	                                    status, pricePerShare?, capitalContribution?,
//	                                    ipContribution?, debtCancelled?, otherContributions?,
//	                                    cliffYears, vestingYears, companyLegends, issueDate,
//	                                    rule144Date?, vestingStartDate?, boardApprovalDate
//	options         addOption           grantId, stakeholderId, equityPlanId, quantity,
//	                                    exercisePrice, type, status, cliffYears, vestingYears,
//	                                    issueDate, expirationDate, vestingStartDate,
//	                                    boardApprovalDate, rule144Date, notes?
//	safes           createSafe          10 fields
//	convertibles    createConvertible   11 fields
//	rounds          createRound         6 fields
//
// ONE of the eleven is not merely unwritten and moves to untypedByDesign when
// someone confirms it: `POST /stakeholders` takes a single object OR an ARRAY
// (`const list = Array.isArray(raw) ? raw : [raw]`, stakeholders.ts), which is the
// polymorphic-body class apps/index records for its own document writes.
//
// Read the bundle's route, mirror its inputs as goja.Scalar fields with prose, and
// delete the entry. One op per line here is one op per line there.
var typingOwed = map[string]string{
	"POST /v1/captable/stakeholders":  "add a stakeholder",
	"POST /v1/captable/classes":       "define a share class",
	"PATCH /v1/captable/classes/{id}": "amend a share class (a full REPLACE, not a merge)",
	"POST /v1/captable/plans":         "create an equity plan",
	"POST /v1/captable/shares":        "issue shares",
	"POST /v1/captable/options":       "grant options from a plan",
	"POST /v1/captable/safes":         "record a SAFE",
	"POST /v1/captable/convertibles":  "record a convertible note",
	"POST /v1/captable/rounds":        "open a priced round",
}

// TestEveryRouteIsTypedOrAccountedFor requires the three ledgers to SUM to the
// served surface, so a route added raw goes red without anyone remembering this
// file, and a name in either list that stops being served goes red too.
func TestEveryRouteIsTypedOrAccountedFor(t *testing.T) {
	app := mountApp(t)
	doc, err := openapi.Spec(app, openapi.Info{Title: "captable", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg, err := openapi.Typed(app)
	if err != nil {
		t.Fatalf("typed: %v", err)
	}

	served, typed := map[string]bool{}, map[string]bool{}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/captable") {
			continue
		}
		for method := range item {
			served[strings.ToUpper(method)+" "+path] = true
		}
	}
	for key := range reg.Ops {
		if _, path, ok := strings.Cut(key, " "); ok && strings.HasPrefix(path, "/v1/captable") {
			typed[key] = true
		}
	}

	var unaccounted []string
	for key := range served {
		if typed[key] {
			continue
		}
		_, byDesign := untypedByDesign[key]
		_, owed := typingOwed[key]
		if !byDesign && !owed {
			unaccounted = append(unaccounted, key)
		}
	}
	if len(unaccounted) > 0 {
		sort.Strings(unaccounted)
		t.Errorf("served but neither typed nor accounted for: %s\n"+
			"A raw route publishes no schema, no MCP tool, no CLI command and no typed SDK "+
			"method. Convert it (writes.go has the mechanism), or account for it: "+
			"untypedByDesign if a WIRE resists typing, typingOwed if it is simply not written "+
			"yet — and say which.", strings.Join(unaccounted, ", "))
	}
	for _, ledger := range []map[string]string{untypedByDesign, typingOwed} {
		for key := range ledger {
			if !served[key] {
				t.Errorf("a ledger names %q, which this surface no longer serves", key)
			}
			if typed[key] {
				t.Errorf("a ledger names %q, which IS a typed op — delete the entry", key)
			}
		}
	}
	if got, want := len(typed)+len(untypedByDesign)+len(typingOwed), len(served); got != want {
		t.Errorf("the ledgers must sum to the served surface: typed %d + byDesign %d + owed %d = %d, served %d",
			len(typed), len(untypedByDesign), len(typingOwed), got, want)
	}
}

// TestEveryTypedOpIsDescribed: prose is the product surface. A typed op with no
// description reaches the document, every generated SDK and the MCP tool list as
// a name and a shape with nothing saying what it does.
func TestEveryTypedOpIsDescribed(t *testing.T) {
	doc, err := openapi.Spec(mountApp(t), openapi.Info{Title: "captable", Version: "v1"})
	if err != nil {
		t.Fatalf("spec: %v", err)
	}
	for path, item := range doc.Paths {
		if !strings.HasPrefix(path, "/v1/captable") {
			continue
		}
		for method, op := range item {
			key := strings.ToUpper(method) + " " + path
			if _, ok := untypedByDesign[key]; ok {
				continue
			}
			if _, ok := typingOwed[key]; ok {
				continue
			}
			if strings.TrimSpace(op.Description) == "" && strings.TrimSpace(op.Summary) == "" {
				t.Errorf("%s has no description — run: go generate -run zipdoc ./apps/captable/...", key)
			}
		}
	}
}
