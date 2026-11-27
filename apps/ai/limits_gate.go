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

package ai

import (
	"context"
	"log/slog"
	"time"

	"github.com/hanzoai/cloud/apps/ai/limits"
	"github.com/hanzoai/cloud/apps/flags"
	flagsplane "github.com/hanzoai/cloud/client/flag"
	"github.com/hanzoai/cloud/sdk"
	aictl "github.com/hanzoai/ai/controllers"
	aiobject "github.com/hanzoai/ai/object"
)

// settleTimeout bounds one plan settle. It runs when a call's usage is recorded,
// after the request that held it may be gone, so it has its own clock.
const settleTimeout = 5 * time.Second

// limitAsk is the ai module's usage-policy hook, answered by the limiter: who pays
// for one request, its class and where the class stands; a covered request carries
// what a Hanzo family may spend and a settle that records what it used; a refusal
// carries its code, class and sentence; anything the policy has no say in is left to
// the free allowance and the wallet.
func limitAsk(lim *limits.Limiter) aiobject.LimitFunc {
	return func(ctx context.Context, q aiobject.LimitAsk) (*aiobject.LimitGrant, *aiobject.LimitHit, error) {
		if hit, err := lim.Key(ctx, q.Model); err != nil {
			return nil, nil, err
		} else if hit != nil {
			return nil, &aiobject.LimitHit{Code: hit.Code, Name: hit.Name, Message: hit.Message, ResetsAt: hit.ResetsAt, Model: hit.Model}, nil
		}
		g, hit, err := lim.Admit(ctx, limits.Ask{
			Subject: q.Subject, Org: q.Namespace, Actor: q.Actor, Model: q.Model, Family: q.Family,
			Class: q.Class, Priced: q.Priced, Apps: q.Apps, Spend: q.Spend, Session: q.Session, Past: q.Past,
			Fast: q.Spend && fast(ctx),
		})
		switch {
		case err != nil:
			return nil, nil, err
		case hit != nil:
			return nil, &aiobject.LimitHit{Code: hit.Code, Name: hit.Name, Class: hit.Class, Message: hit.Message,
				ResetsAt: hit.ResetsAt, Upgrade: hit.Upgrade, Model: hit.Model, Fallback: hit.Fallback}, nil
		case g == nil:
			return nil, nil, nil
		}
		out := &aiobject.LimitGrant{Plan: g.Plan, Pays: g.Pays, Class: g.Class, State: g.State, Cash: g.Cash, Spend: g.Spend,
			Settle: func(nanos int64) {
				ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
				defer cancel()
				if err := lim.Settle(ctx, g, nanos); err != nil {
					slog.Error("ai plan: settle failed; the hold lapses", "org", q.Namespace, "class", g.Class, "nanos", nanos, "err", err)
				}
			}}
		if g.Counted() {
			out.Release = func() {
				ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
				defer cancel()
				if err := lim.Release(ctx, g); err != nil {
					slog.Error("ai plan: an unserved request could not be released", "org", q.Namespace, "class", g.Class, "err", err)
				}
			}
		}
		return out, nil, nil
	}
}

// handover answers the gate's question for a signed-in app's Free-plan chat that
// nothing pays for: is it handed to Enso's router at no charge, or refused with the
// top-up? Only a class with a switch (flags.SeedHandover) can be refused; any other
// follows the plan and is handed over, as it always was. The switch is read as the
// payer's org asks it, live.
func handover(ctx context.Context, org, class string) bool {
	seed, ok := flags.SeedHandover[class]
	if !ok {
		return true
	}
	return flagsplane.Bool(cloud.For(ctx, org), flags.HandoverKey(class), seed)
}

// fast reports whether the request in flight asked for fast mode: ours, read as the
// model API reads it (aictl.WantsFast: the X-Fast header, else the body's "fast"), or
// a vendor's own faster lane (speed: "fast", vendorFast). Off the HTTP path nothing
// asks for it.
func fast(ctx context.Context) bool {
	c, ok := cloud.Request(ctx)
	return ok && (aictl.WantsFast(c) || vendorFast(c.Body()))
}
