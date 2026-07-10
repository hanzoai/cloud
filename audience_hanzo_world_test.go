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

// TestJWTAudiences_AcceptsHanzoWorld pins the world.hanzo.ai OIDC client. IAM
// mints world's access tokens with aud=hanzo-world (each app's aud is its
// client_id). Those bearers hit cloud-api; the identity sanitizer only trusts a
// principal whose aud is in this allowlist. If hanzo-world is not accepted the
// token resolves anonymous and the analyst's api.hanzo.ai calls 401. Pin the
// client_id into the baked default so the forwarded bearer validates.
func TestJWTAudiences_AcceptsHanzoWorld(t *testing.T) {
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

	if !has(defaultJWTAudiences, "hanzo-world") {
		t.Fatalf("defaultJWTAudiences must include hanzo-world (the world.hanzo.ai client_id); got %v", defaultJWTAudiences)
	}
	if !has(jwtAudiencesFromEnv(), "hanzo-world") {
		t.Fatalf("resolved JWT audiences must include hanzo-world; got %v", jwtAudiencesFromEnv())
	}
}
