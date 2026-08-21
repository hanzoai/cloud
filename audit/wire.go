package audit

import "time"

// Wire is the JSON shape of one audit record on the operator/console contract. It
// is cloud's OWN record projection — richer than the IAM record it supersedes: it
// carries the outcome, the validated auth context, and the hash-chain linkage
// (Hash/PrevHash) so a console can show tamper-evidence per row. The JSON tags ARE
// the contract; both the admin god-view (/v1/admin/audit) and the org-scoped trail
// (/v1/audit) serialize this ONE shape so a single console adapter reads either.
type Wire struct {
	// Seq is the record's position in the chain, 0-based and gapless. The Recorder
	// assigns it under its own lock, so it is a true total order: seq n+1 was
	// written after seq n, and a missing number is a missing record.
	Seq uint64 `json:"seq"`
	// Time is when the action happened, RFC3339Nano in UTC. The stored column has
	// the same precision and sorts the same way, so a client can range and order on
	// this string verbatim.
	Time string `json:"time"`
	// Org is the tenant the action was taken IN — the effective org, which for
	// everyone but an impersonating SuperAdmin is also the actor's own. Empty on an
	// unauthenticated request.
	Org string `json:"org"`
	// Sub is the acting user (the IAM subject). Empty for a machine principal or an
	// anonymous request, which is how a service action is told from a person's.
	Sub string `json:"sub"`
	// Email is the actor's validated address, absent when the credential carried
	// none. It comes from the verified token, never from a client header.
	Email string `json:"email,omitempty"`
	// Home is present ONLY on a cross-org action: the org the actor came FROM,
	// while Org is the org they acted IN. A console row carrying `home` is a
	// platform-admin impersonation and should be rendered as one.
	Home string `json:"home,omitempty"`
	// Action is the verb that was performed. It is the event's name, not the HTTP
	// method — a request-sourced record carries both, and the pair is what makes a
	// row readable ("grant.create" at POST /v1/admin/grants).
	Action string `json:"action"`
	// Resource is the KIND of thing acted upon ("org", "role", "secret",
	// "provider-config", "credit"). Where a mutation has no finer semantics than its
	// route, this is the route family and resourceId is empty — the action and the
	// path already pin the object.
	Resource string `json:"resource"`
	// ResourceID is the specific instance, absent when the kind alone identifies it.
	ResourceID string `json:"resourceId,omitempty"`
	// Method is the HTTP verb, on a record a request produced. Absent on an event
	// emitted from inside the binary with no request behind it.
	Method string `json:"method,omitempty"`
	// Path is the request's route. Any segment shaped like a credential is replaced
	// before the record is written, so a key that rides in a path is not preserved
	// here by the very control meant to watch it.
	Path string `json:"path,omitempty"`
	// Result is how the action ended: "success", "deny" or "error". A deny is a
	// decision this binary made and is as much evidence as a success.
	Result string `json:"result"`
	// Status is the HTTP status the caller received. It is the outcome as the client
	// saw it, so a 200 carrying a domain refusal still reads 200 here.
	Status int `json:"status"`
	// Reason is a short explanation for a deny or an error ("SuperAdmin required",
	// "insufficient_balance"). It is never a secret and never a raw upstream error
	// body; absent on a success.
	Reason string `json:"reason,omitempty"`
	// SourceIP is the client address the edge resolved for the request, after the
	// proxy chain — the address a responder would act on, not the socket peer.
	SourceIP string `json:"sourceIp,omitempty"`
	// UserAgent is the client the request announced itself as. Client-supplied, so
	// it is evidence about what claimed to act, not proof of it.
	UserAgent string `json:"userAgent,omitempty"`
	// RequestID ties this row to the request-line log and any downstream trace — the
	// X-Request-Id the pipeline minted for that request.
	RequestID string `json:"requestId,omitempty"`
	// IsAdmin is the VALIDATED platform-SuperAdmin bit at decision time (membership
	// of the reserved admin org), never the client's own claim to be one.
	IsAdmin bool `json:"isAdmin"`
	// Auth is the credential the actor presented: "jwt", "api-key", or "none".
	Auth string `json:"authMethod,omitempty"`
	// Hash is this record's SHA-256 over its own canonical JSON with both hash
	// fields cleared, folded with prevHash. Recomputing it from the row's other
	// fields is what proves the row has not been edited.
	Hash string `json:"hash"`
	// PrevHash is the hash of record seq-1, which is what links the rows into a
	// chain: a deleted or reordered record breaks the recomputation at that point.
	// The first record of a chain carries 64 zeros rather than an empty string, so
	// "start of chain" and "field missing" cannot look alike.
	PrevHash string `json:"prevHash"`
}

// ToWire projects a stored Record onto the console wire shape. Time is emitted as
// RFC3339Nano (UTC) — the same nanosecond-precise, order-preserving format the ts
// column stores — so a client can sort/range on it verbatim.
func (r Record) ToWire() Wire {
	return Wire{
		Seq:        r.Seq,
		Time:       r.Time.UTC().Format(time.RFC3339Nano),
		Org:        r.Actor.Org,
		Sub:        r.Actor.Sub,
		Email:      r.Actor.Email,
		Home:       r.Actor.Home,
		Action:     r.Action,
		Resource:   r.Resource.Type,
		ResourceID: r.Resource.ID,
		Method:     r.Method,
		Path:       r.Path,
		Result:     r.Outcome.Result,
		Status:     r.Outcome.Status,
		Reason:     r.Outcome.Reason,
		SourceIP:   r.SourceIP,
		UserAgent:  r.UserAgent,
		RequestID:  r.RequestID,
		IsAdmin:    r.Auth.IsAdmin,
		Auth:       r.Auth.Method,
		Hash:       r.Hash,
		PrevHash:   r.PrevHash,
	}
}
