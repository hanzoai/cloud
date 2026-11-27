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

package limits

// entitle.go — the plan entitlements (hanzo-inc/cloud#803). Claude Fable 5.2 and
// fast mode cost well above the rest of their kind, so a plan serves them only when
// it includes them by name: the usage policy's switch for that plan
// (flags.EntitlementKey), shipped on for the plans flags.SeedEntitled lists. Which
// requests need one is the model's capability record (apps/ai/capability:
// Record.Entitlement, by every name the model answers to) and the request's own ask
// for fast mode. A request that needs one its payer's plan does not include is refused
// before any provider is asked, whatever the payer holds; a payer with no plan holds
// none.

import (
	"cmp"
	"context"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/apps/ai/capability"
	"github.com/hanzoai/cloud/apps/flags"
	"github.com/hanzoai/cloud/client"
)

// needs are the entitlements q needs its plan to include, in the order the policy
// lists them. A request with no list price needs none: nothing is billed for it.
func needs(q Ask) []string {
	if !q.Priced {
		return nil
	}
	var out []string
	if r, ok := capability.Lookup(q.Model); ok && r.Entitlement != "" {
		out = append(out, r.Entitlement)
	}
	if q.Fast {
		out = append(out, flags.EntitleFast)
	}
	return out
}

// includes reports whether plan p includes the entitlement name: its served rung's
// switch as the usage policy holds it, the shipped value where nothing is saved. No
// plan includes nothing.
func (l *Limiter) includes(ctx context.Context, org string, p plan, name string) bool {
	return p.counts() && l.on(ctx, org, flags.EntitlementKey(name, p.id), flags.Entitled(name, p.id))
}

// Entitlement is one plan entitlement as a payer reads it: whether its plan includes
// it, and, when it does not, the plan a refusal would name.
type Entitlement struct {
	Included bool
	// Upgrade is the cheapest plan on sale that includes it (entitledWay), zero when
	// the plan already does or none on sale does.
	Upgrade Way
}

// entitlements is every entitlement as plan p stands on it, asked the way entitle
// asks: includes for the plan, entitledWay for the way on.
func (l *Limiter) entitlements(ctx context.Context, org string, p plan) map[string]Entitlement {
	out := make(map[string]Entitlement, len(flags.Entitlements))
	for _, name := range flags.Entitlements {
		if l.includes(ctx, org, p, name) {
			out[name] = Entitlement{Included: true}
			continue
		}
		out[name] = Entitlement{Upgrade: l.entitledWay(ctx, org, p.id, name)}
	}
	return out
}

// entitle is the refusal for a request q, read for the plan p of its holder, that
// needs an entitlement p does not include; nil when p includes every one it needs.
func (l *Limiter) entitle(ctx context.Context, q Ask, p plan, class string) *Hit {
	for _, name := range needs(q) {
		if l.includes(ctx, q.Org, p, name) {
			continue
		}
		way := l.entitledWay(ctx, q.Org, p.id, name)
		msg := flags.EntitlementName[name] + " needs a plan that includes the " + name + " entitlement"
		if p.counts() {
			msg += ", and yours does not"
		}
		if way.Name != "" {
			msg += "; " + way.Name + " includes it"
		}
		hit := &Hit{Code: CodeEntitlement, Name: name, Class: class, Upgrade: way.Plan, Message: msg + ":"}
		if name != flags.EntitleFast {
			hit.Model = q.Model
		}
		return hit
	}
	return nil
}

// Entitle answers only the entitlement question Admit asks first: the refusal for a
// request whose model or mode needs an entitlement its payer's plan does not include,
// nil when it needs none or the plan includes them. It counts nothing. The plane's
// door asks it for a sibling's model call, which its sibling pays for and no plan
// counts (apps/ai buy). A payer it cannot name holds no plan.
func (l *Limiter) Entitle(ctx context.Context, q Ask) (*Hit, error) {
	if len(needs(q)) == 0 {
		return nil, nil
	}
	var p plan
	if strings.TrimSpace(q.Subject) != "" {
		var err error
		if q, p, err = l.holder(ctx, q); err != nil {
			return nil, err
		}
	}
	return l.entitle(ctx, q, p, classOf(q, Rule{})), nil
}

// entitledWay is the plan a refusal names for the entitlement name: the cheapest on
// sale, by itself and not by quote, that the usage policy names and that includes
// it — priced above the payer's own plan in its category where one is, else in any
// category. Zero when none is or the catalog cannot be read: the way on is advice,
// never a reason to refuse.
func (l *Limiter) entitledWay(ctx context.Context, org, id, name string) Way {
	return l.way(ctx, "entitle\x00"+name+"\x00"+id, func(rows []client.PlanRow) Way {
		var from *client.PlanRow
		if i := slices.IndexFunc(rows, func(r client.PlanRow) bool { return r.Slug == id }); id != "" && i >= 0 {
			from = &rows[i]
		}
		var all, above []client.PlanRow
		for _, r := range rows {
			if r.Status != "active" || r.ContactSales || r.PerSeat || r.Price <= 0 || r.Slug == id {
				continue
			}
			if !l.on(ctx, org, flags.EntitlementKey(name, r.Slug), flags.Entitled(name, r.Slug)) || !l.named(ctx, org, r.Slug) {
				continue
			}
			all = append(all, r)
			if from != nil && r.Category == from.Category && r.Price > from.Price {
				above = append(above, r)
			}
		}
		on := all
		if len(above) > 0 {
			on = above
		}
		if len(on) == 0 {
			return Way{}
		}
		best := slices.MinFunc(on, func(a, b client.PlanRow) int {
			return cmp.Or(cmp.Compare(a.Price, b.Price), strings.Compare(a.Slug, b.Slug))
		})
		return Way{Plan: best.Slug, Name: best.Name}
	})
}
