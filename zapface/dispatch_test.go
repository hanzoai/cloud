package zapface

import "testing"

// TestAProblemDocumentReachesTheCaller is what the git subsystem's hand-written ZAP
// procedures were kept for. A typed op answers RFC 9457 when the body will not
// decode — before its handler runs, so no envelope exists — and that document spells
// `status` as a number where the envelope spells it as a string. The bridge read the
// type failure as "this is not an envelope" and replaced the handler's sentence with
// a parse complaint, which named the bridge instead of the mistake the caller made.
func TestAProblemDocumentReachesTheCaller(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		ok               bool
	}{
		{"detail carries the sentence",
			`{"type":"about:blank","title":"Bad Request","status":400,"detail":"invalid body"}`,
			"invalid body", true},
		{"title stands in when detail is absent",
			`{"type":"about:blank","title":"Bad Request","status":400}`,
			"Bad Request", true},
		{"an envelope is not a problem document", `{"status":"error","msg":"nope"}`, "", false},
		{"neither is anything else", `<html>502</html>`, "", false},
		{"nor is an empty object", `{}`, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := problemDetail([]byte(tc.body))
			if ok != tc.ok || got != tc.want {
				t.Fatalf("problemDetail = (%q, %v), want (%q, %v)", got, ok, tc.want, tc.ok)
			}
		})
	}
}
