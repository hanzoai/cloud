package email

// ledger.go — every message the org's mail carries, and what became of it, in the
// org's own Base.
//
// Only the base app opens a Base, so this writes over the plane (base_import,
// base_upsert, base_get, base_list) acting for the org, exactly as apps/crm
// base.go does, and the base process stays the single writer. A record survives a
// restart and reads the same from every replica.
//
// Three collections, grown into the org's Base on first use:
//
//	email_messages  one record per message: ours (POST /v1/email) and every message a
//	                provider tells us of, sent from anywhere on the org's account.
//	email_events    one record per provider event, keyed by the provider's own id
//	                for it, so a delivery replayed is still one record.
//	email_tallies   the counts the stats read, per hour and per day, per provider,
//	                for all mail and per campaign. A stats read is a few hundred
//	                rows whatever the volume: nothing scans the messages.
//
// IDS ARE DERIVED. A record's id is 15 hex characters of the SHA-256 of what it is
// (the shape Base takes), so a message is found by its own id, an event by the
// provider's, and a tally by its bucket, each with one read and no index.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	basepeer "github.com/hanzoai/cloud/client/base"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// The collections, by name.
const (
	messages = "email_messages"
	events   = "email_events"
	tallies  = "email_tallies"
)

// schema is the three collections in the shape Base's import takes. The indexes are
// the reads: a page of messages newest first, narrowed by status, provider or
// recipient, and the tallies of one span over a range of time, all or per campaign.
const schema = `[
 {"name": "email_messages", "type": "base", "fields": [
  {"name": "message", "type": "text"},
  {"name": "key", "type": "text"},
  {"name": "origin", "type": "text"},
  {"name": "provider", "type": "text"},
  {"name": "status", "type": "text"},
  {"name": "from", "type": "text"},
  {"name": "to", "type": "json"},
  {"name": "recipient", "type": "text"},
  {"name": "subject", "type": "text"},
  {"name": "campaign", "type": "text"},
  {"name": "tags", "type": "json"},
  {"name": "error", "type": "text"},
  {"name": "idempotency", "type": "text"},
  {"name": "bounce", "type": "text"},
  {"name": "at", "type": "date"},
  {"name": "delivered", "type": "date"},
  {"name": "opened", "type": "date"},
  {"name": "clicked", "type": "date"},
  {"name": "bounced", "type": "date"},
  {"name": "complained", "type": "date"},
  {"name": "unsubscribed", "type": "date"}],
  "indexes": [
  "CREATE INDEX idx_email_messages_at ON email_messages (at, id)",
  "CREATE INDEX idx_email_messages_status ON email_messages (status, at, id)",
  "CREATE INDEX idx_email_messages_provider ON email_messages (provider, at, id)",
  "CREATE INDEX idx_email_messages_recipient ON email_messages (recipient, at, id)"]},
 {"name": "email_events", "type": "base", "fields": [
  {"name": "event", "type": "text"},
  {"name": "provider", "type": "text"},
  {"name": "key", "type": "text"},
  {"name": "message", "type": "text"},
  {"name": "campaign", "type": "text"},
  {"name": "tags", "type": "json"},
  {"name": "link", "type": "text"},
  {"name": "bounce", "type": "text"},
  {"name": "reason", "type": "text"},
  {"name": "recipient", "type": "text"},
  {"name": "person", "type": "text"},
  {"name": "at", "type": "date"},
  {"name": "settled", "type": "bool"}]},
 {"name": "email_tallies", "type": "base", "fields": [
  {"name": "span", "type": "text"},
  {"name": "at", "type": "date"},
  {"name": "provider", "type": "text"},
  {"name": "campaign", "type": "text"},
  {"name": "sent", "type": "number"},
  {"name": "delivered", "type": "number"},
  {"name": "opened", "type": "number"},
  {"name": "clicked", "type": "number"},
  {"name": "bounced", "type": "number"},
  {"name": "complained", "type": "number"},
  {"name": "unsubscribed", "type": "number"}],
  "indexes": [
  "CREATE INDEX idx_email_tallies_campaign ON email_tallies (span, campaign, at)",
  "CREATE INDEX idx_email_tallies_at ON email_tallies (span, at)"]}
]`

// The plane, variables so a test can see what crosses it. Only a test reassigns them.
var (
	importFn = basepeer.BaseImport
	upsertFn = basepeer.BaseUpsert
	getFn    = basepeer.BaseGet
	listFn   = basepeer.BaseList
)

// maxUpsert is the most records one base_upsert writes, and maxIDs the most ids one
// read names.
const (
	maxUpsert = 500
	maxIDs    = 100
)

// grown is every org whose Base this process has grown by the schema.
var grown sync.Map

// ensure grows org's Base by the three collections once per process: what is
// missing is created and nothing that exists is changed, so it is safe on every
// first use and a field an org added survives it.
func ensure(ctx context.Context, org string) error {
	if _, ok := grown.Load(org); ok {
		return nil
	}
	if _, err := importFn(plane.For(ctx, org), &client.BaseImportIn{Collections: json.RawMessage(schema)}); err != nil {
		return fmt.Errorf("email: grow the org's Base: %w", err)
	}
	grown.Store(org, true)
	return nil
}

// rid is a Base record id derived from what the record is.
func rid(kind, key string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + key))
	return hex.EncodeToString(sum[:])[:15]
}

// stamp is a time as Base stores a date.
func stamp(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05.000Z") }

// unstamp reads a Base date, the zero time for none.
func unstamp(s string) time.Time {
	t, _ := time.Parse("2006-01-02 15:04:05.000Z", strings.TrimSpace(s))
	return t
}

// digest is an address as the ledger finds it without holding it in an index: the
// SHA-256 of its trimmed, lower-cased form, the shape the event plane's `em` takes.
func digest(address string) string {
	address = strings.ToLower(strings.TrimSpace(address))
	if address == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(address))
	return hex.EncodeToString(sum[:])
}

// stored is one message record as Base holds it.
type stored struct {
	ID           string   `json:"id"`
	Message      string   `json:"message"`
	Key          string   `json:"key"`
	Origin       string   `json:"origin"`
	Provider     string   `json:"provider"`
	Status       string   `json:"status"`
	From         string   `json:"from"`
	To           []string `json:"to"`
	Recipient    string   `json:"recipient"`
	Subject      string   `json:"subject"`
	Campaign     string   `json:"campaign"`
	Tags         []string `json:"tags"`
	Error        string   `json:"error"`
	Idempotency  string   `json:"idempotency"`
	Bounce       string   `json:"bounce"`
	At           string   `json:"at"`
	Delivered    string   `json:"delivered"`
	Opened       string   `json:"opened"`
	Clicked      string   `json:"clicked"`
	Bounced      string   `json:"bounced"`
	Complained   string   `json:"complained"`
	Unsubscribed string   `json:"unsubscribed"`
}

// status is what a message's record says became of it: the furthest thing that
// happened to it, and a bounce or a complaint over anything before. A message
// nothing has happened to keeps the status its send gave it.
func (m *stored) status() DeliveryStatus {
	switch {
	case m.Complained != "":
		return StatusComplained
	case m.Bounced != "":
		return StatusBounced
	case m.Clicked != "":
		return StatusClicked
	case m.Opened != "":
		return StatusOpened
	case m.Delivered != "":
		return StatusDelivered
	}
	return DeliveryStatus(m.Status)
}

// record is a stored message as the API answers it.
func (m *stored) record(org string) *MessageRecord {
	r := &MessageRecord{
		ID: m.Message, Org: org, Status: m.status(), Origin: m.Origin, From: m.From, To: m.To,
		Subject: m.Subject, Provider: m.Provider, Campaign: m.Campaign, Error: m.Error,
		IdempotencyKey: m.Idempotency, Bounce: m.Bounce, CreatedAt: unstamp(m.At),
	}
	if r.To == nil {
		r.To = []string{}
	}
	if _, pid, ok := strings.Cut(m.Key, ":"); ok {
		r.ProviderID = pid
	}
	for _, t := range []struct {
		at  string
		out **time.Time
	}{
		{m.Delivered, &r.DeliveredAt}, {m.Opened, &r.OpenedAt}, {m.Clicked, &r.ClickedAt},
		{m.Bounced, &r.BouncedAt}, {m.Complained, &r.ComplainedAt}, {m.Unsubscribed, &r.UnsubscribedAt},
	} {
		if at := unstamp(t.at); !at.IsZero() {
			*t.out = &at
		}
	}
	return r
}

// upsert writes records into one collection, maxUpsert at a time, and answers
// whether each was created, in the order given.
func upsert(ctx context.Context, org, source, collection string, records []map[string]any) ([]bool, error) {
	created := make([]bool, 0, len(records))
	for start := 0; start < len(records); start += maxUpsert {
		end := min(start+maxUpsert, len(records))
		raw := make([]json.RawMessage, 0, end-start)
		for _, r := range records[start:end] {
			b, err := json.Marshal(r)
			if err != nil {
				return nil, err
			}
			raw = append(raw, b)
		}
		out, err := upsertFn(plane.For(ctx, org), &client.BaseUpsertIn{Collection: collection, Source: source, Records: raw})
		if err != nil {
			return nil, fmt.Errorf("email: write %s: %w", collection, err)
		}
		for _, w := range out.Records {
			created = append(created, w.Created)
		}
	}
	return created, nil
}

// load reads the records of collection the ids name, maxIDs at a time, each into a
// fresh T, keyed by id. An id it holds no record for is absent. The ids are ours
// (rid), lower-case hex, so they are spelled into the filter as they are.
func load[T any](ctx context.Context, org, collection, also string, ids []string) (map[string]*T, error) {
	out := make(map[string]*T, len(ids))
	for start := 0; start < len(ids); start += maxIDs {
		end := min(start+maxIDs, len(ids))
		terms := make([]string, 0, end-start)
		for _, id := range ids[start:end] {
			terms = append(terms, `id = "`+id+`"`)
		}
		filter := "(" + strings.Join(terms, " || ") + ")"
		if also != "" {
			filter += " && " + also
		}
		page, err := listFn(plane.For(ctx, org), &client.BaseListIn{Collection: collection, Filter: filter, PerPage: end - start, SkipTotal: true})
		if err != nil {
			return nil, fmt.Errorf("email: read %s: %w", collection, err)
		}
		for _, raw := range page.Items {
			v := new(T)
			if err := json.Unmarshal(raw, v); err != nil {
				return nil, err
			}
			var id struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(raw, &id)
			out[id.ID] = v
		}
	}
	return out, nil
}

// fetch reads one message by its record id, nil when there is none.
func fetch(ctx context.Context, org, id string) (*stored, error) {
	out, err := getFn(plane.For(ctx, org), &client.BaseGetIn{Collection: messages, ID: id})
	if err != nil {
		var he *zip.HTTPError
		if errors.As(err, &he) && he.Status == http.StatusNotFound && strings.Contains(he.Error(), "no record") {
			return nil, nil
		}
		return nil, fmt.Errorf("email: read a message: %w", err)
	}
	m := &stored{}
	if err := json.Unmarshal(out.Record, m); err != nil {
		return nil, err
	}
	return m, nil
}

// ---- tallies ----

// The spans a tally is kept over.
const (
	spanHour = "hour"
	spanDay  = "day"
)

// counters are the counts a tally keeps, in the order the API answers them.
var counters = []string{"sent", "delivered", "opened", "clicked", "bounced", "complained", "unsubscribed"}

// bucket is where one count lands: the start of its hour or day, a provider, and a
// campaign ("" is all mail).
type bucket struct {
	span, provider, campaign string
	at                       time.Time
}

func (b bucket) id() string {
	return rid("tally", b.span+"|"+stamp(b.at)+"|"+b.provider+"|"+b.campaign)
}

// counts is a batch of increments, by bucket and counter.
type counts map[bucket]map[string]int

// add counts n of counter at t for provider and campaign: once in the hour and once
// in the day for all mail, and again for the campaign when there is one.
func (c counts) add(counter, provider, campaign string, t time.Time, n int) {
	t = t.UTC()
	campaigns := []string{""}
	if campaign != "" {
		campaigns = append(campaigns, campaign)
	}
	for _, span := range []string{spanHour, spanDay} {
		at := t.Truncate(time.Hour)
		if span == spanDay {
			at = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
		}
		for _, cp := range campaigns {
			b := bucket{span: span, provider: provider, campaign: cp, at: at}
			if c[b] == nil {
				c[b] = map[string]int{}
			}
			c[b][counter] += n
		}
	}
}

// write adds the batch to the org's tallies in one transaction: each record names
// its counters as "<counter>+", which Base adds to what the record holds inside the
// single writer, so two batches never lose each other's counts.
func (c counts) write(ctx context.Context, org string) error {
	if len(c) == 0 {
		return nil
	}
	records := make([]map[string]any, 0, len(c))
	for b, n := range c {
		r := map[string]any{"id": b.id(), "span": b.span, "at": stamp(b.at), "provider": b.provider, "campaign": b.campaign}
		for k, v := range n {
			r[k+"+"] = v
		}
		records = append(records, r)
	}
	_, err := upsert(ctx, org, "webhook", tallies, records)
	return err
}

// tally is one tally record as Base holds it.
type tally struct {
	At           string  `json:"at"`
	Provider     string  `json:"provider"`
	Campaign     string  `json:"campaign"`
	Sent         float64 `json:"sent"`
	Delivered    float64 `json:"delivered"`
	Opened       float64 `json:"opened"`
	Clicked      float64 `json:"clicked"`
	Bounced      float64 `json:"bounced"`
	Complained   float64 `json:"complained"`
	Unsubscribed float64 `json:"unsubscribed"`
}

// maxTallyPages bounds one stats read: a year of days for two providers and many
// campaigns is well inside it.
const maxTallyPages = 200

// readTallies is every tally filter matches, oldest first.
func readTallies(ctx context.Context, org, filter string) ([]tally, error) {
	var out []tally
	for page := 1; ; page++ {
		if page > maxTallyPages {
			return nil, zip.Errorf(http.StatusUnprocessableEntity, "email: the range holds more than %d tallies; ask for a shorter one", maxTallyPages*1000)
		}
		got, err := listFn(plane.For(ctx, org), &client.BaseListIn{
			Collection: tallies, Filter: filter, Sort: "at,id", Page: page, PerPage: 1000, SkipTotal: true,
		})
		if err != nil {
			return nil, fmt.Errorf("email: read tallies: %w", err)
		}
		for _, raw := range got.Items {
			var t tally
			if err := json.Unmarshal(raw, &t); err != nil {
				return nil, err
			}
			out = append(out, t)
		}
		if len(got.Items) < 1000 {
			return out, nil
		}
	}
}

// add puts a tally record's counts into t.
func (t *Tally) add(r tally) {
	t.Sent += int64(r.Sent)
	t.Delivered += int64(r.Delivered)
	t.Opened += int64(r.Opened)
	t.Clicked += int64(r.Clicked)
	t.Bounced += int64(r.Bounced)
	t.Complained += int64(r.Complained)
	t.Unsubscribed += int64(r.Unsubscribed)
}
