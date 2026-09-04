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

package allowance

import (
	"context"
	"fmt"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// The counter has ONE writer — this process — and its caller is the AI gate, which
// runs in a program of its own. So the count is ASKED, not opened: these are the ops
// that answer, on the internal plane (ZAP on this app's canonical unix socket).
//
// TWO OPS BECAUSE THERE ARE TWO MOMENTS. A call is admitted before it runs and counted
// after it answered. One verb doing both counts the ATTEMPT, and an attempt that
// reaches no model has cost us nothing to serve. Read admits; take counts what was
// served.
//
// They stay deliberately dumb about identity. The caller resolves WHOSE allowance this
// is — it holds the request, the credential and the billing subject — and these
// answer. Deriving a subject here would be a second resolver working from facts it
// does not carry.

// expose publishes the count. Mount calls it.
func expose() {
	zip.Post[client.AllowanceIn, client.Allowance](cloud.Plane(), "/allowance/read", planeRead,
		zip.WithOperationID(client.AllowanceRead),
		zip.WithSummary("Read what a subject has left of their plan's free calls"))
	zip.Post[client.AllowanceIn, client.Allowance](cloud.Plane(), "/allowance/take", planeTake,
		zip.WithOperationID(client.AllowanceTake),
		zip.WithSummary("Count one served free call against a subject's plan allowance"))
}

// Answers what a subject has left of their plan's free-call allowance this period,
// without counting anything: the tier the ceiling came from, the ceiling, the count,
// whether it is spent, and when it starts again.
//
// THIS IS THE ADMISSION HALF. The gate asks it before a call and refuses a subject at
// the ceiling; asking costs the subject nothing, so a refusal never becomes usage and
// a request that dies before any model leaves the count exactly where it found it.
//
// The ORG is the CALLER'S — the gateway's assertion — and can never be named in the
// input. It selects the TIER, which is the ceiling this subject is held to. The row
// itself is addressed by subject alone (see Store), so what keeps one caller out of
// another's count is the subject the gate resolved from a verified credential, and
// the plane's own boundary: this answers on a unix socket inside the pod and has no
// address on the edge.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRead(ctx context.Context, in *client.AllowanceIn) (*client.Allowance, error) {
	org, subject, err := scope(ctx, in)
	if err != nil {
		return nil, err
	}
	return mounted.read(ctx, org, subject, time.Now())
}

// scope resolves whose allowance a plane call is about, and refuses a call that
// cannot say. It is ONE rule for both ops: the org is the caller's, asserted by the
// gateway and never nameable in the input, and the subject is theirs to choose only
// within it.
func scope(ctx context.Context, in *client.AllowanceIn) (org, subject string, err error) {
	org = cloud.Who(ctx).Org
	if org == "" {
		return "", "", zip.ErrForbidden("allowance: no org on the call")
	}
	if in == nil || in.Subject == "" {
		return "", "", zip.ErrForbidden("allowance: no subject on the call")
	}
	if mounted == nil {
		return "", "", fmt.Errorf("allowance: no store in the process that owns it")
	}
	return org, in.Subject, nil
}

// Counts ONE SERVED zero-priced call against a subject's plan allowance for the
// current period and answers what stands after it: the tier the ceiling came from,
// the ceiling, the count, whether it is now spent, and when it starts again.
//
// The ORG is the CALLER'S — the gateway's assertion — and can never be named in the
// input; it selects the TIER whose ceiling applies. The row is addressed by subject
// alone, so what keeps one caller out of another's count is the subject the gate
// resolved from a verified credential, and the plane's own boundary.
//
// A CALL IS COUNTED WHERE IT ANSWERED. The ceiling bounds spend and spend is incurred
// when a model is reached, so the gate asks AllowanceRead before the call and this is
// reached only from the caller's record of a served one. Nothing else may reach it:
// counting an attempt charges a customer for an outage of ours.
//
// The read and the increment inside it are ONE statement in ONE transaction, so two
// served calls arriving together cannot both write the same count. A subject already
// at the ceiling is answered spent=true with their count unchanged — refusals are not
// usage.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeTake(ctx context.Context, in *client.AllowanceIn) (*client.Allowance, error) {
	org, subject, err := scope(ctx, in)
	if err != nil {
		return nil, err
	}
	return mounted.take(ctx, org, subject, time.Now())
}
