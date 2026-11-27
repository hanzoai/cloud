package translate

// hangup.go — a translate call's work ends when its caller leaves. fasthttp never
// ends a request's context when the client goes, and a JSON reply writes nothing
// until it is done, so no failed write can say so either (apps/answer learns it
// from its stream's writes). The connection itself is asked instead.

import (
	"context"
	"errors"
	"syscall"
	"time"

	"github.com/zap-proto/zip"
)

// hangupEvery is how often the connection is asked whether its peer is still
// there.
const hangupEvery = 250 * time.Millisecond

// hangup returns ctx ending when the peer of the connection the request arrived on
// closes it. The socket is PEEKED, never read, so the bytes of a request that
// follows on the connection stay where the server will read them. A connection
// that is not a socket — the in-memory one a test's app.Test serves — is not
// watched. A peer that half-closes its side after sending, and still reads the
// reply, reads as gone; HTTP clients do not do that.
func hangup(ctx context.Context, c *zip.Ctx) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	sc, ok := c.Fiber().RequestCtx().Conn().(syscall.Conn)
	if !ok {
		return ctx, cancel
	}
	raw, err := sc.SyscallConn()
	if err != nil {
		return ctx, cancel
	}
	go func() {
		t := time.NewTicker(hangupEvery)
		defer t.Stop()
		peek := make([]byte, 1)
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			gone := false
			if err := raw.Read(func(fd uintptr) bool {
				n, _, err := syscall.Recvfrom(int(fd), peek, syscall.MSG_PEEK|syscall.MSG_DONTWAIT)
				gone = (err == nil && n == 0) ||
					(err != nil && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR))
				return true
			}); err != nil {
				gone = true
			}
			if gone {
				cancel()
				return
			}
		}
	}()
	return ctx, cancel
}
