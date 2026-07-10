package git

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/hanzoai/cloud"
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
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name, err := repoNameParam(c)
	if err != nil {
		return err
	}
	project := projectScope(c)

	// The org path segment must match the authenticated org. A caller may
	// only reach their own org's namespace, never another's, even if they craft
	// a different :org in the URL (Red: path-vs-identity confusion).
	if p := c.Param("org"); p != "" && p != org {
		return zip.ErrForbidden("org path does not match authenticated org")
	}

	service := c.Query("service")
	if service != svcUploadPack && service != svcReceivePack {
		return zip.ErrBadRequest("service must be git-upload-pack or git-receive-pack")
	}
	store, err := s.storeFor(org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	if _, err := store.Get(c.Context(), org, project, name); err != nil {
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
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name, err := repoNameParam(c)
	if err != nil {
		return err
	}
	project := projectScope(c)
	if p := c.Param("org"); p != "" && p != org {
		return zip.ErrForbidden("org path does not match authenticated org")
	}
	store, err := s.storeFor(org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	if _, err := store.Get(c.Context(), org, project, name); err != nil {
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
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	name, err := repoNameParam(c)
	if err != nil {
		return err
	}
	project := projectScope(c)
	if p := c.Param("org"); p != "" && p != org {
		return zip.ErrForbidden("org path does not match authenticated org")
	}
	store, err := s.storeFor(org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "open store: %v", err)
	}
	if _, err := store.Get(c.Context(), org, project, name); err != nil {
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
	// org's storage size so commerce/o11y meter the new bytes. Best-effort —
	// a metering miss must never fail the push the client already committed.
	s.recordUsage(context.WithoutCancel(c.Context()), org, project, name)

	// git-push-to-deploy: trigger a build for every branch ref this push advanced.
	// Best-effort by contract — the push already landed, so a build-trigger failure
	// is logged, never surfaced to the client (build.go OnGitPush is a no-op when
	// the platform subsystem is not co-resident).
	s.firePushBuilds(context.WithoutCancel(c.Context()), org, project, name, req)

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

// firePushBuilds triggers a platform build for every branch ref this push
// advanced (created or updated). Tags and deletes are ignored. Best-effort and
// non-fatal: the push has already landed, so a trigger failure is logged, never
// surfaced to the client. The build resolves the target app from the repo's
// canonical clone URL + branch (clients/platform/push.go).
func (s *svc) firePushBuilds(ctx context.Context, org, project, name string, req *packp.ReferenceUpdateRequest) {
	cloneURL := s.cloneURL(org, name)
	for _, cmd := range req.Commands {
		if cmd == nil || !cmd.Name.IsBranch() || cmd.New == plumbing.ZeroHash {
			continue // only branch refs that were created/updated, never deletes/tags
		}
		// Clone the request-scoped strings: org/project/name and the ref name are
		// subslices of fiber's reused request buffers (fiber returns mutable
		// values — "make copies to use outside the handler"), so a builder that
		// ENQUEUES the event would see them mutate into a later request's path —
		// e.g. a push resolving to the wrong deploy target. Commit is fresh hex.
		ev := cloud.GitPushEvent{
			Org: strings.Clone(org), Project: strings.Clone(project), Repo: strings.Clone(name),
			Branch: strings.Clone(cmd.Name.Short()), Commit: cmd.New.String(), CloneURL: cloneURL,
		}
		if err := cloud.OnGitPush(ctx, ev); err != nil {
			s.log.Warn("git push-to-deploy trigger failed", "org", org, "repo", name, "branch", ev.Branch, "err", err)
		}
	}
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
