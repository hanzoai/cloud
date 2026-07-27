package company

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
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

// requirePlatform admits only a Hanzo platform reviewer — a member of the reserved
// `admin` org (owner == "admin"), the one cross-tenant scope. Fails closed.
func requirePlatform(c *zip.Ctx) error {
	if !principal.IsSuperAdmin(c) {
		return zip.ErrForbidden("the formation register is a Hanzo platform operation")
	}
	return nil
}

// registerList serves GET /v1/company/register — the whole book, newest first.
//
//	?stage=founders      only that stage
//	?structure=c-corp    only that structure
//	?limit=&offset=      page (bounded; see defaultRegisterLimit)
func registerList(s *cloud.Service[state], c *zip.Ctx) error {
	if err := requirePlatform(c); err != nil {
		return err
	}
	f := Filter{
		Stage:     Stage(strings.ToLower(strings.TrimSpace(c.Query("stage")))),
		Structure: Structure(strings.ToLower(strings.TrimSpace(c.Query("structure")))),
		Limit:     atoi(c.Query("limit")),
		Offset:    atoi(c.Query("offset")),
	}
	if f.Stage != "" && !knownStage(f.Stage) {
		return zip.ErrBadRequest("unknown stage")
	}
	rows, err := s.State.store.List(c.Context(), f)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{
		"formations": rows,
		"count":      len(rows),
		"limit":      orDefault(f.Limit, defaultRegisterLimit),
		"offset":     f.Offset,
	})
}

// registerSummary serves GET /v1/company/register/summary — the book's shape: how
// many formations sit at each stage. A queue that is growing should be visible as
// a number, not inferred by paging the list.
func registerSummary(s *cloud.Service[state], c *zip.Ctx) error {
	if err := requirePlatform(c); err != nil {
		return err
	}
	counts, err := s.State.store.Count(c.Context())
	if err != nil {
		return err
	}
	byStage := map[string]int{}
	total := 0
	for stage, n := range counts {
		byStage[string(stage)] = n
		total += n
	}
	return c.JSON(http.StatusOK, map[string]any{"byStage": byStage, "total": total})
}

// registerReview serves GET /v1/company/review — the KYC decision queue.
//
// It reports founders NOT yet settled, oldest formation first, so the queue drains
// in the order founders have been waiting. The decision itself is POST
// /v1/company/kyc/decision; this only says who is waiting.
func registerReview(s *cloud.Service[state], c *zip.Ctx) error {
	if err := requirePlatform(c); err != nil {
		return err
	}
	pending, err := s.State.store.Pending(c.Context(), atoi(c.Query("limit")))
	if err != nil {
		return err
	}

	type waiting struct {
		Org       string `json:"org"`
		Name      string `json:"name"`
		Founder   string `json:"founder"`
		Email     string `json:"email"`
		KYCStatus string `json:"kycStatus"`
		KYCRef    string `json:"kycRef,omitempty"`
		Since     int64  `json:"since"`
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
	return c.JSON(http.StatusOK, map[string]any{"queue": queue, "count": len(queue)})
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

func atoi(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func orDefault(n, def int) int {
	if n <= 0 {
		return def
	}
	return n
}
