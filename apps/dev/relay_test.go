package dev

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"zap-proto.io/go/rpc"
	"zap-proto.io/go/transport/quic"
)

// ownsAs makes source ownership the table of source id -> subject.
func ownsAs(t *testing.T, rows map[string]string) {
	t.Helper()
	old := owned
	owned = func(_ context.Context, _, subject, id string) error {
		if rows[id] == subject {
			return nil
		}
		return errors.New("not found")
	}
	t.Cleanup(func() { owned = old })
}

var tokens = accepts{
	"mine":   {Owner: "hanzo", Subject: "sub-1"},
	"theirs": {Owner: "hanzo", Subject: "sub-2"},
}

type wire struct{ got chan [3]any }

func (w wire) push(p, s uint32, b []byte) error {
	w.got <- [3]any{p, s, append([]byte(nil), b...)}
	return nil
}

func newBridge(id string, w wire) *bridge {
	return &bridge{id: id, sess: &quic.Session{}, valid: tokens, push: w.push}
}

func status(t *testing.T, resp []byte) uint32 {
	t.Helper()
	r, err := rpc.ParseResponse(resp)
	if err != nil {
		t.Fatal(err)
	}
	return r.Status
}

func TestAPhoneIsRefusedWhenNoMacIsAttached(t *testing.T) {
	ownsAs(t, map[string]string{"src_a": "sub-1"})
	phone := newBridge("src_a", wire{make(chan [3]any, 4)})
	resp, _ := phone.dispatch(rpc.BuildRequest(rpc.Call{PromiseID: 1, Cap: []byte("mine")}))
	if status(t, resp) != http.StatusConflict || !strings.Contains(string(resp), "Mac offline") {
		t.Errorf("answered %d; want 409 Mac offline", status(t, resp))
	}
}

func TestANonOwnerIsRefusedOnEitherEnd(t *testing.T) {
	ownsAs(t, map[string]string{"src_b": "sub-1"})
	for name, method := range map[string]uint32{"desktop": methodHost, "phone": 0} {
		b := newBridge("src_b", wire{make(chan [3]any, 4)})
		resp, _ := b.dispatch(rpc.BuildRequest(rpc.Call{Method: method, PromiseID: 1, Cap: []byte("theirs")}))
		if status(t, resp) != http.StatusForbidden {
			t.Errorf("%s of another person answered %d, want 403", name, status(t, resp))
		}
	}
	if desktops.get("src_b") != nil {
		t.Error("another person's desktop was attached")
	}
}

func TestAPhoneIsJoinedToItsOwnersMacAndNobodyElse(t *testing.T) {
	ownsAs(t, map[string]string{"src_c": "sub-1"})
	hw, pw := wire{make(chan [3]any, 8)}, wire{make(chan [3]any, 8)}
	host := newBridge("src_c", hw)
	if resp, _ := host.dispatch(rpc.BuildRequest(rpc.Call{Method: methodHost, PromiseID: 7, Cap: []byte("mine")})); status(t, resp) != http.StatusOK {
		t.Fatalf("attach answered %d", status(t, resp))
	}
	phone := newBridge("src_c", pw)
	req := rpc.BuildRequest(rpc.Call{Method: 3, PromiseID: 11, Cap: []byte("mine"), Payload: []byte("list threads")})
	if resp, err := phone.dispatch(req); resp != nil || err != nil {
		t.Fatalf("dispatch = %v, %v; the Mac answers by itself", resp, err)
	}
	got := <-hw.got
	body := got[2].([]byte)
	if got[0].(uint32) != 7 || binary.LittleEndian.Uint32(body) != 1 || string(body[4:]) != string(req) {
		t.Fatalf("the Mac was sent %v; want the request unchanged on channel 1 under its attach promise", got)
	}

	// The Mac's reply reaches the phone as it was.
	reply := rpc.BuildResponse(rpc.StatusOK, 11, []byte("threads"))
	host.dispatch(rpc.BuildRequest(rpc.Call{Method: methodReply, PromiseID: 8, Payload: channel(1, reply)}))
	if g := <-pw.got; g[0].(uint32) != 11 || g[1].(uint32) != rpc.StatusOK || string(g[2].([]byte)) != "threads" {
		t.Errorf("the phone saw %v; want 11 200 threads", g)
	}

	// A second phone is its own channel, and a different person is still refused.
	other := newBridge("src_c", wire{make(chan [3]any, 4)})
	resp, _ := other.dispatch(rpc.BuildRequest(rpc.Call{PromiseID: 1, Cap: []byte("theirs")}))
	if status(t, resp) != http.StatusForbidden {
		t.Errorf("another person's phone answered %d, want 403", status(t, resp))
	}
}

func TestAMacGoingAwayTellsItsPhones(t *testing.T) {
	ownsAs(t, map[string]string{"src_d": "sub-1"})
	hw, pw := wire{make(chan [3]any, 4)}, wire{make(chan [3]any, 4)}
	host := newBridge("src_d", hw)
	host.dispatch(rpc.BuildRequest(rpc.Call{Method: methodHost, PromiseID: 1, Cap: []byte("mine")}))
	phone := newBridge("src_d", pw)
	phone.dispatch(rpc.BuildRequest(rpc.Call{PromiseID: 2, Cap: []byte("mine")}))
	<-hw.got
	desktops.detach("src_d", host.rel.host)
	if g := <-pw.got; g[1].(uint32) != http.StatusConflict || string(g[2].([]byte)) != "Mac offline" {
		t.Errorf("the phone saw %v; want 409 Mac offline", g)
	}
	resp, _ := newBridge("src_d", pw).dispatch(rpc.BuildRequest(rpc.Call{PromiseID: 3, Cap: []byte("mine")}))
	if status(t, resp) != http.StatusConflict {
		t.Errorf("a later phone answered %d, want 409", status(t, resp))
	}
}

// Both ends on the browser's wire, through the real listener.
func TestAPhoneReachesAMacThroughWebTransport(t *testing.T) {
	ownsAs(t, map[string]string{"src_e": "sub-1"})
	srv, err := serve("127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{selfSigned(t)}}, tokens)
	if err != nil {
		t.Fatalf("serve: %v", err)
	}
	defer srv.Close()
	url := "https://" + srv.Addr().String() + Path + "src_e"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dial := func() (io.ReadWriter, func() error) {
		st, done, err := quic.DialWebTransport(ctx, url, &tls.Config{InsecureSkipVerify: true}, nil)
		if err != nil {
			t.Fatalf("dial: %v", err)
		}
		return st, done
	}
	read := func(st io.Reader) rpc.Response {
		env, err := quic.ReadEnvelope(st)
		if err != nil {
			t.Fatal(err)
		}
		r, err := rpc.ParseResponse(env)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	mac, doneMac := dial()
	defer func() { _ = doneMac() }()
	quic.WriteEnvelope(mac, rpc.BuildRequest(rpc.Call{Method: methodHost, PromiseID: 1, Cap: []byte("mine")}))
	if r := read(mac); r.Status != http.StatusOK || len(r.Body) != 0 {
		t.Fatalf("attach = %+v", r)
	}

	phone, donePhone := dial()
	defer func() { _ = donePhone() }()
	req := rpc.BuildRequest(rpc.Call{Method: 3, PromiseID: 21, Cap: []byte("mine"), Payload: []byte("hi")})
	quic.WriteEnvelope(phone, req)
	push := read(mac)
	if push.PromiseID != 1 || string(push.Body[4:]) != string(req) {
		t.Fatalf("the Mac was pushed %+v; want the phone's envelope unchanged", push)
	}
	ch := binary.LittleEndian.Uint32(push.Body)
	quic.WriteEnvelope(mac, rpc.BuildRequest(rpc.Call{Method: methodReply, PromiseID: 2,
		Payload: channel(ch, rpc.BuildResponse(rpc.StatusOK, 21, []byte("hello")))}))
	if r := read(phone); r.PromiseID != 21 || r.Status != rpc.StatusOK || string(r.Body) != "hello" {
		t.Errorf("the phone saw %+v; want 21 200 hello", r)
	}

	stranger, doneStranger := dial()
	defer func() { _ = doneStranger() }()
	quic.WriteEnvelope(stranger, rpc.BuildRequest(rpc.Call{PromiseID: 5, Cap: []byte("theirs")}))
	if r := read(stranger); r.Status != http.StatusForbidden {
		t.Errorf("another person's phone answered %d, want 403", r.Status)
	}
}

// A cell with no Dev binary fails at once, not after the full wait for its socket.
func TestAStartWithNoDevBinaryFailsFast(t *testing.T) {
	if _, err := os.Stat(Socket); err == nil {
		t.Skip("a Dev socket exists on this host")
	}
	begin := time.Now()
	cmd := exec.Command("sh", "-c", start, "sh", "hanzo/x")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 127 {
		t.Fatalf("start = %v %q; want exit 127", err, out)
	}
	if time.Since(begin) > 5*time.Second || !strings.Contains(string(out), "hanzo-dev is not installed") {
		t.Errorf("took %v, said %q", time.Since(begin), out)
	}
}
