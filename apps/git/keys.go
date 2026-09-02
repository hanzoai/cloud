package git

import (
	"github.com/hanzoai/cloud"
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud/internal/mint"
	"github.com/zap-proto/zip"
	"golang.org/x/crypto/ssh"
)

// keys.go is the control-plane surface for the SSH public-key registry:
//
//	POST   /v1/git/keys        register a key (title + openssh pubkey) -> keyView (201)
//	GET    /v1/git/keys        list the org's keys                    -> {data:[keyView]}
//	DELETE /v1/git/keys/:id    remove a key                           -> 204
//
// These are thin adapters over keystore.go, org-scoped identically to the
// repo routes (principal.Org → X-Org-Id). A key is stored with its SHA256
// fingerprint as the global unique handle; SSH auth (ssh.go) resolves a
// presented key to its owner by that fingerprint.

// registerKeyReq is the register-a-key request body.
type registerKeyReq struct {
	// Title labels the key in the console. Max 256 chars; when omitted the
	// comment on the key line is used.
	Title string `json:"title"`
	// PublicKey is one OpenSSH authorized-key line ("ssh-ed25519 AAAA… you@host").
	// Required; a line that does not parse is refused and never stored.
	PublicKey string `json:"publicKey"`
}

// registerKey registers an SSH public key so it can authenticate `git clone
// git@<host>:<org>/<repo>.git` for the caller's org. The key line is parsed and
// canonicalized before storage, its SHA256 fingerprint becomes the auth lookup
// handle, and the full public key round-trips (it is public). Answers 201.
// Fingerprints are globally unique, so a key already registered — to this org or
// any other — is a 409: one key belongs to exactly one org.
//
// Example: {"title": "laptop", "publicKey": "ssh-ed25519 AAAAC3Nz… z@hanzo.ai"}
func (o ops) registerKey(ctx context.Context, in *registerKeyReq) (*keyView, error) {
	t, terr := tenantOf(ctx)
	if terr != nil {
		return nil, terr
	}
	raw := strings.TrimSpace(in.PublicKey)
	if raw == "" {
		return nil, zip.ErrBadRequest("publicKey is required")
	}
	title := strings.TrimSpace(in.Title)
	if len(title) > 256 {
		return nil, zip.ErrBadRequest("title too long (max 256)")
	}
	// Parse the authorized-key line to validate it and canonicalize the stored
	// form + fingerprint. A malformed key is a 400, never stored.
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(raw))
	if err != nil {
		return nil, zip.ErrBadRequest("invalid openssh public key")
	}
	if title == "" {
		title = strings.TrimSpace(comment) // fall back to the key comment as the label
	}
	// Canonical authorized-key line (type + base64), no trailing newline.
	canonical := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	fp := ssh.FingerprintSHA256(pub)

	// The owner is the BRIDGED principal (ops.go), never an In field: an In field
	// is caller-supplied, so a key written under a user read from one would let a
	// caller register a key in someone else's name.
	row := sshKey{
		ID: mint.ID("gitkey"), Org: t.org, UserID: t.user, Title: title,
		PublicKey: canonical, Fingerprint: fp, CreatedAt: time.Now().Unix(),
	}
	if err := o.s.State.keys.Add(ctx, row); err != nil {
		if errors.Is(err, errKeyConflict) {
			return nil, zip.ErrConflict("this ssh key is already registered")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "register key: %v", err)
	}
	view := row.view()
	return &view, nil
}

// keyList is the collection envelope for registered SSH keys.
type keyList struct {
	// Data holds the org's keys.
	Data []keyView `json:"data"`
}

// keyRef addresses one registered key.
type keyRef struct {
	// ID is the key's identifier ("gitkey_…"), from the :id path segment.
	ID string `json:"id"`
}

// listKeys returns the SSH public keys registered to the caller's org — the keys
// that authenticate `git clone git@<host>:<org>/<repo>.git`. Keys are org-scoped
// on read even though the fingerprint index is global, so one org never sees
// another's.
//
// Example: {}
//
//	Response: {"data": [{"id": "gitkey_4a1b", "title": "laptop",
//		"publicKey": "ssh-ed25519 AAAAC3Nz…", "fingerprint": "SHA256:9pQ…",
//		"createdAt": "2026-07-01T10:00:00Z"}]}
func (o ops) listKeys(ctx context.Context, _ *cloud.Unit) (*keyList, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.keys.List(ctx, t.org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list keys: %v", err)
	}
	out := make([]keyView, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.view())
	}
	return &keyList{Data: out}, nil
}

// deleteKey removes a registered SSH key, scoped to the caller's org: an org can
// only delete its own, and a key id it does not own is not found. Answers 204
// with no body. Once removed the key no longer authenticates any SSH git access.
//
// Example: {"id": "gitkey_4a1b"}
func (o ops) deleteKey(ctx context.Context, in *keyRef) (*cloud.Unit, error) {
	t, err := tenantOf(ctx)
	if err != nil {
		return nil, err
	}
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return nil, zip.ErrBadRequest("key id required")
	}
	if err := o.s.State.keys.Delete(ctx, t.org, id); err != nil {
		if errors.Is(err, errKeyNotFound) {
			return nil, zip.ErrNotFound("key not found")
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "delete key: %v", err)
	}
	return nil, nil
}
