// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"time"

	"github.com/hanzoai/cloud/client"
	flagplane "github.com/hanzoai/cloud/client/flag"
	iamserver "github.com/hanzoai/iam/server"
	"github.com/zap-proto/zip"
)

// The beta flag source IAM cannot reach on its own.
//
// IAM gates the OAuth code mint for hanzo.bot and hanzo.team by redirect host
// (iam/internal/oidc/beta.go). It links no cloud code — cloud imports IAM, so the
// reverse import would be a cycle — and it has no flag-bearing header to read.
// This is the same composition shape as delivery (sender.go): the host that owns
// BOTH the router and the flags app binds what IAM can only ask for. The plane
// call short-circuits when flags is co-resident and wakes it when it is cold.
//
// Nothing is read from the authorize request except the org and OIDC sub IAM
// already resolved from the signed-in account, so a client can never assert its
// own beta entitlement. An error — including no flags app in this deployment —
// is returned to IAM, which fails the gate closed.

// betaGateWait bounds an answer on the sign-in path: a beta check that stalls
// must not stall a login indefinitely. Silence is a refusal, not an open gate.
const betaGateWait = 3 * time.Second

// bindBetaGate is called at Mount, beside bindDelivery, before IAM serves.
// REQUIRES an IAM release carrying server.BindBetaGate and a go.mod bump from
// the pinned v1.34.198 — until then this file does not compile against the
// released module. See iam/BETA_GATE.md.
func bindBetaGate() {
	iamserver.BindBetaGate(flagGate{})
}

// flagGate is a value with no fields: there is nothing to misconfigure, and the
// peer is resolved by name on every call.
type flagGate struct{}

// On asks the flags app whether key is on for the person's org and OIDC sub.
// The org and sub ride the CALLER — the same channel every other plane call
// carries a principal on — never an argument the request could forge.
func (flagGate) On(ctx context.Context, key, org, sub string) (bool, error) {
	if key == "" || org == "" || sub == "" {
		return false, nil
	}
	call := zip.WithCaller(ctx, zip.Caller{Org: org, User: sub})
	call, cancel := context.WithTimeout(call, betaGateWait)
	defer cancel()
	v, err := flagplane.FlagsOn(call, &client.FlagIn{Key: key})
	if err != nil {
		return false, err
	}
	return v != nil && v.On, nil
}
