package webhooks

import "testing"

func TestSubjectMatches(t *testing.T) {
	cases := []struct {
		pattern string
		subject string
		want    bool
	}{
		// exact literals
		{"commerce.order.created", "commerce.order.created", true},
		{"commerce.order.created", "commerce.order.completed", false},
		{"commerce.order.created", "commerce.order", false},          // pattern longer
		{"commerce.order", "commerce.order.created", false},          // subject longer, no wildcard
		// single-token wildcard
		{"commerce.order.*", "commerce.order.created", true},
		{"commerce.order.*", "commerce.order.completed", true},
		{"commerce.order.*", "commerce.order.line.created", false},   // * is exactly one token
		{"commerce.*.created", "commerce.order.created", true},
		{"commerce.*.created", "commerce.order.updated", false},
		{"*.order.created", "commerce.order.created", true},
		// tail wildcard
		{"commerce.>", "commerce.order", true},
		{"commerce.>", "commerce.order.created", true},
		{"commerce.>", "commerce.order.line.created", true},
		{"commerce.>", "commerce", false},                            // > needs at least one tail token
		{"commerce.>", "iam.user.created", false},
		{">", "anything", true},
		{">", "a.b.c", true},
		// mixed
		{"commerce.order.*", "commerce.payment.received", false},
		{"commerce.*.*", "commerce.order.created", true},
		{"commerce.*.*", "commerce.order", false},
		// empties never match
		{"", "commerce.order.created", false},
		{"commerce.>", "", false},
	}
	for _, tc := range cases {
		if got := subjectMatches(tc.pattern, tc.subject); got != tc.want {
			t.Errorf("subjectMatches(%q, %q) = %v, want %v", tc.pattern, tc.subject, got, tc.want)
		}
	}
}

func TestEndpointMatches(t *testing.T) {
	// empty events list == subscribe to everything
	all := Endpoint{Events: nil}
	if !all.matches("commerce.order.created") || !all.matches("iam.user.deleted") {
		t.Fatal("empty events list must match every subject")
	}

	// explicit patterns: match if ANY matches
	e := Endpoint{Events: []string{"commerce.order.*", "iam.>"}}
	for _, subj := range []string{"commerce.order.created", "iam.user.created", "iam.org.member.added"} {
		if !e.matches(subj) {
			t.Errorf("endpoint should match %q", subj)
		}
	}
	for _, subj := range []string{"commerce.payment.received", "commerce.order.line.created", "world.tick"} {
		if e.matches(subj) {
			t.Errorf("endpoint should NOT match %q", subj)
		}
	}
}
