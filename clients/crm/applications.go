package crm

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// Public-intake safety limits.
const (
	// maxIntakeBody caps the raw JSON body of a public application (spam/DoS
	// amplification guard on an unauthenticated route).
	maxIntakeBody = 64 * 1024
	// intakeRateLimit / intakeRateWindow bound submissions per client IP.
	intakeRateLimit  = 20
	intakeRateWindow = time.Minute
	// screenTimeout bounds one AI screen call independent of the request.
	screenTimeout = 60 * time.Second
)

// creditTiers is the allowed suggested-credit ladder (USD). The AI's suggestion
// is snapped to the nearest rung.
var creditTiers = []int{0, 5000, 25000, 50000, 150000}

// tier1Funds is the built-in tier-1 backer list. Lowercased substrings matched
// against the applicant's selected funds and free-text investors. Deterministic
// — independent of the AI screen's own judgement.
var tier1Funds = []string{
	"a16z", "andreessen horowitz", "sequoia", "benchmark", "greylock", "accel",
	"founders fund", "lightspeed", "index ventures", "index", "bessemer",
	"khosla", "gv", "google ventures", "kleiner perkins", "general catalyst",
	"thrive", "coatue", "insight partners", "tiger global", "y combinator",
	"combinator", "techstars", "first round", "craft ventures", "8vc", "ggv",
	"nea", "new enterprise",
}

// applyRequest is the public application payload posted by hanzo.ai/startups.
// `stage` here is the applicant's FUNDING stage (idea/seed/…), NOT the pipeline
// stage — the pipeline stage is server-owned and starts at "applied".
type applyRequest struct {
	Company        string   `json:"company"`
	Website        string   `json:"website"`
	ContactName    string   `json:"contactName"`
	Email          string   `json:"email"`
	Role           string   `json:"role"`
	FundingStage   string   `json:"stage"`
	Investors      string   `json:"investors"`
	Tier1Investors []string `json:"tier1Investors"`
	AmountRaised   string   `json:"amountRaised"`
	TeamSize       string   `json:"teamSize"`
	Building       string   `json:"building"`
	UseCases       []string `json:"useCases"`
	InfraSpend     string   `json:"infraSpend"`
	BYOCompute     bool     `json:"byoCompute"`
	BYOHardware    string   `json:"byoHardware"`
	Techstars      string   `json:"techstarsBatch"`
	HeardVia       string   `json:"heardVia"`
	Honeypot       string   `json:"hp"`
	SubmittedAt    string   `json:"submittedAt"`
}

// intakeOrg returns the org that owns the Startup Program pipeline. It is the
// deployment brand (white-labels to zoo/lux), defaulting to "hanzo".
func (s *svc) intakeOrg() string {
	if b := strings.TrimSpace(s.brand); b != "" {
		return b
	}
	return "hanzo"
}

// actor returns the validated staff user id for event attribution, or "staff".
func actor(c *zip.Ctx) string {
	if u := strings.TrimSpace(c.User()); u != "" {
		return u
	}
	return "staff"
}

// ---- public intake ----

// apply is the UNAUTHENTICATED public application endpoint. It validates, drops
// honeypot hits, dedups on (email, company), writes the Application (+ a
// best-effort CRM Company/Contact for sales visibility), and kicks off the AI
// screen. It NEVER calls tenant(): the org is the fixed program org.
func (s *svc) apply(c *zip.Ctx) error {
	if body := c.Body(); len(body) > maxIntakeBody {
		return zip.ErrBadRequest("application too large")
	}
	var req applyRequest
	if err := c.Bind(&req); err != nil {
		return zip.ErrBadRequest("invalid JSON")
	}

	// Honeypot: a real browser never fills the hidden field. Answer 200 so a bot
	// cannot distinguish a drop from a success, and store nothing.
	if strings.TrimSpace(req.Honeypot) != "" {
		return c.JSON(http.StatusOK, map[string]any{"status": "received"})
	}

	company := clip(req.Company)
	name := clip(req.ContactName)
	email := clip(req.Email)
	if company == "" {
		return zip.ErrBadRequest("company is required")
	}
	if name == "" {
		return zip.ErrBadRequest("contactName is required")
	}
	if !validEmail(email) {
		return zip.ErrBadRequest("a valid email is required")
	}

	org := s.intakeOrg()
	now := time.Now().Unix()
	tier1, matched := detectTier1(req.Tier1Investors, req.Investors)
	meta := buildMetadata(req, matched)

	// Idempotent-ish: a resubmission of the same (email, company) refreshes the
	// existing record rather than duplicating the lead.
	existing, err := s.store.FindApplicationByEmailCompany(c.Context(), org, email, company)
	if err == nil {
		existing.Company, existing.Website = company, clip(req.Website)
		existing.ContactName, existing.Email, existing.Role = name, email, clip(req.Role)
		existing.Tier1 = tier1
		existing.Metadata = meta
		existing.UpdatedAt = now
		saved, uerr := s.store.UpdateApplication(c.Context(), existing)
		if uerr != nil {
			return mapErr(uerr, "application not found")
		}
		s.screenLater(org, saved.ID)
		return c.JSON(http.StatusOK, map[string]any{"id": saved.ID, "stage": saved.Stage, "status": "received"})
	}

	id, gerr := genID("appl")
	if gerr != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", gerr)
	}
	app := Application{
		ID: id, Org: org, Company: company, Website: clip(req.Website),
		ContactName: name, Email: email, Role: clip(req.Role),
		Stage: StageApplied, Tier1: tier1, Metadata: meta,
		Screen: ScreenResult{Status: "pending"},
		Events: []StageEvent{{To: StageApplied, At: now, By: "system", Note: "application received"}},
		CreatedAt: now, UpdatedAt: now,
	}
	// Best-effort CRM projection so the lead also shows in the standard CRM tabs.
	app.CompanyID, app.ContactID = s.projectToCRM(c.Context(), org, app, req)

	saved, cerr := s.store.CreateApplication(c.Context(), app)
	if cerr != nil {
		return mapErr(cerr, "")
	}
	s.screenLater(org, saved.ID)
	return c.JSON(http.StatusCreated, map[string]any{"id": saved.ID, "stage": saved.Stage, "status": "received"})
}

// projectToCRM creates a thin CRM Company + Contact linked to the application, so
// the startup also appears in the org's standard CRM. Best-effort: any failure
// is non-fatal (the Application remains the source of truth) and returns empty
// ids. Reuses the same store + referential-integrity rules as the CRM handlers.
func (s *svc) projectToCRM(ctx context.Context, org string, app Application, req applyRequest) (companyID, contactID string) {
	now := time.Now().Unix()
	cid, err := genID("comp")
	if err != nil {
		return "", ""
	}
	comp := Company{
		ID: cid, Org: org, Name: app.Company, DomainName: domainOf(app.Website),
		Employees: parseIntField(req.TeamSize), Currency: "USD",
		CreatedAt: now, UpdatedAt: now,
	}
	if _, err := s.store.CreateCompany(ctx, comp); err != nil {
		s.log.Warn("startup intake: company projection failed", "err", err)
		return "", ""
	}
	companyID = cid

	first, last := splitName(app.ContactName)
	ctid, err := genID("cont")
	if err != nil {
		return companyID, ""
	}
	ct := Contact{
		ID: ctid, Org: org, FirstName: first, LastName: last, Email: app.Email,
		JobTitle: app.Role, CompanyID: companyID, CreatedAt: now, UpdatedAt: now,
	}
	if _, err := s.store.CreateContact(ctx, ct); err != nil {
		s.log.Warn("startup intake: contact projection failed", "err", err)
		return companyID, ""
	}
	return companyID, ctid
}

// buildMetadata assembles the full submitted payload (every field) plus the
// deterministic tier-1 match — the complete record of what the applicant sent.
func buildMetadata(req applyRequest, tier1Matched []string) map[string]any {
	return map[string]any{
		"company":        clip(req.Company),
		"website":        clip(req.Website),
		"contactName":    clip(req.ContactName),
		"email":          clip(req.Email),
		"role":           clip(req.Role),
		"fundingStage":   clip(req.FundingStage),
		"investors":      clip(req.Investors),
		"tier1Investors": clipSlice(req.Tier1Investors),
		"tier1Matched":   tier1Matched,
		"amountRaised":   clip(req.AmountRaised),
		"teamSize":       clip(req.TeamSize),
		"building":       clip(req.Building),
		"useCases":       clipSlice(req.UseCases),
		"infraSpend":     clip(req.InfraSpend),
		"byoCompute":     req.BYOCompute,
		"byoHardware":    clip(req.BYOHardware),
		"techstarsBatch": clip(req.Techstars),
		"heardVia":       clip(req.HeardVia),
		"submittedAt":    clip(req.SubmittedAt),
	}
}

// ---- staff read/write ----

func (s *svc) listApplications(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	stage := strings.TrimSpace(c.Query("stage"))
	if stage != "" && !validStages[stage] {
		return zip.ErrBadRequest("unknown stage")
	}
	rows, err := s.store.ListApplications(c.Context(), org, stage, limitOf(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"data": rows})
}

func (s *svc) getApplication(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	app, err := s.store.GetApplication(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "application not found")
	}
	return c.JSON(http.StatusOK, app)
}

// patchApplicationReq is the staff mutation: advance the pipeline stage (with a
// required reason when rejecting) and/or attach a note.
type patchApplicationReq struct {
	Stage  string `json:"stage"`
	Reason string `json:"reason"`
	Note   string `json:"note"`
}

func (s *svc) patchApplication(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var req patchApplicationReq
	if err := c.Bind(&req); err != nil {
		return zip.ErrBadRequest("invalid JSON")
	}
	app, err := s.store.GetApplication(c.Context(), org, idParam(c))
	if err != nil {
		return mapErr(err, "application not found")
	}

	target := strings.TrimSpace(req.Stage)
	if target != "" && target != app.Stage {
		if ok, why := canTransition(app.Stage, target); !ok {
			return zip.ErrBadRequest("invalid stage transition: " + why)
		}
		if target == StageRejected && strings.TrimSpace(req.Reason) == "" {
			return zip.ErrBadRequest("reason is required to reject")
		}
		app.Events = append(app.Events, StageEvent{
			From: app.Stage, To: target, At: time.Now().Unix(),
			By: actor(c), Note: clip(req.Note),
		})
		app.Stage = target
		if target == StageRejected {
			app.Reason = clip(req.Reason)
		}
	} else if note := clip(req.Note); note != "" {
		// A note without a stage change is still recorded on the timeline.
		app.Events = append(app.Events, StageEvent{
			From: app.Stage, To: app.Stage, At: time.Now().Unix(), By: actor(c), Note: note,
		})
	}
	app.UpdatedAt = time.Now().Unix()
	saved, uerr := s.store.UpdateApplication(c.Context(), app)
	if uerr != nil {
		return mapErr(uerr, "application not found")
	}
	return c.JSON(http.StatusOK, saved)
}

// ---- stage machine ----

// canTransition reports whether from→to is a legal pipeline move: advance exactly
// one step, move back to any earlier stage (correction), reject from any non-
// rejected stage, or reopen a rejected application to `applied`. Same-stage is a
// no-op (allowed). Returns (false, reason) otherwise.
func canTransition(from, to string) (bool, string) {
	if !validStages[to] {
		return false, "unknown stage"
	}
	if from == to {
		return true, ""
	}
	if to == StageRejected {
		return from != StageRejected, "already rejected"
	}
	if from == StageRejected {
		if to == StageApplied {
			return true, ""
		}
		return false, "a rejected application can only reopen to applied"
	}
	fi, ti := stageIndex(from), stageIndex(to)
	if fi < 0 || ti < 0 {
		return false, "not a pipeline stage"
	}
	if ti == fi+1 || ti < fi {
		return true, ""
	}
	return false, "cannot skip stages"
}

func stageIndex(stage string) int {
	for i, s := range stageOrder {
		if s == stage {
			return i
		}
	}
	return -1
}

// ---- AI screen ----

// screenLater schedules the AI screen. In production it runs detached (a public
// form must not block on a 60s model call); tests set screenSync to run inline
// for deterministic assertions. The screen logic is identical either way.
func (s *svc) screenLater(org, id string) {
	if s.screenSync {
		s.runScreen(context.Background(), org, id)
		return
	}
	go s.runScreen(context.Background(), org, id)
}

// runScreen calls the LLM gateway to score one application and stores the result.
// Non-fatal by construction: a nil AI client, a gateway error, or an unparseable
// response marks the screen `failed` and leaves the application untouched at
// `applied`. On success it stores the screen and auto-advances applied→screened.
func (s *svc) runScreen(ctx context.Context, org, id string) {
	app, err := s.store.GetApplication(ctx, org, id)
	if err != nil {
		s.log.Warn("screen: load failed", "id", id, "err", err)
		return
	}
	now := time.Now().Unix()

	if s.ai == nil {
		app.Screen = ScreenResult{Status: "failed", Error: "ai gateway not configured", ScreenedAt: now}
		s.saveScreen(ctx, app)
		return
	}

	cctx, cancel := context.WithTimeout(ctx, screenTimeout)
	defer cancel()
	resp, aerr := s.ai.ChatCompletion(cctx, &types.ChatRequest{Model: s.defaultModel, Prompt: screenPrompt(app), Org: org})
	if aerr != nil {
		app.Screen = ScreenResult{Status: "failed", Error: aerr.Error(), Model: s.defaultModel, ScreenedAt: now}
		s.saveScreen(ctx, app)
		return
	}
	screen, perr := parseScreen(resp.Content)
	if perr != nil {
		app.Screen = ScreenResult{Status: "failed", Error: "unparseable screen: " + perr.Error(), Model: s.defaultModel, ScreenedAt: now}
		s.saveScreen(ctx, app)
		return
	}
	screen.Status = "done"
	screen.Model = s.defaultModel
	screen.ScreenedAt = now
	app.Screen = screen

	// Auto-advance the freshly-screened application one step.
	if app.Stage == StageApplied {
		app.Events = append(app.Events, StageEvent{
			From: StageApplied, To: StageScreened, At: now, By: "system", Note: "auto-screened by AI",
		})
		app.Stage = StageScreened
	}
	s.saveScreen(ctx, app)
}

func (s *svc) saveScreen(ctx context.Context, app Application) {
	app.UpdatedAt = time.Now().Unix()
	if _, err := s.store.UpdateApplication(ctx, app); err != nil {
		s.log.Warn("screen: save failed", "id", app.ID, "err", err)
	}
}

// screenPrompt renders the strict-JSON scoring instruction for one application.
func screenPrompt(app Application) string {
	payload, _ := json.MarshalIndent(app.Metadata, "", "  ")
	var b strings.Builder
	b.WriteString("You are an analyst screening applications to the Hanzo Startup Program, ")
	b.WriteString("Hanzo AI's credits-and-infrastructure program for venture-backed startups.\n\n")
	b.WriteString("Tier-1 investors include (non-exhaustive): Andreessen Horowitz (a16z), Sequoia, ")
	b.WriteString("Benchmark, Greylock, Accel, Founders Fund, Lightspeed, Index Ventures, Bessemer, ")
	b.WriteString("Khosla Ventures, GV, Kleiner Perkins, General Catalyst, NEA, Y Combinator, and Techstars.\n\n")
	b.WriteString("The credits program requires tier-1 backing. Credit tiers (USD): 0, 5000, 25000, 50000, 150000. ")
	b.WriteString("150000 is reserved for strong tier-1-backed teams; 0 means recommend the free tier only.\n\n")
	b.WriteString("Application:\n")
	b.Write(payload)
	b.WriteString("\n\nReturn ONLY a JSON object, no prose, with exactly these keys:\n")
	b.WriteString(`{"score": <int 0-100>, "tier1_backed": "yes"|"no"|"unclear", `)
	b.WriteString(`"summary": "<two-line summary>", "suggested_credits": <0|5000|25000|50000|150000>, `)
	b.WriteString(`"draft_reply": "<short friendly email reply, 3-5 sentences>"}`)
	return b.String()
}

// screenJSON is the loose shape parsed from the model's reply.
type screenJSON struct {
	Score            *int   `json:"score"`
	Tier1Backed      string `json:"tier1_backed"`
	Summary          string `json:"summary"`
	SuggestedCredits *int   `json:"suggested_credits"`
	DraftReply       string `json:"draft_reply"`
}

// parseScreen extracts and normalizes the screen JSON from a model reply, which
// may be wrapped in markdown fences or surrounding prose.
func parseScreen(content string) (ScreenResult, error) {
	raw := extractJSONObject(content)
	if raw == "" {
		return ScreenResult{}, errNoJSON
	}
	var sj screenJSON
	if err := json.Unmarshal([]byte(raw), &sj); err != nil {
		return ScreenResult{}, err
	}
	res := ScreenResult{
		Tier1Backed: normTier1Backed(sj.Tier1Backed),
		Summary:     clip(sj.Summary),
		DraftReply:  clipTo(sj.DraftReply, 4000),
	}
	if sj.Score != nil {
		res.Score = clampInt(*sj.Score, 0, 100)
	}
	if sj.SuggestedCredits != nil {
		res.SuggestedCredits = snapCredits(*sj.SuggestedCredits)
	}
	return res, nil
}

var errNoJSON = errStr("no JSON object found in reply")

type errStr string

func (e errStr) Error() string { return string(e) }

// extractJSONObject returns the substring from the first '{' to the last '}'.
func extractJSONObject(s string) string {
	i := strings.IndexByte(s, '{')
	j := strings.LastIndexByte(s, '}')
	if i < 0 || j < 0 || j < i {
		return ""
	}
	return s[i : j+1]
}

func normTier1Backed(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "yes", "true", "y":
		return "yes"
	case "no", "false", "n":
		return "no"
	default:
		return "unclear"
	}
}

func snapCredits(v int) int {
	best := creditTiers[0]
	bestD := absInt(v - best)
	for _, t := range creditTiers[1:] {
		if d := absInt(v - t); d < bestD {
			best, bestD = t, d
		}
	}
	return best
}

// ---- small pure helpers ----

// detectTier1 reports whether the applicant claims tier-1 backing (any selected
// fund, or a free-text investor matching the built-in list), plus the matches.
func detectTier1(selected []string, investors string) (bool, []string) {
	matched := make([]string, 0, 4)
	seen := map[string]bool{}
	for _, sel := range selected {
		if v := strings.TrimSpace(sel); v != "" && !seen[strings.ToLower(v)] {
			matched = append(matched, v)
			seen[strings.ToLower(v)] = true
		}
	}
	hay := strings.ToLower(investors)
	for _, f := range tier1Funds {
		if strings.Contains(hay, f) && !seen[f] {
			matched = append(matched, f)
			seen[f] = true
		}
	}
	return len(matched) > 0, matched
}

func validEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	dot := strings.LastIndexByte(s, '.')
	return dot > at+1 && dot < len(s)-1 && !strings.ContainsAny(s, " \t\n")
}

func clipSlice(in []string) []string {
	if len(in) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(in))
	for i, v := range in {
		if i >= 64 {
			break
		}
		out = append(out, clip(v))
	}
	return out
}

func clipTo(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}

func domainOf(website string) string {
	w := strings.TrimSpace(website)
	w = strings.TrimPrefix(w, "https://")
	w = strings.TrimPrefix(w, "http://")
	w = strings.TrimPrefix(w, "www.")
	if i := strings.IndexAny(w, "/?#"); i >= 0 {
		w = w[:i]
	}
	return clip(w)
}

func splitName(full string) (first, last string) {
	parts := strings.Fields(strings.TrimSpace(full))
	if len(parts) == 0 {
		return "", ""
	}
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], strings.Join(parts[1:], " ")
}

func parseIntField(s string) int64 {
	digits := strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, s)
	n, err := strconv.ParseInt(digits, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
