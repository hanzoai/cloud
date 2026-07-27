// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package core

// warehouse — the ONE-copy datastore-read kernel the billing FLEET views
// (metrics/invoices/subscriptions) compose. They read commerce.events — the
// single warehouse table the commerce analytics collector lands every
// customer-activity event in (subscription/invoice/usage lifecycle) — over the
// SAME shared client (datastore.Query) the o11y/compute/analytics lenses
// already use, no second connection. This mirrors compute.go's row-coercers and
// EXISTS-TABLE probe, hoisted here so the three sibling domains share ONE copy
// instead of each re-deriving it (DRY; the admin-package o11y/compute keep their
// own private copies as the read template).
//
// Every read is honest by construction: no datastore connected, or the events
// table not provisioned (the emitter is still being wired) → the real empty
// aggregate, NEVER a fabricated fleet. admin READS only; it owns and creates NO
// table (the collector owns commerce.events). Time bounds are POSITIONAL
// parameters (never interpolated) so the reads are injection-safe; money is USD
// cents; timestamps are RFC3339.

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/hanzoai/cloud/clients/datastore"
)

// BillingEventsTable is the collector-owned warehouse table the commerce
// customer-activity emitters land in (events/client.go → analytics-collector →
// commerce.events). admin only READS it (never creates it — the collector owns
// its writes), exactly as o11y reads hanzo.cloud_usage.
const BillingEventsTable = "commerce.events"

// Canonical customer-activity event names — the CONTRACT with the commerce
// emitters (events/client.go). These are server-side constants (never user
// input), so rendering them into an IN (...) list is injection-safe.
const (
	EvSubscriptionCreated     = "subscription_created"
	EvSubscriptionRenewed     = "subscription_renewed"
	EvSubscriptionPlanChanged = "subscription_plan_changed"
	EvSubscriptionCanceled    = "subscription_canceled"
	EvInvoiceFinalized        = "invoice_finalized"
	EvInvoicePaid             = "invoice_paid"
	EvInvoiceVoid             = "invoice_void"
	EvAPIUsageDebit           = "api_usage_debit"
)

// SubscriptionEvents / InvoiceEvents are the lifecycle sets each fleet view
// folds over (latest-event-wins per entity). Closed server-side constants.
var (
	SubscriptionEvents = []string{EvSubscriptionCreated, EvSubscriptionRenewed, EvSubscriptionPlanChanged, EvSubscriptionCanceled}
	InvoiceEvents      = []string{EvInvoiceFinalized, EvInvoicePaid, EvInvoiceVoid}
)

// WarehouseReady reports whether the shared datastore ledger is connected, the
// gate every fleet read checks first (honest-empty when false).
func WarehouseReady() bool { return datastore.Ready() }

// BillingEventsReady reports whether the warehouse is connected AND the
// collector's commerce.events table is provisioned — the two-part gate every
// billing fleet view opens with, so an unwired collector degrades to an honest
// empty aggregate rather than an error.
func BillingEventsReady(ctx context.Context) bool {
	return datastore.Ready() && CHTableExists(ctx, BillingEventsTable)
}

// CHTableExists probes the datastore for a table's presence. The name is a
// package constant (never user input), so EXISTS TABLE is safe. Any error →
// false (honest "not available yet"), mirroring compute.computeTableExists.
func CHTableExists(ctx context.Context, qualified string) bool {
	rows, err := datastore.Query(ctx, "EXISTS TABLE "+qualified)
	if err != nil || len(rows) == 0 {
		return false
	}
	for _, v := range rows[0] {
		return CHInt64(v) == 1
	}
	return false
}

// SQLInList renders a set of server-side-constant strings as a datastore string
// list ('a','b',…) for an IN (...) clause. ONLY for closed constant sets (the
// event-name enums above) — never for user input; positional args carry all
// caller-derived values.
func SQLInList(vals []string) string {
	quoted := make([]string, len(vals))
	for i, v := range vals {
		quoted[i] = "'" + v + "'"
	}
	return strings.Join(quoted, ",")
}

// WarehouseSince maps the ?range enum (24h|7d|30d, default 30d) to a lower time
// bound, mirroring compute.computeSince so the fleet views share ONE window
// grammar.
func WarehouseSince(rangeLabel string) time.Time {
	now := time.Now().UTC()
	switch strings.TrimSpace(rangeLabel) {
	case "24h":
		return now.Add(-24 * time.Hour)
	case "7d":
		return now.Add(-7 * 24 * time.Hour)
	default:
		return now.Add(-30 * 24 * time.Hour)
	}
}

// CHTimeLit formats a time as a datastore DateTime literal (UTC), bound as a
// POSITIONAL string arg (never interpolated).
func CHTimeLit(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// CHFirstRow returns the first row or an empty map (never nil), so a parser
// reads honest zeros from an empty result instead of panicking.
func CHFirstRow(rows []map[string]any) map[string]any {
	if len(rows) == 0 {
		return map[string]any{}
	}
	return rows[0]
}

// ── map[string]any coercers (the DatastoreQuery row shape) ───────────────────
//
// The datastore driver decodes each column to its native Go type (uint64 for
// count()/sum(UInt*), float64 for round()/JSON numerics, time.Time for DateTime,
// string for String); these accept those natives so a driver/transport change
// can't crash a read. Twins of the admin-package compute.go coercers.

func CHInt64(v any) int64 {
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case int32:
		return int64(n)
	case uint:
		return int64(n)
	case uint64:
		return int64(n)
	case uint32:
		return int64(n)
	case uint16:
		return int64(n)
	case uint8:
		return int64(n)
	case float64:
		return int64(n)
	case float32:
		return int64(n)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0
		}
		return int64(f)
	default:
		return 0
	}
}

func CHFloat64(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case int32:
		return float64(n)
	case uint64:
		return float64(n)
	case uint32:
		return float64(n)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		if err != nil {
			return 0
		}
		return f
	default:
		return 0
	}
}

func CHStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// CHTime coerces a datastore DateTime (time.Time) to an RFC3339 UTC string.
func CHTime(v any) string {
	switch t := v.(type) {
	case time.Time:
		return t.UTC().Format(time.RFC3339)
	case string:
		return t
	default:
		return ""
	}
}
