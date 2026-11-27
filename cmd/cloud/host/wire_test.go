package host

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"

	"github.com/hanzoai/cloud/manifest"
)

// lazyApp stands in for zip's App.Start over one lazy child: the first call
// "starts" it, which takes delay and then opens an echo server at its wire
// socket, and every call answers the zip socket the wire one sits beside.
type lazyApp struct {
	sock  string
	delay time.Duration
	fail  error

	once   sync.Once
	starts atomic.Int32
	calls  atomic.Int32
	ln     net.Listener
}

func (l *lazyApp) start(string) (string, error) {
	l.calls.Add(1)
	if l.fail != nil {
		return "", l.fail
	}
	var err error
	l.once.Do(func() {
		l.starts.Add(1)
		time.Sleep(l.delay)
		l.ln, err = net.Listen("unix", l.sock+manifest.WireSuffix)
		if err != nil {
			return
		}
		go func() {
			for {
				c, err := l.ln.Accept()
				if err != nil {
					return
				}
				go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
			}
		}()
	})
	return l.sock, err
}

// short is a directory short enough for a unix socket path on every platform.
func short(t *testing.T) string {
	t.Helper()
	d, err := os.MkdirTemp("/tmp", "w")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(d) })
	return d
}

func openDoor(t *testing.T, l *lazyApp) (*door, string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	d := newDoor("echo", ln, l.start, luxlog.New("test"))
	go d.accept()
	t.Cleanup(func() { _ = d.close(nil) })
	return d, ln.Addr().String()
}

func echo(t *testing.T, c net.Conn, msg string) {
	t.Helper()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := c.Write([]byte(msg + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != msg+"\n" {
		t.Fatalf("echo = %q, want %q", got, msg)
	}
}

// A client that connects while its app is cold is not refused: its first
// request waits in the socket while the app starts, then is answered.
func TestWireFirstConnectionWaitsForTheStart(t *testing.T) {
	l := &lazyApp{sock: filepath.Join(short(t), "a.sock"), delay: 300 * time.Millisecond}
	_, addr := openDoor(t, l)
	// The door starts the app when it accepts, which can be before Dial returns,
	// so the wait is measured from before the dial.
	t0 := time.Now()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	echo(t, c, "first")
	if waited := time.Since(t0); waited < l.delay {
		t.Fatalf("answered in %v, before the %v start finished", waited, l.delay)
	}
	echo(t, c, "second")

	// A burst of further clients shares the one start.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, err := net.Dial("tcp", addr)
			if err != nil {
				t.Error(err)
				return
			}
			defer c.Close()
			echo(t, c, "burst")
		}()
	}
	wg.Wait()
	if n := l.starts.Load(); n != 1 {
		t.Fatalf("%d starts, want 1", n)
	}
}

// A start that fails closes the connection that asked for it, and the next
// connection asks again.
func TestWireFailedStartClosesTheConnection(t *testing.T) {
	l := &lazyApp{sock: filepath.Join(short(t), "a.sock"), fail: errors.New("no bus")}
	_, addr := openDoor(t, l)
	for i := range 2 {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
			t.Fatalf("connection %d: read = %v, want EOF", i, err)
		}
		c.Close()
	}
	if n := l.calls.Load(); n != 2 {
		t.Fatalf("%d start attempts, want one per connection (2)", n)
	}
}

// An app named by CLOUD_<NAME>_ADDR runs elsewhere and serves its own port: the
// door does not relay to an address that is not this host's child.
func TestWireRefusesAnAppThatRunsElsewhere(t *testing.T) {
	l := &lazyApp{sock: "10.0.0.9:9000"}
	l.once.Do(func() {}) // nothing to open
	_, addr := openDoor(t, l)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("read = %v, want EOF", err)
	}
}

// Requests mark the app used, at most once a second, and an open connection
// keeps it marked on every tick; with no connection the tick marks nothing.
func TestWireKeepsAConnectedAppAwake(t *testing.T) {
	l := &lazyApp{sock: filepath.Join(short(t), "a.sock")}
	d, addr := openDoor(t, l)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	for range 50 {
		echo(t, c, "x")
	}
	// One start, plus at most one touch for the requests that all fit in a second.
	if n := l.calls.Load(); n > 2 {
		t.Fatalf("%d start calls for 50 requests inside a second, want at most 2", n)
	}

	before := l.calls.Load()
	go d.keep(20 * time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	if l.calls.Load() == before {
		t.Fatal("an open connection was not kept awake by the ticker")
	}

	c.Close()
	deadline := time.Now().Add(5 * time.Second)
	for {
		d.mu.Lock()
		n := len(d.conns)
		d.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d connections still tracked after the client closed", n)
		}
		time.Sleep(10 * time.Millisecond)
	}
	idle := l.calls.Load()
	time.Sleep(200 * time.Millisecond)
	if l.calls.Load() != idle {
		t.Fatal("the ticker touched an app with no connection open")
	}
}

// Closing the door stops the port and ends every relayed connection.
func TestWireCloseEndsEveryConnection(t *testing.T) {
	l := &lazyApp{sock: filepath.Join(short(t), "a.sock")}
	d, addr := openDoor(t, l)
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	echo(t, c, "open")
	if err := d.close(nil); err != nil {
		t.Fatalf("close: %v", err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("read after close = %v, want EOF", err)
	}
	if _, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		t.Fatal("the port still accepts after close")
	}
}
