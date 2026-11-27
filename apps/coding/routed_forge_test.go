package coding

import (
	"errors"
	"net/http"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/zap-proto/zip"
)

// A routed run on the forge opens its pull request as a forge login, so the login is
// the caller's own as the forge resolves it, never one the request body names.
func TestRoutedForgeActor_IsTheCallersLoginNeverTheBodys(t *testing.T) {
	iamAddr = map[string]client.Email{"sub-z": verified("z@hanzo.ai")}
	forgeAs(t, map[string]bool{"z": true, "w": true}, func(login string) string { return login + "@hanzo.ai" })
	ctx := asCaller("sub-z")

	_, err := RoutedForgeActor(ctx, client.RouteRunIn{Repo: "hanzo/app", ForgeActor: "w"})
	var he *zip.HTTPError
	if !errors.As(err, &he) || he.Status != http.StatusForbidden {
		t.Fatalf("a body naming another person's login = %v, want 403", err)
	}
	for _, named := range []string{"", "z"} {
		got, err := RoutedForgeActor(ctx, client.RouteRunIn{Repo: "hanzo/app", ForgeActor: named})
		if err != nil || got != "z" {
			t.Errorf("body naming %q: login %q, %v; want the caller's z", named, got, err)
		}
	}
	// A run that never touches the forge as a person has no login to forge.
	if got, err := RoutedForgeActor(ctx, client.RouteRunIn{Repo: "hanzo/app", Made: true}); err != nil || got != "" {
		t.Errorf("a made workspace's run: %q, %v; want none", got, err)
	}
}

// A run with no repository opens no pull request, so it needs no forge login and is
// not refused for want of one: a caller the forge cannot resolve still runs it.
func TestRoutedForgeActor_ARunWithNoRepositoryNeedsNoLogin(t *testing.T) {
	iamAddr = map[string]client.Email{}
	forgeAs(t, map[string]bool{}, func(login string) string { return login + "@hanzo.ai" })
	ctx := asCaller("sub-nobody")
	if _, err := RoutedForgeActor(ctx, client.RouteRunIn{Repo: "hanzo/app"}); err == nil {
		t.Fatal("premise: a caller the forge cannot resolve must be refused a run on a repository")
	}
	if got, err := RoutedForgeActor(ctx, client.RouteRunIn{}); err != nil || got != "" {
		t.Errorf("a run with no repository: %q, %v; want none and no refusal", got, err)
	}
}
