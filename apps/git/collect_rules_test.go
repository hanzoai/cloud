package git

import (
	"reflect"
	"testing"

	"github.com/hanzoai/cloud/client"
)

func TestManifestPackages(t *testing.T) {
	file := func(path, text string) client.GitHubFile { return client.GitHubFile{Path: path, Text: text} }
	for _, c := range []struct {
		name  string
		files []client.GitHubFile
		want  []packageRef
	}{
		{"npm", []client.GitHubFile{file("package.json", `{"name":"@hanzo/ui","version":"1.0.0"}`)}, []packageRef{{Ecosystem: "npm", Name: "@hanzo/ui"}}},
		{"npm hanzogui", []client.GitHubFile{file("package.json", `{"name":"@hanzogui/core","version":"1.0.0"}`)}, []packageRef{{Ecosystem: "npm", Name: "@hanzogui/core"}}},
		{"npm custom name with hanzo repo", []client.GitHubFile{file("package.json", `{"name":"custom-tool","repository":{"type":"git","url":"git+https://github.com/hanzoai/custom-tool.git"}}`)}, []packageRef{{Ecosystem: "npm", Name: "custom-tool"}}},
		{"npm third-party fork postcss", []client.GitHubFile{file("package.json", `{"name":"postcss","repository":"postcss/postcss"}`)}, nil},
		{"npm third-party fork cookie no repo", []client.GitHubFile{file("package.json", `{"name":"cookie","version":"1.0.0"}`)}, nil},
		{"npm third-party fork express", []client.GitHubFile{file("package.json", `{"name":"express","repository":"https://github.com/expressjs/express"}`)}, nil},
		{"npm third-party fork eslint", []client.GitHubFile{file("package.json", `{"name":"eslint"}`)}, nil},
		{"npm third-party fork prettier", []client.GitHubFile{file("package.json", `{"name":"prettier"}`)}, nil},
		{"npm third-party fork sharp", []client.GitHubFile{file("package.json", `{"name":"sharp"}`)}, nil},
		{"npm private", []client.GitHubFile{file("package.json", `{"name":"site","private":true}`)}, nil},
		{"npm unnamed", []client.GitHubFile{file("package.json", `{"workspaces":["a"]}`)}, nil},
		{"npm refused name", []client.GitHubFile{file("package.json", `{"name":"Not Valid"}`)}, nil},
		{"npm not json", []client.GitHubFile{file("package.json", `{`)}, nil},
		{"pyproject project", []client.GitHubFile{file("pyproject.toml", "[build-system]\nrequires = [\"x\"]\n\n[project]\nname = \"hanzo-ai\" # comment\nversion = \"1\"\n")}, []packageRef{{Ecosystem: "pypi", Name: "hanzo-ai"}}},
		{"pyproject poetry", []client.GitHubFile{file("pyproject.toml", "[tool.poetry]\nname = 'hanzo_dev'\n")}, []packageRef{{Ecosystem: "pypi", Name: "hanzo_dev"}}},
		{"pyproject custom with estate repo", []client.GitHubFile{file("pyproject.toml", "[project]\nname = \"jin\"\n[project.urls]\nRepository = \"https://github.com/hanzoai/jin\"\n")}, []packageRef{{Ecosystem: "pypi", Name: "jin"}}},
		{"pyproject third-party rejected", []client.GitHubFile{file("pyproject.toml", "[project]\nname = \"requests\"\n")}, nil},
		{"pyproject dynamic name", []client.GitHubFile{file("pyproject.toml", "[project]\ndynamic = [\"name\"]\n")}, nil},
		{"pyproject name in another table", []client.GitHubFile{file("pyproject.toml", "[tool.other]\nname = \"nope\"\n")}, nil},
		{"setup.cfg", []client.GitHubFile{file("setup.cfg", "[metadata]\nname = hanzo-legacy\nversion = 1\n[options]\nname = no\n")}, []packageRef{{Ecosystem: "pypi", Name: "hanzo-legacy"}}},
		{"setup.cfg third-party rejected", []client.GitHubFile{file("setup.cfg", "[metadata]\nname = flask\nversion = 1\n")}, nil},
		{"cargo", []client.GitHubFile{file("Cargo.toml", "[package]\nname = \"hanzo-ml\"\nversion = \"0.1.0\"\n")}, []packageRef{{Ecosystem: "crates", Name: "hanzo-ml"}}},
		{"cargo custom with luxfi repo", []client.GitHubFile{file("Cargo.toml", "[package]\nname = \"ovm\"\nrepository = \"https://github.com/luxfi/ovm\"\n")}, []packageRef{{Ecosystem: "crates", Name: "ovm"}}},
		{"cargo third-party rejected", []client.GitHubFile{file("Cargo.toml", "[package]\nname = \"serde\"\n")}, nil},
		{"cargo unpublished", []client.GitHubFile{file("Cargo.toml", "[package]\nname = \"hanzo-tool\"\npublish = false # internal\n")}, nil},
		{"cargo workspace", []client.GitHubFile{file("Cargo.toml", "[workspace]\nmembers = [\"a\"]\n")}, nil},
		{"cargo array table first", []client.GitHubFile{file("Cargo.toml", "[[bin]]\nname = \"bin\"\n[package]\nname = \"hanzo-real\"\n")}, []packageRef{{Ecosystem: "crates", Name: "hanzo-real"}}},
		{"the same name twice", []client.GitHubFile{
			file("pyproject.toml", "[project]\nname = \"hanzo-dup\"\n"), file("setup.cfg", "[metadata]\nname = hanzo-dup\n")}, []packageRef{{Ecosystem: "pypi", Name: "hanzo-dup"}}},
		{"unknown file", []client.GitHubFile{file("go.mod", "module x")}, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := manifestPackages(c.files); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("manifestPackages = %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestIsOwnedPackage(t *testing.T) {
	accepted := []struct {
		eco, name, repo string
	}{
		{"npm", "@hanzo/ui", ""},
		{"npm", "@hanzogui/core", ""},
		{"npm", "@hanzoai/sdk", ""},
		{"npm", "@luxfi/sdk", ""},
		{"npm", "@luxdefi/core", ""},
		{"npm", "@zooai/sdk", ""},
		{"npm", "@zoolabs/web", ""},
		{"npm", "@zenlm/models", ""},
		{"npm", "@parsnetwork/sdk", ""},
		{"npm", "@bootnode/cli", ""},
		{"npm", "hanzo-mcp", ""},
		{"pypi", "hanzoai", ""},
		{"crates", "luxfi", ""},
		{"crates", "lux-node", ""},
		{"npm", "zen-sdk", ""},
		{"pypi", "zenlm", ""},
		{"pypi", "zooai", ""},
		{"pypi", "zoolabs", ""},
		{"crates", "pars-cli", ""},
		{"npm", "bootnode", ""},
		{"dockerhub", "hanzoai/cloud", ""},
		{"dockerhub", "luxfi/node", ""},
		{"npm", "custom-pkg", "git+https://github.com/hanzoai/custom-pkg.git"},
		{"npm", "monorepo-sub", "https://github.com/hanzo-js/mono/tree/main/packages/sub"},
		{"pypi", "custom-py", "https://github.com/zenlm/custom-py"},
		{"crates", "custom-rs", "https://github.com/luxfi/custom-rs"},
		{"npm", "shorthand-pkg", "github:zooai/shorthand-pkg"},
	}
	for _, c := range accepted {
		if !isOwnedPackage(c.eco, c.name, c.repo) {
			t.Errorf("isOwnedPackage(%q, %q, %q) = false, want true", c.eco, c.name, c.repo)
		}
	}

	rejected := []struct {
		eco, name, repo string
	}{
		{"npm", "postcss", ""},
		{"npm", "postcss", "https://github.com/postcss/postcss"},
		{"npm", "cookie", ""},
		{"npm", "express", ""},
		{"npm", "eslint", ""},
		{"npm", "prettier", ""},
		{"npm", "sharp", ""},
		{"npm", "jose", ""},
		{"npm", "protobufjs", ""},
		{"npm", "jsonwebtoken", ""},
		{"npm", "msw", ""},
		{"npm", "elliptic", ""},
		{"npm", "passport-oauth2", ""},
		{"npm", "luxon", ""},
		{"npm", "@babel/core", ""},
		{"npm", "zen-observable", "https://github.com/zenparsing/zen-observable"},
		{"pypi", "requests", ""},
		{"crates", "tokio", "https://github.com/tokio-rs/tokio"},
		{"dockerhub", "library/postgres", ""},
	}
	for _, c := range rejected {
		if isOwnedPackage(c.eco, c.name, c.repo) {
			t.Errorf("isOwnedPackage(%q, %q, %q) = true, want false", c.eco, c.name, c.repo)
		}
	}
}

func TestEstateReposNamesEachGitHubRepositoryOnce(t *testing.T) {
	doc := &activityDoc{Repos: []activityRepo{
		{Repo: "hanzoai/ui", GitHub: "https://github.com/hanzoai/ui"},
		{Repo: "hanzo-community/ui", GitHub: "https://github.com/HanzoAI/ui"},
		{Repo: "forge-only/x"},
		{Repo: "gitlab/y", GitHub: "https://gitlab.com/gitlab/y"},
		{Repo: "luxfi/node", GitHub: "https://github.com/luxfi/node.git"},
	}}
	want := []string{"hanzoai/ui", "luxfi/node"}
	if got := estateRepos(doc); !reflect.DeepEqual(got, want) {
		t.Fatalf("estateRepos = %v, want %v", got, want)
	}
}
