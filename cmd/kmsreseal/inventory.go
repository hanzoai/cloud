package main

// inventory.go — the CR-driven manifest. The ~125 KMSSecret CRs
// (secrets.lux.network/v1) are the AUTHORITATIVE (org, path, env, key) source for
// the re-seal migration: the legacy standalone ZapDB is unreadable by cloud and
// carries no org attribution, so the migration is driven by the CRs, never by a
// store copy.
//
// This file parses the CRs and expands each into a set of Targets — one per
// (org, path, env, key). It is PURE (no I/O): a []byte of CR JSON in, a validated
// Inventory out, so it is unit-testable against synthetic fixtures covering every
// edge the live fleet exhibits (explicit keys, folder-sync with empty keys[],
// absent envSlug, duplicate targets across CRs, malformed scope).
//
// Coordinate validity reuses cloud/clients/kms's OWN boundary validators
// (ValidSegment / ValidSubpath) so the tool can never construct a coordinate the
// embedded /v1/kms store would reject — one rule, both sides.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/hanzoai/cloud/clients/kms"
)

// defaultEnv mirrors the standalone's REST default: a secret whose CR omits
// envSlug was written and is read under "default". cloud REFUSES an empty env on
// write, so the tool ALWAYS passes an explicit env — effectiveEnv resolves it.
const defaultEnv = "default"

// ── CR wire shapes (the subset of the KMSSecret spec the tool consumes) ─────────

// crEnvelope accepts BOTH shapes kubectl emits: a List (`{"items":[...]}`) and a
// bare array of items (`[...]`), so a fixture or a `kubectl get -o json` both parse.
type crEnvelope struct {
	Items []cr `json:"items"`
}

type cr struct {
	Metadata crMeta `json:"metadata"`
	Spec     crSpec `json:"spec"`
}

type crMeta struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type crSpec struct {
	HostAPI                string    `json:"hostAPI"`
	Authentication         crAuth    `json:"authentication"`
	ManagedSecretReference crManaged `json:"managedSecretReference"`
}

type crAuth struct {
	UniversalAuth crUniversal `json:"universalAuth"`
}

type crUniversal struct {
	CredentialsRef crCredRef `json:"credentialsRef"`
	SecretsScope   crScope   `json:"secretsScope"`
}

type crCredRef struct {
	SecretName      string `json:"secretName"`
	SecretNamespace string `json:"secretNamespace"`
}

type crScope struct {
	ProjectSlug string   `json:"projectSlug"`
	EnvSlug     string   `json:"envSlug"`
	SecretsPath string   `json:"secretsPath"`
	Keys        []string `json:"keys"`
}

type crManaged struct {
	SecretName      string `json:"secretName"`
	SecretNamespace string `json:"secretNamespace"`
	CreationPolicy  string `json:"creationPolicy"`
	SecretType      string `json:"secretType"`
}

// ── Target: one (org, path, env, key) coordinate + provenance ───────────────────

// Target is a single secret coordinate to migrate, plus the provenance the RUN
// needs (which CR, which host, which credential). Provenance is NEVER part of the
// storage key — org/path/env/key alone address the record on both KMS faces.
//
// A Folder target (Key == "", Folder == true) comes from a CR with an empty keys[]
// (folder-sync): its concrete keys are discovered at RUN time by LISTing the path
// on the source, because the CR does not enumerate them.
type Target struct {
	Org    string // projectSlug — folded into /orgs/{org} by cloud, checked against the token owner
	Path   string // secretsPath, slash-trimmed (root => "")
	Env    string // effective env (envSlug or "default"); never empty
	Key    string // secret name; "" for a Folder target
	Folder bool   // true => CR enumerated no keys; resolve via LIST at RUN time

	// Provenance (reporting + credential resolution only).
	CRNamespace string
	CRName      string
	HostAPI     string
	CredName    string
	CredNS      string
	ManagedName string
	ManagedNS   string
	Policy      string
}

// Coord is the deduplication/verification key: the record address, independent of
// which CR referenced it. Two CRs that name the same (org, path, env, key) collide
// on ONE Coord — an idempotent upsert, migrated once.
type Coord struct {
	Org, Path, Env, Key string
}

func (t Target) Coord() Coord { return Coord{t.Org, t.Path, t.Env, t.Key} }

func (c Coord) String() string {
	return fmt.Sprintf("org=%s path=/%s env=%s key=%s", c.Org, c.Path, c.Env, c.Key)
}

// ── Inventory: the validated manifest + its dry-run statistics ──────────────────

type Inventory struct {
	CRCount   int
	Targets   []Target  // explicit-key targets, deduped by Coord (Folder targets excluded)
	Folders   []Target  // folder-sync targets (empty keys[]) — resolved at RUN time
	Malformed []Problem // CRs/keys rejected by validation (fail-loud, never silently dropped)

	// Stats (dry-run report).
	Orgs        []string
	Envs        map[string]int
	Hosts       map[string]int
	Duplicates  map[Coord]int // Coords referenced by >1 CR (idempotent upserts)
	RowsTotal   int           // every (CR × key) row before dedup
	UniqueCount int           // len(Targets)
}

// Problem records a rejected CR or key with a human reason — surfaced in the
// report so nothing is silently skipped.
type Problem struct {
	CRNamespace string
	CRName      string
	Reason      string
	Detail      string
}

// parseCRs unmarshals CR JSON in either List or bare-array form.
func parseCRs(data []byte) ([]cr, error) {
	trimmed := strings.TrimSpace(string(data))
	if strings.HasPrefix(trimmed, "[") {
		var items []cr
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, fmt.Errorf("parse CR array: %w", err)
		}
		return items, nil
	}
	var env crEnvelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, fmt.Errorf("parse CR list: %w", err)
	}
	return env.Items, nil
}

// BuildInventory expands, validates, and deduplicates the CRs into an Inventory.
// It never returns an error for a single bad CR — a malformed CR is recorded in
// Malformed (fail-loud in the report) while the rest proceed, because one broken
// CR must not block migrating the other 124.
func BuildInventory(crs []cr) Inventory {
	inv := Inventory{
		CRCount: len(crs),
		Envs:    map[string]int{},
		Hosts:   map[string]int{},
	}
	orgSet := map[string]struct{}{}
	seen := map[Coord]int{}
	var ordered []Target

	for _, c := range crs {
		sc := c.Spec.Authentication.UniversalAuth.SecretsScope
		cref := c.Spec.Authentication.UniversalAuth.CredentialsRef
		org := strings.TrimSpace(sc.ProjectSlug)
		if !validOrg(org) {
			inv.Malformed = append(inv.Malformed, Problem{c.Metadata.Namespace, c.Metadata.Name,
				"invalid projectSlug (org)", fmt.Sprintf("%q", sc.ProjectSlug)})
			continue
		}
		path := normPath(sc.SecretsPath)
		if !kms.ValidSubpath(path) {
			inv.Malformed = append(inv.Malformed, Problem{c.Metadata.Namespace, c.Metadata.Name,
				"invalid secretsPath", fmt.Sprintf("%q", sc.SecretsPath)})
			continue
		}
		env := effectiveEnv(sc.EnvSlug)
		if !kms.ValidSegment(env, kms.MaxEnvLen) {
			inv.Malformed = append(inv.Malformed, Problem{c.Metadata.Namespace, c.Metadata.Name,
				"invalid envSlug", fmt.Sprintf("%q", sc.EnvSlug)})
			continue
		}

		base := Target{
			Org: org, Path: path, Env: env,
			CRNamespace: c.Metadata.Namespace, CRName: c.Metadata.Name,
			HostAPI:  strings.TrimSpace(c.Spec.HostAPI),
			CredName: cref.SecretName, CredNS: firstNonEmpty(cref.SecretNamespace, c.Metadata.Namespace),
			ManagedName: c.Spec.ManagedSecretReference.SecretName,
			ManagedNS:   firstNonEmpty(c.Spec.ManagedSecretReference.SecretNamespace, c.Metadata.Namespace),
			Policy:      c.Spec.ManagedSecretReference.CreationPolicy,
		}

		orgSet[org] = struct{}{}
		inv.Hosts[base.HostAPI]++

		keys := nonEmptyKeys(sc.Keys)
		if len(keys) == 0 {
			// Folder-sync: the CR enumerated no keys. Concrete keys are discovered by
			// LISTing the path at RUN time. Counted once as a folder unit.
			f := base
			f.Folder = true
			inv.Folders = append(inv.Folders, f)
			inv.Envs[env]++
			continue
		}
		for _, k := range keys {
			if !kms.ValidSegment(k, kms.MaxNameLen) {
				inv.Malformed = append(inv.Malformed, Problem{c.Metadata.Namespace, c.Metadata.Name,
					"invalid key name", fmt.Sprintf("%q", k)})
				continue
			}
			inv.RowsTotal++
			inv.Envs[env]++
			t := base
			t.Key = k
			coord := t.Coord()
			if n, ok := seen[coord]; ok {
				seen[coord] = n + 1
				continue // idempotent: same record referenced by another CR
			}
			seen[coord] = 1
			ordered = append(ordered, t)
		}
	}

	inv.Targets = ordered
	inv.UniqueCount = len(ordered)
	inv.Duplicates = map[Coord]int{}
	for coord, n := range seen {
		if n > 1 {
			inv.Duplicates[coord] = n
		}
	}
	for o := range orgSet {
		inv.Orgs = append(inv.Orgs, o)
	}
	sort.Strings(inv.Orgs)
	return inv
}

// effectiveEnv resolves the env the tool passes explicitly on BOTH the source read
// and the cloud write. An absent envSlug maps to the standalone's "default"
// (the env the record was written under); a present one is used verbatim. cloud
// refuses an empty env on write, so this is never "".
func effectiveEnv(envSlug string) string {
	if e := strings.TrimSpace(envSlug); e != "" {
		return e
	}
	return defaultEnv
}

// normPath trims the secretsPath to the store's canonical form: no leading/trailing
// slash. The CR writes "/admin-guard-secrets"; the standalone stores it (and the
// operator reads it) as "admin-guard-secrets" (the leading slash is trimmed so the
// path does not smuggle an empty leading segment that the store rejects). cloud
// folds it under /orgs/{org}/<path>. Root ("/" or "") normalizes to "".
func normPath(secretsPath string) string {
	return strings.Trim(strings.TrimSpace(secretsPath), "/")
}

// nonEmptyKeys drops blank entries and trims; an all-blank list is treated as
// folder-sync (len 0).
func nonEmptyKeys(keys []string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if kk := strings.TrimSpace(k); kk != "" {
			out = append(out, kk)
		}
	}
	return out
}

// validOrg accepts the same DNS-1123-ish org label cloud's /v1/kms guard enforces
// (letters/digits/'-'/'_', ≤63 bytes). It is the tenant-isolation boundary folded
// into the store path, so it is validated strictly.
func validOrg(org string) bool {
	if org == "" || len(org) > 63 {
		return false
	}
	for _, r := range org {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}
