package bot

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/kms"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// vaulted mounts the surface with a real KMS behind it — the same sealed store
// a deployment uses, not a stand-in — so the tests that follow prove where a
// credential actually landed rather than that a fake was called.
func vaulted(t *testing.T) (*zip.App, *kms.Client) {
	t.Helper()
	vault, err := kms.New(kms.Config{
		DataDir:      t.TempDir(),
		MasterKeyB64: base64.StdEncoding.EncodeToString(make([]byte, 32)),
	}, luxlog.NewWriter(io.Discard))
	if err != nil {
		t.Fatalf("kms.New: %v", err)
	}
	return mount(t, func(d *cloud.Deps) { d.KMS = vault }), vault
}

// stored returns one skill's document exactly as it sits in the org's file.
// Reading the raw text is the point: a test that decodes into skillSettings
// could not see a secret the handler wrote into a field it forgot about.
func stored(t *testing.T, org, key string) string {
	t.Helper()
	s := mounted.Load()
	if s == nil {
		t.Fatal("the surface is not mounted")
	}
	st, err := s.State.stores.For(org, "")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	docs, err := st.List(context.Background(), "skills", 0, 0)
	if err != nil {
		t.Fatalf("list skills: %v", err)
	}
	for _, d := range docs {
		if d.ID == key {
			return string(d.Doc)
		}
	}
	return ""
}

// heldSecret returns what KMS holds at one coordinate, and whether anything is there.
func heldSecret(t *testing.T, vault *kms.Client, ref string) (string, bool) {
	t.Helper()
	b, err := vault.GetSecret(context.Background(), ref)
	if err != nil {
		return "", false
	}
	return string(b), true
}

func boss(org string) who { return who{org: org, admin: true} }

// ── the browser build catalog ────────────────────────────────────────────────

// The revision is the content address of the catalog, so reloading a catalog
// that did not change answers with the revision it answered with before. A
// revision minted per call would pass nothing here.
func TestReloadIsContentAddressed(t *testing.T) {
	app := mount(t)

	first := payload(t, second(ask(t, app, boss("acme"), "1:a", "plugins.controlUi.reload", `{}`)))
	again := payload(t, second(ask(t, app, boss("acme"), "2:a", "plugins.controlUi.reload", `{}`)))

	rev, _ := first["revision"].(string)
	if rev == "" {
		t.Fatalf("the catalog names no revision: %v", first)
	}
	if again["revision"] != rev {
		t.Errorf("a catalog that did not change answered %v then %v", rev, again["revision"])
	}
	for _, list := range []string{"plugins", "diagnostics"} {
		got, ok := first[list].([]any)
		if !ok {
			t.Errorf("%s is %v, want a list — a missing list is not an empty one", list, first[list])
			continue
		}
		if len(got) != 0 {
			t.Errorf("this cloud builds no browser bundles, yet %s carries %d: %v", list, len(got), got)
		}
	}
}

// A reload names a revision to every connection of the org, which is how a
// browser learns to re-read the catalog it is not holding open.
func TestReloadTellsEveryConnection(t *testing.T) {
	url := serve(t)
	watcher := dial(t, url, boss("acme"))
	if frame := say(t, watcher, "1:a", "connect", ""); frame["ok"] != true {
		t.Fatalf("connect: %v", frame)
	}

	admin := dial(t, url, boss("acme"))
	if frame := say(t, admin, "1:b", "connect", ""); frame["ok"] != true {
		t.Fatalf("connect: %v", frame)
	}
	if frame := say(t, admin, "2:b", "plugins.controlUi.reload", `{}`); frame["ok"] != true && frame["type"] == "res" {
		t.Fatalf("reload: %v", frame)
	}

	frame := next(t, watcher)
	if frame["type"] != "event" || frame["event"] != "plugins.controlUi.changed" {
		t.Fatalf("a reload told the other connection %v", frame)
	}
	said, _ := frame["payload"].(map[string]any)
	if rev, _ := said["revision"].(string); rev == "" {
		t.Errorf("the event names no revision: %v", said)
	}
}

// Changing what the org runs belongs to an admin of the org.
func TestSkillsNeedAdmin(t *testing.T) {
	app := mount(t)
	for _, method := range []string{"plugins.controlUi.reload", "skills.update"} {
		_, frame := ask(t, app, who{org: "acme"}, "1:a", method, `{"skillKey":"maps","enabled":true}`)
		if frame["ok"] != false {
			t.Fatalf("%s ran for a member of the org: %v", method, frame)
		}
		details, _ := wrong(t, frame)["details"].(map[string]any)
		if details["missingScope"] != string(Admin) {
			t.Errorf("%s named %v as the missing scope, want %s", method, details["missingScope"], Admin)
		}
	}
}

// The settings a caller states are the settings that come back, and they are in
// the org's file afterwards rather than in the answer alone.
func TestSkillSettingsPersist(t *testing.T) {
	app := mount(t)
	_, frame := ask(t, app, boss("acme"), "1:a", "skills.update", `{"skillKey":"search","enabled":true}`)
	if frame["ok"] != true {
		t.Fatalf("skills.update: %v", frame)
	}
	body := payload(t, frame)
	if body["skillKey"] != "search" {
		t.Errorf("the answer is for %v, not the skill that was asked for", body["skillKey"])
	}
	config, _ := body["config"].(map[string]any)
	if config["enabled"] != true {
		t.Errorf("the answer reports enabled as %v", config["enabled"])
	}
	if doc := stored(t, "acme", "search"); !strings.Contains(doc, `"enabled":true`) {
		t.Errorf("the setting is not in the org's file: %q", doc)
	}

	if _, frame = ask(t, app, boss("acme"), "2:a", "skills.update", `{"skillKey":"search","enabled":false}`); frame["ok"] != true {
		t.Fatalf("skills.update: %v", frame)
	}
	if doc := stored(t, "acme", "search"); !strings.Contains(doc, `"enabled":false`) {
		t.Errorf("turning a skill off did not land: %q", doc)
	}
}

// A credential is sealed into KMS and never written to the org's file, and it
// comes back as the redaction mark. A handler that stored the key in the
// document would answer the same and fail here.
func TestCredentialIsSealedNotStored(t *testing.T) {
	app, vault := vaulted(t)

	// A pasted key carrying the line break that a copy left in it.
	_, frame := ask(t, app, boss("acme"), "1:a", "skills.update",
		`{"skillKey":"search","apiKey":"  sk-live\r\nsecret  "}`)
	if frame["ok"] != true {
		t.Fatalf("skills.update: %v", frame)
	}
	config, _ := payload(t, frame)["config"].(map[string]any)
	if config["apiKey"] != redactedMark {
		t.Errorf("the answer carries %v where the redaction mark belongs", config["apiKey"])
	}

	doc := stored(t, "acme", "search")
	if strings.Contains(doc, "sk-live") || strings.Contains(doc, "secret") {
		t.Fatalf("the credential was written to the org's file: %q", doc)
	}

	ref := "orgs/acme/bot/skills/search/apiKey"
	got, ok := heldSecret(t, vault, ref)
	if !ok {
		t.Fatalf("nothing is sealed at %s", ref)
	}
	if got != "sk-livesecret" {
		t.Errorf("KMS holds %q; the line break should be gone and the ends trimmed", got)
	}
}

// The three rules a credential lives by, one after the other: the mark means
// keep what is stored, and a blank clears it.
func TestRedactionMarkKeepsTheStoredCredential(t *testing.T) {
	app, vault := vaulted(t)
	ref := "orgs/acme/bot/skills/search/apiKey"

	if _, frame := ask(t, app, boss("acme"), "1:a", "skills.update",
		`{"skillKey":"search","apiKey":"sk-one"}`); frame["ok"] != true {
		t.Fatalf("skills.update: %v", frame)
	}

	// The client echoes back what it was shown. That is not a new value.
	_, frame := ask(t, app, boss("acme"), "2:a", "skills.update",
		`{"skillKey":"search","apiKey":"`+redactedMark+`"}`)
	if frame["ok"] != true {
		t.Fatalf("skills.update: %v", frame)
	}
	if got, _ := heldSecret(t, vault, ref); got != "sk-one" {
		t.Fatalf("echoing the redaction mark overwrote the credential with %q", got)
	}
	config, _ := payload(t, frame)["config"].(map[string]any)
	if config["apiKey"] != redactedMark {
		t.Errorf("the answer no longer reports a credential: %v", config)
	}

	// A blank clears it.
	_, frame = ask(t, app, boss("acme"), "3:a", "skills.update", `{"skillKey":"search","apiKey":""}`)
	if frame["ok"] != true {
		t.Fatalf("skills.update: %v", frame)
	}
	if got, _ := heldSecret(t, vault, ref); got != "" {
		t.Errorf("clearing the credential left %q sealed", got)
	}
	config, _ = payload(t, frame)["config"].(map[string]any)
	if _, still := config["apiKey"]; still {
		t.Errorf("a cleared credential is still reported: %v", config)
	}
}

// An environment value whose name reads as a secret takes the credential's
// path; everything else is plain settings. The answer shows the difference and
// the file carries only one of them.
func TestSecretEnvIsSealedAndPlainEnvIsNot(t *testing.T) {
	app, vault := vaulted(t)

	_, frame := ask(t, app, boss("acme"), "1:a", "skills.update",
		`{"skillKey":"search","env":{"API_TOKEN":"t-1","REGION":"us-west","MAX_TOKENS":"512"}}`)
	if frame["ok"] != true {
		t.Fatalf("skills.update: %v", frame)
	}
	env, _ := payload(t, frame)["config"].(map[string]any)["env"].(map[string]any)
	if env["API_TOKEN"] != redactedMark {
		t.Errorf("a token came back as %v", env["API_TOKEN"])
	}
	if env["REGION"] != "us-west" {
		t.Errorf("a plain setting came back as %v", env["REGION"])
	}
	if env["MAX_TOKENS"] != "512" {
		t.Errorf("a token budget is a number, not a token, yet it came back as %v", env["MAX_TOKENS"])
	}

	doc := stored(t, "acme", "search")
	if strings.Contains(doc, "t-1") {
		t.Errorf("the token was written to the org's file: %q", doc)
	}
	if !strings.Contains(doc, "us-west") {
		t.Errorf("the plain setting is not in the org's file: %q", doc)
	}
	if got, ok := heldSecret(t, vault, "orgs/acme/bot/skills/search/env/API_TOKEN"); !ok || got != "t-1" {
		t.Errorf("KMS holds %q (present=%v) for the token", got, ok)
	}
}

// Sealing needs somewhere to seal into. A deployment with no secret store
// refuses the credential rather than writing it where it can be read.
func TestACredentialNeedsASecretStore(t *testing.T) {
	app := mount(t) // no KMS in these deps
	_, frame := ask(t, app, boss("acme"), "1:a", "skills.update", `{"skillKey":"search","apiKey":"sk-one"}`)
	if frame["ok"] != false {
		t.Fatalf("a credential was accepted with nowhere to seal it: %v", frame)
	}
	if code := wrong(t, frame)["code"]; code != "UNAVAILABLE" {
		t.Errorf("answered %v, want UNAVAILABLE", code)
	}
	if doc := stored(t, "acme", "search"); strings.Contains(doc, "sk-one") {
		t.Fatalf("the refused credential was written anyway: %q", doc)
	}
}

// A skill key and an environment name become path segments — a document id, and
// a coordinate in KMS — so a shape that is not one is refused before either is
// built from it.
func TestSkillNamesAreChecked(t *testing.T) {
	app, _ := vaulted(t)
	for _, params := range []string{
		`{"skillKey":"../../etc","enabled":true}`,
		`{"skillKey":"","enabled":true}`,
		`{"skillKey":"search","env":{"../escape":"x"}}`,
	} {
		_, frame := ask(t, app, boss("acme"), "1:a", "skills.update", params)
		if frame["ok"] != false {
			t.Errorf("%s was accepted", params)
			continue
		}
		if code := wrong(t, frame)["code"]; code != "INVALID_REQUEST" {
			t.Errorf("%s answered %v, want INVALID_REQUEST", params, code)
		}
	}
}

// The other branch of the parameter union installs skill packages from a
// third-party index. Nothing here installs packages, and saying so is better
// than answering as though something was refreshed.
func TestNoSkillPackageSource(t *testing.T) {
	app := mount(t)
	_, frame := ask(t, app, boss("acme"), "1:a", "skills.update", `{"source":"clawhub","all":true}`)
	if frame["ok"] != false {
		t.Fatalf("a package refresh reported success: %v", frame)
	}
	if code := wrong(t, frame)["code"]; code != "INVALID_REQUEST" {
		t.Errorf("answered %v, want INVALID_REQUEST", code)
	}
}

// Every method this family adds is advertised, because the web UI hides a
// control it cannot see a method for and would otherwise never call one.
func TestTheFamilyIsAdvertised(t *testing.T) {
	app := mount(t)
	_, frame := ask(t, app, boss("acme"), "1:a", "connect", "")
	features, _ := payload(t, frame)["features"].(map[string]any)
	listed := map[string]bool{}
	for _, m := range features["methods"].([]any) {
		listed[m.(string)] = true
	}
	for _, m := range []string{"plugins.controlUi.reload", "skills.update"} {
		if !listed[m] {
			t.Errorf("%s answers but is not advertised", m)
		}
	}
	// The four this family does not serve stay off the list. Each could only
	// refuse, and a control the UI renders from an advertised name is a control
	// that fails when it is pressed.
	for _, m := range []string{
		"plugins.controlUi.report", "plugins.setEnabled",
		"plugin.surface.refresh", "mcp.app.view",
	} {
		if listed[m] {
			t.Errorf("%s is advertised and cannot be answered", m)
		}
	}
	events := map[string]bool{}
	for _, e := range features["events"].([]any) {
		events[e.(string)] = true
	}
	if !events["plugins.controlUi.changed"] {
		t.Errorf("a reload raises an event the handshake does not name: %v", features["events"])
	}
}

// The classification of a name whose value must not be shown, as the protocol
// states it: the patterns, and the names that match them and mean something
// else.
func TestLooksSecret(t *testing.T) {
	for name, want := range map[string]bool{
		"API_TOKEN":       true,
		"apiKey":          true,
		"api_key":         true,
		"PASSWORD":        true,
		"clientSecret":    true,
		"privateKey":      true,
		"serviceAccount":  true,
		"maxTokens":       false,
		"tokenBudget":     false,
		"passwordFile":    false,
		"REGION":          false,
		"tokenizer":       false,
		"OPENCLAW_BRANCH": false,
	} {
		if got := looksSecret(name); got != want {
			t.Errorf("looksSecret(%q) = %v, want %v", name, got, want)
		}
	}
}

// second drops the transport status from an ask, for the calls whose status is
// never anything but 200.
func second(code int, frame map[string]any) map[string]any {
	if code != http.StatusOK {
		panic("the transport refused the frame")
	}
	return frame
}

// ── the tenant a reference names ─────────────────────────────────────────────

// A KMS reference names a tenant, and KMS shards its own files on that name
// (clients/kms.fileOrg reads the segment after orgs/). Every other part of the
// reference this file builds is shape-checked — a bot id and a skill key admit
// no '/' — so the org is the one component whose shape is not this package's to
// assume. IAM does not promise one: the identity boundary deliberately admits
// punctuation, folding it injectively downstream instead (cloud.OrgHasUnsafeRune),
// and the org CRUD that mints an org checks no charset at all.
//
// Unfolded, an org whose name spells the path below another org's bot builds
// the same reference string, and the second write replaces the first tenant's
// credential with one it chose. The first tenant keeps using it and cannot see
// that it changed.
func TestASealedCredentialIsNotSharedWithAnotherTenant(t *testing.T) {
	app, vault := vaulted(t)
	announced(t, app, "acme", "botalpha1")

	if _, frame := ask(t, app, who{org: "acme", admin: true, bot: "botalpha1"}, "1:a", "skills.update",
		`{"skillKey":"weather","apiKey":"SECRET-OF-ACME"}`); frame["ok"] != true {
		t.Fatalf("acme could not seal a credential for its own bot: %v", frame)
	}

	other := who{org: "acme/bots/botalpha1", admin: true}
	if _, frame := ask(t, app, other, "2:a", "skills.update",
		`{"skillKey":"weather","apiKey":"SECRET-OF-OTHER"}`); frame["ok"] != true {
		t.Fatalf("the second tenant was refused, so this proves nothing: %v", frame)
	}

	held, ok := heldSecret(t, vault, "orgs/acme/bots/botalpha1/bot/skills/weather/apiKey")
	if !ok {
		t.Fatal("acme's credential is not sealed where acme sealed it")
	}
	if held != "SECRET-OF-ACME" {
		t.Errorf("acme's sealed credential reads %q after another tenant wrote its own: "+
			"the two orgs address one record", held)
	}
}

// The fold is the identity on an ordinary org name, so no deployment's
// references move: a tenant that could read its secret yesterday reads the same
// one today.
func TestAnOrdinaryOrgsReferenceDoesNotMove(t *testing.T) {
	app, vault := vaulted(t)
	if _, frame := ask(t, app, boss("acme"), "1:a", "secrets.store.set",
		`{"name":"OPENAI_API_KEY","value":"sk-live","kind":"env"}`); frame["ok"] != true {
		t.Fatalf("set: %v", frame)
	}
	if held, ok := heldSecret(t, vault, "orgs/acme/bot/OPENAI_API_KEY"); !ok || held != "sk-live" {
		t.Errorf("acme's secret is at %q=%v, want it where it has always been", held, ok)
	}
}
