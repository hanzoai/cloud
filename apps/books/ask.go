package books

// ask.go — the AI ASK brain: POST /v1/books/ask. A founder asks a plain-language question
// ("what's my MRR?", "how long is my runway?") and gets an answer GROUNDED in the real
// numbers computed from their own books — never a hallucinated figure. The flow is:
//
//	question → intent router (deterministic) → the REAL metric(s) from metrics.go
//	         → figures + a templated answer + sharp followups + the report sources
//	         → OPTIONAL LLM narration that rephrases the templated answer WITHOUT
//	           touching a number (the ONE model seam; degrades to the template).
//
// THE FIGURES ARE ALWAYS REAL. The intent router maps the question to metric(s) and reads
// them out of the deterministic engine; the LLM only ever rewrites prose. So whether the AI
// plane is wired or down, the numbers are identical — the books, not the model, are the
// source of truth. The brain is strictly READ-ONLY: it computes over the ledger and never
// calls Post(), so an Ask can restate the books but never move them.

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/zap-proto/zip"
)

// AskRequest is the POST /v1/books/ask body.
type AskRequest struct {
	// Question is the plain-language question about the org's books, e.g. "what is my
	// MRR?". Longer than 2000 characters is truncated, never refused.
	Question string `json:"question"`
	// From is the RFC3339 start of the metric window. Empty means all time, treated as a
	// single reporting period (see monthsBetween).
	From string `json:"from,omitempty"`
	// To is the RFC3339 end of the metric window. Empty means up to now.
	To string `json:"to,omitempty"`
}

// Figure is one grounded number in the answer: a label, its formatted value, and the period
// it covers. This is the exact shape the Ask contract fixes.
type Figure struct {
	// Label names the metric, e.g. "MRR" or "Runway".
	Label string `json:"label"`
	// Value is the figure already formatted through books' own money formatter, so a
	// consumer never re-derives it.
	Value string `json:"value"`
	// Period is the window the figure covers, e.g. "2026-07" or "all-time".
	Period string `json:"period,omitempty"`
}

// AskResponse is the Ask contract: a natural-language answer grounded in Figures, with
// followup questions and the report Sources the figures were computed from.
type AskResponse struct {
	// Answer is one or two sentences answering the question, every number in it taken
	// from Figures.
	Answer string `json:"answer"`
	// Figures are the grounded numbers the answer states, each already formatted.
	Figures []Figure `json:"figures"`
	// Followups are sharper questions to ask next, chosen from the same intent.
	Followups []string `json:"followups"`
	// Sources name the books reports the figures were computed from — "pnl",
	// "balance-sheet", "trial-balance".
	Sources []string `json:"sources"`
}

const maxQuestion = 2000

// AskBooks answers a plain-language question about the caller's own books — "what is my
// MRR?", "how long is my runway?" — with figures taken from their ledger, never a guessed
// number. A deterministic keyword router picks the intent and reads the real metrics, and
// those figures, followups and report sources are computed BEFORE any model call and are
// never altered by one: the optional narration seam only rephrases the sentence, and it
// degrades silently to the templated answer when no AI plane is wired. It is strictly
// read-only — it restates the books, it never posts to them.
//
// Example: {"question": "how long is my runway?"}
func (o booksOps) ask(ctx context.Context, in *AskRequest) (*AskResponse, error) {
	// Tenant first, the order this route has always refused in: an anonymous caller is
	// 401 whatever it asks, and never learns from a 400 which fields exist.
	org, err := tenant(ctx, "ask the books")
	if err != nil {
		return nil, err
	}
	q := strings.TrimSpace(in.Question)
	if q == "" {
		return nil, zip.Errorf(http.StatusBadRequest, "question is required")
	}
	if len(q) > maxQuestion {
		q = q[:maxQuestion]
	}
	st, err := o.s.State.storeFor(org, sandboxFrom(ctx))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "books open failed")
	}
	m, err := computeMetrics(ctx, st, in.From, in.To)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "books metrics failed")
	}

	resp := buildAnswer(q, m) // deterministic: REAL figures + templated answer

	// LLM narration seam: rephrase the templated answer more naturally, grounded on the
	// exact figures, WITHOUT changing a number. Degrades silently to the template.
	if narrated := narrateAsk(ctx, o.s, org, q, resp); narrated != "" {
		resp.Answer = narrated
	}
	return &resp, nil
}

// intent is the coarse class of a books question — the ONE thing the keyword router
// resolves. Each intent selects the figures, the templated sentence, and the report sources.
type intent int

const (
	intentSummary intent = iota
	intentMRR
	intentARR
	intentRevenue
	intentRunway
	intentBurn
	intentCOGS
	intentMargin
	intentCash
	intentDeferred
	intentProfit
)

// classifyQuestion maps a plain-language question to an intent by keyword. First match wins,
// most-specific first, so "annual recurring" resolves to ARR before the looser "revenue".
// No model, no fuzzing — the same question always routes the same way, which is what makes
// the figures reproducible.
func classifyQuestion(q string) intent {
	l := strings.ToLower(q)
	has := func(subs ...string) bool {
		for _, s := range subs {
			if strings.Contains(l, s) {
				return true
			}
		}
		return false
	}
	switch {
	case has("runway", "how long", "months left", "cash last", "out of money", "out of cash"):
		return intentRunway
	case has("arr", "annual recurring", "annualized"):
		return intentARR
	case has("mrr", "recurring", "subscription"):
		return intentMRR
	case has("gross margin", "margin", "profitab"):
		return intentMargin
	case has("cogs", "cost of goods", "cost of revenue", "unit cost"):
		return intentCOGS
	case has("burn", "spend", "spending", "expenses", "expense", "opex", "costs"):
		return intentBurn
	case has("deferred", "wallet", "prepaid", "liabilit", "owe"):
		return intentDeferred
	case has("cash", "bank", "in the bank", "balance"):
		return intentCash
	case has("net income", "bottom line", "profit", "earnings", "are we profitable"):
		return intentProfit
	case has("revenue", "sales", "top line", "income", "how much did we make", "how much money"):
		return intentRevenue
	default:
		return intentSummary
	}
}

// buildAnswer is the PURE router: question + real metrics → the grounded response. It never
// reads the store or a model — it only shapes figures already computed — so it is fully
// unit-tested and the figures it returns are exactly the ledger's numbers.
func buildAnswer(q string, m Metrics) AskResponse {
	p := m.Period
	fig := func(label, value string) Figure { return Figure{Label: label, Value: value, Period: p} }

	switch classifyQuestion(q) {
	case intentMRR:
		return AskResponse{
			Answer:    "Your MRR (recurring revenue) is " + formatUSD(m.MRR) + " for " + p + ", an annualized " + formatUSD(m.ARR) + " ARR.",
			Figures:   []Figure{fig("MRR", formatUSD(m.MRR)), fig("ARR", formatUSD(m.ARR))},
			Followups: []string{"How does MRR compare to total revenue?", "What is my runway at this burn?"},
			Sources:   []string{"pnl"},
		}
	case intentARR:
		return AskResponse{
			Answer:    "Your ARR is " + formatUSD(m.ARR) + " for " + p + " (MRR of " + formatUSD(m.MRR) + " × 12).",
			Figures:   []Figure{fig("ARR", formatUSD(m.ARR)), fig("MRR", formatUSD(m.MRR))},
			Followups: []string{"What share of revenue is recurring vs usage?"},
			Sources:   []string{"pnl"},
		}
	case intentRevenue:
		return AskResponse{
			Answer:    "You recognized " + formatUSD(m.Revenue) + " of revenue in " + p + ", of which " + formatUSD(m.MRR) + " is recurring (MRR).",
			Figures:   []Figure{fig("Revenue", formatUSD(m.Revenue)), fig("MRR", formatUSD(m.MRR))},
			Followups: []string{"What was gross margin on that revenue?", "How much of revenue is recurring?"},
			Sources:   []string{"pnl", "trial-balance"},
		}
	case intentRunway:
		return AskResponse{
			Answer:    runwaySentence(m, p),
			Figures:   []Figure{fig("Runway", runwayValue(m)), fig("Cash", formatUSD(m.Cash)), fig("Monthly burn", formatUSD(m.MonthlyBurn))},
			Followups: []string{"What is driving my burn?", "How would runway change if revenue grew 20%?"},
			Sources:   []string{"balance-sheet", "pnl"},
		}
	case intentBurn:
		return AskResponse{
			Answer:    "Total operating burn was " + formatUSD(m.Burn) + " in " + p + " (" + formatUSD(m.COGS) + " of that is COGS), against " + formatUSD(m.Revenue) + " revenue — net " + formatUSD(m.NetIncome) + ".",
			Figures:   []Figure{fig("Burn", formatUSD(m.Burn)), fig("COGS", formatUSD(m.COGS)), fig("Net income", formatUSD(m.NetIncome))},
			Followups: []string{"What is my runway at this burn?", "Which cost line is largest?"},
			Sources:   []string{"pnl"},
		}
	case intentCOGS:
		return AskResponse{
			Answer:    "Cost of goods (cloud/GPU) was " + formatUSD(m.COGS) + " in " + p + ", leaving " + formatUSD(m.GrossProfit) + " gross profit (" + marginPct(m) + " margin).",
			Figures:   []Figure{fig("COGS", formatUSD(m.COGS)), fig("Gross profit", formatUSD(m.GrossProfit)), fig("Gross margin", marginPct(m))},
			Followups: []string{"Is COGS being tracked on every usage charge?"},
			Sources:   []string{"pnl"},
		}
	case intentMargin:
		return AskResponse{
			Answer:    "Gross margin is " + marginPct(m) + " for " + p + " — " + formatUSD(m.GrossProfit) + " gross profit on " + formatUSD(m.Revenue) + " revenue after " + formatUSD(m.COGS) + " COGS.",
			Figures:   []Figure{fig("Gross margin", marginPct(m)), fig("Gross profit", formatUSD(m.GrossProfit)), fig("Revenue", formatUSD(m.Revenue))},
			Followups: []string{"How does margin trend month over month?"},
			Sources:   []string{"pnl"},
		}
	case intentCash:
		return AskResponse{
			Answer:    "You have " + formatUSD(m.Cash) + " in cash as of " + p + " (bank + processor clearing).",
			Figures:   []Figure{fig("Cash", formatUSD(m.Cash)), fig("Runway", runwayValue(m))},
			Followups: []string{"How many months of runway is that?"},
			Sources:   []string{"balance-sheet", "trial-balance"},
		}
	case intentDeferred:
		return AskResponse{
			Answer:    "Deferred revenue (unspent customer wallet credits you still owe as service) is " + formatUSD(m.DeferredRevenue) + " as of " + p + ".",
			Figures:   []Figure{fig("Deferred revenue", formatUSD(m.DeferredRevenue))},
			Followups: []string{"How fast are credits being consumed into revenue?"},
			Sources:   []string{"balance-sheet"},
		}
	case intentProfit:
		return AskResponse{
			Answer:    profitSentence(m, p),
			Figures:   []Figure{fig("Net income", formatUSD(m.NetIncome)), fig("Revenue", formatUSD(m.Revenue)), fig("Burn", formatUSD(m.Burn))},
			Followups: []string{"What is my runway?", "What would it take to break even?"},
			Sources:   []string{"pnl"},
		}
	default:
		return AskResponse{
			Answer:    "For " + p + ": " + formatUSD(m.Revenue) + " revenue (" + formatUSD(m.MRR) + " MRR), " + formatUSD(m.Burn) + " burn, " + formatUSD(m.Cash) + " cash, and " + runwayValue(m) + " of runway.",
			Figures:   []Figure{fig("Revenue", formatUSD(m.Revenue)), fig("MRR", formatUSD(m.MRR)), fig("Cash", formatUSD(m.Cash)), fig("Runway", runwayValue(m))},
			Followups: []string{"What's my MRR?", "How long is my runway?", "What is my gross margin?"},
			Sources:   []string{"pnl", "balance-sheet"},
		}
	}
}

// runwayValue formats the runway figure: whole months, or "unlimited" for the infinite
// sentinel (a profitable / break-even period consumes no runway).
func runwayValue(m Metrics) string {
	if m.RunwayMonths < 0 {
		return "unlimited"
	}
	if m.RunwayMonths == 1 {
		return "1 month"
	}
	return itoa(m.RunwayMonths) + " months"
}

func runwaySentence(m Metrics, p string) string {
	if m.RunwayMonths < 0 {
		return "You are not burning cash in " + p + " (net income " + formatUSD(m.NetIncome) + "), so runway is effectively unlimited on " + formatUSD(m.Cash) + " cash."
	}
	return "At " + formatUSD(m.MonthlyBurn) + "/mo net burn against " + formatUSD(m.Cash) + " cash, you have " + runwayValue(m) + " of runway."
}

func profitSentence(m Metrics, p string) string {
	if m.NetIncome >= 0 {
		return "You are profitable in " + p + ": " + formatUSD(m.NetIncome) + " net income on " + formatUSD(m.Revenue) + " revenue and " + formatUSD(m.Burn) + " burn."
	}
	return "You are running a net loss of " + formatUSD(-m.NetIncome) + " in " + p + " — " + formatUSD(m.Revenue) + " revenue against " + formatUSD(m.Burn) + " burn."
}

// marginPct renders gross margin basis points as a whole-percent string ("70%"). A period
// with no revenue has an undefined margin, shown as "n/a" rather than a divide artifact.
func marginPct(m Metrics) string {
	if m.Revenue <= 0 {
		return "n/a"
	}
	return itoa(m.GrossMarginBps/100) + "%"
}

// itoa is a tiny signed-int formatter (no fmt import in the pure router path).
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}

// narrateAsk runs ONE grounded completion that rephrases the deterministic answer more
// naturally, billed to the caller's HOME org and scoped to the caller's own org (so an Ask
// never spends another tenant's budget). The prompt hands the model the EXACT figures and
// forbids changing any number — the model rewrites prose only. Returns "" when no AI plane
// is wired or the call errors, so the caller keeps the templated (already-correct) answer.
// The figures are the ledger's; this seam only affects wording.
//
// The BILLING ledger is principal.Ledger read off the request, which the typed op reaches
// through cloud.Request — the org that pays is a header fact, not an input a caller may
// assert. Its caller has already resolved and gated the effective org, which is the
// precondition principal.Ledger states; off the HTTP path there is no request, and the
// meter no-ops on the empty org rather than billing the wrong one.
func narrateAsk(ctx context.Context, s *cloud.Service[*state], org, question string, resp AskResponse) string {
	if s.State.ai == nil {
		return ""
	}
	var billing string
	if c, ok := cloud.Request(ctx); ok {
		billing = principal.Ledger(c)
	}
	var fb strings.Builder
	for _, f := range resp.Figures {
		fb.WriteString("- " + f.Label + ": " + f.Value + " (" + f.Period + ")\n")
	}
	prompt := "You are a precise CFO assistant. Answer the founder's question in ONE or TWO natural sentences.\n" +
		"You MUST use these figures EXACTLY as given — never invent, round, or alter a number:\n" +
		fb.String() +
		"\nQuestion: " + question +
		"\nGrounded draft (rephrase naturally, keep every figure identical): " + resp.Answer +
		"\nReturn only the answer."
	res, err := s.State.ai.ChatCompletion(ctx, &cloud.ChatRequest{
		Model:      s.State.model,
		Prompt:     prompt,
		Org:        org,
		BillingOrg: billing,
	})
	if err != nil || res == nil {
		return ""
	}
	return strings.TrimSpace(res.Content)
}
