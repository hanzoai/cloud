package source

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"time"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// The usage model is the Dev desktop's, field for field: packages/usage/src/schema.ts
// (@hanzodev/usage). Required fields are pointers here only so an absent one can be
// told from a zero; a request is decoded strictly, so an unknown field is refused.

const (
	maxReport   = 1 << 20
	minUsage    = 10 * time.Second
	maxCost     = 20000
	maxSnaps    = 64
	maxWindows  = 16
	maxIncident = 32
	maxFree     = 200
	usageSig    = "hanzo-source-usage/1"
)

var (
	errUsageSoon = errors.New("source: usage reported too soon")

	providerID = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	errorCode  = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)
	dayForm    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

	windowKinds    = set("session", "daily", "weekly", "monthly", "credits", "other")
	indicators     = set("none", "minor", "major", "critical", "maintenance", "unknown")
	snapshotSource = set("oauth", "api", "local", "cookies", "none")
)

func set(v ...string) map[string]bool {
	m := map[string]bool{}
	for _, s := range v {
		m[s] = true
	}
	return m
}

// UsageReport is what a source sends. From and To are ISO-8601 instants.
type UsageReport struct {
	Version   *int            `json:"version"`
	DeviceID  *string         `json:"deviceId"`
	From      *string         `json:"from"`
	To        *string         `json:"to"`
	Cost      []CostDay       `json:"cost"`
	Snapshots []ProviderUsage `json:"snapshots"`
}

// CostDay is tokens and estimated spend for one model on one day.
type CostDay struct {
	Day          *string  `json:"day"`
	Provider     *string  `json:"provider"`
	Model        *string  `json:"model"`
	InputTokens  *float64 `json:"inputTokens"`
	OutputTokens *float64 `json:"outputTokens"`
	CacheTokens  *float64 `json:"cacheTokens"`
	CostUsd      *float64 `json:"costUsd,omitempty"`
}

// ProviderUsage is one provider's latest limits, credits and status on a source.
// SourceID is set on the way out, never read from a report.
type ProviderUsage struct {
	SourceID  string          `json:"sourceId,omitempty"`
	Provider  *string         `json:"provider"`
	FetchedAt *string         `json:"fetchedAt"`
	Source    *string         `json:"source"`
	Plan      *string         `json:"plan,omitempty"`
	Account   *string         `json:"account,omitempty"`
	Windows   []UsageWindow   `json:"windows"`
	Credits   *UsageCredits   `json:"credits,omitempty"`
	Status    *ProviderStatus `json:"status,omitempty"`
	Error     *string         `json:"error,omitempty"`
}

// UsageWindow is one limit window: session, daily, weekly, monthly, credits or other.
type UsageWindow struct {
	Kind          *string  `json:"kind"`
	Label         *string  `json:"label"`
	UsedPercent   *float64 `json:"usedPercent"`
	WindowMinutes *float64 `json:"windowMinutes,omitempty"`
	ResetsAt      *string  `json:"resetsAt,omitempty"`
}

// UsageCredits is a provider's credit balance.
type UsageCredits struct {
	Currency *string  `json:"currency"`
	Balance  *float64 `json:"balance,omitempty"`
	Spent    *float64 `json:"spent,omitempty"`
	Limit    *float64 `json:"limit,omitempty"`
}

// ProviderStatus is the provider's public status page reading.
type ProviderStatus struct {
	Indicator   *string    `json:"indicator"`
	Description *string    `json:"description,omitempty"`
	Incidents   []Incident `json:"incidents"`
	CheckedAt   *string    `json:"checkedAt"`
}

// Incident is a public status incident.
type Incident struct {
	Name   *string `json:"name"`
	Impact *string `json:"impact"`
	URL    *string `json:"url,omitempty"`
}

func bad(format string, a ...any) error { return zip.ErrBadRequest(fmt.Sprintf(format, a...)) }

func str(name string, p *string, max int) error {
	if p == nil {
		return bad("%s is required", name)
	}
	if len(*p) > max {
		return bad("%s is too long", name)
	}
	return nil
}

func instant(name string, p *string) error {
	if p == nil {
		return bad("%s is required", name)
	}
	if _, err := time.Parse(time.RFC3339, *p); err != nil {
		return bad("%s must be an ISO-8601 instant", name)
	}
	return nil
}

func count(name string, p *float64) error {
	if p == nil {
		return bad("%s is required", name)
	}
	if math.IsNaN(*p) || *p < 0 || *p > 1e15 || *p != math.Floor(*p) {
		return bad("%s must be a whole number of at least 0", name)
	}
	return nil
}

func oneOf(name string, p *string, in map[string]bool) error {
	if p == nil || !in[*p] {
		return bad("%s is not one of the accepted values", name)
	}
	return nil
}

func provider(name string, p *string) error {
	if p == nil || len(*p) > maxText || !providerID.MatchString(*p) {
		return bad("%s is not a provider id", name)
	}
	return nil
}

// validate refuses a report that breaks any rule, whole; nothing is trimmed.
func (r *UsageReport) validate(id string) error {
	if r.Version == nil || *r.Version != 1 {
		return bad("version must be 1")
	}
	if r.DeviceID == nil || *r.DeviceID != id {
		return bad("deviceId must be the source's id")
	}
	if err := instant("from", r.From); err != nil {
		return err
	}
	if err := instant("to", r.To); err != nil {
		return err
	}
	if r.Cost == nil || r.Snapshots == nil {
		return bad("cost and snapshots are required")
	}
	if len(r.Cost) > maxCost || len(r.Snapshots) > maxSnaps {
		return bad("report has too many records")
	}
	seen := map[string]bool{}
	for _, c := range r.Cost {
		if c.Day == nil || !dayForm.MatchString(*c.Day) {
			return bad("cost day must be YYYY-MM-DD")
		}
		if _, err := time.Parse("2006-01-02", *c.Day); err != nil {
			return bad("cost day must be YYYY-MM-DD")
		}
		if err := provider("cost provider", c.Provider); err != nil {
			return err
		}
		if err := str("cost model", c.Model, maxFree); err != nil {
			return err
		}
		for n, v := range map[string]*float64{"inputTokens": c.InputTokens, "outputTokens": c.OutputTokens, "cacheTokens": c.CacheTokens} {
			if err := count(n, v); err != nil {
				return err
			}
		}
		if c.CostUsd != nil && (math.IsNaN(*c.CostUsd) || *c.CostUsd < 0 || *c.CostUsd > 1e12) {
			return bad("costUsd must be at least 0")
		}
		k := *c.Day + "\x00" + *c.Provider + "\x00" + *c.Model
		if seen[k] {
			return bad("cost repeats a day, provider and model")
		}
		seen[k] = true
	}
	seen = map[string]bool{}
	for i := range r.Snapshots {
		s := &r.Snapshots[i]
		if err := provider("snapshot provider", s.Provider); err != nil {
			return err
		}
		if seen[*s.Provider] {
			return bad("snapshots repeat a provider")
		}
		seen[*s.Provider] = true
		if err := instant("fetchedAt", s.FetchedAt); err != nil {
			return err
		}
		if err := oneOf("snapshot source", s.Source, snapshotSource); err != nil {
			return err
		}
		if s.Plan != nil && len(*s.Plan) > maxFree {
			return bad("plan is too long")
		}
		// At metadata the account is free text a person typed into a provider: absent.
		if s.Account != nil && *s.Account != "" {
			return bad("account must be absent at the metadata level")
		}
		s.Account = nil
		if s.Error != nil && !errorCode.MatchString(*s.Error) {
			return bad("error must be a short code matching ^[a-z0-9_.-]{1,64}$")
		}
		if s.Windows == nil || len(s.Windows) > maxWindows {
			return bad("windows is required, at most %d", maxWindows)
		}
		for _, w := range s.Windows {
			if err := oneOf("window kind", w.Kind, windowKinds); err != nil {
				return err
			}
			if err := str("window label", w.Label, maxFree); err != nil {
				return err
			}
			if w.UsedPercent == nil || math.IsNaN(*w.UsedPercent) || *w.UsedPercent < 0 || *w.UsedPercent > 100 {
				return bad("usedPercent must be between 0 and 100")
			}
			if w.WindowMinutes != nil && (math.IsNaN(*w.WindowMinutes) || *w.WindowMinutes < 0 || *w.WindowMinutes > 1e9) {
				return bad("windowMinutes is out of range")
			}
			if w.ResetsAt != nil {
				if err := instant("resetsAt", w.ResetsAt); err != nil {
					return err
				}
			}
		}
		if c := s.Credits; c != nil {
			if err := str("credits currency", c.Currency, 16); err != nil {
				return err
			}
			for _, v := range []*float64{c.Balance, c.Spent, c.Limit} {
				if v != nil && (math.IsNaN(*v) || math.IsInf(*v, 0)) {
					return bad("credits must be numbers")
				}
			}
		}
		if st := s.Status; st != nil {
			if err := oneOf("status indicator", st.Indicator, indicators); err != nil {
				return err
			}
			if st.Description != nil && len(*st.Description) > maxFree {
				return bad("status description is too long")
			}
			if err := instant("checkedAt", st.CheckedAt); err != nil {
				return err
			}
			if st.Incidents == nil || len(st.Incidents) > maxIncident {
				return bad("status incidents is required, at most %d", maxIncident)
			}
			for _, in := range st.Incidents {
				if err := str("incident name", in.Name, maxFree); err != nil {
					return err
				}
				if err := oneOf("incident impact", in.Impact, indicators); err != nil {
					return err
				}
				if in.URL != nil && len(*in.URL) > 512 {
					return bad("incident url is too long")
				}
			}
		}
	}
	return nil
}

// decodeReport reads body as exactly one UsageReport, refusing unknown fields and
// trailing data.
func decodeReport(body []byte) (*UsageReport, error) {
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	var r UsageReport
	if err := d.Decode(&r); err != nil {
		return nil, bad("not a usage report: %v", err)
	}
	if _, err := d.Token(); err != io.EOF {
		return nil, bad("not a usage report: trailing data")
	}
	return &r, nil
}

// usageMessage is the byte string a report's signature covers.
func usageMessage(id string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(usageSig + "\n" + id + "\n" + hex.EncodeToString(sum[:]) + "\n")
}

// PutUsage replaces the source's cost rows by (day, provider, model) and its
// snapshots by provider, so a resend never double counts. A revoked source is
// errRevoked and a report under minUsage after the last is errUsageSoon.
func (s *Store) PutUsage(ctx context.Context, owner, subject, id string, r *UsageReport, now time.Time) (int, error) {
	tx, err := s.d.BeginWrite(ctx, s.db)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var revoked, at int64
	err = tx.QueryRowContext(ctx, `SELECT revoked_at, usage_at FROM sources WHERE subject=$1 AND id=$2`, subject, id).Scan(&revoked, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, errNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("get: %w", err)
	}
	switch {
	case revoked != 0:
		return 0, errRevoked
	case at != 0 && now.Sub(time.Unix(at, 0)) < minUsage:
		return 0, errUsageSoon
	}
	for _, c := range r.Cost {
		var usd any
		if c.CostUsd != nil {
			usd = *c.CostUsd
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO source_cost (owner, subject, source, day, provider, model, input, output, cache, cost_usd)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
ON CONFLICT(source, day, provider, model) DO UPDATE SET
  input=excluded.input, output=excluded.output, cache=excluded.cache, cost_usd=excluded.cost_usd`,
			owner, subject, id, *c.Day, *c.Provider, *c.Model,
			int64(*c.InputTokens), int64(*c.OutputTokens), int64(*c.CacheTokens), usd); err != nil {
			return 0, fmt.Errorf("cost: %w", err)
		}
	}
	for _, sn := range r.Snapshots {
		doc, err := json.Marshal(sn)
		if err != nil {
			return 0, fmt.Errorf("snapshot: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO source_snapshot (owner, subject, source, provider, json) VALUES ($1,$2,$3,$4,$5)
ON CONFLICT(source, provider) DO UPDATE SET json=excluded.json`,
			owner, subject, id, *sn.Provider, string(doc)); err != nil {
			return 0, fmt.Errorf("snapshot: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE sources SET usage_at=$1 WHERE id=$2 AND subject=$3`, now.Unix(), id, subject); err != nil {
		return 0, fmt.Errorf("usage_at: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return len(r.Cost) + len(r.Snapshots), nil
}

// UsageRow is one group of the aggregation.
type UsageRow struct {
	// Key is the source id, provider, model, day (YYYY-MM-DD) or user subject the row groups by.
	Key          string  `json:"key"`
	InputTokens  int64   `json:"inputTokens"`
	OutputTokens int64   `json:"outputTokens"`
	CacheTokens  int64   `json:"cacheTokens"`
	CostUsd      float64 `json:"costUsd"`
}

var groupColumn = map[string]string{"source": "source", "provider": "provider", "model": "model", "day": "day", "user": "subject"}

// Rows totals the caller's own cost rows between from and to (inclusive days).
// With all set it totals every person's, which only by=user may ask for.
func (s *Store) Rows(ctx context.Context, subject, by, from, to string, all bool) ([]UsageRow, error) {
	col := groupColumn[by]
	q := `SELECT ` + col + `, CAST(SUM(input) AS BIGINT), CAST(SUM(output) AS BIGINT), CAST(SUM(cache) AS BIGINT), COALESCE(SUM(cost_usd), 0)
FROM source_cost WHERE day>=$1 AND day<=$2`
	args := []any{from, to}
	if !all {
		q += ` AND subject=$3`
		args = append(args, subject)
	}
	q += ` GROUP BY ` + col + ` ORDER BY ` + col
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}
	defer rows.Close()
	out := []UsageRow{}
	for rows.Next() {
		var r UsageRow
		if err := rows.Scan(&r.Key, &r.InputTokens, &r.OutputTokens, &r.CacheTokens, &r.CostUsd); err != nil {
			return nil, fmt.Errorf("usage: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Snapshots is the subject's latest snapshot per (source, provider).
func (s *Store) Snapshots(ctx context.Context, subject string) ([]ProviderUsage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source, json FROM source_snapshot WHERE subject=$1 ORDER BY source, provider`, subject)
	if err != nil {
		return nil, fmt.Errorf("snapshots: %w", err)
	}
	defer rows.Close()
	out := []ProviderUsage{}
	for rows.Next() {
		var id, doc string
		var p ProviderUsage
		if err := rows.Scan(&id, &doc); err != nil {
			return nil, fmt.Errorf("snapshots: %w", err)
		}
		if err := json.Unmarshal([]byte(doc), &p); err != nil {
			return nil, fmt.Errorf("snapshots: %w", err)
		}
		p.SourceID = id
		out = append(out, p)
	}
	return out, rows.Err()
}

// ReportAccepted is the answer to a usage report.
type ReportAccepted struct {
	// Accepted is how many cost rows and snapshots were stored.
	Accepted int `json:"accepted"`
}

func init() {
	spec.Describe("/v1/sources/{id}/usage", http.MethodPost,
		"Report a source's usage",
		"Stores what a linked source reports about its providers: per-day tokens and cost by model, and the latest limit windows, credits and status per provider. "+
			"The body is the Dev usage model (@hanzodev/usage UsageReport: version 1, deviceId, from, to, cost, snapshots) and nothing else; a payload that breaks a rule is refused whole with 400, never trimmed. "+
			"`account` must be absent and `error` a short code. The `x-hanzo-sig` header is the source's ed25519 device-key signature, unpadded base64url, over "+
			"`hanzo-source-usage/1\\n<id>\\n<lowercase hex sha256 of the body>\\n`; a bad one is 401. A resend replaces the same day, provider and model and the same provider snapshot, so it never double counts. "+
			"At most one report per source per 10 seconds and 1 MiB. A revoked source is 410; another person's source is 404. Answers `{\"accepted\": n}`.")
}

// usage is the raw handler: the signature covers the exact body bytes and rides in
// a header, neither of which a typed op can see.
func (o ops) report(c *zip.Ctx) error {
	ctx := c.Context()
	org, subject, err := whoReq(c)
	if err != nil {
		return err
	}
	sto, err := o.open(org)
	if err != nil {
		return err
	}
	id := c.Param("id")
	r, err := sto.Get(ctx, subject, id)
	if errors.Is(err, errNotFound) {
		return zip.ErrNotFound("source not found")
	}
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	if r.RevokedAt != 0 {
		return zip.Errorf(http.StatusGone, "source revoked")
	}
	body := c.Body()
	if len(body) > maxReport {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "a usage report is at most 1 MiB")
	}
	pub, _ := b64(r.Key, ed25519.PublicKeySize)
	sig, ok := b64(c.Header("x-hanzo-sig"), ed25519.SignatureSize)
	if !ok || !ed25519.Verify(pub, usageMessage(id, body), sig) {
		return zip.ErrUnauthorized("usage signature invalid")
	}
	rep, err := decodeReport(body)
	if err != nil {
		return err
	}
	if err := rep.validate(id); err != nil {
		return err
	}
	n, err := sto.PutUsage(ctx, org, subject, id, rep, o.s.State.now())
	switch {
	case errors.Is(err, errRevoked):
		return zip.Errorf(http.StatusGone, "source revoked")
	case errors.Is(err, errUsageSoon):
		return zip.Errorf(http.StatusTooManyRequests, "usage reported too soon")
	case errors.Is(err, errNotFound):
		return zip.ErrNotFound("source not found")
	case err != nil:
		return zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	return c.JSON(http.StatusOK, ReportAccepted{Accepted: n})
}

// UsageQuery selects an aggregation.
type UsageQuery struct {
	// By groups the rows: `source`, `provider`, `model`, `day` or `user`.
	By string `query:"by" json:"by"`
	// From is the first day included, YYYY-MM-DD. Optional.
	From string `query:"from" json:"from"`
	// To is the last day included, YYYY-MM-DD. Optional.
	To string `query:"to" json:"to"`
}

// UsageView is the aggregation answer.
type UsageView struct {
	// Rows are the totals per key.
	Rows []UsageRow `json:"rows"`
	// Snapshots are the caller's own latest snapshot per source and provider.
	Snapshots []ProviderUsage `json:"snapshots"`
}

func day(name, v, def string) (string, error) {
	if v == "" {
		return def, nil
	}
	if len(v) > 10 {
		v = v[:10]
	}
	if _, err := time.Parse("2006-01-02", v); err != nil {
		return "", zip.ErrBadRequest(name + " must be YYYY-MM-DD")
	}
	return v, nil
}

// SourceUsage totals the signed-in person's own reported usage by source,
// provider, model, day or user between two days, with their latest snapshot per
// source and provider. An org admin asking by=user gets every person's totals in
// the org (key is the person's subject) and nothing else of theirs: no source,
// snapshot, plan or label of another person is readable, and a SuperAdmin has no
// extra read. For anyone else by=user is their own single row.
//
// Example: {"by": "provider", "from": "2026-11-01", "to": "2026-11-30"}
func (o ops) usage(ctx context.Context, in *UsageQuery) (*UsageView, error) {
	sto, org, subject, err := o.store(ctx)
	if err != nil {
		return nil, err
	}
	if _, ok := groupColumn[in.By]; !ok {
		return nil, zip.ErrBadRequest("by must be source, provider, model, day or user")
	}
	from, err := day("from", in.From, "0000-01-01")
	if err != nil {
		return nil, err
	}
	to, err := day("to", in.To, "9999-12-31")
	if err != nil {
		return nil, err
	}
	rows, err := sto.Rows(ctx, subject, in.By, from, to, in.By == "user" && orgAdmin(ctx, org))
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	snaps, err := sto.Snapshots(ctx, subject)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "source: %v", err)
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return &UsageView{Rows: rows, Snapshots: snaps}, nil
}

// orgAdmin reports an org admin acting in their own home org. A SuperAdmin acting
// in another org is not one: the home org must be the acted one.
func orgAdmin(ctx context.Context, org string) bool {
	c, ok := cloud.Request(ctx)
	if !ok {
		return false
	}
	return principal.IsOrgAdmin(c) && principal.Owner(c) == org
}
