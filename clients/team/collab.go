package team

// This file is the collaborator RPC plane — the HTTP half of the collaborative
// markup contract the Team front's @hanzo/collaborator-client speaks
// (foundations/core/packages/collaborator-client/src/client.ts, authoritative):
//
//	POST {COLLABORATOR_URL http(s)}/rpc/{documentId}   body {method, payload}
//
//	documentId = "<workspaceUuid>|<objectClass>|<objectId>|<objectAttr>"
//	  - createContent {content:{field:markup}, updates?:{field:b64yUpdate}}
//	                                           → {content:{field:blobRef}}
//	  - updateContent {content:{field:markup}} → {}
//	  - getContent    {source?:blobRef}        → {content:{field:markup}}
//
// createContent ALSO seeds the live-editing update log (collabws.go) from the
// front-supplied Y.js update, so a dialog-authored description is visible in the
// collaborative editor — which replays that log — not only in snapshot reads.
//
// The LIVE editing lane (Y.js sync) is the /collaborator WebSocket served by
// collabws.go in this same service; this RPC lane is markup-snapshot blob I/O,
// and blobs are cloud's domain (deps.VFS — the SAME seam and tenant-scoped key
// layout as files.go). The ingress routes both /collaborator/rpc and
// /collaborator (WS) to cloud.
//
// Snapshot semantics mirror the reference server (server/collaborator rpc):
// createContent/updateContent persist the markup JSON at a timestamped blob id
// (core makeCollabJsonId: "<objectId>-<field>-<unixMillis>"): snapshots are
// immutable, the newest ref is whatever the caller stores on its doc.
// getContent reads the EXACT source snapshot; without a source there is no live
// ydoc here to transform, so it answers empty content (the reference's ydoc
// transform lives in the relay lane).
//
// TENANT ISOLATION (same invariant as files.go, defense in depth): org is the
// VERIFIED token claim; the documentId's workspace segment must be the token's
// workspace (when the token carries one) AND the caller must be a MEMBER of it;
// the physical key embeds org+workspace so a foreign ref cannot resolve.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/types"
)

// maxMarkupSize caps one markup field so an unbounded body can't exhaust the
// blob backend. 10 MiB of ProseMirror JSON is far beyond any real document.
const maxMarkupSize = 10 << 20

// collabService serves the collaborator planes: the markup-snapshot RPC lane
// (this file) and the live hocuspocus WS lane (collabws.go), sharing one
// tenancy gate and one VFS seam.
type collabService struct {
	vfs      types.VFSClient
	accounts *accountStore
	secret   string
	hub      *collabHub
}

func (s *collabService) register(app *zip.App, guard guardFn) {
	// App-level (NOT under /v1/team): the front derives both paths from
	// COLLABORATOR_URL (wss://<host>/collaborator → the live Y.js WebSocket;
	// http(s)://<host>/collaborator/rpc/:id → the snapshot RPC).
	app.Post("/collaborator/rpc/:documentId", guard(s.rpc))
	app.Get("/collaborator", guard(s.ws))
}

// collabDoc is the decoded documentId (collaborator-client encodeDocumentId).
type collabDoc struct {
	workspace   string
	objectClass string
	objectID    string
	objectAttr  string
}

func decodeCollabDoc(raw string) (collabDoc, error) {
	if un, err := url.PathUnescape(raw); err == nil {
		raw = un
	}
	parts := strings.Split(raw, "|")
	if len(parts) != 4 {
		return collabDoc{}, fmt.Errorf("malformed documentId")
	}
	d := collabDoc{workspace: parts[0], objectClass: parts[1], objectID: parts[2], objectAttr: parts[3]}
	if d.workspace == "" || d.objectID == "" || d.objectAttr == "" {
		return collabDoc{}, fmt.Errorf("malformed documentId")
	}
	return d, nil
}

// collabJSONID mirrors @hanzo/core makeCollabJsonId: "<objectId>-<field>-<ms>".
func collabJSONID(objectID, field string, now time.Time) string {
	return fmt.Sprintf("%s-%s-%d", objectID, field, now.UnixMilli())
}

// seedYLog writes the live-editing update log for a NEWLY created doc field from
// the front-supplied base64 Y.js update, so the collaborative editor (collabws.go,
// which replays this log) shows dialog-authored content — not just the snapshot
// reads. No-op when no update is supplied, when it is malformed (the snapshot still
// stands — no worse than before), or when a log already exists (a live-edited doc
// must never be clobbered). One log entry: a full-state update the joining editor
// applies against its empty ydoc. The blob id matches collabws.go's yLogBlobID for
// the SAME (objectID, field), so the WS room loads exactly what this seeded.
func (s *collabService) seedYLog(ctx context.Context, org, workspace, objectID, field, b64 string) error {
	if b64 == "" {
		return nil
	}
	update, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(update) == 0 || len(update) > maxMarkupSize {
		return nil
	}
	key := blobKey(org, workspace, yLogBlobID(collabDoc{objectID: objectID, objectAttr: field}))
	if data, gerr := s.vfs.Get(ctx, key); gerr == nil && len(data) > 0 {
		return nil // a log already exists — never clobber live edits
	} else if gerr != nil && !errors.Is(gerr, types.ErrBlobNotFound) {
		return gerr
	}
	return s.vfs.Put(ctx, key, marshalYLog([][]byte{update}))
}

type collabRequest struct {
	Method  string `json:"method"`
	Payload struct {
		Content map[string]string `json:"content"`
		Source  string            `json:"source"`
		// Updates carries, per field, a base64 Y.js state update encoding the SAME
		// markup — the front computes it (markupToYDoc → encodeStateAsUpdate) so a
		// createContent seeds the live-editing lane's update log, not just the
		// snapshot blob. Without it a dialog-created description is invisible in the
		// collaborative editor, which replays the ydoc log, never the snapshot.
		Updates map[string]string `json:"updates"`
	} `json:"payload"`
}

// rpc dispatches one collaborator RPC. Semantic failures are 200 + {error}
// (the client throws on result.error); auth/tenancy failures are HTTP codes.
func (s *collabService) rpc(c *zip.Ctx) error {
	t, _, err := sessionToken(c, s.secret)
	if err != nil {
		return zip.ErrUnauthorized("invalid session token")
	}
	org, _ := t.Extra["org"].(string)
	if strings.TrimSpace(org) == "" {
		return zip.ErrUnauthorized("invalid session token")
	}
	doc, err := decodeCollabDoc(c.Param("documentId"))
	if err != nil {
		return zip.ErrBadRequest("malformed documentId")
	}
	// The workspace token names its workspace — the documentId must agree. A
	// session token (no workspace claim) falls through to the membership check.
	if t.Workspace != "" && t.Workspace != doc.workspace {
		return zip.ErrNotFound("document not found")
	}
	if s.accounts == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "team: collaborator unavailable")
	}
	w, err := s.accounts.WorkspaceByUUID(c.Context(), org, doc.workspace)
	if err != nil {
		return zip.ErrNotFound("document not found")
	}
	if _, ok := s.accounts.Membership(c.Context(), w.ID, t.Account); !ok {
		return zip.ErrNotFound("document not found")
	}

	var req collabRequest
	if err := c.Bind(&req); err != nil {
		return zip.ErrBadRequest("invalid request body")
	}
	switch req.Method {
	case "createContent", "updateContent":
		refs := map[string]string{}
		now := time.Now()
		for field, markup := range req.Payload.Content {
			if len(markup) > maxMarkupSize {
				return zip.Errorf(http.StatusRequestEntityTooLarge, "markup too large (max %d bytes)", maxMarkupSize)
			}
			blobID := collabJSONID(doc.objectID, field, now)
			if err := s.vfs.Put(c.Context(), blobKey(org, doc.workspace, blobID), []byte(markup)); err != nil {
				return zip.Errorf(http.StatusBadGateway, "blob storage unavailable")
			}
			refs[field] = blobID
			// createContent births a NEW object: seed the live-editing lane's update
			// log from the front-supplied Y.js update so the collaborative editor
			// (collabws.go, which replays this log) shows the content, not just the
			// snapshot reads. Scoped to createContent — updateContent must never
			// clobber a doc that peers may be live-editing — and only when no log
			// exists yet (belt-and-suspenders against a double create).
			if req.Method == "createContent" {
				if err := s.seedYLog(c.Context(), org, doc.workspace, doc.objectID, field, req.Payload.Updates[field]); err != nil {
					return zip.Errorf(http.StatusBadGateway, "blob storage unavailable")
				}
			}
		}
		if req.Method == "updateContent" {
			return c.JSON(http.StatusOK, map[string]any{})
		}
		return c.JSON(http.StatusOK, map[string]any{"content": refs})
	case "getContent":
		content := map[string]string{}
		if src := strings.TrimSpace(req.Payload.Source); src != "" {
			data, err := s.vfs.Get(c.Context(), blobKey(org, doc.workspace, src))
			if err != nil || data == nil {
				// A miss is a real empty answer (the client renders empty markup); a
				// broken backend must not fabricate content either — same shape.
				return c.JSON(http.StatusOK, map[string]any{"content": content})
			}
			content[doc.objectAttr] = string(data)
		}
		return c.JSON(http.StatusOK, map[string]any{"content": content})
	default:
		return c.JSON(http.StatusOK, map[string]any{"error": "unknown method " + req.Method})
	}
}
