// Package audit is the unified cloud binary's compliance-grade audit trail —
// tamper-evident, append-only, and complete over the security-relevant request
// surface (FedRAMP AU-* / SOC 2 CC-* controls).
//
// THE CONTROL, IN ONE SENTENCE. Every security-relevant action against this
// binary is captured as a structured Record, hash-chained to its predecessor so
// any later deletion or modification is detectable, and written INLINE (never
// dropped) to an append-only store the application can only INSERT into.
//
// THREE PIECES, EACH IN ITS LANE (orthogonal, per the Zen of Hanzo):
//   - record.go  — the event model + the hash-chain math (what a record IS and
//     how it links to the one before it). Pure, no I/O.
//   - store.go   — the append-only sink (SQLite primary, INSERT-only; a
//     best-effort OLAP mirror) and the serialized Recorder that
//     owns the chain head. All persistence.
//   - redact.go  — the secret-stripping allowlist/denylist for any structured
//     before/after an explicit emit point supplies. No secret ever
//     reaches a record.
//
// The HTTP middleware (Middleware, in the cloud package) and the query/verify
// endpoints (in clients/admin) are thin callers of this package. This package
// holds the security logic; it has zero knowledge of routes.
package audit

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"
)

// Actor identifies WHO performed the action. It is populated ONLY from a
// validated principal (the sanitized X-User-* headers SanitizeIdentity mints
// from a verified IAM JWT), never from a raw client header — so an actor can
// never be forged by the request that is being audited. A service principal
// (M2M / no user sub) records Org with an empty Sub.
type Actor struct {
	// Org is the tenant (IAM `owner`). Empty for an unauthenticated request.
	Org string `json:"org"`
	// Sub is the user id (IAM `sub`/`preferred_username`). Empty for a service
	// principal or an anonymous request.
	Sub string `json:"sub"`
	// Email is the validated user email, when present.
	Email string `json:"email,omitempty"`
}

// Resource identifies WHAT was acted upon: a type (e.g. "org", "role",
// "secret", "provider-config", "credit") and its id. For a plain HTTP mutation
// with no finer resource semantics, Type is the route family and ID is empty —
// the Action verb + path already pin the object.
type Resource struct {
	Type string `json:"type"`
	ID   string `json:"id,omitempty"`
}

// AuthContext records HOW the actor authenticated and what authority they held
// at decision time — the AC-* evidence (was this a SuperAdmin? by what
// credential?). Method is "jwt" | "api-key" | "none". IsAdmin is the VALIDATED
// SuperAdmin bit (owner == AdminOrg), never a raw X-User-IsAdmin.
type AuthContext struct {
	Method  string `json:"method"`
	IsAdmin bool   `json:"isAdmin"`
}

// Outcome is the result of the action: whether it was allowed and what
// happened. Result is "success" | "deny" | "error". Status is the HTTP status.
// Reason is a short, non-sensitive explanation for a deny/error (e.g.
// "SuperAdmin required", "insufficient_balance") — never a secret, never a
// raw upstream error body.
type Outcome struct {
	Result string `json:"result"`
	Status int    `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Record is one audit event. The JSON tags ARE the on-disk and on-wire contract.
//
// Field order in the struct is deliberate but IRRELEVANT to the hash: the chain
// hashes the CANONICAL (sorted-key) JSON of the record with Hash/PrevHash zeroed
// (see canonicalBytes), so re-ordering fields or adding an omitempty field can
// never change an existing record's hash.
type Record struct {
	// Seq is the strictly-increasing chain position (0-based). It is assigned by
	// the Recorder under its lock, so it is a true total order with no gaps.
	Seq uint64 `json:"seq"`

	// Time is the UTC event timestamp (RFC3339Nano).
	Time time.Time `json:"time"`

	// Actor / Action / Resource / Auth / Outcome — the AU-3 "content of audit
	// records" core: who, what, on what, how-authenticated, with what result.
	Actor    Actor       `json:"actor"`
	Action   string      `json:"action"`
	Resource Resource    `json:"resource"`
	Auth     AuthContext `json:"auth"`
	Outcome  Outcome     `json:"outcome"`

	// SourceIP + UserAgent — the AU-3 "source of the event" fields.
	SourceIP  string `json:"sourceIp,omitempty"`
	UserAgent string `json:"userAgent,omitempty"`

	// RequestID correlates the record to the request-line log and any downstream
	// trace (the X-Request-Id the pipeline mints).
	RequestID string `json:"requestId,omitempty"`

	// Method + Path are the HTTP verb and route for a request-sourced event.
	Method string `json:"method,omitempty"`
	Path   string `json:"path,omitempty"`

	// Before / After capture a mutation's prior and resulting state for the
	// AU-required "before/after" on config-affecting changes. They are populated
	// ONLY by explicit emit points and ONLY after Redact has stripped secrets —
	// the HTTP middleware never sets them (it never reads bodies), so a secret in
	// a request body can never leak here. Raw JSON so any shape round-trips.
	Before json.RawMessage `json:"before,omitempty"`
	After  json.RawMessage `json:"after,omitempty"`

	// PrevHash is the hash of record Seq-1 (hex). For the genesis record (Seq 0)
	// it is genesisPrevHash. Hash is this record's hash. Neither participates in
	// its own hash computation (both are zeroed in canonicalBytes).
	PrevHash string `json:"prevHash"`
	Hash     string `json:"hash"`
}

// genesisPrevHash is the PrevHash of the first record in a fresh chain: 32 zero
// bytes, hex-encoded. A non-empty, fixed anchor so the genesis record's hash is
// still a function of a known constant (not the empty string, which would be
// indistinguishable from "field omitted").
const genesisPrevHash = "0000000000000000000000000000000000000000000000000000000000000000"

// canonicalBytes returns the deterministic byte string a record hashes over: the
// record with Hash AND PrevHash zeroed, marshaled by encoding/json (which sorts
// struct fields in declaration order and, critically, is stable for a given
// struct — the SAME bytes on every machine and every run). Zeroing PrevHash here
// means the hash covers only the record's OWN content; the link to the previous
// record is added explicitly in computeHash by appending prevHash. This keeps
// the two concerns separable and the math obvious.
//
// We marshal a copy with the two hash fields cleared rather than a parallel
// struct so there is exactly ONE definition of a record's fields (DRY): add a
// field to Record and it is covered by the hash automatically.
func canonicalBytes(r Record) ([]byte, error) {
	r.Hash = ""
	r.PrevHash = ""
	return json.Marshal(r)
}

// computeHash returns the hex SHA-256 of (canonical(record) || prevHash-bytes).
// The prevHash is folded in as its RAW hex string bytes — the exact value stored
// in the record's PrevHash field — so the verifier reproduces it byte-for-byte
// from stored data alone. Any change to the record's content OR to which record
// precedes it changes this output, which is the whole tamper-evidence property.
func computeHash(r Record, prevHash string) (string, error) {
	body, err := canonicalBytes(r)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(body)
	h.Write([]byte(prevHash))
	return hex.EncodeToString(h.Sum(nil)), nil
}

// seal finalizes a record into position seq linked to prevHash: it stamps Seq
// and PrevHash, computes Hash, and returns the sealed record ready to append.
// The Recorder calls this under its lock so seq/prevHash reflect the true head.
func seal(r Record, seq uint64, prevHash string) (Record, error) {
	r.Seq = seq
	r.PrevHash = prevHash
	hash, err := computeHash(r, prevHash)
	if err != nil {
		return Record{}, err
	}
	r.Hash = hash
	return r, nil
}
