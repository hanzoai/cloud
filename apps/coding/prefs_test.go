package coding

// prefs_test.go pins what a person's settings do to their run: the model and the
// effort a run takes when it names none, and their own instructions, written
// where the run's harness reads its user's.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hanzoai/cloud/client"
	prefpeer "github.com/hanzoai/cloud/client/pref"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/plane"
	"github.com/zap-proto/zip"
)

// fakePref is the pref app's plane op as a rig sees it.
type fakePref struct {
	mu    sync.Mutex
	doc   string
	fail  error
	asked []zip.Caller
}

// servesPref stands the pref app up beside a rig that is already serving.
func servesPref(t *testing.T, doc string) *fakePref {
	t.Helper()
	s := &fakePref{doc: doc}
	app := zip.New(zip.Config{AppName: prefpeer.App, DisableStartupMessage: true})
	app.Post("/pref/own", func(ctx context.Context, _ *client.PrefsIn) (*client.Prefs, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.asked = append(s.asked, cloud.Who(ctx))
		if s.fail != nil {
			return nil, s.fail
		}
		return &client.Prefs{Doc: s.doc}, nil
	}, zip.WithOperationID(client.PrefOwn))
	plane.Bind()
	go func() { _ = app.Listen(zip.SocketPath(prefpeer.App)) }()
	t.Cleanup(func() { _ = app.Shutdown() })
	waitListening(t, prefpeer.App)
	return s
}

// A run's two keys are read out of a document other surfaces also write, each by
// its own shape: keys it does not own are left alone, and one of its own with the
// wrong shape is refused by name.
func TestPreferences_ReadsARunsTwoKeys(t *testing.T) {
	p, err := preferences(`{"theme":"dark","instructions":"  Prefer small commits.\n","code":{"model":"zen6-coder-max","effort":"high","mode":"plan","place":7}}`)
	if err != nil || p.Instructions != "Prefer small commits." || p.Model != "zen6-coder-max" || p.Effort != "high" {
		t.Fatalf("preferences = %+v, %v", p, err)
	}
	if p, err := preferences(`{}`); err != nil || p != (Preferred{}) {
		t.Fatalf("an empty document is no preference: %+v %v", p, err)
	}
	for _, doc := range []string{`[]`, `{"instructions":7}`, `{"code":"zen6"}`, `{"code":{"model":1}}`,
		`{"instructions":"` + strings.Repeat("x", maxInstructions+1) + `"}`} {
		if _, err := preferences(doc); err == nil {
			t.Errorf("%.40s: a document the run cannot read was read", doc)
		}
	}
}

// A RUN THAT NAMES NO MODEL thinks with the one its person set, asked of the pref
// app as that person.
func TestStart_SettingsDefaultTheModelAndTheEffort(t *testing.T) {
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	planPod(r)
	r.serve(t)
	pref := servesPref(t, `{"code":{"model":"zen6-coder-max","effort":"high"}}`)

	r.startMode(t, client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix it", Mode: "plan"})

	run, ok := r.pod.did("--skip-git-repo-check")
	if !ok || !strings.Contains(run, "-m zen6-coder-max") || !strings.Contains(run, `model_reasoning_effort="high"`) {
		t.Fatalf("the harness did not think with its person's defaults: %q", run)
	}
	pref.mu.Lock()
	defer pref.mu.Unlock()
	if len(pref.asked) != 1 || pref.asked[0].Org != "hanzo" || pref.asked[0].User != "sub-zoe" {
		t.Fatalf("the settings were asked as %+v, not as the run's person", pref.asked)
	}
}

// WHAT THE REQUEST NAMES WINS over what the person set, field by field.
func TestStart_TheRequestWinsOverTheSettings(t *testing.T) {
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	planPod(r)
	r.serve(t)
	servesPref(t, `{"code":{"model":"zen6-coder-max","effort":"high"}}`)

	r.startMode(t, client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix it", Mode: "plan", Model: "enso"})

	run, _ := r.pod.did("--skip-git-repo-check")
	if !strings.Contains(run, "-m enso ") || strings.Contains(run, "zen6-coder-max") || !strings.Contains(run, `model_reasoning_effort="high"`) {
		t.Fatalf("the asked model did not win, or the unasked effort did not default: %q", run)
	}
}

// A HARNESS THAT NAMES NO MODEL is not handed the person's, and not refused for
// it: they did not ask this run for one.
func TestStart_SettingsDoNotRefuseAHarnessThatTakesNoModel(t *testing.T) {
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	planPod(r)
	r.serve(t)
	servesPref(t, `{"code":{"model":"zen6-coder-max","effort":"high"}}`)

	r.startMode(t, client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix it", Mode: "plan", Tool: "claude"})

	run, ok := r.pod.did("claude -p")
	if !ok || strings.Contains(run, "zen6-coder-max") || strings.Contains(run, "reasoning") {
		t.Fatalf("claude was handed a model nobody asked it for: %q", run)
	}
}

// A MODEL FROM THE SETTINGS IS CHECKED as one the request named, and a refusal
// says where it came from — before any session is opened.
func TestStart_ASettingsModelIsCheckedAsAnAskedOne(t *testing.T) {
	r := newRig()
	r.serve(t)
	servesPref(t, `{"code":{"model":"--dangerously-bypass","effort":"max"}}`)

	_, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", client.CodingStartIn{Repo: "site", Prompt: "fix it"})
	if err == nil || !strings.Contains(err.Error(), "from your settings") {
		t.Fatalf("a settings model that is not a model id was run: %v", err)
	}
	if len(r.sessions.opened) != 0 {
		t.Fatal("a refused run opened a session")
	}
	// The request's own refusal is its own, whatever the settings hold.
	_, err = r.startAs(t, asCaller("sub-zoe"), "sub-zoe", client.CodingStartIn{Repo: "site", Prompt: "fix it", Model: "-x"})
	if err == nil || strings.Contains(err.Error(), "settings") {
		t.Fatalf("a request's bad model was blamed on the settings: %v", err)
	}
}

// SETTINGS THAT CANNOT BE READ refuse the run, as ErrSettings, rather than
// starting it without what its person set.
func TestStart_UnreadableSettingsRefuseTheRun(t *testing.T) {
	r := newRig()
	r.serve(t)
	pref := servesPref(t, "{}")
	pref.fail = zip.Errorf(500, "store down")

	_, err := r.startAs(t, asCaller("sub-zoe"), "sub-zoe", client.CodingStartIn{Repo: "site", Prompt: "fix it"})
	if !errors.Is(err, ErrSettings) {
		t.Fatalf("unreadable settings did not refuse the run: %v", err)
	}
}

// A PERSON'S INSTRUCTIONS are written where the run's harness reads its user's,
// on stdin, before the harness starts — a plan's too.
func TestStart_InstructionsReachTheHarnessesOwnFile(t *testing.T) {
	const said = "Prefer small commits.\nNever touch the lockfile."
	r := newRig()
	r.admit = pushes(map[string]string{"sub-zoe": "zoe-gh"}, nil)
	planPod(r)
	r.serve(t)
	servesPref(t, `{"instructions":"Prefer small commits.\nNever touch the lockfile."}`)

	r.startMode(t, client.CodingStartIn{Repo: "github.com/hanzo-apps/site", Prompt: "fix it", Mode: "plan"})

	file, _ := userFiles("dev")
	wrote, harness := at(r.pod, file), at(r.pod, "--skip-git-repo-check")
	if wrote < 0 || harness < 0 || wrote > harness {
		t.Fatalf("the instructions were not written before the harness:\n%s", strings.Join(r.pod.lines(), "\n"))
	}
	r.pod.mu.Lock()
	defer r.pod.mu.Unlock()
	if r.pod.fed[wrote] != said+"\n" {
		t.Fatalf("the instructions were fed %q", r.pod.fed[wrote])
	}
	for i, argv := range r.pod.ran {
		if strings.Contains(strings.Join(argv, " "), "lockfile") {
			t.Fatalf("command %d carries the instructions as an argument: %q", i, argv)
		}
	}
}

// Each harness is handed the instructions in the file it reads as its user's own,
// resolved as it resolves it: the variable it reads first, else its default. The
// script is run here, in a real shell, and the harness's own file is read back —
// added to, when the image already carries one.
func TestEquip_InstructionsLandInTheFileEachHarnessReads(t *testing.T) {
	p := &pod{}
	servePod(t, p)
	in := sandbox{ctx: context.Background(), id: "sbx_1", ttl: 60, session: "sess_1"}
	for _, c := range []struct {
		tool, env, want string
	}{
		{"dev", "", ".hanzo/dev/AGENTS.md"},
		{"", "DEV_HOME=%s/profile", "profile/AGENTS.md"},
		{"codex", "", ".codex/AGENTS.md"},
		{"codex", "CODEX_HOME=%s/profile", "profile/AGENTS.md"},
		{"claude", "", ".claude/CLAUDE.md"},
		{"claude", "CLAUDE_CONFIG_DIR=%s/profile", "profile/CLAUDE.md"},
	} {
		home := t.TempDir()
		if err := in.equip(RunRequest{Tool: c.tool, Instructions: "Be brief."}, func(string, string, string) {}); err != nil {
			t.Fatalf("%s: equip: %v", c.tool, err)
		}
		want := filepath.Join(home, c.want)
		if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(want, []byte("The image's own."), 0o644); err != nil {
			t.Fatal(err)
		}
		p.mu.Lock()
		argv, stdin := p.ran[len(p.ran)-1], p.fed[len(p.fed)-1]
		p.mu.Unlock()
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Env = []string{"HOME=" + home, "PATH=" + os.Getenv("PATH")}
		if c.env != "" {
			cmd.Env = append(cmd.Env, strings.ReplaceAll(c.env, "%s", home))
		}
		cmd.Stdin = strings.NewReader(stdin)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: the script failed: %v %s", c.tool, err, out)
		}
		got, err := os.ReadFile(want)
		if err != nil || string(got) != "The image's own.\nBe brief.\n" {
			t.Fatalf("%s %s: %s holds %q, %v", c.tool, c.env, c.want, got, err)
		}
	}
}

// A routed run carries the person's instructions to the machine.
func TestRouted_CarriesInstructions(t *testing.T) {
	router := &fakeRouter{}
	d, _ := routedDispatcher(&fakeSessions{id: "sess_route1abc"}, &fakeRunner{}, router, &fakeGate{})
	req := routedReq()
	req.Instructions = "Be brief."
	if res := d.Run(context.Background(), req); !res.OK {
		t.Fatalf("routed: %s", res.Error)
	}
	if got := router.runs[0].Instructions; got != "Be brief." {
		t.Fatalf("the routed run carries instructions %q", got)
	}
	if !takes("claude", "tgt_evo") || takes("claude", "") || !takes("", "") {
		t.Fatal("a claimed machine takes the model its person set, whatever the harness")
	}
}
