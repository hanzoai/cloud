// grant.go — how CI gets bytes into a site's S3 prefix WITHOUT holding a bucket
// credential.
//
// The git/CI deploy path cannot stream its artifact through the API: a real docs
// export is ~170 MB against a 16 MiB BodyLimit, so fasthttp refuses the POST
// before any handler runs. CI therefore writes to S3 directly — and the question
// is what it writes with.
//
// It used to be the bucket's OWN long-lived access key, handed to each repo as a
// secret. That is one credential for ONE SHARED BUCKET whose only tenant
// separation is the key prefix (blob.go: sitePrefix = "<org>/<slug>"), so every
// repo holding it could overwrite EVERY org's site, and the blast radius grew
// with each site onboarded. The credential also never expired and lived in as
// many places as there were repos.
//
// A presigned POST policy replaces it. The deploy call that already proved who
// you are hands back a grant that is:
//
//   - PREFIX-SCOPED   the policy carries starts-with($key, "<org>/<slug>/"), so
//     S3 itself refuses a write anywhere else. Not a convention
//     CI is trusted to honour — a condition the server enforces.
//   - SHORT-LIVED     it expires in grantTTL, so a leaked CI log is worthless
//     shortly after the build that produced it.
//   - SIZE-BOUNDED    per-object, so a grant cannot be turned into unbounded
//     storage spend.
//
// No standing secret, nothing to rotate, nothing to leak between tenants. It also
// needs no STS: presigning is a signature computed with the key cloud already
// holds, not a token exchange — which matters, because s3.hanzo.ai does not
// expose an STS endpoint (an AssumeRole POST answers 405).
package projects

import (
	"context"
	"fmt"
	"time"

	s3 "github.com/hanzoai/s3-go"
)

const (
	// grantTTL bounds a grant's life. Long enough to upload a large export over a
	// slow runner link, short enough that a grant found later is dead.
	grantTTL = 30 * time.Minute
	// grantMaxObjectBytes bounds ONE object. It matches the artifact path's
	// per-file cap so both routes into the same prefix agree on what is too big.
	grantMaxObjectBytes = maxFileBytes
)

// uploadGrant is a short-lived, prefix-scoped permission to write a site's
// objects straight to S3. Fields are the form values a POST must carry verbatim
// alongside `key` and `file`; the signature covers them, so changing any of them
// — including widening the key — invalidates the grant.
type uploadGrant struct {
	URL       string            `json:"url"`
	Fields    map[string]string `json:"fields"`
	Prefix    string            `json:"prefix"`
	ExpiresAt int64             `json:"expiresAt"`
	MaxBytes  int64             `json:"maxBytes"`
}

// mintUploadGrant issues a grant for exactly one site's prefix.
//
// It signs against the PUBLIC endpoint, because CI resolves s3.hanzo.ai from
// outside the cluster and the signature covers the host — a grant signed for the
// in-cluster admin endpoint would be rejected when posted to the public one.
// Returns nil (no error) when presigning is unavailable, so a deployment is still
// created and the response simply carries no grant.
func mintUploadGrant(ctx context.Context, b *blobStore, prefix string, now time.Time) (*uploadGrant, error) {
	if b == nil || !b.admin.PresignConfigured() {
		return nil, nil
	}
	cli, err := b.admin.PublicClient()
	if err != nil {
		return nil, fmt.Errorf("presign client: %w", err)
	}
	expires := now.Add(grantTTL)

	p := s3.NewPostPolicy()
	if err := p.SetBucket(b.bucket); err != nil {
		return nil, fmt.Errorf("policy bucket: %w", err)
	}
	// THE containment. The trailing slash matters: without it "acme/site" would
	// also authorize "acme/site-other", handing one tenant a neighbouring prefix.
	if err := p.SetKeyStartsWith(prefix + "/"); err != nil {
		return nil, fmt.Errorf("policy prefix: %w", err)
	}
	if err := p.SetExpires(expires); err != nil {
		return nil, fmt.Errorf("policy expiry: %w", err)
	}
	if err := p.SetContentLengthRange(0, grantMaxObjectBytes); err != nil {
		return nil, fmt.Errorf("policy size: %w", err)
	}

	u, fields, err := cli.PresignedPostPolicy(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("presign post policy: %w", err)
	}
	return &uploadGrant{
		URL: u.String(), Fields: fields, Prefix: prefix,
		ExpiresAt: expires.Unix(), MaxBytes: grantMaxObjectBytes,
	}, nil
}

// reconcilePrefix deletes every object under prefix that is NOT in keep, and
// reports how many it removed.
//
// This is where `aws s3 sync --delete` went. CI can no longer delete — the grant
// authorizes writes only — so the authority to remove a file moved to the server,
// which is where it belonged: deletion is now a decision cloud makes from a
// manifest, not something a build script can do to a prefix on its own.
//
// keep holds keys RELATIVE to prefix, exactly as CI uploaded them.
//
// It fails CLOSED on an empty manifest: a build that reports nothing has almost
// certainly failed to enumerate its output, and honouring that literally would
// delete the entire live site. Serving a stale file is recoverable; deleting a
// site because a shell pipeline produced no lines is not.
func reconcilePrefix(ctx context.Context, cli *s3.Client, bucket, prefix string, keep map[string]bool) (int, error) {
	if len(keep) == 0 {
		return 0, nil
	}
	objCh := cli.ListObjects(ctx, bucket, s3.ListObjectsOptions{Prefix: prefix + "/", Recursive: true})
	var stale []s3.ObjectInfo
	for obj := range objCh {
		if obj.Err != nil {
			return 0, fmt.Errorf("list %s: %w", prefix, obj.Err)
		}
		rel := obj.Key[min(len(prefix)+1, len(obj.Key)):]
		if rel == "" || keep[rel] {
			continue
		}
		stale = append(stale, obj)
	}
	if len(stale) == 0 {
		return 0, nil
	}
	toDelete := make(chan s3.ObjectInfo)
	go func() {
		defer close(toDelete)
		for _, o := range stale {
			select {
			case toDelete <- o:
			case <-ctx.Done():
				return
			}
		}
	}()
	for rmErr := range cli.RemoveObjects(ctx, bucket, toDelete, s3.RemoveObjectsOptions{}) {
		if rmErr.Err != nil {
			return 0, fmt.Errorf("remove %s: %w", rmErr.ObjectName, rmErr.Err)
		}
	}
	return len(stale), nil
}
