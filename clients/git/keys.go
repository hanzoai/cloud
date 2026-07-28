package git

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
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

type registerKeyReq struct {
	Title     string `json:"title"`
	PublicKey string `json:"publicKey"`
}

// registerKey validates an OpenSSH public key, computes its fingerprint, and
// stores it under the caller's org + user. The full key round-trips (it is
// public); the fingerprint is the auth lookup key. A key already registered
// (to this or any org — fingerprint is globally unique) yields 409.
//
// Raw, not a typed op: it answers 201, and zip's typed registrar writes 200 for
// a value and 204 for none with no seam to set another status.
func registerKey(s *cloud.Service[state], c *zip.Ctx) error {
	t, terr := tenantFrom(c)
	if terr != nil {
		return terr
	}
	org := t.org
	var body registerKeyReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	raw := strings.TrimSpace(body.PublicKey)
	if raw == "" {
		return zip.ErrBadRequest("publicKey is required")
	}
	title := strings.TrimSpace(body.Title)
	if len(title) > 256 {
		return zip.ErrBadRequest("title too long (max 256)")
	}
	// Parse the authorized-key line to validate it and canonicalize the stored
	// form + fingerprint. A malformed key is a 400, never stored.
	pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(raw))
	if err != nil {
		return zip.ErrBadRequest("invalid openssh public key")
	}
	if title == "" {
		title = strings.TrimSpace(comment) // fall back to the key comment as the label
	}
	// Canonical authorized-key line (type + base64), no trailing newline.
	canonical := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	fp := ssh.FingerprintSHA256(pub)

	id, err := genID("gitkey")
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	row := sshKey{
		ID: id, Org: org, UserID: strings.TrimSpace(c.User()), Title: title,
		PublicKey: canonical, Fingerprint: fp, CreatedAt: time.Now().Unix(),
	}
	if err := s.State.keys.Add(c.Context(), row); err != nil {
		if errors.Is(err, errKeyConflict) {
			return zip.ErrConflict("this ssh key is already registered")
		}
		return zip.Errorf(http.StatusInternalServerError, "register key: %v", err)
	}
	return c.JSON(http.StatusCreated, row.view())
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
func (o ops) listKeys(ctx context.Context, _ *noInput) (*keyList, error) {
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
func (o ops) deleteKey(ctx context.Context, in *keyRef) (*noContent, error) {
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
