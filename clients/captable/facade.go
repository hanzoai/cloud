package captable

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hanzoai/cloud/clients/gojabase"
)

// facade.go is the in-process cap-table seam: it lets a sibling subsystem (Hanzo
// Company's formation + import + fundraising flows) write to a tenant's cap table
// WITHOUT an HTTP hop, dispatching the SAME goja bundle routes the /v1/captable/*
// handlers do (one transaction per call, per-tenant Base). Every function is
// org-scoped — the caller MUST pass an already-validated org, which selects the
// tenant DB file and scopes every row. The route bodies match the captable bundle
// contracts exactly (schema.go + the bundle route handlers).

// ErrNotMounted is returned when captable has not been mounted. A caller treats it
// as "cap table unavailable".
var ErrNotMounted = errors.New("captable: subsystem not mounted")

// StakeholderInput mirrors the stakeholders.add bundle contract.
type StakeholderInput struct {
	Name                string `json:"name"`
	Email               string `json:"email"`
	StakeholderType     string `json:"stakeholderType"`     // INDIVIDUAL | INSTITUTION
	CurrentRelationship string `json:"currentRelationship"` // FOUNDER | INVESTOR | EMPLOYEE …
	InstitutionName     string `json:"institutionName,omitempty"`
}

// ShareClassInput mirrors the shareClasses.create bundle contract.
type ShareClassInput struct {
	Name                          string  `json:"name"`
	ClassType                     string  `json:"classType"` // COMMON | PREFERRED
	InitialSharesAuthorized       int64   `json:"initialSharesAuthorized"`
	VotesPerShare                 int     `json:"votesPerShare"`
	ParValue                      float64 `json:"parValue"`
	PricePerShare                 float64 `json:"pricePerShare"`
	Seniority                     int     `json:"seniority"`
	ConversionRights              string  `json:"conversionRights"`
	BoardApprovalDate             string  `json:"boardApprovalDate"`
	StockholderApprovalDate       string  `json:"stockholderApprovalDate"`
	LiquidationPreferenceMultiple float64 `json:"liquidationPreferenceMultiple"`
	ParticipationCapMultiple      float64 `json:"participationCapMultiple"`
}

// ShareInput mirrors the shares.add bundle contract.
type ShareInput struct {
	StakeholderID     string `json:"stakeholderId"`
	ShareClassID      string `json:"shareClassId"`
	CertificateID     string `json:"certificateId"`
	Quantity          int64  `json:"quantity"`
	Status            string `json:"status"`    // ACTIVE | DRAFT
	IssueDate         string `json:"issueDate"` // ISO date
	CliffYears        int    `json:"cliffYears"`
	VestingYears      int    `json:"vestingYears"`
	BoardApprovalDate string `json:"boardApprovalDate"`
}

// RoundInput mirrors the rounds.create bundle contract.
type RoundInput struct {
	Name              string  `json:"name"`
	RoundType         string  `json:"roundType"` // PRICED | SAFE | CONVERTIBLE_NOTE
	TargetAmount      float64 `json:"targetAmount"`
	PreMoneyValuation float64 `json:"preMoneyValuation,omitempty"`
	PricePerShare     float64 `json:"pricePerShare,omitempty"`
	ShareClassID      string  `json:"shareClassId,omitempty"`
}

// facadeDispatch runs one bundle route on the tenant's store and returns the raw
// response. The body is round-tripped through JSON to a generic value so the goja
// bundle sees the SAME wire shape (lower-case json keys) the HTTP path produces —
// passing a typed Go struct straight to goja would expose Go field names instead.
func facadeDispatch(ctx context.Context, org, route string, params map[string]string, body any) (*gojabase.Response, error) {
	if mounted == nil || mounted.State.host == nil {
		return nil, ErrNotMounted
	}
	if org == "" {
		return nil, fmt.Errorf("captable: empty org")
	}
	wire, err := toWire(body)
	if err != nil {
		return nil, err
	}
	return mounted.State.host.Dispatch(ctx, org, gojabase.Request{Route: route, Params: params, Body: wire})
}

// toWire normalizes a typed value to a generic JSON value (map[string]any /
// []any / scalar), matching what a decoded HTTP request body looks like.
func toWire(body any) (any, error) {
	if body == nil {
		return nil, nil
	}
	b, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("captable: encode body: %w", err)
	}
	var out any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("captable: normalize body: %w", err)
	}
	return out, nil
}

// okBody checks the response is 2xx and returns the body bytes, else a descriptive
// error carrying the bundle's own message.
func okBody(resp *gojabase.Response, route string) ([]byte, error) {
	if resp.Status/100 != 2 {
		return nil, fmt.Errorf("captable %s: status %d: %s", route, resp.Status, string(resp.Body))
	}
	return resp.Body, nil
}

// SetIncorporation records the entity kind on the tenant's canonical company row —
// the cap-table-layer "org is now a company" fact. companyName is required by the
// bundle's company.update contract.
func SetIncorporation(ctx context.Context, org, companyName, incType, country, state string) error {
	resp, err := facadeDispatch(ctx, org, "company.update", nil, map[string]any{
		"name":                 companyName,
		"incorporationType":    incType,
		"incorporationCountry": country,
		"incorporationState":   state,
	})
	if err != nil {
		return err
	}
	_, err = okBody(resp, "company.update")
	return err
}

// AddStakeholders adds one or more stakeholders (dedup by email in the bundle) and
// returns how many rows were inserted.
func AddStakeholders(ctx context.Context, org string, holders []StakeholderInput) (int, error) {
	if len(holders) == 0 {
		return 0, nil
	}
	resp, err := facadeDispatch(ctx, org, "stakeholders.add", nil, holders)
	if err != nil {
		return 0, err
	}
	body, err := okBody(resp, "stakeholders.add")
	if err != nil {
		return 0, err
	}
	var out struct {
		Inserted int `json:"inserted"`
	}
	_ = json.Unmarshal(body, &out)
	return out.Inserted, nil
}

// StakeholderIDsByEmail lists the tenant's stakeholders and returns an email→id map,
// so a caller that just added founders can resolve their ids to issue shares.
func StakeholderIDsByEmail(ctx context.Context, org string) (map[string]string, error) {
	resp, err := facadeDispatch(ctx, org, "stakeholders.list", nil, nil)
	if err != nil {
		return nil, err
	}
	body, err := okBody(resp, "stakeholders.list")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("captable stakeholders.list: decode: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.Email] = r.ID
	}
	return out, nil
}

// EnsureShareClass creates a share class and returns its id (resolved via the list
// route, since shareClasses.create does not echo the id). If a class with the same
// name already exists it returns that existing id (idempotent seed).
func EnsureShareClass(ctx context.Context, org string, in ShareClassInput) (string, error) {
	if id, _ := shareClassIDByName(ctx, org, in.Name); id != "" {
		return id, nil
	}
	if in.ConversionRights == "" {
		in.ConversionRights = "CONVERTS_TO_FUTURE_ROUND"
	}
	if in.ClassType == "" {
		in.ClassType = "COMMON"
	}
	// The bundle requires the approval dates and the preference/participation
	// multiples on every class. Default them for a plain founding common class
	// (approved today, 1x non-participating) so a caller need only name the class.
	if in.BoardApprovalDate == "" {
		in.BoardApprovalDate = time.Now().Format("2006-01-02")
	}
	if in.StockholderApprovalDate == "" {
		in.StockholderApprovalDate = in.BoardApprovalDate
	}
	resp, err := facadeDispatch(ctx, org, "shareClasses.create", nil, in)
	if err != nil {
		return "", err
	}
	if _, err := okBody(resp, "shareClasses.create"); err != nil {
		return "", err
	}
	return shareClassIDByName(ctx, org, in.Name)
}

func shareClassIDByName(ctx context.Context, org, name string) (string, error) {
	resp, err := facadeDispatch(ctx, org, "shareClasses.list", nil, nil)
	if err != nil {
		return "", err
	}
	body, err := okBody(resp, "shareClasses.list")
	if err != nil {
		return "", err
	}
	var rows []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return "", fmt.Errorf("captable shareClasses.list: decode: %w", err)
	}
	for _, r := range rows {
		if r.Name == name {
			return r.ID, nil
		}
	}
	return "", nil
}

// IssueShares issues a share certificate to a stakeholder.
func IssueShares(ctx context.Context, org string, in ShareInput) error {
	if in.Status == "" {
		in.Status = "ACTIVE"
	}
	if in.IssueDate == "" {
		in.IssueDate = time.Now().Format("2006-01-02")
	}
	if in.BoardApprovalDate == "" {
		in.BoardApprovalDate = in.IssueDate
	}
	resp, err := facadeDispatch(ctx, org, "shares.add", nil, in)
	if err != nil {
		return err
	}
	_, err = okBody(resp, "shares.add")
	return err
}

// RecordRound records a fundraising round and returns its id.
func RecordRound(ctx context.Context, org string, in RoundInput) (string, error) {
	resp, err := facadeDispatch(ctx, org, "rounds.create", nil, in)
	if err != nil {
		return "", err
	}
	body, err := okBody(resp, "rounds.create")
	if err != nil {
		return "", err
	}
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.ID == "" {
		return "", fmt.Errorf("captable rounds.create: malformed response: %s", string(body))
	}
	return out.ID, nil
}
