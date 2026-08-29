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

package cloud

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/zap-proto/zip"
)

// walked returns the Link relations on the answer to `target`, as rel -> href.
//
// Read from the response rather than from the function's arguments: a link a
// client cannot see is not a link, and the header is the only place it sees one.
func walked(t *testing.T, target string, total int) (map[string]string, map[string]any) {
	t.Helper()
	app := zip.New(zip.Config{})
	app.Get("/v1/things", func(c *zip.Ctx) error {
		return Page(c, []string{"a"}, total)
	})
	res, err := app.Test(httptest.NewRequest(http.MethodGet, target, nil))
	if err != nil {
		t.Fatalf("test request: %v", err)
	}
	defer res.Body.Close()

	links := map[string]string{}
	for _, h := range res.Header.Values("Link") {
		for _, one := range strings.Split(h, ",") {
			i, j := strings.Index(one, "<"), strings.Index(one, ">")
			k := strings.Index(one, `rel="`)
			if i < 0 || j < i || k < 0 {
				continue
			}
			rel := one[k+5:]
			if e := strings.Index(rel, `"`); e >= 0 {
				rel = rel[:e]
			}
			links[rel] = one[i+1 : j]
		}
	}
	body, _ := io.ReadAll(res.Body)
	var env map[string]any
	_ = json.Unmarshal(body, &env)
	return links, env
}

func TestPageNamesTheNextPage(t *testing.T) {
	links, env := walked(t, "/v1/things?limit=10&offset=0", 25)

	if got := env["total"]; got != float64(25) {
		t.Fatalf("total = %v, want 25 — the caller cannot find the end without it", got)
	}
	if _, ok := links["next"]; !ok {
		t.Fatal("no next link on a first page of 25 rows taken 10 at a time")
	}
	if !strings.Contains(links["next"], "offset=10") {
		t.Errorf("next = %q, want offset 10", links["next"])
	}
	if _, ok := links["prev"]; ok {
		t.Errorf("prev = %q on the first page, which has nothing before it", links["prev"])
	}
	// 25 rows by 10 is three pages: 0, 10, 20. Naming 15 would overlap page two.
	if !strings.Contains(links["last"], "offset=20") {
		t.Errorf("last = %q, want offset 20 for 25 rows by 10", links["last"])
	}
}

func TestPageStopsAtTheEnd(t *testing.T) {
	links, _ := walked(t, "/v1/things?limit=10&offset=20", 25)

	if next, ok := links["next"]; ok {
		t.Errorf("next = %q past the end of a 25-row list", next)
	}
	if !strings.Contains(links["prev"], "offset=10") {
		t.Errorf("prev = %q, want offset 10", links["prev"])
	}
}

func TestPageKeepsTheCallersQuery(t *testing.T) {
	links, _ := walked(t, "/v1/things?limit=5&offset=0&status=open&sort=name", 20)

	next := links["next"]
	for _, keep := range []string{"status=open", "sort=name", "limit=5"} {
		if !strings.Contains(next, keep) {
			t.Errorf("next = %q dropped %s — walking a list must not widen it", next, keep)
		}
	}
}

func TestPageIsSilentWhenNothingWasPaged(t *testing.T) {
	links, env := walked(t, "/v1/things", 25)

	for _, rel := range []string{"first", "next", "prev", "last"} {
		if href, ok := links[rel]; ok {
			t.Errorf("%s = %q on a request that asked for the whole list", rel, href)
		}
	}
	if env["status"] != "ok" {
		t.Errorf("status = %v, want ok", env["status"])
	}
}
