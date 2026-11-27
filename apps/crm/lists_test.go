package crm

import (
	"testing"
	"time"
)

func TestMembersAreFiledOnceWithTheirEvidence(t *testing.T) {
	ctx := open(t)
	// A person the team already named keeps the name.
	if err := upsert(ctx, org, "application", People, "", map[string]any{"id": personID("ada@example.com"),
		"emails": email("ada@example.com"), "name": name("Ada Lovelace")}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	ms := []Member{
		{List: "lst_a", ListName: "News", Email: "Ada@Example.com", Name: "A. King", Event: "confirmed", Status: "subscribed",
			Source: "page", Ref: "hanzo.ai", Consent: map[string]any{"basis": "explicit", "at": "2026-11-01T09:00:00Z"}, At: at},
		{List: "lst_a", ListName: "News", Email: "bo@example.com", Name: "Bo Bell", Event: "imported", Status: "subscribed",
			Source: "import", Ref: "imp_1", Consent: map[string]any{"basis": "unverified"}, At: at},
	}
	for range 2 {
		if err := FileMembers(ctx, org, ms); err != nil {
			t.Fatal(err)
		}
	}
	ada := must(t, ctx, People, personID("ada@example.com"))
	if n, _ := ada["name"].(map[string]any); n["firstName"] != "Ada" {
		t.Fatalf("a held name was overwritten: %v", ada["name"])
	}
	bo := must(t, ctx, People, personID("bo@example.com"))
	if bo["source"] != "import" {
		t.Fatalf("bo %v", bo)
	}
	list := listRecord("lst_a", "News")["id"].(string)
	must(t, ctx, Lists, list)
	if n := count(t, ctx, ListMembers, `list = "`+list+`"`); n != 2 {
		t.Fatalf("%d members", n)
	}
	if n := count(t, ctx, Activities, `channel = "list"`); n != 2 {
		t.Fatalf("%d membership activities after filing twice", n)
	}
	if PersonID(" ADA@example.com ") != personID("ada@example.com") {
		t.Fatal("PersonID folds the address")
	}
}
