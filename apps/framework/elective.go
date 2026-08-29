// Copyright © 2026 Hanzo AI. MIT License.

package framework

import (
	"context"
	"net/http"
	"strings"
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

// elective refuses a document op whose module the caller's org has not enabled.
//
// It reads the module from the PATH, not a bound parameter: it runs before the
// router has chosen a route, where a parameter is empty — the value that admits
// everything.
//
// The module used to be looked up in a table built from every lane's fixtures,
// because the path carried a name and a name did not say whose it was. The
// address says so, so there is nothing to look up and nothing to keep in step.
func elective(c *zip.Ctx) error {
	module, ok := moduleIn(c.Path())
	if !ok {
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
			"module", module, "err", err)
		return missing()
	}
	if !held.On {
		return missing()
	}
	return c.Next()
}

// moduleIn returns the module a framework path addresses: the half before the dot
// in /v1/framework/<module>.<kind>[/<name>[/submit|/cancel]].
//
// The empty answer is why this needs no list of exceptions. A static segment
// (doctypes, modules, summary) carries no dot and so addresses no DocType, and
// neither does an org's own module that no lane registered — the entitlement
// question is only ever asked about a product somebody sells.
func moduleIn(path string) (string, bool) {
	rest := strings.TrimPrefix(path, prefix)
	if rest == path {
		return "", false
	}
	rest = strings.TrimPrefix(rest, "/")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		rest = rest[:i]
	}
	module, _, ok := strings.Cut(rest, ".")
	if !ok || module == "" {
		return "", false
	}
	for _, m := range doctype.RegisteredModules() {
		if m == module {
			return module, true
		}
	}
	return "", false
}

// missing is the answer a path nobody registered gets. No header, no code, no
// sentence: a body saying "not enabled" reveals the product as surely as a 403.
func missing() error { return zip.ErrNotFound(http.StatusText(http.StatusNotFound)) }
