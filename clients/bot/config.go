package bot

// The config family: the gateway's own configuration, the team's secret store,
// and the host the gateway runs on.
//
// The configuration is ONE document, addressed the way every other piece of
// protocol state is addressed — the bot's file when the call is bound to a
// bot, the org's otherwise (Call.Store). There is no file on disk, so nothing
// here reads or writes a path, and no revision is projected through a key: the
// caller already holds the whole document, so a plain content digest tells it
// nothing it did not send.
//
// The secret store is KMS. A value is sealed there under the org's own store
// path and never lands in a document here; what is kept alongside is the roster
// — which names exist, whether each is a secret or a plain environment value,
// when it changed and who changed it. An `env` value is read back out of KMS on
// the way to a list because the protocol says those are deliberately visible; a
// `secret` value has no way back out at all.
//
// Not served, deliberately:
//
//	config.schema, config.schema.lookup  no Go type declares the shape of this
//	    configuration, so there is no schema to generate. The web UI's schema
//	    load fails open — it skips the form renderer and edits raw JSON — which
//	    is the honest state until the families that consume config declare what
//	    they read.
//	config.openFile  asks the gateway host to open a file in the operator's
//	    editor. There is no operator desktop on the other end of a hosted
//	    gateway and no file to open.
//	status, health  aggregates over sessions, tasks, channels and agents, whose
//	    only caller is a JSON dump on the debug page. They belong to whoever
//	    composes those families, over the per-subsystem /v1/<name>/health probes
//	    that already exist, rather than to a second health path invented here.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
)

func init() {
	Register("config.get", Read, configGet)
	Register("config.set", Admin, configSet)
	Register("config.apply", Admin, configApply)
	Register("config.patch", Admin, configPatch)
	Register("secrets.store.list", Admin, secretsList)
	Register("secrets.store.set", Admin, secretsSet)
	Register("secrets.store.delete", Admin, secretsDelete)
	Register("system.info", Read, systemInfo)
	Declare("config.changed")
}

// Where the family's documents live. The org (and the bot) is the file, so
// neither is a key here.
const (
	configDocs = "config"
	configID   = "gateway"
	secretDocs = "secrets"
)

// ── the configuration document ───────────────────────────────────────────────

// configDoc is the stored configuration: the text the operator authored and the
// object it parses to. The object is kept alongside the text because every read
// and every patch wants it, and re-parsing on each is work with no answer of
// its own.
type configDoc struct {
	Raw     string         `json:"raw"`
	Config  map[string]any `json:"config"`
	Updated int64          `json:"updated,omitempty"`
	By      string         `json:"by,omitempty"`

	// stored distinguishes a configuration that was written from the empty one
	// an org starts with. It is not part of the document.
	stored bool
}

// hash identifies the authored text. It is the token a write must present: a
// caller that read one text and writes against another is working from a copy
// somebody has since replaced.
func (d *configDoc) hash() string { return configDigest(d.Raw) }

// revision identifies the configuration itself rather than its spelling, so
// reformatting the text is not a change to the gateway. Go orders a map's keys
// when it encodes one, so two equal configurations render identically however
// they were written.
func (d *configDoc) revision() string {
	b, err := json.Marshal(d.Config)
	if err != nil {
		return ""
	}
	return configDigest(string(b))
}

// guard states the two ways a write may be refused for what it was written
// against. The web UI matches on "config changed since last load" to offer a
// reload rather than retrying a whole-form save over somebody else's edit, so
// the wording is part of the contract.
func (d *configDoc) guard(given string) error {
	switch given = strings.TrimSpace(given); {
	case given == "":
		return Invalid("config base hash required; re-run config.get and retry")
	case given != d.hash():
		return Invalid("config changed since last load; re-run config.get and retry")
	}
	return nil
}

func configDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func readConfig(c *Call) (*configDoc, error) {
	st, err := c.Store()
	if err != nil {
		return nil, err
	}
	return getConfig(c, st)
}

// getConfig reads the configuration out of the store it is given, so a write
// reads the document it is about to replace inside the act that replaces it.
func getConfig(c *Call, st *Store) (*configDoc, error) {
	var d configDoc
	switch err := st.Get(c.Context(), configDocs, configID, &d); {
	case errors.Is(err, ErrNoDoc):
		// An org that has never written a configuration has the empty one. It
		// is a document like any other, so a first write is guarded like any
		// other rather than through a case of its own.
		return &configDoc{Raw: "{}", Config: map[string]any{}}, nil
	case err != nil:
		c.Log().Error("read bot config", "org", c.Org(), "bot", c.Bot(), "err", err)
		return nil, Unavailable("the configuration could not be read")
	}
	if d.Config == nil {
		d.Config = map[string]any{}
	}
	d.stored = true
	return &d, nil
}

// writeConfig replaces the configuration and tells the org it changed.
//
// There is no second step. A gateway that holds its configuration in memory has
// to be restarted before a saved change takes effect, and reports the revision
// it is running separately from the one on disk; here the document IS the
// configuration and a reader sees the new one immediately, so the revision
// stored and the revision in effect are the same fact. That is why
// appliedConfigHash below is the current revision rather than a stored one, and
// why the web UI's restart banner never lights.
func writeConfig(c *Call, st *Store, d *configDoc, raw string, cfg map[string]any) (string, error) {
	d.Raw = raw
	d.Config = cfg
	d.Updated = time.Now().UnixMilli()
	d.By = c.User()
	if err := st.Put(c.Context(), configDocs, configID, d); err != nil {
		c.Log().Error("write bot config", "org", c.Org(), "bot", c.Bot(), "err", err)
		return "", Unavailable("the configuration could not be written")
	}
	return d.hash(), nil
}

// changeConfig reads the configuration, hands it to fn, and writes back
// whatever fn returns — as one act, and then says so.
//
// The hash a caller states is a compare-and-set on the whole document
// (configDoc.guard), so it has to be checked against the document being
// replaced and not against a copy read a moment earlier: two writers that both
// state the hash they read would otherwise both pass, and the second would
// silently drop everything the first wrote. The same holds for the preference
// subtree a hash-free write may reach — those leaves are merged into the
// document, and merging into a stale one erases the leaves somebody else just
// added.
//
// fn answers the text and the object to store, or nothing at all when the write
// turns out to change nothing.
func changeConfig(c *Call, fn func(*configDoc) (string, map[string]any, error)) (string, bool, error) {
	st, err := c.Store()
	if err != nil {
		return "", false, err
	}
	var (
		hash    string
		written bool
	)
	if err := st.Do(c.Context(), func(st *Store) error {
		d, err := getConfig(c, st)
		if err != nil {
			return err
		}
		raw, cfg, err := fn(d)
		if err != nil || cfg == nil {
			return err
		}
		written = true
		hash, err = writeConfig(c, st, d, raw, cfg)
		return err
	}); err != nil {
		return "", false, err
	}
	if written {
		Publish(c.Org(), c.Bot(), "", "config.changed", nil)
	}
	return hash, written, nil
}

// configObject reads the text a write carries. The protocol sends a
// configuration as a string even when it is an object, so this is the one place
// text becomes configuration.
func configObject(raw, method string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, Invalid("invalid %s params: raw (string) required", method)
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, Invalid("%v", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, Invalid("%s raw must be an object", method)
	}
	return m, nil
}

// ── config.get ───────────────────────────────────────────────────────────────

// configSnapshot is one editable view of the configuration.
//
// sourceConfig, resolved, config and runtimeConfig are the same object. In a
// gateway that expands includes and environment references and then layers
// defaults underneath, they differ, and a caller edits the first it finds; here
// nothing is expanded and nothing is layered, so the authored configuration and
// the effective one are one thing said four ways because the reader asks for
// them by four names.
type configSnapshot struct {
	Exists   bool           `json:"exists"`
	Raw      string         `json:"raw"`
	Parsed   map[string]any `json:"parsed"`
	Source   map[string]any `json:"sourceConfig"`
	Resolved map[string]any `json:"resolved"`
	Runtime  map[string]any `json:"runtimeConfig"`
	Config   map[string]any `json:"config"`
	Valid    bool           `json:"valid"`
	Issues   []configIssue  `json:"issues"`
	Warnings []string       `json:"warnings"`
	Hash     string         `json:"hash"`
	Revision string         `json:"configRevisionHash"`
	Applied  *string        `json:"appliedConfigHash"`
}

// configIssue is one reason a configuration is not valid, addressed to the
// place in the document that caused it.
type configIssue struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (d *configDoc) view() configSnapshot {
	revision := d.revision()
	return configSnapshot{
		Exists:   d.stored,
		Raw:      d.Raw,
		Parsed:   d.Config,
		Source:   d.Config,
		Resolved: d.Config,
		Runtime:  d.Config,
		Config:   d.Config,
		// Anything stored parsed as an object on the way in, and there is no
		// declared shape to check it against, so a stored configuration is
		// valid and carries no issues.
		Valid:    true,
		Issues:   []configIssue{},
		Warnings: []string{},
		Hash:     d.hash(),
		Revision: revision,
		Applied:  &revision,
	}
}

func configGet(c *Call) (any, error) {
	if err := c.Bind(&struct{}{}); err != nil {
		return nil, err
	}
	d, err := readConfig(c)
	if err != nil {
		return nil, err
	}
	return d.view(), nil
}

// ── config.set and config.apply ──────────────────────────────────────────────

type configSetParams struct {
	Raw      string `json:"raw"`
	BaseHash string `json:"baseHash"`
}

// configApplyParams carries, besides the write itself, where to report it and
// how long to wait before restarting. Nothing here restarts and nothing here
// reports into a chat channel, so those fields are accepted and unused: the
// protocol closes its parameter objects, and refusing a field the caller is
// entitled to send would fail a save the caller got right.
type configApplyParams struct {
	Raw          string          `json:"raw"`
	BaseHash     string          `json:"baseHash"`
	SessionKey   string          `json:"sessionKey"`
	Delivery     json.RawMessage `json:"deliveryContext"`
	Note         string          `json:"note"`
	RestartDelay int             `json:"restartDelayMs"`
}

// configAck answers a whole-document write.
type configAck struct {
	OK     bool           `json:"ok"`
	Hash   string         `json:"hash"`
	Config map[string]any `json:"config"`
}

func configSet(c *Call) (any, error) {
	var p configSetParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	return replaceConfig(c, p.Raw, p.BaseHash, "config.set")
}

func configApply(c *Call) (any, error) {
	var p configApplyParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	return replaceConfig(c, p.Raw, p.BaseHash, "config.apply")
}

// replaceConfig is the whole-document write both config.set and config.apply
// are. They differ in what a caller may say alongside the write, not in what
// the write does.
func replaceConfig(c *Call, raw, baseHash, method string) (any, error) {
	cfg, err := configObject(raw, method)
	if err != nil {
		return nil, err
	}
	hash, _, err := changeConfig(c, func(d *configDoc) (string, map[string]any, error) {
		if err := d.guard(baseHash); err != nil {
			return "", nil, err
		}
		return raw, cfg, nil
	})
	if err != nil {
		return nil, err
	}
	return configAck{OK: true, Hash: hash, Config: cfg}, nil
}

// ── config.patch ─────────────────────────────────────────────────────────────

type configPatchParams struct {
	configApplyParams
	Replace []string `json:"replacePaths"`
}

// configPatchAck answers a patch that changed something. changedPaths names
// every leaf that moved, so a caller can see what its patch actually did rather
// than what it asked for.
type configPatchAck struct {
	OK      bool           `json:"ok"`
	Hash    string         `json:"hash"`
	Config  map[string]any `json:"config"`
	Changed []string       `json:"changedPaths"`
}

// configNoopAck answers a patch whose every value was already there. A patch is
// authored intent, so re-sending one is not an error; it simply moved nothing.
type configNoopAck struct {
	OK      bool           `json:"ok"`
	Noop    bool           `json:"noop"`
	Changed []string       `json:"changedPaths"`
	Config  map[string]any `json:"config"`
}

// prefsPath is the one subtree a write may change without naming the document
// it was written against. Preferences are per-leaf and last writer wins, so two
// browser tabs of one operator do not collide over the whole configuration.
const prefsPath = "ui.prefs"

func configPatch(c *Call) (any, error) {
	var p configPatchParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	patch, err := configObject(p.Raw, "config.patch")
	if err != nil {
		return nil, err
	}
	loose := strings.TrimSpace(p.BaseHash) == ""
	if loose && !nestedPrefs(patch) {
		// Replacing or deleting the preference container is not a leaf write,
		// and a stale caller doing it would drop keys a concurrent writer added.
		return nil, Invalid("config base hash required; re-run config.get and retry")
	}

	var (
		merged  map[string]any
		changed []string
	)
	hash, written, err := changeConfig(c, func(d *configDoc) (string, map[string]any, error) {
		if !loose {
			if err := d.guard(p.BaseHash); err != nil {
				return "", nil, err
			}
		}

		replacing := replacePaths(p.Replace)
		if err := duplicateIDs(patch, d.Config, replacing, ""); err != nil {
			return "", nil, err
		}
		merged, _ = mergePatch(d.Config, patch, replacing, "").(map[string]any)

		if lost := shrunkArrays(d.Config, patch, merged, "", replacing); len(lost) > 0 {
			return "", nil, Invalid("config.patch would remove entries from array path(s): %s. "+
				"Pass replacePaths with the exact path(s) when this is intentional, "+
				"or use config.apply for full-config replacement.", strings.Join(lost, ", "))
		}

		changed = changedLeaves(d.Config, merged, "")
		if loose {
			var guarded []string
			for _, path := range changed {
				if !preferencePath(path) {
					guarded = append(guarded, path)
				}
			}
			if len(guarded) > 0 {
				return "", nil, Invalid("config base hash required for %s; "+
					"re-run config.get and retry with baseHash", strings.Join(guarded, ", "))
			}
		}
		if len(changed) == 0 {
			merged = d.Config
			return "", nil, nil
		}

		raw, err := json.MarshalIndent(merged, "", "  ")
		if err != nil {
			return "", nil, Invalid("%v", err)
		}
		return string(raw), merged, nil
	})
	if err != nil {
		return nil, err
	}
	if !written {
		return configNoopAck{OK: true, Noop: true, Changed: []string{}, Config: merged}, nil
	}
	return configPatchAck{OK: true, Hash: hash, Config: merged, Changed: changed}, nil
}

// preferencePath reports whether a leaf belongs to the subtree a hash-free
// write may reach.
func preferencePath(path string) bool {
	return path == prefsPath || strings.HasPrefix(path, prefsPath+".")
}

// nestedPrefs reports whether a patch keeps the preference subtree an object
// the whole way down. A patch that puts anything else there is replacing the
// container rather than writing a leaf inside it.
func nestedPrefs(patch any) bool {
	node := patch
	for _, segment := range strings.Split(prefsPath, ".") {
		m, ok := node.(map[string]any)
		if !ok {
			return false
		}
		v, present := m[segment]
		if !present {
			return true
		}
		if _, ok := v.(map[string]any); !ok {
			return false
		}
		node = v
	}
	return true
}

// replacePaths reads the array paths a caller says it means to shrink. A path
// may be written with or without the trailing brackets that mark an array.
func replacePaths(paths []string) map[string]bool {
	out := map[string]bool{}
	for _, p := range paths {
		if p = strings.TrimSuffix(strings.TrimSpace(p), "[]"); p != "" {
			out[p] = true
		}
	}
	return out
}

// ── merging ──────────────────────────────────────────────────────────────────

// mergePatch layers a patch over a configuration: an object merges key by key,
// a null deletes, and anything else replaces.
//
// Arrays whose entries carry a stable id merge like maps, so editing one entry
// of a list does not require sending its siblings back — and cannot drop them
// by leaving them out. A caller that does mean to shrink such a list says so
// through replacePaths, which turns that path back into a plain replacement.
func mergePatch(base, patch any, replacing map[string]bool, prefix string) any {
	p, ok := patch.(map[string]any)
	if !ok {
		return patch
	}
	out := map[string]any{}
	if b, ok := base.(map[string]any); ok {
		maps.Copy(out, b)
	}
	for _, key := range slices.Sorted(maps.Keys(p)) {
		value := p[key]
		path := joinPath(prefix, key)
		if value == nil {
			delete(out, key)
			continue
		}
		if was, ok := out[key].([]any); ok {
			if now, ok := value.([]any); ok && !replacing[path] {
				if merged := mergeByID(was, now, replacing, path); merged != nil {
					out[key] = merged
					continue
				}
			}
		}
		if _, ok := value.(map[string]any); ok {
			out[key] = mergePatch(out[key], value, replacing, path)
			continue
		}
		out[key] = value
	}
	return out
}

// mergeByID merges two arrays by entry id, or reports that it cannot: an array
// whose base entries are not all id-carrying objects has no key to merge on and
// is replaced whole. An id the base does not have is appended.
func mergeByID(base, patch []any, replacing map[string]bool, path string) []any {
	out := make([]any, len(base))
	index := map[string]int{}
	for i, e := range base {
		id, ok := entryID(e)
		if !ok {
			return nil
		}
		out[i] = e
		index[id] = i
	}
	for _, e := range patch {
		id, ok := entryID(e)
		if !ok {
			out = append(out, e)
			continue
		}
		if i, seen := index[id]; seen {
			out[i] = mergePatch(out[i], e, replacing, path+"[]")
			continue
		}
		index[id] = len(out)
		out = append(out, e)
	}
	return out
}

func entryID(v any) (string, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return "", false
	}
	id, ok := m["id"].(string)
	return id, ok && id != ""
}

// duplicateIDs refuses a merge whose key is ambiguous. Merging by id is
// sequential, so two entries sharing one id would let the last quietly win —
// on either side of the merge.
func duplicateIDs(patch, current any, replacing map[string]bool, path string) error {
	if pa, ok := patch.([]any); ok {
		ca, ok := current.([]any)
		if !ok || replacing[path] || !idKeyed(ca) {
			return nil
		}
		if err := uniqueIDs(ca, path, "current config contains duplicate id %s at %s; "+
			"use replacePaths for an explicit replacement"); err != nil {
			return err
		}
		if err := uniqueIDs(pa, path, "duplicate id %s in the array at %s"); err != nil {
			return err
		}
		was := map[string]any{}
		for _, e := range ca {
			if id, ok := entryID(e); ok {
				was[id] = e
			}
		}
		for _, e := range pa {
			id, ok := entryID(e)
			if !ok {
				continue
			}
			if before, seen := was[id]; seen {
				if err := duplicateIDs(e, before, replacing, path+"[]"); err != nil {
					return err
				}
			}
		}
		return nil
	}
	pm, ok := patch.(map[string]any)
	if !ok {
		return nil
	}
	cm, ok := current.(map[string]any)
	if !ok {
		return nil
	}
	for _, key := range slices.Sorted(maps.Keys(pm)) {
		if err := duplicateIDs(pm[key], cm[key], replacing, joinPath(path, key)); err != nil {
			return err
		}
	}
	return nil
}

func uniqueIDs(entries []any, path, format string) error {
	seen := map[string]bool{}
	for _, e := range entries {
		id, ok := entryID(e)
		if !ok {
			continue
		}
		if seen[id] {
			return Invalid(format, id, orRoot(path))
		}
		seen[id] = true
	}
	return nil
}

// idKeyed reports whether every entry of an array carries a stable id, which is
// what makes it mergeable rather than replaceable.
func idKeyed(entries []any) bool {
	for _, e := range entries {
		if _, ok := entryID(e); !ok {
			return false
		}
	}
	return len(entries) > 0
}

// ── what a patch would destroy ───────────────────────────────────────────────

// shrunkArrays names the array paths a patch would take entries out of without
// saying it meant to. A patch is a partial statement, so a list that comes back
// shorter than it went in is nearly always a caller that sent the entries it
// knew about rather than the ones that exist.
func shrunkArrays(base, patch, merged any, prefix string, replacing map[string]bool) []string {
	bm, ok := base.(map[string]any)
	if !ok {
		return nil
	}
	pm, ok := patch.(map[string]any)
	if !ok {
		return nil
	}
	mm, _ := merged.(map[string]any)

	var out []string
	for _, key := range slices.Sorted(maps.Keys(pm)) {
		path := joinPath(prefix, key)
		was, now, after := bm[key], pm[key], mm[key]

		if array, isArray := was.([]any); isArray {
			patched, ok := now.([]any)
			if now == nil || !ok {
				out = append(out, path)
				continue
			}
			if result, ok := after.([]any); ok {
				if idKeyed(array) {
					if !keepsIDs(array, result) {
						out = append(out, path)
						continue
					}
					// The list survived; an entry's own arrays still might not.
					out = append(out, shrunkInEntries(array, patched, result, path, replacing)...)
				} else if !keepsAll(array, result) {
					out = append(out, path)
					continue
				}
			}
		} else if _, isObject := was.(map[string]any); isObject {
			if _, ok := now.(map[string]any); !ok {
				out = append(out, arraysUnder(was, path)...)
				continue
			}
		}

		if _, ok := now.(map[string]any); ok {
			out = append(out, shrunkArrays(was, now, after, path, replacing)...)
		}
	}
	return notReplaced(out, replacing)
}

// shrunkInEntries follows a patch into the entries of an id-merged array, where
// the same loss can happen one level down.
func shrunkInEntries(base, patch, merged []any, path string, replacing map[string]bool) []string {
	was := map[string]any{}
	for _, e := range base {
		if id, ok := entryID(e); ok {
			was[id] = e
		}
	}
	after := map[string]any{}
	for _, e := range merged {
		if id, ok := entryID(e); ok {
			after[id] = e
		}
	}
	var out []string
	for _, e := range patch {
		id, ok := entryID(e)
		if !ok {
			continue
		}
		if before, seen := was[id]; seen {
			out = append(out, shrunkArrays(before, e, after[id], path+"[]", replacing)...)
		}
	}
	return out
}

// keepsIDs reports whether every id the base held is still there.
func keepsIDs(base, merged []any) bool {
	held := map[string]bool{}
	for _, e := range merged {
		if id, ok := entryID(e); ok {
			held[id] = true
		}
	}
	for _, e := range base {
		if id, ok := entryID(e); ok && !held[id] {
			return false
		}
	}
	return true
}

// keepsAll reports whether every entry the base held is still there, for an
// array with no id to compare on.
func keepsAll(base, merged []any) bool {
	for _, want := range base {
		found := false
		for _, got := range merged {
			if reflect.DeepEqual(want, got) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// arraysUnder names every array beneath a value, for the case where a whole
// subtree is being replaced by something that is not an object.
func arraysUnder(v any, path string) []string {
	if _, ok := v.([]any); ok {
		return []string{path}
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	var out []string
	for _, key := range slices.Sorted(maps.Keys(m)) {
		out = append(out, arraysUnder(m[key], joinPath(path, key))...)
	}
	return out
}

func notReplaced(paths []string, replacing map[string]bool) []string {
	var out []string
	for _, p := range paths {
		if !replacing[p] {
			out = append(out, p)
		}
	}
	return out
}

// ── what a patch changed ─────────────────────────────────────────────────────

// changedLeaves names every leaf whose value differs between two
// configurations. It is what a patch reports back, and what decides whether a
// patch changed anything at all.
func changedLeaves(prev, next any, prefix string) []string {
	pm, isPrev := prev.(map[string]any)
	nm, isNext := next.(map[string]any)
	if !isPrev && !isNext {
		if reflect.DeepEqual(prev, next) {
			return nil
		}
		return []string{orRoot(prefix)}
	}
	keys := slices.Sorted(maps.Keys(bothKeys(pm, nm)))
	if len(keys) == 0 {
		if reflect.DeepEqual(prev, next) {
			return nil
		}
		return []string{orRoot(prefix)}
	}
	var out []string
	for _, key := range keys {
		out = append(out, changedLeaves(pm[key], nm[key], joinPath(prefix, key))...)
	}
	return out
}

func bothKeys(a, b map[string]any) map[string]bool {
	out := make(map[string]bool, len(a)+len(b))
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

func joinPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

func orRoot(path string) string {
	if path == "" {
		return "<root>"
	}
	return path
}

// ── the secret store ─────────────────────────────────────────────────────────

// A name is either an environment-variable name or the one-shot handle a
// connection hand-off mints. Only the first shape is ever listed: a hand-off
// exists for the length of one exchange and is not part of the team's roster.
var (
	secretName = regexp.MustCompile(`^(?:[A-Z][A-Z0-9_]{0,127}|github-setup-[a-f0-9]{32})$`)
	plainName  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,127}$`)
)

// The protocol's bounds on one entry.
const (
	maxSecretValue = 64 << 10
	maxSecretHosts = 128
	maxHostName    = 253
)

// secret is the roster entry for one stored value: everything about it except
// the value, which is in KMS.
type secret struct {
	Kind    string   `json:"kind"`
	Hosts   []string `json:"hosts,omitempty"`
	Created int64    `json:"created"`
	Updated int64    `json:"updated"`
	By      string   `json:"by,omitempty"`
}

// secretEntry is one row of the inventory. A secret carries the hosts it may be
// sent to; an environment value carries the value itself, which is the whole
// difference between the two kinds.
type secretEntry struct {
	Name    string   `json:"name"`
	Scope   string   `json:"scopeKind"`
	ScopeID string   `json:"scopeId"`
	Created int64    `json:"createdAtMs"`
	Updated int64    `json:"updatedAtMs"`
	By      string   `json:"updatedBy,omitempty"`
	Kind    string   `json:"kind"`
	Hosts   []string `json:"allowedHosts,omitempty"`
	Value   string   `json:"value,omitempty"`
}

// secretAck answers a write to the store. Nothing here caches secret material —
// every use resolves it from KMS at the moment it is needed — so no owner ever
// has to be refreshed after a write, and reloaded is always false.
type secretAck struct {
	OK       bool `json:"ok"`
	Reloaded bool `json:"reloaded"`
}

// vaultRef addresses one value in KMS: the org's own secrets when bot is
// empty, a bot's when it is not, under a bot segment that keeps the store
// clear of what an operator put at the org's root by other means.
//
// The org is folded through cloud.SanitizeOrg — the one injective org slugger,
// the same one that chose the file this org's state lives in — so the KMS
// namespace and the file namespace agree. KMS shards its own files on the
// leading orgs/{org} segment (clients/kms.fileOrg), and the org is the only
// part of this path a caller does not choose the shape of: bot ids and skill
// keys admit no '/', an org name is whatever IAM minted. Unfolded, an org
// literally named "acme/bots/b1" would seal its credentials into tenant acme's
// record and read back as acme's own. An org the slugger refuses has no
// namespace, and nothing is written for it.
func vaultRef(org, bot, tail string) (string, error) {
	slug := cloud.SanitizeOrg(org)
	if slug == "" {
		return "", Unavailable("this org has no secret namespace")
	}
	ref := "orgs/" + slug
	if bot != "" {
		ref += "/bots/" + bot
	}
	return ref + "/bot/" + tail, nil
}

// kmsFor is the KMS this call writes through. It is the only place a secret
// value is ever held, so a deployment without one has no secret store at all
// rather than a plaintext one.
func kmsFor(c *Call) (cloud.KMSClient, error) {
	if c.svc.KMS == nil {
		return nil, Unavailable("the secret store is not configured")
	}
	return c.svc.KMS, nil
}

func secretsList(c *Call) (any, error) {
	if err := c.Bind(&struct{}{}); err != nil {
		return nil, err
	}
	keys, err := kmsFor(c)
	if err != nil {
		return nil, err
	}
	st, err := c.OrgStore()
	if err != nil {
		return nil, err
	}
	docs, err := st.List(c.Context(), secretDocs, 0, 0)
	if err != nil {
		c.Log().Error("list bot secrets", "org", c.Org(), "err", err)
		return nil, Unavailable("the secret store could not be read")
	}

	entries := []secretEntry{}
	for _, doc := range docs {
		if !plainName.MatchString(doc.ID) {
			continue
		}
		var s secret
		if err := json.Unmarshal(doc.Doc, &s); err != nil {
			c.Log().Error("read bot secret", "org", c.Org(), "name", doc.ID, "err", err)
			return nil, Unavailable("the secret store could not be read")
		}
		e := secretEntry{
			Name: doc.ID, Scope: "team", ScopeID: "",
			Created: s.Created, Updated: s.Updated, By: s.By, Kind: s.Kind,
		}
		if s.Kind == "env" {
			// An environment value is meant to be readable; it is sealed
			// nonetheless, so reading it back is a trip through KMS.
			ref, err := vaultRef(c.Org(), "", doc.ID)
			if err != nil {
				return nil, err
			}
			value, err := keys.GetSecret(c.Context(), ref)
			if err != nil {
				c.Log().Error("open bot secret", "org", c.Org(), "name", doc.ID, "err", err)
				return nil, Unavailable("the secret store could not be read")
			}
			e.Value = string(value)
		} else {
			// A secret's value has no way out. The hosts it may be sent to are
			// not the value, and an entry with no restriction says so with an
			// empty list rather than by leaving the question unanswered.
			e.Kind = "secret"
			e.Hosts = s.Hosts
			if e.Hosts == nil {
				e.Hosts = []string{}
			}
		}
		entries = append(entries, e)
	}
	slices.SortFunc(entries, func(a, b secretEntry) int { return strings.Compare(a.Name, b.Name) })
	return map[string]any{"entries": entries}, nil
}

type secretsSetParams struct {
	Name  string   `json:"name"`
	Value string   `json:"value"`
	Kind  string   `json:"kind"`
	Hosts []string `json:"allowedHosts"`
}

func secretsSet(c *Call) (any, error) {
	var p secretsSetParams
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if !secretName.MatchString(p.Name) {
		return nil, Invalid("a secret is named in capitals, digits and underscores")
	}
	if p.Kind != "secret" && p.Kind != "env" {
		return nil, Invalid(`a secret's kind is "secret" or "env"`)
	}
	if len(p.Value) > maxSecretValue {
		return nil, Invalid("a secret value may be at most %d bytes", maxSecretValue)
	}
	if err := checkHosts(p.Hosts); err != nil {
		return nil, err
	}
	keys, err := kmsFor(c)
	if err != nil {
		return nil, err
	}
	st, err := c.OrgStore()
	if err != nil {
		return nil, err
	}

	ref, err := vaultRef(c.Org(), "", p.Name)
	if err != nil {
		return nil, err
	}
	// The value is sealed before the roster names it, so a write that fails
	// halfway leaves a value nothing points at rather than a name with nothing
	// behind it.
	if err := keys.PutSecret(c.Context(), ref, []byte(p.Value)); err != nil {
		c.Log().Error("seal bot secret", "org", c.Org(), "name", p.Name, "err", err)
		return nil, Unavailable("the secret could not be stored")
	}

	now := time.Now().UnixMilli()
	s := secret{Kind: p.Kind, Created: now, Updated: now, By: c.User()}
	if p.Kind == "secret" {
		s.Hosts = p.Hosts
	}
	// Carrying the first-write time forward and writing the entry are one act,
	// so two writes of one name cannot both read the entry as absent and each
	// record itself as the first.
	if err := st.Do(c.Context(), func(st *Store) error {
		var was secret
		if err := st.Get(c.Context(), secretDocs, p.Name, &was); err == nil && was.Created > 0 {
			s.Created = was.Created
		}
		return st.Put(c.Context(), secretDocs, p.Name, s)
	}); err != nil {
		c.Log().Error("write bot secret", "org", c.Org(), "name", p.Name, "err", err)
		return nil, Unavailable("the secret could not be stored")
	}
	return secretAck{OK: true}, nil
}

func secretsDelete(c *Call) (any, error) {
	var p struct {
		Name string `json:"name"`
	}
	if err := c.Bind(&p); err != nil {
		return nil, err
	}
	if !secretName.MatchString(p.Name) {
		return nil, Invalid("a secret is named in capitals, digits and underscores")
	}
	keys, err := kmsFor(c)
	if err != nil {
		return nil, err
	}
	st, err := c.OrgStore()
	if err != nil {
		return nil, err
	}
	ref, err := vaultRef(c.Org(), "", p.Name)
	if err != nil {
		return nil, err
	}
	// The material goes first. KMS has no way to forget a record, so the value
	// is emptied instead — and refusing here rather than dropping the roster
	// entry keeps the store from reporting a secret gone that is still sealed.
	if err := keys.PutSecret(c.Context(), ref, nil); err != nil {
		c.Log().Error("clear bot secret", "org", c.Org(), "name", p.Name, "err", err)
		return nil, Unavailable("the secret could not be deleted")
	}
	if err := st.Delete(c.Context(), secretDocs, p.Name); err != nil {
		c.Log().Error("forget bot secret", "org", c.Org(), "name", p.Name, "err", err)
		return nil, Unavailable("the secret could not be deleted")
	}
	return secretAck{OK: true}, nil
}

// checkHosts bounds the hosts a secret may be sent to, and refuses a repeat:
// the same host twice is a caller that built the list wrong.
func checkHosts(hosts []string) error {
	if len(hosts) > maxSecretHosts {
		return Invalid("a secret may name at most %d hosts", maxSecretHosts)
	}
	seen := map[string]bool{}
	for _, h := range hosts {
		if h == "" || len(h) > maxHostName {
			return Invalid("a host is between 1 and %d characters", maxHostName)
		}
		if seen[h] {
			return Invalid("host %s is named twice", h)
		}
		seen[h] = true
	}
	return nil
}

// ── system.info ──────────────────────────────────────────────────────────────

// thisProcess identifies this run of the gateway. Work that cannot survive a
// restart is invalidated by watching it change.
var thisProcess = mint("process")

// hostInfo is what this process can say about the machine it runs on without
// asking the operating system for anything it does not already know.
//
// Memory is required by the schema, so it is answered rather than omitted: a
// row without it is refused whole, which costs more than an imprecise number.
// What the Go runtime knows is this process's heap, not the host's RAM, and the
// two are named apart here so nobody reads one as the other — total is what the
// runtime has reserved from the OS, free is what it holds unused.
//
// Disk, load average and the CPU model are optional, and those stay out. Each
// needs a host-metrics reader — cgroup limits, statfs — which this cloud does
// not have, and the web UI renders a dash for a field that is absent.
type hostInfo struct {
	Machine  string `json:"machineName"`
	Hostname string `json:"hostname"`
	Platform string `json:"platform"`
	Release  string `json:"release"`
	Arch     string `json:"arch"`
	OS       string `json:"osLabel"`
	Runtime  string `json:"nodeVersion"`
	PID      int    `json:"pid"`
	Process  string `json:"processInstanceId"`
	UptimeMs int64  `json:"uptimeMs"`
	CPUCount int    `json:"cpuCount"`
	MemTotal uint64 `json:"memoryTotalBytes"`
	MemFree  uint64 `json:"memoryFreeBytes"`
}

func systemInfo(c *Call) (any, error) {
	if err := c.Bind(&struct{}{}); err != nil {
		return nil, err
	}
	name, err := os.Hostname()
	if err != nil {
		name = "unknown"
	}
	label := runtime.GOOS
	switch label {
	case "darwin":
		label = "macOS"
	case "linux":
		label = "Linux"
	case "windows":
		label = "Windows"
	}
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	return hostInfo{
		Machine:  strings.SplitN(name, ".", 2)[0],
		Hostname: name,
		Platform: runtime.GOOS,
		Arch:     runtime.GOARCH,
		OS:       label,
		Runtime:  runtime.Version(),
		PID:      os.Getpid(),
		Process:  thisProcess,
		UptimeMs: time.Since(c.svc.State.started).Milliseconds(),
		CPUCount: runtime.NumCPU(),
		MemTotal: mem.Sys,
		MemFree:  mem.Sys - mem.HeapInuse,
	}, nil
}
