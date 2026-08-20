package seo

// dataforseo.go is the ONE place this package speaks to the upstream. Every op
// goes through post(); nothing else in the package knows the vendor's host, its
// envelope, or its status vocabulary.
//
// The upstream is DataForSEO's v3 API: HTTP Basic auth, one POST per call, a body
// that is an ARRAY of tasks. This package sends exactly one task per call — a
// batch would make every answer a partial-failure envelope each caller then has to
// unpack, and would make one charge cover work the caller cannot separate.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/money"
	"github.com/zap-proto/zip"
)

const (
	// vendor is the upstream root. It is used once, to seed state.base.
	vendor = "https://api.dataforseo.com/v3"

	// callTimeout bounds one vendor call end to end. Their live endpoints answer in
	// seconds; a request-scoped surface in front of a hung upstream holds a
	// connection open for as long as it waits, so the wait is bounded here rather
	// than left to the caller's patience.
	callTimeout = 60 * time.Second

	// maxAnswer caps what is read back. The widest op (a thousand ranked keywords
	// with their SERP elements) is a few megabytes; past that the answer is not one
	// this surface reshapes, it is one it would forward wholesale.
	maxAnswer = 32 << 20

	// succeeded is the vendor's word for "this worked", at both the envelope and the
	// task level. Everything else is theirs to explain, and their message is the
	// useful part — "Please verify your account", "Invalid Field: 'location_name'" —
	// so it is passed through rather than replaced with one of ours.
	//
	// Spelled out rather than called `ok`, which is the most-shadowed identifier in
	// Go: one `x, ok := ...` in scope and the comparison below would silently be
	// against a bool.
	succeeded = 20000
)

// answer is the shape every /v3 endpoint replies in.
//
// The COST fields are json.Number and not float64 on purpose: they are money, they
// go to eighteen decimals, and a float64 round trip is how $0.00012 becomes
// $0.00011999999999999999 in a ledger. The literal the vendor sent is parsed
// exactly by money.ParseUSD.
type answer struct {
	StatusCode    int         `json:"status_code"`
	StatusMessage string      `json:"status_message"`
	Cost          json.Number `json:"cost"`
	Tasks         []struct {
		StatusCode    int             `json:"status_code"`
		StatusMessage string          `json:"status_message"`
		Cost          json.Number     `json:"cost"`
		Result        json.RawMessage `json:"result"`
	} `json:"tasks"`
}

// task is one product question: where the vendor answers it, where the vendor
// lists its price, and what the ledger calls the act.
//
// The two addresses are both stated because they are genuinely two addresses. The
// vendor's price list keys its labs endpoints WITHOUT the search-engine segment
// the URL carries — /v3/dataforseo_labs/google/ranked_keywords/live is listed
// under dataforseo_labs/ranked_keywords/live — and deriving one from the other
// would encode that quirk as a rule that holds until it doesn't. Two strings,
// side by side, is the honest shape: this is an address table, not a price table.
type task struct {
	// path is the /v3 endpoint, relative to the vendor root.
	path string
	// rate is the key this endpoint's price is listed under in the vendor's own
	// price list. See [charges].
	rate string
	// kind is the ledger's word for one call to this op — the op id, so a line on
	// an invoice and a tool in a model's list are the same name.
	kind string
}

// The six. Each is one product question; several vendor endpoints fold into each.
var (
	keyword    = task{"keywords_data/google_ads/search_volume/live", "keywords_data/google_ads/search_volume/live", "seoKeyword"}
	idea       = task{"dataforseo_labs/google/keyword_ideas/live", "dataforseo_labs/keyword_ideas/live", "seoIdea"}
	rank       = task{"dataforseo_labs/google/ranked_keywords/live", "dataforseo_labs/ranked_keywords/live", "seoRank"}
	competitor = task{"dataforseo_labs/google/serp_competitors/live", "dataforseo_labs/serp_competitors/live", "seoCompetitor"}
	backlink   = task{"backlinks/summary/live", "backlinks/summary/live", "seoBacklink"}
	audit      = task{"on_page/instant_pages", "on_page/instant_pages", "seoAudit"}
)

// ── the vendor's vocabulary ──────────────────────────────────────────────────
//
// What the upstream answers, in ITS names, decoded only as far as the handful of
// fields that become the product. These types are unexported and never reach the
// wire: everything published is declared in typed.go, in our names, so a field the
// vendor renames breaks a translation here instead of a customer's SDK.
//
// A vendor row is around sixty fields wide and grows. Naming all of them would be
// a schema that drifts every release and a contract nobody could hold; naming the
// ones that ARE the product is a translation, which is the job.

// volume is one row of keywords_data/google_ads/search_volume/live. The whole
// result IS the list of keywords — there is no wrapper.
type volume struct {
	Keyword string  `json:"keyword"`
	Volume  int     `json:"search_volume"`
	CPC     float64 `json:"cpc"`
	// The vendor says competition twice and differently: a word here, and an index
	// out of a hundred beside it. The labs endpoints say it once, as a fraction.
	// typed.go publishes the fraction from both.
	Level string `json:"competition"`
	Index int    `json:"competition_index"`
}

// ideas is dataforseo_labs/google/keyword_ideas/live: a wrapper with a count and
// the rows under it.
type ideas struct {
	Total int `json:"total_count"`
	Items []struct {
		Keyword string `json:"keyword"`
		Info    struct {
			Volume      int     `json:"search_volume"`
			CPC         float64 `json:"cpc"`
			Competition float64 `json:"competition"`
			Level       string  `json:"competition_level"`
		} `json:"keyword_info"`
		Properties struct {
			Difficulty int `json:"keyword_difficulty"`
		} `json:"keyword_properties"`
	} `json:"items"`
}

// ranked is dataforseo_labs/google/ranked_keywords/live. Each row is a keyword
// joined to the SERP element the target holds for it — the two halves of "you
// rank here for this".
type ranked struct {
	Total int `json:"total_count"`
	Items []struct {
		Keyword struct {
			Keyword string `json:"keyword"`
			Info    struct {
				Volume int `json:"search_volume"`
			} `json:"keyword_info"`
		} `json:"keyword_data"`
		Element struct {
			Item struct {
				Rank    int     `json:"rank_absolute"`
				URL     string  `json:"url"`
				Title   string  `json:"title"`
				Traffic float64 `json:"etv"`
			} `json:"serp_item"`
		} `json:"ranked_serp_element"`
	} `json:"items"`
}

// rivals is dataforseo_labs/google/serp_competitors/live: the domains that place
// for the same phrases.
type rivals struct {
	Total int `json:"total_count"`
	Items []struct {
		Domain     string  `json:"domain"`
		Position   float64 `json:"avg_position"`
		Keywords   int     `json:"keywords_count"`
		Visibility float64 `json:"visibility"`
		Traffic    float64 `json:"etv"`
	} `json:"items"`
}

// links is backlinks/summary/live. One target, one row.
type links struct {
	Target    string `json:"target"`
	Rank      int    `json:"rank"`
	Backlinks int    `json:"backlinks"`
	Spam      int    `json:"backlinks_spam_score"`
	Broken    int    `json:"broken_backlinks"`
	Domains   int    `json:"referring_domains"`
	Pages     int    `json:"referring_pages"`
	FirstSeen string `json:"first_seen"`
}

// page is on_page/instant_pages: one URL, fetched and checked.
type page struct {
	Items []struct {
		URL    string  `json:"url"`
		Status int     `json:"status_code"`
		Score  float64 `json:"onpage_score"`
		Meta   struct {
			Title       string `json:"title"`
			Description string `json:"description"`
			Content     struct {
				Words int `json:"plain_text_word_count"`
			} `json:"content"`
		} `json:"meta"`
		// Checks is the vendor's own open set of named findings — around fifty
		// today, each a plain yes/no. It is carried as a map because it IS a map:
		// naming the fifty would publish a schema that is wrong the next time they
		// add one, and map[string]bool publishes "an object of booleans", which is
		// true now and stays true.
		Checks map[string]bool `json:"checks"`
	} `json:"items"`
}

// post sends one task and returns the vendor's result rows and what it says it
// charged.
//
// The CHARGE IS READ FROM THE ANSWER, not computed. The vendor states what it
// billed on every reply, per-row scaling already applied, so recomputing it here
// would be a second opinion about somebody else's invoice — right until a rounding
// rule or a per-result tier moved and nothing said so. The quote (rate.go) exists
// for the moment BEFORE the call, when no answer exists yet; this is the moment
// after, and after, there is a fact.
//
// A refusal by the vendor is a 502 and carries their message verbatim. It is not a
// 500: nothing here failed, and it is not a 400: the caller's request may have been
// perfectly formed and the account simply unfunded. The caller needs to read the
// reason, so the reason is passed through.
func post(ctx context.Context, s *state, t task, body any) (json.RawMessage, money.Amount, error) {
	// The vendor takes an array of tasks. Exactly one, always.
	payload, err := json.Marshal([]any{body})
	if err != nil {
		return nil, money.Zero(), zip.ErrInternal("seo: the request will not encode")
	}
	return send(ctx, s, http.MethodPost, t.path, payload)
}

// send performs one authenticated vendor request and judges the envelope. It is
// the whole conversation with the upstream: post() supplies a task body, rate.go
// supplies none, and neither has to know how a refusal is spelled.
func send(ctx context.Context, s *state, method, path string, payload []byte) (json.RawMessage, money.Amount, error) {
	account, err := s.account(ctx)
	if err != nil {
		return nil, money.Zero(), zip.Errorf(http.StatusServiceUnavailable, "seo is not configured: %v", err)
	}
	var body io.Reader
	if len(payload) > 0 {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.base+"/"+path, body)
	if err != nil {
		return nil, money.Zero(), zip.ErrInternal("seo: " + err.Error())
	}
	req.SetBasicAuth(account.login, account.password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return nil, money.Zero(), zip.Errorf(http.StatusBadGateway, "seo: the upstream did not answer: %v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return nil, money.Zero(), zip.Errorf(http.StatusBadGateway, "seo: the upstream answer could not be read: %v", err)
	}
	var a answer
	if err := json.Unmarshal(raw, &a); err != nil {
		// A body that is not the envelope means the HTTP status is the only thing
		// left that says anything, so it is what gets reported.
		return nil, money.Zero(), zip.Errorf(http.StatusBadGateway, "seo: the upstream answered %d with a body this surface does not recognise", resp.StatusCode)
	}
	// Charged BEFORE the status is judged: a refused envelope reports cost 0, and a
	// task that partly ran reports what it actually cost. Reading it here means the
	// caller is debited for exactly what the vendor billed in every outcome, which
	// is the only reading of "meter at their price" that survives a bad day.
	charged := usd(a.Cost)
	if a.StatusCode != succeeded {
		return nil, charged, zip.Errorf(http.StatusBadGateway, "seo: %s", vendorSays(a.StatusMessage, a.StatusCode))
	}
	if len(a.Tasks) == 0 {
		return nil, charged, zip.Errorf(http.StatusBadGateway, "seo: the upstream accepted the request and answered nothing")
	}
	one := a.Tasks[0]
	if one.StatusCode != succeeded {
		return nil, charged, zip.Errorf(http.StatusBadGateway, "seo: %s", vendorSays(one.StatusMessage, one.StatusCode))
	}
	// A task-level cost is the finer fact when both are present — the envelope sums
	// the tasks, and there is one task.
	if c := usd(one.Cost); !c.IsZero() {
		charged = c
	}
	return one.Result, charged, nil
}

// rows sends one task and decodes the vendor's result array into T.
//
// It is a free function rather than a method because Go methods take no type
// parameters, and the alternative — six near-identical decode blocks — is six
// places for one bug to be fixed in five of them.
func rows[T any](ctx context.Context, s *state, t task, body any) ([]T, money.Amount, error) {
	raw, charged, err := post(ctx, s, t, body)
	if err != nil {
		return nil, charged, err
	}
	var out []T
	// A null result is a well-formed "nothing matched" and decodes to a nil slice.
	if len(raw) == 0 {
		return nil, charged, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, charged, zip.Errorf(http.StatusBadGateway, "seo: the upstream answered a shape this surface does not recognise: %v", err)
	}
	return out, charged, nil
}

// vendorSays renders the upstream's refusal. Their message is the whole value —
// it names the field, the account state or the limit — so it is passed through,
// with their code beside it for anyone reading their documentation.
func vendorSays(message string, code int) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return fmt.Sprintf("the upstream refused with code %d", code)
	}
	return fmt.Sprintf("%s (upstream code %d)", message, code)
}

// usd parses a vendor money literal EXACTLY, in both spellings the vendor uses.
//
// Their price list writes small numbers in scientific notation — the per-row
// charge for a backlink summary is published as `3.6e-05` — and the exact decimal
// parser reads plain notation only. Read as plain, that literal fails and this
// function used to answer zero, which quietly deleted the entire per-row half of
// that op's price from both the quote and the card. A money parser that answers
// zero on input it cannot read is a discount nobody authorized.
//
// So the exponent form is expanded through big.Rat, which reads it exactly and is
// not a float: 3.6e-05 becomes the rational 36/1000000, rendered at 18 decimal
// places — the same 18 the money type carries, so nothing is lost on the way in.
//
// Plain notation is still tried FIRST, because ParseUSD refuses more than 18
// fractional digits rather than silently truncating them, and that refusal is
// worth keeping. json.Number holds the literal the vendor sent, so neither path
// ever passes through a float64.
//
// An absent number is zero, which is the vendor saying a call was free.
func usd(n json.Number) money.Amount {
	s := strings.TrimSpace(n.String())
	if s == "" {
		return money.Zero()
	}
	if a, err := money.ParseUSD(s); err == nil {
		return a
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return money.Zero()
	}
	a, err := money.ParseUSD(r.FloatString(money.Decimals))
	if err != nil {
		return money.Zero()
	}
	return a
}
