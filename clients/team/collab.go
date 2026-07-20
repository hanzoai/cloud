package team

// This file is the collaborator RPC plane — the HTTP half of the collaborative
// markup contract the Team front's @hanzo/collaborator-client speaks
// (foundations/core/packages/collaborator-client/src/client.ts, authoritative):
//
//	POST {COLLABORATOR_URL http(s)}/rpc/{documentId}   body {method, payload}
//
//	documentId = "<workspaceUuid>|<objectClass>|<objectId>|<objectAttr>"
//	  - createContent {content:{field:markup}} → {content:{field:blobRef}}
//	  - updateContent {content:{field:markup}} → {}
//	  - getContent    {source?:blobRef}        → {content:{field:markup}}
//
// The LIVE editing lane (Y.js sync) stays on the /collaborator WebSocket to the
// collab relay; this RPC lane is markup-snapshot blob I/O, and blobs are cloud's
// domain (deps.VFS — the SAME seam and tenant-scoped key layout as files.go).
// The ingress path-splits /collaborator/rpc → cloud while /collaborator (WS)
// keeps routing to the relay.
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

// collabService serves the collaborator RPC lane.
type collabService struct {
	vfs      types.VFSClient
	accounts *accountStore
	secret   string
}

func (s *collabService) register(app *zip.App, guard guardFn) {
	// App-level (NOT under /v1/team): the front derives this path from
	// COLLABORATOR_URL (wss://<host>/collaborator → POST /collaborator/rpc/:id).
	app.Post("/collaborator/rpc/:documentId", guard(s.rpc))
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

type collabRequest struct {
	Method  string `json:"method"`
	Payload struct {
		Content map[string]string `json:"content"`
		Source  string            `json:"source"`
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
