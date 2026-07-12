package git

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/zap-proto/zip"
	"golang.org/x/crypto/ssh"
)

// keys.go is the control-plane surface for the SSH public-key registry:
//
//	POST   /v1/git/keys        register a key (title + openssh pubkey) -> keyView (201)
//	GET    /v1/git/keys        list the tenant's keys                 -> {data:[keyView]}
//	DELETE /v1/git/keys/:id    remove a key                           -> 204
//
// These are thin adapters over keystore.go, tenant-scoped identically to the
// repo routes (principal.Tenant → X-Org-Id). A key is stored with its SHA256
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
func (s *svc) registerKey(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
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
	if err := s.keys.Add(c.Context(), row); err != nil {
		if errors.Is(err, errKeyConflict) {
			return zip.ErrConflict("this ssh key is already registered")
		}
		return zip.Errorf(http.StatusInternalServerError, "register key: %v", err)
	}
	return c.JSON(http.StatusCreated, row.view())
}

// listKeys returns the caller org's registered keys.
func (s *svc) listKeys(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.keys.List(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list keys: %v", err)
	}
	out := make([]keyView, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.view())
	}
	return c.JSON(http.StatusOK, map[string]any{"data": out})
}

// deleteKey removes a key by id, scoped to the caller's org (a tenant can only
// delete its own keys).
func (s *svc) deleteKey(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return zip.ErrBadRequest("key id required")
	}
	if err := s.keys.Delete(c.Context(), org, id); err != nil {
		if errors.Is(err, errKeyNotFound) {
			return zip.ErrNotFound("key not found")
		}
		return zip.Errorf(http.StatusInternalServerError, "delete key: %v", err)
	}
	return c.NoContent(http.StatusNoContent)
}
