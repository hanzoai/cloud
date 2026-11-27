// Copyright 2026 Hanzo AI Inc. All Rights Reserved.

package ai

// drained.go — one of our vendor accounts cannot pay, on the bus.
//
// ai says so once an account answers 402, or its vendor's own word for an
// account out of credit, and again each ten minutes while it stays so (hanzoai/ai
// controllers/failover.go `emptied`): an OpenRouter key, an OpenAI or Anthropic
// key, or a provider row. The host puts it on the SuperAdmin's pulse
// (bus.Announce), which GET /v1/admin/feed streams to admin.hanzo.ai as an
// incident, and files it on the audit trail, which admin.hanzo.ai keeps: the
// account is named, never its key, so whoever reads it knows which one to fund at
// the provider key ref in KMS.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/hanzoai/cloud/sdk/audit"
	"github.com/hanzoai/cloud/sdk/bus"
	"github.com/hanzoai/cloud/sdk/supply"
	aiobject "github.com/hanzoai/ai/object"
)

// drained puts a drained account where a SuperAdmin sees it: a row on the audit
// trail (admin.hanzo.ai Audit, which keeps it) and a supply pulse on the live feed.
// Best effort: the request it refused already failed over, so what failed to say
// it is logged and nothing else.
func drained(trail *audit.Recorder) func(context.Context, aiobject.Drained) {
	return func(ctx context.Context, d aiobject.Drained) {
		// A drained account is a provider budget stop on the model_provider layer,
		// counted on the same series as every other layer's (sdk/supply), so ops
		// reads one metric for "which budget stopped". It is said below, once.
		supply.Count(ctx, d.Vendor, supply.Model)
		if trail != nil {
			if _, err := trail.Append(ctx, drainedRecord(d)); err != nil {
				slog.Warn("ai: a drained account was not audited", "vendor", d.Vendor, "account", d.Account, "err", err)
			}
		}
		if err := bus.Announce(drainedPulse(d)); err != nil {
			slog.Warn("ai: a drained account was not put on the pulse", "vendor", d.Vendor, "account", d.Account, "err", err)
		}
	}
}

// drainedRecord is a drained account as an audit row: the platform's account with
// the vendor, refused for want of credit.
func drainedRecord(d aiobject.Drained) audit.Record {
	detail, _ := json.Marshal(map[string]any{"vendor": d.Vendor, "account": d.Account, "model": d.Model, "status": d.Status})
	return audit.Record{
		Actor:    audit.Actor{Org: "admin", Sub: "ai"},
		Action:   "ai.supply.drained",
		Resource: audit.Resource{Type: "ai_vendor_account", ID: d.Vendor + "/" + d.Account},
		Auth:     audit.AuthContext{Method: "service"},
		Outcome:  audit.Outcome{Result: "error", Status: d.Status, Reason: "the account is out of credit"},
		After:    audit.Redact(detail),
	}
}

// drainedPulse is the pulse a drained account reads as.
func drainedPulse(d aiobject.Drained) bus.Pulse {
	account := d.Account
	if account == "" {
		account = "every account"
	}
	detail := fmt.Sprintf("%s %s answered %d: top it up or replace the provider key ref", d.Vendor, account, d.Status)
	if d.Model != "" {
		detail += "; it was serving " + d.Model
	}
	at := d.At
	if at.IsZero() {
		at = time.Now()
	}
	return bus.Pulse{
		Kind:   "supply",
		Key:    d.Vendor + "/" + d.Account,
		At:     at.UTC().Format(time.RFC3339),
		Title:  fmt.Sprintf("%s account out of credit", d.Vendor),
		Detail: detail,
		Org:    "admin",
	}
}
