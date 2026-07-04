package team

// This file is the workspace FILES plane (Phase 2A) — the blob store the Huly SPA
// hits at UPLOAD_URL=/v1/files (POST) and FILES_URL=/v1/files/:workspace/:filename
// ?file=:blobId (GET). The CTO repoints those env vars to /v1/team/files at
// cutover. team-go's pkg/files was only a 307 alias to Base's file API (no store),
// so this is a fresh implementation of the same contract backed by cloud's
// CANONICAL blob seam, deps.VFS (Put/Get) — no new store invented (ONE way).
//
// TENANT ISOLATION — the SAME invariant as the docs store. The org is the VERIFIED
// session-token extra.org claim (never a client header); the physical blob key
// embeds BOTH the org and the workspace (team/blobs/<org>/<ws>/<blobId>), and the
// named :workspace is asserted to belong to that org (WorkspaceByUUID). So a caller
// in org B naming org A's blobId computes key team/blobs/<B>/<ws>/<blobId>, which
// does not exist → 404: a cross-org blob read is structurally impossible, not
// merely access-checked.

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/types"
)

// maxBlobSize caps a single upload so an unbounded body can't exhaust the blob
// backend or memory. 100 MiB matches the Huly attachment ceiling.
const maxBlobSize = 100 << 20

// filesSvc serves the workspace blob plane. vfs is cloud's blob seam (deps.VFS);
// accounts asserts workspace-in-org; secret verifies the session token.
type filesSvc struct {
	vfs      types.VFSClient
	accounts *accountStore
	secret   string
}

func (s *filesSvc) register(app *zip.App, guard guardFn) {
	app.Post("/v1/team/files", guard(s.upload))
	app.Get("/v1/team/files/:workspace/:filename", guard(s.download))
}

// principal resolves (org, workspaceClaim) from the request's VERIFIED session or
// workspace token (bearer or the HttpOnly account cookie). workspaceClaim is the
// token's Workspace claim (present on a workspace token, empty on a session token).
func (s *filesSvc) principal(c *zip.Ctx) (org, workspaceClaim string, err error) {
	t, _, err := sessionToken(c, s.secret)
	if err != nil {
		return "", "", err
	}
	org, _ = t.Extra["org"].(string)
	if strings.TrimSpace(org) == "" {
		return "", "", fmt.Errorf("token carries no org")
	}
	return org, t.Workspace, nil
}

// resolveWorkspace asserts wsUUID belongs to org (the tenant boundary) and returns
// it. A foreign tenant's uuid → 404 (no cross-tenant existence oracle).
func (s *filesSvc) resolveWorkspace(c *zip.Ctx, org, wsUUID string) (string, error) {
	wsUUID = strings.TrimSpace(wsUUID)
	if wsUUID == "" {
		return "", zip.ErrBadRequest("workspace required")
	}
	if s.accounts == nil {
		return "", zip.Errorf(http.StatusServiceUnavailable, "team: file storage unavailable")
	}
	if _, err := s.accounts.WorkspaceByUUID(c.Context(), org, wsUUID); err != nil {
		return "", zip.ErrNotFound("workspace not found")
	}
	return wsUUID, nil
}

// upload stores a blob for the caller's workspace and returns its blobId. The
// workspace comes from the workspace-token claim if present, else the ?workspace=
// query. Accepts a multipart "file" field or a raw body.
func (s *filesSvc) upload(c *zip.Ctx) error {
	org, wsClaim, err := s.principal(c)
	if err != nil {
		return zip.ErrUnauthorized("invalid session token")
	}
	ws, err := s.resolveWorkspace(c, org, firstNonEmpty(wsClaim, c.Query("workspace")))
	if err != nil {
		return err
	}
	data, err := readUpload(c)
	if err != nil {
		return err
	}
	if len(data) == 0 {
		return zip.ErrBadRequest("empty upload")
	}
	blobID := uuid.NewString()
	if err := s.vfs.Put(c.Context(), blobKey(org, ws, blobID), data); err != nil {
		// deps.VFS is DisabledVFS (fail-closed) unless the operator wires a real VFS
		// backend — surface an honest 502 rather than a silent success.
		return zip.Errorf(http.StatusBadGateway, "file storage unavailable")
	}
	// The Huly uploader reads the response body as the blob id. Plain text keeps it
	// compatible across front versions.
	return c.String(http.StatusOK, blobID)
}

// download streams a blob. The blob id is the ?file= query (Huly's FILES_URL) with
// a fallback to the :filename path segment (the /files/:ws/:blobId shape); the
// :filename drives the Content-Type only.
func (s *filesSvc) download(c *zip.Ctx) error {
	org, _, err := s.principal(c)
	if err != nil {
		return zip.ErrUnauthorized("invalid session token")
	}
	ws, err := s.resolveWorkspace(c, org, c.Param("workspace"))
	if err != nil {
		return err
	}
	blobID := strings.TrimSpace(firstNonEmpty(c.Query("file"), c.Param("filename")))
	if blobID == "" {
		return zip.ErrBadRequest("file (blob id) required")
	}
	data, err := s.vfs.Get(c.Context(), blobKey(org, ws, blobID))
	if err != nil || data == nil {
		// A cross-org blobId, a missing blob, or a disabled backend all land here as
		// a 404 — no oracle distinguishing "not yours" from "not found".
		return zip.ErrNotFound("blob not found")
	}
	c.SetHeader("Content-Type", contentType(c.Param("filename")))
	c.SetHeader("Cache-Control", "private, max-age=31536000, immutable")
	return c.Bytes(http.StatusOK, data)
}

// readUpload returns the uploaded bytes from a multipart "file" field or, failing
// that, the raw request body — capped at maxBlobSize.
func readUpload(c *zip.Ctx) ([]byte, error) {
	if fh, err := c.Fiber().FormFile("file"); err == nil && fh != nil {
		if fh.Size > maxBlobSize {
			return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "file too large (max %d bytes)", maxBlobSize)
		}
		f, err := fh.Open()
		if err != nil {
			return nil, zip.ErrBadRequest("cannot read upload")
		}
		defer func() { _ = f.Close() }()
		return io.ReadAll(io.LimitReader(f, maxBlobSize+1))
	}
	body := c.Body()
	if len(body) > maxBlobSize {
		return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "file too large (max %d bytes)", maxBlobSize)
	}
	// Copy: c.Body() is a view into the reused fasthttp buffer, and the bytes are
	// handed to vfs.Put which may retain them past the request.
	out := make([]byte, len(body))
	copy(out, body)
	return out, nil
}

// blobKey is the physical, tenant-scoped VFS key. seg() sanitizes every component
// (org is the verified claim; ws is asserted in-org; blobId folds in a
// client-supplied value — seg() is the traversal guard on it).
func blobKey(org, workspace, blobID string) string {
	return "team/blobs/" + seg(org) + "/" + seg(workspace) + "/" + seg(blobID)
}

// contentType infers a response Content-Type from the filename extension, falling
// back to a safe generic. application/octet-stream (not text/html) so a stored blob
// can never be served as an executable HTML/script document (stored-XSS guard).
func contentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return "application/octet-stream"
	}
	if ct := mime.TypeByExtension(ext); ct != "" {
		if strings.HasPrefix(ct, "text/html") || strings.Contains(ct, "javascript") {
			return "application/octet-stream"
		}
		return ct
	}
	return "application/octet-stream"
}
