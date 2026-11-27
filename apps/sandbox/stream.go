package sandbox

// stream.go — a ZAP stream from the plane into a sandbox's unix socket.
//
//	plane  sandbox_stream {id, path}  ->  {socket, expiresIn}
//
// A typed op is one request and one reply, so the op authorises and hands back a
// unix socket; the stream is that socket. It is bound beside the plane's own
// sockets, named by 96 random bits, accepts exactly one connection and is
// removed when that connection ends or the wait runs out. Whoever can reach the
// plane directory is already a process of this pod, and the name is the
// capability for the one sandbox and socket the op was asked about.
//
// Bytes cross untouched. ZAP frames its messages itself, so a relay that does not
// look inside is a relay that cannot translate: an envelope is the same envelope
// at both ends. The far side is the same exec channel a preview uses (join), so
// nothing in the cell listens on the pod network.

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
)

// streamWait is how long a stream's socket waits for its one connection.
const streamWait = 30 * time.Second

// planeStream authorises the caller for the sandbox and opens its stream socket.
func planeStream(ctx context.Context, in *client.StreamIn) (*client.Streamed, error) {
	s, org, err := live(ctx)
	if err != nil {
		return nil, err
	}
	if !filepath.IsAbs(in.Path) || strings.ContainsAny(in.Path, ",!\n") {
		return nil, zip.ErrBadRequest("path must be the absolute path of a unix socket")
	}
	m, store, err := find(s, ctx, org, in.ID, "stream")
	if err != nil {
		return nil, err
	}
	if m.Status != statusRunning {
		return nil, zip.Errorf(http.StatusConflict, "sandbox is %s", m.Status)
	}
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "stream: %v", err)
	}
	path := filepath.Join(filepath.Dir(zip.SocketPath("sandbox")), "s-"+hex.EncodeToString(id[:])+".sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "stream: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = ln.Close()
		return nil, zip.Errorf(http.StatusInternalServerError, "stream: %v", err)
	}
	go carry(s, ln, m, store, "UNIX-CONNECT:"+filepath.Clean(in.Path))
	return &client.Streamed{Socket: path, ExpiresIn: int(streamWait / time.Second)}, nil
}

// carry accepts the one connection and joins it to the port until either ends.
func carry(s *Service, ln net.Listener, m Sandbox, store *Store, to string) {
	if ul, ok := ln.(*net.UnixListener); ok {
		_ = ul.SetDeadline(time.Now().Add(streamWait))
	}
	near, err := ln.Accept()
	// One connection: closing the listener now unlinks the socket, so there is
	// nothing left to connect to.
	_ = ln.Close()
	if err != nil {
		return
	}
	defer func() { _ = near.Close() }()
	far, err := s.State.rt.join(m, to, leaseEnd(m, s.State.clk.absolute), func() {})
	if err != nil {
		return
	}
	defer func() { _ = far.Close() }()
	done := make(chan struct{}, 2)
	pipe := func(dst io.Writer, src io.Reader) {
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				s.State.web.attend(store, m)
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}
	go pipe(far, near)
	go pipe(near, far)
	<-done
}
