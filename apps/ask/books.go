package ask

// books.go — the FIRST contributor: BOOKS (financial). It grounds MRR/ARR/revenue/burn/runway/
// margin/cash/deferred-revenue/P&L/balance questions by replaying the books domain's OWN
// grounded read — GET /v1/books/metrics — in-process under the caller's own credentials, then
// surfacing the REAL figures it returns. It NEVER recomputes: books stays the single source of
// the numbers AND their formatting (the endpoint returns formatUSD-formatted figures). Per-tenant
// isolation is inherited from the replay carrying the caller's own X-Org-Id — a books read can
// only ever return the caller's own org's ledger.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/hanzoai/cloud"
	fiber "github.com/zap-proto/fiber/v3"
)

// booksMetricsPath is the books domain's grounded read the contributor replays. It is the ONE
// endpoint that returns the deterministic metric snapshot as formatted figures.
const booksMetricsPath = "/v1/books/metrics"

// maxMetricsResponse bounds the in-process read so a broken upstream cannot balloon memory.
const maxMetricsResponse = 1 << 20

// booksContributor grounds financial questions against the books domain. It holds the app it
// replays against (the SAME binary; the read flows the normal middleware chain), exactly as
// agent.go's aiCompleter holds the app to replay /v1/chat/completions.
type booksContributor struct{ app cloud.Router }

func newBooksContributor(app cloud.Router) *booksContributor { return &booksContributor{app: app} }

func (booksContributor) Name() string { return "books" }

// CanAnswer is the books classifier: does the question ask about money the ledger knows? Keyword
// match over the founder-vocabulary the metrics engine grounds. First-match in the registry, so
// a books question routes here deterministically. An LLM classifier can replace this body later
// without touching the router or the seam.
func (booksContributor) CanAnswer(question string) bool {
	l := strings.ToLower(question)
	for _, kw := range booksKeywords {
		if strings.Contains(l, kw) {
			return true
		}
	}
	return false
}

// booksKeywords is the financial vocabulary that routes a question to the books ledger. It
// mirrors the books intent router's keywords so /v1/ask grounds exactly what /v1/books/ask does.
var booksKeywords = []string{
	"mrr", "arr", "recurring", "subscription", "annualized",
	"revenue", "sales", "top line", "income", "how much did we make", "how much money",
	"burn", "spend", "spending", "expense", "expenses", "opex", "costs", "cost of",
	"runway", "how long", "cash last", "out of money", "out of cash",
	"margin", "profitab", "profit", "net income", "bottom line", "earnings", "break even", "break-even",
	"cash", "bank", "in the bank", "balance", "cogs",
	"deferred", "wallet", "prepaid", "liabilit", "owe",
	"p&l", "pnl", "p and l", "financ",
}

// Gather replays GET /v1/books/metrics in-process under the caller's own credentials and returns
// the REAL figures it read, with the domain read that backed them. The figures are books' own —
// this contributor formats nothing and invents nothing; an empty ledger yields honest zero
// figures. The caller's creds carry the caller's org, so the read is scoped to that org and no
// other — tenant isolation is inherited, never re-implemented here.
func (b booksContributor) Gather(ctx context.Context, cred map[string]string) ([]Fact, []string, error) {
	req := httptest.NewRequest(http.MethodGet, booksMetricsPath, nil).WithContext(ctx)
	for k, v := range cred {
		req.Header.Set(k, v)
	}
	resp, err := b.app.Fiber().Test(req, fiber.TestConfig{Timeout: 0})
	if err != nil {
		return nil, nil, fmt.Errorf("books metrics replay: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, nil, fmt.Errorf("books metrics status %d", resp.StatusCode)
	}
	var out struct {
		Figures []Fact `json:"figures"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxMetricsResponse)).Decode(&out); err != nil {
		return nil, nil, fmt.Errorf("books metrics decode: %w", err)
	}
	return out.Figures, []string{"books/metrics"}, nil
}
