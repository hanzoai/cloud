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
	"path"
	"strings"

	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/object"
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

// blobStore writes a project's built static files into the object store under a
// deterministic prefix and serves them publicly. The store itself is the ONE
// process-wide path (sdk/object, the SAME store the /v1/s3 file-manager and the
// whole cloud binary use); blobStore adds only the projects-specific bucket on top.
type blobStore struct {
	store  object.Store
	bucket string
	made   object.Made
}

// objects is the object store the blob store keeps sites in: the process's own
// (sdk/object). A var so a test stands a double in.
var objects = object.Default

func openBlobStore() *blobStore {
	return &blobStore{
		store:  objects(),
		bucket: env.Or("CLOUD_PROJECTS_BUCKET", "hanzo-sites"),
	}
}

// configured reports whether the object store can be opened here; a production
// process with none configured answers false and its deploys refuse.
func (b *blobStore) configured() bool { return object.Usable(b.store) == nil }

// sitePrefix is the deterministic S3 key prefix for a project's current live site:
// "<namespace>/<slug>" (namespace.go). Both are validated slugs and a slug is
// unique across every org, so the join is unambiguous and globally unique. The canonical PUBLIC
// URL of a deployed site is siteURL (the pretty <slug>.<apex> host the sites edge
// serves), not a raw S3 URL — this only names the storage layout.
func sitePrefix(namespace, slug string) string { return namespace + "/" + slug }

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
		before, _, ok := strings.Cut(name, "/")
		if !ok {
			return // a root-level file that isn't index.html — not a single wrapper
		}
		top := before
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

// uploadSite updates the project's live prefix with the artifact's files: it
// writes every file first with the canonical MIME type (sites.ContentType) and
// Cache-Control policy (sites.CacheControlFor), then reconciles the prefix
// against the new manifest so removed pages stop serving immediately while
// dropped fingerprinted chunks remain retained for graceAge (36 h) for open
// tabs. htmlOverride is the project's optional per-project HTML/document TTL
// override (empty = honest default). Returns the file count and total bytes
// written.
func (b *blobStore) uploadSite(ctx context.Context, namespace, slug, htmlOverride string, st *site) (prefix string, files int, total int64, err error) {
	if err := b.ensureBucket(ctx); err != nil {
		return "", 0, 0, err
	}
	prefix = sitePrefix(namespace, slug)
	keep := make(map[string]bool, len(st.files))
	for rel, data := range st.files {
		key := prefix + "/" + rel
		ct := sites.ContentType(rel)
		if ct == "" {
			ct = "application/octet-stream"
		}
		_, err := b.store.Put(ctx, b.bucket, key, bytes.NewReader(data), int64(len(data)),
			object.Put{ContentType: ct, CacheControl: sites.CacheControlFor(rel, htmlOverride)})
		if err != nil {
			return "", 0, 0, fmt.Errorf("put %q: %w", key, err)
		}
		keep[rel] = true
		files++
		total += int64(len(data))
	}
	if _, err := reconcilePrefix(ctx, b.store, b.bucket, prefix, keep); err != nil {
		return "", 0, 0, fmt.Errorf("reconcile prefix: %w", err)
	}
	return prefix, files, total, nil
}

// ensureBucket creates the projects bucket if absent. It installs no policy: the
// bucket is private. A site is served by the sites edge, which reads it with the
// platform's credential and applies the project's own visibility, so an
// anonymous read on the bucket would only expose private projects past that rule.
func (b *blobStore) ensureBucket(ctx context.Context) error {
	if err := b.made.Ensure(ctx, b.store, b.bucket); err != nil {
		return fmt.Errorf("make bucket: %w", err)
	}
	return nil
}

// purgePrefix removes every object under prefix so a redeploy never leaves stale
// files behind (a deploy is the full site, not a diff).
//
// A prefix must actually SCOPE the delete. Empty or "/" would list — and remove —
// the entire bucket, so the ONE primitive that deletes objects refuses it here
// rather than trusting every caller to have computed a real one. That matters now
// that retention passes a prefix READ FROM A ROW (pruneReleases) and not only
// values computed on the spot: one bad row must not become a bucket wipe.
func purgePrefix(ctx context.Context, store object.Store, bucket, prefix string) error {
	if strings.Trim(prefix, "/") == "" {
		return fmt.Errorf("purge: refusing an unscoped prefix %q", prefix)
	}
	var keys []string
	for obj, err := range store.List(ctx, bucket, object.List{Prefix: prefix + "/"}) {
		if err != nil {
			return fmt.Errorf("purge: list %s: %w", prefix, err)
		}
		keys = append(keys, obj.Key)
	}
	return store.RemoveMany(ctx, bucket, keys)
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
