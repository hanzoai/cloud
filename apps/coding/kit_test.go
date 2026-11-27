package coding

// kit_test.go pins a run's half of its org's tool plane: what it asks the tool
// app, where its harness is handed the skills, and how each MCP server reaches
// the harness — at the run's own address for it, opened by the run's ticket, and
// never with the server's own address or credential anywhere the run can read.

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	toolpeer "github.com/hanzoai/cloud/client/tool"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// mcpSecret is a server's credential, spelled once so every assertion about
// where it may not appear asks about the same string. KMS holds it at the ref the
// tool plane seals it at; nothing in a run may.
const mcpSecret = "Bearer " + "sk_test_mcp_example_token"

// fakeTools is the tool app's kit op as a rig sees it.
type fakeTools struct {
	mu     sync.Mutex
	kit    client.Kit
	fail   error
	orgs   []string
	people []string
}

// servesTools stands the tool app up beside a rig that is already serving.
func servesTools(t *testing.T, kit client.Kit) *fakeTools {
	t.Helper()
	s := &fakeTools{kit: kit}
	app := zip.New(zip.Config{AppName: toolpeer.App, DisableStartupMessage: true})
	app.Post("/tools/kit", func(ctx context.Context, _ *client.KitIn) (*client.Kit, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.orgs = append(s.orgs, cloud.Who(ctx).Org)
		s.people = append(s.people, cloud.Who(ctx).User)
		if s.fail != nil {
			return nil, s.fail
		}
		out := s.kit
		return &out, nil
	}, zip.WithOperationID(client.ToolsKit))
	plane.Bind()
	go func() { _ = app.Listen(zip.SocketPath(toolpeer.App)) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitListening(t, toolpeer.App)
	return s
}

// anOrgsKit is two skills and three servers: one whose id is a name the harness
// already carries, and one with nothing activated.
func anOrgsKit() client.Kit {
	return client.Kit{
		Skills: []client.SkillDoc{
			{Name: "deploy", Content: "---\ndescription: Ship it\n---\n# Deploy\n"},
			{Name: "git_repos", Content: "# Repos\n"},
		},
		Omitted: []string{"triage"},
		Servers: []client.Remote{
			{Name: "stripe-com", Tools: []string{"create_payment_link", "list_invoices"}},
			{Name: "run", Tools: []string{"search"}},
			{Name: "idle"},
		},
	}
}

// unpacked is the skills archive a command was fed, by file.
func unpacked(t *testing.T, stdin string) map[string]string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdin))
	if err != nil {
		t.Fatalf("the skills were not fed as base64: %v", err)
	}
	out := map[string]string{}
	tr := tar.NewReader(bytes.NewReader(raw))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("the skills were not fed as a tar: %v", err)
		}
		b, _ := io.ReadAll(tr)
		out[h.Name] = string(b)
	}
}

// THE ORG'S KIT REACHES THE HARNESS, AND NO SERVER'S CREDENTIAL DOES. The tool
// app is asked as the run's org; the skills land where dev reads its user's; each
// server is in dev's MCP configuration beside the ask server, under a key that
// cannot take its name, at the run's own address for it and sent the run's
// ticket — and a server's address and credential are in no argument, no stdin
// and no environment of any command the run makes. The run's ticket opens those
// servers and no others while the harness runs.
func TestStart_TheOrgsKitReachesTheHarness(t *testing.T) {
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	planPod(r)
	answer := r.pod.answer
	opened := map[string]bool{}
	r.pod.answer = func(argv []string) client.Ran {
		if slices.Contains(argv, "--skip-git-repo-check") {
			flights.Lock()
			for _, f := range flights.m {
				f.mu.Lock()
				for id := range f.servers {
					opened[id] = true
				}
				f.mu.Unlock()
			}
			flights.Unlock()
		}
		return answer(argv)
	}
	r.secrets = map[string]string{"orgs/hanzo/tools-mcp/stripe-com": mcpSecret}
	r.serve(t)
	tools := servesTools(t, anOrgsKit())

	r.startMode(t, client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix it", Mode: "plan"})

	_, skills := userFiles("dev")
	wrote, harness := at(r.pod, skills), at(r.pod, "--skip-git-repo-check")
	if wrote < 0 || harness < 0 || wrote > harness {
		t.Fatalf("the skills were not written before the harness:\n%s", strings.Join(r.pod.lines(), "\n"))
	}
	r.pod.mu.Lock()
	defer r.pod.mu.Unlock()
	files := unpacked(t, r.pod.fed[wrote])
	if len(files) != 2 || files["deploy/SKILL.md"] != anOrgsKit().Skills[0].Content || files["git_repos/SKILL.md"] != "# Repos\n" {
		t.Fatalf("the skills written were %v", files)
	}
	argv := strings.Join(r.pod.ran[harness], " ")
	for _, want := range []string{
		`mcp_servers.stripe-com.url="` + relayURL("stripe-com") + `"`,
		`mcp_servers.stripe-com.env_http_headers={"X-Hanzo-Run"="HANZO_RUN"}`,
		`mcp_servers.stripe-com.enabled_tools=["create_payment_link","list_invoices"]`,
		`mcp_servers.run-2.url="` + relayURL("run") + `"`,
		`mcp_servers.run-2.enabled_tools=["search"]`,
	} {
		if !strings.Contains(argv, want) {
			t.Fatalf("missing %s in %s", want, argv)
		}
	}
	if strings.Contains(argv, "idle") {
		t.Fatalf("a server with nothing activated was handed over: %s", argv)
	}
	if lines := strings.SplitN(r.pod.fed[harness], "\n", 3); len(lines) != 3 || !strings.HasSuffix(lines[2], "fix it") {
		t.Fatalf("the harness was fed %q, want the gateway key, the ticket and its task", r.pod.fed[harness])
	}
	for i, cmd := range r.pod.ran {
		for _, where := range []string{strings.Join(cmd, " "), r.pod.fed[i]} {
			for _, leak := range []string{mcpSecret, "HANZO_MCP", "mcp.stripe.com", "tools-mcp"} {
				if strings.Contains(where, leak) {
					t.Fatalf("command %d carries %q: %q", i, leak, cmd)
				}
			}
		}
	}
	if !opened["stripe-com"] || !opened["run"] || len(opened) != 2 {
		t.Fatalf("while the harness ran its ticket opened %v, want the two servers it carries", opened)
	}
	said := ""
	for _, e := range r.sessions.events {
		said += e.payload
	}
	if !strings.Contains(said, "triage") {
		t.Fatalf("the run did not say what it left out: %s", said)
	}
	tools.mu.Lock()
	defer tools.mu.Unlock()
	if !slices.Equal(tools.orgs, []string{"hanzo"}) || !slices.Equal(tools.people, []string{"sub-zoe"}) {
		t.Fatalf("the tool app was asked as %v %v, not the run's org and person", tools.orgs, tools.people)
	}
}

// CLAUDE is handed the same servers in its own spelling: an --mcp-config entry at
// the run's own address whose header is the ticket as `${HANZO_RUN}`, and each
// tool an admin activated allowed by name — never a whole server, whose other
// tools would then run unasked.
func TestArgv_ClaudeCarriesTheOrgsServers(t *testing.T) {
	servers := []Server{
		{Name: "stripe-com", ID: "stripe-com", Tools: []string{"create_payment_link", "list_invoices"}},
		{Name: "docs", ID: "m1a2b3c", Tools: []string{"search"}},
	}
	argv := argvFor("claude", "https://api.test/v1/agent/ask", "", "", servers...)
	i := slices.Index(argv, "--mcp-config")
	if i < 0 {
		t.Fatalf("no --mcp-config: %q", argv)
	}
	var config struct {
		MCPServers map[string]mcpServer `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(argv[i+1]), &config); err != nil {
		t.Fatalf("the config is not JSON: %v", err)
	}
	for key, id := range map[string]string{"stripe-com": "stripe-com", "docs": "m1a2b3c"} {
		if got := config.MCPServers[key]; got.URL != relayURL(id) || len(got.Headers) != 1 || got.Headers[TicketHeader] != "${HANZO_RUN}" {
			t.Fatalf("%s is %+v", key, got)
		}
	}
	if config.MCPServers[askServer].URL == "" {
		t.Fatal("the ask server went missing beside the org's")
	}
	if allowed := argv[slices.Index(argv, "--allowedTools")+1]; allowed !=
		"mcp__run__ask_user,mcp__stripe-com__create_payment_link,mcp__stripe-com__list_invoices,mcp__docs__search" {
		t.Fatalf("allowed %q", allowed)
	}
}

// Every harness reads its user's skills from its own directory, and the archive
// the run feeds lands there, as <name>/SKILL.md — run here in a real shell.
func TestEquip_SkillsLandWhereEachHarnessReadsThem(t *testing.T) {
	p := &pod{}
	servePod(t, p)
	in := sandbox{ctx: context.Background(), id: "sbx_1", ttl: 60, session: "sess_1"}
	for tool, want := range map[string]string{"dev": ".agents/skills", "codex": ".agents/skills", "claude": ".claude/skills"} {
		home := t.TempDir()
		kit := Kit{Skills: []client.SkillDoc{{Name: "deploy", Content: "# Deploy\n$(rm -rf /) `id`\n"}}}
		if err := in.equip(RunRequest{Tool: tool, Kit: kit}, func(string, string, string) {}); err != nil {
			t.Fatalf("%s: equip: %v", tool, err)
		}
		p.mu.Lock()
		argv, stdin := p.ran[len(p.ran)-1], p.fed[len(p.fed)-1]
		p.mu.Unlock()
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: the script failed: %v %s", tool, err, out)
		}
		got, err := os.ReadFile(filepath.Join(home, want, "deploy", "SKILL.md"))
		if err != nil || string(got) != kit.Skills[0].Content {
			t.Fatalf("%s: %s/deploy/SKILL.md holds %q, %v", tool, want, got, err)
		}
	}
}

// A DEPLOYMENT WITHOUT THE PREF OR THE TOOL APP RUNS BARE: nothing is written for
// the harness and nothing is added to its configuration.
func TestStart_WithNoPrefOrToolAppARunIsBare(t *testing.T) {
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	planPod(r)
	r.serve(t)

	r.startMode(t, client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix it", Mode: "plan"})

	file, skills := userFiles("dev")
	if at(r.pod, file) >= 0 || at(r.pod, skills) >= 0 {
		t.Fatalf("a bare run wrote for its harness:\n%s", strings.Join(r.pod.lines(), "\n"))
	}
	run, ok := r.pod.did("--skip-git-repo-check")
	if !ok || strings.Contains(run, relayPath) || !strings.Contains(run, "-m "+codingModel) {
		t.Fatalf("a bare run's harness: %q", run)
	}
}

// A TOOL APP THAT CANNOT ANSWER fails the run, with the reason on its session,
// before its agent starts.
func TestStart_AnUnreadableKitFailsTheRun(t *testing.T) {
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	planPod(r)
	r.serve(t)
	tools := servesTools(t, client.Kit{})
	tools.fail = zip.Errorf(502, "skills: the brand's documents: down")

	r.startMode(t, client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix it", Mode: "plan"})

	if _, ran := r.pod.did("--skip-git-repo-check"); ran {
		t.Fatal("the agent started without its org's kit")
	}
	end := r.terminal(t)
	if end["status"] != "error" || !strings.Contains(end["error"].(string), "skills and MCP servers could not be read") {
		t.Fatalf("the run did not fail with its reason: %v", end)
	}
}

// AN INTERPRETER has no harness to hand a kit to, and the tool app is not asked.
func TestStart_AnInterpreterIsNotEquipped(t *testing.T) {
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	planPod(r)
	r.serve(t)
	tools := servesTools(t, anOrgsKit())

	r.startMode(t, client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "print(1)", Tool: "python"})

	tools.mu.Lock()
	defer tools.mu.Unlock()
	if len(tools.orgs) != 0 {
		t.Fatalf("the tool app was asked for an interpreter's run")
	}
}

// A server's name becomes a configuration key: lowercase, what TOML and claude
// both admit, never a name the harness already carries, never a second time.
func TestServerKey(t *testing.T) {
	taken := map[string]bool{askServer: true, builtinServer: true}
	for _, c := range [][2]string{
		{"stripe-com", "stripe-com"},
		{"run", "run-2"},
		{"hanzo", "hanzo-2"},
		{"Stripe_COM", "stripecom"},
		{"stripe-com", "stripe-com-2"},
		{"a.b\"]=x", "abx"},
		{"___", "mcp"},
	} {
		if got := serverKey(c[0], taken); got != c[1] {
			t.Errorf("serverKey(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

// A TOOL NO ALLOW-LIST CAN SPELL fails the run: a name carrying a comma would
// allow claude a second thing, and the tool app never answers one.
func TestStart_AToolNoHarnessCanNameFailsTheRun(t *testing.T) {
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	planPod(r)
	r.serve(t)
	servesTools(t, client.Kit{Servers: []client.Remote{{Name: "docs", Tools: []string{"search,Bash"}}}})

	r.startMode(t, client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix it", Mode: "plan"})

	if _, ran := r.pod.did("--skip-git-repo-check"); ran {
		t.Fatal("the agent started with a tool no allow-list can name")
	}
	if end := r.terminal(t); end["status"] != "error" || !strings.Contains(end["error"].(string), "no harness can name") {
		t.Fatalf("the run did not fail with its reason: %v", end)
	}
}

// A RUN CARRIES SO MANY SERVERS, whatever the tool app answered: past the bound a
// server is left out and the run says which, beside the ones the tool app left
// out itself.
func TestStart_ARunCarriesSoManyServers(t *testing.T) {
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	planPod(r)
	r.serve(t)
	kit := client.Kit{Excess: []string{"s99"}}
	for i := range client.KitServers + 2 {
		kit.Servers = append(kit.Servers, client.Remote{Name: fmt.Sprintf("s%02d", i), Tools: []string{"search"}})
	}
	servesTools(t, kit)

	r.startMode(t, client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix it", Mode: "plan"})

	run, ok := r.pod.did("--skip-git-repo-check")
	if !ok || strings.Count(run, ".enabled_tools=") != client.KitServers || strings.Contains(run, "mcp_servers.s16.") {
		t.Fatalf("the harness was handed %d servers: %s", strings.Count(run, ".enabled_tools="), run)
	}
	said := ""
	for _, e := range r.sessions.events {
		said += e.payload
	}
	if !strings.Contains(said, "left out, over what a run carries: s99, s16, s17") {
		t.Fatalf("the run did not say which servers it left out: %s", said)
	}
}
