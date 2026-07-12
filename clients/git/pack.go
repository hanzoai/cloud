package git

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/hanzoai/cloud"
)

// errBadPack marks a client-side protocol error (a malformed upload-pack or
// receive-pack request body) so a transport adapter can answer 400 rather than
// 500. Wrapped by serveUploadPack / serveReceivePack around the decode step.
var errBadPack = errors.New("git: malformed pack request")

// readerOnly hides an io.Reader's Closer (if any) from go-git's pack decoders.
// The SSH channel is an io.ReadWriteCloser; go-git captures the decode reader as
// the packfile reader and Closes it after unpacking — which on a raw channel
// would close the WRITE side too. Wrapping the read side in a plain io.Reader
// means that Close becomes a no-op (io.NopCloser), leaving the channel writable
// for the response/report.
type readerOnly struct{ r io.Reader }

func (ro readerOnly) Read(p []byte) (int, error) { return ro.r.Read(p) }

// pack.go is the ONE Git pack code path. Both transports — smart-HTTP
// (clients/git/smart_http.go) and SSH (clients/git/ssh.go) — drive clone/fetch
// and push through the SAME funcs here, so there is exactly one place that
// decodes a request, runs the go-git server session, records usage, and fires
// the push-to-deploy hook. The transports differ only in framing: HTTP carries
// the request/response as a body/response byte stream; SSH carries it as a
// channel's stdin/stdout. Both are io.Reader (in) + io.Writer (out), so the
// pack logic never knows which transport called it (DRY — Rich Hickey's
// "one fact, one place").
//
// The org/project/name are already resolved + org-checked by the caller
// (the HTTP handler from X-Org-Id, the SSH session from the presented key's
// bound org), so these funcs are transport-agnostic and take plain strings —
// never a *zip.Ctx or an *ssh.Session.

// serveUploadPack runs the upload-pack (clone/fetch) exchange for one repo,
// reading the client's wants/haves from in and writing the packfile result to
// out. The caller has verified the repo exists and the org owns it.
func serveUploadPack(s *cloud.Service[state], ctx context.Context, org, project, name string, in io.Reader, out io.Writer) error {
	req := packp.NewUploadPackRequest()
	if err := req.Decode(in); err != nil {
		return fmt.Errorf("%w: decode upload-pack request: %v", errBadPack, err)
	}
	sess, err := uploadSession(s, org, project, name)
	if err != nil {
		return fmt.Errorf("git session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	resp, err := sess.UploadPack(ctx, req)
	if err != nil {
		return fmt.Errorf("upload-pack: %w", err)
	}
	defer func() { _ = resp.Close() }()

	if err := resp.Encode(out); err != nil {
		return fmt.Errorf("encode upload-pack: %w", err)
	}
	return nil
}

// serveReceivePack runs the receive-pack (push) exchange for one repo, reading
// the ref-update commands + packfile from in and writing the report-status to
// out. On success it re-measures storage (billing) and fires the push-to-deploy
// build hook — the SAME side effects a smart-HTTP push produces — so a push over
// SSH and a push over HTTPS are indistinguishable downstream.
//
// The caller has verified the repo exists and the org owns it. usage +
// build-trigger are best-effort by contract: the push has already landed, so a
// metering miss or trigger failure is never surfaced to the client.
func serveReceivePack(s *cloud.Service[state], ctx context.Context, org, project, name string, in io.Reader, out io.Writer) error {
	req := packp.NewReferenceUpdateRequest()
	if err := req.Decode(in); err != nil {
		return fmt.Errorf("%w: decode receive-pack request: %v", errBadPack, err)
	}
	sess, err := receiveSession(s, org, project, name)
	if err != nil {
		return fmt.Errorf("git session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	report, err := sess.ReceivePack(ctx, req)
	if report == nil && err != nil {
		return fmt.Errorf("receive-pack: %w", err)
	}

	// Re-measure + fire builds on a cancel-immune context: the client's transport
	// may close the instant the report is flushed, but the push already committed,
	// so the metering + build-trigger must still run to completion.
	bg := context.WithoutCancel(ctx)
	recordUsage(s, bg, org, project, name)
	firePushBuilds(s, bg, org, project, name, req)

	if report != nil {
		if err := report.Encode(out); err != nil {
			return fmt.Errorf("encode report: %w", err)
		}
	}
	return nil
}

// sshUploadPack drives the FULL native-git-protocol upload-pack (clone/fetch)
// exchange over an SSH channel: it advertises refs first (native protocol has no
// separate info/refs request the way smart-HTTP does), THEN reads the client's
// wants/haves and streams the packfile — all on ONE go-git session, over the
// channel's stdin (in) / stdout (out). This is the SSH framing wrapper around the
// SAME go-git upload-pack session the smart-HTTP path uses.
func sshUploadPack(s *cloud.Service[state], ctx context.Context, org, project, name string, in io.Reader, out io.Writer) error {
	sess, err := uploadSession(s, org, project, name)
	if err != nil {
		return fmt.Errorf("git session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	// 1) Advertise refs (native protocol — no "# service=" prefix). An empty repo
	// still advertises capabilities + a flush so the client can proceed / stop.
	ar, err := sess.AdvertisedReferencesContext(ctx)
	if err != nil {
		if errors.Is(err, transport.ErrEmptyRemoteRepository) {
			// Empty repo: send a bare capabilities advertisement + flush so the
			// client cleanly observes "nothing to fetch" instead of hanging.
			ar = packp.NewAdvRefs()
		} else {
			return fmt.Errorf("advertise refs: %w", err)
		}
	}
	if err := ar.Encode(out); err != nil {
		return fmt.Errorf("encode refs: %w", err)
	}

	// 2) Read the client's wants/haves. An empty request (client had everything /
	// nothing to want) is a clean end, not an error. Read-only view so a decoder
	// Close cannot tear down the channel's write side (see readerOnly).
	req := packp.NewUploadPackRequest()
	if err := req.Decode(readerOnly{in}); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return fmt.Errorf("%w: decode upload-pack request: %v", errBadPack, err)
	}
	if req.IsEmpty() {
		return nil
	}

	resp, err := sess.UploadPack(ctx, req)
	if err != nil {
		if errors.Is(err, transport.ErrEmptyUploadPackRequest) {
			return nil
		}
		return fmt.Errorf("upload-pack: %w", err)
	}
	defer func() { _ = resp.Close() }()
	if err := resp.Encode(out); err != nil {
		return fmt.Errorf("encode upload-pack: %w", err)
	}
	return nil
}

// sshReceivePack drives the FULL native-git-protocol receive-pack (push) exchange
// over an SSH channel: advertise refs first, THEN read the ref-update commands +
// packfile and apply them, then write the report-status — on ONE session, over
// the channel's stdin/stdout. It records usage + fires the build hook on success,
// the SAME side effects the smart-HTTP push produces.
func sshReceivePack(s *cloud.Service[state], ctx context.Context, org, project, name string, in io.Reader, out io.Writer) error {
	sess, err := receiveSession(s, org, project, name)
	if err != nil {
		return fmt.Errorf("git session: %w", err)
	}
	defer func() { _ = sess.Close() }()

	ar, err := sess.AdvertisedReferencesContext(ctx)
	if err != nil {
		return fmt.Errorf("advertise refs: %w", err)
	}
	if err := ar.Encode(out); err != nil {
		return fmt.Errorf("encode refs: %w", err)
	}

	// Decode from a READ-ONLY view of the channel. go-git's ReferenceUpdateRequest
	// captures the reader AS the packfile reader and calls Close() on it after
	// unpacking; if that reader were the ssh.Channel itself (an io.ReadWriteCloser),
	// Close would tear down the WHOLE channel — including the write side we still
	// need to send the report-status on. readerOnly hides the Closer so the pack
	// unpack cannot close our write side.
	req := packp.NewReferenceUpdateRequest()
	if err := req.Decode(readerOnly{in}); err != nil {
		if errors.Is(err, io.EOF) {
			return nil // client disconnected without pushing — clean end
		}
		return fmt.Errorf("%w: decode receive-pack request: %v", errBadPack, err)
	}

	report, err := sess.ReceivePack(ctx, req)
	if report == nil && err != nil {
		return fmt.Errorf("receive-pack: %w", err)
	}

	bg := context.WithoutCancel(ctx)
	recordUsage(s, bg, org, project, name)
	firePushBuilds(s, bg, org, project, name, req)

	if report != nil {
		if err := report.Encode(out); err != nil {
			return fmt.Errorf("encode report: %w", err)
		}
	}
	return nil
}
