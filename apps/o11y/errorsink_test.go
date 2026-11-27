// Copyright 2025 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.

package o11y

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/o11y/pkg/types/errortrackingtypes"
	luxlog "github.com/luxfi/log"
)

// The issue a fact is filed under is the fact's own grouping key, so the issue row
// and the facts behind it share one key: the face reads an issue's occurrences as
// WHERE issue = <fingerprint>, and a second fingerprint here would split them.
func TestOccurrenceKeepsTheFactsIssue(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	occ := occurrence(event.Fault{
		Org: "hanzo", ID: "f-1", Time: at, Product: "hanzo-ai", Env: "production",
		Release: "1.2.3", Path: "/pricing", Person: "u-1", Trace: "t-1", Span: "s-1",
		Issue: "abc123", Class: "TypeError", Message: "x is undefined", Origin: "app.js",
		Level:  "error",
		Frames: []event.FaultFrame{{Function: "render", File: "app.js", Line: 10, Column: 4, Own: true}},
	})
	if occ.Fingerprint != "abc123" {
		t.Fatalf("fingerprint = %q, want the fact's issue", occ.Fingerprint)
	}
	if occ.Type != "TypeError" || occ.Value != "x is undefined" || occ.Culprit != "app.js" {
		t.Fatalf("exception = %q %q %q", occ.Type, occ.Value, occ.Culprit)
	}
	if occ.Environment != "production" || occ.Release != "1.2.3" || occ.ServiceName != "hanzo-ai" {
		t.Fatalf("where = %q %q %q", occ.Environment, occ.Release, occ.ServiceName)
	}
	if !occ.Timestamp.Equal(at) || occ.TraceID != "t-1" || occ.SpanID != "s-1" || occ.EventID != "f-1" {
		t.Fatalf("correlation = %+v", occ)
	}
	if occ.User == nil || occ.User.ID != "u-1" {
		t.Fatalf("user = %+v", occ.User)
	}
	want := errortrackingtypes.Frame{Function: "render", Filename: "app.js", Lineno: 10, Colno: 4, InApp: true}
	if len(occ.Frames) != 1 || occ.Frames[0] != want {
		t.Fatalf("frames = %+v", occ.Frames)
	}
}

// A fact with no class or level still files as an error, never as nothing.
func TestOccurrenceDefaults(t *testing.T) {
	occ := occurrence(event.Fault{Org: "hanzo", Issue: "i", Path: "/x"})
	if occ.Type != "Error" || occ.Level != "error" || occ.Culprit != "/x" {
		t.Fatalf("defaults = %q %q %q", occ.Type, occ.Level, occ.Culprit)
	}
}

// The announcement names what failed, where, in which build, and where to look.
func TestNewIssueText(t *testing.T) {
	text := newIssueText(
		event.Fault{Org: "hanzo", Product: "hanzo-ai", Env: "production", Release: "1.2.3"},
		&errortrackingtypes.Issue{Type: "TypeError", Value: "x is undefined", Culprit: "app.js"},
		"https://sentry.hanzo.ai/")
	for _, want := range []string{"hanzo/hanzo-ai", "TypeError: x is undefined", "production 1.2.3", "at app.js", "https://sentry.hanzo.ai"} {
		if !strings.Contains(text, want) {
			t.Errorf("announcement %q lacks %q", text, want)
		}
	}
}

// Only operator/configured orgs broadcast new Sentinel issues on the operator
// alert egress chain. External customer tenants (e.g. jamessburnss1313) are
// still ingested into Sentinel without paging Hanzo's internal inbox.
func TestAnnounceIssueFiltersCustomerOrgsByDefault(t *testing.T) {
	t.Setenv(sentinelAnnounceOrgsEnv, "")
	t.Setenv(alertsOrgEnv, "")

	var mu sync.Mutex
	var sent []string
	swapEgress(t, egress{name: "email", always: true, send: func(_ context.Context, text string) error {
		mu.Lock()
		sent = append(sent, text)
		mu.Unlock()
		return nil
	}})

	log := luxlog.New("test")
	issue := &errortrackingtypes.Issue{Type: "TypeError", Value: "boom", Culprit: "/products/front-lip"}

	// Customer storefront & customer CLI errors must NOT page operator egress.
	announceIssue(context.Background(), log, event.Fault{Org: "jamessburnss1313", Product: "carbondistrict-ie", Issue: "c1"}, issue)
	announceIssue(context.Background(), log, event.Fault{Org: "jamessburnss1313", Product: "cli", Issue: "c2"}, issue)

	// First-party operator org errors MUST still page.
	announceIssue(context.Background(), log, event.Fault{Org: "hanzo", Product: "cloud", Issue: "h1"}, issue)

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || !strings.Contains(sent[0], "hanzo/cloud") {
		t.Fatalf("sent = %v, want exactly one announcement for hanzo/cloud", sent)
	}
}

func TestShouldAnnounceOrg(t *testing.T) {
	t.Run("default uses alerts org (hanzo)", func(t *testing.T) {
		t.Setenv(sentinelAnnounceOrgsEnv, "")
		t.Setenv(alertsOrgEnv, "")
		if !shouldAnnounceOrg("hanzo") || !shouldAnnounceOrg("HANZO") {
			t.Fatal("default must announce hanzo")
		}
		if shouldAnnounceOrg("jamessburnss1313") || shouldAnnounceOrg("") {
			t.Fatal("default must not announce customer or empty orgs")
		}
	})

	t.Run("respects CLOUD_ALERTS_ORG when allowlist unset", func(t *testing.T) {
		t.Setenv(sentinelAnnounceOrgsEnv, "")
		t.Setenv(alertsOrgEnv, "lux")
		if !shouldAnnounceOrg("lux") {
			t.Fatal("must announce configured CLOUD_ALERTS_ORG")
		}
		if shouldAnnounceOrg("hanzo") || shouldAnnounceOrg("jamessburnss1313") {
			t.Fatal("must not announce orgs other than CLOUD_ALERTS_ORG")
		}
	})

	t.Run("respects explicit CLOUD_SENTINEL_ANNOUNCE_ORGS allowlist", func(t *testing.T) {
		t.Setenv(sentinelAnnounceOrgsEnv, "hanzo, lux , zoo")
		for _, org := range []string{"hanzo", "lux", "ZOO"} {
			if !shouldAnnounceOrg(org) {
				t.Errorf("allowlisted org %q should announce", org)
			}
		}
		if shouldAnnounceOrg("jamessburnss1313") || shouldAnnounceOrg("") {
			t.Fatal("non-allowlisted org must not announce")
		}
	})

	t.Run("wildcard allows any non-empty org", func(t *testing.T) {
		t.Setenv(sentinelAnnounceOrgsEnv, "*")
		if !shouldAnnounceOrg("jamessburnss1313") || !shouldAnnounceOrg("hanzo") {
			t.Fatal("wildcard must announce any non-empty org")
		}
		if shouldAnnounceOrg("") {
			t.Fatal("wildcard must still reject empty org")
		}
	})
}
