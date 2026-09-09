package bot

import (
	"fmt"
	"sort"
	"testing"

	"github.com/zap-proto/zip"
)

// A board is one document holding every widget on it, and one producer is not
// the only thing writing to it: an agent puts widgets while the person watching
// rearranges them and answers grant prompts. These tests put several of those
// in flight at once, which is the ordinary case rather than a pathological one.

// widgets reads a board back by widget name.
func widgets(t *testing.T, app *zip.App, w who, session string) map[string]map[string]any {
	t.Helper()
	_, frame := ask(t, app, w, "b:"+session, "board.get", `{"sessionKey":"`+session+`"}`)
	if frame["ok"] != true {
		t.Fatalf("board.get refused: %v", frame)
	}
	rows, _ := payload(t, frame)["widgets"].([]any)
	out := map[string]map[string]any{}
	for _, row := range rows {
		if r, ok := row.(map[string]any); ok {
			name, _ := r["name"].(string)
			out[name] = r
		}
	}
	return out
}

// aside runs one call beside whatever the test is doing and hands back its
// answer when the test asks for it. Nothing fails from the goroutine: a call
// that could not be made answers nil, and the test says so where it can.
func aside(app *zip.App, w who, id, method, params string) func() map[string]any {
	out := make(chan map[string]any, 1)
	go func() {
		res, err := app.Fiber().Test(asking(w, id, method, params))
		if err != nil {
			out <- nil
			return
		}
		_, frame, err := answered(res)
		if err != nil {
			frame = nil
		}
		out <- frame
	}()
	return func() map[string]any { return <-out }
}

// Every accepted put is on the board. A put reads the whole board, adds its
// widget and stores the whole board back, so two puts that each read before
// either wrote keep only the second one's widget — and the producer of the
// first was told ok.
func TestConcurrentWidgetPutsAllLand(t *testing.T) {
	app := mount(t)
	me := who{org: "acme"}

	const puts = 8
	for round := range 8 {
		session := fmt.Sprintf("k%d", round)
		params := make([]string, puts)
		for i := range params {
			params[i] = fmt.Sprintf(
				`{"sessionKey":%q,"name":"w%d","content":{"kind":"html","html":"<p>w%d</p>"}}`, session, i, i)
		}
		for i, frame := range atOnce(t, app, me, "board.widget.put", params) {
			if frame["ok"] != true {
				t.Fatalf("put %d refused: %v", i, frame)
			}
		}

		held := widgets(t, app, me, session)
		lost := []string{}
		for i := range puts {
			if name := fmt.Sprintf("w%d", i); held[name] == nil {
				lost = append(lost, name)
			}
		}
		sort.Strings(lost)
		if len(lost) > 0 {
			t.Fatalf("%d of %d accepted widget puts are not on the board: %v", len(lost), puts, lost)
		}
	}
}

// An approval that answers ok is an approval. The person is shown what a widget
// asks for and says yes; if the grant is recorded into a copy of the board that
// another put then replaces, the prompt comes back and the answer they gave
// went nowhere — the same lying success as a Stop button over a run that keeps
// going.
func TestAGrantSurvivesConcurrentPuts(t *testing.T) {
	app := mount(t)
	me := who{org: "acme", admin: true}

	for round := range 12 {
		session := fmt.Sprintf("k%d", round)
		put := `{"sessionKey":"` + session + `","name":"a","content":{"kind":"html","html":"<p>a</p>"},` +
			`"declared":{"tools":["sessions.list"]}}`
		if _, frame := ask(t, app, me, "p:a", "board.widget.put", put); frame["ok"] != true {
			t.Fatalf("put: %v", frame)
		}
		row := widgets(t, app, me, session)["a"]
		if row == nil || row["grantState"] != "pending" {
			t.Fatalf("a widget that declared a tool is %v, want pending", row)
		}
		revision, _ := row["revision"].(float64)
		instance, _ := row["instanceId"].(string)

		// The person answers the prompt while the producer keeps writing.
		answer := aside(app, me, "g:a", "board.widget.grant", fmt.Sprintf(
			`{"sessionKey":%q,"name":"a","decision":"granted","revision":%d,"instanceId":%q}`,
			session, int(revision), instance))
		noise := make([]string, 6)
		for i := range noise {
			noise[i] = fmt.Sprintf(
				`{"sessionKey":%q,"name":"n%d","content":{"kind":"html","html":"<p>n%d</p>"}}`, session, i, i)
		}
		atOnce(t, app, me, "board.widget.put", noise)

		granted := answer()
		if granted == nil {
			t.Fatal("the grant could not be made")
		}
		if granted["ok"] != true {
			continue // refused, and a refusal is an honest answer
		}
		if state := widgets(t, app, me, session)["a"]["grantState"]; state != "granted" {
			t.Fatalf("board.widget.grant answered ok and the widget is %q: the person's approval was recorded into a board that was then replaced",
				state)
		}
	}
}
