package reward

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/authz"
	"github.com/hanzoai/reward/catalog"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

func TestTheMountServesTheModulesRoutesAndOurOwn(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		for _, path := range []string{"/v1/reward/health", "/v1/reward/catalog"} {
			if code, body := r.do(http.MethodGet, path, nobodyHeaders, nil); code != http.StatusOK {
				t.Errorf("GET %s = %d %s", path, code, body)
			}
		}
		// the seeded program is there, with the weekly share rule off.
		code, body := r.do(http.MethodGet, "/v1/admin/reward/programs/hanzo-rewards", platformHeaders, nil)
		if code != http.StatusOK || !strings.Contains(string(body), `"weekly-share"`) {
			t.Fatalf("the seeded program: %d %s", code, body)
		}
		// a member with no points reads a real level, Photon 0, from their own record.
		code, body = r.do(http.MethodGet, "/v1/reward/me", memberHeaders("u1", "acme"), nil)
		if code != http.StatusOK || !strings.Contains(string(body), "Photon 0") || !strings.Contains(string(body), `"band":"Photon"`) {
			t.Errorf("GET /v1/reward/me = %d %s", code, body)
		}
		// our own operations are registered: signed out they are refused as 401, not absent.
		for _, route := range [][2]string{
			{http.MethodGet, "/v1/reward/me/posts"}, {http.MethodPost, "/v1/reward/me/posts"},
			{http.MethodPost, "/v1/reward/me/posts/scan"}, {http.MethodPost, "/v1/reward/me/posts/x/recheck"},
			{http.MethodPost, "/v1/reward/me/github/badge"}, {http.MethodPost, "/v1/reward/me/github/check"},
		} {
			if code, _ := r.do(route[0], route[1], nobodyHeaders, map[string]any{}); code != http.StatusUnauthorized && code != http.StatusForbidden {
				t.Errorf("%s %s signed out = %d, want 401 or 403", route[0], route[1], code)
			}
		}

	})
}

func TestTheAdminRoutesRefuseEveryoneButTheSuperAdmin(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		routes := [][2]string{
			{http.MethodGet, "/v1/admin/reward/programs"}, {http.MethodGet, "/v1/admin/reward/claims"},
			{http.MethodGet, "/v1/admin/reward/posts"}, {http.MethodPost, "/v1/admin/reward/legacy"},
			{http.MethodPost, "/v1/admin/reward/posts/post:x/recheck"},
		}
		orgAdmin := memberHeaders("u1", "acme")
		orgAdmin["X-User-IsOrgAdmin"] = "true"
		for name, h := range map[string]map[string]string{"anonymous": nobodyHeaders, "member": memberHeaders("u1", "acme"), "org admin": orgAdmin} {
			for _, route := range routes {
				if code, _ := r.do(route[0], route[1], h, map[string]any{}); code != http.StatusUnauthorized && code != http.StatusForbidden {
					t.Errorf("%s: %s %s = %d, want 401 or 403", name, route[0], route[1], code)
				}
			}
		}
		if code, body := r.do(http.MethodGet, "/v1/admin/reward/posts", platformHeaders, nil); code != http.StatusOK {
			t.Errorf("the SuperAdmin's list of posts: %d %s", code, body)
		}

	})
}

func TestIngestRefusesEveryClientAndOnlyTheProcessStatesEvents(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		ev := map[string]any{"id": "tokens-1", "type": "usage.tokens", "subject": "u1", "fields": map[string]any{"tokens": 100000, "model": ""}}
		orgAdmin := memberHeaders("u1", "acme")
		orgAdmin["X-User-IsOrgAdmin"] = "true"
		for name, h := range map[string]map[string]string{"anonymous": nobodyHeaders, "member": memberHeaders("u1", "acme"), "org admin": orgAdmin} {
			if code, _ := r.do(http.MethodPost, "/v1/reward/events", h, ev); code != http.StatusUnauthorized && code != http.StatusForbidden {
				t.Errorf("%s ingested: %d", name, code)
			}
		}
		if n := len(r.claims("u1")); n != 0 {
			t.Fatalf("a refused client left %d claims", n)
		}
		// the GitHub plane op is the platform's too: a member's call is refused before it reads a body.
		if code, _ := r.do(http.MethodPost, "/v1/reward/github", memberHeaders("u1", "acme"), map[string]any{"event": "star", "body": map[string]any{}}); code == http.StatusOK {
			t.Error("a member told the program a GitHub delivery")
		}
		// in process, the process states it, and the same id twice is one claim.
		out, err := r.s.emit(context.Background(), catalogEvent("tokens-1", "usage.tokens", "u1", map[string]any{"tokens": 100000, "model": ""}))
		if err != nil || claimed(out) != 1 {
			t.Fatalf("emit: %v %+v", err, out)
		}
		if out, err = r.s.emit(context.Background(), catalogEvent("tokens-1", "usage.tokens", "u1", map[string]any{"tokens": 100000, "model": ""})); err != nil || claimed(out) != 0 {
			t.Errorf("the same event again claimed %d (%v)", claimed(out), err)
		}
		if n := len(r.claimsOf("u1", "usage.tokens")); n != 1 {
			t.Errorf("claims = %d, want 1", n)
		}

	})
}

func TestAThirdPartyEventWithoutEvidenceIsRefusedNotStated(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		_, err := r.s.emit(context.Background(), catalogEvent("s1", "github.star", "u1", map[string]any{"repo": "hanzoai/x"}))
		if err == nil {
			t.Fatal("a star with no evidence was stated")
		}
		if n := len(r.claims("u1")); n != 0 {
			t.Errorf("claims = %d", n)
		}

	})
}

func catalogEvent(id, typ, subject string, fields map[string]any) catalog.Event {
	return catalog.Event{ID: id, Type: typ, Subject: subject, Fields: fields}
}

// claimJSON reads a claim back through the staff queue, as admin.hanzo.ai does.
func (r *rig) queue(subject string) []map[string]any {
	r.t.Helper()
	code, body := r.do(http.MethodGet, "/v1/admin/reward/claims?subject="+subject, platformHeaders, nil)
	if code != http.StatusOK {
		r.t.Fatalf("queue: %d %s", code, body)
	}
	var out struct {
		Claims []map[string]any `json:"claims"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		r.t.Fatalf("queue body: %v %s", err, body)
	}
	return out.Claims
}

func TestAnotherProcessStatesAnEventThroughThePlaneAndOnlyAsThePlatform(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		r := newRig(t)
		in := &client.RewardEventIn{ID: "discord.joined:42", Type: "discord.joined", Subject: "sub-ann"}
		if _, err := r.s.event(cloud.For(context.Background(), "acme"), in); err == nil {
			t.Fatal("an org that is not the platform stated an event")
		}
		ctx := cloud.For(context.Background(), authz.AdminOrg)
		if _, err := r.s.event(ctx, &client.RewardEventIn{ID: "x", Type: "no.such", Subject: "sub-ann"}); err == nil {
			t.Error("an event the catalog does not list was stated")
		}
		// the seed prices Discord at nothing and leaves it off, so the event is taken and makes no claim.
		out, err := r.s.event(ctx, in)
		if err != nil || out.Claims != 0 {
			t.Errorf("discord.joined = %+v %v", out, err)
		}
		// an event on another site without its evidence is the caller's bug: 400.
		_, err = r.s.event(ctx, &client.RewardEventIn{ID: "s", Type: "github.star", Subject: "sub-ann", Fields: mustFields(t, map[string]any{"repo": "hanzoai/x"})})
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusBadRequest {
			t.Errorf("a star with no evidence: %v", err)
		}

	})
}
