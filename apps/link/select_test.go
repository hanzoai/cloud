package link

import "testing"

func TestParseModelRef(t *testing.T) {
	cases := []struct {
		in       string
		model    string
		provider string
		profile  string
		pinned   bool
	}{
		{"gpt-4o", "gpt-4o", "", "", false},
		{"Opus@anthropic:work", "Opus", "anthropic", "work", true},
		{"Opus@anthropic", "Opus", "anthropic", "", true},
		{"Opus@", "Opus", "", "", false},
		{"  claude-3  ", "claude-3", "", "", false},
		{"m@openai:personal:2", "m", "openai", "personal:2", true}, // only first ':' splits
	}
	for _, c := range cases {
		model, sel := ParseModelRef(c.in)
		if model != c.model {
			t.Errorf("%q: model = %q, want %q", c.in, model, c.model)
		}
		if sel.Pinned != c.pinned {
			t.Errorf("%q: pinned = %v, want %v", c.in, sel.Pinned, c.pinned)
		}
		if sel.Account.Provider != c.provider || sel.Account.Profile != c.profile {
			t.Errorf("%q: account = %+v, want {%s %s}", c.in, sel.Account, c.provider, c.profile)
		}
	}
}

func TestSelectionFromPrecedence(t *testing.T) {
	// header beats model-ref beats session-pin
	model, sel := SelectionFrom("xai:team", "Opus@anthropic:work", "openai:home")
	if model != "Opus" || sel.Account.Provider != "xai" || sel.Account.Profile != "team" {
		t.Fatalf("header should win: model=%q sel=%+v", model, sel)
	}
	model, sel = SelectionFrom("", "Opus@anthropic:work", "openai:home")
	if model != "Opus" || sel.Account.Provider != "anthropic" || sel.Account.Profile != "work" {
		t.Fatalf("model-ref should win over session: %+v", sel)
	}
	model, sel = SelectionFrom("", "Opus", "openai:home")
	if model != "Opus" || sel.Account.Provider != "openai" || sel.Account.Profile != "home" {
		t.Fatalf("session pin should apply: %+v", sel)
	}
	_, sel = SelectionFrom("", "gpt-4o", "")
	if sel.Pinned {
		t.Fatalf("no selector should be unpinned (auto), got %+v", sel)
	}
}

func TestSelectionMatches(t *testing.T) {
	work := Link{Provider: "openai", Account: "work", Status: StatusLinked}
	personal := Link{Provider: "openai", Account: "personal", Status: StatusLinked}
	anthropic := Link{Provider: "anthropic", Account: "max", Status: StatusLinked}

	if !auto().matches(work) {
		t.Fatal("auto selection must match every account")
	}
	// pin provider+profile → only that account
	pin := Selection{Account: Account{"openai", "work"}, Pinned: true}
	if !pin.matches(work) || pin.matches(personal) || pin.matches(anthropic) {
		t.Fatal("pin openai:work must match only openai/work")
	}
	// pin provider, open profile → any account of that provider (the cycling set)
	prov := Selection{Account: Account{"openai", ""}, Pinned: true}
	if !prov.matches(work) || !prov.matches(personal) || prov.matches(anthropic) {
		t.Fatal("pin openai (open profile) must match all openai accounts, no others")
	}
}

func TestAccountString(t *testing.T) {
	if got := (Account{"openai", "work"}).String(); got != "openai:work" {
		t.Errorf("String = %q", got)
	}
	if got := (Account{"openai", ""}).String(); got != "openai" {
		t.Errorf("default-profile String = %q, want bare provider", got)
	}
	if got := (Account{"openai", "default"}).String(); got != "openai" {
		t.Errorf("explicit default profile should render bare: %q", got)
	}
}
