package kms_test

// WHO MAY LEARN WHAT AN ORG HOLDS. A listing carries no value, but the names in
// an org's store say what the org runs and where its credentials are, and the
// brand org's store is the platform's own. So a listing shows every name to a
// SuperAdmin (audited) and to an admin of a customer org holding their own access
// token, shows any other caller only the names its own grants open, and shows a
// plain member nothing, on every spelling of every path. In a held org (a brand)
// org admin is a self-service role and lists nothing either. Each refusal is
// paired with a caller the same request serves, so a broken endpoint cannot pass
// as a gate.

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
)

// names is every secret name the grant world seeds in org hanzo.
var names = []string{"HANZO_TOKEN", "htpasswd", "OTHER", "CLOUD_KMS_MASTER_KEY", "GITHUB_TOKEN"}

// listed is the sorted names a 200 listing carries.
func listed(t *testing.T, p isoProbe) []string {
	t.Helper()
	p.isStatus(t, 200)
	var got struct{ Names []string }
	if err := json.Unmarshal([]byte(p.body), &got); err != nil {
		t.Fatalf("[%s]: %v: %s", p.what, err, p.body)
	}
	slices.Sort(got.Names)
	return got.Names
}

func TestGrant_ListingTakesAdminOrAGrant(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		w := newGrantWorld(t, grantDoc, true)

		// A member of the org sees no name on any path, whichever spelling it uses.
		for _, q := range []string{
			"", "?path=/", "?env=prod", "?path=/attendant&env=prod", "?path=/cloud&env=prod",
			"?secretPath=/pkg&environment=prod", "?path=/deploy", "?path=attendant",
		} {
			w.call(t, "member lists "+q, "GET", "/v1/kms/secrets"+q, member, nil, "").
				refused(t, noOrgScope).noLeak(t, names...)
			w.call(t, "member lists in hanzo "+q, "GET", "/v1/kms/secrets"+q, member, inHanzo, "").
				refused(t, "").noLeak(t, names...)
		}
		// Nor learns whether a name exists by asking for it.
		a := w.read(t, "member reads a secret that exists", "cloud/CLOUD_KMS_MASTER_KEY?env=prod", member, nil)
		b := w.read(t, "member reads a secret that does not", "cloud/NO_SUCH_KEY?env=prod", member, nil)
		if a.status != 403 || b.status != 403 {
			t.Fatalf("existence oracle: %d vs %d, want 403 for both", a.status, b.status)
		}

		// An admin of the brand org lists nothing of the platform's store.
		w.call(t, "brand org admin lists hanzo", "GET", "/v1/kms/secrets", orgAdmin, nil, "").
			refused(t, "listing org hanzo's secrets").noLeak(t, names...)
		// A SuperAdmin sees every name, and no value, on the record.
		all := listed(t, w.call(t, "SuperAdmin lists hanzo", "GET", "/v1/kms/secrets", superAdmin, inHanzo, "").noLeak(t, every...))
		if want := []string{"CLOUD_KMS_MASTER_KEY", "GITHUB_TOKEN", "HANZO_TOKEN", "OTHER", "htpasswd"}; !slices.Equal(all, want) {
			t.Fatalf("SuperAdmin's listing = %v, want %v", all, want)
		}

		// A customer org's admin sees every name it holds, holding their own access
		// token; an id_token of theirs, a token acting for them, and their member list
		// nothing.
		for _, ref := range []string{"orgs/acme/ci/DEPLOY@prod", "orgs/acme/BILLING@default"} {
			if err := w.deps.KMS.PutSecret(context.Background(), ref, []byte("acme-value")); err != nil {
				t.Fatalf("seed %s: %v", ref, err)
			}
		}
		acmeAdmin := isoTok{owner: "acme", username: "boss", isAdmin: true}
		if got := listed(t, w.call(t, "acme admin lists acme", "GET", "/v1/kms/secrets", acmeAdmin, nil, "").noLeak(t, "acme-value")); !slices.Equal(got, []string{"BILLING", "DEPLOY"}) {
			t.Fatalf("acme admin's listing = %v, want acme's two names", got)
		}
		for _, who := range []struct {
			name string
			tok  isoTok
		}{
			{"acme admin's id_token", isoTok{owner: "acme", username: "boss", isAdmin: true, idToken: true}},
			{"a token acting for acme's admin", isoTok{owner: "acme", username: "boss", isAdmin: true, act: "acme/default"}},
			{"acme member", isoTok{owner: "acme", username: "staff"}},
		} {
			p := w.call(t, who.name+" lists acme", "GET", "/v1/kms/secrets", who.tok, nil, "").noLeak(t, "DEPLOY", "BILLING")
			if p.status != 401 && p.status != 403 {
				t.Fatalf("[%s]: status %d, want a refusal: %s", p.what, p.status, p.body)
			}
		}

		// A holder sees only what its grants open: the operator its two charted keys,
		// not the key beside one of them nor the master key.
		op := machine("hanzo-operator", "hanzo")
		if got := listed(t, w.call(t, "operator lists the org", "GET", "/v1/kms/secrets", op, nil, "")); !slices.Equal(got, []string{"HANZO_TOKEN", "htpasswd"}) {
			t.Fatalf("operator's listing = %v, want its two granted keys", got)
		}
		if got := listed(t, w.call(t, "operator lists /pkg", "GET", "/v1/kms/secrets?path=/pkg&env=prod", op, nil, "")); !slices.Equal(got, []string{"htpasswd"}) {
			t.Fatalf("operator's /pkg listing = %v, want htpasswd alone", got)
		}
		w.call(t, "operator lists where it holds nothing", "GET", "/v1/kms/secrets?path=/cloud", op, nil, "").
			refused(t, "").noLeak(t, names...)
		w.call(t, "operator lists another env", "GET", "/v1/kms/secrets?path=/pkg&env=default", op, nil, "").
			refused(t, "").noLeak(t, names...)
		// A grant is (holder, org): minted by lux, the same client lists nothing of lux.
		w.call(t, "hanzo-operator minted by lux lists", "GET", "/v1/kms/secrets", machine("hanzo-operator", "lux"), nil, "").
			refused(t, "listing org lux's secrets")
		ci := machine("hanzo-ci", "hanzo")
		if got := listed(t, w.call(t, "CI lists /deploy", "GET", "/v1/kms/secrets?path=/deploy&env=prod", ci, nil, "")); !slices.Equal(got, []string{"GITHUB_TOKEN"}) {
			t.Fatalf("CI's listing = %v, want its one key", got)
		}

		// Every listing KMS was asked is on the chain, the refused ones as denials
		// naming the holder. A plain member of hanzo never reaches KMS — it is refused
		// at the org (noOrgScope) — so its sixteen attempts are no rows here.
		rows := w.kmsRecords(t, "kms.list")
		var denied, super, admin int
		for _, r := range rows {
			j := mustJSON(t, r)
			switch {
			case r.Outcome.Result == "deny" && strings.Contains(j, "hanzo/dev"):
				denied++
			case r.Outcome.Result == "success" && r.Auth.IsAdmin && strings.Contains(r.Outcome.Reason, "superadmin"):
				super++
			case r.Outcome.Result == "success" && strings.HasPrefix(r.Outcome.Reason, "org admin "):
				admin++
			}
		}
		if denied != 0 || super != 1 || admin != 1 {
			t.Fatalf("the chain holds %d member denials, %d SuperAdmin and %d admin listings, want 0, 1 and 1: %s",
				denied, super, admin, mustJSON(t, rows))
		}
		verifyChain(t, w.chain)
	})
}
