package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hanzoai/cloud/forge"
)

// The release train of hanzo-inc/cloud as the fake forge holds it: v8.5.1129 is
// smoked, pinned and live; v8.5.1130 and v8.5.1131 were claimed and their runs
// failed; main is ahead of them with a run still going.
const (
	sha1129 = "3bef4b1c597c0ccd450c6eaef2beefeeae2ef2be"
	sha1130 = "160edf6dd07d044722bf3efed5fab1f85f1d0de0"
	sha1131 = "883e9cd244be1c39f38229fcb6216a55653b7289"
	shaMain = "0c66629ea5123914b0ab9e605bd4bba320fcd473"
	digest  = "sha256:66e5ed30fd87e1c5a99639d89c15531eaf49b727bd3b06c78a02d7c0ba1e1f29"
)

// trainForge answers the forge's reads of hanzo-inc/cloud, each of which must
// be made as the caller, and counts the tag objects it is asked to peel.
func trainForge(t *testing.T) *atomic.Int64 {
	var peels atomic.Int64
	t.Helper()
	type job struct {
		id               int
		name, conclusion string
		steps            bool
	}
	runs := map[string]struct {
		id, number         int
		status, conclusion string
		jobs               []job
	}{
		sha1129: {17600, 1420, "completed", "success", []job{{1, "gate", "success", true}}},
		sha1130: {17647, 1423, "completed", "failure", []job{{74140, "unit", "failure", false}, {74143, "test-amd64", "failure", true}, {74147, "image", "success", true}}},
		sha1131: {17654, 1425, "completed", "failure", []job{{74238, "test-amd64", "failure", true}, {74242, "image", "failure", true}}},
		shaMain: {17677, 1426, "in_progress", "", []job{{74276, "review", "failure", true}}},
	}
	logs := map[int]string{
		74143: "2026-11-11T08:44:30Z --- FAIL: TestRecord (0.02s)\n2026-11-11T08:44:31Z FAIL\tgithub.com/hanzoai/cloud\t9.1s\n",
		74238: "2026-11-11T12:11:00Z --- FAIL: TestLive (0.01s)\n",
		74242: "2026-11-11T11:47:40Z go: downloading x\n2026-11-11T11:47:47Z ##[error]go mod download: 403\n",
		74276: "2026-11-12T06:53:20Z ##[error]review refused the diff\n",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Sudo") != "z" {
			http.Error(w, "read as the caller", http.StatusBadRequest)
			return
		}
		enc := json.NewEncoder(w)
		p := strings.TrimPrefix(r.URL.Path, forge.API+"/repos/hanzo-inc/cloud")
		switch {
		case p == "/git/commits/HEAD":
			_ = enc.Encode(map[string]any{"sha": shaMain})
		case strings.HasPrefix(p, "/git/tags/"):
			peels.Add(1)
			_ = enc.Encode(map[string]any{"object": map[string]any{"type": "commit", "sha": sha1131}})
		case p == "/git/refs/tags/v":
			old := []any{}
			for i := range 200 { // an old line of annotated releases, none of them read
				old = append(old, map[string]any{"ref": "refs/tags/v8.4." + strconv.Itoa(i),
					"object": map[string]any{"type": "tag", "sha": strings.Repeat("a", 39) + strconv.Itoa(i%10)}})
			}
			_ = enc.Encode(append(old,
				map[string]any{"ref": "refs/tags/v8.5.1129", "object": map[string]any{"type": "commit", "sha": sha1129}},
				map[string]any{"ref": "refs/tags/v8.5.1131", "object": map[string]any{"type": "commit", "sha": sha1131}},
				map[string]any{"ref": "refs/tags/v8.5.1130", "object": map[string]any{"type": "commit", "sha": sha1130}},
				map[string]any{"ref": "refs/tags/v8.5.99", "object": map[string]any{"type": "commit", "sha": sha1129}},
				map[string]any{"ref": "refs/tags/v8.5.1131-rc1", "object": map[string]any{"type": "commit", "sha": sha1131}},
			))
		case p == "/git/refs/tags/smoked/sha256-":
			_ = enc.Encode([]any{map[string]any{"ref": "refs/tags/smoked/sha256-" + strings.TrimPrefix(digest, "sha256:"),
				"object": map[string]any{"type": "commit", "sha": sha1129}}})
		case p == "/actions/runs":
			run, ok := runs[r.URL.Query().Get("head_sha")]
			list := []any{}
			if ok {
				list = append(list, map[string]any{"id": run.id, "run_number": run.number, "run_attempt": 1, "path": "cicd.yml@refs/heads/main",
					"head_branch": "main", "head_sha": r.URL.Query().Get("head_sha"), "status": run.status, "conclusion": run.conclusion,
					"html_url": "https://git.hanzo.test/hanzo-inc/cloud/actions/runs/" + strconv.Itoa(run.number)})
			}
			_ = enc.Encode(map[string]any{"total_count": len(list), "workflow_runs": list})
		case strings.HasPrefix(p, "/actions/runs/") && strings.HasSuffix(p, "/jobs"):
			for _, run := range runs {
				if p != "/actions/runs/"+strconv.Itoa(run.id)+"/jobs" {
					continue
				}
				jobs := []any{}
				for _, j := range run.jobs {
					steps := []any{}
					if j.steps {
						steps = append(steps, map[string]any{"number": 1, "name": "run", "status": "completed", "conclusion": j.conclusion})
					}
					jobs = append(jobs, map[string]any{"id": j.id, "run_id": run.id, "name": j.name, "status": "completed", "conclusion": j.conclusion, "steps": steps})
				}
				_ = enc.Encode(map[string]any{"total_count": len(jobs), "jobs": jobs})
				return
			}
			http.NotFound(w, r)
		case strings.HasPrefix(p, "/actions/jobs/") && strings.HasSuffix(p, "/logs"):
			id, _ := strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(p, "/actions/jobs/"), "/logs"))
			l, ok := logs[id]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write([]byte(l))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	prev := releaseForge
	releaseForge = func(context.Context) (*forge.Client, error) {
		c, err := forge.New(srv.URL, "tok")
		if err != nil {
			return nil, err
		}
		return c.As("z"), nil
	}
	t.Cleanup(func() { releaseForge = prev })
	return &peels
}

// trainService is hanzo-cloud running and pinned to v8.5.1129.
func trainService(t *testing.T) *atomic.Int64 {
	t.Helper()
	stubLabels(t, map[string]label{"ghcr.io/hanzo-inc/cloud@" + digest: {Commit: sha1129, Repo: "https://github.com/hanzoai/cloud"}})
	stubPins(t, map[string]layer{
		"r9:" + file("hanzo-cloud"):    {Repository: "ghcr.io/hanzo-inc/cloud", Tag: "v8.5.1129", Digest: digest},
		"r9:" + overlay("hanzo-cloud"): {Absent: true},
	})
	return trainForge(t)
}

func decodeRelease(t *testing.T, resp *http.Response) Release {
	t.Helper()
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var b map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&b)
		t.Fatalf("status = %d: %v", resp.StatusCode, b)
	}
	var out Release
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// "Why isn't hanzo-cloud on v8.5.1131?" in one answer: the newest versions
// claimed, what their runs said and which jobs failed with the lines that say
// why, what was smoked, what universe pins and what the pods run — and the
// sentence that says where the newest one stopped.
func TestReleaseSaysWhereTheTrainStopped(t *testing.T) {
	peels := trainService(t)
	s := fakeService(pinnedApp("hanzo-cloud", "r9", "Synced", "Healthy",
		[]any{"ghcr.io/hanzo-inc/cloud:v8.5.1129@" + digest}, nil))

	out := decodeRelease(t, getAs(t, s, "/v1/deploy/release?service=hanzo-cloud", orgHeaders("hanzo")))
	if out.Repo != "hanzo-inc/cloud" || out.Live == nil || out.Live.Commit != sha1129 || out.Pinned == nil || out.Pinned.Tag != "v8.5.1129" {
		t.Fatalf("release = %+v", out)
	}
	tags := []string{}
	for _, v := range out.Versions {
		tags = append(tags, v.Tag)
	}
	if strings.Join(tags, ",") != "v8.5.1131,v8.5.1130,v8.5.1129" {
		t.Fatalf("versions = %v, want the three newest, newest first, and no rc", tags)
	}
	v1131, v1130, v1129 := out.Versions[0], out.Versions[1], out.Versions[2]
	if v1131.State != forge.Failed || len(v1131.Smoked) != 0 || v1131.Pinned || v1131.Live || v1131.Run != 17654 {
		t.Fatalf("v8.5.1131 = %+v", v1131)
	}
	if len(v1131.Failed) != 2 || v1131.Failed[1].Name != "image" || strings.Join(v1131.Failed[1].Failing, "|") != "##[error]go mod download: 403" {
		t.Fatalf("v8.5.1131 failed = %+v", v1131.Failed)
	}
	if len(v1130.Failed) != 2 || v1130.Failed[0].Name != "unit" || v1130.Failed[0].Failing != nil ||
		strings.Join(v1130.Failed[1].Failing, "|") != "--- FAIL: TestRecord (0.02s)|FAIL\tgithub.com/hanzoai/cloud\t9.1s" {
		t.Fatalf("v8.5.1130 failed = %+v", v1130.Failed)
	}
	if !v1129.Pinned || !v1129.Live || v1129.State != forge.Passed || len(v1129.Smoked) != 1 || v1129.Smoked[0] != digest {
		t.Fatalf("v8.5.1129 = %+v", v1129)
	}
	if out.Head.Commit != shaMain || out.Head.State != forge.Failed || len(out.Head.Failed) != 1 || out.Head.Failed[0].Name != "review" {
		t.Fatalf("head = %+v", out.Head)
	}
	if n := peels.Load(); n != 0 {
		t.Fatalf("%d tag objects peeled; the kept versions are lightweight, and the 200 annotated ones are not kept", n)
	}
	want := "v8.5.1131 (883e9cd244) was claimed and never smoked: its runs failed in test-amd64, image; v8.5.1129 is live"
	if out.Stuck != want {
		t.Fatalf("stuck = %q\nwant    %q", out.Stuck, want)
	}
}

// The service is scoped as live is, and its repository's namespace must be one
// the caller's org owns: refused before the forge is asked.
func TestReleaseIsScopedLikeLive(t *testing.T) {
	trainService(t)
	s := fakeService(pinnedApp("hanzo-cloud", "r9", "Synced", "Healthy",
		[]any{"ghcr.io/hanzo-inc/cloud:v8.5.1129@" + digest}, nil))
	for _, c := range []struct {
		path string
		org  string
		want int
	}{
		{"/v1/deploy/release?service=hanzo-cloud", "globex", http.StatusNotFound},
		{"/v1/deploy/release?service=hanzo-cloud&repo=someone/else", "hanzo", http.StatusForbidden},
		{"/v1/deploy/release", "hanzo", http.StatusBadRequest},
	} {
		resp := getAs(t, s, c.path, orgHeaders(c.org))
		_ = resp.Body.Close()
		if resp.StatusCode != c.want {
			t.Errorf("GET %s as %s = %d, want %d", c.path, c.org, resp.StatusCode, c.want)
		}
	}
}
