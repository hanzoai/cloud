// Copyright © 2026 Hanzo AI. MIT License.

package settings

import (
	"context"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

// The cross-tenant settings path: a SuperAdmin managing ANOTHER org's per-product
// configuration. It is the SAME engine the self-service ops use — read and write —
// with the org supplied as an ARGUMENT instead of read from the validated principal.
//
// The org is a parameter here for the same reason it is NOT one on the self-service
// path (see the tenant doc in settings.go): naming an org other than your own is the
// one thing only a SuperAdmin may do, and the admin surface has already proven that
// (core.Admit) before it calls in. Everything downstream is unchanged — the config
// cap, the KMS-or-fail-closed secret routing to orgs/{org}/settings/{product}/{key},
// the read mask. Only the org SOURCE differs, so there is one custody engine, not two.
//
// These are ordinary Go calls, not an internal-plane op like fleetConfig, because the
// caller is in this same binary and arrives already authorized; the plane exists for
// platform code that has no principal to gate on, which is not this case.

// active returns the mounted settings service or fails closed. A cross-tenant caller
// reaching this before settings.Use has run gets a 503, never a nil dereference.
func active() (*service, error) {
	if mounted == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "settings: not mounted")
	}
	return mounted, nil
}

// requireOrg validates a caller-supplied TARGET org before it becomes a store key and
// a KMS ref segment. The self-service path never validates the org — it is the minted
// owner claim — but here the org rode in on a URL, so it is guarded exactly as
// principal does on ingress: no unsafe rune, within MaxOrgLen. The value is used
// verbatim (never normalized): casing or trimming would fold distinct owners into one
// bucket, so a malformed org is refused, not repaired.
func requireOrg(org string) (string, error) {
	o := strings.TrimSpace(org)
	if o == "" {
		return "", zip.ErrBadRequest("org is required")
	}
	if principal.OrgHasUnsafeRune(o) || len(o) > principal.MaxOrgLen {
		return "", zip.ErrBadRequest("org is malformed")
	}
	return o, nil
}

// AdminGet reads one org's configuration for a product on behalf of a SuperAdmin,
// secrets masked, with the same empty-is-not-404 rule as the self-service read. The
// org is explicit; the caller has already been admitted.
func AdminGet(ctx context.Context, org, product string) (View, error) {
	s, err := active()
	if err != nil {
		return View{}, err
	}
	o, err := requireOrg(org)
	if err != nil {
		return View{}, err
	}
	p, err := requireProduct(product)
	if err != nil {
		return View{}, err
	}
	return s.read(ctx, o, p)
}

// AdminPut writes one org's configuration for a product on behalf of a SuperAdmin and
// answers the stored result, secrets masked. Secret VALUES seal into KMS under
// orgs/{org}/settings/{product}/{key} — the target org's namespace, exactly as the
// self-service write uses the caller's own — and a write carrying a secret with no KMS
// configured is refused whole (503). A secret the body omits keeps its stored value.
func AdminPut(ctx context.Context, org, product string, config map[string]any, secrets map[string]string) (View, error) {
	s, err := active()
	if err != nil {
		return View{}, err
	}
	o, err := requireOrg(org)
	if err != nil {
		return View{}, err
	}
	p, err := requireProduct(product)
	if err != nil {
		return View{}, err
	}
	return s.write(ctx, o, p, config, secrets)
}

// AdminList returns every product this org has configuration for, most-recently-
// written first, secrets masked. An org with no configuration answers an empty list.
func AdminList(ctx context.Context, org string) ([]View, error) {
	s, err := active()
	if err != nil {
		return nil, err
	}
	o, err := requireOrg(org)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.List(ctx, o)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list settings: %v", err)
	}
	views := make([]View, 0, len(rows))
	for _, st := range rows {
		views = append(views, toView(st))
	}
	return views, nil
}
