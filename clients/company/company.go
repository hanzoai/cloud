package company

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// company.go mounts the /v1/company surface and wires the state machine to its
// provider seams. The design is decomplected: ACTION endpoints populate the
// formation's data (structure, founders, KYC, payment, documents, esign, genesis,
// import), and ONE transition door — POST /v1/company/advance {to} — runs the
// guarded machine (Advance). Side effects live in the actions; ordering + gates live
// in the machine; the two never braid.
//
// Surface (all org-scoped; /v1 only):
//
//	POST   /v1/company                     begin a formation                  -> Formation (201)
//	GET    /v1/company                     the formation + next stages
//	PUT    /v1/company/structure           set structure/jurisdiction/name
//	POST   /v1/company/founders            set founders
//	POST   /v1/company/kyc                 start founder KYC (idv seam)
//	POST   /v1/company/kyc/callback        record a KYC result {email,status}
//	POST   /v1/company/payment             charge the $999 formation fee
//	POST   /v1/company/documents           generate formation docs → data room + file
//	POST   /v1/company/esign               request signatures on the docs
//	POST   /v1/company/esign/complete      record signing complete
//	POST   /v1/company/genesis             seed cap table + anchor equity genesis on-chain
//	POST   /v1/company/advance             {to} run the next guarded transition
//	POST   /v1/company/skip                mark already-incorporated (enables import)
//	POST   /v1/company/import/documents    ingest a Drive folder → data room
//	POST   /v1/company/import/captable     ingest a Sheet → captable
//	POST   /v1/company/fundraise/round     record a fundraising round (captable)
//	POST   /v1/company/fundraise/deck      share a deck in the data room
//	POST   /v1/company/fundraise/safe      request signature on a SAFE/note

const maxBody = 1 << 20 // 1 MiB — formation payloads are small structured records.

type state struct {
	store *Store
	prov  providerSet
}

var mounted *cloud.Service[state]

// Mount wires the company surface. It keeps a package global for Shutdown, so it
// constructs the Service value directly (the "complex flavour").
func Mount(app *zip.App, deps cloud.Deps) error {
	if app == nil {
		return fmt.Errorf("company.Mount: nil zip.App")
	}
	if deps.Logger == nil {
		return fmt.Errorf("company.Mount: nil deps.Logger")
	}
	if deps.DataDir == "" {
		return fmt.Errorf("company.Mount: empty DataDir")
	}
	if err := os.MkdirAll(deps.DataDir, 0o755); err != nil {
		return fmt.Errorf("company.Mount: data dir: %w", err)
	}
	store, err := openStore(filepath.Join(deps.DataDir, "company.db"))
	if err != nil {
		return fmt.Errorf("company.Mount: open store: %w", err)
	}
	b := cloud.NewBase(deps, "company")
	s := &cloud.Service[state]{Base: b, State: state{
		store: store,
		prov: providerSet{
			kyc:      manualKYC{},
			charge:   resourceCharger{bill: b.Bill},
			docs:     dataroomSink{},
			esign:    stubEsign{},
			captable: captableAdapter{},
			anchor:   newEVMAnchor(b.Log),
			filing:   stubFiling{},
			upgrade:  captableUpgrader{},
			google:   newHTTPGoogle(),
		},
	}}
	mounted = s
	routes(app, s)
	b.Log.Info("company mounted", "brand", deps.Brand, "feeCents", feeCents())
	return nil
}

func routes(app *zip.App, s *cloud.Service[state]) {
	app.Post("/v1/company", cloud.Handle(s, begin))
	app.Get("/v1/company", cloud.Handle(s, get))
	app.Put("/v1/company/structure", cloud.Handle(s, setStructure))
	app.Post("/v1/company/founders", cloud.Handle(s, setFounders))
	app.Post("/v1/company/kyc", cloud.Handle(s, startKYC))
	app.Post("/v1/company/kyc/callback", cloud.Handle(s, kycCallback))
	app.Post("/v1/company/payment", cloud.Handle(s, pay))
	app.Post("/v1/company/documents", cloud.Handle(s, generateDocuments))
	app.Post("/v1/company/esign", cloud.Handle(s, requestEsign))
	app.Post("/v1/company/esign/complete", cloud.Handle(s, completeEsign))
	app.Post("/v1/company/genesis", cloud.Handle(s, recordGenesis))
	app.Post("/v1/company/advance", cloud.Handle(s, advance))
	app.Post("/v1/company/skip", cloud.Handle(s, skip))
	app.Post("/v1/company/import/documents", cloud.Handle(s, importDocuments))
	app.Post("/v1/company/import/captable", cloud.Handle(s, importCapTable))
	app.Post("/v1/company/fundraise/round", cloud.Handle(s, fundraiseRound))
	app.Post("/v1/company/fundraise/deck", cloud.Handle(s, fundraiseDeck))
	app.Post("/v1/company/fundraise/safe", cloud.Handle(s, fundraiseSafe))
}

// Shutdown closes the store. Idempotent. Matches cloud.ShutdownFunc so Wire can
// reference it directly (like captable/dataroom/sign).
func Shutdown(context.Context) error {
	if mounted == nil || mounted.State.store == nil {
		return nil
	}
	err := mounted.State.store.Close()
	mounted = nil
	return err
}

// ---- shared helpers ----

func tenant(c *zip.Ctx) (string, bool) { return principal.Org(c) }

// genID returns a prefixed, collision-resistant id (prefix + 128 random bits).
func genID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(b[:]), nil
}

// feeCents is the formation fee, overridable by ops via CLOUD_COMPANY_FEE_CENTS.
func feeCents() int64 {
	if v := strings.TrimSpace(os.Getenv("CLOUD_COMPANY_FEE_CENTS")); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	return formationFeeCents
}

// load resolves the org and loads its formation, or writes the right error.
func load(s *cloud.Service[state], c *zip.Ctx) (*Formation, string, error) {
	org, ok := tenant(c)
	if !ok {
		return nil, "", zip.ErrForbidden("X-Org-Id required")
	}
	f, err := s.State.store.Get(c.Context(), org)
	if errors.Is(err, errNotFound) {
		return nil, org, zip.ErrNotFound("no formation for this org — POST /v1/company to begin")
	}
	if err != nil {
		return nil, org, zip.Errorf(http.StatusInternalServerError, "load: %v", err)
	}
	return f, org, nil
}

func save(s *cloud.Service[state], c *zip.Ctx, f *Formation) error {
	f.UpdatedAt = time.Now().Unix()
	if err := s.State.store.Put(c.Context(), f); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "save: %v", err)
	}
	return nil
}

// requireStage returns a 409 if the formation is not at one of the allowed stages —
// so an action can only run at the step it belongs to.
func requireStage(f *Formation, allowed ...Stage) error {
	for _, s := range allowed {
		if f.Stage == s {
			return nil
		}
	}
	return zip.Errorf(http.StatusConflict, "action not available at stage %q", f.Stage)
}

func ok(c *zip.Ctx, f *Formation) error {
	return c.JSON(http.StatusOK, view(f))
}

// view renders a formation plus the machine's out-edges for the UI.
func view(f *Formation) map[string]any {
	next := NextStages(f)
	ns := make([]string, len(next))
	for i, s := range next {
		ns[i] = string(s)
	}
	return map[string]any{"formation": f, "nextStages": ns}
}

// ---- begin / get ----

func begin(s *cloud.Service[state], c *zip.Ctx) error {
	org, okOrg := tenant(c)
	if !okOrg {
		return zip.ErrForbidden("X-Org-Id required")
	}
	if existing, err := s.State.store.Get(c.Context(), org); err == nil {
		return c.JSON(http.StatusOK, view(existing)) // idempotent: one formation per org
	} else if !errors.Is(err, errNotFound) {
		return zip.Errorf(http.StatusInternalServerError, "load: %v", err)
	}
	var body struct {
		Structure           Structure    `json:"structure"`
		Jurisdiction        Jurisdiction `json:"jurisdiction"`
		Name                string       `json:"name"`
		AlreadyIncorporated bool         `json:"alreadyIncorporated"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	now := time.Now().Unix()
	f := &Formation{
		Org: org, Stage: StageStructure,
		Structure: body.Structure, Jurisdiction: body.Jurisdiction, Name: strings.TrimSpace(body.Name),
		AlreadyIncorporated: body.AlreadyIncorporated,
		CreatedAt:           now, UpdatedAt: now,
	}
	if err := save(s, c, f); err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, view(f))
}

func get(s *cloud.Service[state], c *zip.Ctx) error {
	f, _, err := load(s, c)
	if err != nil {
		return err
	}
	return ok(c, f)
}

// ---- structure / founders ----

func setStructure(s *cloud.Service[state], c *zip.Ctx) error {
	f, _, err := load(s, c)
	if err != nil {
		return err
	}
	if err := requireStage(f, StageStructure); err != nil {
		return err
	}
	var body struct {
		Structure    Structure    `json:"structure"`
		Jurisdiction Jurisdiction `json:"jurisdiction"`
		Name         string       `json:"name"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	if !validStructures[body.Structure] {
		return zip.ErrBadRequest("structure must be one of c-corp, llc, dao-llc")
	}
	if !validJurisdictions[body.Jurisdiction] {
		return zip.ErrBadRequest("jurisdiction must be DE or WY")
	}
	if strings.TrimSpace(body.Name) == "" {
		return zip.ErrBadRequest("name is required")
	}
	f.Structure, f.Jurisdiction, f.Name = body.Structure, body.Jurisdiction, strings.TrimSpace(body.Name)
	if err := save(s, c, f); err != nil {
		return err
	}
	return ok(c, f)
}

func setFounders(s *cloud.Service[state], c *zip.Ctx) error {
	f, _, err := load(s, c)
	if err != nil {
		return err
	}
	if err := requireStage(f, StageStructure, StageFounders); err != nil {
		return err
	}
	var body struct {
		Founders []Founder `json:"founders"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	if len(body.Founders) == 0 {
		return zip.ErrBadRequest("at least one founder is required")
	}
	founders := make([]Founder, 0, len(body.Founders))
	for _, fo := range body.Founders {
		if strings.TrimSpace(fo.Email) == "" || strings.TrimSpace(fo.Name) == "" {
			return zip.ErrBadRequest("each founder needs a name and email")
		}
		if fo.EquityBps < 0 || fo.EquityBps > 10000 {
			return zip.ErrBadRequest("equityBps must be between 0 and 10000")
		}
		founders = append(founders, Founder{
			Name: strings.TrimSpace(fo.Name), Email: strings.TrimSpace(fo.Email),
			EquityBps: fo.EquityBps, KYCStatus: KYCPending,
		})
	}
	f.Founders = founders
	if err := save(s, c, f); err != nil {
		return err
	}
	return ok(c, f)
}

// ---- KYC (idv seam) ----

func startKYC(s *cloud.Service[state], c *zip.Ctx) error {
	f, org, err := load(s, c)
	if err != nil {
		return err
	}
	if err := requireStage(f, StageFounders); err != nil {
		return err
	}
	if len(f.Founders) == 0 {
		return zip.ErrBadRequest("add founders before starting KYC")
	}
	sessions := make([]map[string]string, 0, len(f.Founders))
	for i := range f.Founders {
		ref, url, status, err := s.State.prov.kyc.Start(c.Context(), org, f.Founders[i])
		if err != nil {
			return zip.Errorf(http.StatusBadGateway, "kyc start for %s: %v", f.Founders[i].Email, err)
		}
		f.Founders[i].KYCRef = ref
		f.Founders[i].KYCStatus = status
		sessions = append(sessions, map[string]string{"email": f.Founders[i].Email, "ref": ref, "verifyUrl": url, "status": status})
	}
	if err := save(s, c, f); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"provider": s.State.prov.kyc.Name(), "sessions": sessions, "formation": f})
}

func kycCallback(s *cloud.Service[state], c *zip.Ctx) error {
	f, _, err := load(s, c)
	if err != nil {
		return err
	}
	var body struct {
		Email  string `json:"email"`
		Status string `json:"status"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	status := strings.ToLower(strings.TrimSpace(body.Status))
	if status != KYCVerified && status != KYCFailed && status != KYCPending {
		return zip.ErrBadRequest("status must be one of verified, failed, pending")
	}
	found := false
	for i := range f.Founders {
		if f.Founders[i].Email == strings.TrimSpace(body.Email) {
			f.Founders[i].KYCStatus = status
			found = true
		}
	}
	if !found {
		return zip.ErrNotFound("no founder with that email")
	}
	if err := save(s, c, f); err != nil {
		return err
	}
	return ok(c, f)
}

// ---- payment (the $999 gate) ----

func pay(s *cloud.Service[state], c *zip.Ctx) error {
	f, org, err := load(s, c)
	if err != nil {
		return err
	}
	if err := requireStage(f, StagePayment); err != nil {
		return err
	}
	if f.Paid {
		return ok(c, f) // idempotent — already paid
	}
	ref, err := s.State.prov.charge.Charge(c.Context(), org, feeCents(), "Hanzo Company formation fee")
	if err != nil {
		// Map the metering error to the canonical 402/503 billing contract.
		return cloud.DenyResource(c, err)
	}
	f.Paid, f.PaymentRef = true, ref
	if err := save(s, c, f); err != nil {
		return err
	}
	return ok(c, f)
}

// ---- documents (generate → data room + state filing) ----

func generateDocuments(s *cloud.Service[state], c *zip.Ctx) error {
	f, org, err := load(s, c)
	if err != nil {
		return err
	}
	if err := requireStage(f, StageDocuments); err != nil {
		return err
	}
	docs, err := renderFormationDocs(f)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "render docs: %v", err)
	}
	ids := make([]string, 0, len(docs))
	for _, d := range docs {
		id, err := s.State.prov.docs.Ingest(c.Context(), org, d.Name, d.ContentType, d.Data)
		if err != nil {
			return zip.Errorf(http.StatusBadGateway, "data room ingest %s: %v", d.Name, err)
		}
		ids = append(ids, id)
	}
	f.DocumentIDs = ids
	// Submit the state filing (honest stub records "manual" — no fake filing).
	filing, err := s.State.prov.filing.Submit(c.Context(), f)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "state filing: %v", err)
	}
	f.Filing = filing
	if err := save(s, c, f); err != nil {
		return err
	}
	return ok(c, f)
}

// ---- esign ----

func requestEsign(s *cloud.Service[state], c *zip.Ctx) error {
	f, org, err := load(s, c)
	if err != nil {
		return err
	}
	if err := requireStage(f, StageEsign); err != nil {
		return err
	}
	signers := make([]Signer, 0, len(f.Founders))
	for _, fo := range f.Founders {
		signers = append(signers, Signer{Name: fo.Name, Email: fo.Email})
	}
	ref, err := s.State.prov.esign.Request(c.Context(), org, f.DocumentIDs, signers)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "esign request: %v", err)
	}
	f.EsignRef = ref
	if err := save(s, c, f); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"provider": s.State.prov.esign.Name(), "esignRef": ref, "formation": f})
}

func completeEsign(s *cloud.Service[state], c *zip.Ctx) error {
	f, org, err := load(s, c)
	if err != nil {
		return err
	}
	if err := requireStage(f, StageEsign); err != nil {
		return err
	}
	if f.EsignRef == "" {
		return zip.ErrBadRequest("no esign request to complete")
	}
	// A real provider's webhook drives completion; the complete signal is idempotent.
	complete, err := s.State.prov.esign.Status(c.Context(), org, f.EsignRef)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "esign status: %v", err)
	}
	// Honor an explicit override for the manual/stub provider (webhook not wired).
	var body struct {
		Signed *bool `json:"signed"`
	}
	_ = decode(c, &body)
	if body.Signed != nil {
		complete = *body.Signed
	}
	f.Signed = complete
	if err := save(s, c, f); err != nil {
		return err
	}
	return ok(c, f)
}

// ---- genesis (seed cap table + anchor on-chain) ----

func recordGenesis(s *cloud.Service[state], c *zip.Ctx) error {
	f, org, err := load(s, c)
	if err != nil {
		return err
	}
	if err := requireStage(f, StageGenesis); err != nil {
		return err
	}
	// Seed the canonical cap table with the founding allocation (stakeholders +
	// common class + issued shares), then anchor the deterministic root on-chain.
	if err := s.State.prov.captable.SeedFounders(c.Context(), org, f.Name, f.Founders); err != nil {
		return zip.Errorf(http.StatusBadGateway, "seed cap table: %v", err)
	}
	g, err := s.State.prov.anchor.Anchor(c.Context(), f)
	if g != nil {
		f.Genesis = g
		_ = save(s, c, f) // persist the computed root even if the on-chain submit failed
	}
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "anchor genesis: %v", err)
	}
	return ok(c, f)
}

// ---- the transition door ----

func advance(s *cloud.Service[state], c *zip.Ctx) error {
	f, _, err := load(s, c)
	if err != nil {
		return err
	}
	var body struct {
		To Stage `json:"to"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	if body.To == "" {
		return zip.ErrBadRequest("to is required")
	}
	if aerr := Advance(f, body.To); aerr != nil {
		if errors.Is(aerr, errIllegalTransition) {
			return zip.Errorf(http.StatusConflict, "%v", aerr)
		}
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", aerr)
	}
	// Reaching the terminal company stage upgrades the org (records the
	// incorporation on the canonical cap table). It must succeed before the
	// transition is persisted.
	if f.Stage == StageCompany {
		if uerr := s.State.prov.upgrade.MarkCompany(c.Context(), f); uerr != nil {
			return zip.Errorf(http.StatusBadGateway, "upgrade org to company: %v", uerr)
		}
	}
	if err := save(s, c, f); err != nil {
		return err
	}
	return ok(c, f)
}

// ---- skip / import ----

func skip(s *cloud.Service[state], c *zip.Ctx) error {
	f, _, err := load(s, c)
	if err != nil {
		return err
	}
	if err := requireStage(f, StageStructure); err != nil {
		return err
	}
	f.AlreadyIncorporated = true
	if aerr := Advance(f, StageImport); aerr != nil {
		return zip.Errorf(http.StatusUnprocessableEntity, "%v", aerr)
	}
	if err := save(s, c, f); err != nil {
		return err
	}
	return ok(c, f)
}

func importDocuments(s *cloud.Service[state], c *zip.Ctx) error {
	f, org, err := load(s, c)
	if err != nil {
		return err
	}
	if err := requireStage(f, StageImport); err != nil {
		return err
	}
	var body struct {
		FolderID string `json:"folderId"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.FolderID) == "" {
		return zip.ErrBadRequest("folderId (a Google Drive folder id) is required")
	}
	files, err := s.State.prov.google.ListFolder(c.Context(), org, body.FolderID)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "list drive folder: %v", err)
	}
	ingested := make([]string, 0, len(files))
	for _, file := range files {
		if strings.HasPrefix(file.MimeType, "application/vnd.google-apps.folder") {
			continue // skip sub-folders in this shallow import
		}
		data, ct, err := s.State.prov.google.Download(c.Context(), org, file)
		if err != nil {
			return zip.Errorf(http.StatusBadGateway, "download %s: %v", file.Name, err)
		}
		id, err := s.State.prov.docs.Ingest(c.Context(), org, file.Name, ct, data)
		if err != nil {
			return zip.Errorf(http.StatusBadGateway, "data room ingest %s: %v", file.Name, err)
		}
		ingested = append(ingested, id)
	}
	f.ImportedDocs = append(f.ImportedDocs, ingested...)
	if len(f.ImportedDocs) > 0 {
		f.Imported = true
	}
	if err := save(s, c, f); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"ingested": len(ingested), "formation": f})
}

func importCapTable(s *cloud.Service[state], c *zip.Ctx) error {
	f, org, err := load(s, c)
	if err != nil {
		return err
	}
	if err := requireStage(f, StageImport); err != nil {
		return err
	}
	var body struct {
		SpreadsheetID string `json:"spreadsheetId"`
		Range         string `json:"range"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.SpreadsheetID) == "" {
		return zip.ErrBadRequest("spreadsheetId (a Google Sheets id) is required")
	}
	rows, err := s.State.prov.google.SheetValues(c.Context(), org, body.SpreadsheetID, body.Range)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "read sheet: %v", err)
	}
	holders, err := parseCapTableRows(rows)
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	inserted, err := s.State.prov.captable.AddStakeholders(c.Context(), org, holders)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "captable import: %v", err)
	}
	f.CapTableImported = true
	if err := save(s, c, f); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"stakeholdersImported": inserted, "rows": len(rows), "formation": f})
}

// ---- fundraising ----

func fundraiseRound(s *cloud.Service[state], c *zip.Ctx) error {
	f, org, err := load(s, c)
	if err != nil {
		return err
	}
	if f.Stage != StageCompany {
		return zip.Errorf(http.StatusConflict, "fundraising is available after incorporation (stage company)")
	}
	var body RoundInput
	if err := decode(c, &body); err != nil {
		return err
	}
	if strings.TrimSpace(body.Name) == "" {
		return zip.ErrBadRequest("round name is required")
	}
	if body.RoundType == "" {
		body.RoundType = "PRICED"
	}
	id, err := s.State.prov.captable.RecordRound(c.Context(), org, body)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "record round: %v", err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"roundId": id})
}

func fundraiseDeck(s *cloud.Service[state], c *zip.Ctx) error {
	f, org, err := load(s, c)
	if err != nil {
		return err
	}
	if f.Stage != StageCompany {
		return zip.Errorf(http.StatusConflict, "fundraising is available after incorporation (stage company)")
	}
	raw := c.Fiber().Body()
	if len(raw) == 0 {
		return zip.ErrBadRequest("send the deck bytes as the request body")
	}
	name := c.Query("name")
	if name == "" {
		name = "pitch-deck"
	}
	ct := c.Header("Content-Type")
	id, err := s.State.prov.docs.Ingest(c.Context(), org, name, ct, raw)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "data room ingest: %v", err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"documentId": id})
}

func fundraiseSafe(s *cloud.Service[state], c *zip.Ctx) error {
	f, org, err := load(s, c)
	if err != nil {
		return err
	}
	if f.Stage != StageCompany {
		return zip.Errorf(http.StatusConflict, "fundraising is available after incorporation (stage company)")
	}
	var body struct {
		DocumentIDs []string `json:"documentIds"`
		Signers     []Signer `json:"signers"`
	}
	if err := decode(c, &body); err != nil {
		return err
	}
	if len(body.DocumentIDs) == 0 || len(body.Signers) == 0 {
		return zip.ErrBadRequest("documentIds and signers are required")
	}
	ref, err := s.State.prov.esign.Request(c.Context(), org, body.DocumentIDs, body.Signers)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "esign request: %v", err)
	}
	return c.JSON(http.StatusCreated, map[string]any{"esignRef": ref, "provider": s.State.prov.esign.Name()})
}

// decode reads and size-limits the JSON request body into v. An empty body decodes
// to the zero value (so an optional-body POST is fine).
func decode(c *zip.Ctx, v any) error {
	raw := c.Fiber().Body()
	if len(raw) == 0 {
		return nil
	}
	if len(raw) > maxBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request body too large")
	}
	return c.Bind(v)
}
