package knowledge

import (
	arzip "archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/framework"
	"github.com/hanzoai/cloud/clients/knowledge/evernote"
	"github.com/hanzoai/cloud/clients/knowledge/notion"
	"github.com/hanzoai/cloud/clients/knowledge/obsidian"
	"github.com/hanzoai/cloud/clients/knowledge/roam"
	"github.com/hanzoai/cloud/clients/knowledge/vault"
	"github.com/hanzoai/cloud/clients/principal"
	"github.com/zap-proto/zip"
)

// import.go serves POST /v1/kb/import: an Obsidian-importer-equivalent that ingests
// an uploaded export (Obsidian vault zip, Notion export zip, Evernote .enex, Roam
// JSON) as a tree of kb-page documents with the link structure intact. Each format
// is normalized by a pure package (obsidian/notion/roam/evernote) into []vault.Page
// with "[[wikilinks]]" preserved in the body; this handler files them through the
// SAME framework.Ingest path a connector sync uses, so the kb-page after_save hook
// indexes each page AND extracts its wikilinks into kb-link edges — one write path,
// one link-extraction path. It is org-scoped via principal.Org.

// Import bounds: the upload size, the page count, and the per-entry extract size,
// so a hostile archive cannot exhaust the per-org store.
const (
	maxImportBytes = 64 << 20 // 64 MB upload
	maxImportPages = 5000
	maxEntryBytes  = 8 << 20 // 8 MB per archive entry
)

// importVault reads the uploaded export, dispatches to the format normalizer, and
// files the resulting pages. `format` (query) selects the normalizer; `project`
// (query, optional) scopes every imported page to a project.
func importVault(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := principal.Org(c)
	if !ok {
		return zip.ErrForbidden("valid principal required")
	}
	if !framework.Installed(c.Context(), org, DTPage) {
		return zip.ErrBadRequest("install the kb module first (POST /v1/framework/modules/kb/install)")
	}
	format := strings.ToLower(strings.TrimSpace(c.Query("format")))
	project := strings.TrimSpace(c.Query("project"))

	data, err := readUpload(c)
	if err != nil {
		return err
	}

	pages, err := normalize(format, data)
	if err != nil {
		return zip.ErrBadRequest(err.Error())
	}
	if len(pages) == 0 {
		return c.JSON(http.StatusOK, map[string]any{"format": format, "imported": 0, "pages": []string{}})
	}

	imported, names, err := fileVaultPages(c.Context(), org, pages, project)
	if err != nil {
		s.Log.Warn("kb import: partial", "org", org, "format", format, "imported", imported, "err", err)
	}
	return c.JSON(http.StatusOK, map[string]any{"format": format, "imported": imported, "pages": names})
}

// normalize dispatches raw upload bytes to the format's pure normalizer. Zip
// formats are unpacked here (the normalizers stay I/O-free); Roam accepts a raw
// JSON or a zip wrapping one; Evernote takes the .enex XML directly.
func normalize(format string, data []byte) ([]vault.Page, error) {
	switch format {
	case "obsidian":
		files, err := unzip(data)
		if err != nil {
			return nil, fmt.Errorf("obsidian: %w", err)
		}
		return obsidian.Parse(files)
	case "notion":
		files, err := unzip(data)
		if err != nil {
			return nil, fmt.Errorf("notion: %w", err)
		}
		return notion.ParseExport(files)
	case "roam":
		return roam.Parse(roamJSON(data))
	case "evernote":
		return evernote.Parse(data)
	default:
		return nil, fmt.Errorf("unknown format %q (want obsidian|notion|roam|evernote)", format)
	}
}

// readUpload returns the uploaded bytes from the multipart "file" field, or the raw
// request body when no multipart part is present — either way bounded by
// maxImportBytes.
func readUpload(c *zip.Ctx) ([]byte, error) {
	if fh, err := c.Fiber().FormFile("file"); err == nil && fh != nil {
		if fh.Size > maxImportBytes {
			return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "upload too large (max %d bytes)", maxImportBytes)
		}
		f, err := fh.Open()
		if err != nil {
			return nil, zip.ErrBadRequest("cannot read upload")
		}
		defer func() { _ = f.Close() }()
		data, err := io.ReadAll(io.LimitReader(f, maxImportBytes+1))
		if err != nil || len(data) > maxImportBytes {
			return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "upload too large (max %d bytes)", maxImportBytes)
		}
		return data, nil
	}
	body := c.Body()
	if len(body) == 0 {
		return nil, zip.ErrBadRequest(`empty upload (send multipart "file" or a raw body)`)
	}
	if len(body) > maxImportBytes {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "upload too large (max %d bytes)", maxImportBytes)
	}
	return body, nil
}

// unzip decodes a zip archive into vault.Files, skipping directories and bounding
// each entry to maxEntryBytes.
func unzip(data []byte) ([]vault.File, error) {
	zr, err := arzip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("not a valid zip: %w", err)
	}
	out := make([]vault.File, 0, len(zr.File))
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", f.Name, err)
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxEntryBytes))
		_ = rc.Close()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name, err)
		}
		out = append(out, vault.File{Path: f.Name, Data: b})
	}
	return out, nil
}

// roamJSON returns the Roam export JSON: the body itself, or — when the upload is a
// zip (Roam's download wraps the JSON) — the first .json entry inside it.
func roamJSON(data []byte) []byte {
	if !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return data
	}
	files, err := unzip(data)
	if err != nil {
		return data
	}
	for _, f := range files {
		if strings.EqualFold(pathExt(f.Path), ".json") {
			return f.Data
		}
	}
	return data
}

func pathExt(p string) string {
	if i := strings.LastIndexByte(p, '.'); i >= 0 {
		return p[i:]
	}
	return ""
}

// fileVaultPages files normalized pages as kb-page documents, filing parents before
// children so the kb-page `parent` Link resolves. It assigns each page a slug unique
// within the org (suffixing on collision) and remaps parent references to the final
// slugs. Returns the count filed and their final names.
func fileVaultPages(ctx context.Context, org string, pages []vault.Page, project string) (int, []string, error) {
	if len(pages) > maxImportPages {
		pages = pages[:maxImportPages]
	}

	// 1. Assign a unique final slug per page and record the remap from the
	//    normalizer's original slug (unique per export for formats that use parents).
	used := map[string]bool{}
	remap := map[string]string{}
	final := make([]vault.Page, len(pages))
	for i, p := range pages {
		slug := uniqueSlug(ctx, org, p.Slug, used)
		used[slug] = true
		remap[p.Slug] = slug
		final[i] = p
		final[i].Slug = slug
	}

	// 2. Remap parents to final slugs; a parent outside the batch becomes a root.
	for i := range final {
		if final[i].Parent == "" {
			continue
		}
		rp := remap[final[i].Parent]
		if rp == "" || !used[rp] {
			final[i].Parent = ""
			continue
		}
		final[i].Parent = rp
	}

	// 3. File in parent-before-child order.
	ordered := topoOrder(final)
	var firstErr error
	names := make([]string, 0, len(ordered))
	for _, p := range ordered {
		data := map[string]any{
			"title":  p.Title,
			"slug":   p.Slug,
			"body":   p.Body,
			"status": "Published",
		}
		if p.Parent != "" {
			data["parent"] = p.Parent
		}
		if project != "" {
			data["project"] = project
		}
		ing, err := framework.Ingest(ctx, org, DTPage, data, "")
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		names = append(names, ing.Name)
	}
	return len(names), names, firstErr
}

// uniqueSlug returns base if free (unused in this batch and absent in the org), else
// base-2, base-3, … The org check keeps a re-import from colliding with an earlier
// one; it is bounded and idempotent enough for a one-shot import.
func uniqueSlug(ctx context.Context, org, base string, used map[string]bool) string {
	if base == "" {
		base = "page"
	}
	candidate := base
	for i := 2; ; i++ {
		if !used[candidate] && !pageExists(ctx, org, candidate) {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
}

// pageExists reports whether a kb-page with this name already exists in the org.
func pageExists(ctx context.Context, org, name string) bool {
	docs, err := framework.Search(ctx, org, DTPage, map[string]string{"name": name}, 1)
	return err == nil && len(docs) == 1
}

// topoOrder returns the pages so every parent precedes its children. A page whose
// parent is not in the batch (or a cycle) is treated as a root, so no page is ever
// dropped.
func topoOrder(pages []vault.Page) []vault.Page {
	bySlug := make(map[string]vault.Page, len(pages))
	for _, p := range pages {
		bySlug[p.Slug] = p
	}
	emitted := map[string]bool{}
	out := make([]vault.Page, 0, len(pages))

	var emit func(p vault.Page, seen map[string]bool)
	emit = func(p vault.Page, seen map[string]bool) {
		if emitted[p.Slug] {
			return
		}
		if p.Parent != "" && !seen[p.Slug] {
			if parent, ok := bySlug[p.Parent]; ok && !emitted[p.Parent] {
				seen[p.Slug] = true // guard against a parent cycle
				emit(parent, seen)
			}
		}
		if emitted[p.Slug] {
			return
		}
		emitted[p.Slug] = true
		out = append(out, p)
	}
	for _, p := range pages {
		emit(p, map[string]bool{})
	}
	return out
}
