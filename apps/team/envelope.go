// Package team mounts the Hanzo Cloud /v1/team/* surface: the native-Go port of
// hanzo team-go (HIP-0106, task #45) into the unified cloud binary. Phase 1 is
// the SPA READ PLANE + bots-as-members:
//
//	GET  /v1/team/health                       liveness (HIP-0106 uniform contract)
//	POST /v1/team/account                       JSON-RPC login + workspace selection
//	GET  /v1/team/account/providers             login providers (IAM only)
//	GET  /v1/team/account/auth/:provider        IAM OAuth start (redirect)
//	GET  /v1/team/account/auth/:provider/callback IAM OAuth callback (mint token)
//	PUT  /v1/team/account/cookie                set the HttpOnly session cookie
//	DEL  /v1/team/account/cookie                clear it
//	GET  /v1/team/transactor/:token             the workspace data-plane WebSocket
//	GET  /v1/team/bots                          list the org's bot members (read)
//	POST /v1/team/bots/sync                      re-project the org's agents (admin)
//
// TENANT ISOLATION. The org (tenant key) is NEVER a client-supplied header on any
// data path:
//
//   - transactor: org = the `extra.org` claim of the HS256 workspace token in the
//     :token path segment, minted by selectWorkspace and VERIFIED (token.Decode
//     verify=true) against SERVER_SECRET before the WebSocket upgrade. Every docs
//     SQLite file lives at {DataDir}/team/workspaces/orgs/<org>/ws/<ws>.db.
//   - account RPC: org = the `extra.org` claim of the HS256 session token in the
//     bearer/cookie, likewise VERIFIED. Every account-store query filters by org;
//     selectWorkspace resolves the workspace scoped to (org, slug) so a foreign
//     tenant's slug is unresolvable.
//   - bots read routes: org = principal.Org(c) — the value the identity
//     middleware minted from the VALIDATED IAM owner claim (HIP-0026) — and never
//     a client X-Org-Id.
//
// The session-token org is itself minted from the VERIFIED IAM `owner` claim at
// the OAuth callback, so the chain IAM owner → session token → workspace token →
// docs path is signed end-to-end and a client can forge none of it.
//
// Order 138: binds /v1/team/* before the AI subsystem's /v1/* catch-all (150).
package team

import (
	"encoding/binary"
	"errors"
)

// This file is the ZAP envelope codec — ported VERBATIM from
// github.com/hanzoai/team-go/pkg/transactor/envelope.go. Every frame on the
// transactor WebSocket is a single ZAP Envelope object tunnelling one RPC. The
// browser side (a byte-identical TS port in the team front's zap-envelope.ts)
// speaks the same Envelope; the exact bytes are locked by TestGoldenHex — the
// same golden the TS port is verified against.
//
// Envelope wire layout (ZAP Version2, little-endian):
//
//	header[16]  "ZAP\0" | version u16=2 | flags u16=0 | rootOffset u32=16 | size u32
//	object @16  fixed section, 24 bytes:
//	            id      @0  u32
//	            kind    @4  u8   (0=request 1=response 2=push)
//	            method  @8  ptr  (relOffset u32 @8,  length u32 @12)
//	            payload @16 ptr  (relOffset u32 @16, length u32 @20)
//	            then method bytes, then payload bytes — appended after the fixed
//	            section in field order. An empty value is a (relOffset=0,len=0)
//	            null pointer with no bytes appended.

// Kind discriminates the three frame directions on the wire.
type Kind uint8

const (
	KindRequest  Kind = 0 // client → server call
	KindResponse Kind = 1 // server → client reply (correlated by ID)
	KindPush     Kind = 2 // server → client broadcast (ID is ignored)
)

const (
	zapHeader   = 16                // header size
	zapRoot     = 16                // root object offset (right after the header)
	zapData     = 24                // fixed section size: id4+kind1+pad3 + method ptr8 + payload ptr8
	zapFixedEnd = zapRoot + zapData // 40 — where the variable section begins
)

// field offsets within the object's fixed section.
const (
	fID      = 0
	fKind    = 4
	fMethod  = 8
	fPayload = 16
)

// ErrInvalid is returned for a malformed ZAP frame.
var ErrInvalid = errors.New("zap: invalid frame")

// Envelope is one decoded ZAP frame.
type Envelope struct {
	ID      uint32
	Kind    Kind
	Method  string
	Payload []byte // JSON
}

var le = binary.LittleEndian

// Encode serializes e into a single ZAP message.
func Encode(e Envelope) []byte {
	method := []byte(e.Method)
	payload := e.Payload

	// Append method then payload after the fixed section; each pointer's forward
	// relative offset is measured from its own field position.
	pos := zapFixedEnd
	var mRel uint32
	mAt := pos
	if len(method) > 0 {
		mRel = uint32(pos - (zapRoot + fMethod))
		pos += len(method)
	}
	var pRel uint32
	pAt := pos
	if len(payload) > 0 {
		pRel = uint32(pos - (zapRoot + fPayload))
		pos += len(payload)
	}
	size := pos

	buf := make([]byte, size)
	copy(buf[0:4], "ZAP\x00")
	le.PutUint16(buf[4:6], 2) // version
	le.PutUint16(buf[6:8], 0) // flags
	le.PutUint32(buf[8:12], zapRoot)
	le.PutUint32(buf[12:16], uint32(size))

	le.PutUint32(buf[zapRoot+fID:], e.ID)
	buf[zapRoot+fKind] = uint8(e.Kind)
	le.PutUint32(buf[zapRoot+fMethod:], mRel)
	le.PutUint32(buf[zapRoot+fMethod+4:], uint32(len(method)))
	le.PutUint32(buf[zapRoot+fPayload:], pRel)
	le.PutUint32(buf[zapRoot+fPayload+4:], uint32(len(payload)))

	copy(buf[mAt:], method)
	copy(buf[pAt:], payload)
	return buf
}

// Decode parses a ZAP message into an Envelope. The payload is copied out so the
// caller may retain it past the frame's lifetime.
func Decode(data []byte) (Envelope, error) {
	if len(data) < zapHeader || string(data[0:4]) != "ZAP\x00" {
		return Envelope{}, ErrInvalid
	}
	root := int(le.Uint32(data[8:12]))
	if root < zapHeader || root+zapData > len(data) {
		return Envelope{}, ErrInvalid
	}
	payload := readBytes(data, root+fPayload)
	cp := make([]byte, len(payload))
	copy(cp, payload)
	return Envelope{
		ID:      le.Uint32(data[root+fID:]),
		Kind:    Kind(data[root+fKind]),
		Method:  string(readBytes(data, root+fMethod)),
		Payload: cp,
	}, nil
}

// readBytes reads a (relOffset, length) forward pointer at pos. relOffset 0 is a
// null/empty pointer; out-of-bounds targets read as empty.
func readBytes(data []byte, pos int) []byte {
	if pos+8 > len(data) {
		return nil
	}
	rel := le.Uint32(data[pos:])
	if rel == 0 {
		return nil
	}
	length := int(le.Uint32(data[pos+4:]))
	abs := pos + int(rel)
	if abs < zapHeader || abs+length > len(data) {
		return nil
	}
	return data[abs : abs+length]
}
