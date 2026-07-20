package projects

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"strings"

	minio "github.com/hanzoai/s3-go"

	"github.com/hanzoai/cloud/clients/s3admin"
	"github.com/hanzoai/cloud/clients/sites"
)

// Deploy artifact guards. A builder one-click deploy ships a small tar(.gz) of
// the built site through the API (bounded by the app/gateway BodyLimit); large
// sites use the git/CI path that syncs to S3 directly and never streams bytes
// through this handler.
const (
	maxFiles      = 5000      // total entries in one deploy artifact
	maxFileBytes  = 64 << 20  // per-file cap (64 MiB)
	maxTotalBytes = 512 << 20 // total uncompressed cap (512 MiB)
)

// blobStore writes a project's built static files into S3 under a deterministic
// prefix and serves them publicly. The S3 connection itself comes from the ONE
// shared access path (clients/s3admin, the SAME S3_ADMIN_* credentials the
// /v1/s3 file-manager and the whole cloud binary use); blobStore adds only the
// projects-specific bucket + public URL bases on top.
type blobStore struct {
	admin  s3admin.Admin
	bucket string
}

func openBlobStore() *blobStore {
	return &blobStore{
		admin:  s3admin.New(),
		bucket: env("CLOUD_PROJECTS_BUCKET", "hanzo-sites"),
	}
}

func (b *blobStore) configured() bool { return b.admin.Configured() }

func (b *blobStore) client() (*minio.Client, error) { return b.admin.Client() }

// sitePrefix is the deterministic S3 key prefix for a project's current live site:
// "<org>/<slug>". org and slug are both validated slugs, so the join is
// unambiguous and globally unique (slug is unique per org). The canonical PUBLIC
// URL of a deployed site is siteURL (the pretty <slug>.<apex> host the sites edge
// serves), not a raw S3 URL — this only names the storage layout.
func sitePrefix(org, slug string) string { return org + "/" + slug }

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
	return finalizeSite(out)
}

// walkArtifact parses a deploy artifact in EITHER zip or tar(.gz) form into the
// normalized site map. ZIP is what a browser file-upload naturally produces;
// tar.gz is what the CLI/builder ships. One deploy contract (index.html at the
// root, the same size/traversal guards, the same single-wrapper normalization),
// two container formats — the caller never has to know which it received.
func walkArtifact(raw []byte) (*site, error) {
	if isZip(raw) {
		return walkZip(raw)
	}
	return walkTarGz(bytes.NewReader(raw))
}

// isZip reports whether raw begins with a ZIP local-file/central-directory/EOCD
// signature ("PK\x03\x04", "PK\x05\x06", or "PK\x07\x08"). A POSIX tar has no
// leading magic (its "ustar" marker sits at offset 257) and gzip starts 0x1f 0x8b,
// so anything that is not a ZIP falls through to the tar(.gz) walker.
func isZip(raw []byte) bool {
	return len(raw) >= 4 && raw[0] == 'P' && raw[1] == 'K' &&
		(raw[2] == 0x03 || raw[2] == 0x05 || raw[2] == 0x07)
}

// walkZip parses a ZIP archive into the normalized path→bytes map, enforcing the
// SAME guards as walkTarGz (path traversal, per-file + total budgets, file count)
// and finalizing with the same contract (single-wrapper strip + index.html at
// root). The declared uncompressed size is advisory (a crafted header can lie),
// so the real cap is enforced on the bytes actually read via LimitReader.
func walkZip(raw []byte) (*site, error) {
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("zip: %w", err)
	}
	out := &site{files: make(map[string][]byte)}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		clean, ok := safeRel(f.Name)
		if !ok {
			return nil, fmt.Errorf("unsafe path in artifact: %q", f.Name)
		}
		if clean == "" {
			continue
		}
		if f.UncompressedSize64 > maxFileBytes {
			return nil, fmt.Errorf("file %q exceeds %d bytes", clean, maxFileBytes)
		}
		if len(out.files) >= maxFiles {
			return nil, fmt.Errorf("artifact exceeds %d files", maxFiles)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %q: %w", clean, err)
		}
		data, err := io.ReadAll(io.LimitReader(rc, maxFileBytes+1))
		_ = rc.Close()
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
	return finalizeSite(out)
}

// finalizeSite normalizes a freshly-walked artifact and enforces the deploy
// contract, shared by every container format so the contract is identical: it
// strips a single wrapping top-level directory when present (a zip/tar made from
// a project FOLDER), then requires a non-empty artifact with index.html at the
// root. One finalize step; the format walkers only produce the raw file map.
func finalizeSite(out *site) (*site, error) {
	if len(out.files) == 0 {
		return nil, errors.New("artifact contains no files")
	}
	stripSingleRoot(out)
	if _, ok := out.files["index.html"]; !ok {
		return nil, errors.New("artifact missing index.html at root")
	}
	return out, nil
}

// stripSingleRoot removes a single common top-level directory prefix IFF every
// file lives under it AND that directory holds index.html — the natural shape of
// a zip made by compressing a project folder (e.g. "dist/index.html" →
// "index.html"). If files already sit at the root, if there are multiple
// top-level entries, or if the wrapper has no index.html, nothing is stripped and
// the artifact is used verbatim (an honest "missing index.html" follows when it
// truly has none).
func stripSingleRoot(out *site) {
	if _, ok := out.files["index.html"]; ok {
		return // already rooted — never strip
	}
	root := ""
	for name := range out.files {
		i := strings.IndexByte(name, '/')
		if i < 0 {
			return // a root-level file that isn't index.html — not a single wrapper
		}
		top := name[:i]
		if root == "" {
			root = top
		} else if top != root {
			return // more than one top-level directory — nothing to strip
		}
	}
	if root == "" {
		return
	}
	prefix := root + "/"
	if _, ok := out.files[prefix+"index.html"]; !ok {
		return // the single wrapper has no index.html — don't strip
	}
	stripped := make(map[string][]byte, len(out.files))
	for name, data := range out.files {
		stripped[strings.TrimPrefix(name, prefix)] = data
	}
	out.files = stripped
}

// uploadSite replaces the project's live prefix with the artifact's files:
// it purges the existing prefix, then puts every file with a content-type derived
// from its extension and the canonical Cache-Control policy (sites.CacheControlFor,
// the SAME policy the site server serves with). htmlOverride is the project's
// optional per-project HTML/document TTL override (empty = honest default).
// Returns the file count and total bytes written.
func (b *blobStore) uploadSite(ctx context.Context, org, slug, htmlOverride string, st *site) (prefix string, files int, total int64, err error) {
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
			minio.PutObjectOptions{ContentType: ct, CacheControl: sites.CacheControlFor(rel, htmlOverride)})
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
		if err := cli.MakeBucket(ctx, b.bucket, minio.MakeBucketOptions{Region: b.admin.Region()}); err != nil {
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
//
// The Principal MUST be the scalar string "*" and the Resource a scalar string
// (not {"AWS":["*"]} / arrays): SeaweedFS's S3 policy engine rejects the array
// forms with "Policy has invalid resource". This scalar form is equally valid
// on AWS S3 and MinIO, so it is the one canonical policy for every backend.
func publicReadPolicy(bucket string) string {
	return `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":["s3:GetObject"],"Resource":"arn:aws:s3:::` + bucket + `/*"}]}`
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

// ---- env helper (local to projects; mirror provisioning conventions) ----

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
