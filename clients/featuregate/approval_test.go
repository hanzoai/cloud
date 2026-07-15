// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
// Licensed under the Apache License, Version 2.0.

package featuregate

import (
	"context"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// ctxFor builds a zip.Ctx carrying the given identity headers by driving a
// throwaway app whose one handler captures the ctx.
func ctxWith(t *testing.T, headers map[string]string, fn func(c *zip.Ctx)) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test")})
	app.Get("/probe", func(c *zip.Ctx) error {
		fn(c)
		return c.NoContent(204)
	})
	hr := httptest.NewRequest("GET", "http://x/probe", nil)
	for k, v := range headers {
		hr.Header.Set(k, v)
	}
	resp, err := app.Fiber().Test(hr)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func TestApprovals_AdminAlwaysApproved(t *testing.T) {
	a := newApprovalsWithLookup(func(context.Context, string, string) (string, bool) {
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
	a := newApprovalsWithLookup(func(context.Context, string, string) (string, bool) {
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
	a := newApprovalsWithLookup(func(context.Context, string, string) (string, bool) {
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
		a := newApprovalsWithLookup(func(context.Context, string, string) (string, bool) {
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
	a := newApprovalsWithLookup(func(context.Context, string, string) (string, bool) {
		return "", false // IAM unreachable
	}, time.Minute)
	ctxWith(t, map[string]string{"X-User-Id": "u", "X-Org-Id": "acme"}, func(c *zip.Ctx) {
		if !a.Approved(c) {
			t.Fatal("IAM unreachable should FAIL-OPEN (approved) for availability")
		}
	})
}

func TestApprovals_UnauthenticatedNotApproved(t *testing.T) {
	a := newApprovalsWithLookup(func(context.Context, string, string) (string, bool) {
		t.Fatal("no lookup for an unauthenticated caller")
		return "", false
	}, time.Minute)
	ctxWith(t, map[string]string{}, func(c *zip.Ctx) {
		if a.Approved(c) {
			t.Fatal("an unauthenticated caller is not approved")
		}
	})
}

func TestApprovalStatusFromAccount(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantStatus string
		wantOK     bool
	}{
		{"top-level pending", `{"owner":"acme","properties":{"approvalStatus":"pending"}}`, "pending", true},
		{"data-wrapped approved", `{"status":"ok","data":{"owner":"acme","properties":{"approvalStatus":"approved"}}}`, "approved", true},
		{"no properties", `{"owner":"acme"}`, "", true},
		{"error envelope", `{"status":"error","msg":"nope"}`, "", false},
		{"no owner", `{"properties":{"approvalStatus":"pending"}}`, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := approvalStatusFromAccount([]byte(tc.body))
			if got != tc.wantStatus || ok != tc.wantOK {
				t.Fatalf("= (%q,%v), want (%q,%v)", got, ok, tc.wantStatus, tc.wantOK)
			}
		})
	}
}
