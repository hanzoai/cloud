package idv

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// rest.go is the real, config-driven verification provider: a hosted-inquiry REST
// adapter that fits Persona, Onfido, and Stripe Identity. All three expose the same
// shape this adapter drives — create an inquiry/session for a subject (returns an id
// + a hosted URL the subject completes) and fetch it to read the current decision.
// Their JSON field names and status vocabularies differ; the differences are
// isolated to two places: the request/response field mapping (small, below) and the
// per-provider status table (classifierFor, config.go). Everything else — the
// KMS-sealed bearer, the fail-closed decision mapping, the timeouts — is shared.
//
// KEY CUSTODY. The provider API key is NEVER held in code or a plaintext env var: it
// is resolved through the injected key func, which the composition root wires to
// deps.KMS.GetSecret against a config'd secret reference. The key is fetched per
// call (short-lived in memory) and never logged.

// httpTimeout bounds every provider call so a slow provider can never wedge a
// request-path verification.
const httpTimeout = 15 * time.Second

// REST is a hosted-inquiry verification provider. name is the provider label
// (persona|onfido|stripe); base is its API root; key resolves the KMS-sealed bearer
// token; classify maps the provider's status vocabulary to our honest Status
// (fail-closed — an unrecognized token is pending, never verified).
type REST struct {
	name     string
	base     string
	key      func(ctx context.Context) ([]byte, error)
	classify map[string]Status
	http     *http.Client
}

// Name identifies the wired provider.
func (r *REST) Name() string { return r.name }

// inquiryReq is the create-inquiry body. reference_id carries the caller's opaque
// subject ref so a webhook is correlatable without PII; name/email open the hosted
// flow; kind selects the individual vs business verification template.
type inquiryReq struct {
	ReferenceID string `json:"reference_id"`
	Name        string `json:"name,omitempty"`
	Email       string `json:"email,omitempty"`
	Kind        string `json:"kind"`
}

// inquiryResp is the provider response subset we read. id is the provider reference;
// hosted_url (aka url) is where the subject completes the flow; status is the
// provider's status token we classify.
type inquiryResp struct {
	ID        string `json:"id"`
	HostedURL string `json:"hosted_url"`
	URL       string `json:"url"`
	Status    string `json:"status"`
}

// Start creates a verification inquiry for the subject and returns the hosted flow.
// The returned status is classify(provider status): on a fresh inquiry that is
// pending or review, NEVER verified. Any failure to obtain a well-formed provider
// response is returned as an error (the consumer surfaces 502) — it never degrades
// to a verified result.
func (r *REST) Start(ctx context.Context, org string, subj Subject) (Session, error) {
	if !ValidKind(subj.Kind) {
		return Session{}, fmt.Errorf("idv: invalid subject kind %q", subj.Kind)
	}
	body, err := json.Marshal(inquiryReq{
		ReferenceID: subj.Ref, Name: subj.Name, Email: subj.Email, Kind: string(subj.Kind),
	})
	if err != nil {
		return Session{}, fmt.Errorf("idv: encode inquiry: %w", err)
	}
	var resp inquiryResp
	if err := r.do(ctx, http.MethodPost, "/inquiries", body, &resp); err != nil {
		return Session{}, err
	}
	if strings.TrimSpace(resp.ID) == "" {
		return Session{}, fmt.Errorf("idv: %s returned no inquiry id", r.name)
	}
	url := resp.HostedURL
	if url == "" {
		url = resp.URL
	}
	st := classify(r.classify, resp.Status)
	if st.Terminal() {
		// A brand-new inquiry cannot be a settled decision; a provider reporting one
		// is anomalous. Fail closed to pending rather than trust a premature terminal.
		st = StatusPending
	}
	return Session{Ref: resp.ID, VerifyURL: url, Status: st}, nil
}

// Check polls the current provider decision for a reference and maps it to our
// honest Status. A transport/parse failure is an error, never a verified result.
func (r *REST) Check(ctx context.Context, org, ref string) (Result, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return Result{}, fmt.Errorf("idv: empty verification ref")
	}
	var resp inquiryResp
	if err := r.do(ctx, http.MethodGet, "/inquiries/"+ref, nil, &resp); err != nil {
		return Result{}, err
	}
	return Result{Ref: ref, Status: classify(r.classify, resp.Status)}, nil
}

// do performs one provider call: resolves the sealed bearer, sends the request, and
// decodes a 2xx JSON body into out. It is fail-closed at every step — a key that
// won't resolve, a non-2xx status, or an unreadable/undecodable body is an error, so
// no failure path can yield a positive decision. The bearer is never logged.
func (r *REST) do(ctx context.Context, method, path string, body []byte, out any) error {
	token, err := r.key(ctx)
	if err != nil {
		return fmt.Errorf("idv: %s key unavailable: %w", r.name, err)
	}
	if len(token) == 0 {
		return fmt.Errorf("idv: %s key is empty", r.name)
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(r.base, "/")+path, rdr)
	if err != nil {
		return fmt.Errorf("idv: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := r.http.Do(req)
	if err != nil {
		return fmt.Errorf("idv: %s request: %w", r.name, err)
	}
	defer func() { _ = res.Body.Close() }()
	// Bound the body read so a hostile/huge provider response can't OOM the process.
	data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("idv: %s read body: %w", r.name, err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		// The provider status text is intentionally NOT echoed (it may carry subject
		// detail); only the HTTP code is surfaced.
		return fmt.Errorf("idv: %s returned HTTP %d", r.name, res.StatusCode)
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("idv: %s decode response: %w", r.name, err)
		}
	}
	return nil
}

// classify maps a provider status token to our honest Status. It is the pure,
// fail-closed core of the whole seam: a token is looked up (case-insensitively) in
// the provider's table, and ANY miss — an unknown value, an empty string, a
// provider-internal "processing"/"created" state — resolves to StatusPending. Only
// an explicitly-tabled success token yields StatusVerified, so no ambiguity, typo,
// or new provider state can ever produce a verified result by default.
func classify(table map[string]Status, token string) Status {
	t := strings.ToLower(strings.TrimSpace(token))
	if t == "" {
		return StatusPending
	}
	if st, ok := table[t]; ok {
		return st
	}
	return StatusPending
}
