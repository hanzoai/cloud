package cloudflare

// r2.go — Cloudflare R2, WIRED (account-scoped: /accounts/{id}/r2/buckets). List is a
// read (authClient); create + delete are mutations (authWrite → org admin). Each
// resolves the caller-org token and account id, then relays the CF response verbatim.

import (
	"context"
	"net/http"
	"strings"

	"github.com/zap-proto/zip"
)

// bucketsIn pages and filters the bucket list. Every field is optional and rides
// the query string; each is forwarded to Cloudflare under the same name.
type bucketsIn struct {
	// PerPage is how many buckets one page holds.
	PerPage string `json:"per_page"`
	// Cursor continues from the position a previous page returned.
	Cursor string `json:"cursor"`
	// NameContains filters to buckets whose name contains this substring.
	NameContains string `json:"name_contains"`
	// Order names the field to sort by, and Direction sorts asc or desc.
	Order     string `json:"order"`
	Direction string `json:"direction"`
}

// R2BucketList lists the R2 buckets on the org's Cloudflare account. Any org
// member may read.
func (o ops) r2BucketList(ctx context.Context, in *bucketsIn) (*cfResult, error) {
	cl, acct, err := o.acctClient(ctx)
	if err != nil {
		return nil, err
	}
	q := forward(map[string]string{
		"per_page": in.PerPage, "cursor": in.Cursor, "name_contains": in.NameContains,
		"order": in.Order, "direction": in.Direction,
	})
	return cl.relay(ctx, http.MethodGet, "/accounts/"+acct+"/r2/buckets"+q, nil)
}

// bucketCreateIn names a new R2 bucket.
type bucketCreateIn struct {
	// Name is the bucket name to create.
	Name string `json:"name"`
}

// R2BucketCreate creates an R2 bucket on the org's Cloudflare account. Requires
// org admin.
//
// Example: {"name": "assets"}
func (o ops) r2BucketCreate(ctx context.Context, in *bucketCreateIn) (*cfResult, error) {
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if !nameRE.MatchString(name) {
		return nil, zip.ErrBadRequest("bucket name is invalid")
	}
	return cl.relay(ctx, http.MethodPost, "/accounts/"+acct+"/r2/buckets", map[string]string{"name": name})
}

// bucketRef addresses one R2 bucket by name, from the path.
type bucketRef struct {
	// Bucket is the R2 bucket name.
	Bucket string `json:"bucket"`
}

// R2BucketDelete deletes an R2 bucket. Requires org admin. Cloudflare refuses a
// bucket that still holds objects, and that refusal is relayed.
//
// Example: {"bucket": "assets"}
func (o ops) r2BucketDelete(ctx context.Context, in *bucketRef) (*cfResult, error) {
	cl, acct, err := o.acctWrite(ctx)
	if err != nil {
		return nil, err
	}
	bucket, err := seg("bucket", in.Bucket, nameRE)
	if err != nil {
		return nil, err
	}
	return cl.relay(ctx, http.MethodDelete, "/accounts/"+acct+"/r2/buckets/"+bucket, nil)
}
