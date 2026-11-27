package cloudflare

// kv.go — Cloudflare Workers KV, WIRED (account-scoped: /accounts/{id}/storage/kv/
// namespaces). Namespace list is a read; namespace create/delete and every value
// write are mutations (authWrite → org admin); a value read is a read (authClient).
// A value is relayed RAW (getRaw) since a stored value is not the CF JSON envelope.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/zap-proto/zip"
)

// namespacesIn pages and sorts the namespace list. Every field is optional and
// rides the query string; each is forwarded to Cloudflare under the same name.
type namespacesIn struct {
	// Page is the 1-based page of namespaces to return.
	Page string `json:"page"`
	// PerPage is how many namespaces one page holds.
	PerPage string `json:"per_page"`
	// Order names the field to sort by, and Direction sorts asc or desc.
	Order     string `json:"order"`
	Direction string `json:"direction"`
}

// KVNamespaceList lists the Workers KV namespaces on the org's Cloudflare
// account. Any org member may read.
func (o ops) kvNamespaceList(ctx context.Context, in *namespacesIn) (*cfResult, error) {
	cl, acct, err := o.acctClient(ctx)
	if err != nil {
		return nil, err
	}
	q := forward(map[string]string{
		"page": in.Page, "per_page": in.PerPage, "order": in.Order, "direction": in.Direction,
	})
	return cl.relay(ctx, http.MethodGet, "/accounts/"+acct+"/storage/kv/namespaces"+q, nil)
}

// namespaceCreateIn titles a new KV namespace.
type namespaceCreateIn struct {
	// Title is the namespace's display title. Cloudflare mints the id.
	Title string `json:"title"`
}

// KVNamespaceCreate creates a Workers KV namespace on the org's Cloudflare
// account. Requires org admin. Cloudflare mints the namespace id the value routes
// address.
//
// Example: {"title": "sessions"}
func (o ops) kvNamespaceCreate(ctx context.Context, in *namespaceCreateIn) (*cfResult, error) {
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return nil, zip.ErrBadRequest("namespace title is required")
	}
	return cl.relay(ctx, http.MethodPost, "/accounts/"+acct+"/storage/kv/namespaces",
		map[string]string{"title": title})
}

// namespaceRef addresses one KV namespace by id, from the path.
type namespaceRef struct {
	// Namespace is the Cloudflare KV namespace id.
	Namespace string `json:"namespace"`
}

// KVNamespaceDelete deletes a Workers KV namespace and every key in it. Requires
// org admin.
//
// Example: {"namespace": "0123456789abcdef0123456789abcdef"}
func (o ops) kvNamespaceDelete(ctx context.Context, in *namespaceRef) (*cfResult, error) {
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := seg("namespace", in.Namespace, nameRE)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodDelete, "/accounts/"+acct+"/storage/kv/namespaces/"+ns, nil)
}

// kvValueGet relays a namespace key's raw value (getRaw — a stored value is bytes,
// not the CF envelope), with its content type. A missing key is Cloudflare's own 404.
//
// NOT a typed op: a KV value is opaque bytes under whatever content type it was
// written with, and a typed op answers JSON. Typing it would re-encode a stored
// value into a JSON document.
func (o ops) kvValueGet(c *zip.Ctx) error {
	ctx := c.Context()
	cl, acct, err := o.acctClient(ctx)
	if err != nil {
		return err
	}
	ns, err := pathSeg(c, "namespace", nameRE)
	if err != nil {
		return err
	}
	key, err := kvKeySeg(c.Param("key"))
	if err != nil {
		return err
	}
	data, ctype, err := cl.getRaw(ctx, "/accounts/"+acct+"/storage/kv/namespaces/"+ns+"/values/"+key)
	if err != nil {
		return cfErr(err)
	}
	c.SetHeader("Content-Type", ctype)
	return c.Bytes(http.StatusOK, data)
}

// kvValuePut writes a key's value: the request body IS the value (any content type),
// forwarded verbatim; optional expiration params ride the query. Mutation → org admin.
//
// NOT a typed op: the request body IS the stored value, under the caller's own
// content type. A typed In would parse it as JSON and refuse everything else.
func (o ops) kvValuePut(c *zip.Ctx) error {
	ctx := c.Context()
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return err
	}
	ns, err := pathSeg(c, "namespace", nameRE)
	if err != nil {
		return err
	}
	key, err := kvKeySeg(c.Param("key"))
	if err != nil {
		return err
	}
	ctype := strings.TrimSpace(c.Header("Content-Type"))
	if ctype == "" {
		ctype = "text/plain"
	}
	var out json.RawMessage
	path := "/accounts/" + acct + "/storage/kv/namespaces/" + ns + "/values/" + key + query(c, "expiration", "expiration_ttl")
	if err := cl.cfUpload(ctx, http.MethodPut, path, ctype, c.Body(), &out); err != nil {
		return cfErr(err)
	}
	return writeResult(c, out)
}

// valueRef addresses one key in one KV namespace, both from the path.
type valueRef struct {
	// Namespace is the Cloudflare KV namespace id.
	Namespace string `json:"namespace"`
	// Key is the key within that namespace. KV keys are broad (up to 512 bytes),
	// so this one is escaped rather than charset-restricted.
	Key string `json:"key"`
}

// KVValueDelete removes one key from a Workers KV namespace. Requires org admin.
//
// Example: {"namespace": "0123456789abcdef0123456789abcdef", "key": "session/abc"}
func (o ops) kvValueDelete(ctx context.Context, in *valueRef) (*cfResult, error) {
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return nil, err
	}
	ns, err := seg("namespace", in.Namespace, nameRE)
	if err != nil {
		return nil, err
	}
	key, err := kvKeySeg(in.Key)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodDelete, "/accounts/"+acct+"/storage/kv/namespaces/"+ns+"/values/"+key, nil)
}

// kvKeySeg validates + url-escapes a KV key for the value path segment. KV keys are
// broad (≤512 bytes, many chars), so unlike a CF id they are ESCAPED rather than
// charset-restricted: url.PathEscape turns any "/" into %2F so the key stays ONE path
// segment and cannot inject path structure. A dot-only key ("."/"..") is refused
// outright — PathEscape leaves dots intact, so it could otherwise walk up to the
// namespace endpoint — as are empty, over-long, non-UTF-8, and control-char keys.
func kvKeySeg(raw string) (string, error) {
	// Do NOT trim: a KV key may legitimately carry leading/trailing spaces.
	if raw == "" || len(raw) > 512 || raw == "." || raw == ".." {
		return "", zip.ErrBadRequest("key is invalid")
	}
	if !utf8.ValidString(raw) {
		return "", zip.ErrBadRequest("key is invalid")
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			return "", zip.ErrBadRequest("key is invalid")
		}
	}
	return url.PathEscape(raw), nil
}
