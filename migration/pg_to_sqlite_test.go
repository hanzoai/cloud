package migration

import (
	"context"
	"database/sql"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hanzoai/cek"
	_ "github.com/hanzoai/cloud/internal/devmaster"
	"github.com/hanzoai/namespace"

	// github.com/hanzoai/sqlite is the ONE Hanzo SQLite driver: it registers
	// the "sqlite" database/sql name under both build tags (cgo →
	// mattn+SQLCipher, encrypted at rest; !cgo → pure-Go modernc). Importing
	// modernc directly instead would double-register "sqlite" under CGO and
	// panic at init. Blank import registers the driver.
	_ "github.com/hanzoai/sqlite"
)

// TestRoundTripPerOrgUserRouting exercises the introspective copy plus
// per-(org, user) routing using SQLite-as-source. The migrator's
// information_schema discovery is PG-specific; for the test we drive
// copyTable directly with a hand-rolled TableInfo, which is the same
// shape discoverTables would produce against a live PG.
//
// This proves the routing contract:
//
//   - rows with both org_id and user_id → /data/<org>/<user>/cloud.sqlite
//   - rows with only org_id            → /data/<org>/_org/cloud.sqlite
//   - rows with neither                → /data/_global/_org/cloud.sqlite
//
// And the parity contract: WrittenRows == SourceRows per table.
func TestRoundTripPerOrgUserRouting(t *testing.T) {
	dir := t.TempDir()
	srcPath := filepath.Join(dir, "src.sqlite")

	// The source stands in for POSTGRES and is read as a plain file, not a cek
	// store — it is deliberately not encrypted.
	src, err := sql.Open("sqlite", srcPath)
	if err != nil {
		t.Fatalf("open src: %v", err)
	}
	defer src.Close()

	// Three test tables, exercising each routing path.
	if _, err := src.Exec(`
CREATE TABLE projects (
    project_id TEXT,
    org_id TEXT,
    user_id TEXT,
    name TEXT,
    created_at TEXT
);
CREATE TABLE org_settings (
    org_id TEXT,
    setting_key TEXT,
    setting_value TEXT
);
CREATE TABLE legacy_audit (
    event_id TEXT,
    actor TEXT,
    payload TEXT
);
`); err != nil {
		t.Fatalf("create source schemas: %v", err)
	}

	// Seed:
	// - projects: 2 rows for (hanzo, z@hanzo.ai), 1 row for (lux, z@lux.network)
	// - org_settings: 1 row for hanzo, 1 row for lux
	// - legacy_audit: 1 row with no routing columns
	seeds := []struct {
		stmt string
		args []any
	}{
		{`INSERT INTO projects VALUES (?, ?, ?, ?, ?)`, []any{"p1", "hanzo", "z@hanzo.ai", "alpha", "2026-06-04T00:00:00Z"}},
		{`INSERT INTO projects VALUES (?, ?, ?, ?, ?)`, []any{"p2", "hanzo", "z@hanzo.ai", "beta", "2026-06-04T00:01:00Z"}},
		{`INSERT INTO projects VALUES (?, ?, ?, ?, ?)`, []any{"p3", "lux", "z@lux.network", "gamma", "2026-06-04T00:02:00Z"}},
		{`INSERT INTO org_settings VALUES (?, ?, ?)`, []any{"hanzo", "default_region", "sfo3"}},
		{`INSERT INTO org_settings VALUES (?, ?, ?)`, []any{"lux", "default_region", "sfo3"}},
		{`INSERT INTO legacy_audit VALUES (?, ?, ?)`, []any{"e1", "system", "{\"k\":\"v\"}"}},
	}
	for _, s := range seeds {
		if _, err := src.Exec(s.stmt, s.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	// Hand-build TableInfos (the discoverTables PG-side equivalent).
	tables := []TableInfo{
		{
			Schema: "", Name: "projects",
			Columns: []ColumnInfo{
				{Name: "project_id", DataType: "text"},
				{Name: "org_id", DataType: "text"},
				{Name: "user_id", DataType: "text"},
				{Name: "name", DataType: "text"},
				{Name: "created_at", DataType: "text"},
			},
			OrgCol: "org_id", UserCol: "user_id",
		},
		{
			Schema: "", Name: "org_settings",
			Columns: []ColumnInfo{
				{Name: "org_id", DataType: "text"},
				{Name: "setting_key", DataType: "text"},
				{Name: "setting_value", DataType: "text"},
			},
			OrgCol: "org_id", UserCol: "",
		},
		{
			Schema: "", Name: "legacy_audit",
			Columns: []ColumnInfo{
				{Name: "event_id", DataType: "text"},
				{Name: "actor", DataType: "text"},
				{Name: "payload", DataType: "text"},
			},
			OrgCol: "", UserCol: "",
		},
	}

	dstRoot := filepath.Join(dir, "data")
	pool := newDstPool(dstRoot)
	defer pool.Close()

	var reports []TableReport
	for _, ti := range tables {
		r, err := copyTable(context.Background(), src, pool, ti, 500)
		if err != nil {
			t.Fatalf("copy %s: %v", ti.Name, err)
		}
		reports = append(reports, r)
	}

	// Parity per table.
	for _, r := range reports {
		if r.SourceRows != r.WrittenRows {
			t.Errorf("%s: src=%d written=%d", r.Table, r.SourceRows, r.WrittenRows)
		}
	}

	// Close the pool BEFORE reading anything back. A dst handle owns an open
	// transaction until it is closed, and on the pure-Go encryption backend the
	// ciphertext at the real path is only written when the handle seals — so a
	// verification that reads while the writer is still open sees an unfinished
	// file. Closing here is also what the migration itself does before its output
	// is considered complete. The defer above stays as the error-path net; Close is
	// safe twice.
	pool.Close()

	// Count rows in the per-user file. Read the same keyed way the migration wrote
	// it: the dst databases are encrypted at rest, so a bare sql.Open cannot read
	// them, and naming them any other way would open an empty one.
	dst1, err := openDst(t, dstRoot, "hanzo", "z@hanzo.ai")
	if err != nil {
		t.Fatalf("open dst1: %v", err)
	}
	defer dst1.Close()
	var n int
	if err := dst1.QueryRow(`SELECT COUNT(*) FROM projects`).Scan(&n); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if n != 2 {
		t.Errorf("expected 2 projects rows under hanzo/z@hanzo.ai, got %d", n)
	}

	// org_settings row should land under _org sentinel.
	dst2, err := openDst(t, dstRoot, "hanzo", sentinelOrgUser)
	if err != nil {
		t.Fatalf("open dst2: %v", err)
	}
	defer dst2.Close()
	var orgN int
	if err := dst2.QueryRow(`SELECT COUNT(*) FROM org_settings`).Scan(&orgN); err != nil {
		t.Fatalf("count org_settings: %v", err)
	}
	if orgN != 1 {
		t.Errorf("expected 1 org_settings row under hanzo/_org, got %d", orgN)
	}

	// legacy_audit row should land under the _global sentinel.
	dst3, err := openDst(t, dstRoot, sentinelOrg, sentinelOrgUser)
	if err != nil {
		t.Fatalf("open dst3: %v", err)
	}
	defer dst3.Close()
	var globN int
	if err := dst3.QueryRow(`SELECT COUNT(*) FROM legacy_audit`).Scan(&globN); err != nil {
		t.Fatalf("count legacy_audit: %v", err)
	}
	if globN != 1 {
		t.Errorf("expected 1 legacy_audit row under _global/_org, got %d", globN)
	}

	// Routing report content.
	for _, r := range reports {
		if r.Table == `"projects"` {
			gotTargets := make([]string, 0, len(r.Targets))
			for _, tt := range r.Targets {
				gotTargets = append(gotTargets, tt.Org+"/"+tt.User)
			}
			sort.Strings(gotTargets)
			want := []string{"hanzo/z@hanzo.ai", "lux/z@lux.network"}
			if !equalStringSlices(gotTargets, want) {
				t.Errorf("projects targets = %v, want %v", gotTargets, want)
			}
		}
	}
}

func TestPickRoutingColumnsCanonicalNames(t *testing.T) {
	cols := []ColumnInfo{
		{Name: "id"},
		{Name: "org_id"},
		{Name: "user_id"},
	}
	o, u := pickRoutingColumns(cols)
	if o != "org_id" || u != "user_id" {
		t.Errorf("canonical pick: %q %q", o, u)
	}
}

func TestPickRoutingColumnsFallbacks(t *testing.T) {
	cols := []ColumnInfo{
		{Name: "id"},
		{Name: "owner"},
		{Name: "user_email"},
	}
	o, u := pickRoutingColumns(cols)
	if o != "owner" || u != "user_email" {
		t.Errorf("fallback pick: %q %q", o, u)
	}
}

func TestPickRoutingColumnsNoneFound(t *testing.T) {
	cols := []ColumnInfo{
		{Name: "id"},
		{Name: "name"},
	}
	o, u := pickRoutingColumns(cols)
	if o != "" || u != "" {
		t.Errorf("expected empty pair, got %q %q", o, u)
	}
}

func TestTokenizeRejectsPathTraversal(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{"hanzo", "hanzo"},
		{"../etc/passwd", ""},
		{"  hanzo  ", "hanzo"},
		{"z@hanzo.ai", "z@hanzo.ai"},
		{"/abs/path", ""},
		{nil, ""},
		{"  ", ""},
	}
	for _, c := range cases {
		if got := tokenize(c.in); got != c.want {
			t.Errorf("tokenize(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestMapPGTypeCoverage(t *testing.T) {
	cases := map[string]string{
		"smallint":         "INTEGER",
		"integer":          "INTEGER",
		"bigint":           "INTEGER",
		"bigserial":        "INTEGER",
		"boolean":          "INTEGER",
		"bool":             "INTEGER",
		"real":             "REAL",
		"double precision": "REAL",
		"numeric":          "REAL",
		"bytea":            "BLOB",
		"text":             "TEXT",
		"jsonb":            "TEXT",
		"uuid":             "TEXT",
		"timestamptz":      "TEXT",
	}
	for in, want := range cases {
		if got := mapPGType(in); got != want {
			t.Errorf("mapPGType(%q) = %q, want %q", in, got, want)
		}
	}
}

// A hostile org or user cannot leave the data directory. The property is NOT
// "rejected" — namespace folds every separator and dot into one safe segment and
// disambiguates it with a hash of the raw value, so a legacy PG row naming
// "../etc" gets its own database rather than an error — it is "cannot escape".
// Asserting the wrong one of those would be a test that passes for the wrong
// reason.
func TestDestinationNamespaceNeutralisesTraversal(t *testing.T) {
	for _, tc := range []struct{ org, user string }{
		{"../etc", "user"},
		{"org", "/abs"},
		{"..", ".."},
		{`a\b`, "c/d"},
	} {
		ns, err := destinationNamespace(tc.org, tc.user)
		if err != nil {
			continue // refused outright is also fine
		}
		path, err := namespace.Path("/data", ns, dstSubsystem)
		if err != nil {
			t.Fatalf("namespace.Path(%q): %v", ns, err)
		}
		if !strings.HasPrefix(path, "/data/") || strings.Contains(path, "..") {
			t.Errorf("(%q, %q) rendered to %q, which can leave the data directory", tc.org, tc.user, path)
		}
	}

	// The two sentinels are absences. A row with no user lands in the ORG's own
	// database; a row with neither lands in the deployment's, which is a different
	// KIND — so no org, however spelled, can be routed into it.
	orgNS, err := destinationNamespace("hanzo", sentinelOrgUser)
	if err != nil {
		t.Fatalf("org-scoped sentinel: %v", err)
	}
	if orgNS.Group().String() != "" {
		t.Errorf("the org-scoped sentinel became a group %q — it is an absence, not a name", orgNS.Group())
	}
	globalNS, err := destinationNamespace(sentinelOrg, sentinelOrgUser)
	if err != nil {
		t.Fatalf("global sentinel: %v", err)
	}
	if globalNS != namespace.System() {
		t.Errorf("the global sentinel named %q, want the system namespace", globalNS)
	}
	if globalNS == orgNS {
		t.Error("an org was routed into the deployment's own database")
	}
}

func TestRunRequiresDSNAndRoot(t *testing.T) {
	if _, err := Run(context.Background(), Options{DstRoot: "/tmp/x"}); err == nil {
		t.Error("expected error for missing SrcDSN")
	}
	if _, err := Run(context.Background(), Options{SrcDSN: "postgres://"}); err == nil {
		t.Error("expected error for missing DstRoot")
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// openDst opens a destination database the way the migration wrote it: same
// namespace, same subsystem, same root. Any other spelling opens an empty file
// and the assertion that follows would be meaningless.
func openDst(t *testing.T, root, org, user string) (*sql.DB, error) {
	t.Helper()
	ns, err := destinationNamespace(org, user)
	if err != nil {
		t.Fatalf("destinationNamespace(%q, %q): %v", org, user, err)
	}
	return cek.Open(ns, dstSubsystem, root)
}
