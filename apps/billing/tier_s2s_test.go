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
	"net/http"
	"testing"
)

// The other half, and the reason this is not a widening: admitting a service
// token must not become a way to read another tenant's plan, or to skip auth.
// The org rides the gateway-pinned X-Org-Id — a header the gateway STRIPS from
// every client request — never a caller-supplied field. A wrong token, an absent
// token, or a token naming no org must all still refuse, exactly as they do on
// balance.
func TestTier_NoPrincipalIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, org string }{
		{"an org header alone", "hanzo"},
		{"nothing at all", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := mountApp(t, "")
			code, _ := orgCall(t, app, "/v1/billing/tier?user=hanzo", tc.org)
			if code != http.StatusUnauthorized {
				t.Errorf("want 401, got %d", code)
			}
		})
	}
}
