package tel

import (
	"github.com/hanzoai/cloud/internal/environ"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/shorten"
)

// restCarrier speaks the carrier's HTTP API.
//
// Everything that identifies WHICH carrier — the base URL and the credential —
// is configuration, read from the environment at mount. The code holds the wire
// shape, not the vendor: a second carrier is a second base URL and a mapping, and
// nothing above this file changes.
//
// Credentials come from the environment, which on a cluster is populated from
// KMS. There is no literal here and there must never be one.
type restCarrier struct {
	base string
	key  string
	http *http.Client
}

// carrierFromEnv builds the carrier the deployment is configured for, or nil when
// it is configured for none. Nil is a valid state — a brand may run the surface
// with only the stub, and mount refuses individual calls rather than the process.
func carrierFromEnv() Carrier {
	base := strings.TrimRight(environ.Or("TEL_CARRIER_BASE", ""), "/")
	key := environ.Or("TEL_CARRIER_KEY", "")
	if base == "" || key == "" {
		return nil
	}
	return &restCarrier{
		base: base,
		key:  key,
		// A carrier call sits inside a request the user is waiting on, so the
		// timeout is short enough to fail before they give up. Without one, a
		// stalled upstream holds the handler open until the server's own timeout.
		http: &http.Client{Timeout: 20 * time.Second},
	}
}

func (c *restCarrier) do(ctx context.Context, method, path string, body any, out any) error {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode: %w", err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, r)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("carrier unreachable: %w", err)
	}
	defer res.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode >= 300 {
		// The carrier's own message is carried through, trimmed. A handler that
		// swallows it leaves "the call failed" as the only thing anyone can act on.
		return fmt.Errorf("carrier %d: %s", res.StatusCode, shorten.To(strings.TrimSpace(string(raw)), 300))
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, out)
}

// The carrier answers with its own field names. These envelopes are the ONE place
// that vocabulary is translated into ours — every other file in this package
// speaks Number, Call and SMS.

type numberRow struct {
	ID          string   `json:"id"`
	PhoneNumber string   `json:"phone_number"`
	Country     string   `json:"country_code"`
	Type        string   `json:"phone_number_type"`
	Features    []string `json:"features"`
	Cost        string   `json:"monthly_cost"`
	Currency    string   `json:"currency"`
}

func (n numberRow) ours() Number {
	monthly := int64(0)
	// Money arrives as a decimal string. Parsed into minor units rather than
	// carried as a float — a rate that is exact on the invoice must not become
	// approximate on the way to it.
	if f, err := strconv.ParseFloat(n.Cost, 64); err == nil {
		monthly = int64(f*100 + 0.5)
	}
	return Number{
		ID: n.ID, E164: n.PhoneNumber, Country: n.Country, Type: n.Type,
		Capable: n.Features, Monthly: monthly, Currency: n.Currency,
	}
}

func (c *restCarrier) Search(ctx context.Context, q NumberQuery) ([]Number, error) {
	v := url.Values{}
	v.Set("filter[country_code]", q.Country)
	if q.Area != "" {
		v.Set("filter[national_destination_code]", q.Area)
	}
	if q.Type != "" {
		v.Set("filter[phone_number_type]", q.Type)
	}
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	v.Set("filter[limit]", strconv.Itoa(limit))

	var body struct {
		Data []numberRow `json:"data"`
	}
	if err := c.do(ctx, http.MethodGet, "/available_phone_numbers?"+v.Encode(), nil, &body); err != nil {
		return nil, err
	}
	out := make([]Number, 0, len(body.Data))
	for _, r := range body.Data {
		out = append(out, r.ours())
	}
	return out, nil
}

func (c *restCarrier) Buy(ctx context.Context, e164 string) (Number, error) {
	var body struct {
		Data numberRow `json:"data"`
	}
	req := map[string]any{"phone_numbers": []map[string]string{{"phone_number": e164}}}
	if err := c.do(ctx, http.MethodPost, "/number_orders", req, &body); err != nil {
		return Number{}, err
	}
	n := body.Data.ours()
	if n.E164 == "" {
		n.E164 = e164
	}
	return n, nil
}

func (c *restCarrier) Release(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/phone_numbers/"+url.PathEscape(id), nil, nil)
}

func (c *restCarrier) Call(ctx context.Context, r CallRequest) (Call, error) {
	req := map[string]any{"to": r.To, "from": r.From}
	if r.Webhook != "" {
		req["webhook_url"] = r.Webhook
	}
	if r.Record {
		req["record"] = "record-from-answer"
	}
	var body struct {
		Data struct {
			ID     string `json:"call_control_id"`
			Status string `json:"call_session_status"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/calls", req, &body); err != nil {
		return Call{}, err
	}
	status := body.Data.Status
	if status == "" {
		status = "queued"
	}
	return Call{ID: body.Data.ID, From: r.From, To: r.To, Status: status, Agent: r.Agent}, nil
}

func (c *restCarrier) Hangup(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodPost, "/calls/"+url.PathEscape(id)+"/actions/hangup", map[string]any{}, nil)
}

func (c *restCarrier) Send(ctx context.Context, r SMSRequest) (SMS, error) {
	req := map[string]any{"from": r.From, "to": r.To, "text": r.Text}
	if len(r.Media) > 0 {
		req["media_urls"] = r.Media
	}
	var body struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := c.do(ctx, http.MethodPost, "/messages", req, &body); err != nil {
		return SMS{}, err
	}
	// `queued`, not `sent`. The carrier has accepted it; whether it arrives is
	// reported later, and recording acceptance as delivery is how a message that
	// never landed shows up as one that did.
	return SMS{ID: body.Data.ID, From: r.From, To: r.To, Text: r.Text, Status: "queued"}, nil
}
