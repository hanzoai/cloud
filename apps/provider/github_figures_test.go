package provider

// github_figures_test.go pins what the estate's collector may ask of GitHub
// through the App: the platform alone asks, the answer is facts about public
// repositories, and no credential is in it.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/authz"
	"github.com/zap-proto/zip"
)

// figuresGitHub serves the App's installations and GraphQL. node answers one
// repository by index; errs, when set, is the GraphQL errors array.
type figuresGitHub struct {
	listed, minted, asked atomic.Int32
	status                int
	node                  func(i int, owner, name string) any
	errs                  []map[string]any
	query                 string
	auth                  string
}

func (g *figuresGitHub) serve(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/app/installations":
			g.listed.Add(1)
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 7, "account": map[string]any{"login": "hanzoai", "type": "Organization"}}})
		case r.URL.Path == "/app/installations/7/access_tokens":
			g.minted.Add(1)
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"token": "ghs_inst_7", "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
		case r.URL.Path == "/graphql" && r.Method == http.MethodPost:
			g.asked.Add(1)
			g.auth = r.Header.Get("Authorization")
			if g.status != 0 {
				w.WriteHeader(g.status)
				return
			}
			var in struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			g.query = in.Query
			data := map[string]any{}
			for i := 0; ; i++ {
				o, ok := in.Variables["o"+itoa(i)].(string)
				if !ok {
					break
				}
				data["r"+itoa(i)] = g.node(i, o, in.Variables["n"+itoa(i)].(string))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "errors": g.errs})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	withGithubApp(t, srv)
	figureInstall.Store(0)
	t.Cleanup(func() { figureInstall.Store(0) })
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func node(name string, stars int64, blobs map[string]any) map[string]any {
	n := map[string]any{
		"nameWithOwner": name, "isPrivate": false, "stargazerCount": stars, "forkCount": 2,
		"watchers": map[string]any{"totalCount": 4}, "issues": map[string]any{"totalCount": 3},
		"f0": nil, "f1": nil, "f2": nil, "f3": nil,
	}
	for k, v := range blobs {
		n[k] = v
	}
	return n
}

func platform() context.Context { return cloud.For(context.Background(), authz.AdminOrg) }

func TestGitHubFigures_AnswersCountsAndManifests(t *testing.T) {
	newApp(t, newKMS(t))
	g := &figuresGitHub{node: func(i int, o, n string) any {
		switch n {
		case "ui":
			return node("hanzoai/UI", 412, map[string]any{
				"f0": map[string]any{"text": `{"name":"@hanzo/ui"}`, "isTruncated": false},
				"f3": map[string]any{"text": "[package]\nname = \"x\"\n", "isTruncated": false},
			})
		case "big":
			return node(o+"/"+n, 1, map[string]any{
				"f0": map[string]any{"text": `{"name":"cut"}`, "isTruncated": true},
				"f1": map[string]any{"text": strings.Repeat("x", maxManifest+1), "isTruncated": false},
			})
		case "private":
			p := node(o+"/"+n, 9, nil)
			p["isPrivate"] = true
			return p
		}
		return nil // not found
	}, errs: []map[string]any{{"type": "NOT_FOUND", "message": "Could not resolve", "path": []any{"r3"}}}}
	g.serve(t)

	got, err := planeGitHubFigures(platform(), &client.GitHubFiguresIn{Repos: []string{"hanzoai/ui", "hanzoai/big", "hanzoai/private", "hanzoai/gone"}})
	if err != nil {
		t.Fatalf("figures: %v", err)
	}
	if len(got.Repos) != 2 || got.Repos[0].Repo != "hanzoai/UI" || got.Repos[1].Repo != "hanzoai/big" {
		t.Fatalf("repos = %+v, want the two public ones, sorted", got.Repos)
	}
	ui := got.Repos[0]
	if ui.Stars != 412 || ui.Forks != 2 || ui.Issues != 3 || ui.Watchers != 4 {
		t.Fatalf("counts = %+v", ui)
	}
	if len(ui.Files) != 2 || ui.Files[0].Path != "package.json" || ui.Files[1].Path != "Cargo.toml" {
		t.Fatalf("files = %+v, want package.json then Cargo.toml", ui.Files)
	}
	if len(got.Repos[1].Files) != 0 {
		t.Fatalf("a truncated or oversized manifest was sent: %+v", got.Repos[1].Files)
	}
	if strings.Join(got.Missing, ",") != "hanzoai/private,hanzoai/gone" {
		t.Fatalf("missing = %v, want the private and the gone", got.Missing)
	}
	// Names ride as variables, and the token that read is an installation's.
	if strings.Contains(g.query, "hanzoai") || !strings.Contains(g.query, "$o0") || g.auth != "Bearer ghs_inst_7" {
		t.Fatalf("query %q auth %q", g.query, g.auth)
	}
	// No credential in the answer.
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "ghs_") {
		t.Fatalf("a token crossed: %s", b)
	}
}

// The installation that served is remembered: a run of questions lists the App's
// installations once and mints once.
func TestGitHubFigures_ListsInstallationsOnce(t *testing.T) {
	newApp(t, newKMS(t))
	g := &figuresGitHub{node: func(i int, o, n string) any { return node(o+"/"+n, 1, nil) }}
	g.serve(t)
	for range 3 {
		if _, err := planeGitHubFigures(platform(), &client.GitHubFiguresIn{Repos: []string{"hanzoai/ui"}}); err != nil {
			t.Fatal(err)
		}
	}
	if g.listed.Load() != 1 || g.minted.Load() != 1 || g.asked.Load() != 3 {
		t.Fatalf("listed %d, minted %d, asked %d; want 1, 1, 3", g.listed.Load(), g.minted.Load(), g.asked.Load())
	}
}

func TestGitHubFigures_IsThePlatformsAlone(t *testing.T) {
	newApp(t, newKMS(t))
	g := &figuresGitHub{node: func(i int, o, n string) any { return node(o+"/"+n, 1, nil) }}
	g.serve(t)
	for _, org := range []string{"acme", "", "hanzo"} {
		_, err := planeGitHubFigures(cloud.For(context.Background(), org), &client.GitHubFiguresIn{Repos: []string{"hanzoai/ui"}})
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusForbidden {
			t.Fatalf("org %q: err = %v, want 403", org, err)
		}
	}
	if g.listed.Load()+g.asked.Load() != 0 {
		t.Fatal("GitHub was reached for a caller that was refused")
	}
}

func TestGitHubFigures_RefusesWhatIsNotARepositoryList(t *testing.T) {
	newApp(t, newKMS(t))
	g := &figuresGitHub{node: func(i int, o, n string) any { return nil }}
	g.serve(t)
	hundredOne := make([]string, 101)
	for i := range hundredOne {
		hundredOne[i] = "hanzoai/r"
	}
	for name, repos := range map[string][]string{
		"none": nil, "a hundred and one": hundredOne,
		"no owner": {"ui"}, "a path": {"hanzoai/ui/x"}, "an injection": {`a/b"){x}`},
	} {
		_, err := planeGitHubFigures(platform(), &client.GitHubFiguresIn{Repos: repos})
		var he *zip.HTTPError
		if !errors.As(err, &he) || he.Status != http.StatusBadRequest {
			t.Fatalf("%s: err = %v, want 400", name, err)
		}
	}
}

// An error that is not one repository's is the question's failure, and a rate
// limit or an outage is a 502, never an empty answer.
func TestGitHubFigures_AFailureIsNotAnEmptyAnswer(t *testing.T) {
	for name, g := range map[string]*figuresGitHub{
		"rate limited":  {status: http.StatusForbidden},
		"unavailable":   {status: http.StatusBadGateway},
		"a query error": {node: func(i int, o, n string) any { return nil }, errs: []map[string]any{{"type": "RATE_LIMITED", "message": "slow down", "path": []any{}}}},
	} {
		t.Run(name, func(t *testing.T) {
			newApp(t, newKMS(t))
			g.serve(t)
			_, err := planeGitHubFigures(platform(), &client.GitHubFiguresIn{Repos: []string{"hanzoai/ui"}})
			var he *zip.HTTPError
			if !errors.As(err, &he) || he.Status != http.StatusBadGateway {
				t.Fatalf("err = %v, want 502", err)
			}
		})
	}
}
