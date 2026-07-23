package cloudflare

// r2.go — Cloudflare R2, WIRED (account-scoped: /accounts/{id}/r2/buckets). List is a
// read (authClient); create + delete are mutations (authWrite → org admin). Each
// resolves the caller-org token and account id, then relays the CF response verbatim.

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"
)

func r2BucketList(s *cloud.Service[state], c *zip.Ctx) error {
	cl, acct, err := acctClient(s, c)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodGet, "/accounts/"+acct+"/r2/buckets"+query(c, "per_page", "cursor", "name_contains", "order", "direction"), nil)
}

func r2BucketCreate(s *cloud.Service[state], c *zip.Ctx) error {
	cl, acct, err := acctWrite(s, c)
	if err != nil {
		return err
	}
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	name := strings.TrimSpace(in.Name)
	if !nameRE.MatchString(name) {
		return zip.ErrBadRequest("bucket name is invalid")
	}
	return cl.pass(c, http.MethodPost, "/accounts/"+acct+"/r2/buckets", map[string]string{"name": name})
}

func r2BucketDelete(s *cloud.Service[state], c *zip.Ctx) error {
	cl, acct, err := acctWrite(s, c)
	if err != nil {
		return err
	}
	bucket, err := pathSeg(c, "bucket", nameRE)
	if err != nil {
		return err
	}
	return cl.pass(c, http.MethodDelete, "/accounts/"+acct+"/r2/buckets/"+bucket, nil)
}
