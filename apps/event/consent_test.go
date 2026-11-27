package event

import (
	"net/http"
	"strings"
	"testing"
)

// TestPolicyDecide: the region is the rule. The EU, the EEA, the UK and Switzerland
// are eu, and so is any visitor no edge placed (no header, XX, T1, junk): the strict
// answer to silence. A listed US state, or the US with no attested state, is
// us_privacy; every other place is global. Outside eu everything is on by default.
func TestPolicyDecide(t *testing.T) {
	all := Grants{true, true, true}
	for _, c := range []struct {
		country, sub, region, mode string
		refused                    bool
	}{
		{"DE", "", "DE", ModeEU, false}, {"fr", "IDF", "FR-IDF", ModeEU, false}, {"GB", "ENG", "GB-ENG", ModeEU, true},
		{"CH", "", "CH", ModeEU, false}, {"NO", "", "NO", ModeEU, false}, {"IS", "", "IS", ModeEU, false}, {"LI", "", "LI", ModeEU, false},
		{"", "", "", ModeEU, false}, {"XX", "CA", "", ModeEU, false}, {"T1", "", "", ModeEU, true}, {"USA", "CA", "", ModeEU, false},
		{"US", "CA", "US-CA", ModeUSPrivacy, false}, {"us", "ca", "US-CA", ModeUSPrivacy, true}, {"US", "TX", "US-TX", ModeUSPrivacy, false},
		{"US", "RI", "US-RI", ModeUSPrivacy, false}, {"US", "", "US", ModeUSPrivacy, false},
		{"US", "NY", "US-NY", ModeGlobal, false}, {"US", "WA", "US-WA", ModeGlobal, true}, {"JP", "13", "JP-13", ModeGlobal, false}, {"CA", "ON", "CA-ON", ModeGlobal, false},
	} {
		d := Policy.Decide(c.country, c.sub, c.refused)
		want := all
		if c.mode == ModeEU {
			want = Grants{}
		}
		if d.Mode != c.mode || d.Defaults != want || d.Region != c.region || d.Refused != c.refused || d.Version != Policy.Version || d.Floor != Policy.Floor {
			t.Errorf("%q/%q refused=%v: %+v, want mode %s region %q", c.country, c.sub, c.refused, d, c.mode, c.region)
		}
	}
	if Policy.Version != 3 || Policy.Floor != 2 {
		t.Errorf("policy %d floor %d: a version-2 choice must still count under version 3", Policy.Version, Policy.Floor)
	}
	if len(Policy.EU) != 32 || len(Policy.USPrivacy["US"]) != 20 {
		t.Errorf("eu %d countries, us_privacy %d states: the lists are the policy's", len(Policy.EU), len(Policy.USPrivacy["US"]))
	}
}

// TestARefusalForwardsNothing: GPC or DNT takes everything in every region, whatever
// the page stated.
func TestARefusalForwardsNothing(t *testing.T) {
	for _, c := range []struct {
		country, sub string
		want         Grants
	}{{"DE", "", Grants{}}, {"", "", Grants{}}, {"US", "CA", Grants{}}, {"JP", "", Grants{}}} {
		if got := Policy.Decide(c.country, c.sub, true).Effective("analytics,marketing,ads"); got != c.want {
			t.Errorf("%s/%s under GPC: %+v, want %+v", c.country, c.sub, got, c.want)
		}
		if got := Policy.Decide(c.country, c.sub, false).Effective("analytics,marketing,ads"); got != (Grants{true, true, true}) {
			t.Errorf("%s/%s: a stated grant is what forwards: %+v", c.country, c.sub, got)
		}
	}
}

// TestTheStreamIsFirstParty: every visitor's events are kept — no choice, a refusal,
// Global Privacy Control or Do Not Track, anywhere — with their ids, address and
// agent, and the person on an error. Consent says only what each may be forwarded
// under: what the page stated, and nothing when it stated none or under GPC/DNT.
func TestTheStreamIsFirstParty(t *testing.T) {
	for _, v := range []visit{{country: "DE"}, {country: "US", region: "CA"}, {country: "DE", refused: true}, {country: "US", region: "CA", refused: true}, {country: "US", region: "NY"}, {}} {
		v.ip, v.ua = "203.0.113.9", chrome
		got, _ := stamp([]CaptureEvent{
			{Type: "pageview", URL: "https://hanzo.ai/pricing?plan=pro", DistinctID: "anon-1", AnonymousID: "anon-1", SessionID: "s1"},
			{Event: "login_completed", URL: "https://hanzo.ai/", DistinctID: "u-1", Properties: map[string]any{"consent": ""}},
			{Type: "identify", DistinctID: "u-1", AnonymousID: "anon-1", Properties: map[string]any{"consent": "analytics,marketing,ads"}},
			{Type: "error", URL: "https://hanzo.ai/a?b=c", DistinctID: "anon-1", PersonID: "sub-1", Error: &Exception{Message: "x"}},
		}, v)
		if len(got) != 4 {
			t.Fatalf("%+v: kept %d of 4", v, len(got))
		}
		for _, e := range got {
			if e.DistinctID == "" || e.ip != "203.0.113.9" || e.Properties["user_agent"] != chrome {
				t.Errorf("%+v: %s lost who sent it: %+v", v, e.Type, e)
			}
		}
		if got[0].URL != "https://hanzo.ai/pricing?plan=pro" || got[0].SessionID != "s1" || got[3].Type != "error" || got[3].PersonID != "sub-1" {
			t.Errorf("%+v: the stream was rewritten: %+v", v, got)
		}
		dec := Policy.Decide(v.country, v.region, v.refused)
		want := "analytics,marketing,ads"
		if v.refused {
			want = ""
		}
		if got[0].Properties["consent"] != "" || got[1].Properties["consent"] != "" || got[2].Properties["consent"] != want {
			t.Errorf("%+v: consent = %q %q %q", v, got[0].Properties["consent"], got[1].Properties["consent"], got[2].Properties["consent"])
		}
		if (got[0].Properties["refused"] == "1") != v.refused {
			t.Errorf("%+v: refused = %v", v, got[0].Properties["refused"])
		}
		if got[0].Properties["consent_mode"] != dec.Mode {
			t.Errorf("consent_mode = %v", got[0].Properties["consent_mode"])
		}
	}
}

// TestAnOldClientsConsentForwardsNothing: an @hanzo/event built before every visitor
// opted in presumed a grant from the visitor's clock, so its event is kept and is
// forwarded under none.
func TestAnOldClientsConsentForwardsNothing(t *testing.T) {
	ev := func(version string) CaptureEvent {
		return CaptureEvent{Event: "$pageview", URL: "https://hanzo.ai/", Library: "@hanzo/event", LibraryVer: version,
			Properties: map[string]any{"consent": "analytics,marketing,ads"}}
	}
	for v, granted := range map[string]bool{"0.3.41": false, "0.3.57": false, "0.2.9": false, "junk": false, "0.3.58": true, "0.3.59": true, "0.4.0": true, "1.0.0": true} {
		got, _ := stamp([]CaptureEvent{ev(v)}, visit{})
		if len(got) != 1 || (got[0].Properties["consent"] == "analytics,marketing,ads") != granted {
			t.Errorf("%s: kept %d, consent %v, want granted %v", v, len(got), got, granted)
		}
	}
}

// TestEveryLaneKeepsTheStream: whatever admitted an event — a team token, a person's
// bearer, a publishable key, the org's secret key — it is kept, and forwarded under
// what it stated.
func TestEveryLaneKeepsTheStream(t *testing.T) {
	for _, a := range []admission{
		{org: "acme", full: true, subject: "u-1"},
		{org: "acme", full: true},
		{org: "acme", full: true, project: "site"},
		{org: "acme", full: true, vouched: true},
	} {
		e := CaptureEvent{Type: "event", Event: "doc_opened", DistinctID: "u-1", vouched: a.vouched}
		if got, _ := stamp([]CaptureEvent{e}, visit{refused: true}); len(got) != 1 || got[0].Properties["consent"] != "" {
			t.Errorf("%+v: kept %v", a, got)
		}
	}
}

// TestAnUngrantedBatchLands: a batch that states no consent is stored whole, and the
// receipt carries no wait.
func TestAnUngrantedBatchLands(t *testing.T) {
	w := fakeWarehouse(t)
	app := mountApp(t)
	code, body := doBody(t, app, http.MethodPost, "/v1/event", "user-dave", "acme", `{"batch":[{"event":"a"},{"event":"b"}]}`)
	if code != http.StatusOK || !strings.Contains(string(body), `"accepted":2`) || strings.Contains(string(body), "withheld") || len(w.facts) != 2 {
		t.Fatalf("ungranted batch = %d %s, %d facts", code, body, len(w.facts))
	}
}
