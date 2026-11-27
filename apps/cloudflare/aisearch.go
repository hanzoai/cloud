package cloudflare

// aisearch.go — Cloudflare AI Search, WIRED (account-scoped:
// /accounts/{id}/ai-search/namespaces/{namespace}/...). An org's instances live in
// ONE namespace, and that namespace is DERIVED from the org — never named by a
// caller. Reads take the member gate (authClient); every mutation takes the admin
// gate (authWrite).
//
// WHY THE NAMESPACE IS DERIVED. Cloudflare puts every instance in the account's
// `default` namespace unless told otherwise, and a namespace is part of an
// instance's identity: import ids, data sources and the REST paths all carry it, and
// moving an instance to another namespace is a REPLACEMENT that discards its index
// (Terraform's `cloudflare_ai_search_instance` forces new on a `namespace` change).
// So the name has to come from something that never changes, and it has to be
// impossible to leave out. It comes from the validated org — the key every per-org
// store in this process is filed under — folded through principal.OrgNamespace, the
// one entry point that turns an org into a name. There is no namespace parameter
// anywhere on this surface; a body that names one other than the derived name is
// refused, and `default` can never be reached.
//
// WHAT IT IS NOT. A namespace is a Cloudflare grouping, not Hanzo's authorization or
// payer boundary. Who may act is decided before any of this runs — the validated
// principal, then org membership or org admin — and who pays is the org whose own
// token made the call (X-Hanzo-Org on every response). Per-document ACLs cannot be
// expressed as a namespace at all, which is why this plane manages instances and does
// NOT relay search or chat queries: a query answered straight from a namespace would
// return every document in it to every org member. Queries go through Hanzo's own
// /v1/search and /v1/knowledge/search, which bound every search by the principal
// (apps/knowledge ownerFilter).
//
// WHAT IT REFUSES. No route moves an instance between namespaces (Cloudflare's PATCH
// with `new_namespace`), and an update that names a different namespace, or any
// `new_namespace`, is a 400. Changing an instance's namespace is a replacement that
// goes through dual-write, backfill and cutover; apps/cloudflare/LLM.md is the
// procedure.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

const (
	// aiSearchNamespacePrefix starts every namespace this plane derives. It tells
	// Hanzo's namespaces apart from any other in the same account, and it is what
	// makes `default` structurally unreachable rather than merely checked for.
	aiSearchNamespacePrefix = "hz-"
	// aiSearchNamespaceHex is how many hex digits of the digest follow the prefix:
	// 96 bits, and 27 characters in all, under Cloudflare's 28.
	aiSearchNamespaceHex = 24
	// aiSearchNamespaceDomain separates this derivation from any other digest of the
	// same org, so the name is not reusable as anything else.
	aiSearchNamespaceDomain = "hanzo/cloudflare/ai-search/namespace\x00"
	// aiSearchScope names what the namespace is derived from. One value today: an
	// org. It is published so a reader never has to guess.
	aiSearchScope = "org"
)

var (
	// aiSearchNamespaceRE is Cloudflare's own rule for a namespace name: 1 to 28
	// lowercase letters, digits and hyphens, starting and ending with a letter or
	// digit. A derived name is checked against it before it is used.
	aiSearchNamespaceRE = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,26}[a-z0-9])?$`)
	// aiSearchInstanceRE is Cloudflare's rule for an instance id: 1 to 64 lowercase
	// letters, digits, hyphens and underscores. No `/` and no `.`, so an id can never
	// carry path structure into the upstream URL.
	aiSearchInstanceRE = regexp.MustCompile(`^[a-z0-9_-]{1,64}$`)
)

// aiSearchNamespace derives the AI Search namespace an org's instances live in.
//
// org MUST be the validated principal's org. It is folded through
// principal.OrgNamespace — the same injective fold every per-org store is named by —
// and the canonical name is hashed, so the result fits Cloudflare's 28-character
// charset whatever the org is called, is the same on every call, and differs for
// every other org. Nothing a caller sends reaches it.
func aiSearchNamespace(org string) (string, error) {
	ns, err := principal.OrgNamespace(org, "")
	if err != nil {
		return "", zip.ErrBadRequest("this org cannot name an AI Search namespace")
	}
	sum := sha256.Sum256([]byte(aiSearchNamespaceDomain + ns.String()))
	name := aiSearchNamespacePrefix + hex.EncodeToString(sum[:])[:aiSearchNamespaceHex]
	if !aiSearchNamespaceRE.MatchString(name) {
		// Unreachable by construction; refusing beats sending Cloudflare a name it
		// would put somewhere we did not mean.
		return "", zip.Errorf(http.StatusInternalServerError, "derived AI Search namespace is invalid")
	}
	return name, nil
}

// aiSearchPath is the upstream base for one namespace on one account.
func aiSearchPath(acct, ns string) string {
	return "/accounts/" + acct + "/ai-search/namespaces/" + ns
}

// searchScope is the preamble every AI Search op that reaches Cloudflare shares:
// the gate (admin for a mutation), then the namespace derived from the org that gate
// validated. The account is resolved by the caller AFTER it has checked its own
// input, so a refused request never reaches Cloudflare — not even for account
// discovery.
func (o ops) searchScope(ctx context.Context, write bool) (cl *client, org, ns string, err error) {
	if write {
		cl, org, err = o.authWrite(ctx)
	} else {
		cl, org, err = o.authClient(ctx)
	}
	if err != nil {
		return nil, "", "", err
	}
	if ns, err = aiSearchNamespace(org); err != nil {
		return nil, "", "", err
	}
	return cl, org, ns, nil
}

// sameNamespace refuses a caller-named namespace unless it IS the derived one. An
// empty value is not a name and passes; anything else — `default` included — is a
// 400 that says which namespace this org is in.
func sameNamespace(field, got, derived string) error {
	got = strings.TrimSpace(got)
	if got == "" || got == derived {
		return nil
	}
	return zip.ErrBadRequest(field + " is derived from your org and cannot be chosen: this org's AI Search namespace is " + derived)
}

// ── namespace ───────────────────────────────────────────────────────────────────

// AISearchNamespace is the AI Search namespace an org's instances live in.
type AISearchNamespace struct {
	// Namespace is the Cloudflare AI Search namespace name, derived from the org.
	// It is the value to give `namespace` on a `cloudflare_ai_search_instance`, and
	// it never changes for the life of the org.
	Namespace string `json:"namespace"`
	// Scope is what the namespace is derived from. Always "org".
	Scope string `json:"scope"`
	// Org is the org it was derived from: the caller's own, from its credential.
	Org string `json:"org"`
}

// AISearchNamespaceGet answers which Cloudflare AI Search namespace the caller's
// org owns. The name is derived from the org, so it is answered without
// contacting Cloudflare and without a connected token; it is the same on every
// call and differs for every other org. Any org member may read.
func (o ops) aiSearchNamespaceGet(ctx context.Context, _ *cloud.Unit) (*AISearchNamespace, error) {
	org, err := principal.Acting(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := aiSearchNamespace(org)
	if err != nil {
		return nil, err
	}
	return &AISearchNamespace{Namespace: ns, Scope: aiSearchScope, Org: org}, nil
}

// aiSearchNamespaceCreateIn describes the namespace to create. Its name is not the
// caller's to choose.
type aiSearchNamespaceCreateIn struct {
	// Name may be omitted. If present it must equal the org's derived namespace
	// (GET /v1/cloudflare/ai-search/namespace); any other name, `default` included,
	// is refused.
	Name string `json:"name"`
	// Description is an optional note Cloudflare stores on the namespace, up to 256
	// characters.
	Description string `json:"description"`
}

// AISearchNamespaceCreate creates the org's AI Search namespace on its Cloudflare
// account. The name is derived from the org, never chosen; a `name` other than
// that one is refused with 400. Create it once, before the first instance.
// Requires org admin.
//
// Example: {"description": "Acme support content"}
func (o ops) aiSearchNamespaceCreate(ctx context.Context, in *aiSearchNamespaceCreateIn) (*cfResult, error) {
	cl, org, ns, err := o.searchScope(ctx, true)
	if err != nil {
		return nil, err
	}
	if err := sameNamespace("name", in.Name, ns); err != nil {
		return nil, err
	}
	desc := strings.TrimSpace(in.Description)
	if len(desc) > 256 {
		return nil, zip.ErrBadRequest("description is longer than 256 characters")
	}
	acct, err := cl.resolveAccount(ctx, org)
	if err != nil {
		return nil, err
	}
	body := map[string]string{"name": ns}
	if desc != "" {
		body["description"] = desc
	}
	return cl.relay(ctx, http.MethodPost, "/accounts/"+acct+"/ai-search/namespaces", body)
}

// ── instances ───────────────────────────────────────────────────────────────────

// aiSearchInstancesIn pages the instance list. Both fields are optional, ride the
// query string, and are forwarded to Cloudflare under the same name.
type aiSearchInstancesIn struct {
	// Page is the 1-based page of instances to return.
	Page string `json:"page"`
	// PerPage is how many instances one page holds.
	PerPage string `json:"per_page"`
}

// AISearchInstanceList lists the AI Search instances in the org's own namespace on
// its Cloudflare account. Instances in any other namespace, `default` included, are
// not this org's and are never listed. Any org member may read.
func (o ops) aiSearchInstanceList(ctx context.Context, in *aiSearchInstancesIn) (*cfResult, error) {
	cl, org, ns, err := o.searchScope(ctx, false)
	if err != nil {
		return nil, err
	}
	acct, err := cl.resolveAccount(ctx, org)
	if err != nil {
		return nil, err
	}
	q := forward(map[string]string{"page": in.Page, "per_page": in.PerPage})
	return cl.relay(ctx, http.MethodGet, aiSearchPath(acct, ns)+"/instances"+q, nil)
}

// aiSearchBody is the caller's JSON object, kept field by field as the caller wrote
// each value, so a forward drops nothing Cloudflare accepts. It is unexported so it
// reaches no schema.
type aiSearchBody map[string]json.RawMessage

// decodeObject reads b as a JSON object. An empty body is an empty object.
func decodeObject(b []byte) (aiSearchBody, error) {
	if len(strings.TrimSpace(string(b))) == 0 {
		return aiSearchBody{}, nil
	}
	var m aiSearchBody
	if err := json.Unmarshal(b, &m); err != nil || m == nil {
		return nil, zip.ErrBadRequest("body must be a JSON object")
	}
	return m, nil
}

// placed checks a body's namespace fields against the derived namespace and
// removes them, so what is forwarded addresses the namespace by URL alone.
// `namespace` may repeat the derived name and nothing else; `new_namespace`, which
// is how Cloudflare moves an instance, is refused whatever it says.
func (b aiSearchBody) placed(derived string) error {
	if _, ok := b["new_namespace"]; ok {
		return zip.ErrBadRequest("an AI Search instance cannot change namespace in place: create a replacement instance, dual-write, backfill, then cut over")
	}
	if raw, ok := b["namespace"]; ok {
		var got string
		if err := json.Unmarshal(raw, &got); err != nil {
			return zip.ErrBadRequest("namespace must be a string")
		}
		if err := sameNamespace("namespace", got, derived); err != nil {
			return err
		}
		delete(b, "namespace")
	}
	return nil
}

// AISearchInstanceCreate is a new instance: its id, what it indexes, and every other
// field Cloudflare's create takes — the body's OWN BYTES travel, so a field not
// named here reaches Cloudflare unchanged. The three named fields are what a caller
// has to know, and the namespace is the reason this type exists.
type AISearchInstanceCreate struct {
	// ID is the instance name: 1 to 64 lowercase letters, digits, hyphens and
	// underscores. Cloudflare will not rename it later.
	ID string `json:"id"`
	// Type is what the instance indexes: "r2" for a bucket, "web-crawler" for a
	// site. Omitted lets Cloudflare infer it from Source.
	Type string `json:"type,omitempty"`
	// Source is the R2 bucket name, or the website URL, the instance indexes.
	Source string `json:"source,omitempty"`
	// Namespace may be omitted, and should be: the instance is created in the org's
	// derived namespace regardless. If present it must equal that name; any other
	// value, `default` included, is refused.
	Namespace string `json:"namespace,omitempty"`

	// body is the caller's own bytes, unexported so it reaches no schema.
	body json.RawMessage
}

// UnmarshalJSON keeps the caller's bytes and fills the named fields, and never
// refuses the body here: zip decodes before the handler runs, so a refusal here would
// outrank the admin gate. The handler re-decides in its own order — admin, then the
// body. (D1Query in d1.go is the same shape, and says why at more length.)
func (in *AISearchInstanceCreate) UnmarshalJSON(b []byte) error {
	type plain AISearchInstanceCreate // no methods, so no recursion
	var p plain
	_ = json.Unmarshal(b, &p)
	*in = AISearchInstanceCreate(p)
	in.body = append(json.RawMessage(nil), b...)
	return nil
}

// AISearchInstanceCreate creates an AI Search instance in the org's own namespace on
// its Cloudflare account and relays Cloudflare's record of it. The namespace is
// derived from the org and is not a parameter: a body naming another namespace,
// `default` included, is refused with 400 before anything reaches Cloudflare. Every
// other field is forwarded as written. Create the namespace first (POST
// /v1/cloudflare/ai-search/namespace). Requires org admin.
//
// Example: {"id": "support-docs", "type": "r2", "source": "support-docs"}
func (o ops) aiSearchInstanceCreate(ctx context.Context, in *AISearchInstanceCreate) (*cfResult, error) {
	cl, org, ns, err := o.searchScope(ctx, true)
	if err != nil {
		return nil, err
	}
	body, err := decodeObject(in.body)
	if err != nil {
		return nil, err
	}
	if err := body.placed(ns); err != nil {
		return nil, err
	}
	if !aiSearchInstanceRE.MatchString(in.ID) {
		return nil, zip.ErrBadRequest("id is required: 1 to 64 lowercase letters, digits, hyphens and underscores")
	}
	acct, err := cl.resolveAccount(ctx, org)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodPost, aiSearchPath(acct, ns)+"/instances", body)
}

// aiSearchInstanceRef addresses one instance in the org's namespace, from the path.
type aiSearchInstanceRef struct {
	// Instance is the AI Search instance id.
	Instance string `json:"instance"`
}

// AISearchInstanceGet reads one AI Search instance from the org's own namespace.
// An instance in any other namespace is not found. Any org member may read.
//
// Example: {"instance": "support-docs"}
func (o ops) aiSearchInstanceGet(ctx context.Context, in *aiSearchInstanceRef) (*cfResult, error) {
	cl, org, ns, err := o.searchScope(ctx, false)
	if err != nil {
		return nil, err
	}
	id, err := seg("instance", in.Instance, aiSearchInstanceRE)
	if err != nil {
		return nil, err
	}
	acct, err := cl.resolveAccount(ctx, org)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodGet, aiSearchPath(acct, ns)+"/instances/"+id, nil)
}

// AISearchInstanceUpdate changes an instance's configuration in place. The URL names
// the instance and the org names the namespace; every body field is forwarded as
// written, EXCEPT a namespace change, which is not an update.
type AISearchInstanceUpdate struct {
	// Instance is the AI Search instance id, from the path.
	Instance string `json:"-" url:"instance"`
	// Namespace may be omitted. If present it must equal the org's derived
	// namespace: an instance's namespace never changes in place.
	Namespace string `json:"namespace,omitempty" url:"-"`

	// body is the caller's own bytes, unexported so it reaches no schema.
	body json.RawMessage
}

// UnmarshalJSON keeps the caller's bytes; see AISearchInstanceCreate.UnmarshalJSON.
func (in *AISearchInstanceUpdate) UnmarshalJSON(b []byte) error {
	type plain AISearchInstanceUpdate // no methods, so no recursion
	var p plain
	_ = json.Unmarshal(b, &p)
	*in = AISearchInstanceUpdate(p)
	in.body = append(json.RawMessage(nil), b...)
	return nil
}

// AISearchInstanceUpdate updates an AI Search instance in the org's own namespace
// and relays Cloudflare's record of it. Every field is forwarded as written except
// the namespace: a `namespace` other than the org's own, or any `new_namespace`, is
// refused with 400, because moving an instance discards its index. A namespace
// change is a replacement — new instance, dual-write, backfill, cutover — never an
// update. Requires org admin.
//
// Example: {"instance": "support-docs", "max_num_results": 10}
func (o ops) aiSearchInstanceUpdate(ctx context.Context, in *AISearchInstanceUpdate) (*cfResult, error) {
	cl, org, ns, err := o.searchScope(ctx, true)
	if err != nil {
		return nil, err
	}
	id, err := seg("instance", in.Instance, aiSearchInstanceRE)
	if err != nil {
		return nil, err
	}
	body, err := decodeObject(in.body)
	if err != nil {
		return nil, err
	}
	if err := body.placed(ns); err != nil {
		return nil, err
	}
	acct, err := cl.resolveAccount(ctx, org)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodPut, aiSearchPath(acct, ns)+"/instances/"+id, body)
}

// AISearchInstanceDelete deletes an AI Search instance from the org's own namespace,
// and its index with it. Requires org admin.
//
// Example: {"instance": "support-docs"}
func (o ops) aiSearchInstanceDelete(ctx context.Context, in *aiSearchInstanceRef) (*cfResult, error) {
	cl, org, ns, err := o.searchScope(ctx, true)
	if err != nil {
		return nil, err
	}
	id, err := seg("instance", in.Instance, aiSearchInstanceRE)
	if err != nil {
		return nil, err
	}
	acct, err := cl.resolveAccount(ctx, org)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodDelete, aiSearchPath(acct, ns)+"/instances/"+id, nil)
}
