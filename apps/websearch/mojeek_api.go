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
// registry entry and one set of results; the key decides which endpoint it calls,
// exactly as the cache→static→browser chain decides how a page is fetched.
// Registering an api-mojeek beside the scraped mojeek would be two spellings of
// one index, and a caller choosing between them would be choosing a credential,
// which is not their decision to make.
//
// A run WITHOUT the key still asks the page, but the page is not counted on: from
// this cluster's egress it has answered `failed` (measured 2026-10-10). So the key
// is what makes mojeek an engine search can stand on, and a lineup with no other
// engine that answers is refused at save and replaced by the keyless default at
// search time (lineup.go) — an unpaid bill is a downgrade, never an empty page.
//
// The key is the platform's, sealed in KMS at orgs/admin/websearch/MOJEEK_KEY@prod
// and set through PUT /v1/websearch/engines/mojeek/key. It rides the query string
// because that is the API's contract, so it is never logged, never part of the
// cache key (build names the page URL), and stripped from any transport error.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/sdk/env"
)

func mojeekAPIURL() string {
	return env.Or("WEBSEARCH_MOJEEK_API_URL", "https://api.mojeek.com/search")
}

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

// mojeekAPIFetch asks the API with the key the platform holds (q.key, read from
// KMS for this search — see lineup.go). No key is (nil, nil): the page answers.
func mojeekAPIFetch(ctx context.Context, q question) ([]webResult, error) {
	if q.key == "" {
		return nil, nil // the scrape answers; see the file comment
	}
	v := url.Values{}
	v.Set("api_key", q.key)
	v.Set("q", q.query)
	v.Set("fmt", "json")
	v.Set("t", strconv.Itoa(q.count))
	if q.lang != "" {
		v.Set("lb", q.lang)
	}
	if q.region != "" {
		v.Set("rb", q.region)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, mojeekAPIURL()+"?"+v.Encode(), nil)
	if err != nil {
		return nil, errors.New(mojeekName + ": request could not be built")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := searchClient.Do(req)
	if err != nil {
		// The key rides the query string, and a transport error quotes the URL.
		// Keep the cause, drop the URL, so no error from here can carry the key.
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, ue.Err
		}
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
