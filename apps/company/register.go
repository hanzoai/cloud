package company

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// register.go is the platform's own book.
//
// Hanzo forms the entity, so Hanzo carries the formation KYC/AML obligation. That
// obligation is not discharged per-tenant: it is answered across the whole book —
// how many entities we formed, which founders await a decision, what has been
// stalled long enough to chase. Every other read in this package is keyed by org
// and structurally cannot answer those.
//
// So these are SuperAdmin OPERATIONS, not gates on a founder's path. They do not
// touch the formation machine: nothing here advances a stage, and no tenant can
// reach them. The register reads; the machine runs; neither knows the other.

// reviewer resolves the Hanzo platform reviewer behind a typed op: the validated
// SuperAdmin principal, and their user id for attribution.
//
// It is the ONE place this package reaches for the REQUEST rather than the
// tenant, and it must: platform-ness lives in a header (X-User-IsAdmin, which
// only the identity boundary can mint) that principal.OrgFrom does not carry, and
// a reviewer is attributed by X-User-Id. It fails closed off the HTTP path, where
// there is no request and therefore no attested reviewer.
func reviewer(ctx context.Context) (string, bool) {
	c, ok := cloud.Request(ctx)
	if !ok || !principal.IsSuperAdmin(c) {
		return "", false
	}
	return c.User(), true
}

// requirePlatform admits only a Hanzo platform reviewer — a member of the reserved
// `admin` org (owner == "admin"), the one cross-tenant scope. Fails closed.
func requirePlatform(ctx context.Context) error {
	if _, ok := reviewer(ctx); !ok {
		return zip.ErrForbidden("the formation register is a Hanzo platform operation")
	}
	return nil
}

// registerFilter narrows the register. Every field is a query parameter and every
// one is optional; an unparseable number reads as its default, exactly as it did
// before these were typed.
type registerFilter struct {
	// Stage keeps only formations at that stage. Empty means any.
	Stage string `json:"stage"`
	// Structure keeps only formations of that entity kind. Empty means any.
	Structure string `json:"structure"`
	// Limit bounds the page; 0 or less means the default of 200.
	Limit int `json:"limit"`
	// Offset skips that many rows.
	Offset int `json:"offset"`
}

// registerPage is one page of the platform's formation register.
type registerPage struct {
	// Formations are the rows, newest activity first.
	Formations []Registration `json:"formations"`
	// Count is how many rows this page holds.
	Count int `json:"count"`
	// Limit is the page size that was applied.
	Limit int `json:"limit"`
	// Offset is the offset that was applied.
	Offset int `json:"offset"`
}

// ListRegister returns the platform's whole formation register, newest activity
// first — every org's formation, not the caller's. It is a Hanzo platform
// operation: a caller who is not a platform reviewer gets 403.
//
// Filter by stage and structure, page with limit and offset. An unknown stage is
// refused with 400 rather than returning a silently empty page.
//
// Example: {"stage": "founders", "limit": 50}
func (o ops) registerList(ctx context.Context, in *registerFilter) (*registerPage, error) {
	if err := requirePlatform(ctx); err != nil {
		return nil, err
	}
	f := Filter{
		Stage:     Stage(strings.ToLower(strings.TrimSpace(in.Stage))),
		Structure: Structure(strings.ToLower(strings.TrimSpace(in.Structure))),
		Limit:     in.Limit,
		Offset:    in.Offset,
	}
	if f.Stage != "" && !knownStage(f.Stage) {
		return nil, zip.ErrBadRequest("unknown stage")
	}
	rows, err := o.s.State.store.List(ctx, f)
	if err != nil {
		return nil, err
	}
	return &registerPage{
		Formations: rows,
		Count:      len(rows),
		Limit:      orDefault(f.Limit, defaultRegisterLimit),
		Offset:     f.Offset,
	}, nil
}

// registerCounts is the register's shape: how many formations sit at each stage.
type registerCounts struct {
	// ByStage counts formations per stage, keyed by the stage name.
	ByStage map[string]int `json:"byStage"`
	// Total is every formation in the register.
	Total int `json:"total"`
}

// SummarizeRegister counts the platform's formations by stage — the register's
// shape in one read, so a queue that is growing is visible as a number rather
// than inferred by paging the list. A Hanzo platform operation: a caller who is
// not a platform reviewer gets 403.
func (o ops) registerSummary(ctx context.Context, _ *noInput) (*registerCounts, error) {
	if err := requirePlatform(ctx); err != nil {
		return nil, err
	}
	counts, err := o.s.State.store.Count(ctx)
	if err != nil {
		return nil, err
	}
	byStage := map[string]int{}
	total := 0
	for stage, n := range counts {
		byStage[string(stage)] = n
		total += n
	}
	return &registerCounts{ByStage: byStage, Total: total}, nil
}

// waiting is one founder in the KYC decision queue.
type waiting struct {
	// Org is the tenant whose formation the founder belongs to.
	Org string `json:"org"`
	// Name is the proposed company name.
	Name string `json:"name"`
	// Founder is the founder's name.
	Founder string `json:"founder"`
	// Email is the founder's email — the key a decision is posted against.
	Email string `json:"email"`
	// KYCStatus is the founder's unsettled status.
	KYCStatus string `json:"kycStatus"`
	// KYCRef is the identity-verification session reference, when one was opened.
	KYCRef string `json:"kycRef,omitempty"`
	// Since is when the formation was last touched, as a unix second.
	Since int64 `json:"since"`
}

// reviewQueue is the founders awaiting a KYC decision.
type reviewQueue struct {
	// Queue is one entry per unsettled founder, oldest formation first.
	Queue []waiting `json:"queue"`
	// Count is how many founders are waiting.
	Count int `json:"count"`
}

// ReviewQueue reports the founders whose KYC is not yet settled, oldest formation
// first, so the queue drains in the order founders have been waiting. A Hanzo
// platform operation: a caller who is not a platform reviewer gets 403.
//
// It only says who is waiting; the decision itself is POST
// /v1/company/kyc/decision.
//
// Example: {"limit": 50}
func (o ops) registerReview(ctx context.Context, in *reviewFilter) (*reviewQueue, error) {
	if err := requirePlatform(ctx); err != nil {
		return nil, err
	}
	pending, err := o.s.State.store.Pending(ctx, in.Limit)
	if err != nil {
		return nil, err
	}

	queue := []waiting{}
	for _, f := range pending {
		for _, fo := range f.Founders {
			if settledKYC(fo.KYCStatus) {
				continue
			}
			queue = append(queue, waiting{
				Org:       f.Org,
				Name:      f.Name,
				Founder:   fo.Name,
				Email:     fo.Email,
				KYCStatus: fo.KYCStatus,
				KYCRef:    fo.KYCRef,
				Since:     f.UpdatedAt,
			})
		}
	}
	return &reviewQueue{Queue: queue, Count: len(queue)}, nil
}

// reviewFilter bounds the decision queue.
type reviewFilter struct {
	// Limit bounds how many formations are scanned; 0 or less means the default of 200.
	Limit int `json:"limit"`
}

// settledKYC reports whether a founder's KYC has reached a terminal status.
// Verified and reviewer-confirmed stay DISTINCT values everywhere — the manual
// path never launders itself into looking provider-reported — but both are done,
// so neither belongs in a review queue.
func settledKYC(status string) bool {
	switch status {
	case KYCVerified, KYCReviewerConfirmed, KYCFailed:
		return true
	}
	return false
}

// knownStage reports whether a stage is one the machine defines, so a filter typo
// is a 400 rather than a silently empty page.
func knownStage(s Stage) bool {
	switch s {
	case StageStructure, StageFounders, StagePayment, StageDocuments,
		StageEsign, StageGenesis, StageCompany, StageImport:
		return true
	}
	return false
}

func orDefault(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}
