package provisioning

// Boot smoke-test for the dedicated Hanzo KV engine (Red low-2). The one
// fail-OPEN corner of the kv add-on is auth: the kv-server binary reads NO
// password from env, so the per-instance requirepass is enforced ONLY if the
// image actually loads the mounted config file passed as its first positional
// arg. TestDedicated_KVEngineAssemblesDSN unit-proves the WIRING (args[0] is the
// config path; the mounted Secret carries `requirepass <pw>`). This test proves
// the IMAGE HONORS that wiring end-to-end: it boots ghcr.io/hanzoai/kv with the
// exact engine.args + engine.secretEnv config and asserts an UNAUTHENTICATED
// PING is REJECTED (requirepass in force), then that the DSN's `default:<pw>`
// credential authenticates.
//
// Skips unless CLOUD_KV_SMOKE_IMAGE names the image (e.g. ghcr.io/hanzoai/kv:9)
// AND docker is available, so the default `go test ./clients/provisioning/...`
// stays green with no container runtime; CI (or a release gate) sets the image
// and this boots the real thing.

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestDedicatedKV_RequirepassEnforced(t *testing.T) {
	image := os.Getenv("CLOUD_KV_SMOKE_IMAGE")
	if image == "" {
		t.Skip("CLOUD_KV_SMOKE_IMAGE unset — set it (e.g. ghcr.io/hanzoai/kv:9) to boot the real kv image and prove requirepass is enforced")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not on PATH — the kv boot smoke-test needs a container runtime")
	}

	e := dedicatedEngines["kv"]
	const pw = "sm0ke-Test_Pass-9x" // genToken alphabet ([A-Za-z0-9_-]); no quoting/injection
	conf := e.secretEnv("", pw, "")["kv.conf"]
	if !strings.Contains(conf, "requirepass "+pw) {
		t.Fatalf("engine kv.conf did not carry requirepass: %q", conf)
	}

	// Render the mounted requirepass config exactly as the admin Secret projects it.
	dir := t.TempDir()
	confPath := dir + "/kv.conf"
	if err := os.WriteFile(confPath, []byte(conf), 0o600); err != nil {
		t.Fatalf("write kv.conf: %v", err)
	}

	port := freePort(t)

	// Pull explicitly so an image/registry failure is a clear signal, not a
	// timeout inside `docker run`.
	pullCtx, cancelPull := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancelPull()
	if out, err := exec.CommandContext(pullCtx, "docker", "pull", image).CombinedOutput(); err != nil {
		t.Skipf("cannot pull %s (%v): %s", image, err, out)
	}

	// Boot the image with the EXACT engine args, the config mounted where args[0]
	// expects it (secretMount/kv.conf). Mirrors the operator's spec.args + secret
	// volumeMount verbatim.
	runArgs := []string{
		"run", "-d", "--rm",
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", port, e.clientPort),
		"-v", confPath + ":" + e.secretMount + "/kv.conf:ro",
		image,
	}
	runArgs = append(runArgs, e.args...)
	out, err := exec.Command("docker", runArgs...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker run: %v: %s", err, out)
	}
	cid := strings.TrimSpace(string(out))
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", cid).Run() })

	rc := dialRESP(t, port)
	defer rc.close()

	// 1. FAIL-OPEN GUARD: an unauthenticated PING must be REJECTED. If the image
	// ignored the positional config, requirepass would be unset and this returns
	// +PONG — an unauthenticated instance, the exact corner we lock down.
	reply := rc.cmd(t, "PING")
	if !strings.HasPrefix(reply, "-NOAUTH") && !strings.Contains(strings.ToUpper(reply), "AUTH") {
		t.Fatalf("unauthenticated PING was not rejected (requirepass NOT enforced): %q", reply)
	}

	// 2. The DSN credential (user "default", the mounted requirepass) authenticates.
	if reply := rc.cmd(t, "AUTH", "default", pw); !strings.HasPrefix(reply, "+OK") {
		t.Fatalf("AUTH default <pw> failed: %q", reply)
	}
	if reply := rc.cmd(t, "PING"); !strings.HasPrefix(reply, "+PONG") {
		t.Fatalf("authenticated PING = %q, want +PONG", reply)
	}
}

// freePort reserves an ephemeral port and releases it for docker to bind.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p
}

// respConn is a minimal synchronous RESP connection: ONE reader per socket, so a
// read that buffers past a line boundary is never lost across commands.
type respConn struct {
	c net.Conn
	r *bufio.Reader
}

func (rc *respConn) close() { _ = rc.c.Close() }

// cmd sends one inline RESP command and returns the first reply line (trimmed).
// Sufficient for PING/AUTH whose replies are single-line simple strings/errors.
func (rc *respConn) cmd(t *testing.T, args ...string) string {
	t.Helper()
	_ = rc.c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := fmt.Fprintf(rc.c, "%s\r\n", strings.Join(args, " ")); err != nil {
		t.Fatalf("write %v: %v", args, err)
	}
	line, err := rc.r.ReadString('\n')
	if err != nil {
		t.Fatalf("read reply to %v: %v", args, err)
	}
	return strings.TrimRight(line, "\r\n")
}

// dialRESP waits for the kv port to accept connections (image boot).
func dialRESP(t *testing.T, port int) *respConn {
	t.Helper()
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(30 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, time.Second)
		if err == nil {
			return &respConn{c: conn, r: bufio.NewReader(conn)}
		}
		if time.Now().After(deadline) {
			t.Fatalf("kv never accepted a connection on %s: %v", addr, err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
