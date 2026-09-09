// botdrive opens the protocol socket at /v1/bot and walks the floor the control
// UI walks, printing what came back.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fasthttp/websocket"
)

var ws *websocket.Conn

func main() {
	url := flag.String("url", "ws://127.0.0.1:8899/v1/bot", "socket")
	flag.Parse()

	var err error
	ws, _, err = websocket.DefaultDialer.Dial(*url, nil)
	if err != nil {
		fmt.Println("DIAL FAILED:", err)
		os.Exit(1)
	}
	defer ws.Close() //nolint:errcheck
	fmt.Println("dialed", *url)

	// connect first: the hello-ok is the whole contract in one frame.
	hello := ask("1", "connect", `{"protocolVersion":4,"client":{"name":"botdrive","version":"1"},"caps":{}}`)
	report("connect", hello)
	if p, ok := hello["payload"].(map[string]any); ok {
		fmt.Println("  protocol:", p["protocol"])
		if f, ok := p["features"].(map[string]any); ok {
			if m, ok := f["methods"].([]any); ok {
				fmt.Println("  features.methods:", len(m))
			}
		}
		if pol, ok := p["policy"].(map[string]any); ok {
			fmt.Println("  policy:", brief(pol))
		}
	}

	// the floor, in the order the UI walks it.
	report("sessions.list", ask("2", "sessions.list", `{}`))
	report("agents.list", ask("3", "agents.list", `{}`))
	report("models.list", ask("4", "models.list", `{}`))
	report("commands.list", ask("5", "commands.list", `{}`))
	report("plugins.list", ask("6", "plugins.list", `{}`))
	report("health", ask("7", "health", `{}`))
	report("sessions.subscribe", ask("8", "sessions.subscribe", `{}`))
	report("system.info", ask("9", "system.info", `{}`))

	created := ask("10", "sessions.create", `{"key":"main","idempotencyKey":"drive-1","label":"Main"}`)
	report("sessions.create", created)
	report("sessions.resolve", ask("11", "sessions.resolve", `{"key":"main"}`))
	report("sessions.describe", ask("12", "sessions.describe", `{"key":"main"}`))
	report("chat.startup", ask("13", "chat.startup", `{"sessionKey":"main"}`))
	report("chat.history", ask("14", "chat.history", `{"sessionKey":"main"}`))
	report("chat.metadata", ask("15", "chat.metadata", `{"sessionKey":"main"}`))

	// the turn.
	fmt.Println("\n--- chat.send: the turn ---")
	sent := ask("16", "chat.send", `{"sessionKey":"main","message":"hello from botdrive","idempotencyKey":"run-abc"}`)
	report("chat.send", sent)
	drain(6 * time.Second)

	fmt.Println("\n--- transcript after the turn ---")
	report("chat.history", ask("17", "chat.history", `{"sessionKey":"main"}`))

	fmt.Println("\n--- chat.abort on a finished run ---")
	report("chat.abort", ask("18", "chat.abort", `{"sessionKey":"main","runId":"run-abc"}`))
}

func ask(id, method, params string) map[string]any {
	frame := fmt.Sprintf(`{"type":"req","id":%q,"method":%q,"params":%s}`, id, method, params)
	if err := ws.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
		fmt.Println("write", method, err)
		os.Exit(1)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		_ = ws.SetReadDeadline(deadline)
		_, data, err := ws.ReadMessage()
		if err != nil {
			return map[string]any{"__read_error": err.Error()}
		}
		var m map[string]any
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m["type"] == "res" && m["id"] == id {
			return m
		}
		if m["type"] == "event" && m["event"] != "tick" {
			fmt.Printf("  << event %v %s\n", m["event"], clip(m["payload"], 120))
		}
	}
}

// drain reads whatever the server says on its own initiative for a while.
func drain(d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		_ = ws.SetReadDeadline(end)
		_, data, err := ws.ReadMessage()
		if err != nil {
			return
		}
		var m map[string]any
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		if m["event"] == "tick" {
			continue
		}
		payload, _ := m["payload"].(map[string]any)
		fmt.Printf("  << event %-18v state=%-8v %s\n", m["event"], payload["state"], clip(m["payload"], 200))
		if m["event"] == "chat" && (payload["state"] == "final" || payload["state"] == "error" || payload["state"] == "aborted") {
			return
		}
	}
}

func report(name string, m map[string]any) {
	if e, ok := m["__read_error"]; ok {
		fmt.Printf("%-30s READ ERROR %v\n", name, e)
		return
	}
	ok, _ := m["ok"].(bool)
	mark := "ok  "
	if !ok {
		mark = "FAIL"
	}
	fmt.Printf("%-30s %s %s\n", name, mark, clip(pick(m), 220))
}

func pick(m map[string]any) any {
	if v, ok := m["payload"]; ok && m["ok"] == true {
		return v
	}
	if v, ok := m["error"]; ok {
		return v
	}
	return m
}

func brief(m map[string]any) string { return clip(m, 200) }

func clip(v any, n int) string {
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > n {
		return s[:n] + "…"
	}
	return strings.TrimSpace(s)
}
