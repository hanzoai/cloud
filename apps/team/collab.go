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
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/types"
)

// maxMarkupSize caps one markup field so an unbounded body can't exhaust the
// blob backend. 10 MiB of ProseMirror JSON is far beyond any real document.
const maxMarkupSize = 10 << 20

// collabPrefix is THE path both collaborator planes hang under — app-level, NOT
// under /v1/team, because the front derives both from COLLABORATOR_URL. One
// constant, so the group the RPC is declared on cannot drift from the WebSocket
// beside it, and because cmd/zipdoc resolves a typed op's prefix from the
// CONSTANT VALUE of the Group argument.
const collabPrefix = "/collaborator"

// collabService serves the collaborator planes: the markup-snapshot RPC lane
// (this file) and the live hocuspocus WS lane (collabws.go), sharing one
// tenancy gate and one VFS seam. degraded is the fail-closed posture Mount
// resolved (no HS256 secret): a typed op cannot be wrapped by Mount's guard, so
// it asks for itself — see typed.go.
type collabService struct {
	vfs      types.VFSClient
	accounts *accountStore
	ident    *identity
	hub      *collabHub
	degraded bool
}

func (s *collabService) register(app cloud.Router, guard guardFn) {
	// The live Y.js WebSocket (wss://<host>/collaborator). UNTYPED, and it cannot
	// be otherwise: the response is a protocol upgrade, not a value.
	app.Get(collabPrefix, guard(s.ws))
	g := app.Group(collabPrefix)
	// The RPC is a typed op, so it receives only a context: it authenticates with
	// team's OWN HS256 token, which rides in a header or the account cookie, and
	// cloud.Request is the only way to reach either (typed.go). cloud.Bridge
	// parks that request, and the composer owns that install, once at its root.
	zip.Post(g, "/rpc/:documentId", s.rpc)
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
// must never be clobbered).
//
// It seeds THROUGH the collab hub (seedIfAbsent), not with a bare Get-then-Put. The
// old get-then-put had a TOCTOU: between the Get(miss) and the Put, a concurrent first
// edit on the live WS lane could land and then be overwritten by the seed. Routing
// through the hub reuses the room's flushMu/mu, so the seed and any live edit for this
// field serialize on ONE room and the seed applies only when the log is still empty.
// docName is the canonical documentId the hocuspocus provider uses for THIS field, so
// the seed and the field's editor share exactly one room; the blob id (yLogBlobID for
// the SAME objectID+field) is what the WS room loads.
func (s *collabService) seedYLog(ctx context.Context, org string, doc collabDoc, field, b64 string) error {
	if b64 == "" {
		return nil
	}
	update, err := base64.StdEncoding.DecodeString(b64)
	if err != nil || len(update) == 0 || len(update) > maxMarkupSize {
		return nil
	}
	fieldDoc := collabDoc{workspace: doc.workspace, objectClass: doc.objectClass, objectID: doc.objectID, objectAttr: field}
	docName := fieldDoc.workspace + "|" + fieldDoc.objectClass + "|" + fieldDoc.objectID + "|" + fieldDoc.objectAttr
	return s.hub.seedIfAbsent(ctx, org, doc.workspace, docName, fieldDoc, update)
}

// collabPayload is the argument of one collaborator RPC — the union of what the
// three verbs take, which is what the client sends: createContent and
// updateContent carry `content` (and createContent may carry `updates`),
// getContent carries `source`.
type collabPayload struct {
	// Content maps a document field to its ProseMirror markup JSON.
	Content map[string]string `json:"content"`
	// Source is the blob ref a getContent reads the snapshot from. Absent means
	// there is no snapshot to read, which answers empty content.
	Source string `json:"source"`
	// Updates carries, per field, a base64 Y.js state update encoding the SAME
	// markup — the front computes it (markupToYDoc → encodeStateAsUpdate) so a
	// createContent seeds the live-editing lane's update log, not just the
	// snapshot blob. Without it a dialog-created description is invisible in the
	// collaborative editor, which replays the ydoc log, never the snapshot.
	Updates map[string]string `json:"updates"`
}

// collabRequest is one collaborator RPC: the document from the path, the verb,
// and the verb's payload.
type collabRequest struct {
	// DocumentID addresses the document field, as
	// "<workspaceUuid>|<objectClass>|<objectId>|<objectAttr>" — the
	// collaborator-client encodeDocumentId shape, from the path.
	DocumentID string `json:"documentId"`
	// Method is the verb: createContent, updateContent or getContent.
	Method string `json:"method"`
	// Payload is the verb's argument.
	Payload collabPayload `json:"payload"`
}

// collabResult is the RPC reply. Content is a POINTER because the three verbs
// answer three different shapes and the difference is load-bearing to the
// client: createContent and getContent always carry `content` — possibly an
// EMPTY object, which is the honest answer for a getContent with no snapshot —
// while updateContent carries nothing at all and must stay `{}`. A plain map
// with omitempty would collapse the first case into the second.
type collabResult struct {
	// Content maps each document field to its value for the verb: the new blob
	// ref after a createContent, the stored markup after a getContent.
	Content *map[string]string `json:"content,omitempty"`
	// Error carries a SEMANTIC refusal, which this RPC reports under 200 because
	// the client throws on result.error — auth and tenancy failures are HTTP
	// statuses instead.
	Error string `json:"error,omitempty"`
}

// CollabRPC is the collaborative-markup snapshot plane the Team front's editor
// speaks: createContent stores a document field's markup at a fresh, immutable
// blob ref and returns it, updateContent stores a new snapshot and answers
// nothing, and getContent reads back the exact snapshot a ref names.
//
// createContent ALSO seeds the live-editing update log from the front-supplied
// Y.js update, so a dialog-authored description is visible in the collaborative
// editor — which replays that log — and not only in snapshot reads.
// updateContent never touches that log: peers may be live-editing the document,
// and their edits are not this call's to overwrite.
//
// Every call is scoped to the caller's VERIFIED session or workspace token: the
// documentId's workspace must be the token's workspace when the token names one,
// and the caller must be a member of it. An unknown workspace, another tenant's
// workspace and a workspace the caller is not in all answer the same 404, so a
// probe learns nothing about what exists.
//
// Example: {"documentId": "6579…|tracker:class:Issue|issue-1|description", "method": "getContent", "payload": {"source": "issue-1-description-1730000000000"}}
func (s *collabService) rpc(ctx context.Context, in *collabRequest) (*collabResult, error) {
	if s.degraded {
		return nil, unavailable()
	}
	cl, err := callerOf(ctx, s.ident)
	if err != nil {
		return nil, zip.ErrUnauthorized("invalid session token")
	}
	org := cl.org
	if org == "" {
		return nil, zip.ErrUnauthorized("invalid session token")
	}
	doc, err := decodeCollabDoc(in.DocumentID)
	if err != nil {
		return nil, zip.ErrBadRequest("malformed documentId")
	}
	// An HS256 workspace token names its workspace — the documentId must agree. A
	// credential that names none (a session token, and every IAM caller) falls
	// through to the membership check, which is the whole authorization there.
	if cl.workspace != "" && cl.workspace != doc.workspace {
		return nil, zip.ErrNotFound("document not found")
	}
	if s.accounts == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "team: collaborator unavailable")
	}
	if _, err := s.ident.admit(ctx, cl, doc.workspace); err != nil {
		return nil, zip.ErrNotFound("document not found")
	}

	switch in.Method {
	case "createContent", "updateContent":
		refs := map[string]string{}
		now := time.Now()
		for field, markup := range in.Payload.Content {
			if len(markup) > maxMarkupSize {
				return nil, zip.Errorf(http.StatusRequestEntityTooLarge, "markup too large (max %d bytes)", maxMarkupSize)
			}
			blobID := collabJSONID(doc.objectID, field, now)
			if err := s.vfs.Put(ctx, blobKey(org, doc.workspace, blobID), []byte(markup)); err != nil {
				return nil, zip.Errorf(http.StatusBadGateway, "blob storage unavailable")
			}
			refs[field] = blobID
			// createContent births a NEW object: seed the live-editing lane's update
			// log from the front-supplied Y.js update so the collaborative editor
			// (collabws.go, which replays this log) shows the content, not just the
			// snapshot reads. Scoped to createContent — updateContent must never
			// clobber a doc that peers may be live-editing — and only when no log
			// exists yet (belt-and-suspenders against a double create).
			if in.Method == "createContent" {
				if err := s.seedYLog(ctx, org, doc, field, in.Payload.Updates[field]); err != nil {
					return nil, zip.Errorf(http.StatusBadGateway, "blob storage unavailable")
				}
			}
		}
		if in.Method == "updateContent" {
			return &collabResult{}, nil
		}
		return &collabResult{Content: &refs}, nil
	case "getContent":
		content := map[string]string{}
		if src := strings.TrimSpace(in.Payload.Source); src != "" {
			data, err := s.vfs.Get(ctx, blobKey(org, doc.workspace, src))
			if err != nil || data == nil {
				// A miss is a real empty answer (the client renders empty markup); a
				// broken backend must not fabricate content either — same shape.
				return &collabResult{Content: &content}, nil
			}
			content[doc.objectAttr] = string(data)
		}
		return &collabResult{Content: &content}, nil
	default:
		return &collabResult{Error: "unknown method " + in.Method}, nil
	}
}
