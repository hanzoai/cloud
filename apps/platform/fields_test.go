package platform

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/escrow"
	"github.com/hanzoai/cloud/internal/kmstest"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk/tenure"
	"github.com/hanzoai/namespace"
)

// raw reads a column as the database holds it, past the store.
func raw(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	var v string
	if err := s.db.QueryRow(query, args...).Scan(&v); err != nil {
		t.Fatalf("read %q: %v", query, err)
	}
	return v
}

// freshStore is a store over its own KMS, which a test reads what the store keeps from.
func freshStore(t *testing.T) (*Store, *kmstest.Mem) {
	t.Helper()
	k := kmstest.New()
	s, err := openStore(t.TempDir(), testMaster, k)
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, k
}

const envSecret = `[{"name":"DATABASE_URL","value":"postgres://app:hunter2-s3cr3t@db.internal:5432/app"}]`

// What a customer's application runs with, and the challenge that proves a host is the
// org's, are KMS values: the rows hold the path of each and no byte of either, a read
// asks KMS, an update writes the new environment to the same path, and a delete asks KMS
// to forget both.
func TestEnvironmentAndChallengeLiveInKMS(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s, k := freshStore(t)
		a := mkApp("acme", "web", "api")
		a.EnvJSON = envSecret
		bare := mkApp("acme", "web", "bare") // an environment of "[]" is kept nowhere
		for _, x := range []Application{a, bare} {
			if err := s.CreateApplication(ctx, x); err != nil {
				t.Fatal(err)
			}
		}
		d := Domain{Host: "app.acme.example", Org: "acme", ProjectID: "web", AppID: a.ID, AppSlug: "api", Status: "pending", Token: "hanzo-verify=4b1e9f0c2a77d3", CreatedAt: 5}
		if err := s.CreateDomain(ctx, d); err != nil {
			t.Fatal(err)
		}

		envRef, tokRef := "orgs/acme/platform/"+a.ID+"/env_json@default", "orgs/acme/platform/app.acme.example/token@default"
		if env := raw(t, s, `SELECT env_json FROM platform_apps WHERE id = $1`, a.ID); env != "kms:"+envRef {
			t.Fatalf("env_json holds %.60q, want its KMS path", env)
		}
		if env := raw(t, s, `SELECT env_json FROM platform_apps WHERE id = $1`, bare.ID); env != "[]" {
			t.Fatalf("an empty environment is stored as %q", env)
		}
		if tok := raw(t, s, `SELECT token FROM platform_domains WHERE host = $1`, d.Host); tok != "kms:"+tokRef {
			t.Fatalf("token holds %.60q, want its KMS path", tok)
		}
		if row := raw(t, s, `SELECT env_json || '|' || repo_url || '|' || domains_json FROM platform_apps WHERE id = $1`, a.ID); strings.Contains(row, "hunter2") || strings.Contains(row, "DATABASE_URL") {
			t.Fatalf("the row holds an environment byte: %q", row)
		}
		if held := k.Held(); len(held) != 2 || held[envRef] != envSecret || held[tokRef] != d.Token {
			t.Fatalf("KMS holds %v, want the environment and the token at their paths", held)
		}
		// What a predicate reads stays readable.
		if raw(t, s, `SELECT repo_url FROM platform_apps WHERE id = $1`, a.ID) != a.RepoURL {
			t.Error("repo_url is not plain")
		}

		for name, read := range map[string]func() (Application, error){
			"GetApplication":     func() (Application, error) { return s.GetApplication(ctx, "acme", "web", "api") },
			"GetApplicationByID": func() (Application, error) { return s.GetApplicationByID(ctx, "acme", a.ID) },
		} {
			if got, err := read(); err != nil || got.EnvJSON != envSecret {
				t.Fatalf("%s = %q, %v", name, got.EnvJSON, err)
			}
		}
		if list, err := s.ListApplications(ctx, "acme", "web"); err != nil || len(list) != 2 {
			t.Fatalf("ListApplications = %+v, %v", list, err)
		} else {
			for _, x := range list {
				if (x.ID == a.ID) != (x.EnvJSON == envSecret) || (x.ID == bare.ID && x.EnvJSON != "[]") {
					t.Fatalf("ListApplications carries %q for %s", x.EnvJSON, x.ID)
				}
			}
		}
		// A sweep across every org names the environment and reads nothing.
		reads := k.Reads()
		if all, err := s.AllApplications(ctx); err != nil || len(all) != 2 || all[0].EnvJSON != "" {
			t.Fatalf("AllApplications = %+v, %v", all, err)
		}
		if k.Reads() != reads {
			t.Fatal("a sweep across every org read KMS")
		}
		// An app read without its environment is written back with it untouched.
		all, _ := s.AllApplications(ctx)
		for _, x := range all {
			if x.ID == a.ID {
				x.Status = "live"
				if err := s.UpdateApplication(ctx, x); err != nil {
					t.Fatal(err)
				}
			}
		}
		if got, _ := s.GetApplicationByID(ctx, "acme", a.ID); got.EnvJSON != envSecret || got.Status != "live" {
			t.Fatalf("an update of an app read without its environment left %q, status %q", got.EnvJSON, got.Status)
		}
		a.Status = "live"
		a.EnvJSON = `[{"name":"MODE","value":"prod"}]`
		if err := s.UpdateApplication(ctx, a); err != nil {
			t.Fatal(err)
		}
		if env := raw(t, s, `SELECT env_json FROM platform_apps WHERE id = $1`, a.ID); env != "kms:"+envRef || k.Held()[envRef] != a.EnvJSON {
			t.Fatalf("after an update env_json is %.60q and KMS holds %q", env, k.Held()[envRef])
		}
		if running, err := s.RunningApps(ctx); err != nil || len(running) != 1 || running[0].App.EnvJSON != "" {
			t.Fatalf("RunningApps = %+v, %v", running, err)
		}

		// The uniqueness probe names a host's token and reads none.
		reads = k.Reads()
		if got, ok, err := s.LookupDomain(ctx, d.Host); err != nil || !ok || got.Token != "" || got.Org != "acme" {
			t.Fatalf("LookupDomain = %+v, %v, %v", got, ok, err)
		}
		if k.Reads() != reads {
			t.Fatal("the uniqueness probe read KMS")
		}
		if got, err := s.GetDomain(ctx, "acme", a.ID, d.Host); err != nil || got.Token != d.Token {
			t.Fatalf("GetDomain = %+v, %v", got, err)
		}
		if list, err := s.ListDomainsByApp(ctx, "acme", a.ID); err != nil || len(list) != 1 || list[0].Token != d.Token {
			t.Fatalf("ListDomainsByApp = %+v, %v", list, err)
		}

		if ok, err := s.DeleteDomain(ctx, "acme", a.ID, d.Host); err != nil || !ok {
			t.Fatalf("DeleteDomain = %v, %v", ok, err)
		}
		if _, ok := k.Held()[tokRef]; ok {
			t.Fatal("a released domain left its token in KMS")
		}
		if _, ok, err := s.DeleteApplication(ctx, "acme", "web", "api"); err != nil || !ok {
			t.Fatalf("DeleteApplication = %v, %v", ok, err)
		}
		if refs := k.Refs(); len(refs) != 0 {
			t.Fatalf("a deleted app left %v in KMS", refs)
		}
	})
}

// A reference names its own app's environment: a column pointed at another org's path is
// refused by KMS, and one that names nothing KMS holds is a dangling reference, not an
// empty environment.
func TestAnEnvironmentReferenceReadsOnlyItsOwnValue(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s, _ := freshStore(t)
		a, g := mkApp("acme", "web", "a"), mkApp("globex", "web", "g")
		a.EnvJSON, g.EnvJSON = envSecret, `[{"name":"G","value":"globex"}]`
		for _, x := range []Application{a, g} {
			if err := s.CreateApplication(ctx, x); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE platform_apps SET env_json = $1 WHERE id = $2`,
			raw(t, s, `SELECT env_json FROM platform_apps WHERE id = $1`, g.ID), a.ID); err != nil {
			t.Fatal(err)
		}
		if got, err := s.GetApplicationByID(ctx, "acme", a.ID); err == nil || got.EnvJSON != "" {
			t.Fatalf("acme's app read globex's environment: %q, %v", got.EnvJSON, err)
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE platform_apps SET env_json = 'kms:orgs/acme/platform/`+a.ID+`/nothing@default' WHERE id = $1`, a.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetApplicationByID(ctx, "acme", a.ID); !errors.Is(err, escrow.ErrGone) {
			t.Fatalf("a dangling reference read as %v, want ErrGone", err)
		}
	})
}

// With no KMS an environment is neither kept nor read, and the app is not written.
func TestNoKMSKeepsNoEnvironment(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		s, err := openStore(t.TempDir(), testMaster, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		a := mkApp("acme", "web", "api")
		a.EnvJSON = envSecret
		if err := s.CreateApplication(context.Background(), a); !errors.Is(err, escrow.ErrNoKMS) {
			t.Fatalf("CreateApplication with no KMS = %v, want ErrNoKMS", err)
		}
		if n := raw(t, s, `SELECT CAST(COUNT(*) AS TEXT) FROM platform_apps`); n != "0" {
			t.Fatalf("a refused create left %s rows", n)
		}
	})
}

// A file written before KMS holds the environment and the challenge in plaintext or sealed
// as a field. The open writes each to KMS and replaces the column with its path, once.
// Built by hand on both engines: the rows are the old shapes in the current table.
func TestALegacyRowMovesToKMSOnce(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s, k := freshStore(t)
		c, err := tenure.NewCipher(testMaster)
		if err != nil {
			t.Fatal(err)
		}
		f, err := c.Fields(namespace.System(), subsystem)
		if err != nil {
			t.Fatal(err)
		}
		plain, sealed := mkApp("acme", "web", "plain"), mkApp("acme", "web", "sealed")
		for _, x := range []Application{plain, sealed} {
			if err := s.CreateApplication(ctx, x); err != nil {
				t.Fatal(err)
			}
		}
		r := f.Row("platform_apps", "acme", sealed.ID)
		sealedEnv := r.Seal("env_json", `[{"name":"S","value":"sealed-env"}]`)
		rd := f.Row("platform_domains", "acme", "sealed.acme.example")
		sealedTok := rd.Seal("token", "sealed-challenge")
		if r.Err() != nil || rd.Err() != nil {
			t.Fatal(r.Err(), rd.Err())
		}
		for _, q := range []struct {
			sql  string
			args []any
		}{
			{`UPDATE platform_apps SET env_json = $1 WHERE id = $2`, []any{`[{"name":"K","value":"legacy-env"}]`, plain.ID}},
			{`UPDATE platform_apps SET env_json = $1 WHERE id = $2`, []any{sealedEnv, sealed.ID}},
			{`INSERT INTO platform_domains (host, org, project_id, app_id, app_slug, status, token, created_at) VALUES ('old.acme.example', 'acme', 'web', $1, 'plain', 'pending', 'legacy-challenge', 1)`, []any{plain.ID}},
			{`INSERT INTO platform_domains (host, org, project_id, app_id, app_slug, status, token, created_at) VALUES ('sealed.acme.example', 'acme', 'web', $1, 'sealed', 'pending', $2, 1)`, []any{sealed.ID, sealedTok}},
		} {
			if _, err := s.db.ExecContext(ctx, q.sql, q.args...); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.moveToKMS(ctx); err != nil {
			t.Fatalf("move: %v", err)
		}
		for id, want := range map[string]string{plain.ID: `[{"name":"K","value":"legacy-env"}]`, sealed.ID: `[{"name":"S","value":"sealed-env"}]`} {
			if v := raw(t, s, `SELECT env_json FROM platform_apps WHERE id = $1`, id); v != "kms:orgs/acme/platform/"+id+"/env_json@default" || strings.Contains(v, "legacy-env") || strings.Contains(v, "sealed-env") {
				t.Fatalf("%s: env_json holds %.60q, want its KMS path", id, v)
			}
			if got, err := s.GetApplicationByID(ctx, "acme", id); err != nil || got.EnvJSON != want {
				t.Fatalf("%s after the move: %q, %v, want %q", id, got.EnvJSON, err, want)
			}
		}
		for host, want := range map[string]string{"old.acme.example": "legacy-challenge", "sealed.acme.example": "sealed-challenge"} {
			if v := raw(t, s, `SELECT token FROM platform_domains WHERE host = $1`, host); v != "kms:orgs/acme/platform/"+host+"/token@default" {
				t.Fatalf("%s: token holds %.60q, want its KMS path", host, v)
			}
			d, ok, err := s.LookupDomain(ctx, host)
			if err != nil || !ok || s.revealToken(ctx, &d) != nil || d.Token != want {
				t.Fatalf("%s after the move: %+v, %v, %v", host, d, ok, err)
			}
		}
		before, reads := k.Held(), k.Reads()
		cols := raw(t, s, `SELECT env_json || token FROM platform_apps a, platform_domains d WHERE a.id = d.app_id AND d.host = 'old.acme.example'`)
		if err := s.migrate(); err != nil {
			t.Fatal(err)
		}
		if after := k.Held(); len(after) != len(before) || k.Reads() != reads {
			t.Fatalf("a second open touched KMS: %d values, then %d; reads %d then %d", len(before), len(after), reads, k.Reads())
		}
		if cols != raw(t, s, `SELECT env_json || token FROM platform_apps a, platform_domains d WHERE a.id = d.app_id AND d.host = 'old.acme.example'`) {
			t.Fatal("a second open rewrote a column")
		}
	})
}
