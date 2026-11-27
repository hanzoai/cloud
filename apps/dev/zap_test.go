package dev

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/account"
	dev "github.com/hanzoai/dev/go"
	"zap-proto.io/go/rpc"
	"zap-proto.io/go/transport/quic"

	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/meter"
)

type accepts map[string]cloud.VerifiedIdentity

func (a accepts) Validate(raw string) (cloud.VerifiedIdentity, error) {
	if who, ok := a[raw]; ok {
		return who, nil
	}
	return cloud.VerifiedIdentity{}, errors.New("untrusted")
}

// cell is a Dev server's ZAP socket: it keeps what it is sent, whole.
func cell(t *testing.T) (path string, got func() [][]byte) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path = filepath.Join(dir, "zap.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var mu sync.Mutex
	var in [][]byte
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		for {
			env, err := quic.ReadEnvelope(c)
			if err != nil {
				return
			}
			mu.Lock()
			in = append(in, env)
			mu.Unlock()
		}
	}()
	return path, func() [][]byte { mu.Lock(); defer mu.Unlock(); return append([][]byte(nil), in...) }
}

func TestTheFirstEnvelopeMustProveWhoAsks(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	asked := false
	p.Stream = func(_, _, _, _ string) string { asked = true; return "" }
	b := &bridge{id: "m_x", sess: &quic.Session{}, valid: accepts{"good": {Owner: "hanzo", Subject: "sub-1"}}}

	for name, cap := range map[string][]byte{"no bearer": nil, "a bearer IAM refuses": []byte("forged")} {
		resp, err := b.dispatch(rpc.BuildRequest(rpc.Call{Method: 0, PromiseID: 7, Cap: cap, Payload: []byte("frame")}))
		if err != nil || resp == nil {
			t.Fatalf("%s: %v %v; want a refusal envelope", name, resp, err)
		}
		if r, _ := rpc.ParseResponse(resp); r.Status != rpc.StatusUnauthorized || r.PromiseID != 7 {
			t.Errorf("%s answered %+v, want 401 for promise 7", name, r)
		}
	}
	if asked {
		t.Error("a cell was reached by a caller IAM had not vouched for")
	}
}

// What goes in at the browser's end comes out of the cell's, byte for byte, the
// bearer included, and the stream is opened as the person the bearer names.
func TestAVouchedSessionReachesTheCellUnchanged(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	sock, got := cell(t)
	var org, user, id, path string
	p.Stream = func(o, u, i, pa string) string { org, user, id, path = o, u, i, pa; return sock }
	b := &bridge{id: "m_x", sess: &quic.Session{}, valid: accepts{"good": {Owner: "hanzo", Subject: "sub-1"}}}

	first := rpc.BuildRequest(rpc.Call{Method: 0, PromiseID: 1, Cap: []byte("good"), Payload: []byte(`{"a":1}`)})
	second := rpc.BuildRequest(rpc.Call{Method: 0, PromiseID: 2, Payload: []byte(`{"b":2}`)})
	for _, env := range [][]byte{first, second} {
		if resp, err := b.dispatch(env); err != nil || resp != nil {
			t.Fatalf("dispatch = %v, %v; the cell's replies come back by themselves, so there is none here", resp, err)
		}
	}
	if org != "hanzo" || user != "sub-1" || id != "m_x" || path != Socket {
		t.Errorf("stream opened for %q/%q on %q at %q; want hanzo/sub-1 on m_x at %s", org, user, id, path, Socket)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(got()) < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	in := got()
	if len(in) != 2 || string(in[0]) != string(first) || string(in[1]) != string(second) {
		t.Fatalf("the cell received %d envelopes; want the two sent, unchanged", len(in))
	}
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// The whole path on the browser's wire: a WebTransport session, the bearer in the
// first envelope, the cell's reply and an event it pushes unasked, and a refusal.
func TestABrowserSessionRunsThroughToTheCell(t *testing.T) {
	p := planetest.ServeSandboxes(t)
	dir, err := os.MkdirTemp("/tmp", "dz")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "zap.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	// The cell: answers promise N with "ack N", then pushes an event under promise 99.
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		for {
			env, err := quic.ReadEnvelope(c)
			if err != nil {
				return
			}
			call, err := rpc.ParseRequest(env)
			if err != nil {
				return
			}
			_ = quic.WriteEnvelope(c, rpc.BuildResponse(rpc.StatusOK, call.PromiseID, append([]byte("ack "), call.Payload...)))
			_ = quic.WriteEnvelope(c, rpc.BuildResponse(rpc.StatusOK, 99, []byte("event")))
		}
	}()
	p.Stream = func(_, _, _, _ string) string { return sock }

	srv, err := serve("127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSigned(t)}},
		accepts{"good": {Owner: "hanzo", Subject: "sub-1"}})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer srv.Close()
	url := "https://" + srv.Addr().String() + Path + "m_x"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	dial := func() (io.ReadWriter, func() error) {
		st, done, err := quic.DialWebTransport(ctx, url, &tls.Config{InsecureSkipVerify: true}, nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		return st, done
	}

	st, done := dial()
	defer func() { _ = done() }()
	if err := quic.WriteEnvelope(st, rpc.BuildRequest(rpc.Call{Method: 0, PromiseID: 1, Cap: []byte("good"), Payload: []byte("hello")})); err != nil {
		t.Fatal(err)
	}
	var bodies []string
	for i := 0; i < 2; i++ {
		env, err := quic.ReadEnvelope(st)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		r, err := rpc.ParseResponse(env)
		if err != nil || r.Status != rpc.StatusOK {
			t.Fatalf("reply %d = %+v, %v", i, r, err)
		}
		bodies = append(bodies, fmt.Sprintf("%d:%s", r.PromiseID, r.Body))
	}
	if !slices.Contains(bodies, "1:ack hello") || !slices.Contains(bodies, "99:event") {
		t.Errorf("the browser saw %v; want the cell's reply to promise 1 and its unasked event, and nothing else", bodies)
	}

	bad, doneBad := dial()
	defer func() { _ = doneBad() }()
	if err := quic.WriteEnvelope(bad, rpc.BuildRequest(rpc.Call{Method: 0, PromiseID: 5, Cap: []byte("forged")})); err != nil {
		t.Fatal(err)
	}
	env, err := quic.ReadEnvelope(bad)
	if err != nil {
		t.Fatal(err)
	}
	if r, _ := rpc.ParseResponse(env); r.Status != rpc.StatusUnauthorized {
		t.Errorf("a forged bearer was answered %d, want 401", r.Status)
	}
}

// A page on another origin opens a session — every page is on another origin, since
// nothing is served from zap.<brand> — and the bearer in its first envelope is still
// what decides who it is. The WebTransport library's default refused any Origin but its
// own host, so Chromium's handshake failed with a 400 before the session existed.
func TestABrowserOnAnotherOriginOpensASession(t *testing.T) {
	planetest.ServeSandboxes(t)
	srv, err := serve("127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSigned(t)}}, accepts{})
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, origin := range []string{"https://hanzo.app", "https://evil.example"} {
		st, done, err := quic.DialWebTransport(ctx, "https://"+srv.Addr().String()+Path+"m_x",
			&tls.Config{InsecureSkipVerify: true}, http.Header{"Origin": []string{origin}})
		if err != nil {
			t.Fatalf("a page on %s could not open a session: %v", origin, err)
		}
		if err := quic.WriteEnvelope(st, rpc.BuildRequest(rpc.Call{Method: 0, PromiseID: 3, Payload: []byte("x")})); err != nil {
			t.Fatal(err)
		}
		env, err := quic.ReadEnvelope(st)
		if err != nil {
			t.Fatal(err)
		}
		if r, _ := rpc.ParseResponse(env); r.Status != rpc.StatusUnauthorized {
			t.Errorf("a session from %s with no bearer was answered %d, want 401", origin, r.Status)
		}
		_ = done()
	}
}

// A verified bearer is stated on the plane with who pays for it: the billing account
// its token names, which is the wallet the same token pays from over HTTP.
func TestAVerifiedBearerIsStatedWithWhoPays(t *testing.T) {
	who := cloud.VerifiedIdentity{Subject: "sub-1", Username: "alice", Owner: "hanzo", BillingAccount: "org:hanzo"}
	c := caller(who)
	if c.User != "sub-1" || c.Name != "alice" || c.Owner != who.Home() || c.Account != "org:hanzo" {
		t.Fatalf("stated as %+v", c)
	}
	if got := meter.PayerFrom(actingAs("hanzo", c)); got != account.Org("hanzo") {
		t.Errorf("paid from %v, want the account the token names", got)
	}
	// The wasm loop's model call is paid for the same way, not left with no payer.
	m := &model{reply: []*infer.ChatResponse{{Content: "hi"}}}
	h := &host{ai: m, org: "hanzo", owner: "sub-1", who: c, model: "enso"}
	if _, err := h.Ask(context.Background(), dev.Ask{}); err != nil {
		t.Fatal(err)
	}
	if len(m.seen) != 1 || m.seen[0].Payer != account.Org("hanzo") {
		t.Errorf("the model call named payer %v, want the account the token names", m.seen)
	}
}
