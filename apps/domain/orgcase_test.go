package domain

import "testing"

// Two orgs whose names differ only in case hold nothing of each other's: a
// domain one holds is neither listed for nor found by the other. (The deployment's
// store is held to the same rule in store_test.go.)
func TestOrgsThatDifferInCaseHoldApart(t *testing.T) {
	s := NewMemStore()
	if err := s.Put(Holding{Org: "acme", Domain: "Example.COM"}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := s.Get("Acme", "example.com"); ok {
		t.Fatal("Acme finds acme's domain")
	}
	if got, _ := s.ListByOrg("Acme"); len(got) != 0 {
		t.Fatalf("Acme lists %d of acme's domains", len(got))
	}
	if _, ok, _ := s.Get("acme", "EXAMPLE.com"); !ok {
		t.Fatal("acme does not find its own domain under another case of it")
	}
}
