package client

// market.go is what the marketplace asks of the apps that own the rest of a
// sale: which tools an org reaches and owns, whether a buyer's payment
// authorization will settle before any money moves, and what the public may know
// about a seller.
//
// A job is two-sided work between two orgs, and it is paid ONCE, on a rail:
// x402 settles it from the buyer's balance into the seller's payout wallet when
// the work is released. Until then the money stays in the buyer's own books —
// the buyer's signed authorization is checked, its nonce held for the job, and
// its amount set aside in the buyer's wallet (X402Authorize) — and it moves only
// when it is settled (X402Settle), or returns to the wallet when the job ends
// unpaid (X402Void). Hanzo never holds the funds of a job.

import (
	"encoding/json"
	"strings"
)

// The plane ops the marketplace calls that are not the price table's.
const (
	// X402Authorize checks one payment authorization for a priced resource
	// without settling it: with no payment it answers the challenge to sign, and
	// with one whether it verifies and stays valid until a stated time. Asked to
	// hold, it also reserves the authorization's nonce for that resource, so it
	// can settle that resource or nothing, and sets its amount aside in the
	// payer's wallet, so the payment is there when it is settled.
	X402Authorize = "x402_authorize"
	// X402Void gives up one held authorization that will never be settled: the
	// amount set aside returns to the payer's wallet. An authorization already
	// presented for settlement is not voided, and says so.
	X402Void = "x402_void"
	// ToolsOwned answers whether the caller's org owns one skill, MCP server or
	// tool — the thing a listing says it sells.
	ToolsOwned = "tools_owned"
	// ToolsList answers the tools the caller's org can reach in one project.
	ToolsList = "tools_list"
	// ToolsActivate switches one tool on or off for the caller's org in one
	// project: the marketplace's install and uninstall.
	ToolsActivate = "tools_activate"
	// ToolsReview switches on the code-review and dev skills for the caller's
	// org when a pull request opens: the GitHub delivery, which holds no admin.
	ToolsReview = "tools_review"
	// PrincipalPublic answers what anyone may know about a set of orgs that sell
	// in public: whether each holds a live TaxPrincipalCredential.
	PrincipalPublic = "principal_public"
)

// jobResourcePrefix namespaces a JOB as an x402 resource, as toolResourcePrefix
// does a tool: no request path begins "job:", so a job can never be priced by
// hitting a URL and a URL can never be bought as a job.
const jobResourcePrefix = "job:"

// JobResource names one job as an x402 resource. The marketplace keys its price
// table on it and the rail records it as what a settlement bought.
func JobResource(id string) string { return jobResourcePrefix + id }

// JobOf reads one back. ok=false means the resource is not a job.
func JobOf(resource string) (string, bool) {
	id, ok := strings.CutPrefix(resource, jobResourcePrefix)
	return id, ok && id != ""
}

// AuthorizationIn asks the rail about one payment authorization for a priced
// resource, as the CALLING tenant would pay it.
type AuthorizationIn struct {
	// Resource is the priced resource, e.g. JobResource(id).
	Resource string `json:"resource" validate:"required"`
	// Payment is the signed PaymentPayload, the PAYMENT-SIGNATURE value (base64).
	// Empty asks for the challenge alone.
	Payment string `json:"payment,omitempty"`
	// Until is the unix time the authorization must still be acceptable at: the
	// latest moment it may be settled. Zero checks only that it is valid now.
	Until int64 `json:"until,omitempty"`
	// Hold reserves the authorization's nonce for Resource once it verifies: from
	// then on it settles Resource or nothing, so a payment agreed for one job
	// cannot be spent on anything else first. And it sets the amount aside in the
	// payer's wallet, refused (402, insufficient_funds) when the wallet cannot
	// cover it, so the payment agreed is one the payer can make.
	Hold bool `json:"hold,omitempty"`
}

// VoidIn gives up one held authorization, as the tenant that holds it.
type VoidIn struct {
	// Resource is the priced resource the authorization is held for.
	Resource string `json:"resource" validate:"required"`
	// Payment is the signed PaymentPayload that was held, the PAYMENT-SIGNATURE
	// value (base64).
	Payment string `json:"payment" validate:"required"`
}

// Voided is the rail's answer to a void.
type Voided struct {
	// Spent is true when the authorization was already presented for
	// settlement: its money moved, or is moving, and nothing was returned.
	Spent bool `json:"spent,omitempty"`
	// Returned is what went back to the payer's wallet: zero when nothing was
	// still set aside.
	Returned Money `json:"returned"`
}

// Authorization is the rail's answer, as data for the reason Settled is: a refusal
// carries the terms the client must read to pay again.
type Authorization struct {
	// OK is true when the payment verifies against the resource's terms and is
	// acceptable now and at Until. Nothing is paid; asked to hold, the amount is
	// set aside in the payer's wallet.
	OK bool `json:"ok"`
	// From is the address that signed, when a payment was presented.
	From string `json:"from,omitempty"`
	// ValidBefore is when the authorization stops being acceptable, unix seconds.
	ValidBefore int64 `json:"validBefore,omitempty"`
	// Spent is true when the authorization was already presented for settlement:
	// its money moved, or is moving. A payment agreed for a job that reads spent
	// was paid, whatever the job says.
	Spent bool `json:"spent,omitempty"`
	// Network is the CAIP-2 network the terms are stated on.
	Network string `json:"network,omitempty"`
	// Asset is the token contract the authorization transfers.
	Asset string `json:"asset,omitempty"`
	// PayTo is the payee's address.
	PayTo string `json:"payTo,omitempty"`
	// Challenge is the PAYMENT-REQUIRED value (base64): the terms to sign.
	Challenge string `json:"challenge,omitempty"`
	// Status is the refusal's status: 402 for a payment that does not verify, 409
	// for a resource that is not for sale, 503 when the rail cannot state terms.
	Status int    `json:"status,omitempty"`
	Code   string `json:"code,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// The kinds of thing tools answers ownership for.
const (
	OwnedSkill = "skill" // a skill the org authored, by id or name
	OwnedMCP   = "mcp"   // an MCP server the org registered, by id or name
	OwnedTool  = "tool"  // a tool the org's own scope resolves, by name, and whose it is
)

// OwnedIn names one thing, by kind and by its id or name in the caller's org.
type OwnedIn struct {
	Kind string `json:"kind" validate:"required"`
	Ref  string `json:"ref" validate:"required"`
	// Project is the project a tool is resolved in; the org's default when empty.
	Project string `json:"project,omitempty"`
}

// Owned is tools' answer. Mine is false for a thing that does not exist and for
// one another org owns alike: the caller learns only whether it is its own.
type Owned struct {
	Mine bool `json:"mine"`
	// ID is the thing's own id when it is the caller's.
	ID string `json:"id,omitempty"`
	// Name is the thing's display name when it is the caller's.
	Name string `json:"name,omitempty"`
	// Source is where a tool comes from — connector, function, zap-service,
	// agent, skill or mcp — when the caller's scope resolves it.
	Source string `json:"source,omitempty"`
	// Platform is true for a tool every org reaches the same one of: a connector
	// action or a first-party service. No org owns it, so Mine is false; it is the
	// platform's.
	Platform bool `json:"platform,omitempty"`
}

// ToolsIn names the project a tool question is asked in; the org rides the
// caller. Empty is the org's default project.
type ToolsIn struct {
	Project string `json:"project,omitempty"`
}

// ToolSet is the tools an org can reach, deduplicated by name.
type ToolSet struct {
	Tools []ToolBrief `json:"tools"`
}

// ToolBrief is one tool as discovery shows it, in the field names GET /v1/tool
// answers.
type ToolBrief struct {
	Name         string          `json:"name"`
	Source       string          `json:"source"`
	Description  string          `json:"description"`
	Schema       json.RawMessage `json:"inputSchema,omitempty"`
	Dispatchable bool            `json:"dispatchable"`
	Activated    bool            `json:"activated"`
}

// ActivateIn switches one tool on or off.
type ActivateIn struct {
	Tool    string `json:"tool" validate:"required"`
	On      bool   `json:"on"`
	Project string `json:"project,omitempty"`
}

// Activated is a tool's activation after the write.
type Activated struct {
	Tool      string `json:"tool"`
	Installed bool   `json:"installed"`
}

// ReviewIn names the repository a pull request opened in, as the project the
// skills are switched on for as well as the org's default.
type ReviewIn struct {
	Project string `json:"project,omitempty"`
}

// Reviewed is the skills the pull request switched on.
type Reviewed struct {
	Activated []string `json:"activated"`
}

// PublicIn names the orgs asked about: those that sell in public, at most 200.
type PublicIn struct {
	Orgs []string `json:"orgs"`
}

// Publics is one answer per org asked about, in the order asked.
type Publics struct {
	Principals []Public `json:"principals"`
}

// Public is what anyone may know about an org that sells in public, and no more.
type Public struct {
	Org string `json:"org"`
	// Documented is true while the org's tax form is certified and valid — what
	// each of its agents' TaxPrincipalCredential rests on, whether or not it has
	// agents yet. It names no form, number or country.
	Documented bool `json:"documented"`
}
