package sandbox

// agent.go — a model at a sandbox's keyboard, with its person watching.
//
//	POST  /:id/agent   {message, history?, model?}   ->  text/event-stream
//
// A person says what they want done and a model does it in the sandbox: a shell
// for a terminal, and for a desktop or a browser the screen too — it takes a
// picture, clicks where it sees, types, presses keys. The same sandbox is on the
// person's screen through the screen door (screen.go), so they watch the work
// happen and can take the keyboard back, and the same route is what an agent of
// another surface (the Grok bot, an MCP client) calls to get a task done on a
// computer of the org's own.
//
// THE SERVER KEEPS NOTHING BETWEEN TURNS. The caller holds the conversation and
// sends it back as `history`, so there is no agent session to leak, reap or bill
// idle: a turn is one request that streams its steps and ends. What it spends is
// the model's tokens, on the caller's own account, and the sandbox hour it was
// already paying for.
//
// THE HANDS ARE THE SANDBOX'S OWN TOOLS, through the one way in (exec). A click
// is `xdotool`, a picture is `scrot`, and neither is a new door: the desktop and
// browser images carry both, the display is `:1`, and a command run there is
// bounded, metered and kept alive by the same machinery as any other. Nothing is
// interpolated into a shell: every argument a model supplies is validated and
// handed over as its own argv word, so a model that emits `ctrl+l; rm -rf ~` for
// a key gets a refusal, not a command.

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hanzoai/cloud/client"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/cloud/sdk/env"
	"github.com/hanzoai/cloud/sdk/infer"
	"github.com/hanzoai/cloud/sdk/principal"
	"github.com/hanzoai/cloud/sdk/spec"
	"github.com/zap-proto/zip"
)

// agentIn is one turn: what the person says now, and the conversation so far.
type agentIn struct {
	// Message is what to do, in words.
	Message string `json:"message"`
	// History is the conversation before this turn, oldest first: what the person
	// said and what the model answered in words. The picture of the screen is not
	// part of it — the model takes its own, each turn, of the screen as it is now.
	History []agentTurn `json:"history,omitempty"`
	// Model is the model that works the sandbox, by the id GET /v1/models lists.
	// Empty takes the deployment's (SANDBOX_AGENT_MODEL).
	Model string `json:"model,omitempty"`
}

// agentTurn is one earlier turn: role is `user` or `assistant`.
type agentTurn struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

// agentFrame is one thing the stream says. type is:
//
//	say     the model's words, in text
//	act     the model reached for a tool: tool and args
//	result  what the tool answered: tool, ok and text (a screenshot says so, in
//	        words; the picture itself goes to the model, not down the stream)
//	error   the turn could not go on: text says why
//	end     the turn is over; rounds is how many times the model was asked
type agentFrame struct {
	Type   string          `json:"type"`
	Text   string          `json:"text,omitempty"`
	Tool   string          `json:"tool,omitempty"`
	Args   json.RawMessage `json:"args,omitempty"`
	OK     *bool           `json:"ok,omitempty"`
	Rounds int             `json:"rounds,omitempty"`
}

const (
	// agentDisplay is the display every sandbox that has a screen draws on.
	agentDisplay = ":1"
	// agentShot is where a picture of the screen is written, and read back from.
	agentShot = "/tmp/.hanzo-agent-screen.jpg"
	// A picture is JPEG at this quality: a screen read at 1280x800 is some 20 KB
	// and a model reads it as well as it would a lossless one.
	agentShotQuality = "50"
	// keptShots is how many pictures a model is shown: the screen now, and the one
	// before it. Older ones are dropped from the conversation, because each is
	// tokens the next round would pay for again.
	keptShots = 2
	// outputCap bounds what a shell tool hands back to the model.
	outputCap = 6000
)

// agentRounds is how many times a turn may ask the model, and agentBudget how
// long a turn may last. Both are the deployment's to set.
func agentRounds() int {
	if v, err := strconv.Atoi(env.Or("SANDBOX_AGENT_ROUNDS", "")); err == nil && v > 0 {
		return v
	}
	return 60
}

func agentBudget() time.Duration {
	if v, err := strconv.Atoi(env.Or("SANDBOX_AGENT_SECONDS", "")); err == nil && v > 0 {
		return time.Duration(v) * time.Second
	}
	return 30 * time.Minute
}

// agentModel is the model a turn works with when the caller names none.
func agentModel() string { return env.Or("SANDBOX_AGENT_MODEL", "enso") }

const screenPrompt = `You are working a real Linux computer for a person who is watching its screen. You see it through screenshots and you act through tools: click, type, key, scroll, navigate (opens a web address in the browser), autofill (types a saved Unified Vault, 1Password or LastPass login or TOTP code without revealing secrets), save_credential (seals a credential staged in RAM at /dev/shm/.hanzo-credential.json into Hanzo KMS and shreds the file), shell (a command in a terminal on the same machine) and wait.

How to work:
- Take a screenshot before you act and after each step that changes the screen, and check it did what you meant. Coordinates in click and scroll are pixels of the screenshot.
- Prefer the keyboard where it is sure: ctrl+l then navigate for an address, Return to submit, ctrl+t for a new tab.
- When a login form or API-key field appears and the user wants to sign in, click the username input and call autofill with credential set to the site's domain or credential name (use field: "login_totp" when a 2FA code input follows password, or field: "totp" for a standalone 2FA code, and submit: true to press Return). Never guess a password.
- When helping a sandbox desktop app or signup flow save credentials securely, ensure {name, domain, url, username, password, totp} is written with mode 0600 to /dev/shm/.hanzo-credential.json (RAM tmpfs, never /workspace) and call save_credential; never print or echo passwords in shell output or chat.
- Use shell for anything a command does better than a mouse: reading a file, installing a package, starting a program.
- Be brief. Say what you are about to do in a few words, do it, and when the task is done say what you did and what the person should look at in one or two sentences.
- If something blocks you (a missing vault credential, a captcha, a payment), stop and say so; do not guess a password.`

const shellPrompt = `You are working a real Linux computer for a person, through one tool: shell, a command run in a terminal on that machine. Run commands, read their output, and go on until the task is done. Be brief: say what you are about to do in a few words, and when the task is done say what you did and where to look in one or two sentences. If something blocks you (a login, a missing credential), stop and say so; do not guess.`

func objectOf(props string, required ...string) json.RawMessage {
	req := ""
	if len(required) > 0 {
		req = `,"required":["` + strings.Join(required, `","`) + `"]`
	}
	return json.RawMessage(`{"type":"object","properties":{` + props + `}` + req + `}`)
}

var shellTool = infer.ToolDef{
	Name:        "shell",
	Description: "Run a command in a terminal on the machine, and read its exit code, output and errors. It runs in the home directory, not on the screen.",
	Schema:      objectOf(`"command":{"type":"string","description":"the shell command line"}`, "command"),
}

var screenTools = []infer.ToolDef{
	{Name: "screenshot", Description: "Take a picture of the screen as it is now. Coordinates in click, move, drag and scroll are pixels of this picture.", Schema: objectOf("")},
	{Name: "click", Description: "Move the pointer to a point on the screen and press a mouse button there.",
		Schema: objectOf(`"x":{"type":"integer"},"y":{"type":"integer"},"button":{"type":"string","enum":["left","right","middle"]},"double":{"type":"boolean","description":"double-click"}`, "x", "y")},
	{Name: "move", Description: "Move the pointer to a point on the screen without pressing a button.",
		Schema: objectOf(`"x":{"type":"integer"},"y":{"type":"integer"}`, "x", "y")},
	{Name: "drag", Description: "Press and hold the primary mouse button at (x, y), drag to (toX, toY), and release.",
		Schema: objectOf(`"x":{"type":"integer"},"y":{"type":"integer"},"toX":{"type":"integer"},"toY":{"type":"integer"}`, "x", "y", "toX", "toY")},
	{Name: "type", Description: "Type text at the keyboard, into whatever has focus.",
		Schema: objectOf(`"text":{"type":"string"}`, "text")},
	{Name: "key", Description: "Press a key or a combination, as xdotool names them: Return, Tab, Escape, ctrl+l, alt+Left, ctrl+shift+t. Several, separated by spaces, are pressed in turn.",
		Schema: objectOf(`"keys":{"type":"string"}`, "keys")},
	{Name: "scroll", Description: "Scroll at a point on the screen: dy above zero is down, below zero is up, in notches.",
		Schema: objectOf(`"x":{"type":"integer"},"y":{"type":"integer"},"dy":{"type":"integer"}`, "x", "y", "dy")},
	{Name: "navigate", Description: "Open a web address in the browser: focuses the address bar, types it and presses Return.",
		Schema: objectOf(`"url":{"type":"string","description":"an http or https address"}`, "url")},
	{Name: "autofill", Description: "Fill a saved login, API key, or 6-digit TOTP code from the organization's Unified Vault (Hanzo KMS, 1Password, or LastPass) into the focused input without revealing secret values.",
		Schema: objectOf(`"credential":{"type":"string","description":"credential id, name, envVar, or site domain such as github.com"},"field":{"type":"string","enum":["login","login_totp","username","password","totp","secret"]},"submit":{"type":"boolean","description":"press Return after filling"}`)},
	{Name: "save_credential", Description: "Seal a credential staged at /dev/shm/.hanzo-credential.json by a sandbox desktop app directly into the organization's Unified Vault (Hanzo KMS) and shred the ephemeral file without revealing secret values.",
		Schema: objectOf(`"credential":{"type":"string","description":"optional display name override"},"url":{"type":"string","description":"optional login page URL override"}`)},
	{Name: "wait", Description: "Wait for the screen to settle, up to ten seconds.",
		Schema: objectOf(`"seconds":{"type":"number"}`, "seconds")},
	shellTool,
}

// agentTools is what a sandbox's model is offered: every tool where there is a
// screen, and the shell alone where there is none.
func agentTools(screen bool) []infer.ToolDef {
	if screen {
		return screenTools
	}
	return []infer.ToolDef{shellTool}
}

// shellFn runs one argv in the sandbox, as the caller's own exec does.
type shellFn func(ctx context.Context, argv []string, timeoutSec int) (ExecResult, error)

// toolArgs is every argument any tool takes; each tool reads its own.
type toolArgs struct {
	X          *int    `json:"x"`
	Y          *int    `json:"y"`
	ToX        *int    `json:"toX"`
	ToY        *int    `json:"toY"`
	DY         *int    `json:"dy"`
	Button     string  `json:"button"`
	Double     bool    `json:"double"`
	Text       string  `json:"text"`
	Keys       string  `json:"keys"`
	URL        string  `json:"url"`
	Credential string  `json:"credential"`
	Field      string  `json:"field"`
	Submit     bool    `json:"submit"`
	Command    string  `json:"command"`
	Seconds    float64 `json:"seconds"`
}

var keyWord = regexp.MustCompile(`^[A-Za-z0-9_+\-]{1,40}$`)

// point is a coordinate that is on a screen: a model that emits -1 or a million is
// told so, rather than handed to a program that would take it.
func point(v *int, name string) (string, error) {
	if v == nil || *v < 0 || *v > 20000 {
		return "", fmt.Errorf("%s must be a whole number of pixels, on the screen", name)
	}
	return strconv.Itoa(*v), nil
}

// act runs one tool call and answers what the model is told: the words, a picture
// when the tool took one, and whether it worked. It never raises — a tool that
// fails is a fact the model can act on, and a turn the person is paying for is not
// thrown away over one.
func act(ctx context.Context, run shellFn, name, raw string, screen bool) (text, picture string, ok bool) {
	return actWithVault(ctx, run, nil, nil, name, raw, screen)
}

func actWithVault(ctx context.Context, run shellFn, fill autofillFn, save saveCredentialFn, name, raw string, screen bool) (text, picture string, ok bool) {
	var a toolArgs
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &a); err != nil {
			return "The arguments were not JSON: " + err.Error(), "", false
		}
	}
	say := func(r ExecResult, err error) (string, bool) {
		if err != nil {
			return "The sandbox answered: " + err.Error(), false
		}
		out := strings.TrimSpace(r.Stdout)
		if e := strings.TrimSpace(r.Stderr); e != "" {
			out += "\n" + e
		}
		return fmt.Sprintf("exit %d\n%s", r.ExitCode, clip(out, outputCap)), r.ExitCode == 0
	}
	if name != "shell" && name != "save_credential" && !screen {
		return "This computer has no screen, so " + name + " is not available. Use shell.", "", false
	}
	switch name {
	case "shell":
		if c := strings.TrimSpace(a.Command); c == "" || len(c) > 8000 {
			return "command is required, and at most 8000 characters", "", false
		}
		t, good := say(run(ctx, []string{"sh", "-c", a.Command}, 120))
		return t, "", good
	case "screenshot":
		r, err := run(ctx, []string{"sh", "-c", "scrot -p -q " + agentShotQuality + " -o " + agentShot + " && base64 -w0 " + agentShot}, 30)
		if err != nil || r.ExitCode != 0 || strings.TrimSpace(r.Stdout) == "" {
			t, _ := say(r, err)
			return "The screen could not be read. " + t, "", false
		}
		return "Here is the screen.", "data:image/jpeg;base64," + strings.TrimSpace(r.Stdout), true
	case "click":
		x, err := point(a.X, "x")
		if err != nil {
			return err.Error(), "", false
		}
		y, err := point(a.Y, "y")
		if err != nil {
			return err.Error(), "", false
		}
		button := map[string]string{"": "1", "left": "1", "middle": "2", "right": "3"}[strings.ToLower(a.Button)]
		if button == "" {
			return "button is left, right or middle", "", false
		}
		argv := []string{"xdotool", "mousemove", "--sync", x, y, "click"}
		if a.Double {
			argv = append(argv, "--repeat", "2", "--delay", "80")
		}
		t, good := say(run(ctx, append(argv, button), 15))
		return t, "", good
	case "move":
		x, err := point(a.X, "x")
		if err != nil {
			return err.Error(), "", false
		}
		y, err := point(a.Y, "y")
		if err != nil {
			return err.Error(), "", false
		}
		t, good := say(run(ctx, []string{"xdotool", "mousemove", "--sync", x, y}, 15))
		return t, "", good
	case "drag":
		x, err := point(a.X, "x")
		if err != nil {
			return err.Error(), "", false
		}
		y, err := point(a.Y, "y")
		if err != nil {
			return err.Error(), "", false
		}
		toX, err := point(a.ToX, "toX")
		if err != nil {
			return err.Error(), "", false
		}
		toY, err := point(a.ToY, "toY")
		if err != nil {
			return err.Error(), "", false
		}
		t, good := say(run(ctx, []string{
			"xdotool", "mousemove", "--sync", x, y,
			"mousedown", "1",
			"mousemove", "--sync", toX, toY,
			"mouseup", "1",
		}, 20))
		return t, "", good
	case "type":
		if a.Text == "" || len(a.Text) > 4000 {
			return "text is required, and at most 4000 characters", "", false
		}
		t, good := say(run(ctx, []string{"xdotool", "type", "--delay", "12", "--clearmodifiers", "--", a.Text}, 60))
		return t, "", good
	case "key":
		words := strings.Fields(strings.NewReplacer(",", " ").Replace(a.Keys))
		if len(words) == 0 || len(words) > 10 {
			return "keys is one to ten keys or combinations, like Return or ctrl+l", "", false
		}
		for _, w := range words {
			if !keyWord.MatchString(w) {
				return fmt.Sprintf("%q is not a key name: use names like Return, Tab, ctrl+l, alt+Left", w), "", false
			}
		}
		t, good := say(run(ctx, append([]string{"xdotool", "key", "--clearmodifiers"}, words...), 15))
		return t, "", good
	case "scroll":
		x, err := point(a.X, "x")
		if err != nil {
			return err.Error(), "", false
		}
		y, err := point(a.Y, "y")
		if err != nil {
			return err.Error(), "", false
		}
		if a.DY == nil || *a.DY == 0 || *a.DY > 50 || *a.DY < -50 {
			return "dy is a number of notches between -50 and 50, not zero", "", false
		}
		button, n := "5", *a.DY
		if n < 0 {
			button, n = "4", -n
		}
		t, good := say(run(ctx, []string{"xdotool", "mousemove", "--sync", x, y, "click", "--repeat", strconv.Itoa(n), "--delay", "30", button}, 30))
		return t, "", good
	case "navigate":
		u, err := url.Parse(strings.TrimSpace(a.URL))
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(a.URL) > 2000 {
			return "url must be an http or https address", "", false
		}
		// The address is a positional argument, never part of the script, so nothing
		// in it can be read as shell.
		script := `xdotool key --clearmodifiers ctrl+l; sleep 0.3; xdotool type --delay 8 --clearmodifiers -- "$1"; xdotool key --clearmodifiers Return`
		t, good := say(run(ctx, []string{"sh", "-c", script, "sh", u.String()}, 30))
		return t, "", good
	case "autofill":
		if fill == nil {
			return "Vault autofill is not mounted on this sandbox.", "", false
		}
		t, good := fill(ctx, a.Credential, a.Field, a.Submit)
		return t, "", good
	case "save_credential":
		if save == nil {
			return "Vault credential sealing is not mounted on this sandbox.", "", false
		}
		t, good := save(ctx, a.Credential, a.URL)
		return t, "", good
	case "wait":
		d := min(max(a.Seconds, 0), 10)
		select {
		case <-time.After(time.Duration(d * float64(time.Second))):
			return fmt.Sprintf("Waited %.1f seconds.", d), "", true
		case <-ctx.Done():
			return "The turn ended while waiting.", "", false
		}
	}
	return fmt.Sprintf("There is no tool named %q.", name), "", false
}

// Computer performs one validated GUI or shell action inside a sandbox the caller
// holds. It uses the exact argv validation and X11 :1 tools (`scrot`, `xdotool`)
// that converse uses, so an autonomous bot driving its own desktop computer and a
// person's sandbox agent share one implementation and one safety boundary.
func Computer(s *Service, ctx context.Context, org string, in client.ComputerIn) (client.ComputerOut, error) {
	action := strings.ToLower(strings.TrimSpace(in.Action))
	if action == "" {
		return client.ComputerOut{}, zip.ErrBadRequest("action is required: screenshot, click, move, drag, scroll, type, key, navigate, autofill, save_credential, wait, or shell")
	}
	// Accept common aliases so models trained on standard computer-use toolkits
	// work on the first try without guessing vocabulary.
	switch action {
	case "double_click":
		action = "click"
		in.Double = true
	case "right_click":
		action = "click"
		in.Button = "right"
	case "middle_click":
		action = "click"
		in.Button = "middle"
	case "mouse_move":
		action = "move"
	case "left_click_drag":
		action = "drag"
	}
	m, _, err := find(s, ctx, org, strings.TrimSpace(in.ID), "computer")
	if err != nil {
		return client.ComputerOut{}, err
	}
	if m.Status != statusRunning {
		return client.ComputerOut{}, zip.Errorf(http.StatusConflict, "sandbox is %s", cmp.Or(m.Status, "unknown"))
	}
	raw, err := json.Marshal(toolArgs{
		X: in.X, Y: in.Y, ToX: in.ToX, ToY: in.ToY, DY: in.DY,
		Button: in.Button, Double: in.Double, Text: in.Text,
		Keys: in.Keys, URL: in.URL, Credential: in.Credential,
		Field: in.Field, Submit: in.Submit, Command: in.Command, Seconds: in.Seconds,
	})
	if err != nil {
		return client.ComputerOut{}, zip.ErrBadRequest("invalid computer arguments")
	}
	screen := classes[m.Class].screen
	run := func(rc context.Context, argv []string, t int) (ExecResult, error) {
		return Run(s, rc, org, m.ID, Cmd{Argv: argv, TimeoutSec: t})
	}
	text, pic, ok := actWithVault(ctx, run, makeAutofill(s, org, m.ID, screen), makeSaveCredential(s, org, m.ID), action, string(raw), screen)
	return client.ComputerOut{OK: ok, Text: text, Image: pic}, nil
}

// clip bounds a tool's text, saying so where it cut.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… (cut; " + strconv.Itoa(len(s)-n) + " more characters)"
}

// thin keeps the pictures a model is shown to the latest few: the earlier ones are
// words in the conversation from then on.
func thin(msgs []infer.ChatMessage) []infer.ChatMessage {
	kept := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		if len(msgs[i].Images) == 0 {
			continue
		}
		if kept++; kept > keptShots {
			msgs[i].Images = nil
			msgs[i].Content = "(an earlier look at the screen)"
		}
	}
	return msgs
}

// converse runs one turn: ask the model, run the tools it asks for, show it the
// screen it left, and go round again until it answers in words. emit says what
// happened as it does and answers false when nobody is listening any more, which
// ends the turn — nobody is watching, and a model should not go on spending for
// no one.
func converse(ctx context.Context, ai infer.Client, req infer.ChatRequest, in agentIn, screen bool, run shellFn, emit func(agentFrame) bool) {
	converseWithVault(ctx, ai, req, in, screen, run, nil, nil, emit)
}

func converseWithVault(ctx context.Context, ai infer.Client, req infer.ChatRequest, in agentIn, screen bool, run shellFn, fill autofillFn, save saveCredentialFn, emit func(agentFrame) bool) {
	system := shellPrompt
	if screen {
		system = screenPrompt
	}
	msgs := []infer.ChatMessage{{Role: infer.RoleSystem, Content: system}}
	for _, h := range in.History {
		if role := strings.ToLower(h.Role); (role == infer.RoleUser || role == infer.RoleAssistant) && strings.TrimSpace(h.Text) != "" {
			msgs = append(msgs, infer.ChatMessage{Role: role, Content: h.Text})
		}
	}
	msgs = append(msgs, infer.ChatMessage{Role: infer.RoleUser, Content: in.Message})
	tools := agentTools(screen)

	rounds := 0
	for ; rounds < agentRounds(); rounds++ {
		if ctx.Err() != nil {
			emit(agentFrame{Type: "error", Text: "The turn ran out of time."})
			return
		}
		req.Messages, req.Tools = msgs, tools
		resp, err := ai.ChatCompletion(ctx, &req)
		if err != nil || resp == nil {
			why := "the model gave no answer"
			if err != nil {
				why = err.Error()
			}
			emit(agentFrame{Type: "error", Text: "The model could not be reached: " + why})
			return
		}
		if strings.TrimSpace(resp.Content) != "" && !emit(agentFrame{Type: "say", Text: resp.Content}) {
			return
		}
		if len(resp.ToolCalls) == 0 {
			emit(agentFrame{Type: "end", Rounds: rounds + 1})
			return
		}
		msgs = append(msgs, infer.ChatMessage{Role: infer.RoleAssistant, Content: resp.Content, ToolCalls: resp.ToolCalls})
		var picture string
		for _, tc := range resp.ToolCalls {
			args := json.RawMessage(tc.Arguments)
			if !json.Valid(args) {
				args = nil
			}
			if !emit(agentFrame{Type: "act", Tool: tc.Name, Args: args}) {
				return
			}
			text, shot, ok := actWithVault(ctx, run, fill, save, tc.Name, tc.Arguments, screen)
			if shot != "" {
				picture = shot
			}
			if !emit(agentFrame{Type: "result", Tool: tc.Name, OK: &ok, Text: text}) {
				return
			}
			msgs = append(msgs, infer.ChatMessage{Role: infer.RoleTool, ToolCallID: tc.ID, Name: tc.Name, Content: text})
		}
		if picture != "" {
			// A tool result is words; a picture goes to the model as what its person
			// would show it, straight after the results it answers.
			msgs = thin(append(msgs, infer.ChatMessage{Role: infer.RoleUser, Content: "The screen now.", Images: []string{picture}}))
		}
	}
	emit(agentFrame{Type: "error", Text: fmt.Sprintf("The turn used all %d rounds and stopped. Say \"continue\" to go on.", rounds)})
}

// stream writes the stream: one event a frame, and a comment every few seconds so
// an intermediary does not close it while the model thinks.
type stream struct {
	mu   sync.Mutex
	w    *bufio.Writer
	dead bool
	done chan struct{}
}

func newStream(w *bufio.Writer) *stream {
	f := &stream{w: w, done: make(chan struct{})}
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-f.done:
				return
			case <-t.C:
				f.write(": ping\n\n")
			}
		}
	}()
	return f
}

func (f *stream) write(s string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.dead {
		return false
	}
	if _, err := f.w.WriteString(s); err != nil || f.w.Flush() != nil {
		f.dead = true
	}
	return !f.dead
}

func (f *stream) send(fr agentFrame) bool {
	b, _ := json.Marshal(fr)
	return f.write("event: " + fr.Type + "\ndata: " + string(b) + "\n\n")
}

func (f *stream) stop() { close(f.done) }

// agentRun serves one turn.
func agentRun(s *Service, c *zip.Ctx) error {
	org, ok := orgOf(c)
	if !ok {
		return principal.Refused(c)
	}
	var in agentIn
	if err := json.Unmarshal(c.Body(), &in); err != nil {
		return zip.ErrBadRequest("body is JSON: {\"message\": \"what to do\"}")
	}
	if in.Message = strings.TrimSpace(in.Message); in.Message == "" || len(in.Message) > 16000 {
		return zip.ErrBadRequest("message is required: what to do, in words (at most 16000 characters)")
	}
	if len(in.History) > 200 {
		return zip.ErrBadRequest("history is at most 200 turns")
	}
	ctx := c.Forward()
	m, _, err := find(s, ctx, org, idParam(c), "agent")
	if err != nil {
		return err
	}
	if m.Status != statusRunning {
		return zip.Errorf(http.StatusConflict, "sandbox is %s", cmp.Or(m.Status, "unknown"))
	}
	if s.State.ai == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "sandbox: no model gateway is mounted here, so nothing can work this computer")
	}
	req := infer.ChatRequest{
		Model:     cmp.Or(strings.TrimSpace(in.Model), agentModel()),
		Org:       org,
		Payer:     cloud.PayerOf(ctx).Wallet,
		MaxTokens: 1500,
		Actor:     org + "/" + cloud.Who(ctx).User,
		Feature:   "sandbox.agent",
	}
	id, screen := m.ID, classes[m.Class].screen
	run := func(rc context.Context, argv []string, t int) (ExecResult, error) {
		return Run(s, rc, org, id, Cmd{Argv: argv, TimeoutSec: t})
	}
	// The turn outlives the handler that started it, as a stream does, and states
	// the caller again on the way out: every call it makes authorizes on them.
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), agentBudget())
	c.SetHeader("Content-Type", "text/event-stream")
	c.SetHeader("Cache-Control", "no-store")
	c.SetHeader("X-Accel-Buffering", "no")
	return c.SendStreamWriter(func(w *bufio.Writer) {
		defer cancel()
		f := newStream(w)
		defer f.stop()
		converseWithVault(bg, s.State.ai, req, in, screen, run, makeAutofill(s, org, id, screen), makeSaveCredential(s, org, id), f.send)
	})
}

func init() {
	spec.Describe("/v1/sandbox/:id/agent", http.MethodPost,
		"Have a model work a sandbox",
		"Says what to do on a sandbox you hold, and a model does it there while you watch: a shell on any "+
			"sandbox, and on a desktop or a browser the screen too — it takes a screenshot, clicks where it "+
			"sees, types, presses keys, opens addresses and runs commands. The answer is a "+
			"text/event-stream, one event a step, ending with `end`.\n\n"+
			"The body is JSON: `message` (required, what to do, in words), `history` (the conversation so far, "+
			"oldest first, each {role: user|assistant, text}) and `model` (a model id from GET /v1/models; "+
			"empty takes the deployment's).\n\n"+
			"Events (`event:` name, `data:` JSON): `say` {text} the model's words; `act` {tool, args} a tool it "+
			"reached for; `result` {tool, ok, text} what the tool answered; `error` {text} the turn could not "+
			"go on; `end` {rounds} the turn is over. A `: ping` comment every 15 seconds keeps intermediaries "+
			"from closing the stream.\n\n"+
			"The server keeps nothing between turns: send the conversation so far as `history` "+
			"({role: user|assistant, text}). The model is billed to the caller's account at the gateway's rates, "+
			"and the sandbox hour is the one the lease already bills. Open the sandbox's screen "+
			"(POST /v1/sandbox/{id}/screen/ticket) beside it to watch.\n\n"+
			"409 when the sandbox is not running; 404 for a sandbox you do not hold; 503 where no model "+
			"gateway is mounted.")
}
