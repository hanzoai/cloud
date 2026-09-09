package bot

// The extend family: what a plugin and a skill add to the surface.
//
//	plugins.controlUi.reload   the browser builds, and their revision
//	skills.update              one skill's settings for the org
//
// skills.update persists a skill's settings, with every credential sealed into
// KMS and never into the org's file.
//
// plugins.controlUi.reload re-reads the browser builds. This cloud builds none:
// clients/plugin mounts a manifest entry as an http.Handler and has no notion
// of a plugin that ships JavaScript to the page. The catalog it answers is
// therefore empty, which is true and is what the plugins page renders as
// "nothing installed".
//
// plugins.controlUi.report, plugins.setEnabled, plugin.surface.refresh and
// mcp.app.view are not registered. A report is a receipt against a build in the
// catalog and the catalog is empty; setEnabled flips a policy bit on an
// installed plugin and none is installed for an org; a surface refresh rotates
// a capability URL, and a URL carrying its own authority is a credential, which
// here come from IAM; a view is a lease held by the tool call that made it, and
// nothing here holds one. Each could only refuse, so each is left off the
// advertised list and its control off the page.
//
// What a real one needs is the catalog first: an org-scoped installed set
// carrying each plugin's declared capability surface, so enabling can compare
// that surface against what the org already accepted. The rest follows it.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"sort"
	"strings"
)

func init() {
	Register("plugins.controlUi.reload", Admin, reloadUi)
	Register("skills.update", Admin, updateSkill)
	Declare("plugins.controlUi.changed")
}

// ── the browser build catalog ────────────────────────────────────────────────

// uiCatalog is what a browser reads to know which plugin modules to import, and
// at which revision. The revision is the content address of the two lists, so a
// reload that changed nothing answers with the revision it answered with
// before, and a client can tell "reloaded" from "changed" without diffing.
//
// This cloud builds no browser bundles: clients/plugin mounts a manifest entry
// as an http.Handler and has no notion of a plugin that ships JavaScript to the
// Control UI. So the catalog is empty, and honestly so. Filling it needs three
// things that do not exist yet — an org-scoped record of which plugins are
// installed, a build that produces each module and its styles, and an origin to
// serve those assets from that is not the RPC plane.
type uiCatalog struct {
	Revision    string     `json:"revision"`
	Plugins     []uiModule `json:"plugins"`
	Diagnostics []uiNote   `json:"diagnostics"`
}

// uiModule is one plugin's browser build: where to import it from, and the
// stylesheets to load with it.
type uiModule struct {
	PluginID string   `json:"pluginId"`
	Name     string   `json:"name"`
	Revision string   `json:"revision"`
	EntryURL string   `json:"entryUrl"`
	Styles   []string `json:"styles"`
}

// uiNote explains one plugin that has no usable build.
type uiNote struct {
	PluginID string `json:"pluginId"`
	Message  string `json:"message"`
	Code     string `json:"code,omitempty"`
}

// uiBuilds reads the catalog. Both lists are always present and never null: the
// client checks membership, and a missing list is not an empty one.
func uiBuilds() uiCatalog {
	c := uiCatalog{Plugins: []uiModule{}, Diagnostics: []uiNote{}}
	c.Revision = c.address()
	return c
}

// address is the content address of a catalog: sha-256 over its two lists,
// never over the revision itself.
func (c uiCatalog) address() string {
	b, err := json.Marshal(struct {
		Plugins     []uiModule `json:"plugins"`
		Diagnostics []uiNote   `json:"diagnostics"`
	}{c.Plugins, c.Diagnostics})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// holds reports whether the catalog carries a build for one plugin.
func (c uiCatalog) holds(id string) bool {
	for _, m := range c.Plugins {
		if m.PluginID == id {
			return true
		}
	}
	return false
}

type reloadParams struct {
	PluginID string `json:"pluginId"`
}

// reloadUi re-reads the browser builds and tells every connection of the org
// which revision is now current. It touches no backend code: a plugin's server
// half is mounted from the deployment manifest at boot and is not reachable
// from here.
func reloadUi(c *Call) (any, error) {
	var p reloadParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	cat := uiBuilds()
	if id := strings.TrimSpace(p.PluginID); id != "" && !cat.holds(id) {
		return nil, Invalid("no browser build for plugin %s", id)
	}
	Publish(c.Org(), c.Bot(), "", "plugins.controlUi.changed", map[string]any{"revision": cat.Revision})
	return cat, nil
}

// ── skills ───────────────────────────────────────────────────────────────────

// redactedMark stands for a value the server holds and will not send back. A
// client that echoes it is repeating what it was shown rather than setting
// anything, so it means keep what is stored.
const redactedMark = "__OPENCLAW_REDACTED__"

const (
	skillEnvMax   = 64   // entries in one env map
	skillValueMax = 4096 // bytes of one credential or env value
)

// skillRE bounds a skill key and an environment name. Both become path segments
// — a document id, and a coordinate in KMS — so the shape is checked before
// either is built from one.
var skillRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// skillSettings is one skill's stored settings. Secrets are not among them: Key
// records that a credential is sealed in KMS and Sealed names the environment
// entries whose values are, so the redacted answer can be built without reading
// a single secret back.
type skillSettings struct {
	Enabled *bool             `json:"enabled,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Sealed  []string          `json:"sealed,omitempty"`
	Key     bool              `json:"key,omitempty"`
}

type skillParams struct {
	SkillKey string            `json:"skillKey"`
	Enabled  *bool             `json:"enabled"`
	APIKey   *string           `json:"apiKey"`
	Env      map[string]string `json:"env"`
}

type sourceParams struct {
	Source string `json:"source"`
}

// updateSkill writes one skill's settings for the org: whether it runs, its
// environment, and its credential.
//
// The credential never reaches the org's file. It is sealed into KMS under the
// scope that made the call, and the settings record only that one exists, so a
// read of the store yields no secret to leak and the redacted answer needs no
// decryption.
//
// Three rules govern a credential, and implementing two of them corrupts one. A
// pasted key loses its line breaks and anything that cannot survive a header.
// The redaction mark means the client is echoing what it was shown, so the
// stored value stands. A blank clears it.
//
// The catalog of skills, and what running one means, belong to hanzoai/ai.
// These settings are the operator's stated intent for a skill, and this is
// where the intent is stated.
func updateSkill(c *Call) (any, error) {
	var which sourceParams
	if err := loose(c, &which); err != nil {
		return nil, err
	}
	if which.Source != "" {
		// The other branch of the parameter union installs and refreshes skill
		// packages from a third-party index. Nothing here installs packages.
		return nil, Invalid("no skill package source is served here")
	}

	var p skillParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if !skillRE.MatchString(p.SkillKey) {
		return nil, Invalid("bad skill key")
	}
	if len(p.Env) > skillEnvMax {
		return nil, Invalid("env carries more than %d entries", skillEnvMax)
	}

	st, err := c.Store()
	if err != nil {
		return nil, err
	}

	// The credentials are sealed first, and then the settings document is read,
	// changed and written as one act. Sealing is KMS's work — a call into
	// another subsystem, which in a split deployment is a round trip — and the
	// act holds this org's file for as long as it runs, so nothing that waits
	// on somebody else belongs inside it.
	var edit skillEdit
	edit.enabled = p.Enabled
	if p.APIKey != nil {
		v := credential(*p.APIKey)
		if len(v) > skillValueMax {
			return nil, Invalid("the credential exceeds %d bytes", skillValueMax)
		}
		if v != redactedMark {
			if err := seal(c, p.SkillKey, "apiKey", v); err != nil {
				return nil, err
			}
			edit.key = new(bool)
			*edit.key = v != ""
		}
	}
	for name, raw := range p.Env {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if !skillRE.MatchString(name) {
			return nil, Invalid("bad env name %q", name)
		}
		v := strings.TrimSpace(raw)
		if len(v) > skillValueMax {
			return nil, Invalid("env %s exceeds %d bytes", name, skillValueMax)
		}
		if v == redactedMark {
			continue
		}
		if !looksSecret(name) {
			edit.plain = append(edit.plain, [2]string{name, v})
			continue
		}
		if err := seal(c, p.SkillKey, "env/"+name, v); err != nil {
			return nil, err
		}
		edit.sealed = append(edit.sealed, [2]string{name, v})
	}

	var s skillSettings
	if err := st.Do(c.Context(), func(st *Store) error {
		if err := st.Get(c.Context(), "skills", p.SkillKey, &s); err != nil && !errors.Is(err, ErrNoDoc) {
			return Unavailable("the skill's settings could not be read")
		}
		edit.apply(&s)
		if err := st.Put(c.Context(), "skills", p.SkillKey, s); err != nil {
			return Unavailable("the skill's settings could not be written")
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "skillKey": p.SkillKey, "config": s.redact()}, nil
}

// skillEdit is what one skills.update does to the stored settings once its
// credentials are sealed. Separating it from the sealing is what lets the
// document be read and written as one act: sealing reaches another subsystem,
// applying reaches nothing.
//
// plain and sealed are name/value pairs in the order they were sent; an empty
// value removes the entry. A name's class is a function of the name, so an
// entry never moves between the two — a sealed one is recorded by name alone,
// because its value is in KMS.
type skillEdit struct {
	enabled *bool
	key     *bool
	plain   [][2]string
	sealed  [][2]string
}

func (e *skillEdit) apply(s *skillSettings) {
	if e.enabled != nil {
		s.Enabled = e.enabled
	}
	if e.key != nil {
		s.Key = *e.key
	}
	for _, kv := range e.plain {
		if kv[1] == "" {
			delete(s.Env, kv[0])
			continue
		}
		if s.Env == nil {
			s.Env = map[string]string{}
		}
		s.Env[kv[0]] = kv[1]
	}
	for _, kv := range e.sealed {
		s.Sealed = slices.DeleteFunc(s.Sealed, func(n string) bool { return n == kv[0] })
		if kv[1] != "" {
			s.Sealed = append(s.Sealed, kv[0])
			sort.Strings(s.Sealed)
		}
	}
}

// redact renders the settings for the answer, every secret standing as the
// redaction mark. It reads no secret to do it: the settings already say which
// names have one.
func (s skillSettings) redact() map[string]any {
	out := map[string]any{}
	if s.Enabled != nil {
		out["enabled"] = *s.Enabled
	}
	if s.Key {
		out["apiKey"] = redactedMark
	}
	if len(s.Env) > 0 || len(s.Sealed) > 0 {
		env := map[string]string{}
		for k, v := range s.Env {
			env[k] = v
		}
		for _, k := range s.Sealed {
			env[k] = redactedMark
		}
		out["env"] = env
	}
	return out
}

// seal writes one secret into KMS under the scope that made the call, so a bot
// keeps its own credentials and the org keeps its own. An empty value seals
// nothing over the old ciphertext, which is what clearing a credential means.
func seal(c *Call, key, field, value string) error {
	if c.svc.KMS == nil {
		return Unavailable("no secret store is configured")
	}
	ref, err := vaultRef(c.Org(), c.Bot(), "skills/"+key+"/"+field)
	if err != nil {
		return err
	}
	if err := c.svc.KMS.PutSecret(c.Context(), ref, []byte(value)); err != nil {
		c.Log().Error("seal skill secret", "org", c.Org(), "skill", key, "err", err)
		return Unavailable("the credential could not be sealed")
	}
	return nil
}

// secretRE and notSecret are the protocol's own classification of a name whose
// value must not be shown, transcribed from src/config/sensitive-paths.ts. The
// exempt suffixes are the names that match the patterns and mean something
// else — a token budget is a number, not a token.
var (
	secretRE  = regexp.MustCompile(`(?i)token$|password|secret|api.?key|encrypt.?key|private.?key|serviceaccount(ref)?$`)
	notSecret = []string{
		"maxtokens", "maxoutputtokens", "maxinputtokens", "maxcompletiontokens",
		"contexttokens", "totaltokens", "tokencount", "tokenlimit", "tokenbudget",
		"passwordfile",
	}
)

// looksSecret reports whether a name's value is one to seal and to redact.
func looksSecret(name string) bool {
	low := strings.ToLower(name)
	for _, exempt := range notSecret {
		if strings.HasSuffix(low, exempt) {
			return false
		}
	}
	return secretRE.MatchString(name)
}

// credential reduces a pasted secret to what can actually be sent as one: line
// breaks and control characters gone, anything above Latin-1 gone, ends
// trimmed. Interior spaces stay — "Bearer x" is a value someone meant.
func credential(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		if r > 0xff || r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}

// loose decodes parameters the protocol leaves open. Call.Bind closes the
// object, which is right for a closedObject schema and wrong for a method whose
// TypeScript reads the fields it wants off raw params: closing it here would
// refuse a caller the protocol accepts.
func loose(c *Call, v any) error {
	if len(c.Params()) == 0 {
		return nil
	}
	if err := json.Unmarshal(c.Params(), v); err != nil {
		return Invalid("params: %v", err)
	}
	return nil
}
