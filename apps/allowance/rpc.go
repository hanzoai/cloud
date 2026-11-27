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
	"net/http"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/zap-proto/zip"
)

// The gate that admits a free call runs in a program of its own, so the count is
// ASKED: these are the ops that answer, on the internal plane (ZAP on this app's
// canonical unix socket). The count itself is the KV's (Store), one across pods.
//
// TWO OPS, BECAUSE A PAGE READS AND A GATE TAKES. Take admits a call and counts it in
// one step, before any upstream is reached; read answers the same standing without
// counting, for the usage page.
//
// They stay deliberately dumb about identity. The caller resolves WHOSE allowance this
// is — it holds the request, the credential and the billing subject — and these
// answer. Deriving a subject here would be a second resolver working from facts it
// does not carry.

// expose publishes the count. Mount calls it.
func expose() {
	cloud.Plane().Post("/allowance/read", planeRead,
		zip.WithOperationID(client.AllowanceRead),
		zip.WithSummary("Read what a subject has left of their plan's free calls"))
	cloud.Plane().Post("/allowance/take", planeTake,
		zip.WithOperationID(client.AllowanceTake),
		zip.WithSummary("Admit one free call against a subject's plan allowance, counting it"))
	cloud.Plane().Post("/allowance/give", planeGive,
		zip.WithOperationID(client.AllowanceGive),
		zip.WithSummary("Give back the free call a take counted for a call that was not served"))
}

// Answers what a subject has left of their plan's free-call allowance in the window
// that holds them, without counting anything: the tier the ceiling came from, the
// ceiling, the count, whether the next call would be refused, and when that window
// reopens.
//
// The ORG is the CALLER'S — the gateway's assertion — and can never be named in the
// input. With the payer it selects the CEILING: the payer's plan and balance. The
// count itself is addressed by subject (see Store), so what keeps one caller out of
// another's count is the subject the gate resolved from a verified credential, and
// the plane's own boundary: this answers on a unix socket inside the pod and has no
// address on the edge.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeRead(ctx context.Context, in *client.AllowanceIn) (*client.Allowance, error) {
	m, org, payer, subject, err := scope(ctx, in)
	if err != nil {
		return nil, err
	}
	return mounted.read(ctx, m, org, payer, subject, time.Now())
}

// scope resolves whose allowance a plane call is about, which wallet decides its
// ceiling, and on which meter, and refuses a call that cannot say. It is ONE rule for
// both ops: the org is the caller's, asserted by the gateway and never nameable in the
// input, and the subject and payer are theirs to choose only within it. A meter
// nobody defined is refused rather than read as AI calls.
func scope(ctx context.Context, in *client.AllowanceIn) (m gauge, org, payer, subject string, err error) {
	org = cloud.Who(ctx).Org
	if org == "" {
		return gauge{}, "", "", "", zip.ErrForbidden("allowance: no org on the call")
	}
	if in == nil || in.Subject == "" {
		return gauge{}, "", "", "", zip.ErrForbidden("allowance: no subject on the call")
	}
	m, known := meters[in.Meter]
	if !known {
		return gauge{}, "", "", "", zip.Errorf(http.StatusBadRequest, "allowance: no meter %q", in.Meter)
	}
	if mounted == nil {
		return gauge{}, "", "", "", fmt.Errorf("allowance: not mounted in this process")
	}
	payer = in.Payer
	if payer == "" {
		payer = in.Subject
	}
	return m, org, payer, in.Subject, nil
}

// Admits ONE free call against a subject's plan allowance in the window that holds
// them, counting it as it is admitted, and answers what stands after it: the tier the
// ceiling came from, the ceiling, the count, whether this call was refused, and when
// that window reopens. A Free person's first call after their last session ended
// opens a new one, five hours long, at its own instant.
//
// A CALL IS COUNTED WHEN IT IS ADMITTED. The gate asks this before it reaches any
// upstream, so calls in flight cannot all take the last unit; a call that is then not
// served gives its unit back (allowance_give). On the sandbox meter the unit is a
// lease, counted as the lease is admitted, because the pod is held from that moment
// on.
//
// The count is one step on the KV across every window, so two calls arriving
// together cannot both take the last unit. A subject already at the ceiling is
// answered spent=true with their count unchanged — refusals are not usage.
//
// The ORG is the CALLER'S — the gateway's assertion — and can never be named in the
// input; with the payer it selects the ceiling. The count is addressed by subject, so
// what keeps one caller out of another's count is the subject the gate resolved from
// a verified credential, and the plane's own boundary.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeTake(ctx context.Context, in *client.AllowanceIn) (*client.Allowance, error) {
	m, org, payer, subject, err := scope(ctx, in)
	if err != nil {
		return nil, err
	}
	return mounted.take(ctx, m, org, payer, subject, time.Now())
}

// Gives back the unit a take counted for a call that was admitted and then not
// served — an upstream that failed, a refusal after admission — so only served calls
// spend the allowance, and answers the standing after it. The unit returns to the
// windows the call was counted in — the hour and day at At, the session that opened
// at Start, the take's own answer — never below zero.
//
// The same scope as a take: the org is the caller's and the count is addressed by
// the subject the gate resolved, so a caller can give back only its own.
//
// A named handler, not a closure, so zipdoc can lift this prose into the registry.
func planeGive(ctx context.Context, in *client.AllowanceIn) (*client.Allowance, error) {
	m, org, payer, subject, err := scope(ctx, in)
	if err != nil {
		return nil, err
	}
	at := time.Now()
	if in.At > 0 {
		at = time.Unix(in.At, 0)
	}
	return mounted.give(ctx, m, org, payer, subject, at, in.Start)
}
