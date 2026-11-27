package forge

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"testing"
	"time"
)

// queue serves n queued jobs, oldest first, paged the way the forge pages them,
// and records what each read asked for.
func queue(t *testing.T, n int, jobURL func(id int) string) (*Client, *[]url.Values) {
	t.Helper()
	var asked []url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != API+"/admin/actions/jobs" || r.Header.Get("Authorization") != "token tok" || r.Header.Get("Sudo") != "" {
			http.Error(w, "not the machine reading the site queue", http.StatusBadRequest)
			return
		}
		q := r.URL.Query()
		asked = append(asked, q)
		page, _ := strconv.Atoi(q.Get("page"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		jobs := []map[string]any{}
		for id := (page-1)*limit + 1; id <= min(page*limit, n); id++ {
			jobs = append(jobs, map[string]any{"id": id, "url": jobURL(id), "labels": []string{"linux-amd64"}})
		}
		writeJSON(w, map[string]any{"jobs": jobs, "total_count": n})
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "tok")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c.Machine(), &asked
}

// Queued reads the whole queue across its pages, oldest first, and names each
// job's repository from the job's own address.
func TestQueuedReadsTheWholeQueue(t *testing.T) {
	const n = queuePage + 3
	c, asked := queue(t, n, func(id int) string {
		return fmt.Sprintf("https://git.hanzo.ai/v1/repos/hanzo-apps/repo-%d/actions/jobs/%d", id, id)
	})
	jobs, err := c.Queued(context.Background())
	if err != nil {
		t.Fatalf("Queued: %v", err)
	}
	if len(jobs) != n {
		t.Fatalf("%d jobs, want %d", len(jobs), n)
	}
	for i, j := range jobs {
		if j.ID != int64(i+1) || j.Owner != "hanzo-apps" || j.Repo != fmt.Sprintf("repo-%d", i+1) || !slices.Equal(j.Labels, []string{"linux-amd64"}) {
			t.Fatalf("job %d = %+v", i, j)
		}
	}
	if len(*asked) != 2 {
		t.Fatalf("%d reads, want 2 pages", len(*asked))
	}
	q := (*asked)[0]
	if q.Get("status") != "queued" || q.Get("sort") != "id" || q.Get("order") != "asc" {
		t.Fatalf("read asked %v, want the queued jobs oldest first", q)
	}
}

// A job whose address names no repository fails the read, rather than dropping
// out of a queue that would then look shorter than it is.
func TestQueuedRefusesAJobWithNoRepository(t *testing.T) {
	c, _ := queue(t, 1, func(id int) string { return "https://git.hanzo.ai/v1/actions/jobs/1" })
	if _, err := c.Queued(context.Background()); err == nil {
		t.Fatal("a job with no repository was read")
	}
}

// The registry is read whole, as the machine, with each runner's labels and
// when it was last heard from; a forge that does not say reads as zero.
func TestRunnersReadsTheRegistry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != API+"/admin/actions/runners" || r.Header.Get("Sudo") != "" {
			http.Error(w, "not the machine's registry read", http.StatusBadRequest)
			return
		}
		p, _ := strconv.Atoi(r.URL.Query().Get("page"))
		var rs []map[string]any
		for i := range 50 {
			if id := (p-1)*50 + i + 1; id <= 51 {
				rs = append(rs, map[string]any{"id": id, "name": "runner-" + strconv.Itoa(id), "status": "offline",
					"labels": []any{map[string]any{"name": "linux-amd64"}}})
			}
		}
		if p == 1 {
			rs[0]["status"], rs[0]["busy"] = "online", true
			rs[0]["last_online"], rs[0]["last_active"] = "2026-11-12T06:50:00Z", "2026-11-12T06:49:00Z"
		}
		writeJSON(w, map[string]any{"runners": rs, "total_count": 51})
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "tok")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	rs, err := c.As("someone").Runners(context.Background())
	if err != nil {
		t.Fatalf("Runners: %v", err)
	}
	if len(rs) != 51 {
		t.Fatalf("%d runners, want 51", len(rs))
	}
	first := rs[0]
	if !first.Online || !first.Busy || first.Labels[0] != "linux-amd64" ||
		!first.LastOnline.Equal(time.Date(2026, 11, 12, 6, 50, 0, 0, time.UTC)) || first.LastActive.IsZero() {
		t.Fatalf("runner = %+v", first)
	}
	if rs[1].Online || !rs[1].LastOnline.IsZero() {
		t.Fatalf("an offline runner the forge said nothing about = %+v", rs[1])
	}
}
