package cli

// gpu_lanes_test.go — the presence record carries the lanes the local studio can
// render, the render handler collects every kind of file a studio saves, and a
// long-running worker asks HANZO_TOKEN_COMMAND for its bearer.

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRegistrationCarriesLanes(t *testing.T) {
	w := &worker{lanes: []string{"image.zen", "mesh"}, models: []laneModel{{ID: "triposr", Lane: "mesh", Licence: "MIT"}}}
	raw, err := json.Marshal(w.buildRegistration())
	if err != nil {
		t.Fatal(err)
	}
	var got fleetWorker
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Lanes, w.lanes) || len(got.Models) != 1 || got.Models[0] != w.models[0] {
		t.Fatalf("registration = %s", raw)
	}
}

// A studio that cannot render advertises no lanes, whatever it said before.
func TestNoLanesWhileNotReady(t *testing.T) {
	w := &worker{lanes: []string{"image.zen"}, studioReady: false}
	if !w.refreshLanes(t.Context()) {
		t.Fatal("dropping the lanes of a node that stopped rendering is a change")
	}
	if len(w.lanes) != 0 || len(w.models) != 0 {
		t.Fatalf("lanes = %v, want none", w.lanes)
	}
	if w.refreshLanes(t.Context()) {
		t.Fatal("no lanes twice is not a change")
	}
}

func TestCollectOutputsTakesImagesMeshesAndAudio(t *testing.T) {
	entry := []byte(`{"outputs":{
	  "9":{"images":[{"filename":"a.png","subfolder":"image"}]},
	  "6":{"3d":[{"filename":"m.glb","subfolder":"model3d"}]},
	  "7":{"audio":[{"filename":"s.mp3","subfolder":"music"}]}}}`)
	got := collectOutputs(entry)
	slices.Sort(got)
	want := []string{"image/a.png", "model3d/m.glb", "music/s.mp3"}
	if !slices.Equal(got, want) {
		t.Fatalf("outputs = %v, want %v", got, want)
	}
}

// HANZO_TOKEN_COMMAND is asked for a bearer, and asked again only near its expiry.
func TestTokenCommandIsAskedAgainNearExpiry(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(dir, "count")
	script := filepath.Join(dir, "tok")
	// A JWT whose payload is {"exp": <exp>}: header and signature are not read here.
	mint := func(exp int64) string {
		p := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp)))
		return "eyJhbGciOiJub25lIn0." + p + ".sig"
	}
	write := func(tok string) {
		body := "#!/bin/sh\necho x >> " + count + "\necho " + tok + "\n"
		if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	runs := func() int {
		b, _ := os.ReadFile(count)
		return strings.Count(string(b), "x")
	}
	var tc tokenCommand
	write(mint(time.Now().Add(time.Hour).Unix()))
	a, err := tc.token(t.Context(), script)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := tc.token(t.Context(), script)
	if a != b || runs() != 1 {
		t.Fatalf("a fresh token was asked for again: %d runs", runs())
	}
	write(mint(time.Now().Add(time.Hour).Unix()))
	tc.exp = time.Now().Add(30 * time.Second).Unix() // the held one is about to lapse
	if _, err := tc.token(t.Context(), script); err != nil || runs() != 2 {
		t.Fatalf("a lapsing token was not refreshed: %v, %d runs", err, runs())
	}
	write("not-a-token")
	tc.exp = 0
	if _, err := tc.token(t.Context(), script); err == nil {
		t.Fatal("a command that prints no token must fail")
	}
}

// A prompt the studio failed is a failed render, with the node and its reason.
func TestRenderFailedReadsTheStudiosVerdict(t *testing.T) {
	failed := []byte(`{"outputs":{},"status":{"status_str":"error","completed":false,"messages":[
	  ["execution_start",{"prompt_id":"p"}],
	  ["execution_error",{"node_type":"CLIPLoader","exception_message":"Error while initializing BPE"}]]}}`)
	if got := renderFailed(failed); got != "CLIPLoader: Error while initializing BPE" {
		t.Fatalf("renderFailed = %q", got)
	}
	ok := []byte(`{"outputs":{"9":{"images":[{"filename":"a.png","subfolder":""}]}},"status":{"status_str":"success","completed":true}}`)
	if got := renderFailed(ok); got != "" {
		t.Fatalf("a successful prompt read as failed: %q", got)
	}
}
