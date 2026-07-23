package cloudflare

// kv.go — Cloudflare Workers KV, WIRED (account-scoped: /accounts/{id}/storage/kv/
// namespaces). Namespace list is a read; namespace create/delete and every value
// write are mutations (authWrite → org admin); a value read is a read (authClient).
// A value is relayed RAW (getRaw) since a stored value is not the CF JSON envelope.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

func kvNamespaceList(s *cloud.Service[state], c *zip.Ctx) error {
	cl, acct, err := acctClient(s, c)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodGet, "/accounts/"+acct+"/storage/kv/namespaces"+query(c, "page", "per_page", "order", "direction"), nil)
}

func kvNamespaceCreate(s *cloud.Service[state], c *zip.Ctx) error {
	cl, acct, err := acctWrite(s, c)
	if err != nil {
		return err
	}
	var in struct {
		Title string `json:"title"`
	}
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		return zip.ErrBadRequest("namespace title is required")
	}
	return cl.pass(c, http.MethodPost, "/accounts/"+acct+"/storage/kv/namespaces", map[string]string{"title": title})
}

func kvNamespaceDelete(s *cloud.Service[state], c *zip.Ctx) error {
	cl, acct, err := acctWrite(s, c)
	if err != nil {
		return err
	}
	ns, err := pathSeg(c, "namespace", nameRE)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodDelete, "/accounts/"+acct+"/storage/kv/namespaces/"+ns, nil)
}

// kvValueGet relays a namespace key's raw value (getRaw — a stored value is bytes,
// not the CF envelope), with its content type. A missing key is Cloudflare's own 404.
func kvValueGet(s *cloud.Service[state], c *zip.Ctx) error {
	cl, acct, err := acctClient(s, c)
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
	data, ctype, err := cl.getRaw(c.Context(), "/accounts/"+acct+"/storage/kv/namespaces/"+ns+"/values/"+key)
	if err != nil {
		return cfErr(err)
	}
	c.SetHeader("Content-Type", ctype)
	return c.Bytes(http.StatusOK, data)
}

// kvValuePut writes a key's value: the request body IS the value (any content type),
// forwarded verbatim; optional expiration params ride the query. Mutation → org admin.
func kvValuePut(s *cloud.Service[state], c *zip.Ctx) error {
	cl, acct, err := acctWrite(s, c)
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
	if err := cl.cfUpload(c.Context(), http.MethodPut, path, ctype, c.Body(), &out); err != nil {
		return cfErr(err)
	}
	return writeResult(c, out)
}

func kvValueDelete(s *cloud.Service[state], c *zip.Ctx) error {
	cl, acct, err := acctWrite(s, c)
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
	return cl.pass(c, http.MethodDelete, "/accounts/"+acct+"/storage/kv/namespaces/"+ns+"/values/"+key, nil)
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
