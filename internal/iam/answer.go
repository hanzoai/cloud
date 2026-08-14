// Package iam reads what IAM answers.
//
// IAM speaks TWO wire shapes on one base path, and every caller here has to read
// both. The legacy verbs answer the {status,msg,data} envelope; the typed nouns —
// /v1/iam/users/get among them — answer THE RESOURCE DIRECTLY, and their errors
// come back as {"status":404,"error":"…"} where `status` is a NUMBER, not the
// string "ok".
//
// Assuming the envelope breaks both halves, and the failure looks like anything
// except a parsing mistake: a bare resource parses with Status "" and is rejected
// as `iam status 200`, and an error body does not parse at all and is reported as
// `iam non-envelope response (400)`. Both wore the words of a business rule —
// "photo stored but the profile could not be updated" on one door, "no Hanzo
// account for <address> in this org" on the other — which is why each survived
// until somebody drove the call by hand.
//
// So: the HTTP status decides, and the body is read only for what it carries. A
// 2xx with no envelope IS the data; a non-2xx yields IAM's own message.
//
// It lives here because two clients ask it (apps/account, apps/team) and the
// question has one answer. Stating it twice is how one of them stays wrong after
// the other is fixed — which is exactly the state this package was extracted from.
package iam

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/http"
)

// envelope is the legacy verb shape.
type envelope struct {
	Status string          `json:"status"`
	Msg    string          `json:"msg"`
	Data   json.RawMessage `json:"data"`
}

// Answer returns the payload of one IAM response, or an error carrying IAM's own
// message. raw is the (size-bounded) body; it is never logged by this package —
// a user row carries an address, and a key mint carries a secret exactly once.
func Answer(status int, raw []byte) (json.RawMessage, error) {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return nil, fmt.Errorf("iam denied (%d)", status)
	}
	var env envelope
	enveloped := json.Unmarshal(raw, &env) == nil && env.Status != ""

	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("iam: %s", message(env, raw, status))
	}
	if enveloped {
		if env.Status != "ok" {
			return nil, fmt.Errorf("iam: %s", message(env, raw, status))
		}
		return env.Data, nil
	}
	// A 2xx that is not an envelope: the body is the resource.
	return json.RawMessage(raw), nil
}

// message is IAM's own words for a refusal, whichever shape carried them, and a
// bare status only when it said nothing.
func message(env envelope, raw []byte, status int) string {
	var alt struct {
		Error string `json:"error"`
		Msg   string `json:"msg"`
	}
	_ = json.Unmarshal(raw, &alt)
	return cmp.Or(env.Msg, alt.Error, alt.Msg, fmt.Sprintf("iam status %d", status))
}
