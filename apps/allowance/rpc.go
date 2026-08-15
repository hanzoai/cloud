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
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"
)

// The counter has ONE writer — this process — and its reader is the AI gate, which
// runs in a program of its own. So the count is ASKED, not opened: this is the op
// that answers, on the internal plane (ZAP on this app's canonical unix socket).
//
// It stays deliberately dumb about identity. The caller resolves WHOSE allowance it
// is spending — the gate holds the request, the credential and the billing subject —
// and this takes it. Deriving a subject here would be a second resolver working from
// facts it does not carry.

// expose publishes the count. Mount calls it.
func expose() {
	zip.Post[plane.AllowanceIn, plane.Allowance](cloud.Plane(), "/allowance/take", planeTake,
		zip.WithOperationID(plane.AllowanceTake),
		zip.WithSummary("Count one free call against a subject's plan allowance"))
}

// Counts ONE zero-priced call against a subject's plan allowance for the current
// period and answers what stands after it: the tier the ceiling came from, the
// ceiling, the count, whether it is now spent, and when it starts again.
//
// The ORG is the CALLER'S — the gateway's assertion — and can never be named in the
// input, so one tenant cannot spend another's allowance. The SUBJECT is the caller's
// to choose, but only within that org: it is a caller inside the tenancy the
// credential already pinned.
//
// TAKING IS THE ANSWER, not a step before it. A read followed by a separate
// increment is two calls racing for the same last unit, and both would be admitted;
// one call under one transaction cannot be. A subject already at the ceiling is
// answered spent=true with their count unchanged — refusals are not usage.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeTake(ctx context.Context, in *plane.AllowanceIn) (*plane.Allowance, error) {
	org := cloud.Who(ctx).Org
	if org == "" {
		return nil, zip.ErrForbidden("allowance: no org on the call")
	}
	if in == nil || in.Subject == "" {
		return nil, zip.ErrForbidden("allowance: no subject on the call")
	}
	if mounted == nil {
		return nil, fmt.Errorf("allowance: no store in the process that owns it")
	}
	return mounted.take(ctx, org, in.Subject, time.Now())
}
