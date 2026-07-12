// Copyright 2026 The Hanzo Authors. All Rights Reserved.
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

package cloud

import (
	"os"
	"testing"
)

// TestJWTAudiences_AcceptsAdminGuard is the operator-cockpit keystone. The
// admin.hanzo.ai forward-auth guard is the confidential client `hanzo-admin-guard`,
// so IAM mints its access tokens with aud=hanzo-admin-guard (each app's aud is its
// client_id). The guard forwards that bearer to cloud-api /v1/admin/*; the identity
// sanitizer only grants SuperAdmin (owner==adminOrg) to a VALIDATED principal, and
// validation enforces this audience allowlist. If hanzo-admin-guard is not accepted
// the token resolves anonymous and the SuperAdmin gate reads false -> 403, even
// though the token's owner IS admin. Pin the client_id into the baked default so the
// forwarded bearer validates.
func TestJWTAudiences_AcceptsAdminGuard(t *testing.T) {
	os.Unsetenv("CLOUD_JWT_AUDIENCES")
	os.Unsetenv("GATEWAY_ALLOWED_AUDIENCES")

	has := func(list []string, v string) bool {
		for _, s := range list {
			if s == v {
				return true
			}
		}
		return false
	}

	if !has(defaultJWTAudiences, "hanzo-admin-guard") {
		t.Fatalf("defaultJWTAudiences must include hanzo-admin-guard (the admin-cockpit guard client_id); got %v", defaultJWTAudiences)
	}
	if !has(jwtAudiencesFromEnv(), "hanzo-admin-guard") {
		t.Fatalf("resolved JWT audiences must include hanzo-admin-guard; got %v", jwtAudiencesFromEnv())
	}
}
