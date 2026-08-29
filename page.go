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

// page.go — a list says how to walk it.
//
// A caller holding the first page of a list has to construct the second one.
// That means knowing this API pages by `limit` and `offset`, knowing the
// parameters are spelled those two ways, and knowing when to stop — three facts
// it can only have by reading documentation, and three that a rename breaks. RFC
// 8288 is the standard answer: name the next page and the relation it stands in,
// and the caller follows a URL it never had to build.
//
// HEADERS, because zip already writes `self`, `service-desc` and `service-doc`
// there and a client should not have to look in two places for links. `Link` is
// a list header (RFC 8288 §3), so these join whatever is already on the answer
// rather than replacing it. The envelope keeps the exact shape the operator's
// transport decodes — { status, msg, data, total } — so adopting this changes no
// body anyone parses.
//
// `limit` and `offset` because that is what this API already takes: 119 routes
// declare a limit and 21 an offset. Deriving a page from anything else would be
// a second convention for one idea.

package cloud

import (
	"net/url"
	"strconv"

	fiber "github.com/zap-proto/fiber/v3"
	"github.com/zap-proto/zip"
)

// Page writes the list envelope and the links that walk the list.
//
//	return cloud.Page(c, rows, total)
//
// `total` is the size of the whole list, not of this page — it is what tells a
// caller (and this function) where the last page falls.
func Page(c *zip.Ctx, data any, total int) error {
	walk(c, total)
	return c.JSON(200, map[string]any{"status": "ok", "msg": "", "data": data, "total": total})
}

// walk adds the RFC 8288 relations that name the other pages of this list.
//
// Silent when the request did not page. A caller that asked for everything has
// one page, and `first`/`last` pointing at the address it already holds is noise
// rather than navigation.
func walk(c *zip.Ctx, total int) {
	limit, err := strconv.Atoi(c.Query("limit"))
	if err != nil || limit <= 0 {
		return
	}
	offset, err := strconv.Atoi(c.Query("offset"))
	if err != nil || offset < 0 {
		offset = 0
	}

	add := func(rel string, off int) {
		c.Fiber().Response().Header.Add(fiber.HeaderLink, "<"+page(c, off)+`>; rel="`+rel+`"`)
	}

	add("first", 0)
	if offset > 0 {
		add("prev", max(0, offset-limit))
	}
	if offset+limit < total {
		add("next", offset+limit)
	}
	// The last page starts at the last multiple of limit below total. Computed
	// rather than assumed to be total-limit: with total 25 and limit 10 that
	// would name offset 15, a page overlapping the one before it.
	if total > 0 {
		add("last", (total-1)/limit*limit)
	}
}

// page renders this request's address with `offset` moved and everything else
// left alone — a filter or a sort the caller applied still applies on the page
// it walks to.
func page(c *zip.Ctx, offset int) string {
	q, err := url.ParseQuery(string(c.Fiber().Request().URI().QueryString()))
	if err != nil {
		q = url.Values{}
	}
	q.Set("offset", strconv.Itoa(offset))
	return c.Path() + "?" + q.Encode()
}
