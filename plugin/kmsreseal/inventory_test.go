package main

import (
	"testing"
)

// synthetic CR JSON mirroring every shape the live fleet exhibits: explicit keys,
// absent envSlug (→ default), folder-sync (empty keys[]), a duplicate coordinate
// across two CRs (idempotent upsert), a second org, and three malformed CRs. Uses
// the List envelope shape kubectl emits.
const fixtureListForm = `{"items":[
  {"metadata":{"namespace":"hanzo","name":"admin-guard-kms-sync"},
   "spec":{"hostAPI":"http://kms.hanzo.svc",
     "authentication":{"universalAuth":{
       "credentialsRef":{"secretName":"hanzo-platform-iam-creds","secretNamespace":"hanzo"},
       "secretsScope":{"projectSlug":"hanzo","envSlug":"prod","secretsPath":"/admin-guard-secrets","keys":["GUARD_HMAC_KEY","IAM_CLIENT_SECRET"]}}},
     "managedSecretReference":{"secretName":"admin-guard-secrets","secretNamespace":"hanzo","creationPolicy":"Orphan"}}},

  {"metadata":{"namespace":"hanzo","name":"no-env-kms-sync"},
   "spec":{"hostAPI":"http://kms.hanzo.svc",
     "authentication":{"universalAuth":{
       "credentialsRef":{"secretName":"hanzo-platform-iam-creds"},
       "secretsScope":{"projectSlug":"hanzo","secretsPath":"/no-env","keys":["ONLY_KEY"]}}},
     "managedSecretReference":{"secretName":"no-env","creationPolicy":"Orphan"}}},

  {"metadata":{"namespace":"hanzo","name":"commerce-folder-kms-sync"},
   "spec":{"hostAPI":"http://kms.hanzo.svc",
     "authentication":{"universalAuth":{
       "credentialsRef":{"secretName":"hanzo-commerce-iam-creds"},
       "secretsScope":{"projectSlug":"hanzo-commerce","envSlug":"prod","secretsPath":"/","keys":[]}}},
     "managedSecretReference":{"secretName":"commerce-secrets","creationPolicy":"Orphan"}}},

  {"metadata":{"namespace":"hanzo","name":"dup-a-kms-sync"},
   "spec":{"hostAPI":"http://kms.hanzo.svc",
     "authentication":{"universalAuth":{
       "credentialsRef":{"secretName":"hanzo-platform-iam-creds"},
       "secretsScope":{"projectSlug":"hanzo","envSlug":"prod","secretsPath":"/datastore","keys":["DATASTORE_PASSWORD"]}}},
     "managedSecretReference":{"secretName":"datastore-a","creationPolicy":"Orphan"}}},

  {"metadata":{"namespace":"hanzo","name":"dup-b-kms-sync"},
   "spec":{"hostAPI":"http://kms.hanzo.svc",
     "authentication":{"universalAuth":{
       "credentialsRef":{"secretName":"hanzo-platform-iam-creds"},
       "secretsScope":{"projectSlug":"hanzo","envSlug":"prod","secretsPath":"/datastore","keys":["DATASTORE_PASSWORD"]}}},
     "managedSecretReference":{"secretName":"datastore-b","creationPolicy":"Orphan"}}},

  {"metadata":{"namespace":"hanzo-vector","name":"devnet-kms-sync"},
   "spec":{"hostAPI":"http://kms.hanzo-devnet.svc",
     "authentication":{"universalAuth":{
       "credentialsRef":{"secretName":"devnet-kms-creds"},
       "secretsScope":{"projectSlug":"hanzo-vector","envSlug":"devnet","secretsPath":"/qdrant","keys":["API_KEY"]}}},
     "managedSecretReference":{"secretName":"qdrant","creationPolicy":"Orphan"}}},

  {"metadata":{"namespace":"hanzo","name":"bad-org-kms-sync"},
   "spec":{"authentication":{"universalAuth":{"secretsScope":{"projectSlug":"acme/../evil","envSlug":"prod","secretsPath":"/x","keys":["K"]}}}}},

  {"metadata":{"namespace":"hanzo","name":"bad-path-kms-sync"},
   "spec":{"authentication":{"universalAuth":{"secretsScope":{"projectSlug":"hanzo","envSlug":"prod","secretsPath":"/a/../b","keys":["K"]}}}}},

  {"metadata":{"namespace":"hanzo","name":"bad-key-kms-sync"},
   "spec":{"authentication":{"universalAuth":{"secretsScope":{"projectSlug":"hanzo","envSlug":"prod","secretsPath":"/ok","keys":["good","bad/slash"]}}}}}
]}`

func buildFixture(t *testing.T) Inventory {
	t.Helper()
	crs, err := parseCRs([]byte(fixtureListForm))
	if err != nil {
		t.Fatalf("parseCRs: %v", err)
	}
	return BuildInventory(crs)
}

func TestInventory_ExpandsAndValidates(t *testing.T) {
	inv := buildFixture(t)

	if inv.CRCount != 9 {
		t.Fatalf("CRCount=%d, want 9", inv.CRCount)
	}
	// Explicit-key rows: admin(2) + no-env(1) + dup-a(1) + dup-b(1 dup) + devnet(1) + bad-key good(1) = 7 rows.
	if inv.RowsTotal != 7 {
		t.Errorf("RowsTotal=%d, want 7", inv.RowsTotal)
	}
	// Unique targets: dup collapses to one → 6 unique.
	if inv.UniqueCount != 6 {
		t.Errorf("UniqueCount=%d, want 6 (dup collapsed)", inv.UniqueCount)
	}
	if len(inv.Folders) != 1 {
		t.Errorf("Folders=%d, want 1 (commerce root folder-sync)", len(inv.Folders))
	}
	// Malformed: bad-org, bad-path, and the single bad key inside bad-key-kms-sync.
	if len(inv.Malformed) != 3 {
		t.Errorf("Malformed=%d, want 3: %+v", len(inv.Malformed), inv.Malformed)
	}
}

func TestInventory_EnvDefaultDivergence(t *testing.T) {
	inv := buildFixture(t)
	var found bool
	for _, tg := range inv.Targets {
		if tg.CRName == "no-env-kms-sync" {
			found = true
			if tg.Env != "default" {
				t.Errorf("absent envSlug → Env=%q, want %q (standalone default; cloud refuses empty)", tg.Env, defaultEnv)
			}
		}
	}
	if !found {
		t.Fatal("no-env target missing")
	}
}

func TestInventory_DuplicateCollapsesToOneUpsert(t *testing.T) {
	inv := buildFixture(t)
	c := Coord{Org: "hanzo", Path: "datastore", Env: "prod", Key: "DATASTORE_PASSWORD"}
	if n := inv.Duplicates[c]; n != 2 {
		t.Errorf("Duplicates[%s]=%d, want 2 (referenced by dup-a + dup-b)", c, n)
	}
	// It appears exactly once in the migration set.
	count := 0
	for _, tg := range inv.Targets {
		if tg.Coord() == c {
			count++
		}
	}
	if count != 1 {
		t.Errorf("duplicate coordinate migrated %d times, want exactly 1 (idempotent)", count)
	}
}

func TestInventory_FolderSyncMarked(t *testing.T) {
	inv := buildFixture(t)
	f := inv.Folders[0]
	if !f.Folder || f.Key != "" {
		t.Errorf("folder target Folder=%v Key=%q, want true/empty", f.Folder, f.Key)
	}
	if f.Org != "hanzo-commerce" || f.Path != "" {
		t.Errorf("folder target org/path = %s//%s, want hanzo-commerce root ('')", f.Org, f.Path)
	}
	if f.Env != "prod" {
		t.Errorf("folder env=%q, want prod", f.Env)
	}
}

func TestInventory_NormPathTrimsSlashes(t *testing.T) {
	cases := map[string]string{
		"/admin-guard-secrets": "admin-guard-secrets",
		"admin-guard-secrets/": "admin-guard-secrets",
		"/":                    "",
		"":                     "",
		"/platform/api/":       "platform/api",
		"//weird//":            "weird",
	}
	for in, want := range cases {
		if got := normPath(in); got != want {
			t.Errorf("normPath(%q)=%q, want %q", in, got, want)
		}
	}
}

func TestInventory_MultiOrgAndHostAndBareArrayForm(t *testing.T) {
	inv := buildFixture(t)
	// Valid-scope CRs contribute their org: hanzo, hanzo-commerce (folder), hanzo-vector.
	// bad-org is rejected before orgSet; bad-path likewise.
	if len(inv.Orgs) != 3 {
		t.Errorf("Orgs=%v, want 3 [hanzo hanzo-commerce hanzo-vector]", inv.Orgs)
	}
	// devnet points at a SEPARATE standalone host — surfaced so the cutover excludes it.
	if inv.Hosts["http://kms.hanzo-devnet.svc"] == 0 {
		t.Errorf("devnet host not tracked: %v", inv.Hosts)
	}

	// Bare-array form parses identically to the List envelope form.
	bare := `[{"metadata":{"namespace":"n","name":"x"},"spec":{"authentication":{"universalAuth":{"secretsScope":{"projectSlug":"hanzo","envSlug":"prod","secretsPath":"/p","keys":["K"]}}}}}]`
	crs, err := parseCRs([]byte(bare))
	if err != nil || len(crs) != 1 {
		t.Fatalf("bare array parse: err=%v n=%d", err, len(crs))
	}
}
