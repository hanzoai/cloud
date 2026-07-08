package audit

import "time"

// Wire is the JSON shape of one audit record on the operator/console contract. It
// is cloud's OWN record projection — richer than the IAM record it supersedes: it
// carries the outcome, the validated auth context, and the hash-chain linkage
// (Hash/PrevHash) so a console can show tamper-evidence per row. The JSON tags ARE
// the contract; both the admin god-view (/v1/admin/audit) and the org-scoped trail
// (/v1/audit) serialize this ONE shape so a single console adapter reads either.
type Wire struct {
	Seq        uint64 `json:"seq"`
	Time       string `json:"time"`
	Org        string `json:"org"`
	Sub        string `json:"sub"`
	Email      string `json:"email,omitempty"`
	Action     string `json:"action"`
	Resource   string `json:"resource"`
	ResourceID string `json:"resourceId,omitempty"`
	Method     string `json:"method,omitempty"`
	Path       string `json:"path,omitempty"`
	Result     string `json:"result"`
	Status     int    `json:"status"`
	Reason     string `json:"reason,omitempty"`
	SourceIP   string `json:"sourceIp,omitempty"`
	UserAgent  string `json:"userAgent,omitempty"`
	RequestID  string `json:"requestId,omitempty"`
	IsAdmin    bool   `json:"isAdmin"`
	Auth       string `json:"authMethod,omitempty"`
	Hash       string `json:"hash"`
	PrevHash   string `json:"prevHash"`
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
