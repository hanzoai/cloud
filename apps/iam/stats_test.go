// Copyright © 2026 Hanzo AI. MIT License.

package iam

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/iam/pkg/model"
)

func TestPublicStatsCountsLiveUsersAndOrgs(t *testing.T) {
	resetPublicStatsCache()
	defer resetPublicStatsCache()

	app := mountApp(t)
	db := DB()
	if db == nil {
		t.Fatal("DB() is nil after mountApp")
	}

	ctx := context.Background()
	// mountApp seeds one live user (hanzo/z) and a signing cert (no org rows).
	// Put two organizations, two more live users, and one soft-deleted user.
	for _, o := range []*model.Organization{
		{Owner: "admin", Name: "hanzo", DisplayName: "Hanzo"},
		{Owner: "admin", Name: "lux", DisplayName: "Lux"},
	} {
		if _, err := db.Put(ctx, db.NewKey("organizations", o.Owner+"/"+o.Name, 0, nil), o); err != nil {
			t.Fatalf("put org %s: %v", o.Name, err)
		}
	}
	for _, u := range []*model.User{
		{Owner: "hanzo", Name: "alice", Email: "alice@hanzo.ai"},
		{Owner: "hanzo", Name: "bob", Email: "bob@hanzo.ai"},
		{Owner: "hanzo", Name: "gone", Email: "gone@hanzo.ai", IsDeleted: true},
	} {
		if _, err := db.Put(ctx, db.NewKey("users", u.Owner+"/"+u.Name, 0, nil), u); err != nil {
			t.Fatalf("put user %s: %v", u.Name, err)
		}
	}

	req := newGet("/v1/iam/stats")
	resp, err := app.Test(req, zip.TestConfig{FailOnTimeout: true})
	if err != nil {
		t.Fatalf("GET /v1/iam/stats: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	cc := resp.Header.Get("Cache-Control")
	if !strings.Contains(cc, "public") || !strings.Contains(cc, "s-maxage=300") {
		t.Errorf("Cache-Control = %q, want public with s-maxage=300", cc)
	}

	var got PublicStats
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode stats: %v", err)
	}
	if got.Users != 3 {
		t.Errorf("users = %d, want 3 (1 seeded + 2 live, excluding soft-deleted)", got.Users)
	}
	if got.Organizations != 2 {
		t.Errorf("organizations = %d, want 2 (1 seeded + 1 added)", got.Organizations)
	}
}

func TestPublicStatsDegradedReturns503(t *testing.T) {
	resetPublicStatsCache()
	defer resetPublicStatsCache()

	notADir := filepath.Join(t.TempDir(), "occupied")
	if err := os.WriteFile(notADir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	app := zip.New(zip.Config{Logger: luxlog.New("iamtest"), DisableStartupMessage: true})
	t.Setenv("CLOUD_DATA_DIR", notADir)
	if err := Use(app, cloud.Deps{}); err != nil {
		t.Fatalf("Use: %v", err)
	}

	status, _ := get(t, app, "/v1/iam/stats")
	if status != http.StatusServiceUnavailable {
		t.Errorf("degraded GET /v1/iam/stats = %d, want 503", status)
	}
}

func newGet(path string) *http.Request {
	req, _ := http.NewRequest(http.MethodGet, path, nil)
	return req
}
