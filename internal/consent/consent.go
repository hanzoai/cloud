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

package consent

import (
	"strings"

	"github.com/zap-proto/zip"
)

// OptedOut reports whether the request carries Global Privacy Control or Do Not
// Track, the browser's refusal of everything optional.
//
// It is the server's own line, independent of the client's: a request can arrive
// carrying the header from a surface that never read it, a browser or extension that
// sets it itself, or a privacy proxy that adds one, and the answer is the same.
func OptedOut(c *zip.Ctx) bool {
	return strings.TrimSpace(c.Header("DNT")) == "1" || strings.TrimSpace(c.Header("Sec-GPC")) == "1"
}

// Consented is the choice a page states for its own request — a checkout's, a
// form's — as @hanzo/event spells it ("analytics,marketing,ads"). It is nil off a
// browser (a request with no Sec-Fetch-Site, which every current browser sends),
// none at all under GPC or DNT, and otherwise the categories the page named, in
// their one order, any other word dropped.
func Consented(c *zip.Ctx, stated *string) *string {
	if stated == nil || strings.TrimSpace(c.Header("Sec-Fetch-Site")) == "" {
		return nil
	}
	var out []string
	if !OptedOut(c) {
		on := strings.Split(*stated, ",")
		for _, k := range []string{"analytics", "marketing", "ads"} {
			for _, v := range on {
				if strings.TrimSpace(v) == k {
					out = append(out, k)
					break
				}
			}
		}
	}
	s := strings.Join(out, ",")
	return &s
}
