package iam

import (
	"strings"
	"testing"
)

// The bodies are the ones IAM actually sends, kept verbatim: a bare user row from
// the typed noun, an envelope from a legacy verb, and both error shapes. Reading
// one of them wrong is what left two endpoints broken in different ways for months.

func TestAnswerReadsBothWireShapes(t *testing.T) {
	for _, c := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"typed noun answers the resource", `{"owner":"acme","name":"dana","email":"dana@acme.com"}`, 200, `"name":"dana"`},
		{"legacy verb answers the envelope", `{"status":"ok","msg":"","data":{"owner":"acme","name":"dana"}}`, 200, `"name":"dana"`},
		{"envelope carrying a bare true", `{"status":"ok","msg":"","data":true}`, 200, `true`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := Answer(c.status, []byte(c.body))
			if err != nil {
				t.Fatalf("%s: %v", c.name, err)
			}
			if !strings.Contains(string(got), c.want) {
				t.Fatalf("payload %q does not carry %q", got, c.want)
			}
		})
	}
}

func TestAnswerSurfacesIAMsOwnWords(t *testing.T) {
	for _, c := range []struct {
		name, body string
		status     int
		want       string
	}{
		// The typed noun's refusal: `status` is a NUMBER, so an envelope decode
		// yields nothing and the message lives under `error`.
		{"typed refusal", `{"status":400,"error":"exactly one of name or email is required"}`, 400, "exactly one of name or email"},
		{"typed not found", `{"status":404,"error":"user acme/dana not found"}`, 404, "acme/dana not found"},
		{"legacy refusal", `{"status":"error","msg":"auth:Unauthorized operation","data":null}`, 200, "Unauthorized operation"},
		{"denial", `{"status":403,"error":"forbidden"}`, 403, "iam denied (403)"},
		{"silence", ``, 500, "iam status 500"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Answer(c.status, []byte(c.body))
			if err == nil {
				t.Fatalf("%s was read as success", c.name)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error %q does not carry %q", err, c.want)
			}
		})
	}
}

// The regression that made the invite unreadable even once IAM answered it: a
// bare resource has no `status` field, so an envelope-only reader saw Status ""
// and called a 200 a failure.
func TestAnswerDoesNotCallASuccessfulResourceAFailure(t *testing.T) {
	got, err := Answer(200, []byte(`{"owner":"acme","name":"dana","id":"c0ffee"}`))
	if err != nil {
		t.Fatalf("a 200 carrying the resource was reported as an error: %v", err)
	}
	if !strings.Contains(string(got), `"id":"c0ffee"`) {
		t.Fatalf("the resource was not returned whole: %s", got)
	}
}
