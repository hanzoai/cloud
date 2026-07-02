package git

import (
	"bytes"
	"context"
	"fmt"
	"net/http"

	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/zap-proto/zip"
)

// Smart-HTTP git protocol (git v1, the widely-supported baseline). Three
// endpoints per repo let `git clone` / `git fetch` / `git push` operate
// natively over HTTPS against the billy-backed storer:
//
//	GET  info/refs?service=git-upload-pack|git-receive-pack  ref advertisement
//	POST git-upload-pack                                     clone/fetch
//	POST git-receive-pack                                    push
//
// go-git's server transport (plumbing/transport/server) does the heavy lifting:
// it produces the AdvRefs, encodes the packfile for fetch, and applies the
// received pack + ref updates for push. This file is only the HTTP framing
// (pktline service header, content types, body decode/encode).

const (
	svcUploadPack  = "git-upload-pack"
	svcReceivePack = "git-receive-pack"
)

// infoRefs serves GET /info/refs — the ref-advertisement phase. The service is
// selected by the ?service= query param; both upload-pack (fetch) and
// receive-pack (push) advertise here.
func (s *svc) infoRefs(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name, err := repoNameParam(c)
	if err != nil {
		return err
	}
	project := projectScope(c)

	// The org path segment must match the authenticated tenant. A caller may
	// only reach their own org's namespace, never another's, even if they craft
	// a different :org in the URL (Red: path-vs-identity confusion).
	if p := c.Param("org"); p != "" && p != org {
		return zip.ErrForbidden("org path does not match authenticated tenant")
	}

	service := c.Query("service")
	if service != svcUploadPack && service != svcReceivePack {
		return zip.ErrBadRequest("service must be git-upload-pack or git-receive-pack")
	}
	if _, err := s.store.Get(c.Context(), org, project, name); err != nil {
		return zip.ErrNotFound("repo not found")
	}

	sess, err := s.session(org, project, name, service)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "git session: %v", err)
	}
	defer func() { _ = sess.Close() }()

	ar, err := sess.AdvertisedReferencesContext(c.Context())
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "advertise refs: %v", err)
	}
	// Smart-HTTP requires a "# service=<name>\n" pktline + flush before the
	// AdvRefs body (git http-backend contract). AdvRefs.Encode writes each
	// Prefix payload as one pkt-line and APPENDS the newline itself, so the
	// payload must NOT carry a trailing "\n" — a doubled newline makes the real
	// git CLI reject the response ("invalid server response").
	ar.Prefix = [][]byte{[]byte(fmt.Sprintf("# service=%s", service)), pktline.Flush}

	var buf bytes.Buffer
	if err := ar.Encode(&buf); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "encode refs: %v", err)
	}
	c.SetHeader("Content-Type", fmt.Sprintf("application/x-%s-advertisement", service))
	c.SetHeader("Cache-Control", "no-cache")
	return c.Bytes(http.StatusOK, buf.Bytes())
}

// uploadPack serves POST /git-upload-pack — the clone/fetch phase. It decodes
// the client's wants/haves, runs the upload-pack session, and streams the
// packfile response.
func (s *svc) uploadPack(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name, err := repoNameParam(c)
	if err != nil {
		return err
	}
	project := projectScope(c)
	if p := c.Param("org"); p != "" && p != org {
		return zip.ErrForbidden("org path does not match authenticated tenant")
	}
	if _, err := s.store.Get(c.Context(), org, project, name); err != nil {
		return zip.ErrNotFound("repo not found")
	}

	body := c.Body()
	if int64(len(body)) > maxBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request body exceeds %d bytes", maxBody)
	}
	req := packp.NewUploadPackRequest()
	if err := req.Decode(bytes.NewReader(body)); err != nil {
		return zip.ErrBadRequest("decode upload-pack request: " + err.Error())
	}

	sess, err := s.uploadSession(org, project, name)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "git session: %v", err)
	}
	defer func() { _ = sess.Close() }()

	resp, err := sess.UploadPack(c.Context(), req)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "upload-pack: %v", err)
	}
	defer func() { _ = resp.Close() }()

	var buf bytes.Buffer
	if err := resp.Encode(&buf); err != nil {
		return zip.Errorf(http.StatusInternalServerError, "encode upload-pack: %v", err)
	}
	c.SetHeader("Content-Type", "application/x-git-upload-pack-result")
	c.SetHeader("Cache-Control", "no-cache")
	return c.Bytes(http.StatusOK, buf.Bytes())
}

// receivePack serves POST /git-receive-pack — the push phase. It decodes the
// ref-update commands + packfile, applies them to the storer, records the new
// storage size (billing hook), and returns the report-status.
func (s *svc) receivePack(c *zip.Ctx) error {
	org, ok := tenant(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name, err := repoNameParam(c)
	if err != nil {
		return err
	}
	project := projectScope(c)
	if p := c.Param("org"); p != "" && p != org {
		return zip.ErrForbidden("org path does not match authenticated tenant")
	}
	if _, err := s.store.Get(c.Context(), org, project, name); err != nil {
		return zip.ErrNotFound("repo not found")
	}

	body := c.Body()
	if int64(len(body)) > maxBody {
		return zip.Errorf(http.StatusRequestEntityTooLarge, "request body exceeds %d bytes", maxBody)
	}
	req := packp.NewReferenceUpdateRequest()
	if err := req.Decode(bytes.NewReader(body)); err != nil {
		return zip.ErrBadRequest("decode receive-pack request: " + err.Error())
	}

	sess, err := s.receiveSession(org, project, name)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "git session: %v", err)
	}
	defer func() { _ = sess.Close() }()

	report, err := sess.ReceivePack(c.Context(), req)
	if report == nil && err != nil {
		return zip.Errorf(http.StatusInternalServerError, "receive-pack: %v", err)
	}

	// Push landed (or partially landed with a report): re-measure and record the
	// tenant's storage size so commerce/o11y meter the new bytes. Best-effort —
	// a metering miss must never fail the push the client already committed.
	s.recordUsage(context.WithoutCancel(c.Context()), org, project, name)

	var buf bytes.Buffer
	if report != nil {
		if encErr := report.Encode(&buf); encErr != nil {
			return zip.Errorf(http.StatusInternalServerError, "encode report: %v", encErr)
		}
	}
	c.SetHeader("Content-Type", "application/x-git-receive-pack-result")
	c.SetHeader("Cache-Control", "no-cache")
	return c.Bytes(http.StatusOK, buf.Bytes())
}

// ---- session helpers ----

// session returns the advertised-references session for the requested service.
func (s *svc) session(org, project, name, service string) (transport.Session, error) {
	if service == svcReceivePack {
		return s.receiveSession(org, project, name)
	}
	return s.uploadSession(org, project, name)
}

func (s *svc) uploadSession(org, project, name string) (transport.UploadPackSession, error) {
	ep, err := transport.NewEndpoint("/" + name)
	if err != nil {
		return nil, err
	}
	return s.storage.transport(org, project, name).NewUploadPackSession(ep, nil)
}

func (s *svc) receiveSession(org, project, name string) (transport.ReceivePackSession, error) {
	ep, err := transport.NewEndpoint("/" + name)
	if err != nil {
		return nil, err
	}
	return s.storage.transport(org, project, name).NewReceivePackSession(ep, nil)
}
