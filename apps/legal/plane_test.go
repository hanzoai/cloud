package legal

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
)

func w9Fields() []client.LegalField {
	return []client.LegalField{
		{Key: "name", Value: "Globex Holdings"}, {Key: "business_name", Value: "none"},
		{Key: "classification", Value: "Individual/sole proprietor"}, {Key: "tin", Value: "***-**-6789"},
		{Key: "address", Value: "1 Market St, San Francisco, CA 94105, US"}, {Key: "profile", Value: "1"},
	}
}

// A peer's signature is legal's: the built-in text, the caller's own org, the
// same completion route and the same trail as a person's.
func TestPlaneSignsTheBuiltInForTheCaller(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		app, _ := mount(t)
		p := plane{mounted}
		globex := cloud.For(context.Background(), "globex")

		// An override of the template changes the org's own documents — never what a
		// peer asks to be signed.
		if code, _ := do(t, app, http.MethodPut, "/v1/legal/templates/w9-certification", "globex", map[string]any{
			"title": "W-9", "body": "I certify nothing. {{.name}}", "fields": []map[string]string{{"key": "name", "label": "Name"}},
		}); code != 200 {
			t.Fatalf("override = %d", code)
		}
		s, err := p.sign(globex, &client.LegalSignIn{Template: "w9-certification", Fields: w9Fields(),
			Signers: []client.LegalSigner{{Name: "Ada", Email: "ada@globex.test"}}})
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if s.Status != string(StatusOutForSig) || s.Template != "w9-certification" || len(s.Digest) != 64 {
			t.Fatalf("signing = %+v", s)
		}
		code, out := do(t, app, http.MethodGet, "/v1/legal/documents/"+s.ID, "globex", nil)
		body, _ := out["document"].(map[string]any)["body"].(string)
		if code != 200 || !strings.Contains(body, "Under penalties of perjury") || strings.Contains(body, "I certify nothing") {
			t.Fatalf("the peer's document is not the built-in text: %d %s", code, body)
		}

		// Completion is legal's own route, and the read reports it.
		if code, _ := do(t, app, http.MethodPost, "/v1/legal/documents/"+s.ID+"/sign/complete", "globex", map[string]any{"signed": true}); code != 200 {
			t.Fatalf("complete = %d", code)
		}
		got, err := p.document(globex, &client.LegalDocumentIn{ID: s.ID})
		if err != nil || got.Status != string(StatusSigned) || got.Signed == 0 || got.Digest != s.Digest {
			t.Fatalf("document = %+v %v", got, err)
		}

		// The tenant is the caller's: another org's id is not found, and no org is refused.
		if _, err := p.document(cloud.For(context.Background(), "acme"), &client.LegalDocumentIn{ID: s.ID}); err == nil {
			t.Fatal("another org read the document")
		}
		if _, err := p.sign(context.Background(), &client.LegalSignIn{Template: "w9-certification"}); err == nil {
			t.Fatal("a call with no org was answered")
		}
		// A missing field fails closed; a template that is not built in is not found.
		if _, err := p.sign(globex, &client.LegalSignIn{Template: "w9-certification", Fields: w9Fields()[:2],
			Signers: []client.LegalSigner{{Name: "Ada"}}}); err == nil || !strings.Contains(err.Error(), "missing") {
			t.Fatalf("missing fields = %v", err)
		}
		if _, err := p.sign(globex, &client.LegalSignIn{Template: "made-up", Signers: []client.LegalSigner{{Name: "Ada"}}}); err == nil {
			t.Fatal("an unknown template was signed")
		}
	})
}
