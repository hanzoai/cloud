package projects

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/zap-proto/zip"

	"github.com/hanzoai/cloud/apps/event"
	"github.com/hanzoai/cloud/sdk"
	fiber "github.com/zap-proto/fiber/v3"
)

const chrome = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36"

// tagsFor asks the tag endpoint as one visitor. The identity headers stand in for
// what the boundary mints (event.TestAudience and the root package's boundary test
// drive the rest).
func tagsFor(t *testing.T, st *Store, key string, h map[string]string) (tagConfig, http.Header) {
	t.Helper()
	s := &cloud.Service[state]{State: state{store: st}}
	app := zip.New(zip.Config{DisableStartupMessage: true})
	app.Raw(http.MethodGet, "/v1/project/tags", func(c *zip.Ctx) error { return serveTags(s, c) })
	if err := app.Build(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/v1/project/tags?key="+key, nil)
	for k, v := range h {
		r.Header.Set(k, v)
	}
	res, err := app.Fiber().Test(r, fiber.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatal(err)
	}
	var out tagConfig
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	return out, res.Header
}

func types(tags []browserTagOut) []string {
	out := []string{}
	for _, t := range tags {
		out = append(out, t.Type)
	}
	return out
}

// The tag set answers for who is asking: nothing for a program, Google Analytics
// alone for the site's own team, everything for a person.
func TestTagsAnswerForTheAudience(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := storeAt(t)
		p := Project{Org: "acme", Slug: "shop", ID: "shop", CreatedAt: 1, UpdatedAt: 1,
			Tags: map[string]string{"ga4": "G-ACME", "meta": "123", "tiktok": "TT1"}}
		if err := setProjectDefaults(&p, nil); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateProject(ctx, p); err != nil {
			t.Fatal(err)
		}
		p.InternalNetworks = []string{"198.51.100.0/24"}
		if err := st.UpdateProject(ctx, p); err != nil {
			t.Fatal(err)
		}

		home, office := "203.0.113.9", "198.51.100.40"
		all := []string{"ga", "meta", "tiktok"}
		for _, tc := range []struct {
			name, audience string
			want           []string
			h              map[string]string
		}{
			{"curl", event.Bot, []string{}, map[string]string{"User-Agent": "curl/8.5.0", "X-Forwarded-For": home}},
			{"a person", event.Person, all, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home}},
			{"a member of the site's org", event.Internal, []string{"ga"}, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home, "X-User-Id": "u-1", "X-User-Orgs": "acme", "X-Org-Id": "acme"}},
			{"a member of another org", event.Person, all, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home, "X-User-Id": "u-2", "X-User-Orgs": "globex", "X-Org-Id": "globex"}},
			{"the project's own network", event.Internal, []string{"ga"}, map[string]string{"User-Agent": chrome, "X-Forwarded-For": office}},
			// The page's IAM bearer is who is asking and never the key: the site still
			// resolves from ?key=.
			{"a bearer beside the key", event.Person, all, map[string]string{"User-Agent": chrome, "X-Forwarded-For": home, "Authorization": "Bearer eyJhbGciOiJSUzI1NiJ9.e30.sig"}},
		} {
			out, hdr := tagsFor(t, st, p.Key, tc.h)
			if out.Audience != tc.audience || !reflect.DeepEqual(types(out.Tags), tc.want) {
				t.Errorf("%s: audience %q tags %v, want %q %v", tc.name, out.Audience, types(out.Tags), tc.audience, tc.want)
			}
			if hdr.Get("Vary") == "" {
				t.Errorf("%s: a signed-in answer is not the signed-out one, so it varies", tc.name)
			}
		}
	})
}

func TestNetworksOf(t *testing.T) {
	got, err := networksOf([]string{" 198.51.100.7/24", "2001:db8::1", "203.0.113.9", "198.51.100.0/24", "::ffff:192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"198.51.100.0/24", "2001:db8::1/128", "203.0.113.9/32", "192.0.2.1/32"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("networksOf = %v, want %v", got, want)
	}
	if got, err := networksOf([]string{}); err != nil || len(got) != 0 || got == nil {
		t.Errorf("an empty list clears: %v %v", got, err)
	}
	for _, bad := range [][]string{{"office"}, {"198.51.100.0/33"}, make([]string, maxNetworks+1)} {
		if _, err := networksOf(bad); err == nil {
			t.Errorf("networksOf(%v) accepted", bad)
		}
	}
}

// The networks round-trip the store and reach ingest through the key, the one
// place ingest learns a project's settings.
func TestNetworksReachTheKey(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := storeAt(t)
		p := Project{Org: "acme", Slug: "shop", ID: "shop", CreatedAt: 1, UpdatedAt: 1}
		if err := setProjectDefaults(&p, nil); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateProject(ctx, p); err != nil {
			t.Fatal(err)
		}
		got, _ := st.GetProject(ctx, "acme", "shop")
		if got.InternalNetworks == nil || len(got.InternalNetworks) != 0 {
			t.Errorf("a new project has no internal networks, as an empty list: %#v", got.InternalNetworks)
		}
		got.InternalNetworks = []string{"198.51.100.0/24"}
		if err := st.UpdateProject(ctx, got); err != nil {
			t.Fatal(err)
		}
		at, ok, err := keyResolver{store: st}.Resolve(ctx, p.Key)
		if err != nil || !ok || !reflect.DeepEqual(at.InternalNetworks, []string{"198.51.100.0/24"}) {
			t.Errorf("Resolve = %+v %v %v", at, ok, err)
		}
		if v := toProject(got); !reflect.DeepEqual(v.InternalNetworks, []string{"198.51.100.0/24"}) {
			t.Errorf("the project view carries them: %v", v.InternalNetworks)
		}
	})
}

// The rule is the region the edge attested, read only from a Cloudflare hop: the set is
// the same under every rule, and a caller's own location headers place it nowhere.
func TestTagsAnswerForTheRegion(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		st := storeAt(t)
		p := Project{Org: "acme", Slug: "shop", ID: "shop", CreatedAt: 1, UpdatedAt: 1, Tags: map[string]string{"ga4": "G-ACME", "meta": "123"}}
		if err := setProjectDefaults(&p, nil); err != nil {
			t.Fatal(err)
		}
		if err := st.CreateProject(ctx, p); err != nil {
			t.Fatal(err)
		}
		const edge = "104.22.100.153" // a Cloudflare address
		at := func(country, region string, through bool) map[string]string {
			h := map[string]string{"User-Agent": chrome, "X-Forwarded-For": "203.0.113.9", "CF-IPCountry": country, "CF-Region-Code": region}
			if through {
				h["X-Forwarded-For"] = "203.0.113.9, " + edge
			}
			return h
		}
		for _, tc := range []struct {
			name, mode, region string
			h                  map[string]string
		}{
			{"Germany", event.ModeEU, "DE", at("DE", "", true)},
			{"California", event.ModeUSPrivacy, "US-CA", at("US", "CA", true)},
			{"the US with no state", event.ModeUSPrivacy, "US", at("US", "", true)},
			{"New York", event.ModeGlobal, "US-NY", at("US", "NY", true)},
			{"Japan", event.ModeGlobal, "JP", at("JP", "", true)},
			{"unplaced", event.ModeEU, "", at("XX", "", true)},
			{"a caller's own headers, around Cloudflare", event.ModeEU, "", at("US", "NY", false)},
		} {
			out, hdr := tagsFor(t, st, p.Key, tc.h)
			if out.Consent.Mode != tc.mode || out.Consent.Region != tc.region {
				t.Errorf("%s: mode %q region %q, want %q %q", tc.name, out.Consent.Mode, out.Consent.Region, tc.mode, tc.region)
			}
			if !reflect.DeepEqual(types(out.Tags), []string{"ga", "meta"}) {
				t.Errorf("%s: tags %v, want the whole set", tc.name, types(out.Tags))
			}
			if !strings.Contains(hdr.Get("Vary"), "CF-Region-Code") {
				t.Errorf("%s: Vary = %q", tc.name, hdr.Get("Vary"))
			}
		}
	})
}
