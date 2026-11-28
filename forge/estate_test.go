package forge

import "testing"

// A delivery from a namespace the estate created beside its tenants' names the
// estate's org; a tenant's canonical namespace still names the tenant; anything
// else is refused.
func TestOrgReadsTheEstatesOwnNamespaces(t *testing.T) {
	for owner, want := range map[string]string{
		"hanzoai": "hanzo", "hanzo-inc": "hanzo", "hanzozt": "hanzo", "HanzoZT": "hanzo",
		"charts": "hanzo", "hanzo-apps": "hanzo", "hanzo-community": "hanzo", "hanzo-dart": "hanzo", "hanzo-docs": "hanzo",
		"luxfi": "lux", "zooai": "zoo", "adnexus": "adnexus",
	} {
		got, err := Org(owner)
		if err != nil || got != want {
			t.Fatalf("Org(%q) = %q, %v; want %q", owner, got, err, want)
		}
	}
	// Non-existent GitHub orgs ("hanzo", "lux", "zoo") are NOT forge namespaces
	// in Org(), though they remain Reserved() so no customer can claim them.
	for _, owner := range []string{"nobody-here", "hanzo", "lux", "zoo"} {
		if _, err := Org(owner); err == nil {
			t.Fatalf("Org(%q) resolved; that namespace belongs to no org here", owner)
		}
	}
	if o, err := Owner("hanzo"); err != nil || o != "hanzoai" {
		t.Fatalf("Owner(hanzo) = %q, %v; the write direction must stay the canonical namespace", o, err)
	}
	for org, want := range map[string]string{"lux": "luxfi", "zoo": "zooai", "adnexus": "adnexus"} {
		if o, err := Owner(org); err != nil || o != want {
			t.Fatalf("Owner(%q) = %q, %v; want %q", org, o, err, want)
		}
	}
}

func TestSyncTargetAlwaysMapsOneToOneAndGuardsEstate(t *testing.T) {
	cases := []struct {
		name          string
		iamOrg, ws    string
		acct, repo    string
		wantOwner     string
		wantRepo      string
		wantCanonical bool
		wantErr       bool
	}{
		{"hanzo-inc under hanzo", "hanzo", "hanzoai", "hanzo-inc", "cloud", "hanzo-inc", "cloud", true, false},
		{"hanzoai under hanzo", "hanzo", "hanzoai", "HanzoAI", "AI", "hanzoai", "ai", true, false},
		{"hanzoskill under hanzo", "hanzo", "hanzoai", "hanzoskill", "create-content", "hanzoskill", "create-content", true, false},
		{"zenlm under hanzo", "hanzo", "hanzoai", "zenlm", "zen", "zenlm", "zen", true, false},
		{"luxfi under lux", "lux", "luxfi", "luxfi", "node", "luxfi", "node", true, false},
		{"zooai under zoo", "zoo", "zooai", "zooai", "gym", "zooai", "gym", true, false},
		{"third-party under hanzo stays 1:1", "hanzo", "hanzoai", "acme-gh", "widgets", "acme-gh", "widgets", false, false},
		{"customer own org stays 1:1", "acme", "acme-ws", "acme-gh", "widgets", "acme-gh", "widgets", false, false},
		{"cross-brand luxfi under hanzo refused", "hanzo", "hanzoai", "luxfi", "node", "", "", false, true},
		{"customer targeting estate org refused", "acme", "acme-ws", "hanzo-inc", "cloud", "", "", false, true},
		{"reserved alias hanzo refused", "hanzo", "hanzoai", "hanzo", "universe", "", "", false, true},
		{"reserved alias lux refused", "lux", "luxfi", "lux", "node", "", "", false, true},
		{"reserved alias zoo refused", "zoo", "zooai", "zoo", "gym", "", "", false, true},
		{"empty account uses workspace without underscore", "hanzo", "hanzoai", "", "orphan", "hanzoai", "orphan", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotOwner, gotRepo, gotCanonical, err := SyncTarget(tc.iamOrg, tc.ws, tc.acct, tc.repo)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("SyncTarget(%q, %q, %q, %q) = (%q, %q, %v, nil); want error",
						tc.iamOrg, tc.ws, tc.acct, tc.repo, gotOwner, gotRepo, gotCanonical)
				}
				return
			}
			if err != nil || gotOwner != tc.wantOwner || gotRepo != tc.wantRepo || gotCanonical != tc.wantCanonical {
				t.Fatalf("SyncTarget(%q, %q, %q, %q) = (%q, %q, %v, %v); want (%q, %q, %v, nil)",
					tc.iamOrg, tc.ws, tc.acct, tc.repo,
					gotOwner, gotRepo, gotCanonical, err,
					tc.wantOwner, tc.wantRepo, tc.wantCanonical)
			}
		})
	}
}
