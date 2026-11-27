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
	"errors"
	"fmt"
	"github.com/hanzoai/cloud/sdk/object"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/apps/sites"
)

const (
	// touchAge is how far an immutable key's LastModified may trail the last live
	// release that named it. A completion re-stamps every immutable key of its
	// manifest older than this, so LastModified reads as "last named by a live
	// release, give or take touchAge" — and a site that publishes every few
	// minutes re-stamps each key twice a day rather than on every publish.
	touchAge = 12 * time.Hour
	// graceAge is how long an immutable key a release dropped stays in the prefix,
	// counted from its LastModified: the 24 hours a tab opened on the previous
	// release may stay open and still lazy-load its chunks, plus the touchAge by
	// which that LastModified can trail the release. 36 h = 12 h + 24 h.
	graceAge = touchAge + 24*time.Hour
	// grantTTL bounds a grant's life. Long enough to upload a large export over a
	// slow runner link, short enough that a grant found later is dead.
	grantTTL = 30 * time.Minute
	// grantMaxObjectBytes bounds ONE object. It matches the artifact path's
	// per-file cap so both routes into the same prefix agree on what is too big.
	grantMaxObjectBytes = maxFileBytes
)

// projectsUploadGrant is a short-lived, prefix-scoped permission to write a site's
// objects straight to S3. Fields are the form values a POST must carry verbatim
// alongside `key` and `file`; the signature covers them, so changing any of them
// — including widening the key — invalidates the grant.
type projectsUploadGrant struct {
	// URL is the address to POST each object to. It is signed for the PUBLIC
	// endpoint, because the signature covers the host and CI posts from outside the
	// cluster.
	URL string `json:"url"`
	// Fields are form values every POST must carry VERBATIM, alongside `key` and
	// `file`. The signature covers them, so altering any one of them — including
	// widening the key to reach outside the prefix — invalidates the grant rather
	// than extending it.
	Fields map[string]string `json:"fields"`
	// Prefix is the only place this grant can write: the deployment's own key
	// prefix. It authorizes WRITES ONLY, which is why completing a deployment
	// reconciles the prefix against a manifest instead of letting CI delete.
	Prefix string `json:"prefix"`
	// ExpiresAt is when the grant stops being accepted, as Unix seconds. It is
	// short-lived by design and is handed out ONCE, on the response that queues the
	// deployment — a later read of that deployment does not carry it, so a grant
	// cannot be fetched again after the build it was minted for.
	ExpiresAt int64 `json:"expiresAt"`
	// MaxBytes bounds ONE object, not the upload as a whole.
	MaxBytes int64 `json:"maxBytes"`
	// Copy is true when the completion of this deployment accepts copyFrom and
	// copy. A client that does not see it uploads every object itself, since a
	// server that ignores those fields would leave the copied keys missing.
	Copy bool `json:"copy,omitempty"`
}

// mintGrant issues a grant for exactly one site's prefix.
//
// It signs against the PUBLIC endpoint, because CI resolves s3.hanzo.ai from
// outside the cluster and the signature covers the host — a grant signed for the
// in-cluster admin endpoint would be rejected when posted to the public one.
// Returns nil (no error) when presigning is unavailable, so a deployment is still
// created and the response simply carries no grant.
func mintGrant(ctx context.Context, b *blobStore, prefix string, now time.Time) (*projectsUploadGrant, error) {
	if b == nil {
		return nil, nil
	}
	expires := now.Add(grantTTL)
	// THE containment is the prefix, and the trailing slash matters: without it
	// "acme/site" would also authorize "acme/site-other", handing one tenant a
	// neighbouring prefix. The form signs the content type too (any value), so each
	// object is served with the type the publisher states.
	f, err := b.store.Form(ctx, object.Form{Bucket: b.bucket, Prefix: prefix + "/", TTL: grantTTL, MaxBytes: grantMaxObjectBytes})
	if errors.Is(err, object.ErrNoPresign) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("presign post policy: %w", err)
	}
	return &projectsUploadGrant{
		URL: f.URL, Fields: f.Fields, Prefix: prefix,
		ExpiresAt: expires.Unix(), MaxBytes: grantMaxObjectBytes, Copy: true,
	}, nil
}

// projectsObject is one object a site's prefix holds, as the object store itself
// records it.
type projectsObject struct {
	// Key is the object's path RELATIVE to the deployment prefix — the form an
	// upload names it by and a completion's `keys` lists it in.
	Key string `json:"key"`
	// Size is the object's length in bytes.
	Size int64 `json:"size"`
	// ETag is the store's content identity with its quotes removed. For an object
	// written in one part it is the lowercase hex MD5 of the bytes, so a publisher
	// compares it with its own file's digest; a multipart upload's ends in
	// `-<parts>` and equals no single MD5, so it reads as different.
	ETag string `json:"etag"`
	// modified is the store's LastModified. It is not published: only the
	// reconcile reads it, to tell a key a live release named lately from one
	// none has.
	modified time.Time
}

// listPrefix is the store's own record of everything under prefix: each object's
// key relative to it, its size and its ETag. It is the ONE listing of a site's
// prefix — what a publisher diffs its export against, what a completion proves
// its manifest present by, and what the reconcile tidies.
//
// It is whole or it is an error. A listing the store ends early, or a
// cancelled context, is reported here rather than returned as the half it got: a
// partial listing names present keys absent.
//
// The trailing slash keeps it inside one site: "acme/site" must not list
// "acme/site-two".
func listPrefix(ctx context.Context, store object.Store, bucket, prefix string) ([]projectsObject, error) {
	objs := []projectsObject{}
	for obj, err := range store.List(ctx, bucket, object.List{Prefix: prefix + "/"}) {
		if object.NoBucket(err) {
			return objs, nil // a bucket nobody has deployed into yet holds nothing
		}
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", prefix, err)
		}
		if rel := obj.Key[min(len(prefix)+1, len(obj.Key)):]; rel != "" {
			objs = append(objs, projectsObject{Key: rel, Size: obj.Size, ETag: obj.ETag, modified: obj.Modified})
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("list %s: %w", prefix, err)
	}
	return objs, nil
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
func reconcilePrefix(ctx context.Context, cli object.Store, bucket, prefix string, keep map[string]bool) (int, error) {
	if len(keep) == 0 {
		return 0, nil
	}
	held, err := listPrefix(ctx, cli, bucket, prefix)
	if err != nil {
		return 0, err
	}
	return tidy(ctx, cli, bucket, prefix, held, keep)
}

// tidy is reconcilePrefix over a listing already taken, so a completion
// that listed the prefix to prove its manifest present tidies that same
// listing and lists it once. It fails closed on an empty keep for the reason
// reconcilePrefix does.
//
// An immutable key (sites.Immutable: a fingerprinted asset the edge caches for
// a year) outlives the release that dropped it by graceAge. A tab opened on the
// previous release still lazy-loads its chunks by their old names, and deleting
// one the moment a new release lands breaks that page mid-session. So the keys
// keep names are re-stamped first (touch), and a dropped immutable key whose
// LastModified is within graceAge stays; every other dropped key is deleted.
// A failed re-stamp is returned before anything is deleted: the release is live
// either way, and stale files left behind are the safe half of a reconcile that
// did not finish.
func tidy(ctx context.Context, cli object.Store, bucket, prefix string, held []projectsObject, keep map[string]bool) (int, error) {
	if len(keep) == 0 {
		return 0, nil
	}
	now := time.Now()
	if err := touch(ctx, cli, bucket, prefix, held, keep, now); err != nil {
		return 0, err
	}
	var stale []string
	for _, o := range held {
		if keep[o.Key] || sites.Immutable(o.Key) && now.Sub(o.modified) < graceAge {
			continue
		}
		stale = append(stale, prefix+"/"+o.Key)
	}
	if len(stale) == 0 {
		return 0, nil
	}
	if err := cli.RemoveMany(ctx, bucket, stale); err != nil {
		return 0, fmt.Errorf("remove: %w", err)
	}
	return len(stale), nil
}

// touch moves LastModified to now on every immutable key keep names whose
// LastModified in held is older than touchAge, by copying each object onto
// itself. S3 refuses a self-copy that changes nothing, so the copy REPLACEs the
// metadata — with the Content-Type, Cache-Control and user metadata the object
// already carries, read first — and the bytes and how they are served stay as
// they were.
func touch(ctx context.Context, cli object.Store, bucket, prefix string, held []projectsObject, keep map[string]bool, now time.Time) error {
	var due []string
	for _, o := range held {
		if keep[o.Key] && sites.Immutable(o.Key) && now.Sub(o.modified) > touchAge {
			due = append(due, o.Key)
		}
	}
	_, err := fan(ctx, due, func(ctx context.Context, k string) error {
		key := prefix + "/" + k
		info, err := cli.Stat(ctx, bucket, key)
		if err != nil {
			return err
		}
		ct := info.ContentType
		if ct == "" {
			ct = sites.ContentType(k)
		}
		ref := object.Ref{Bucket: bucket, Key: key}
		_, err = cli.Copy(ctx, ref, ref, object.Copy{Replace: true, Meta: info.Meta, ContentType: ct, CacheControl: info.CacheControl})
		return err
	})
	return err
}

// validKey reports whether k may address an object inside a prefix: relative,
// already clean, and not climbing out of it. Both prefixes of a copy are the
// caller's own org, and the key is the only part of either address the caller
// writes.
func validKey(k string) bool {
	return k != "" && !strings.HasPrefix(k, "/") && path.Clean(k) == k && !strings.HasPrefix(k, "../")
}

// copyKeys copies each key from srcPrefix to dstPrefix inside one bucket, 32 at a
// time, and reports how many it finished. A key validKey refuses is refused
// before anything is copied.
func copyKeys(ctx context.Context, cli object.Store, bucket, srcPrefix, dstPrefix string, keys []string) (int, error) {
	for _, k := range keys {
		if !validKey(k) {
			return 0, fmt.Errorf("invalid key %q", k)
		}
	}
	return fan(ctx, keys, func(ctx context.Context, k string) error {
		_, err := cli.Copy(ctx, object.Ref{Bucket: bucket, Key: dstPrefix + "/" + k}, object.Ref{Bucket: bucket, Key: srcPrefix + "/" + k}, object.Copy{})
		return err
	})
}

// fan runs do for each key, 32 at a time, and reports how many finished. The
// first failure cancels the rest and is returned named by its key.
func fan(ctx context.Context, keys []string, do func(context.Context, string) error) (int, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu    sync.Mutex
		done  int
		first error
		wg    sync.WaitGroup
		sem   = make(chan struct{}, 32)
	)
	for _, k := range keys {
		sem <- struct{}{}
		wg.Add(1)
		go func(k string) {
			defer func() { <-sem; wg.Done() }()
			err := do(ctx, k)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if first == nil {
					first = fmt.Errorf("%s: %w", k, err)
					cancel()
				}
				return
			}
			done++
		}(k)
	}
	wg.Wait()
	return done, first
}
