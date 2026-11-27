package projects

// fakeS3 is the object store the projects tests drive the REAL write path against:
// local disk (sdk/object) behind the faults and the bookkeeping these tests need. It
// is a store and not a stub — a REDEPLOY really purges the old prefix and writes the
// new one, a pinned copy really refuses a source that moved — so what the tests read
// back is what an object store would hold.
//
// What it adds to local disk, each for a test that needs it:
//
//   - failPut makes every object write fail, so the deploy upload path fails
//     deterministically for the "failed deploy is not billed" test.
//   - failCopyAfter lets that many server-side copies succeed and fails every later
//     one: the PARTIAL-COPY simulation the release tests need to prove a half-copied
//     release can never be activated.
//   - mutateOnCopy rewrites a source key's bytes just before the first copy reads it,
//     simulating a concurrent writer racing a publish so the pinned copy guard can
//     be exercised.
//   - seedSparse declares an object of a SIZE without its bytes, which is how the
//     byte-cap test asserts on half a gigabyte without allocating it.
//   - seedAt and stamp set and read an object's LastModified, which the reconcile's
//     grace for immutable keys reads.

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"iter"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/sdk/object"
)

type fakeS3 struct {
	object.Store
	l *object.Local

	mu       sync.Mutex
	sizes    map[string]int64     // "<bucket>/<key>" -> declared size of a sparse object
	modified map[string]time.Time // "<bucket>/<key>" -> LastModified a test set
	puts     int
	copies   int

	failPut       atomic.Bool
	failCopyAfter atomic.Int32
	mutateOnCopy  atomic.Value // string
}

// startFakeS3 stands the double in as this process's object store for one test.
func startFakeS3(t *testing.T) *fakeS3 {
	t.Helper()
	l := &object.Local{Dir: t.TempDir(), Base: func() string { return "https://s3.example.test" }}
	f := &fakeS3{Store: l, l: l, sizes: map[string]int64{}, modified: map[string]time.Time{}}
	was := objects
	objects = func() object.Store { return f }
	t.Cleanup(func() { objects = was })
	return f
}

func (f *fakeS3) ensure(bucket string) {
	_ = f.l.Ensure(context.Background(), bucket)
}

// seed writes an object directly into the double, standing in for the build output
// an agent's code execution already produced inside our object store.
func (f *fakeS3) seed(bucket, key, body string) {
	f.seedAt(bucket, key, body, "", time.Now())
}

// seedAt writes an object as a write at a given moment would have left it: its
// bytes, the Content-Type it is served with, and that LastModified.
func (f *fakeS3) seedAt(bucket, key, body, contentType string, at time.Time) {
	f.ensure(bucket)
	_, _ = f.l.Put(context.Background(), bucket, key, strings.NewReader(body), int64(len(body)), object.Put{ContentType: contentType})
	f.mu.Lock()
	f.modified[bucket+"/"+key] = at
	delete(f.sizes, bucket+"/"+key)
	f.mu.Unlock()
}

// seedSparse declares an object of a given SIZE without allocating its bytes. The
// release plane never reads object bodies — it caps on the size the listing reports
// and then hands the bytes to the store to copy — so a sparse object is a faithful
// model of a large build output.
func (f *fakeS3) seedSparse(bucket, key string, size int64) {
	f.ensure(bucket)
	_, _ = f.l.Put(context.Background(), bucket, key, strings.NewReader(""), 0, object.Put{})
	f.mu.Lock()
	f.modified[bucket+"/"+key] = time.Now()
	f.sizes[bucket+"/"+key] = size
	f.mu.Unlock()
}

// remove drops one object, standing in for bytes reclaimed out of band — a bucket
// lifecycle rule, an operator purge, or a retention pass.
func (f *fakeS3) remove(bucket, key string) {
	_ = f.Remove(context.Background(), bucket, key)
}

// stamp reports an object's LastModified and the Content-Type it is served with.
func (f *fakeS3) stamp(bucket, key string) (time.Time, string) {
	in, err := f.l.Stat(context.Background(), bucket, key)
	if err != nil {
		return time.Time{}, ""
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if at, ok := f.modified[bucket+"/"+key]; ok {
		return at, in.ContentType
	}
	return in.Modified, in.ContentType
}

// body returns a stored object's bytes (ok=false when absent).
func (f *fakeS3) body(bucket, key string) (string, bool) {
	r, err := f.l.Open(context.Background(), bucket, key, object.Get{})
	if err != nil {
		return "", false
	}
	defer r.Close()
	b, _ := io.ReadAll(r)
	return string(b), true
}

// copyCount reports how many server-side COPYs the double has served.
func (f *fakeS3) copyCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.copies
}

// each calls fn for every object whose "<bucket>/<key>" starts with prefix.
func (f *fakeS3) each(prefix string, fn func(full string, size int64)) {
	ctx := context.Background()
	names, _ := f.l.Buckets(ctx)
	for _, b := range names {
		for in, err := range f.l.List(ctx, b.Name, object.List{}) {
			if err != nil {
				continue
			}
			full := b.Name + "/" + in.Key
			if !strings.HasPrefix(full, prefix) {
				continue
			}
			size := in.Size
			f.mu.Lock()
			if n, ok := f.sizes[full]; ok {
				size = n
			}
			f.mu.Unlock()
			fn(full, size)
		}
	}
}

// bytesUnder is the stored size of every object whose "<bucket>/<key>" starts with
// prefix, declared sizes included — what a listing of it sums to.
func (f *fakeS3) bytesUnder(prefix string) int64 {
	var n int64
	f.each(prefix, func(_ string, size int64) { n += size })
	return n
}

func (f *fakeS3) count(prefix string) int {
	n := 0
	f.each(prefix, func(string, int64) { n++ })
	return n
}

func (f *fakeS3) Put(ctx context.Context, bucket, key string, r io.Reader, size int64, o object.Put) (object.Info, error) {
	if f.failPut.Load() {
		return object.Info{}, errors.New("AccessDenied: denied")
	}
	info, err := f.l.Put(ctx, bucket, key, r, size, o)
	if err == nil {
		f.mu.Lock()
		f.puts++
		delete(f.sizes, bucket+"/"+key) // a real body replaces a declared size
		delete(f.modified, bucket+"/"+key)
		f.mu.Unlock()
	}
	return info, err
}

func (f *fakeS3) Remove(ctx context.Context, bucket, key string) error {
	err := f.l.Remove(ctx, bucket, key)
	f.forget(bucket, key)
	return err
}

func (f *fakeS3) RemoveMany(ctx context.Context, bucket string, keys []string) error {
	err := f.l.RemoveMany(ctx, bucket, keys)
	for _, k := range keys {
		f.forget(bucket, k)
	}
	return err
}

func (f *fakeS3) forget(bucket, key string) {
	f.mu.Lock()
	delete(f.sizes, bucket+"/"+key)
	delete(f.modified, bucket+"/"+key)
	f.mu.Unlock()
}

// stat reports a stored object with the size and time a test declared.
func (f *fakeS3) declared(bucket string, in object.Info) object.Info {
	f.mu.Lock()
	defer f.mu.Unlock()
	if n, ok := f.sizes[bucket+"/"+in.Key]; ok {
		in.Size = n
	}
	if at, ok := f.modified[bucket+"/"+in.Key]; ok {
		in.Modified = at
	}
	return in
}

func (f *fakeS3) Stat(ctx context.Context, bucket, key string) (object.Info, error) {
	in, err := f.l.Stat(ctx, bucket, key)
	return f.declared(bucket, in), err
}

func (f *fakeS3) List(ctx context.Context, bucket string, o object.List) iter.Seq2[object.Info, error] {
	return func(yield func(object.Info, error) bool) {
		for in, err := range f.l.List(ctx, bucket, o) {
			if err == nil {
				in = f.declared(bucket, in)
			}
			if !yield(in, err) {
				return
			}
		}
	}
}

func (f *fakeS3) Copy(ctx context.Context, dst, src object.Ref, o object.Copy) (object.Info, error) {
	// A store's own refusal: copying an object onto itself is legal only when the
	// copy changes its metadata, so a re-stamp that forgets to replace fails here as
	// it fails live.
	if dst == src && !o.Replace {
		return object.Info{}, errors.New("InvalidRequest: this copy request is illegal because it is trying to copy an object to itself without changing the object's metadata")
	}
	if n := f.failCopyAfter.Load(); n > 0 {
		f.mu.Lock()
		done := f.copies
		f.mu.Unlock()
		if int32(done) >= n {
			return object.Info{}, errors.New("AccessDenied: denied")
		}
	}
	// Simulate a concurrent writer mutating the build output mid-publish.
	if victim, _ := f.mutateOnCopy.Load().(string); victim != "" {
		if _, ok := f.body(src.Bucket, victim); ok {
			_, _ = f.l.Put(ctx, src.Bucket, victim, strings.NewReader("MUTATED BY A CONCURRENT WRITER"), 30, object.Put{})
			f.mutateOnCopy.Store("")
		}
	}
	info, err := f.l.Copy(ctx, dst, src, o)
	if err != nil {
		return info, err
	}
	f.mu.Lock()
	f.copies++
	// A copy is the same object again, so a declared size travels with it.
	if n, sized := f.sizes[src.Bucket+"/"+src.Key]; sized {
		f.sizes[dst.Bucket+"/"+dst.Key] = n
	} else {
		delete(f.sizes, dst.Bucket+"/"+dst.Key)
	}
	delete(f.modified, dst.Bucket+"/"+dst.Key)
	f.mu.Unlock()
	return info, nil
}

// etagOf is the store's content identity for a body — real MD5, as an object store
// reports for a single-part write. The release plane digests these, so the double
// MUST report honest per-object etags or content-addressing would be untested.
func etagOf(body []byte) string { return fmt.Sprintf("%x", md5.Sum(body)) }
