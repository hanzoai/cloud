package websearch

// mojeek_api.go — the same engine, asked properly when we hold a key.
//
// Mojeek is already here as a scrape of its public result page, and that stays:
// it is keyless, it answers a datacenter IP where DuckDuckGo serves a challenge,
// and it is the engine that carries `site:`. What it cannot do is scale — one
// page is ten results, parsed out of markup that can change under us.
//
// The API is the same index without either limit. Measured against the live
// service on the same query the scraped engines have historically fumbled:
//
//	firecracker microvm kvm   40 results, led by firecracker-microvm.github.io
//	                          (bing led with a fireworks retailer)
//
// Forty per request against the scrape's ten, JSON instead of HTML, and no
// challenge page in the failure modes.
//
// SO IT IS A FALLBACK CHAIN, NOT A SECOND ENGINE. `mojeek` is one name, one
// registry entry and one set of results; the key decides which door it knocks
// on, exactly as the cache→static→browser chain decides how a page is fetched.
// Registering an api-mojeek beside the scraped mojeek would be two spellings of
// one index, and a caller choosing between them would be choosing a credential,
// which is not their decision to make.
//
// A run WITHOUT the key is unchanged. That is deliberate and not a courtesy: the
// keyless path is the free tier of this product, and an engine that stops working
// when a bill goes unpaid is an outage, not a downgrade.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// mojeekKey is the API key, KMS-sourced onto the env like every other secret
// this process reads. Empty means the scrape answers, which is a working answer.
func mojeekKey() string { return strings.TrimSpace(os.Getenv("WEBSEARCH_MOJEEK_KEY")) }

func mojeekAPIURL() string {
	return envOr("WEBSEARCH_MOJEEK_API_URL", "https://api.mojeek.com/search")
}

// mojeekAPICount is how many hits to ask for. The plan allows 40 per request and
// the merge caps at 20 — asking for the full 40 is still right, because dedupe
// against the other engines eats into it and a short page is the one thing a
// second engine cannot fix.
const mojeekAPICount = "40"

// mojeekAPIResponse is the subset we read. The envelope nests under `response`,
// and `status` is a STRING that says OK or explains itself — an exhausted
// balance answers HTTP 200 with status "Access Denied: denied due to
// insufficient balance" and an empty result list, which is why the status is
// checked and not just the code. A 200 with no results and no complaint would
// otherwise read as "the web has nothing", which is the silent-zero this package
// keeps finding.
type mojeekAPIResponse struct {
	Response struct {
		Status  string `json:"status"`
		Results []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
			Desc  string `json:"desc"`
		} `json:"results"`
	} `json:"response"`
}

func mojeekAPIFetch(ctx context.Context, query, lang string) ([]webResult, error) {
	key := mojeekKey()
	if key == "" {
		return nil, nil // the scrape answers; see the file comment
	}
	v := url.Values{}
	v.Set("api_key", key)
	v.Set("q", query)
	v.Set("fmt", "json")
	v.Set("t", mojeekAPICount)
	if lang != "" {
		v.Set("lb", lang)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mojeekAPIURL()+"?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	resp, err := searchClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errStatus(mojeekName, resp.StatusCode)
	}
	var out mojeekAPIResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return nil, err
	}
	// A refusal the transport called success. Reported as an ERROR so the engine
	// reads `failed` rather than `blind` — "we are out of credit" and "the web
	// has nothing" must not look alike in a log.
	if s := out.Response.Status; s != "" && !strings.EqualFold(s, "OK") {
		return nil, errMojeek(s)
	}

	res := make([]webResult, 0, len(out.Response.Results))
	for _, r := range out.Response.Results {
		if strings.TrimSpace(r.URL) == "" {
			continue
		}
		res = append(res, webResult{
			URL:     r.URL,
			Title:   strings.TrimSpace(r.Title),
			Content: collapse(r.Desc),
			Engine:  mojeekName,
		})
	}
	return res, nil
}

type mojeekErr string

func (e mojeekErr) Error() string { return mojeekName + ": " + string(e) }

func errMojeek(status string) error { return mojeekErr(status) }
