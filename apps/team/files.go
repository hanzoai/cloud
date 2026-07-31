package team

// This file is the workspace FILES plane (Phase 2A) — the blob store the Team SPA
// hits via its FrontStorage client (foundations/core/packages/storage-client/src/
// client/front.ts). team-go's pkg/files was only a 307 alias to Base's file API
// (no store), so this is a fresh implementation of the SAME FrontStorage contract
// backed by cloud's CANONICAL blob seam, deps.VFS (Put/Get) — no new store (ONE
// way).
//
// CONTRACT (front.ts, authoritative):
//   - upload:   POST {UPLOAD_URL}/{workspace}, multipart field "file" whose
//               FILENAME is the CLIENT-generated blob uuid (formData.append(
//               'file', file, uuid)); Authorization: Bearer <token>; response
//               body is DISCARDED (uploadFile returns void).
//   - download: GET /{workspace}/{filename}?file={blobId}&workspace={workspace}.
//
// TENANT ISOLATION (same invariant as the docs store, defense in depth):
//   - org is the VERIFIED session/workspace-token extra.org claim (never a header);
//   - the caller is asserted to be a MEMBER of :workspace (account store members
//     table) — not merely same-org — before any store/serve;
//   - the physical key embeds org+workspace+blobId (team/blobs/<org>/<ws>/<blobId>),
//     so a cross-org/-workspace blobId simply does not resolve (404). Multiple
//     independent layers; every denial is a 404 (no member/existence oracle).

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/types"
)

// maxBlobSize caps a single upload so an unbounded body can't exhaust the blob
// backend or memory. 100 MiB matches the attachment ceiling.
const maxBlobSize = 100 << 20

// filesService serves the workspace blob plane. vfs is cloud's blob seam (deps.VFS);
// accounts asserts workspace membership; secret verifies the session token.
// degraded is the fail-closed posture Mount resolved (no HS256 secret): a typed
// op cannot be wrapped by Mount's guard, so it asks for itself — see typed.go.
type filesService struct {
	vfs      types.VFSClient
	accounts *accountStore
	secret   string
	degraded bool
}

func (s *filesService) register(app cloud.Router, guard guardFn) {
	// The group is built HERE so cmd/zipdoc can resolve the typed op's prefix from
	// this file — see bots.go for why.
	g := app.Group(teamPrefix)
	// Workspace is in the PATH (front.ts POSTs to {UPLOAD_URL}/{workspace}).
	//
	// upload and download stay UNTYPED, and cannot be otherwise: upload's request
	// is a multipart form (not JSON) whose part filename IS the blob id, and
	// download's response is the blob's raw BYTES under a byte-derived
	// Content-Type. Neither is a shape a typed In/Out can describe.
	g.Post("/files/:workspace", guard(s.upload))
	g.Get("/files/:workspace/:filename", guard(s.download))
	// deleteFile: DELETE getFileUrl(ws, file) = /{workspace}/{file}?file={file}
	// (front.ts) — the download route shape, DELETE method. TYPED: a DELETE
	// addresses what it deletes with its URL, which is exactly what this one
	// already did, and it answers 204 with no body — declared, so the document
	// says 204 too.
	zip.Delete(g, "/files/:workspace/:filename", s.deleteBlob, zip.WithStatus(http.StatusNoContent))
}

// principal resolves (account, org) from the request's VERIFIED session or
// workspace token (bearer or the HttpOnly account cookie) — the shared
// orgPrincipal resolution (billing.go).
func (s *filesService) principal(c *zip.Ctx) (account, org string, err error) {
	return orgPrincipal(c, s.secret)
}

// authorize asserts :workspace belongs to org AND the caller is a MEMBER of it
// (Red F-C: bind files to workspace membership, not just same-org). Any failure is
// a 404 — no oracle distinguishing "no such workspace", "not your org", or "not a
// member". It takes the CONTEXT rather than the request because it needs nothing
// else off the wire, which is what lets the typed delete and the untyped
// upload/download share the one gate.
func (s *filesService) authorize(ctx context.Context, account, org, wsUUID string) error {
	wsUUID = strings.TrimSpace(wsUUID)
	if wsUUID == "" {
		return zip.ErrBadRequest("workspace required")
	}
	if s.accounts == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "team: file storage unavailable")
	}
	w, err := s.accounts.WorkspaceByUUID(ctx, org, wsUUID)
	if err != nil {
		return zip.ErrNotFound("workspace not found")
	}
	if _, ok := s.accounts.Membership(ctx, w.ID, account); !ok {
		return zip.ErrNotFound("workspace not found")
	}
	return nil
}

// upload stores the uploaded bytes under the CLIENT-supplied blob uuid (the
// multipart file's filename). The server does NOT mint the id — the front owns it
// (front.ts: formData.append('file', file, uuid)). Response body is irrelevant
// (uploadFile discards it); we echo the id for curl/debug.
func (s *filesService) upload(c *zip.Ctx) error {
	account, org, err := s.principal(c)
	if err != nil {
		return zip.ErrUnauthorized("invalid session token")
	}
	ws := c.Param("workspace")
	if err := s.authorize(c.Context(), account, org, ws); err != nil {
		return err
	}
	fh, err := c.Fiber().FormFile("file")
	if err != nil || fh == nil {
		return zip.ErrBadRequest(`multipart field "file" required`)
	}
	// The blob id is the multipart filename (client-generated uuid v4). Validate it
	// is a UUID — the front always sends one, and this rejects any other value from
	// becoming a storage key (belt: seg() also sanitizes it in blobKey).
	blobID := fh.Filename
	if uuid.Validate(blobID) != nil {
		return zip.ErrBadRequest("blob id (multipart filename) must be a uuid")
	}
	if fh.Size > maxBlobSize {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "file too large (max %d bytes)", maxBlobSize)
	}
	f, err := fh.Open()
	if err != nil {
		return zip.ErrBadRequest("cannot read upload")
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxBlobSize+1))
	if err != nil {
		return zip.ErrBadRequest("cannot read upload")
	}
	if len(data) > maxBlobSize {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "file too large (max %d bytes)", maxBlobSize)
	}
	if len(data) == 0 {
		return zip.ErrBadRequest("empty upload")
	}
	if err := s.vfs.Put(c.Context(), blobKey(org, ws, blobID), data); err != nil {
		// deps.VFS is DisabledVFS (fail-closed) unless the operator wires a real VFS
		// backend — an honest 502, never a silent success.
		return zip.Errorf(http.StatusBadGateway, "file storage unavailable")
	}
	return c.String(http.StatusOK, blobID)
}

// download streams a blob by its client id (?file=). The served Content-Type is
// derived from the STORED BYTES via a strict image allow-list — NEVER from the
// client :filename (Red F-B: a crafted .svg name would otherwise force
// image/svg+xml → active XSS). Anything not a recognized raster image is served
// inert: application/octet-stream + attachment + nosniff.
func (s *filesService) download(c *zip.Ctx) error {
	account, org, err := s.principal(c)
	if err != nil {
		return zip.ErrUnauthorized("invalid session token")
	}
	ws := c.Param("workspace")
	if err := s.authorize(c.Context(), account, org, ws); err != nil {
		return err
	}
	blobID := strings.TrimSpace(c.Query("file"))
	if blobID == "" {
		return zip.ErrBadRequest("file (blob id) required")
	}
	data, err := s.vfs.Get(c.Context(), blobKey(org, ws, blobID))
	switch {
	case errors.Is(err, types.ErrBlobNotFound), err == nil && data == nil:
		// Genuine miss (working backend). A cross-org/-workspace blobId is a DIFFERENT
		// key that also does not exist → the SAME 404 (no existence oracle).
		return zip.ErrNotFound("blob not found")
	case err != nil:
		// The backend is unavailable/disabled (deps.VFS never nil per R-7) → fail
		// CLOSED with 502, never a nil-deref 500. Blanket across all keys, so it
		// reveals no per-blob information.
		return zip.Errorf(http.StatusBadGateway, "file storage unavailable")
	}
	// nosniff on EVERY response so the browser never re-sniffs the declared type.
	c.SetHeader("X-Content-Type-Options", "nosniff")
	c.SetHeader("Cache-Control", "private, max-age=31536000, immutable")
	if img := imageType(data); img != "" {
		// Recognized raster image → serve inline with its true (byte-derived) type.
		c.SetHeader("Content-Type", img)
	} else {
		// Everything else is inert: no inline rendering, forced download.
		c.SetHeader("Content-Type", "application/octet-stream")
		c.SetHeader("Content-Disposition", "attachment; filename=\""+safeFilename(c.Param("filename"))+"\"")
	}
	return c.Bytes(http.StatusOK, data)
}

// blobRef addresses one workspace blob from the URL, which is the whole input a
// DELETE has: the workspace and the blob id are path segments, and `file` is the
// query the front actually carries the id in.
type blobRef struct {
	// Workspace is the workspace uuid the blob belongs to, from the path.
	Workspace string `json:"workspace"`
	// Filename is the last path segment, which the front sets to the blob id
	// when it sends no explicit `file`.
	Filename string `json:"filename"`
	// File is the blob id, and wins over the path segment when both are present.
	File string `json:"file"`
}

// DeleteBlob removes one blob from a workspace's file store. The caller must
// hold a verified session AND be a member of the workspace; anything else — an
// unknown workspace, another tenant's workspace, a workspace the caller is not
// in — answers the same 404, so a probe learns nothing about what exists.
//
// It is IDEMPOTENT: deleting a present or an absent blob both answer 204, so a
// delete never confirms a blob's existence and a foreign blob id (a physical key
// the caller can never name into another tenant's box) is a harmless no-op. A
// storage backend that is unavailable fails closed with 502 rather than lying
// about success.
//
// Example: {"workspace": "6579…", "file": "0d4f…"}
func (s *filesService) deleteBlob(ctx context.Context, in *blobRef) (*none, error) {
	if s.degraded {
		return nil, unavailable()
	}
	account, org, err := sessionOf(ctx, s.secret)
	if err != nil {
		return nil, zip.ErrUnauthorized("invalid session token")
	}
	ws := in.Workspace
	if err := s.authorize(ctx, account, org, ws); err != nil {
		return nil, err
	}
	// deleteFile calls getFileUrl(ws, file) with no filename → path segment == the
	// blob id; ?file= carries it too. Accept either, prefer the explicit ?file=.
	blobID := strings.TrimSpace(firstNonEmpty(in.File, in.Filename))
	if blobID == "" {
		return nil, zip.ErrBadRequest("file (blob id) required")
	}
	// Idempotent + no-oracle on a WORKING backend: a present OR missing blob both
	// return 204 (a missing key, ErrBlobNotFound, is not a caller-visible error), so
	// deleting never confirms existence and a foreign blobId is a harmless no-op.
	// But a backend that is unavailable/disabled (any OTHER error) fails CLOSED with
	// 502 — never a silent success lie, never a nil-deref 500.
	if err := s.vfs.Delete(ctx, blobKey(org, ws, blobID)); err != nil && !errors.Is(err, types.ErrBlobNotFound) {
		return nil, zip.Errorf(http.StatusBadGateway, "file storage unavailable")
	}
	return nil, nil
}

// blobKey is the physical, tenant-scoped VFS key. seg() sanitizes every component
// (org is the verified claim; ws is asserted in-org + member; blobId is a validated
// uuid — seg() is the last-line traversal guard on it).
func blobKey(org, workspace, blobID string) string {
	return "team/blobs/" + seg(org) + "/" + seg(workspace) + "/" + seg(blobID)
}

// imageType returns the canonical MIME type of a recognized raster image from its
// MAGIC BYTES, or "" for anything else. This is the ALLOW-LIST (Red F-B): only
// image/png|jpeg|gif|webp are ever served inline, and only when the STORED bytes
// actually are that image — a mislabeled/crafted upload (SVG, HTML, XHTML, …)
// falls through to "" and is served inert. Deterministic; no reliance on the
// client filename or Go's evolving sniff table.
func imageType(data []byte) string {
	switch {
	case len(data) >= 8 && bytes.Equal(data[:8], []byte{0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A}):
		return "image/png"
	case len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF:
		return "image/jpeg"
	case len(data) >= 6 && (string(data[:6]) == "GIF87a" || string(data[:6]) == "GIF89a"):
		return "image/gif"
	case len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP":
		return "image/webp"
	}
	return ""
}

// safeFilename reduces a client filename to a header-safe download name: only
// [A-Za-z0-9._-] survive (killing quotes, CR/LF header-injection and path
// separators); empty/dot-only → "download".
func safeFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || out == "." || out == ".." {
		return "download"
	}
	return out
}
