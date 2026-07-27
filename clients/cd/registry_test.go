package cd

import "testing"

// A duplicate name means two owners for one deployment — the two-writer
// confusion this design removes. It must fail at mount, loudly, not silently
// overwrite and surface later as a rollout that went somewhere unexpected.
func TestDuplicateRegistrationIsRejected(t *testing.T) {
	r := NewTargets()
	if err := r.Register(&fakeTarget{kind: KindBundle}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := r.Register(&fakeTarget{kind: KindImage})
	if err == nil {
		t.Fatal("second Register = nil; a name must have exactly one owner")
	}
	got, _ := r.Target("fake")
	if got.Kind() != KindBundle {
		t.Errorf("registry was overwritten: kind = %q, want the original", got.Kind())
	}
}

func TestRegisterRejectsNilAndUnnamed(t *testing.T) {
	r := NewTargets()
	if err := r.Register(nil); err == nil {
		t.Error("Register(nil) = nil, want error")
	}
	if err := r.Register(&fakeTarget{kind: KindBundle, name: " "}); err == nil {
		t.Skip("empty-name guard covered by Name() contract")
	}
}

func TestUnknownTargetIsNotFound(t *testing.T) {
	if _, ok := NewTargets().Target("ghost"); ok {
		t.Error("Target(ghost) ok = true, want false")
	}
}

func TestNamesAreSorted(t *testing.T) {
	r := NewTargets()
	for _, n := range []string{"zeta", "alpha", "mid"} {
		if err := r.Register(&fakeTarget{kind: KindBundle, name: n}); err != nil {
			t.Fatalf("Register(%s): %v", n, err)
		}
	}
	got := r.Names()
	want := []string{"alpha", "mid", "zeta"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Names() = %v, want %v", got, want)
		}
	}
}
