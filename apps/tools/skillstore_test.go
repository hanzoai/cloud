package tools

import (
	"context"
	"path/filepath"
	"testing"
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

// The store round-trip. Needs an openable encrypted store, so it runs where the
// suite's peers' store tests run (Linux/CI) and fails for the same platform
// reason elsewhere — not a reason to leave the behaviour unpinned.
func TestSkillStoreRoundTripIsOrgScoped(t *testing.T) {
	st, err := OpenSkillStore(filepath.Join(t.TempDir(), "skills.db"))
	if err != nil {
		t.Skipf("OpenSkillStore: %v", err)
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
}

func TestSkillStoreRefusesEmptyOrgAndName(t *testing.T) {
	st, err := OpenSkillStore(filepath.Join(t.TempDir(), "skills.db"))
	if err != nil {
		t.Skipf("OpenSkillStore: %v", err)
	}
	defer st.Close()
	if _, err := st.Put(context.Background(), Skill{Name: "x", Content: "c"}); err == nil {
		t.Error("Put with empty org succeeded; want refusal")
	}
	if _, err := st.Put(context.Background(), Skill{Org: "acme", Content: "c"}); err == nil {
		t.Error("Put with empty name succeeded; want refusal")
	}
}
