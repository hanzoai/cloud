package event

import "testing"

func TestLocalHost(t *testing.T) {
	for h, want := range map[string]bool{
		"localhost": true, "LOCALHOST": true, "127.0.0.1": true, "127.5.5.5": true, "::1": true, "[::1]": true,
		"0.0.0.0": true, "app.localhost": true, "mac.local": true,
		"hanzo.ai": false, "localhost.hanzo.ai": false, "10.0.0.5": false, "": false,
	} {
		if got := localHost(h); got != want {
			t.Errorf("localHost(%q) = %v, want %v", h, got, want)
		}
	}
}

func TestFromLocal(t *testing.T) {
	if !fromLocal("http://localhost:3000/x", "") {
		t.Error("localhost url")
	}
	if fromLocal("https://hanzo.ai/", "http://localhost:3000") {
		t.Error("the url names the page, not the origin")
	}
	if !fromLocal("", "http://127.0.0.1:5173") {
		t.Error("origin answers for an event with no url")
	}
	if fromLocal("", "") || fromLocal("/relative", "") {
		t.Error("no host is not local")
	}
}

func TestLegacyExceptionBecomesError(t *testing.T) {
	e := CaptureEvent{Type: "event", Event: "$exception", Error: &Exception{Type: "TypeError", Message: "x"}}
	e.Type, e.Event = "error", ""
	if routeOf(e).signal != signalError {
		t.Fatal("not filed as an error")
	}
}

func TestStampTheRequest(t *testing.T) {
	got, local := stamp([]CaptureEvent{
		{Event: "a", URL: "https://hanzo.ai/x", Properties: map[string]any{"country": "ZZ", "user_agent": "forged", "ip": "9.9.9.9", "k": "v", "consent": "analytics"}},
		{Event: "b", URL: "http://localhost:3000/"},
		{Event: "c", URL: "http://localhost:3000/", keepLocal: true, Properties: map[string]any{"consent": "analytics"}},
		{Type: "event", Event: "$exception", URL: "https://hanzo.ai/", Error: &Exception{Message: "x"}},
	}, visit{ip: "203.0.113.7", country: "US", ua: "Mozilla/5.0 test"})
	if local != 1 || len(got) != 3 {
		t.Fatalf("dropped %d kept %d, want 1 and 3", local, len(got))
	}
	a := got[0]
	if a.ip != "203.0.113.7" {
		t.Errorf("ip = %q", a.ip)
	}
	if a.Properties["country"] != "US" || a.Properties["user_agent"] != "Mozilla/5.0 test" {
		t.Errorf("country and user agent come from the request: %v", a.Properties)
	}
	if _, ok := a.Properties["ip"]; ok || a.Properties["k"] != "v" {
		t.Errorf("ip is never a property, the rest stays: %v", a.Properties)
	}
	if got[1].Event != "c" {
		t.Errorf("a project that keeps local keeps it: %q", got[1].Event)
	}
	if e := got[2]; e.Type != "error" || e.Event != "" || routeOf(e).signal != signalError {
		t.Errorf("legacy $exception is an error: %+v", e)
	}
	none, _ := stamp([]CaptureEvent{{Event: "a", Properties: map[string]any{"country": "ZZ", "consent": "analytics"}}}, visit{})
	if _, ok := none[0].Properties["country"]; ok {
		t.Error("an unattested country is absent, never the caller's word")
	}
}

func TestDevice(t *testing.T) {
	for _, c := range []struct{ ua, browser, os, class string }{
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Safari/605.1.15", "Safari", "macOS", "desktop"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", "Chrome", "Windows", "desktop"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0", "Edge", "Windows", "desktop"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1", "Safari", "iOS", "mobile"},
		{"Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/126.0 Mobile/15E148 Safari/604.1", "Chrome", "iOS", "tablet"},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36", "Chrome", "Android", "mobile"},
		{"Mozilla/5.0 (Linux; Android 13; SM-X700) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36", "Chrome", "Android", "tablet"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:128.0) Gecko/20100101 Firefox/128.0", "Firefox", "Linux", "desktop"},
		{"curl/8.4.0", "Other", "Other", "desktop"},
	} {
		b, o, d := device(c.ua)
		if b != c.browser || o != c.os || d != c.class {
			t.Errorf("%q = %s/%s/%s, want %s/%s/%s", c.ua, b, o, d, c.browser, c.os, c.class)
		}
	}
}

func TestLegacyExceptionFromProperties(t *testing.T) {
	e := CaptureEvent{Type: "event", Event: "$exception", URL: "https://hanzo.ai/", Properties: map[string]any{
		"$exception_list": []any{map[string]any{"type": "TypeError", "value": "x is undefined", "stacktrace": map[string]any{
			"frames": []any{map[string]any{"mangled_name": "f", "source": "https://hanzo.ai/a.js", "line": float64(3), "column": float64(9)}},
		}}},
		"$exception_handled": false,
	}}
	got, _ := stamp([]CaptureEvent{e}, visit{country: "US"})
	if len(got) != 1 || got[0].Type != "error" || got[0].Error == nil {
		t.Fatalf("not an error: %+v", got)
	}
	x := got[0].Error
	if x.Type != "TypeError" || x.Message != "x is undefined" || len(x.Frames) != 1 || x.Frames[0].Line != 3 || x.Handled == nil || *x.Handled {
		t.Errorf("error = %+v", x)
	}
	if routeOf(got[0]).signal != signalError {
		t.Error("not filed on the error plane")
	}
	// An act that names nothing stays an act.
	act := CaptureEvent{Type: "event", Event: "$exception", URL: "https://hanzo.ai/", Properties: map[string]any{"consent": "analytics"}}
	plain, _ := stamp([]CaptureEvent{act}, visit{country: "US"})
	if plain[0].Type == "error" {
		t.Error("an empty $exception is not an error")
	}
}
