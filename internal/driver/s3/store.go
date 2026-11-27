package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud/sdk/object"
	hs3 "github.com/hanzos3/go"
)

// store is sdk/object's Store over the Hanzo S3 gateway.
type store struct {
	admin Admin
	cli   *hs3.Client
}

// Open builds the store from a. It does not dial: a client is configuration.
func Open(a Admin) (object.Store, error) {
	cli, err := a.Client()
	if err != nil {
		return nil, err
	}
	return &store{admin: a, cli: cli}, nil
}

// The driver registers itself: S3_ADMIN_ACCESS_KEY/SECRET_KEY select it.
func init() {
	object.Register(object.Driver{Name: "hanzo-s3", Open: func() (object.Store, bool, error) {
		a := New()
		if !a.Configured() {
			return nil, false, nil
		}
		s, err := Open(a)
		return s, err == nil, err
	}})
}

// Delegate hands the admin credential to a process that writes on its own.
func (s *store) Delegate() (object.Delegation, bool) { return s.admin.Delegate() }

func (s *store) Ensure(ctx context.Context, bucket string) error {
	ok, err := s.cli.BucketExists(ctx, bucket)
	if err == nil && ok {
		return nil
	}
	if err == nil {
		err = s.cli.MakeBucket(ctx, bucket, hs3.MakeBucketOptions{Region: s.admin.region})
	}
	if err != nil {
		// A bucket made by someone else in the meantime is the outcome asked for.
		if ex, e2 := s.cli.BucketExists(ctx, bucket); e2 == nil && ex {
			return nil
		}
		return wrap(err)
	}
	return nil
}

func (s *store) Has(ctx context.Context, bucket string) (bool, error) {
	ok, err := s.cli.BucketExists(ctx, bucket)
	return ok, wrap(err)
}

func (s *store) Buckets(ctx context.Context) ([]object.Bucket, error) {
	all, err := s.cli.ListBuckets(ctx)
	if err != nil {
		return nil, wrap(err)
	}
	out := make([]object.Bucket, len(all))
	for i, b := range all {
		out[i] = object.Bucket{Name: b.Name, Created: b.CreationDate}
	}
	return out, nil
}

func (s *store) Drop(ctx context.Context, bucket string) error {
	return wrap(s.cli.RemoveBucket(ctx, bucket))
}

func (s *store) Put(ctx context.Context, bucket, key string, body io.Reader, size int64, o object.Put) (object.Info, error) {
	opts := hs3.PutObjectOptions{ContentType: o.ContentType, CacheControl: o.CacheControl, UserMetadata: o.Meta, PartSize: uint64(max(o.PartSize, 0))}
	switch {
	case o.IfAbsent:
		opts.SetMatchETagExcept("*")
	case o.IfMatch != "":
		opts.SetMatchETag(strings.Trim(o.IfMatch, `"`))
	}
	up, err := s.cli.PutObject(ctx, bucket, key, body, size, opts)
	if err != nil {
		err = wrap(err)
		// A conditional write that raced another is refused with 409 on AWS S3
		// whatever the code says; nothing was written, which is all a lost
		// compare-and-set means.
		if resp, ok := errors.AsType[hs3.ErrorResponse](err); ok && resp.StatusCode == 409 && (o.IfAbsent || o.IfMatch != "") && !object.Absent(err) && !object.NotEmpty(err) && !object.Conflict(err) {
			err = fmt.Errorf("%w: %w", object.ErrConflict, err)
		}
		return object.Info{}, err
	}
	return object.Info{Key: key, Size: up.Size, ETag: strings.Trim(up.ETag, `"`), ContentType: o.ContentType, CacheControl: o.CacheControl, Meta: o.Meta, Modified: up.LastModified}, nil
}

func (s *store) Open(ctx context.Context, bucket, key string, o object.Get) (*object.Body, error) {
	var opts hs3.GetObjectOptions
	if o.Ranged {
		var err error
		switch {
		case o.Last >= o.First:
			err = opts.SetRange(o.First, o.Last)
		case o.First > 0: // to the end
			err = opts.SetRange(o.First, 0)
		}
		if err != nil {
			return nil, err
		}
	}
	// Core.GetObject is one request whose answer is the status, the headers and
	// the body asked for. Client.GetObject is a lazy reader that re-issues its own
	// requests from the current offset, which drops a range on the second read.
	obj, st, _, err := s.core().GetObject(ctx, bucket, key, opts)
	if err != nil {
		return nil, s.classify(ctx, wrap(err), bucket, key)
	}
	in := info(st)
	if o.Ranged && o.Last >= o.First {
		// The response is the part, so its length is the part's. The reader is held
		// to the range as well, for a store that answers with more.
		n := min(o.Last-o.First+1, st.Size)
		in.Size = n
		return &object.Body{ReadCloser: limited{io.LimitReader(obj, n), obj}, Info: in}, nil
	}
	return &object.Body{ReadCloser: obj, Info: in}, nil
}

type limited struct {
	io.Reader
	c io.Closer
}

func (l limited) Close() error { return l.c.Close() }

func (s *store) Stat(ctx context.Context, bucket, key string) (object.Info, error) {
	st, err := s.cli.StatObject(ctx, bucket, key, hs3.StatObjectOptions{})
	if err != nil {
		return object.Info{}, wrap(err)
	}
	return info(st), nil
}

// info renders the client's record. User metadata arrives as x-amz-meta-* headers.
func info(st hs3.ObjectInfo) object.Info {
	out := object.Info{
		Key: st.Key, Size: st.Size, ETag: strings.Trim(st.ETag, `"`),
		ContentType: st.ContentType, Modified: st.LastModified,
		CacheControl: st.Metadata.Get("Cache-Control"),
	}
	for k, v := range st.Metadata {
		if name, ok := strings.CutPrefix(http.CanonicalHeaderKey(k), "X-Amz-Meta-"); ok && len(v) > 0 {
			if out.Meta == nil {
				out.Meta = map[string]string{}
			}
			out.Meta[strings.ToLower(name)] = v[0]
		}
	}
	if len(out.Meta) == 0 {
		for k, v := range st.UserMetadata {
			if out.Meta == nil {
				out.Meta = map[string]string{}
			}
			out.Meta[strings.ToLower(k)] = v
		}
	}
	return out
}

func (s *store) Remove(ctx context.Context, bucket, key string) error {
	err := wrap(s.cli.RemoveObject(ctx, bucket, key, hs3.RemoveObjectOptions{}))
	if object.NoKey(err) {
		return nil
	}
	return err
}

func (s *store) RemoveMany(ctx context.Context, bucket string, keys []string) error {
	ch := make(chan hs3.ObjectInfo)
	go func() {
		defer close(ch)
		for _, k := range keys {
			select {
			case ch <- hs3.ObjectInfo{Key: k}:
			case <-ctx.Done():
				return
			}
		}
	}()
	var first error
	for rm := range s.cli.RemoveObjects(ctx, bucket, ch, hs3.RemoveObjectsOptions{}) {
		if rm.Err != nil && first == nil {
			first = wrap(rm.Err)
		}
	}
	if first == nil {
		first = ctx.Err()
	}
	return first
}

func (s *store) List(ctx context.Context, bucket string, o object.List) iter.Seq2[object.Info, error] {
	return func(yield func(object.Info, error) bool) {
		var level []object.Info
		n := 0
		for obj := range s.cli.ListObjectsIter(ctx, bucket, hs3.ListObjectsOptions{Prefix: o.Prefix, Recursive: !o.Shallow}) {
			if obj.Err != nil {
				yield(object.Info{}, wrap(obj.Err))
				return
			}
			in := info(obj)
			if o.Shallow {
				if strings.HasSuffix(obj.Key, "/") && obj.Key != o.Prefix {
					in = object.Info{Key: obj.Key, Dir: true}
				}
				// One level is small, and stores disagree on whether its objects
				// come before its prefixes; the contract is key order.
				level = append(level, in)
				continue
			}
			if o.Max > 0 && n >= o.Max {
				return
			}
			n++
			if !yield(in, nil) {
				return
			}
		}
		// The client ends a listing silently when its context is cancelled; a half
		// listing must never pass for a whole one.
		if err := ctx.Err(); err != nil {
			yield(object.Info{}, err)
			return
		}
		slices.SortFunc(level, func(a, b object.Info) int { return strings.Compare(a.Key, b.Key) })
		for _, in := range level {
			if o.Max > 0 && n >= o.Max {
				return
			}
			n++
			if !yield(in, nil) {
				return
			}
		}
	}
}

func (s *store) Copy(ctx context.Context, dst, src object.Ref, o object.Copy) (object.Info, error) {
	d := hs3.CopyDestOptions{Bucket: dst.Bucket, Object: dst.Key}
	if o.Replace {
		d.ReplaceMetadata = true
		d.UserMetadata = o.Meta
		d.ContentType = o.ContentType
		d.CacheControl = o.CacheControl
	}
	up, err := s.cli.CopyObject(ctx, d, hs3.CopySrcOptions{Bucket: src.Bucket, Object: src.Key, MatchETag: strings.Trim(o.IfMatch, `"`)})
	if err != nil {
		return object.Info{}, s.classify(ctx, wrap(err), src.Bucket, src.Key)
	}
	return object.Info{Key: dst.Key, Size: up.Size, ETag: strings.Trim(up.ETag, `"`), Modified: up.LastModified}, nil
}

// classify turns a refusal that does not say what it was into the one that does,
// when the object is the cause. Stores word a missing object differently (one
// answers 400 to a copy of it, another 500 to a read), and what the caller needs
// is that it is missing.
func (s *store) classify(ctx context.Context, err error, bucket, key string) error {
	if object.Absent(err) || object.Conflict(err) {
		return err
	}
	if _, e := s.Stat(ctx, bucket, key); object.Absent(e) {
		return e
	}
	return err
}

func (s *store) Share(ctx context.Context, sh object.Share) (object.Link, error) {
	pub, err := s.admin.PublicClient()
	if err != nil {
		return object.Link{}, err
	}
	var u *url.URL
	switch {
	case len(sh.Header) > 0:
		u, err = pub.PresignHeader(ctx, orGet(sh.Method), sh.Bucket, sh.Key, sh.TTL, sh.Query, sh.Header)
	case orGet(sh.Method) == http.MethodGet:
		u, err = pub.PresignedGetObject(ctx, sh.Bucket, sh.Key, sh.TTL, sh.Query)
	case sh.Method == http.MethodHead:
		u, err = pub.PresignedHeadObject(ctx, sh.Bucket, sh.Key, sh.TTL, sh.Query)
	case sh.Method == http.MethodPut && len(sh.Query) == 0:
		u, err = pub.PresignedPutObject(ctx, sh.Bucket, sh.Key, sh.TTL)
	default:
		u, err = pub.PresignHeader(ctx, sh.Method, sh.Bucket, sh.Key, sh.TTL, sh.Query, nil)
	}
	if err != nil {
		return object.Link{}, wrap(err)
	}
	return object.Link{URL: u.String()}, nil
}

func orGet(m string) string {
	if m == "" {
		return http.MethodGet
	}
	return m
}

func (s *store) Form(ctx context.Context, f object.Form) (object.Link, error) {
	pub, err := s.admin.PublicClient()
	if err != nil {
		return object.Link{}, err
	}
	p := hs3.NewPostPolicy()
	// THE containment. The trailing slash on a prefix matters: without it
	// "acme/site" would also authorize "acme/site-other", handing one tenant a
	// neighbouring prefix.
	where := p.SetKeyStartsWith(f.Prefix)
	if f.Key != "" {
		where = p.SetKey(f.Key)
	}
	// Content-Type is SIGNED: one value when the form names it, else any (the empty
	// prefix). The store refuses a POST carrying a form field the policy does not
	// name, and a publisher sends the type of each object.
	typed := p.SetContentTypeStartsWith("")
	if f.ContentType != "" {
		typed = p.SetContentType(f.ContentType)
	}
	for _, err := range []error{
		p.SetBucket(f.Bucket), where, typed,
		p.SetExpires(timeIn(f.TTL)),
		p.SetContentLengthRange(f.MinBytes, f.MaxBytes),
	} {
		if err != nil {
			return object.Link{}, err
		}
	}
	u, fields, err := pub.PresignedPostPolicy(ctx, p)
	if err != nil {
		return object.Link{}, wrap(err)
	}
	// The signer echoes an empty Content-Type for the starts-with condition. It is a
	// placeholder and must not travel as a value: a caller that forwards it and then
	// sends the object's real type posts the field twice, which the store refuses.
	if f.ContentType == "" {
		delete(fields, "Content-Type")
	}
	return object.Link{URL: u.String(), Fields: fields}, nil
}

func (s *store) core() hs3.Core { return hs3.Core{Client: s.cli} }

func (s *store) Begin(ctx context.Context, bucket, key string, o object.Put) (string, error) {
	id, err := s.core().NewMultipartUpload(ctx, bucket, key, hs3.PutObjectOptions{ContentType: o.ContentType, CacheControl: o.CacheControl, UserMetadata: o.Meta})
	return id, wrap(err)
}

func (s *store) Parts(ctx context.Context, bucket, key, id string) ([]object.Part, error) {
	var out []object.Part
	for marker := 0; ; {
		r, err := s.core().ListObjectParts(ctx, bucket, key, id, marker, 1000)
		if err != nil {
			return nil, wrap(err)
		}
		for _, p := range r.ObjectParts {
			out = append(out, object.Part{Number: p.PartNumber, ETag: strings.Trim(p.ETag, `"`), Size: p.Size})
		}
		if !r.IsTruncated {
			return out, nil
		}
		marker = r.NextPartNumberMarker
	}
}

func (s *store) Join(ctx context.Context, bucket, key, id string, parts []object.Part) (object.Info, error) {
	done := make([]hs3.CompletePart, len(parts))
	for i, p := range parts {
		done[i] = hs3.CompletePart{PartNumber: p.Number, ETag: p.ETag}
	}
	up, err := s.core().CompleteMultipartUpload(ctx, bucket, key, id, done, hs3.PutObjectOptions{})
	if err != nil {
		return object.Info{}, wrap(err)
	}
	if in, err := s.Stat(ctx, bucket, key); err == nil {
		return in, nil
	}
	return object.Info{Key: key, ETag: strings.Trim(up.ETag, `"`)}, nil
}

func (s *store) Abort(ctx context.Context, bucket, key, id string) error {
	return wrap(s.core().AbortMultipartUpload(ctx, bucket, key, id))
}

func timeIn(d time.Duration) time.Time { return time.Now().Add(d) }
