package books

// inbox.go — the per-org INBOX: a queue of uploaded-but-unbooked documents and the fixed
// expense-category vocabulary the scanner maps into. A document's lifecycle is
// unsorted → draft → booked:
//
//	unsorted  POST /v1/books/inbox stored the raw bytes (hash + filename), nothing extracted
//	draft     POST /v1/books/scan  extracted its fields; the extracted summary is attached
//	booked    POST /v1/books/scan/book posted a reviewed voucher for it
//
// The queue de-duplicates on the file hash (UNIQUE), so re-uploading the same document is an
// idempotent no-op — one row per document. GET /v1/books/inbox lists the open work
// (unsorted + draft) with each item's extracted summary and confidence.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// inbox item statuses — the document lifecycle.
const (
	inboxUnsorted = "unsorted" // uploaded, not yet extracted
	inboxDraft    = "draft"    // extracted into a reviewable draft
	inboxBooked   = "booked"   // a reviewed voucher was posted
)

// expenseCategory maps a human category slug to its COA expense account. It is the fixed
// vocabulary the scanner and the vendor/rule tables share, so a category is always a real
// account, never free text.
type expenseCategory struct {
	Slug    string `json:"slug"`
	Account string `json:"account"`
	Label   string `json:"label"`
}

// expenseCategories is the closed set of scanner categories. Order is presentation order.
var expenseCategories = []expenseCategory{
	{"software", SoftwareExpense, "Software & SaaS"},
	{"cloud", CloudCOGS, "Cloud / GPU"},
	{"office", OfficeExpense, "Office & supplies"},
	{"travel", TravelExpense, "Travel & transport"},
	{"meals", MealsExpense, "Meals & entertainment"},
	{"marketing", MarketingExpense, "Marketing & advertising"},
	{"services", ServicesExpense, "Professional services"},
	{"fees", ProcessorFees, "Processor fees"},
}

// categoryAccount resolves a category identifier — either a slug ("software") or an account
// number already ("5300") — to its COA expense account, defaulting to 5900 Uncategorized for
// anything unrecognized (never a guessed real category).
func categoryAccount(id string) string {
	id = strings.ToLower(strings.TrimSpace(id))
	if id == "" {
		return UncategorizedExpense
	}
	for _, c := range expenseCategories {
		if id == c.Slug || id == c.Account {
			return c.Account
		}
	}
	// An account number that is a seeded expense is accepted as-is (a rule may target a
	// COGS/fee account directly); anything else falls to Uncategorized.
	if a, ok := accountByNumber[id]; ok && a.Type == Expense {
		return id
	}
	return UncategorizedExpense
}

// InboxItem is one queued document as GET /v1/books/inbox surfaces it: its hash id,
// filename, status, and — once scanned — the extracted summary and its confidence.
type InboxItem struct {
	ID         string     `json:"id"` // the file hash (== a scan's ScanID)
	Filename   string     `json:"filename,omitempty"`
	Status     string     `json:"status"`
	CreatedAt  string     `json:"createdAt"`
	Extracted  *Extracted `json:"extracted,omitempty"`
	Vendor     string     `json:"vendor,omitempty"`
	Category   string     `json:"category,omitempty"`
	Confidence string     `json:"confidence,omitempty"`
}

// inboxSummary is the JSON blob persisted in books_inbox.extracted: enough of a scan draft
// to render the queue without re-scanning.
type inboxSummary struct {
	Extracted  Extracted `json:"extracted"`
	Vendor     string    `json:"vendor"`
	Category   string    `json:"category"`
	Confidence string    `json:"confidence"`
}

// inboxUploadHandler answers POST /v1/books/inbox: it stores the uploaded bytes' hash +
// filename as an 'unsorted' item, idempotently by hash (a re-upload of the same file returns
// the existing item, never a duplicate row).
func inboxUploadHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to upload a document")
	}
	body := c.Body()
	if len(body) == 0 {
		return zip.ErrBadRequest("empty upload")
	}
	if len(body) > maxScanUpload {
		return zip.ErrBadRequest("upload too large")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	id := scanIDOf(body)
	if err := st.insertInboxUnsorted(c.Context(), id, strings.TrimSpace(c.Query("filename"))); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "inbox write failed")
	}
	item, err := st.inboxItem(c.Context(), id)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "inbox read failed")
	}
	return booksJSON(c, item)
}

// inboxListHandler answers GET /v1/books/inbox: the open queue (unsorted + draft), newest
// first, each with its extracted summary and confidence when scanned. Booked items drop out.
func inboxListHandler(s *cloud.Service[*state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrUnauthorized("sign in to view the inbox")
	}
	st, err := s.State.storeFor(org, sandboxQuery(c))
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	items, err := st.listInboxOpen(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "inbox read failed")
	}
	return booksJSON(c, map[string]any{"items": items})
}

// ── store methods ──

// insertInboxUnsorted records a newly uploaded document as 'unsorted', idempotent by hash.
func (s *store) insertInboxUnsorted(ctx context.Context, hash, filename string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO books_inbox (filename, hash, status, extracted, created_at)
		 VALUES (?,?,?,'',?)
		 ON CONFLICT(hash) DO NOTHING`,
		filename, hash, inboxUnsorted, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("books insertInboxUnsorted: %w", err)
	}
	return nil
}

// upsertInboxDraft records (or advances to) a 'draft' item carrying the scan's extracted
// summary. If the document was never uploaded to the inbox first, this creates the row; if
// it was 'unsorted', it moves it to 'draft'. A booked item is left untouched.
func (s *store) upsertInboxDraft(ctx context.Context, hash, filename string, draft ScanDraft) error {
	blob, err := json.Marshal(inboxSummary{
		Extracted:  draft.Extracted,
		Vendor:     draft.Vendor,
		Category:   draft.Category,
		Confidence: draft.Confidence,
	})
	if err != nil {
		return fmt.Errorf("books upsertInboxDraft marshal: %w", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO books_inbox (filename, hash, status, extracted, created_at)
		 VALUES (?,?,?,?,?)
		 ON CONFLICT(hash) DO UPDATE SET
		   extracted = excluded.extracted,
		   filename  = CASE WHEN books_inbox.filename = '' THEN excluded.filename ELSE books_inbox.filename END,
		   status    = CASE WHEN books_inbox.status = ? THEN books_inbox.status ELSE ? END`,
		filename, hash, inboxDraft, string(blob), time.Now().UTC().Format(time.RFC3339),
		inboxBooked, inboxDraft)
	if err != nil {
		return fmt.Errorf("books upsertInboxDraft: %w", err)
	}
	return nil
}

// markInboxBooked moves an item to 'booked' after its voucher posts. It is a no-op when no
// inbox row exists (a scan booked without ever touching the inbox is still valid).
func (s *store) markInboxBooked(ctx context.Context, hash string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE books_inbox SET status = ? WHERE hash = ?`, inboxBooked, hash)
	if err != nil {
		return fmt.Errorf("books markInboxBooked: %w", err)
	}
	return nil
}

// inboxItem reads one item by its hash id.
func (s *store) inboxItem(ctx context.Context, hash string) (InboxItem, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT hash, filename, status, extracted, created_at FROM books_inbox WHERE hash = ?`, hash)
	return scanInboxRow(row.Scan)
}

// listInboxOpen returns the open queue (unsorted + draft), newest first.
func (s *store) listInboxOpen(ctx context.Context) ([]InboxItem, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT hash, filename, status, extracted, created_at
		 FROM books_inbox WHERE status IN (?, ?) ORDER BY id DESC`, inboxUnsorted, inboxDraft)
	if err != nil {
		return nil, fmt.Errorf("books listInboxOpen: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []InboxItem{}
	for rows.Next() {
		item, err := scanInboxRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

// scanInboxRow decodes one books_inbox row (via a Scan func so it serves both QueryRow and
// Rows), unpacking the extracted summary when present.
func scanInboxRow(scan func(...any) error) (InboxItem, error) {
	var it InboxItem
	var extracted string
	if err := scan(&it.ID, &it.Filename, &it.Status, &extracted, &it.CreatedAt); err != nil {
		return InboxItem{}, err
	}
	if strings.TrimSpace(extracted) != "" {
		var sum inboxSummary
		if err := json.Unmarshal([]byte(extracted), &sum); err == nil {
			ex := sum.Extracted
			it.Extracted = &ex
			it.Vendor = sum.Vendor
			it.Category = sum.Category
			it.Confidence = sum.Confidence
		}
	}
	return it, nil
}
