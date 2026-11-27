package tools

import (
	"context"
	"testing"

	// devmaster keys this test binary: cek opens nothing without a master and a
	// test process has no KMS.
	"github.com/hanzoai/cloud/internal/sqltest"
	_ "github.com/hanzoai/cloud/sdk/kit/master"
)

// A nil store is the pre-Mount / disabled case. It must be an EMPTY skill set,
// not a panic and not an error: the registry calls List on every provider for
// every request, so a provider that cannot answer has to degrade to silence.
func TestOrgSkillProviderNilStore(t *testing.T) {
	p := orgSkillProvider{}
	got, err := p.List(context.Background(), Scope{Org: "acme"})
	if err != nil {
		t.Fatalf("List with nil store: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List with nil store = %d tools, want 0", len(got))
	}
}

// No org means no scope to read, and reading every org's skills would be the
// worst possible failure mode here.
func TestOrgSkillProviderRefusesEmptyOrg(t *testing.T) {
	p := orgSkillProvider{store: &SkillStore{}}
	got, err := p.List(context.Background(), Scope{Org: ""})
	if err != nil {
		t.Fatalf("List with empty org: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List with empty org = %d tools, want 0", len(got))
	}
}

func TestOrgSkillProviderIsNotDispatchable(t *testing.T) {
	_, err := orgSkillProvider{}.Dispatch(context.Background(), Principal{}, "skill_x", nil)
	if err != ErrNotDispatchable {
		t.Errorf("Dispatch err = %v, want ErrNotDispatchable", err)
	}
}

// The store round-trip.
func TestSkillStoreRoundTripIsOrgScoped(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, err := OpenSkillStore(t.TempDir())
		if err != nil {
			t.Fatalf("OpenSkillStore: %v", err)
		}
		defer st.Close()
		ctx := context.Background()

		if _, err := st.Put(ctx, Skill{Org: "acme", Name: "invoicing", Description: "d", Content: "# Invoicing"}); err != nil {
			t.Fatalf("Put: %v", err)
		}
		if _, err := st.Put(ctx, Skill{Org: "other", Name: "secret", Content: "# Not yours"}); err != nil {
			t.Fatalf("Put other org: %v", err)
		}

		got, err := st.List(ctx, "acme")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 1 || got[0].Name != "invoicing" {
			t.Fatalf("List(acme) = %+v, want exactly the acme skill", got)
		}

		// Same name again REVISES rather than duplicating — two rows with one name
		// would collide in the registry's name-keyed dedup.
		if _, err := st.Put(ctx, Skill{Org: "acme", Name: "invoicing", Content: "# Invoicing v2"}); err != nil {
			t.Fatalf("Put revision: %v", err)
		}
		got, _ = st.List(ctx, "acme")
		if len(got) != 1 {
			t.Fatalf("after revision List = %d rows, want 1", len(got))
		}
		if got[0].Content != "# Invoicing v2" {
			t.Errorf("content = %q, want the revision", got[0].Content)
		}

		// Deleting another org's id must not touch it.
		if err := st.Delete(ctx, "acme", "secret"); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if other, _ := st.List(ctx, "other"); len(other) != 1 {
			t.Errorf("cross-org delete removed another org's skill: %+v", other)
		}
	})
}

func TestSkillStoreRefusesEmptyOrgAndName(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, err := OpenSkillStore(t.TempDir())
		if err != nil {
			t.Fatalf("OpenSkillStore: %v", err)
		}
		defer st.Close()
		if _, err := st.Put(context.Background(), Skill{Name: "x", Content: "c"}); err == nil {
			t.Error("Put with empty org succeeded; want refusal")
		}
		if _, err := st.Put(context.Background(), Skill{Org: "acme", Content: "c"}); err == nil {
			t.Error("Put with empty name succeeded; want refusal")
		}
	})
}

// A push takes a name over from another push, and never from an admin: the
// admin's skill keeps its content and stays admitted, and a push that later
// drops the name leaves it where it is.
func TestAPushDoesNotTakeOverAnAdminsSkill(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		ctx := context.Background()
		s, err := OpenSkillStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = s.Close() }()
		if _, err := s.Put(ctx, Skill{Org: "acme", Name: "deploy", Content: "# the admin's deploy", Admitted: true}); err != nil {
			t.Fatal(err)
		}
		if err := s.Replace(ctx, "acme", "acme/site", []Skill{{Name: "deploy", Content: "# a push's deploy"}, {Name: "triage", Content: "# site triage"}}); err != nil {
			t.Fatal(err)
		}
		if err := s.Replace(ctx, "acme", "acme/docs", []Skill{{Name: "triage", Content: "# docs triage"}}); err != nil {
			t.Fatal(err)
		}
		byName := func() map[string]Skill {
			t.Helper()
			all, err := s.List(ctx, "acme")
			if err != nil {
				t.Fatal(err)
			}
			m := map[string]Skill{}
			for _, sk := range all {
				m[sk.Name] = sk
			}
			return m
		}
		got := byName()
		if d := got["deploy"]; d.Content != "# the admin's deploy" || !d.Admitted || d.Source != "" {
			t.Fatalf("a push took over the admin's skill: %+v", d)
		}
		if tr := got["triage"]; tr.Content != "# docs triage" || tr.Admitted || tr.Source != "acme/docs" {
			t.Fatalf("a later push wins a pushed name: %+v", tr)
		}
		if err := s.Replace(ctx, "acme", "acme/site", nil); err != nil {
			t.Fatal(err)
		}
		if d := byName()["deploy"]; d.Content != "# the admin's deploy" || !d.Admitted {
			t.Fatalf("a push that dropped the name removed the admin's skill: %+v", d)
		}
	})
}

// A row read from a repository before blocks were read whole holds only ">";
// listing it reads the description again from the file it was read from.
func TestSkillStoreListReadsAStoredBlockHeaderFromTheFile(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, err := OpenSkillStore(t.TempDir())
		if err != nil {
			t.Fatalf("OpenSkillStore: %v", err)
		}
		defer st.Close()
		ctx := context.Background()
		md := "---\nname: security-audit\ndescription: >\n  Focused security audit of code.\n  Find vulnerabilities.\n---\n# Security audit\n"
		if err := st.Replace(ctx, "acme", "app/security-audit", []Skill{{Name: "security-audit", Description: ">", Content: md}}); err != nil {
			t.Fatalf("Replace: %v", err)
		}
		got, err := st.List(ctx, "acme")
		if err != nil || len(got) != 1 {
			t.Fatalf("List = %+v, %v", got, err)
		}
		if want := "Focused security audit of code. Find vulnerabilities."; got[0].Description != want {
			t.Fatalf("description = %q, want %q", got[0].Description, want)
		}
	})
}
