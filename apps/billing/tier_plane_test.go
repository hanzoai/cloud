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

package billing

import (
	"context"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/internal/planetest"
)

// THE TENANT HAS TO SURVIVE THE HOP, and nothing tested that it did.
//
// The endpoint resolves who is asking and the op behind it acts for them, but
// they are two resolutions on two different context shapes: the endpoint has a
// REQUEST, and the plane op has only what ask() stamped — plane.For sets
// zip.Caller{Org} and leaves everything else empty. A fix to one says nothing
// about the other, which is exactly how a first attempt at this passed its own
// tests and changed a 401 into a 403 in production and nothing else.
//
// So this asserts the HANDOFF rather than either end: a trusted service with no
// session reaches the endpoint, and the org the endpoint resolved is the org
// the op is invoked for. The stub stands in for commerce and records what it
// was told.
func TestTheOrgTheDoorResolvedIsTheOrgThePlaneOpActsFor(t *testing.T) {
	const token = "test-commerce-service-token"

	// The stub has to be REACHABLE as commerce, not merely registered: plane.Ask
	// resolves the peer by name (zip.Serving), and without that it answers
	// ErrNoPeer and the endpoint reports 503 before the op is ever invoked —
	// which is what this test did until it served the plane.
	// planetest.Dir, not t.TempDir: a temp dir named for the test carries the
	// TEST'S NAME, and this one is 49 characters. Darwin allows 103 bytes of
	// sun_path total, so the socket under it did not bind — as "invalid argument",
	// which names neither the limit nor the length. Every other plane test in this
	// repo already takes the short dir; this was the one that did not.
	t.Setenv("ZIP_RUNTIME_DIR", planetest.Dir(t))
	plane.Unbind()
	cloud.ResetPlane()
	t.Cleanup(cloud.ResetPlane)

	var sawOrg, sawSubject string
	zip.Post[plane.TierIn, plane.Tier](cloud.Plane(), "/billing/tier",
		func(ctx context.Context, in *plane.TierIn) (*plane.Tier, error) {
			sawOrg = cloud.Who(ctx).Org
			sawSubject = in.Subject
			return &plane.Tier{User: in.Subject, Tier: plane.TierLimits{Name: "pro"}}, nil
		},
		zip.WithOperationID(plane.BillingTier))

	stop, err := cloud.ServePlane("commerce", nil)
	if err != nil {
		t.Fatalf("ServePlane(commerce): %v", err)
	}
	defer func() { _ = stop() }()

	app := mountApp(t, "", token)

	code, body := s2sCall(t, app, "/v1/billing/tier?user=hanzo", token, "hanzo")
	if code != http.StatusOK {
		t.Fatalf("the trusted service did not reach the op: %d %s", code, body)
	}
	// The whole point: the op was invoked FOR the tenant the endpoint admitted,
	// not for nobody. Empty here is the production 403 — the op reached, and
	// refusing because it could not tell whose books it was reading.
	if sawOrg != "hanzo" {
		t.Errorf("the plane op acted for org %q, want \"hanzo\" — the tenant did not "+
			"survive the hop from the endpoint to the op", sawOrg)
	}
	// The SUBJECT is a separate question and this does not decide it. A service
	// token carries no X-User-Name or X-User-Id, so principal.Subject has no
	// person to resolve and the op is asked about the tenant with an empty one.
	// Whether commercebilling.TierOf should read an org's own tier from that, or
	// whether the service path ought to name the org as the subject, is a
	// question about which wallet a tenant-wide read belongs to — not about
	// whether the tenant survived the hop, which is what this test is for.
	t.Logf("subject on the service path: %q (no user header to resolve one from)", sawSubject)
}
