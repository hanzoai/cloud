package bot

import (
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// Scope is one capability a caller may hold. The six below are exactly what
// the web UI asks for on every connect (ui/src/api/gateway.ts:102-109,
// CONTROL_UI_OPERATOR_SCOPES) and what a method declares it needs.
type Scope string

const (
	// Open marks a method that needs no capability beyond a validated
	// identity. Only the handshake is open: a caller cannot hold a scope
	// before it has been told which ones it has.
	Open Scope = ""

	Read      Scope = "operator.read"
	Write     Scope = "operator.write"
	Admin     Scope = "operator.admin"
	Approvals Scope = "operator.approvals"
	Pairing   Scope = "operator.pairing"
	Questions Scope = "operator.questions"
)

// scopes is the closed set. A method that asks for anything else is a
// programming error and Register says so at load rather than at request time.
var scopes = map[Scope]bool{
	Read: true, Write: true, Admin: true,
	Approvals: true, Pairing: true, Questions: true,
}

// Grant is what one caller holds.
type Grant []Scope

// Allows reports whether the grant satisfies need. The implications are the
// protocol's own, from src/shared/operator-scope-compat.ts
// (operatorScopeSatisfied):
//
//	operator.admin satisfies every operator scope
//	operator.read  is satisfied by operator.read OR operator.write
//	every other scope is satisfied only by itself
//
// Note that write does not imply admin, and admin is not a superset by
// accident: it is the one scope that stands for all of them.
func (g Grant) Allows(need Scope) bool {
	if need == Open {
		return true
	}
	var read, write bool
	for _, held := range g {
		switch held {
		case Admin:
			return true
		case need:
			return true
		case Read:
			read = true
		case Write:
			write = true
		}
	}
	if need == Read {
		return read || write
	}
	return false
}

// strings renders the grant for the handshake, which advertises it verbatim.
func (g Grant) strings() []string {
	out := make([]string, 0, len(g))
	for _, s := range g {
		out = append(out, string(s))
	}
	return out
}

// narrow returns the part of the grant a caller asked for. A caller that asks
// for nothing gets everything it holds; one that asks for more than it holds
// gets the overlap rather than a refusal, because the UI asks for all six on
// every connect and a member who is not an admin of the org must still be able
// to work.
func (g Grant) narrow(want []string) Grant {
	if len(want) == 0 {
		return g
	}
	out := make(Grant, 0, len(g))
	for _, held := range g {
		for _, w := range want {
			if string(held) == w {
				out = append(out, held)
				break
			}
		}
	}
	return out
}

// grantFor maps the validated IAM principal onto capabilities. IAM is the only
// authority here: there is no token of this package's own, no device key, and
// nothing a caller can present that IAM has not already vouched for.
//
// A member of the org may drive its agents — read and write, answer their
// questions, resolve the approvals their own work raises. Admitting a device
// into the org's trust set and changing the org's configuration are acts on
// the org itself, so they belong to an admin of that org (or to platform sudo).
func grantFor(c *zip.Ctx) Grant {
	if !principal.Validated(c) {
		return nil
	}
	g := Grant{Read, Write, Approvals, Questions}
	if principal.IsOrgAdmin(c) || principal.IsSuperAdmin(c) {
		g = append(g, Admin, Pairing)
	}
	return g
}
