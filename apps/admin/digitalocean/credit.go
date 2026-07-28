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

package digitalocean

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hanzoai/cloud/apps/admin/money"
)

// CreditIssued reports the TOTAL promotional credit DigitalOcean has ever applied
// to this account, in cents, discovered from DO's own invoices.
//
// WHY THIS IS DISCOVERED AND NOT DECLARED. The grant used to be a constant in
// finance/providers.go — `"do-ai": 2_600_000`. It was wrong, and being wrong is
// the normal state of a hand-entered number: nobody re-types it when the vendor
// applies another tranche or lets one expire. On 2026-07-28 the constant said
// $26,000, the operator believed $50,000, and DO's ledger showed $21,263.65 ever
// applied. Three numbers, no two agreeing, and the one nobody could check was the
// one the dashboard rendered.
//
// DO knows this exactly, so ask DO. Every invoice carries the credit it consumed
// as a line item with product == "Credits" (ours read `Hatch Credit for:
// Techstars`), NEGATIVE because it offsets usage. Their absolute sum is the credit
// that has actually flowed. If DO applies the missing tranche tomorrow, this
// number moves on its own and no one has to remember.
//
// NOT the wallet. Cash top-ups (DO "Payment" rows — ours were two Apple Pay
// entries totalling $4.00) are NOT credit and are deliberately excluded: mixing
// them in is what makes an exhausted grant look alive. Account balance already
// counts them; this counts only the promotional grant.
//
// Cost: one invoice-list call plus one detail call per invoice. Callers should
// cache — the value changes at most once a month.
func (c *Client) CreditIssued(ctx context.Context) (money.Cents, error) {
	if !c.Ready() {
		return 0, fmt.Errorf("DO_API_TOKEN not configured")
	}
	body, err := c.get(ctx, "/v2/customers/my/invoices?per_page=200")
	if err != nil {
		return 0, err
	}
	var list struct {
		Invoices []struct {
			UUID string `json:"invoice_uuid"`
		} `json:"invoices"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return 0, fmt.Errorf("do invoices decode: %w", err)
	}

	var total money.Cents
	for _, inv := range list.Invoices {
		if inv.UUID == "" {
			continue
		}
		// per_page is required: DO pages invoice items at 20 by default, and a
		// truncated page silently under-reports the credit.
		ib, err := c.get(ctx, "/v2/customers/my/invoices/"+inv.UUID+"?per_page=500")
		if err != nil {
			// One unreadable invoice must not fabricate a smaller grant, which would
			// read as "we have more headroom than we do". Fail the whole answer.
			return 0, fmt.Errorf("do invoice %s: %w", inv.UUID, err)
		}
		var det struct {
			Items []struct {
				Product string `json:"product"`
				Amount  string `json:"amount"`
			} `json:"invoice_items"`
		}
		if err := json.Unmarshal(ib, &det); err != nil {
			return 0, fmt.Errorf("do invoice %s decode: %w", inv.UUID, err)
		}
		for _, it := range det.Items {
			if it.Product != "Credits" {
				continue
			}
			if v := dollarsToCents(it.Amount); v < 0 {
				total += -v
			}
		}
	}
	return total, nil
}
