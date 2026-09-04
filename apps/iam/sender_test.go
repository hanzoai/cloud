// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	iamserver "github.com/hanzoai/iam/server"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// runIn points this process's plane runtime directory at a fresh one and drops
// whatever the previous test resolved, so each case decides for itself what is and
// is not listening.
//
// The directory is SHORT on purpose, and t.TempDir is why it cannot be used: a unix
// socket address is 104 bytes on Darwin and t.TempDir spends most of them on the
// test's own name, so binding failed here with "invalid argument" — a test that
// breaks when a test is RENAMED, and only on a Mac. A deployment's run directory is
// /var/lib/cloud/run; this matches that shape rather than the harness's.
func runIn(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "plane")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	t.Setenv("ZIP_RUNTIME_DIR", dir)
	client.Unbind()
	t.Cleanup(client.Unbind)
	t.Cleanup(func() { iamserver.BindSender(nil) })
	iamserver.BindSender(nil)
	return dir
}

// listen puts a real listener on app's socket. Nothing is served on it — that a
// listener is THERE is the fact under test.
func listen(t *testing.T, app string) {
	t.Helper()
	l, err := net.Listen("unix", zip.SocketPath(app))
	if err != nil {
		t.Fatalf("listen %s: %v", app, err)
	}
	t.Cleanup(func() { _ = l.Close() })
}

// A SOCKET FILE WITH NOBODY BEHIND IT MUST NOT LIGHT DELIVERY.
//
// This is the defect this file exists to close, and it is the second time the fleet
// has met it. A socket file outlives the process that bound it wherever the run
// directory is a volume — cloud's is, /var/lib/cloud/run on a PVC — so the live
// identity pod carries sockets from generations that died a week before it started.
// The old delivery decision stat-ed notify.sock, found a file, bound a sender, and
// every screen then offered a code that no send could carry. client.Reach dials, and
// a file with no listener and no router to wake one is ErrNoPeer, which hides the
// method honestly.
//
// It runs with no router, which is the "no socket, no router" arm: a developer's
// machine and a single-app binary are exactly this shape, and for them "not deployed
// here" is simply true.
func TestAStaleSocketFileDoesNotLightDelivery(t *testing.T) {
	dir := runIn(t)
	t.Setenv("ZIP_ADDR", "") // no router in this process tree

	stale := filepath.Join(dir, notifyApp+".sock")
	if err := os.WriteFile(stale, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	bindDelivery(luxlog.NewNoOpLogger())

	if iamserver.DeliveryConfigured() {
		t.Fatalf("a socket FILE at %s with no listener lit delivery — every screen would offer a code that no send can carry", stale)
	}
}

// A notify that is actually THERE lights delivery.
//
// The other half of the same property: the predicate must follow the fact, so the
// honest degrade above cannot be a permanently-off switch wearing a comment.
func TestALiveNotifyLightsDelivery(t *testing.T) {
	runIn(t)
	listen(t, notifyApp)

	bindDelivery(luxlog.NewNoOpLogger())

	if !iamserver.DeliveryConfigured() {
		t.Fatal("notify is listening and delivery stayed off — email/SMS sign-in and both delivered second factors would be dark")
	}
}

// AN OUTAGE MAY NEVER READ AS AN ABSENCE: delivery stays ON when reaching notify
// fails for any reason other than "no such app here".
//
// Hiding the method on an outage would hide it forever — nothing offers a code, so
// nothing calls notify, so nothing wakes it. That is the shape that once left
// commerce unwoken behind a stale socket for three days while every prepaid-balance
// read refused. Binding means a send reports the real fault instead, which is a thing
// an operator can see and fix.
//
// The arrangement is a real production one: a process SPAWNED by a router (zip hands
// every child the socket it must serve on, so a set ZIP_ADDR is proof of a parent
// that owns an app list) whose start endpoint is not there. Reach cannot ask what is
// deployed, and it says so with an error that is not ErrNoPeer — the exact
// distinction the two branches turn on.
func TestAnOutageKeepsDeliveryOn(t *testing.T) {
	runIn(t)
	t.Setenv("ZIP_ADDR", "the-router-that-spawned-us") // under a router…
	// …whose start endpoint is absent: nothing is listening on the host socket.

	bindDelivery(luxlog.NewNoOpLogger())

	if !iamserver.DeliveryConfigured() {
		t.Fatal("an outage switched delivery OFF — the method would never come back, because nothing would ever wake notify")
	}
}

// The channel IAM says and the channel notify says are translated at exactly one
// boundary, and an unknown one is refused before anything is dialled rather than
// guessed into one of the two notify serves.
func TestChannelsAreTranslatedAtTheBoundary(t *testing.T) {
	runIn(t) // nothing listening: a translated channel still gets as far as the dial

	for _, tc := range []struct{ in, want string }{
		{"email", "email"},
		{"phone", "sms"},
		{"sms", "sms"},
	} {
		err := sender{}.Send(context.Background(), iamserver.Message{
			Org: "hanzo", Channel: tc.in, To: "dest", Body: "code",
		})
		if err == nil {
			t.Fatalf("channel %q: a send with no notify must fail", tc.in)
		}
		if !strings.Contains(err.Error(), "deliver "+tc.want) {
			t.Errorf("channel %q was carried as something other than %q: %v", tc.in, tc.want, err)
		}
	}

	err := sender{}.Send(context.Background(), iamserver.Message{Org: "hanzo", Channel: "carrier-pigeon", To: "d"})
	if err == nil || !strings.Contains(err.Error(), "unknown delivery channel") {
		t.Errorf("an unknown channel must be refused by name, got %v", err)
	}
}

// The transport is a VALUE, so the typed-nil hazard is unrepresentable rather than
// guarded against. An interface holding a struct value is never nil; the constructor
// this replaced returned an untyped nil precisely to avoid holding a typed one.
func TestTheTransportCannotBeNil(t *testing.T) {
	var s iamserver.Sender = sender{}
	if s == nil {
		t.Fatal("a struct value in an interface reported nil")
	}
}
