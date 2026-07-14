package knowledge

import (
	"os"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/clients/framework"
)

// TestDocTypesValidate asserts every KB fixture is a well-formed DocType by the
// engine's OWN validator — the same gate the install path runs. A malformed fixture
// (bad fieldname, missing Link target, reserved name) would fail here, so this is the
// contract check that the lane installs cleanly into any org.
func TestDocTypesValidate(t *testing.T) {
	dts := DocTypes()
	if len(dts) != 5 {
		t.Fatalf("expected 5 KB doctypes, got %d", len(dts))
	}
	names := map[string]framework.DocType{}
	for _, dt := range dts {
		if dt.Module != Module {
			t.Errorf("doctype %q: module = %q, want %q", dt.Name, dt.Module, Module)
		}
		if err := dt.Validate(); err != nil {
			t.Errorf("doctype %q failed engine Validate: %v", dt.Name, err)
		}
		names[dt.Name] = dt
	}
	for _, want := range []string{DTPage, DTMemory, DTSource, DTConnector, DTLink} {
		if _, ok := names[want]; !ok {
			t.Errorf("missing expected doctype %q", want)
		}
	}
}

// TestLinkEdgeIsSourceLinkPlusTitle asserts the kb-link edge is a Link (source) +
// Data (target_title) reference — the persisted backlink form. source must be a Link
// to kb-page, and there is no stored resolved-target foreign key (targets resolve by
// value at graph read), and no knowledge text (so it is never vector-indexed).
func TestLinkEdgeIsSourceLinkPlusTitle(t *testing.T) {
	l := link()
	fields := map[string]framework.DocField{}
	for _, f := range l.Fields {
		fields[f.Fieldname] = f
	}
	src, ok := fields["source"]
	if !ok || src.Fieldtype != framework.FieldLink || src.Options != DTPage || !src.Reqd {
		t.Errorf("kb-link.source must be a required Link to %q, got %+v", DTPage, src)
	}
	tt, ok := fields["target_title"]
	if !ok || tt.Fieldtype != framework.FieldData || !tt.Reqd {
		t.Errorf("kb-link.target_title must be a required Data field, got %+v", tt)
	}
	// kb-link must NOT be in the indexed (vector) set — an edge carries no knowledge.
	for _, dt := range indexedDocTypes {
		if dt == DTLink {
			t.Errorf("kb-link must not be vector-indexed")
		}
	}
}

// TestPageHierarchyIsSelfLink verifies kb-page carries a `parent` Link back to
// kb-page — the self-reference that gives the Notion-like tree. The body must be the
// Lexical RichText fieldtype (so the console renders the rich editor) and the page
// is slug-named.
func TestPageHierarchyIsSelfLink(t *testing.T) {
	p := page()
	if p.Autoname != "field:slug" {
		t.Errorf("kb-page autoname = %q, want field:slug", p.Autoname)
	}
	var parent, body *framework.DocField
	for i := range p.Fields {
		switch p.Fields[i].Fieldname {
		case "parent":
			parent = &p.Fields[i]
		case "body":
			body = &p.Fields[i]
		}
	}
	if parent == nil || parent.Fieldtype != framework.FieldLink || parent.Options != DTPage {
		t.Errorf("kb-page.parent must be a Link to %q, got %+v", DTPage, parent)
	}
	if body == nil || body.Fieldtype != framework.FieldRichText {
		t.Errorf("kb-page.body must be RichText (Lexical), got %+v", body)
	}
}

// TestConnectorHasNoTokenField is the security invariant: the connection document
// must NOT carry the OAuth token — the retrievable secret belongs in KMS. It may
// carry only the KMS PATH (kms_ref) and non-secret metadata. A Password/secret field
// on this doctype would be a finding.
func TestConnectorHasNoTokenField(t *testing.T) {
	c := connector()
	for _, f := range c.Fields {
		nm := strings.ToLower(f.Fieldname)
		if strings.Contains(nm, "token") || strings.Contains(nm, "secret") || f.Fieldtype == framework.FieldPassword {
			t.Errorf("kb-connector must not store a token/secret; found field %q (%s)", f.Fieldname, f.Fieldtype)
		}
	}
	// It MUST carry the KMS ref path so the token is fetched from KMS.
	found := false
	for _, f := range c.Fields {
		if f.Fieldname == "kms_ref" {
			found = true
		}
	}
	if !found {
		t.Error("kb-connector must carry kms_ref (the KMS path of the token)")
	}
}

// TestPointIDDeterministicAndPerOrg proves the vector point id is stable for a given
// (org, doctype, name) — so a re-save overwrites and a trash deletes exactly it — and
// DIFFERS across orgs for the same doc, so two tenants' identically-named docs can
// never collide on a point even if a collection were shared. This underpins per-org
// isolation at the vector layer.
func TestPointIDDeterministicAndPerOrg(t *testing.T) {
	a1 := pointID("orgA", DTPage, "welcome")
	a2 := pointID("orgA", DTPage, "welcome")
	b1 := pointID("orgB", DTPage, "welcome")
	if a1 != a2 {
		t.Errorf("pointID not deterministic: %q != %q", a1, a2)
	}
	if a1 == b1 {
		t.Errorf("pointID collides across orgs for same doc: %q", a1)
	}
	// UUID-shaped (8-4-4-4-12).
	if parts := strings.Split(a1, "-"); len(parts) != 5 ||
		len(parts[0]) != 8 || len(parts[1]) != 4 || len(parts[2]) != 4 || len(parts[3]) != 4 || len(parts[4]) != 12 {
		t.Errorf("pointID not UUID-shaped: %q", a1)
	}
}

// TestCollectionPerOrg proves each org gets its OWN prefixed collection name and the
// org is used verbatim (case-preserving — folding would merge distinct tenants).
func TestCollectionPerOrg(t *testing.T) {
	x := &indexer{}
	if got := x.collection("acme"); got != "kb_acme" {
		t.Errorf("collection(acme) = %q, want kb_acme", got)
	}
	if x.collection("acme") == x.collection("ACME") {
		t.Error("collection must not fold case: acme and ACME must differ")
	}
	if strings.Contains(x.collection("a b"), " ") {
		t.Error("collection name must not contain a space")
	}
}

// TestLexicalText extracts prose from a Lexical EditorState so the embedding is over
// text, not editor JSON; a non-Lexical value degrades to identity (never dropped).
func TestLexicalText(t *testing.T) {
	lex := `{"root":{"type":"root","children":[` +
		`{"type":"heading","children":[{"type":"text","text":"Runbook"}]},` +
		`{"type":"paragraph","children":[{"type":"text","text":"Restart the pod."}]}]}}`
	got := lexicalText(lex)
	if !strings.Contains(got, "Runbook") || !strings.Contains(got, "Restart the pod.") {
		t.Errorf("lexicalText missed content: %q", got)
	}
	if strings.Contains(got, "\"type\"") {
		t.Errorf("lexicalText leaked editor JSON: %q", got)
	}
	// Non-Lexical (plain string) → identity.
	if got := lexicalText("just markdown"); got != "just markdown" {
		t.Errorf("non-Lexical should be identity, got %q", got)
	}
	if got := lexicalText(""); got != "" {
		t.Errorf("empty should stay empty, got %q", got)
	}
}

// TestDocText builds the embedding text per doctype (title + the right body field)
// and strips a Lexical page body.
func TestDocText(t *testing.T) {
	page := docText(DTPage, "Welcome", map[string]any{
		"body": `{"root":{"type":"root","children":[{"type":"paragraph","children":[{"type":"text","text":"hello team"}]}]}}`,
	})
	if !strings.Contains(page, "Welcome") || !strings.Contains(page, "hello team") {
		t.Errorf("page docText wrong: %q", page)
	}
	mem := docText(DTMemory, "note", map[string]any{"content": "the api key rotates monthly"})
	if !strings.Contains(mem, "the api key rotates monthly") {
		t.Errorf("memory docText wrong: %q", mem)
	}
	// Empty doc → empty text (nothing to index).
	if got := docText(DTPage, "", map[string]any{}); got != "" {
		t.Errorf("empty page should yield empty text, got %q", got)
	}
}

// TestDocMetaAlwaysCarriesOrg proves the vector payload always pins the org (the
// defense-in-depth filter key) and never leaks a secret.
func TestDocMetaAlwaysCarriesOrg(t *testing.T) {
	m := docMeta("orgA", DTSource, "n1", "GH README", map[string]any{
		"project": "p1", "provider": "github", "url": "https://x", "external_id": "e1",
	})
	if m["org"] != "orgA" {
		t.Errorf("payload missing org pin: %+v", m)
	}
	if m["doctype"] != DTSource || m["project"] != "p1" || m["provider"] != "github" {
		t.Errorf("payload metadata wrong: %+v", m)
	}
	if _, leaked := m["body"]; leaked {
		t.Error("payload must not copy the document body")
	}
}

// ---- OAuth state: the connector security core ----

// TestStateRoundTripBindsOrg proves a signed state recovers the exact org+provider
// it was minted for. This is the boundary that stops an attacker binding their
// provider account to a victim org — the callback trusts the SIGNED org, not a header.
func TestStateRoundTripBindsOrg(t *testing.T) {
	t.Setenv("KB_OAUTH_STATE_SECRET", "test-hmac-key-32-bytes-xxxxxxxxxx")
	st, err := signState("acme", "github")
	if err != nil {
		t.Fatalf("signState: %v", err)
	}
	org, err := verifyState(st, "github")
	if err != nil {
		t.Fatalf("verifyState: %v", err)
	}
	if org != "acme" {
		t.Errorf("state org = %q, want acme", org)
	}
}

// TestStateRejectsTamperAndCrossProvider proves the MAC catches tampering and the
// provider binding is enforced (a github state can't be replayed on the slack
// callback), and that a missing secret fails closed.
func TestStateRejectsTamperAndCrossProvider(t *testing.T) {
	t.Setenv("KB_OAUTH_STATE_SECRET", "test-hmac-key-32-bytes-xxxxxxxxxx")
	st, _ := signState("acme", "github")

	// Cross-provider replay must fail.
	if _, err := verifyState(st, "slack"); err == nil {
		t.Error("state signed for github must not verify for slack")
	}

	// Tampered payload must fail the MAC.
	tampered := "Z" + st[1:]
	if _, err := verifyState(tampered, "github"); err == nil {
		t.Error("tampered state must not verify")
	}

	// Forged state under the WRONG key must fail (the whole point of the HMAC).
	t.Setenv("KB_OAUTH_STATE_SECRET", "a-totally-different-key-yyyyyyyyyy")
	if _, err := verifyState(st, "github"); err == nil {
		t.Error("state must not verify under a different secret")
	}

	// No secret at all → fail closed (empty env).
	_ = os.Unsetenv("KB_OAUTH_STATE_SECRET")
	if _, err := signState("acme", "github"); err == nil {
		t.Error("signState must fail closed with no secret")
	}
}

// TestKMSRefPerOrg proves the token's KMS path embeds the org so one tenant's token
// is never at another's path (per-org secret isolation).
func TestKMSRefPerOrg(t *testing.T) {
	if kmsRef("acme", "github") == kmsRef("globex", "github") {
		t.Error("kmsRef must differ per org")
	}
	if !strings.Contains(kmsRef("acme", "github"), "acme") ||
		!strings.Contains(kmsRef("acme", "github"), "github") {
		t.Errorf("kmsRef must embed org+provider: %q", kmsRef("acme", "github"))
	}
}

// TestSanitizeDocTypes proves a client can only ever narrow a search to KB knowledge
// doctypes — a foreign doctype (another lane's) is dropped, never widening the query.
func TestSanitizeDocTypes(t *testing.T) {
	got := sanitizeDocTypes([]string{DTPage, "Sales Invoice", DTMemory, "hd-ticket"})
	if len(got) != 2 {
		t.Fatalf("expected 2 allowed doctypes, got %v", got)
	}
	for _, d := range got {
		if d != DTPage && d != DTMemory && d != DTSource {
			t.Errorf("leaked non-KB doctype: %q", d)
		}
	}
}

// TestProvidersClosedSet proves the connector provider set is closed (fail-closed on
// an unknown provider).
func TestProvidersClosedSet(t *testing.T) {
	for _, p := range []string{"github", "slack", "google"} {
		if !providers[p] {
			t.Errorf("expected provider %q supported", p)
		}
	}
	if providers["evil"] || providers[""] {
		t.Error("provider set must be closed")
	}
}
