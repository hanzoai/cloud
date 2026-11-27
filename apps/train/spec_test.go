package train

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/hanzoai/cloud/internal/sqltest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/locus"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/zap-proto/zip"
)

type arts map[string]map[string]any

func (a arts) lookup(_ context.Context, kind, key string) (map[string]any, bool, error) {
	m, ok := a[kind+":"+key]
	return m, ok, nil
}

// kai is a job Hanzo runs, inside a budget.
func kai() Spec {
	return Spec{BaseModel: "kai", Revision: "a7", Dataset: &Dataset{URI: "stage:a11"}, Resources: Resources{Budget: 100}}
}

func code(t *testing.T, err error) (string, []string) {
	t.Helper()
	var he *zip.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("want a problem, got %v", err)
	}
	if he.Status != 400 {
		t.Fatalf("status %d, want 400", he.Status)
	}
	var sup []string
	if s, ok := he.Detail["supported"].([]string); ok {
		sup = s
	}
	return he.Code, sup
}

const (
	shaA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	shaB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func TestARefusalNamesItsCodeAndWhatIsSupported(t *testing.T) {
	store := arts{
		"lora:" + shaA:                          {"base_model": "kai", "revision": "a7", "targets": "[q]"},
		"lora:" + shaB:                          {"base_model": "kai", "revision": "a6", "targets": "[q]"},
		"basis:basis://zen7/math":               {"base_model": "zen7", "address": "basis://zen7/math"},
		"basis:basis://kai/triage":              {"base_model": "kai", "address": "basis://kai/triage"},
		"capability:" + shaA:                    {"base_model": "kai"},
		"capability:" + strings.Repeat("d", 64): {"base_model": "zen7"},
	}
	cases := []struct {
		name  string
		noun  string
		edit  func(*Spec)
		code  string
		wants []string
	}{
		{"no base", "jobs", func(s *Spec) { s.BaseModel = "" }, "invalid_request", nil},
		{"an llm job", "jobs", func(s *Spec) { s.BaseModel = "zen7" }, "unsupported_base", []string{"kai"}},
		{"a kai client", "clients", func(s *Spec) {}, "unsupported_base", []string{"engine"}},
		{"lora on kai", "jobs", func(s *Spec) { s.Adaptation.Mode = "lora" }, "unsupported_adaptation", []string{"full", "readout", "auto"}},
		{"qlora on kai", "jobs", func(s *Spec) { s.Adaptation.Mode = "qlora" }, "unsupported_adaptation", nil},
		{"an old mode", "jobs", func(s *Spec) { s.Adaptation.Mode = "orthogonal_subspace" }, "invalid_request", nil},
		{"a basis with nothing to build it from", "jobs", func(s *Spec) { s.Adaptation.Mode = "basis" }, "invalid_request", nil},
		{"a basis and its sources both", "jobs", func(s *Spec) {
			s.Adaptation = Adaptation{Mode: "basis", Basis: "basis://kai/triage", Sources: []string{shaA}}
		}, "invalid_request", nil},
		{"a residual outside basis", "jobs", func(s *Spec) { s.Adaptation = Adaptation{Mode: "full", ResidualRank: 4} }, "invalid_request", nil},
		{"protect naming nothing", "jobs", func(s *Spec) { s.Protect = &Protect{Projection: &Projection{}} }, "invalid_request", nil},
		{"distillation with no suites", "jobs", func(s *Spec) {
			s.Protect = &Protect{Capabilities: []string{shaA}, Distillation: true}
		}, "invalid_request", nil},
		{"a capability kai cannot project off yet", "jobs", func(s *Spec) {
			s.Protect = &Protect{Capabilities: []string{shaA}, Projection: &Projection{}}
		}, "unsupported_protect", []string{"projection", "distillation"}},
		{"a capability of another base", "jobs", func(s *Spec) {
			s.Protect = &Protect{Capabilities: []string{strings.Repeat("d", 64)}, Projection: &Projection{}}
		}, "incompatible_basis", nil},
		{"a capability this org lacks", "jobs", func(s *Spec) {
			s.Protect = &Protect{Capabilities: []string{shaB}}
		}, "invalid_request", nil},
		{"a basis output on kai", "jobs", func(s *Spec) { s.Output.Kind = "basis" }, "unsupported_output", []string{"capability", "checkpoint"}},
		{"merged on kai", "jobs", func(s *Spec) { s.Output.Kind = "merged" }, "unsupported_output", nil},
		{"another loss", "jobs", func(s *Spec) { s.Objective.Loss = "focal" }, "unsupported_objective", []string{"cross_entropy"}},
		{"a negative weight", "jobs", func(s *Spec) { s.Objective.Terms = []Term{{Kind: "pairwise_margin", Weight: -1}} }, "invalid_request", nil},
		{"a term twice", "jobs", func(s *Spec) {
			s.Objective.Terms = []Term{{Kind: "consistency", Weight: 1}, {Kind: "consistency", Weight: 2}}
		}, "invalid_request", nil},
		{"sources on different revisions", "jobs", func(s *Spec) {
			s.Adaptation = Adaptation{Mode: "basis", Sources: []string{shaA, shaB}}
		}, "incompatible_adapters", nil},
		{"a source this org lacks", "jobs", func(s *Spec) {
			s.Adaptation = Adaptation{Mode: "basis", Sources: []string{strings.Repeat("c", 64)}}
		}, "invalid_request", nil},
		{"one source, run nowhere", "jobs", func(s *Spec) {
			s.Adaptation = Adaptation{Mode: "basis", Sources: []string{shaA}, ResidualRank: 8}
		}, "unsupported_adaptation", nil},
		{"another base's basis", "jobs", func(s *Spec) {
			s.Adaptation = Adaptation{Mode: "basis", Basis: "basis://zen7/math"}
		}, "incompatible_basis", nil},
		{"a basis of this base, run nowhere", "jobs", func(s *Spec) {
			s.Adaptation = Adaptation{Mode: "basis", Basis: "basis://kai/triage", Rank: 16}
		}, "unsupported_adaptation", nil},
		{"a hub revision without a rev", "jobs", func(s *Spec) { s.Revision = "hub:hanzoai/kai-1" }, "invalid_request", nil},
		{"a kai job on hf data", "jobs", func(s *Spec) { s.Dataset.URI = "hf://org/set" }, "invalid_request", []string{"stage", "artifact"}},
		{"a dataset this org never uploaded", "jobs", func(s *Spec) { s.Dataset.URI = "artifact:" + shaA }, "invalid_request", nil},
		{"a device the base runs nowhere", "jobs", func(s *Spec) { s.Resources.Devices = []string{"tpu"} }, "invalid_request", nil},
		{"a device twice", "jobs", func(s *Spec) { s.Resources.Devices = []string{"cuda", "cuda"} }, "invalid_request", nil},
		{"a budget on the org's own machines", "jobs", func(s *Spec) {
			s.Resources.Machines, s.Resources.Budget = []string{"dgx"}, 100
		}, "invalid_request", nil},
		{"a job Hanzo runs with no budget", "jobs", func(s *Spec) { s.Resources.Budget = 0 }, "budget_required", nil},
		{"nine splits", "jobs", func(s *Spec) {
			s.Dataset.Splits = map[string]string{}
			for i := range 9 {
				s.Dataset.Splits[string(rune('a'+i))] = "s"
			}
		}, "invalid_request", nil},
		{"a negative cap", "jobs", func(s *Spec) { s.Resources.MaxSeconds = -1 }, "invalid_request", nil},
		{"from, run nowhere", "jobs", func(s *Spec) { s.From = shaA }, "invalid_request", nil},
		{"too many machines", "jobs", func(s *Spec) {
			s.Resources.Machines = []string{"a", "b", "c", "d", "e", "f", "g", "h", "i"}
		}, "invalid_request", nil},
		{"a machine twice", "jobs", func(s *Spec) { s.Resources.Machines = []string{"dgx", "dgx"} }, "invalid_request", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := kai()
			c.edit(&s)
			got, sup := code(t, s.validate(context.Background(), c.noun, store))
			if got != c.code {
				t.Fatalf("code %q, want %q", got, c.code)
			}
			if c.wants != nil && !slices.Equal(sup, c.wants) {
				t.Fatalf("supported %v, want %v", sup, c.wants)
			}
		})
	}
}

func TestProjectionTakesAStrength(t *testing.T) {
	for _, c := range []struct {
		in   *float64
		want float64
		ok   bool
	}{{nil, 1, true}, {ptr(0), 0, true}, {ptr(0.5), 0.5, true}, {ptr(1.5), 0, false}, {ptr(-0.1), 0, false}} {
		s := kai()
		s.Protect = &Protect{Suites: []string{"x"}, Projection: &Projection{Strength: c.in}}
		err := s.validate(context.Background(), "jobs", arts{})
		if (err == nil) != c.ok {
			t.Fatalf("strength %v: %v", c.in, err)
		}
		if c.ok && *s.Protect.Projection.Strength != c.want {
			t.Fatalf("strength %v became %v", c.in, *s.Protect.Projection.Strength)
		}
	}
}

func ptr(x float64) *float64 { return &x }

func newStore(t *testing.T) (*store, context.Context) {
	t.Helper()
	stores := cloud.NewOrgStoreAt(cloud.Base{DataDir: t.TempDir()}, subsystem, openStore(testMaster))
	st, err := stores.For(principal.MustOrgNamespace("acme", ""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stores.CloseAll() })
	return st, context.Background()
}

func TestAStoredBooleanProjectionIsReshaped(t *testing.T) {
	sqltest.Engines(t, func(t *testing.T, _ sqltest.Open) {
		st, _ := newStore(t)
		ctx := context.Background()
		for id, p := range map[string]string{"job_a": `true`, "job_b": `false`} {
			spec := `{"base_model":"kai","protect":{"suites":["x"],"projection":` + p + `}}`
			if _, err := st.db.Exec(`INSERT INTO jobs (`+jobCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, id, "acme", "default", "kai", spec, queued, "", "", "", "u", "", bit(false), 1, 0, 0); err != nil {
				t.Fatal(err)
			}
		}
		secs := `{"base_model":"kai","resources":{"seconds":600,"machines":["m1"]}}`
		if _, err := st.db.Exec(`INSERT INTO jobs (`+jobCols+`) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`, "job_c", "acme", "default", "kai", secs, queued, "", "", "", "u", "", bit(false), 1, 0, 0); err != nil {
			t.Fatal(err)
		}
		if _, err := openStore(testMaster)(principal.MustOrgNamespace("acme", ""), st.db); err != nil {
			t.Fatal(err)
		}
		r, err := st.job(ctx, "job_c")
		if err != nil {
			t.Fatal(err)
		}
		var legacy Spec
		if err := json.Unmarshal([]byte(r.Spec), &legacy); err != nil || legacy.Resources.MaxSeconds != 600 || strings.Contains(r.Spec, `"seconds"`) {
			t.Fatalf("resources.seconds became %s (%v), want max_seconds 600", r.Spec, err)
		}
		for id, want := range map[string]string{"job_a": `{"strength":1}`, "job_b": ``} {
			r, err := st.job(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			var s Spec
			if err := json.Unmarshal([]byte(r.Spec), &s); err != nil {
				t.Fatalf("%s: %v", id, err)
			}
			got := ""
			if s.Protect.Projection != nil {
				b, _ := json.Marshal(s.Protect.Projection)
				got = string(b)
			}
			if got != want {
				t.Fatalf("%s: projection %s, want %s", id, got, want)
			}
		}
	})
}

func TestAutoSaysWhatItChoseAndWhy(t *testing.T) {
	s := kai()
	if err := s.validate(context.Background(), "jobs", arts{}); err != nil {
		t.Fatal(err)
	}
	if s.Adaptation.Mode != "full" || s.Adaptation.Chose == nil || s.Adaptation.Chose.Mode != "full" || s.Adaptation.Chose.Why == "" {
		t.Fatalf("auto: %+v", s.Adaptation)
	}
	s = kai()
	s.Protect = &Protect{Suites: []string{"typed_decisions"}, Projection: &Projection{}, Distillation: true}
	if err := s.validate(context.Background(), "jobs", arts{}); err != nil {
		t.Fatal(err)
	}
	if s.Adaptation.Mode != "full" || *s.Protect.Budget != defaultBudget || s.Output.Kind != "capability" || s.Objective.Loss != "cross_entropy" {
		t.Fatalf("defaults: %+v budget %+v output %q loss %q", s.Adaptation, *s.Protect.Budget, s.Output.Kind, s.Objective.Loss)
	}
	s = kai()
	s.Adaptation.Mode = "full"
	if err := s.validate(context.Background(), "jobs", arts{}); err != nil || s.Adaptation.Chose != nil {
		t.Fatalf("an explicit mode reports no choice: %+v %v", s.Adaptation, err)
	}
}

func TestAChoiceASpecAcceptsIsOneTheCatalogLists(t *testing.T) {
	for _, f := range families {
		for _, noun := range f.Nouns {
			for _, m := range f.Adaptations {
				s := Spec{BaseModel: f.Base, Adaptation: Adaptation{Mode: m}}
				if f.Base == "kai" {
					s.Revision, s.Dataset, s.Resources.Budget = "a7", &Dataset{URI: "stage:a"}, 100
				}
				if f.Base == "kai" {
					s.Protect = &Protect{Suites: []string{"x"}, Projection: &Projection{}, Distillation: true}
				}
				if err := s.validate(context.Background(), noun, arts{}); err != nil {
					t.Fatalf("%s %s %s: %v", f.Base, noun, m, err)
				}
			}
		}
	}
	raw, _ := json.Marshal(families)
	if !strings.Contains(string(raw), `"base":"kai"`) {
		t.Fatalf("catalog: %s", raw)
	}
}

// A FILE MADE BEFORE A COLUMN EXISTED GAINS IT, and keeps its rows. Only a SQLite file
// can be that old: hanzo-sql was never given the older shapes.
func TestAnOlderFileGainsTheColumnsItLacks(t *testing.T) {
	db, err := locus.Open(principal.MustOrgNamespace("acme", ""), "train", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, old := range []string{
		`CREATE TABLE jobs (id TEXT PRIMARY KEY, project TEXT NOT NULL, base_model TEXT NOT NULL, spec TEXT NOT NULL, status TEXT NOT NULL,
  error TEXT NOT NULL DEFAULT '', result TEXT NOT NULL DEFAULT '', published TEXT NOT NULL DEFAULT '', created_by TEXT NOT NULL,
  created INTEGER NOT NULL, started INTEGER NOT NULL DEFAULT 0, ended INTEGER NOT NULL DEFAULT 0)`,
		`CREATE TABLE tasks (id TEXT PRIMARY KEY, job TEXT NOT NULL, role TEXT NOT NULL, machine TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
  lease TEXT NOT NULL DEFAULT '', holder TEXT NOT NULL DEFAULT '', host TEXT NOT NULL DEFAULT '', coordinator TEXT NOT NULL DEFAULT '', seen INTEGER NOT NULL DEFAULT 0,
  created INTEGER NOT NULL)`,
		`INSERT INTO jobs (id, project, base_model, spec, status, created_by, created) VALUES ('job_old', 'default', 'kai', '{"base_model":"kai"}', 'queued', 'u', 7)`,
		`INSERT INTO tasks (id, job, role, status, created) VALUES ('tsk_old', 'job_old', 'lead', 'open', 7)`,
	} {
		if _, err := db.Exec(old); err != nil {
			t.Fatal(err)
		}
	}
	st, err := openStore(testMaster)(principal.MustOrgNamespace("acme", ""), db)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r, err := st.job(context.Background(), "job_old")
	if err != nil || r.Created != 7 || r.Org != "" || r.Payer != "" || r.Validated {
		t.Fatalf("the old job read as %+v (%v)", r, err)
	}
	ts, err := st.tasks(context.Background(), "job_old")
	if err != nil || len(ts) != 1 || ts[0].Class != "" || ts[0].Devices != 0 || ts[0].Held != 0 {
		t.Fatalf("the old task read as %+v (%v)", ts, err)
	}
}
