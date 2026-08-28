// Copyright © 2026 Hanzo AI. MIT License.

package framework

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/plane"
	"github.com/hanzoai/cloud/plane/entitlement"
	"github.com/hanzoai/doctype"
	"github.com/zap-proto/zip"
)

// The opt-in: a document op is refused unless the caller's org has enabled the
// module that owns its DocType. crm, cms and erp are modules on this engine, so
// the module is what a customer buys.
//
// Asked on every request, not only at install: DocTypes stay in an org's database
// after it stops paying, so install alone would be a one-way door.

// prefix is the group every framework route hangs off. Two readers need it: the
// router that mounts the group, and doctypeIn.
const prefix = "/v1/framework"

// wait bounds the entitlement read before the refusal fails closed.
const wait = 3 * time.Second

var (
	once    sync.Once
	modules map[string]string // DocType name -> owning module
)

// moduleOf resolves the module owning a DocType, or "" for one no module declares.
//
// The empty answer is why this needs no list of exceptions: a static segment
// (doctypes, roles, modules, summary, health) can never be a DocType — the
// doctype package reserves those names — and an org's own DocType belongs to no
// registered module. Both are served.
func moduleOf(name string) string {
	once.Do(func() {
		modules = map[string]string{}
		for _, m := range doctype.RegisteredModules() {
			for _, dt := range doctype.Fixtures(m) {
				modules[dt.Name] = m
			}
		}
	})
	return modules[name]
}

// elective refuses a document op whose module the caller's org has not enabled.
//
// It reads the DocType from the PATH, not a bound parameter: it runs before the
// router has chosen a route, where a parameter is empty — the value that admits
// everything.
func elective(c *zip.Ctx) error {
	name, ok := doctypeIn(c.Path())
	if !ok {
		return c.Next()
	}
	module := moduleOf(name)
	if module == "" {
		return c.Next()
	}
	// 404, not 401: a product a stranger has not bought owes no confirmation that
	// it exists.
	org, ok := principal.Org(c)
	if !ok || org == "" {
		return missing()
	}
	ctx, cancel := context.WithTimeout(plane.For(c.Context(), org), wait)
	defer cancel()
	held, err := entitlement.EntitlementHolds(ctx, &plane.ProductIn{Product: module})
	if err != nil {
		// Fail closed: admitting when entitlement cannot answer makes every module
		// free for everyone. The reason goes to the log, never the wire.
		c.Log().Warn("framework: refusing, entitlement did not answer",
			"doctype", name, "module", module, "err", err)
		return missing()
	}
	if !held.On {
		return missing()
	}
	return c.Next()
}

// doctypeIn returns the DocType a framework path addresses: the one segment after
// the group in /v1/framework/<doctype>[/<name>[/submit|/cancel]].
func doctypeIn(path string) (string, bool) {
	rest := strings.TrimPrefix(path, prefix)
	if rest == path {
		return "", false
	}
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		return "", false
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	return rest, rest != ""
}

// missing is the answer a path nobody registered gets. No header, no code, no
// sentence: a body saying "not enabled" reveals the product as surely as a 403.
func missing() error { return zip.ErrNotFound(http.StatusText(http.StatusNotFound)) }
