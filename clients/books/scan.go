package books

// scan.go — the SCANNER: turn an uploaded receipt/invoice into a balanced expense voucher
// under human review. The flow is deliberately split so the AI never writes the ledger:
//
//	POST /v1/books/scan       upload bytes → text → the AI EXTRACTS structured fields →
//	                          resolve the vendor's category (vendors/rules) → return a
//	                          DRAFT with a PROPOSED balanced voucher. NOTHING is posted.
//	POST /v1/books/scan/book  the reviewed (possibly edited) voucher → Post() once,
//	                          idempotent by (scan, file-hash). This is the ONLY write.
//
// THE AI PROPOSES, A HUMAN (OR A CONFIDENT RULE) CONFIRMS, POST() WRITES. The model only
// ever produces a JSON extraction of what the document says; the balanced voucher is built
// deterministically in Go (Dr category expense + Dr sales-tax / Cr vendor payable), and it
// reaches the immutable ledger only through the same post() choke point every other source
// uses — merge → toggle → the Σdebit==Σcredit invariant → idempotent by source id. So a
// mis-read scan can propose a wrong DRAFT, but it can never silently move money.
//
// MONEY is exact int64 cents end to end (the AI returns integer cents; a fractional-cent
// value never enters the ledger). Every read/write is scoped to the caller's OWN org.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// scanSourceKind is the voucher source_kind for a booked scan — the idempotency namespace
// (paired with the file hash as source_id) and the "source" a transactions row reports.
const scanSourceKind = "scan"

// maxScanUpload bounds a single receipt/invoice upload; a receipt is small, and the PDF
// path already caps pages/lines, so this only guards the plain-text branch.
const maxScanUpload = 8 << 20

// scannerRoutes registers the scanner + inbox + vendors/rules + transactions surface on the
// books app (called from routes()).
func scannerRoutes(app *zip.App, s *cloud.Service[*state]) {
	app.Post("/v1/books/scan", cloud.Handle(s, scanHandler))
	app.Post("/v1/books/scan/book", cloud.Handle(s, scanBookHandler))
	app.Post("/v1/books/inbox", cloud.Handle(s, inboxUploadHandler))
	app.Get("/v1/books/inbox", cloud.Handle(s, inboxListHandler))
	app.Get("/v1/books/vendors", cloud.Handle(s, vendorsListHandler))
	app.Post("/v1/books/vendors", cloud.Handle(s, vendorUpsertHandler))
	app.Get("/v1/books/rules", cloud.Handle(s, rulesListHandler))
	app.Post("/v1/books/rules", cloud.Handle(s, ruleUpsertHandler))
	app.Get("/v1/books/transactions", cloud.Handle(s, transactionsHandler))
}

// LineItem is one line of a scanned document — a description and its amount in exact cents.
type LineItem struct {
	Description string `json:"description"`
	AmountCents int64  `json:"amountCents"`
}

// Extracted is the structured shape the AI extracts from a receipt/invoice. Amounts are
// exact int64 cents (the AI is instructed to return integer cents, never a decimal), so no
// float rounding can enter downstream. Category is the AI's proposed slug — a HINT that
// vendor/rule resolution overrides when it knows better.
type Extracted struct {
	Merchant   string     `json:"merchant"`
	IssuedAt   string     `json:"issuedAt"` // YYYY-MM-DD
	TotalCents int64      `json:"totalCents"`
	TaxCents   int64      `json:"taxCents"`
	Currency   string     `json:"currency"`
	Category   string     `json:"category"` // proposed slug (software|cloud|office|…)
	LineItems  []LineItem `json:"lineItems,omitempty"`
	Note       string     `json:"note,omitempty"`
}

// ScanDraft is the POST /v1/books/scan response: the extracted fields plus a PROPOSED
// balanced voucher that has NOT been posted. Confidence is "auto" when a vendor/rule
// resolved the category, else "low" — in which case Questions carries a clarifying prompt
// and the frontend should confirm the category (and can persist a rule) before booking.
type ScanDraft struct {
	ScanID     string     `json:"scanId"` // file hash — the (scan, id) idempotency key
	Extracted  Extracted  `json:"extracted"`
	Vendor     string     `json:"vendor"`
	Category   string     `json:"category"`   // resolved COA expense account number
	Confidence string     `json:"confidence"` // auto | low
	Voucher    Voucher    `json:"voucher"`    // PROPOSED — not yet posted
	Balanced   bool       `json:"balanced"`   // Σdebit == Σcredit (always true when built)
	Questions  []Question `json:"questions,omitempty"`
}

// scanHandler answers POST /v1/books/scan: it reads the uploaded bytes, extracts text (PDF
// or plain), asks the AI for a structured extraction, resolves the vendor's category, and
// returns a DRAFT with a proposed balanced voucher. It writes the inbox row (unsorted →
// draft) but posts NOTHING — booking is the explicit second step.
func scanHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to scan a receipt")
	}
	if s.State.ai == nil {
		return zip.Errorf(http.StatusNotImplemented, "scanner AI not available")
	}
	body := c.Body()
	if len(body) == 0 {
		return zip.ErrBadRequest("empty upload")
	}
	if len(body) > maxScanUpload {
		return zip.ErrBadRequest("upload too large")
	}
	text, err := scanText(body)
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	ex, err := scanExtract(c.Context(), s.State.ai, s.State.model, org, principal.HomeOrg(c), text)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "scan extraction failed: %s", err.Error())
	}
	scanID := scanIDOf(body)
	draft, err := st.buildDraft(c.Context(), ex, scanID)
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	if err := st.upsertInboxDraft(c.Context(), scanID, strings.TrimSpace(c.Query("filename")), draft); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "inbox write failed")
	}
	return booksJSON(c, draft)
}

// BookRequest is the POST /v1/books/scan/book body: the reviewed voucher (the frontend may
// have edited any field) plus the ScanID that keys idempotency. SourceKind/SourceID are
// FORCED server-side to (scan, ScanID) so a client can never post a scan under another
// source's key or double-book by editing the id.
type BookRequest struct {
	ScanID  string  `json:"scanId"`
	Voucher Voucher `json:"voucher"`
}

// BookResponse reports the outcome of a scan book: posted=false means the same scan already
// booked (idempotent no-op), never a second voucher.
type BookResponse struct {
	ScanID string `json:"scanId"`
	Posted bool   `json:"posted"`
}

// scanBookHandler answers POST /v1/books/scan/book: it posts the reviewed voucher through
// the ONE post() choke point, idempotent by (scan, scanId). A re-book of the same scan
// returns posted=false and writes nothing. This is the scanner's only write.
func scanBookHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to book a scan")
	}
	var in BookRequest
	if err := c.Bind(&in); err != nil {
		return err
	}
	scanID := strings.TrimSpace(in.ScanID)
	if scanID == "" {
		return zip.ErrBadRequest("scanId is required")
	}
	if len(in.Voucher.Legs) == 0 {
		return zip.ErrBadRequest("voucher has no legs")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	v := in.Voucher
	v.SourceKind, v.SourceID = scanSourceKind, scanID // FORCE the idempotency key
	if v.PostingAt == "" {
		v.PostingAt = time.Now().UTC().Format(time.RFC3339)
	}
	if v.Description == "" {
		v.Description = "Scanned bill " + scanID
	}
	posted, err := st.post(c.Context(), v, RoundOffAllowance)
	if err != nil {
		return zip.ErrBadRequest(err.Error()) // an unbalanced reviewed voucher is a client error
	}
	if err := st.markInboxBooked(c.Context(), scanID); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "inbox update failed")
	}
	s.State.syncDurable(org, sandboxQuery(c), boolToInt(posted))
	return booksJSON(c, BookResponse{ScanID: scanID, Posted: posted})
}

// scanText extracts the document's text: a PDF is rendered via pdfLines (pure Go, the same
// extractor bank statements use); anything else is treated as plain UTF-8 text.
func scanText(body []byte) (string, error) {
	if looksPDF(body) {
		lines, err := pdfLines(body)
		if err != nil {
			return "", err
		}
		return strings.Join(lines, "\n"), nil
	}
	t := strings.TrimSpace(string(body))
	if t == "" {
		return "", fmt.Errorf("no extractable text")
	}
	return t, nil
}

// scanIDOf is the deterministic idempotency key for a scanned document: the sha256 of the
// uploaded bytes. The same file re-uploaded yields the same id, so scan/book is idempotent
// and the inbox de-duplicates on it.
func scanIDOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// scanPrompt instructs the model to return ONLY strict JSON — the exact Extracted shape,
// amounts as integer CENTS (never decimals), so the parse is exact and no prose leaks in.
func scanPrompt(text string) string {
	if len(text) > 12000 {
		text = text[:12000]
	}
	return `You are a precise bookkeeping extractor. Read the receipt/invoice text below and return ONLY a single JSON object, no prose, no markdown fences, with EXACTLY these fields:
{"merchant": string, "issuedAt": "YYYY-MM-DD", "totalCents": integer, "taxCents": integer, "currency": string, "category": one of ["software","cloud","office","travel","meals","marketing","services","fees"], "lineItems": [{"description": string, "amountCents": integer}], "note": string}
Rules: all amounts are INTEGER CENTS (e.g. $12.34 => 1234), never decimals. taxCents is the sales tax portion of totalCents (0 if none). Pick the single best category. If a field is unknown use "" or 0.

RECEIPT TEXT:
` + text
}

// scanExtract runs ONE grounded completion that extracts the document's fields, billed to
// the caller's HOME org and scoped to the caller's own org, then parses the model's JSON
// robustly. The model only ever extracts — it proposes numbers a human reviews and post()
// enforces; it never writes the ledger.
func scanExtract(ctx context.Context, ai cloud.AIClient, model, org, billingOrg, text string) (Extracted, error) {
	res, err := ai.ChatCompletion(ctx, &cloud.ChatRequest{
		Model:      model,
		Prompt:     scanPrompt(text),
		Org:        org,
		BillingOrg: billingOrg,
	})
	if err != nil {
		return Extracted{}, err
	}
	if res == nil || strings.TrimSpace(res.Content) == "" {
		return Extracted{}, fmt.Errorf("empty extraction")
	}
	return parseExtraction(res.Content)
}

// parseExtraction robustly pulls the JSON object out of the model's text — tolerating
// markdown fences or stray prose around it — and unmarshals + validates it. It extracts the
// span from the first '{' to the last '}', the object the prompt asked for.
func parseExtraction(raw string) (Extracted, error) {
	start := strings.IndexByte(raw, '{')
	end := strings.LastIndexByte(raw, '}')
	if start < 0 || end <= start {
		return Extracted{}, fmt.Errorf("no JSON object in extraction")
	}
	var ex Extracted
	if err := json.Unmarshal([]byte(raw[start:end+1]), &ex); err != nil {
		return Extracted{}, fmt.Errorf("extraction JSON: %w", err)
	}
	ex.Merchant = strings.TrimSpace(ex.Merchant)
	ex.Currency = strings.ToLower(strings.TrimSpace(ex.Currency))
	if ex.Currency == "" {
		ex.Currency = "usd"
	}
	if err := validateExtracted(ex); err != nil {
		return Extracted{}, err
	}
	return ex, nil
}

// validateExtracted enforces the money invariants a booked bill depends on: a positive
// total, a non-negative tax that does not exceed the total, and no negative line item.
func validateExtracted(ex Extracted) error {
	if ex.Merchant == "" {
		return fmt.Errorf("extraction missing merchant")
	}
	if ex.TotalCents <= 0 {
		return fmt.Errorf("extraction has non-positive total")
	}
	if ex.TaxCents < 0 || ex.TaxCents > ex.TotalCents {
		return fmt.Errorf("extraction tax out of range")
	}
	for _, li := range ex.LineItems {
		if li.AmountCents < 0 {
			return fmt.Errorf("extraction has negative line item")
		}
	}
	return nil
}

// buildDraft turns an extraction into a PROPOSED balanced voucher under review. It resolves
// the vendor's category (vendors/rules); a known vendor yields confidence "auto", an unknown
// one falls back to the AI's proposed category at confidence "low" with a clarifying
// question so a human confirms and can persist a rule. The voucher is:
//
//	Dr <category expense>  (total − tax)
//	Dr Sales-tax payable   (tax)
//	Cr Vendor payable      (total)
//
// which balances by construction (net + tax == total). It is NOT posted here.
func (st *store) buildDraft(ctx context.Context, ex Extracted, scanID string) (ScanDraft, error) {
	postingAt, err := scanPostingAt(ex.IssuedAt)
	if err != nil {
		return ScanDraft{}, err
	}
	vendor, account, confidence, err := st.classifyMerchant(ctx, ex.Merchant)
	if err != nil {
		return ScanDraft{}, err
	}
	if confidence != "auto" {
		account = categoryAccount(ex.Category) // AI hint, default 5900 Uncategorized
		vendor = ex.Merchant
		confidence = "low"
	}

	net := ex.TotalCents - ex.TaxCents
	legs := make([]Leg, 0, 3)
	if net != 0 {
		legs = append(legs, Leg{Account: account, Debit: net})
	}
	if ex.TaxCents != 0 {
		legs = append(legs, Leg{Account: SalesTaxPayable, Debit: ex.TaxCents})
	}
	legs = append(legs, Leg{Account: VendorPayable, Credit: ex.TotalCents})

	v := Voucher{
		SourceKind:  scanSourceKind,
		SourceID:    scanID,
		PostingAt:   postingAt,
		Description: vendor,
		Legs:        legs,
	}
	// Prove it balances now (the same check post() will enforce) so the draft reports it.
	_, balErr := processGLMap(v.Legs, RoundOffAllowance, RoundOff)

	draft := ScanDraft{
		ScanID:     scanID,
		Extracted:  ex,
		Vendor:     vendor,
		Category:   account,
		Confidence: confidence,
		Voucher:    v,
		Balanced:   balErr == nil,
	}
	if confidence == "low" {
		draft.Questions = []Question{{
			ID:       scanID,
			Kind:     "scan-category",
			Text:     "Which category is this " + vendor + " bill? Set a vendor or rule so future bills self-classify.",
			Amount:   dollars(ex.TotalCents),
			Account:  account,
			PostedAt: postingAt,
		}}
	}
	return draft, nil
}

// scanPostingAt converts a YYYY-MM-DD issue date into an RFC3339 UTC midnight posting time
// (ledger-comparable). An empty/invalid date falls back to today — a scan is still bookable;
// the reviewer can correct the date before booking.
func scanPostingAt(issued string) (string, error) {
	issued = strings.TrimSpace(issued)
	if issued == "" {
		return time.Now().UTC().Format(time.RFC3339), nil
	}
	t, err := time.Parse("2006-01-02", issued)
	if err != nil {
		return time.Now().UTC().Format(time.RFC3339), nil
	}
	return t.UTC().Format(time.RFC3339), nil
}
