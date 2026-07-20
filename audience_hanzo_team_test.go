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

// TestJWTAudiences_AcceptsHanzoTeam pins the hanzo.team OIDC client. IAM mints
// team's access tokens with aud=hanzo-team (each app's aud is its client_id);
// the team OAuth callback sets that token as the hanzo_iam_token cookie, and
// the usage/wallet page (/v1/team/billing/ui/) reads /v1/billing/balance +
// /v1/usage/summary same-origin on it. If hanzo-team is not accepted the
// cookie resolves anonymous and every wallet read 401s — and the callback's
// own validator (NewTokenValidator shares this allowlist) refuses the login.
func TestJWTAudiences_AcceptsHanzoTeam(t *testing.T) {
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

	if !has(defaultJWTAudiences, "hanzo-team") {
		t.Fatalf("defaultJWTAudiences must include hanzo-team (the hanzo.team client_id); got %v", defaultJWTAudiences)
	}
	if !has(jwtAudiencesFromEnv(), "hanzo-team") {
		t.Fatalf("resolved JWT audiences must include hanzo-team; got %v", jwtAudiencesFromEnv())
	}
}
