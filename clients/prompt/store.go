package prompt

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	// modernc.org/sqlite — the pure-Go SQLite driver already in the cloud module
	// (same one provisioning uses). No cgo; all-SQLite by construction.
	_ "modernc.org/sqlite"
)

// Store is the per-deployment prompts store: an org-scoped, versioned prompt
// registry in a single SQLite file. Prompts are a cloud-native primitive here —
// the console (Langfuse) delegates /api/public/v2/prompts → /v1/prompts, so cloud
// owns the data rather than proxying back into a redirect loop.
type Store struct{ db *sql.DB }

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	// Single connection: low-volume registry, every write atomic against the
	// file lock (mirrors provisioning's store).
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA busy_timeout=5000",
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("pragma %q: %w", pragma, err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS prompts (
		org            TEXT    NOT NULL,
		name           TEXT    NOT NULL,
		version        INTEGER NOT NULL,
		type           TEXT    NOT NULL DEFAULT 'text',
		labels         TEXT    NOT NULL DEFAULT '[]',
		tags           TEXT    NOT NULL DEFAULT '[]',
		prompt         TEXT    NOT NULL DEFAULT '',
		config         TEXT    NOT NULL DEFAULT '{}',
		commit_message TEXT,
		created_at     TEXT    NOT NULL,
		updated_at     TEXT    NOT NULL,
		PRIMARY KEY (org, name, version)
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("prompts schema: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// PromptMeta mirrors the console2 FE shape (one row per prompt name).
type PromptMeta struct {
	Name          string   `json:"name"`
	Versions      []int    `json:"versions"`
	Type          string   `json:"type"`
	Labels        []string `json:"labels"`
	Tags          []string `json:"tags"`
	LastUpdatedAt string   `json:"lastUpdatedAt"`
}

// PromptVersion is a single version of a named prompt (the detail payload).
type PromptVersion struct {
	Name          string          `json:"name"`
	Version       int             `json:"version"`
	Type          string          `json:"type"`
	Labels        []string        `json:"labels"`
	Tags          []string        `json:"tags"`
	Prompt        string          `json:"prompt"`
	Config        json.RawMessage `json:"config,omitempty"`
	CommitMessage string          `json:"commitMessage,omitempty"`
	CreatedAt     string          `json:"createdAt"`
	UpdatedAt     string          `json:"updatedAt"`
}

func decodeStrArr(s string) []string {
	var a []string
	if json.Unmarshal([]byte(s), &a) != nil || a == nil {
		return []string{}
	}
	return a
}

func encodeStrArr(a []string) string {
	if a == nil {
		a = []string{}
	}
	b, _ := json.Marshal(a)
	return string(b)
}

// List returns one PromptMeta per name for the org (all versions aggregated;
// latest version supplies the metadata). Honest empty slice when none exist.
func (s *Store) List(org string) ([]PromptMeta, error) {
	rows, err := s.db.Query(
		`SELECT name, version, type, labels, tags, updated_at
		   FROM prompts WHERE org = ? ORDER BY name, version`, org)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byName := map[string]*PromptMeta{}
	order := []string{}
	for rows.Next() {
		var name, typ, labels, tags, upd string
		var ver int
		if err := rows.Scan(&name, &ver, &typ, &labels, &tags, &upd); err != nil {
			return nil, err
		}
		m, ok := byName[name]
		if !ok {
			m = &PromptMeta{Name: name, Versions: []int{}}
			byName[name] = m
			order = append(order, name)
		}
		m.Versions = append(m.Versions, ver)
		// Rows are ascending by version, so the last write wins = latest metadata.
		m.Type = typ
		m.Labels = decodeStrArr(labels)
		m.Tags = decodeStrArr(tags)
		m.LastUpdatedAt = upd
	}
	out := make([]PromptMeta, 0, len(order))
	for _, n := range order {
		out = append(out, *byName[n])
	}
	return out, rows.Err()
}

// Get returns every version of a named prompt, newest first (empty when absent).
func (s *Store) Get(org, name string) ([]PromptVersion, error) {
	rows, err := s.db.Query(
		`SELECT name, version, type, labels, tags, prompt, config, commit_message, created_at, updated_at
		   FROM prompts WHERE org = ? AND name = ? ORDER BY version DESC`, org, name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []PromptVersion{}
	for rows.Next() {
		var p PromptVersion
		var labels, tags, config string
		var commit sql.NullString
		if err := rows.Scan(&p.Name, &p.Version, &p.Type, &labels, &tags, &p.Prompt, &config, &commit, &p.CreatedAt, &p.UpdatedAt); err != nil {
			return nil, err
		}
		p.Labels = decodeStrArr(labels)
		p.Tags = decodeStrArr(tags)
		p.Config = json.RawMessage(config)
		p.CommitMessage = commit.String
		out = append(out, p)
	}
	return out, rows.Err()
}

// Create inserts the next version of a named prompt (auto-incrementing per name)
// and returns the stored version.
func (s *Store) Create(org string, p PromptVersion) (PromptVersion, error) {
	var maxv sql.NullInt64
	if err := s.db.QueryRow(`SELECT MAX(version) FROM prompts WHERE org = ? AND name = ?`, org, p.Name).Scan(&maxv); err != nil {
		return p, err
	}
	p.Version = int(maxv.Int64) + 1
	now := time.Now().UTC().Format(time.RFC3339)
	p.CreatedAt, p.UpdatedAt = now, now
	if p.Type == "" {
		p.Type = "text"
	}
	config := "{}"
	if len(p.Config) > 0 {
		config = string(p.Config)
	}
	_, err := s.db.Exec(
		`INSERT INTO prompts (org, name, version, type, labels, tags, prompt, config, commit_message, created_at, updated_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		org, p.Name, p.Version, p.Type, encodeStrArr(p.Labels), encodeStrArr(p.Tags), p.Prompt, config, p.CommitMessage, now, now)
	return p, err
}
