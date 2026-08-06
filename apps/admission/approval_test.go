// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
// Licensed under the Apache License, Version 2.0.

package admission

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/plane"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ctxFor builds a zip.Ctx carrying the given identity headers by driving a
// throwaway app whose one handler captures the ctx.
func ctxWith(t *testing.T, headers map[string]string, fn func(c *zip.Ctx)) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	compose(app)
	app.Get("/probe", func(c *zip.Ctx) error {
		fn(c)
		return c.NoContent(204)
	})
	hr := httptest.NewRequest("GET", "http://x/probe", nil)
	for k, v := range headers {
		hr.Header.Set(k, v)
	}
	resp, err := app.Test(hr)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func TestApprovals_AdminAlwaysApproved(t *testing.T) {
	a := newApprovalsWithLookup(func(context.Context) (string, bool) {
		t.Fatal("admin must not trigger an IAM lookup")
		return "", false
	}, time.Minute)
	ctxWith(t, map[string]string{"X-User-IsAdmin": "true", "X-User-Id": "u"}, func(c *zip.Ctx) {
		if !a.Approved(c) {
			t.Fatal("admin should be approved")
		}
	})
}

func TestApprovals_ForwardHeaderWins(t *testing.T) {
	a := newApprovalsWithLookup(func(context.Context) (string, bool) {
		t.Fatal("header path must not trigger an IAM lookup")
		return "", false
	}, time.Minute)
	ctxWith(t, map[string]string{"X-User-Id": "u", "X-User-Approved": "true"}, func(c *zip.Ctx) {
		if !a.Approved(c) {
			t.Fatal("X-User-Approved=true should be approved")
		}
	})
	ctxWith(t, map[string]string{"X-User-Id": "u", "X-User-Approved": "false"}, func(c *zip.Ctx) {
		if a.Approved(c) {
			t.Fatal("X-User-Approved=false should NOT be approved")
		}
	})
}

func TestApprovals_IAMLookup_PendingGates(t *testing.T) {
	calls := 0
	a := newApprovalsWithLookup(func(context.Context) (string, bool) {
		calls++
		return "pending", true
	}, time.Minute)
	ctxWith(t, map[string]string{"X-User-Id": "u", "X-Org-Id": "acme"}, func(c *zip.Ctx) {
		if a.Approved(c) {
			t.Fatal("approvalStatus=pending should NOT be approved")
		}
	})
	// Second call hits the cache (no second lookup).
	ctxWith(t, map[string]string{"X-User-Id": "u", "X-Org-Id": "acme"}, func(c *zip.Ctx) {
		if a.Approved(c) {
			t.Fatal("cached pending should NOT be approved")
		}
	})
	if calls != 1 {
		t.Fatalf("IAM lookups = %d, want 1 (cached)", calls)
	}
}

func TestApprovals_IAMLookup_ApprovedAndAbsentPass(t *testing.T) {
	for _, status := range []string{"approved", "", "rejected"} {
		a := newApprovalsWithLookup(func(context.Context) (string, bool) {
			return status, true
		}, time.Minute)
		ctxWith(t, map[string]string{"X-User-Id": "u", "X-Org-Id": "acme"}, func(c *zip.Ctx) {
			if !a.Approved(c) {
				t.Fatalf("approvalStatus=%q should be approved (fail-open, only 'pending' gates)", status)
			}
		})
	}
}

func TestApprovals_FailOpenOnIAMError(t *testing.T) {
	a := newApprovalsWithLookup(func(context.Context) (string, bool) {
		return "", false // IAM unreachable
	}, time.Minute)
	ctxWith(t, map[string]string{"X-User-Id": "u", "X-Org-Id": "acme"}, func(c *zip.Ctx) {
		if !a.Approved(c) {
			t.Fatal("IAM unreachable should FAIL-OPEN (approved) for availability")
		}
	})
}

func TestApprovals_UnauthenticatedNotApproved(t *testing.T) {
	a := newApprovalsWithLookup(func(context.Context) (string, bool) {
		t.Fatal("no lookup for an unauthenticated caller")
		return "", false
	}, time.Minute)
	ctxWith(t, map[string]string{}, func(c *zip.Ctx) {
		if a.Approved(c) {
			t.Fatal("an unauthenticated caller is not approved")
		}
	})
}

// The waitlist read, over the real plane.
//
// This replaces a table test over an IAM JSON envelope — {status,data,properties}
// with its top-level and data-wrapped shapes — which went away with the HTTP
// client that had to parse it. What that test was really pinning is the
// distinction the gate turns on, and it is pinned here against a live socket:
//
//	iam SAID "pending"     → gated
//	iam SAID nothing       → approved (an absent approvalStatus is approved)
//	iam could not be ASKED → approved (fail-open), and NOT cached
//
// The third is the one worth a socket. It is a security gate whose availability
// rule says an unreachable identity store must not lock everyone out, and moving
// the transport underneath it is exactly when that rule gets broken by accident.
func TestApprovals_PlaneAnswerDecidesTheGate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   string
		approved bool
	}{
		{"pending gates", "pending", false},
		{"approved passes", "approved", true},
		{"absent status is approved", "", true},
		{"rejected is not pending, so it passes", "rejected", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stop := servePeerApproval(t, tc.status)
			t.Cleanup(func() { _ = stop(); cloud.ResetPlane() })
			a := NewApprovals(time.Minute)
			ctxWith(t, map[string]string{"X-User-Id": "u"}, func(c *zip.Ctx) {
				if got := a.Approved(c); got != tc.approved {
					t.Fatalf("iam said %q → Approved = %v, want %v", tc.status, got, tc.approved)
				}
			})
		})
	}
}

// TestApprovals_AbsentPeerFailsOpenAndIsNotCached is the availability rule, with
// the peer genuinely not there. A gate that locked every user out because iam was
// restarting would be a worse outage than the one it is guarding against — and
// caching that verdict would keep them locked out after iam came back.
func TestApprovals_AbsentPeerFailsOpenAndIsNotCached(t *testing.T) {
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	plane.Unbind()
	cloud.ResetPlane() // nothing listening

	a := NewApprovals(time.Minute)
	ctxWith(t, map[string]string{"X-User-Id": "u"}, func(c *zip.Ctx) {
		if !a.Approved(c) {
			t.Fatal("an unreachable iam must FAIL OPEN; the gate locked a user out")
		}
	})
	if _, cached := a.get("u"); cached {
		t.Fatal("a fail-open verdict was CACHED; a recovered iam would not re-gate for a full ttl")
	}

	// iam comes back saying pending, and the very next request is gated.
	stop := servePeerApproval(t, "pending")
	t.Cleanup(func() { _ = stop(); cloud.ResetPlane() })
	ctxWith(t, map[string]string{"X-User-Id": "u"}, func(c *zip.Ctx) {
		if a.Approved(c) {
			t.Fatal("iam recovered and said pending, but the caller was still approved")
		}
	})
}

// TestApprovals_NoCredentialCrossesTheWire is the point of the change, checked on
// the wire rather than by asking the callee.
//
// The lookup used to replay the caller's Cookie and Authorization to IAM. It
// cannot now, because it is handed neither — but "cannot" is a claim about source,
// and the bytes are the fact. A recording relay sits at the socket the caller
// dials, and the assertion is that the caller's secret is nowhere in the frames
// while the subject the gateway asserted is.
//
// (A plane handler has no header accessor at all, which is the structural half of
// the same guarantee: even a forwarded credential would have nothing to read it.)
func TestApprovals_NoCredentialCrossesTheWire(t *testing.T) {
	const secret = "super-secret-credential"
	front, back := t.TempDir(), t.TempDir()

	t.Setenv("ZIP_RUNTIME_DIR", back)
	plane.Unbind()
	cloud.ResetPlane()
	var sawUser string
	zip.Post[struct{}, plane.Approval](cloud.Plane(), "/iam/approval",
		func(ctx context.Context, _ *struct{}) (*plane.Approval, error) {
			sawUser = cloud.Who(ctx).User
			return &plane.Approval{Status: "approved"}, nil
		},
		zip.WithOperationID(plane.IAMApproval))
	stop, err := cloud.ServePlane("iam", nil)
	if err != nil {
		t.Fatalf("ServePlane(iam): %v", err)
	}
	t.Cleanup(func() { _ = stop(); cloud.ResetPlane() })
	peer := filepath.Join(back, "iam.sock")
	waitAccept(t, peer)

	var mu sync.Mutex
	var seen bytes.Buffer
	ln, err := net.Listen("unix", filepath.Join(front, "iam.sock"))
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				up, derr := net.Dial("unix", peer)
				if derr != nil {
					return
				}
				defer func() { _ = up.Close() }()
				done := make(chan struct{})
				go func() {
					_, _ = io.Copy(io.MultiWriter(up, locked{&mu, &seen}), c)
					_ = up.(*net.UnixConn).CloseWrite()
					close(done)
				}()
				_, _ = io.Copy(c, up)
				<-done
			}()
		}
	}()

	t.Setenv("ZIP_RUNTIME_DIR", front)
	plane.Unbind()
	a := NewApprovals(time.Minute)
	ctxWith(t, map[string]string{
		"X-User-Id":     "u-real",
		"Cookie":        "session=" + secret,
		"Authorization": "Bearer " + secret,
	}, func(c *zip.Ctx) { _ = a.Approved(c) })

	mu.Lock()
	wire := seen.String()
	mu.Unlock()
	if wire == "" {
		t.Fatal("the relay captured nothing; no bytes crossed the socket")
	}
	if strings.Contains(wire, secret) {
		t.Fatalf("the caller's CREDENTIAL is on the wire to iam:\n%s", wire)
	}
	if !strings.Contains(wire, "u-real") {
		t.Fatalf("the asserted subject is NOT on the wire; iam cannot know who is asking:\n%s", wire)
	}
	if sawUser != "u-real" {
		t.Fatalf("iam saw subject %q, want u-real", sawUser)
	}
}

// locked serializes writes into a capture buffer shared with the test goroutine.
type locked struct {
	mu *sync.Mutex
	b  *bytes.Buffer
}

func (l locked) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

// waitAccept blocks until path accepts.
func waitAccept(t *testing.T, path string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if c, err := net.Dial("unix", path); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s never accepted", path)
}

// servePeerApproval stands iam up on a real socket answering one status. peek, when
// set, observes the call's context on the callee side.
func servePeerApproval(t *testing.T, status string) func() error {
	t.Helper()
	t.Setenv("ZIP_RUNTIME_DIR", t.TempDir())
	plane.Unbind()
	cloud.ResetPlane()
	zip.Post[struct{}, plane.Approval](cloud.Plane(), "/iam/approval",
		func(context.Context, *struct{}) (*plane.Approval, error) {
			return &plane.Approval{Status: status}, nil
		},
		zip.WithOperationID(plane.IAMApproval))
	stop, err := cloud.ServePlane("iam", nil)
	if err != nil {
		t.Fatalf("ServePlane(iam): %v", err)
	}
	for i := 0; i < 200; i++ {
		if c, derr := net.Dial("unix", zip.SocketPath("iam")); derr == nil {
			_ = c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return stop
}
