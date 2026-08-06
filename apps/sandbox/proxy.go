// The fs/proc/git proxy: how a caller reaches inside a box.
//
// Boxes have no public address, deliberately. So every filesystem call from
// hanzo.app goes IAM edge → cloud → box, and cloud rewrites only the path
// prefix — /v1/sandbox/boxes/:id/fs/read becomes /v1/box/fs/read, the box's own
// address for the same thing.
//
// The alternative — handing the caller a box address and a token — means
// terminating auth somewhere other than the IAM edge, which is inventing a
// second auth path. That is banned, so we pay one in-cluster hop instead.
// apps/exec makes the same trade for the same reason.
//
// The credential SWAP is the other half: the user's identity is verified at the
// gateway and stops there. What goes to the box is the shared service key. A
// box therefore never sees a user token, which is the correct property for a
// process whose whole job is running that user's code.
package sandbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/sandbox/wire"
	"github.com/zap-proto/zip"
)

// boxClient is the one HTTP client for box calls. The response-header timeout
// bounds a wedged box without bounding the BODY, because a legitimate `pnpm
// install` streams for minutes after its first byte.
var boxClient = &http.Client{
	Transport: &http.Transport{ResponseHeaderTimeout: 180 * time.Second},
}

// forward proxies one call into the box. It rewrites the path and the
// credential and touches nothing else — the status, the Content-Type and the
// body are the box's, including its own 4xx.
func forward(s *cloud.Service[state], c *zip.Ctx) error {
	bx, store, err := load(s, c)
	if err != nil {
		return err
	}
	if bx.Status != "running" || bx.Host == "" {
		// A suspended box is not an error the caller should have to guess at:
		// say which state it is in, so a client can decide to resume it.
		return zip.Errorf(http.StatusConflict, "box is %s; resume it first", bx.Status)
	}
	if s.State.key == "" {
		return zip.Errorf(http.StatusServiceUnavailable, "sandbox not configured: CODE_EXEC_API_KEY unset")
	}

	sub, ok := boxPath(c.Path(), bx.ID)
	if !ok {
		return zip.ErrBadRequest("not a box subpath")
	}

	// Forward the parameters the box surface declares, and only those. Copying a
	// raw query string would make this an opaque tunnel that cannot tell a real
	// parameter from a smuggled one; wire.QueryParams is the list both ends read.
	q := url.Values{}
	for _, name := range wire.QueryParams {
		if v := c.Query(name); v != "" {
			q.Set(name, v)
		}
	}
	target := bx.Host + sub
	if enc := q.Encode(); enc != "" {
		target += "?" + enc
	}

	var body io.Reader
	if b := c.Body(); len(b) > 0 {
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(c.Context(), c.Method(), target, body)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "build request: %v", err)
	}
	if ct := c.Header("Content-Type"); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	// The service key, never the caller's. A box runs the caller's code; handing
	// it the caller's IAM token would let that code act as the user everywhere.
	req.Header.Set(wire.KeyHeader, s.State.key)
	// And WHICH box we mean. The key is shared pool-wide, so it proves the caller
	// is cloud and nothing about the destination — and bx.Host can be stale in
	// the one way that matters, pointing at a recycled address now serving
	// another tenant. The box compares this against its own id and refuses when
	// it does not match, which is the only check that cannot be fooled by the
	// row being wrong.
	req.Header.Set(wire.BoxHeader, bx.ID)

	resp, err := boxClient.Do(req)
	if err != nil {
		return zip.Errorf(http.StatusBadGateway, "box unreachable: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Touching the box is what keeps it alive — the idle reaper reads this, so
	// an actively-edited project is never suspended out from under its user.
	bx.LastUsedAt = time.Now().Unix()
	_ = store.Put(c.Context(), bx)

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		c.SetHeader("Content-Type", ct)
	}
	// SendStream, not a buffered read: a `pnpm install` streams for minutes and
	// the caller should see it arrive rather than wait for the last byte.
	return c.Status(resp.StatusCode).SendStream(resp.Body)
}

// boxPath maps this surface's path onto the box's own.
//
//	/v1/sandbox/boxes/<id>/fs/read  ->  /v1/box/fs/read
//
// It is a prefix rewrite and nothing more: what lives below /fs, /proc and /git
// is the box's to define, and enumerating it here would 404 whatever this file
// had not heard of.
func boxPath(path, id string) (string, bool) {
	marker := "/v1/sandbox/boxes/" + id + "/"
	i := strings.Index(path, marker)
	if i < 0 {
		return "", false
	}
	rest := path[i+len(marker):]
	switch {
	case rest == "fs", strings.HasPrefix(rest, "fs/"),
		strings.HasPrefix(rest, "proc/"), strings.HasPrefix(rest, "git/"):
		return "/v1/box/" + rest, true
	default:
		return "", false
	}
}

// bind names a freshly claimed box to itself.
//
// It is the ONE call that carries no X-Box-Id, because it is the call that
// establishes one — see boxd's guard, which exempts this path for exactly that
// reason. Everything after it is checked.
func (st *state) bind(ctx context.Context, host, id string) error {
	body, err := json.Marshal(wire.Bind{ID: id})
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+wire.PathBind, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(wire.KeyHeader, st.key)
	resp, err := boxClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<10))
		return fmt.Errorf("box refused the bind: %d %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}
