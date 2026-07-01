package projectsvc

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"strconv"
	"strings"

	minio "github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// Deploy artifact guards. A builder one-click deploy ships a small tar(.gz) of
// the built site through the API (bounded by the app/gateway BodyLimit); large
// sites use the git/CI path that syncs to S3 directly and never streams bytes
// through this handler.
const (
	maxFiles      = 5000       // total entries in one deploy artifact
	maxFileBytes  = 64 << 20   // per-file cap (64 MiB)
	maxTotalBytes = 512 << 20  // total uncompressed cap (512 MiB)
)

// blobStore writes a project's built static files into S3 under a deterministic
// prefix and serves them publicly. It reuses the SAME shared admin credentials
// the provisioning control plane uses (CLOUD_S3_ADMIN_*), so there is one S3
// access path for the whole cloud binary.
type blobStore struct {
	endpoint  string
	ak        string
	sk        string
	secure    bool
	region    string
	bucket    string
	publicURL string // public base for built sites, e.g. https://s3.hanzo.ai
	sitesURL  string // optional pretty base served by the static container, e.g. https://sites.hanzo.app
}

func openBlobStore() *blobStore {
	return &blobStore{
		endpoint:  env("CLOUD_S3_ADMIN_ENDPOINT", "s3.hanzo.svc:9000"),
		ak:        os.Getenv("CLOUD_S3_ADMIN_ACCESS_KEY"),
		sk:        os.Getenv("CLOUD_S3_ADMIN_SECRET_KEY"),
		secure:    boolEnv("CLOUD_S3_SECURE", false),
		region:    env("CLOUD_S3_REGION", "us-east-1"),
		bucket:    env("CLOUD_PROJECTS_BUCKET", "hanzo-sites"),
		publicURL: strings.TrimRight(env("CLOUD_PROJECTS_PUBLIC_URL", "https://s3.hanzo.ai"), "/"),
		sitesURL:  strings.TrimRight(os.Getenv("CLOUD_PROJECTS_SITES_URL"), "/"),
	}
}

func (b *blobStore) configured() bool { return b.ak != "" && b.sk != "" }

func (b *blobStore) client() (*minio.Client, error) {
	return minio.New(b.endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(b.ak, b.sk, ""),
		Secure: b.secure,
		Region: b.region,
	})
}

// prefix is the deterministic S3 key prefix for a project's current live site:
// "<org>/<slug>". org and slug are both validated slugs, so the join is
// unambiguous and globally unique (slug is unique per org).
func sitePrefix(org, slug string) string { return org + "/" + slug }

// liveURL is the canonical public URL for a deployed project. Prefer the pretty
// static-container base when configured; otherwise the direct S3 object URL,
// which is reachable as soon as the bucket has a public-read policy.
func (b *blobStore) liveURL(org, slug string) string {
	pfx := sitePrefix(org, slug)
	if b.sitesURL != "" {
		return b.sitesURL + "/" + pfx + "/"
	}
	return b.publicURL + "/" + b.bucket + "/" + pfx + "/index.html"
}

// site is the in-memory representation of a deploy artifact: relative path →
// bytes. walkTarGz produces it; uploadSite consumes it. Splitting the tar walk
// from the S3 put makes the parsing/guards unit-testable without S3.
type site struct {
	files map[string][]byte
	bytes int64
}

// walkTarGz parses a tar, optionally gzip-compressed, into a normalized
// path→bytes map. It enforces the artifact guards and rejects path traversal
// ("..", absolute, or escaping entries). Directory and non-regular entries are
// skipped. A leading single top-level directory (e.g. "dist/") is NOT stripped
// here — callers that build the artifact decide the layout; the builder ships
// files at the root and CI ships dist/* at the root via `tar -C dist`.
func walkTarGz(r io.Reader) (*site, error) {
	br := bufioPeek(r)
	var tr *tar.Reader
	if isGzip(br.head) {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("gzip: %w", err)
		}
		defer func() { _ = zr.Close() }()
		tr = tar.NewReader(zr)
	} else {
		tr = tar.NewReader(br)
	}

	out := &site{files: make(map[string][]byte)}
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg && hdr.Typeflag != tar.TypeRegA {
			continue
		}
		clean, ok := safeRel(hdr.Name)
		if !ok {
			return nil, fmt.Errorf("unsafe path in artifact: %q", hdr.Name)
		}
		if clean == "" {
			continue
		}
		if hdr.Size > maxFileBytes {
			return nil, fmt.Errorf("file %q exceeds %d bytes", clean, maxFileBytes)
		}
		if len(out.files) >= maxFiles {
			return nil, fmt.Errorf("artifact exceeds %d files", maxFiles)
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxFileBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read %q: %w", clean, err)
		}
		if int64(len(data)) > maxFileBytes {
			return nil, fmt.Errorf("file %q exceeds %d bytes", clean, maxFileBytes)
		}
		out.bytes += int64(len(data))
		if out.bytes > maxTotalBytes {
			return nil, fmt.Errorf("artifact exceeds %d bytes total", maxTotalBytes)
		}
		out.files[clean] = data
	}
	if len(out.files) == 0 {
		return nil, errors.New("artifact contains no files")
	}
	if _, ok := out.files["index.html"]; !ok {
		return nil, errors.New("artifact missing index.html at root")
	}
	return out, nil
}

// uploadSite replaces the project's live prefix with the artifact's files:
// it purges the existing prefix, then puts every file with a content-type
// derived from its extension. Returns the file count and total bytes written.
func (b *blobStore) uploadSite(ctx context.Context, org, slug string, st *site) (prefix string, files int, total int64, err error) {
	cli, err := b.client()
	if err != nil {
		return "", 0, 0, fmt.Errorf("s3 connect: %w", err)
	}
	if err := b.ensureBucket(ctx, cli); err != nil {
		return "", 0, 0, err
	}
	prefix = sitePrefix(org, slug)
	if err := purgePrefix(ctx, cli, b.bucket, prefix); err != nil {
		return "", 0, 0, fmt.Errorf("purge prefix: %w", err)
	}
	for rel, data := range st.files {
		key := prefix + "/" + rel
		ct := mime.TypeByExtension(path.Ext(rel))
		if ct == "" {
			ct = "application/octet-stream"
		}
		_, err := cli.PutObject(ctx, b.bucket, key, bytes.NewReader(data), int64(len(data)),
			minio.PutObjectOptions{ContentType: ct, CacheControl: cacheControlFor(rel)})
		if err != nil {
			return "", 0, 0, fmt.Errorf("put %q: %w", key, err)
		}
		files++
		total += int64(len(data))
	}
	return prefix, files, total, nil
}

// ensureBucket creates the projects bucket if absent and installs an anonymous
// read-only policy so deployed sites are reachable directly over S3.
func (b *blobStore) ensureBucket(ctx context.Context, cli *minio.Client) error {
	exists, err := cli.BucketExists(ctx, b.bucket)
	if err != nil {
		return fmt.Errorf("bucket exists: %w", err)
	}
	if !exists {
		if err := cli.MakeBucket(ctx, b.bucket, minio.MakeBucketOptions{Region: b.region}); err != nil {
			if ex, _ := cli.BucketExists(ctx, b.bucket); !ex {
				return fmt.Errorf("make bucket: %w", err)
			}
		}
	}
	policy := publicReadPolicy(b.bucket)
	if err := cli.SetBucketPolicy(ctx, b.bucket, policy); err != nil {
		return fmt.Errorf("set bucket policy: %w", err)
	}
	return nil
}

// purgePrefix removes every object under prefix so a redeploy never leaves stale
// files behind (a deploy is the full site, not a diff).
func purgePrefix(ctx context.Context, cli *minio.Client, bucket, prefix string) error {
	objCh := cli.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix + "/", Recursive: true})
	toDelete := make(chan minio.ObjectInfo)
	go func() {
		defer close(toDelete)
		for obj := range objCh {
			if obj.Err != nil {
				continue
			}
			toDelete <- obj
		}
	}()
	for rmErr := range cli.RemoveObjects(ctx, bucket, toDelete, minio.RemoveObjectsOptions{}) {
		if rmErr.Err != nil {
			return rmErr.Err
		}
	}
	return nil
}

// publicReadPolicy is the canonical anonymous read-only bucket policy. Listing
// is denied; only GetObject is public, so deployed assets are fetchable but the
// bucket is not enumerable.
func publicReadPolicy(bucket string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":["*"]},"Action":["s3:GetObject"],"Resource":["arn:aws:s3:::` + bucket + `/*"]}]}`
}

// cacheControlFor returns a sensible Cache-Control per asset class: HTML is
// always revalidated (so a redeploy is seen immediately); hashed assets cache
// long. Heuristic by extension — content-hashed bundles are the common case.
func cacheControlFor(rel string) string {
	switch path.Ext(rel) {
	case ".html", ".json", ".xml", ".txt":
		return "no-cache"
	case ".js", ".css", ".woff", ".woff2", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".webp", ".avif", ".ico":
		return "public, max-age=31536000, immutable"
	default:
		return "public, max-age=3600"
	}
}

// safeRel normalizes a tar entry name to a clean relative path or rejects it.
// Rejects absolute paths and any path that escapes the root via "..".
func safeRel(name string) (string, bool) {
	n := strings.TrimPrefix(strings.ReplaceAll(name, `\`, "/"), "./")
	if n == "" || n == "." {
		return "", true
	}
	if strings.HasPrefix(n, "/") {
		return "", false
	}
	clean := path.Clean(n)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", false
	}
	return clean, true
}

// ---- tiny gzip sniff (avoid forcing callers to know the encoding) ----

type peekReader struct {
	head []byte
	r    io.Reader
	used bool
}

func bufioPeek(r io.Reader) *peekReader {
	head := make([]byte, 2)
	n, _ := io.ReadFull(r, head)
	return &peekReader{head: head[:n], r: r}
}

func (p *peekReader) Read(b []byte) (int, error) {
	if !p.used && len(p.head) > 0 {
		n := copy(b, p.head)
		p.head = p.head[n:]
		if len(p.head) == 0 {
			p.used = true
		}
		return n, nil
	}
	return p.r.Read(b)
}

func isGzip(head []byte) bool { return len(head) >= 2 && head[0] == 0x1f && head[1] == 0x8b }

// ---- env helpers (local to projectsvc; mirror provisioningsvc conventions) ----

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func boolEnv(key string, def bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
