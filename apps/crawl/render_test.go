package crawl

// Drawing a page is read_page's second half, so what is pinned here is what is
// new: the render asks for a screenshot, the answer carries the picture, the DOM
// and the links, the address is judged as read_page judges it, and the fee is
// taken for a picture and for nothing less.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hanzoai/cloud/internal/planetest"
	"github.com/hanzoai/cloud/sdk"
	"github.com/hanzoai/outbound"
	luxlog "github.com/luxfi/log"
	"github.com/zap-proto/zip"
)

// png is not a real picture and does not need to be: the op carries bytes.
var png = []byte("\x89PNG\r\n\x1a\nthe first screen")

// drawn answers /crawl as the renderer does for a screenshot run, and says what
// it was asked.
func drawn(asked *map[string]any, result map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if asked != nil {
			*asked = body
		}
		res := map[string]any{"url": site + "/landed", "success": true, "markdown": "the words of the page"}
		for k, v := range result {
			res[k] = v
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "results": []map[string]any{res}})
	}
}

func screenshot() map[string]any {
	return map[string]any{
		"screenshot":   base64.StdEncoding.EncodeToString(png),
		"cleaned_html": "<main><h1>Hello</h1></main>",
		"links": map[string]any{
			"internal": []map[string]any{{"href": site + "/a", "text": "A"}, {"href": site + "/a", "text": "again"}},
			"external": []map[string]any{{"href": "https://example.org/", "text": "Org"}, {"href": "", "text": "nowhere"}},
		},
		"metadata": map[string]any{"title": "A page", "description": "what it is"},
	}
}

// reaching stands in for the guarded client: every GET answers 200 from the address it asked for.
type reaching struct{}

func (reaching) RoundTrip(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("")), Request: r, Header: http.Header{}}, nil
}

func guarded(t *testing.T, rt http.RoundTripper) {
	t.Helper()
	was := client
	client = &http.Client{Transport: rt}
	t.Cleanup(func() { client = was })
}

func TestRenderAsksForAScreenshotAndAnswersTheWholePage(t *testing.T) {
	var asked map[string]any
	browserAt(t, drawn(&asked, screenshot()))
	guarded(t, reaching{})

	got, err := Render(context.Background(), site)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}

	cfg, _ := asked["crawler_config"].(map[string]any)
	if cfg["screenshot"] != true || cfg["force_viewport_screenshot"] != true {
		t.Errorf("the render did not ask for the first screen as a screenshot: crawler_config = %v", cfg)
	}
	if !bytes.Equal(got.Screenshot, png) {
		t.Errorf("screenshot = %q, want the bytes the renderer sent", got.Screenshot)
	}
	if got.DOM != "<main><h1>Hello</h1></main>" || got.Cut {
		t.Errorf("DOM = %q (cut %v)", got.DOM, got.Cut)
	}
	if got.URL != site+"/landed" || got.Title != "A page" || got.Markdown != "the words of the page" {
		t.Errorf("page = %q %q %q", got.URL, got.Title, got.Markdown)
	}
	if got.Metadata["renderer"] != "browser" || got.Metadata["description"] != "what it is" {
		t.Errorf("metadata = %v", got.Metadata)
	}
	// Internal first, each address once, an anchor with no address dropped.
	want := []Link{{site + "/a", "A"}, {"https://example.org/", "Org"}}
	if len(got.Links) != len(want) || got.Links[0] != want[0] || got.Links[1] != want[1] {
		t.Errorf("links = %v, want %v", got.Links, want)
	}
}

func TestRenderCutsTheDOMAtAValidBoundary(t *testing.T) {
	// A cut inside a multibyte rune must not leave half of it.
	dom := strings.Repeat("é", maxDOM)
	res := screenshot()
	res["cleaned_html"] = dom
	browserAt(t, drawn(nil, res))
	guarded(t, reaching{})

	got, err := Render(context.Background(), site)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !got.Cut || len(got.DOM) > maxDOM || len(got.DOM) < maxDOM-1 {
		t.Fatalf("DOM is %d bytes, cut %v; want it cut at %d", len(got.DOM), got.Cut, maxDOM)
	}
	if strings.Contains(got.DOM, "�") || got.DOM != strings.Repeat("é", len(got.DOM)/2) {
		t.Error("the cut left a broken rune")
	}
}

func TestRenderKeepsAtMostMaxLinks(t *testing.T) {
	var in []map[string]any
	for i := 0; i < maxLinks+20; i++ {
		in = append(in, map[string]any{"href": site + "/" + strings.Repeat("x", i+1), "text": "t"})
	}
	res := screenshot()
	res["links"] = map[string]any{"internal": in}
	browserAt(t, drawn(nil, res))
	guarded(t, reaching{})

	got, err := Render(context.Background(), site)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if len(got.Links) != maxLinks {
		t.Errorf("links = %d, want %d", len(got.Links), maxLinks)
	}
}

func TestRenderTakesLinksInAShapeItDoesNotKnowAsNone(t *testing.T) {
	res := screenshot()
	res["links"] = []string{"not", "the", "shape"}
	browserAt(t, drawn(nil, res))
	guarded(t, reaching{})

	got, err := Render(context.Background(), site)
	if err != nil || len(got.Links) != 0 || !bytes.Equal(got.Screenshot, png) {
		t.Fatalf("a courtesy of the render failed it: %v, %d links", err, len(got.Links))
	}
}

func TestRenderWithoutAPictureIsAnError(t *testing.T) {
	res := screenshot()
	delete(res, "screenshot")
	browserAt(t, drawn(nil, res))
	guarded(t, reaching{})

	if _, err := Render(context.Background(), site); err == nil || !strings.Contains(err.Error(), "no screenshot") {
		t.Fatalf("a render that drew nothing returned %v, want the error", err)
	}
}

// ── the address is judged as read_page judges it ────────────────────────────

func TestRenderRefusesAnInternalOrForeignTarget(t *testing.T) {
	browserAt(t, func(http.ResponseWriter, *http.Request) {
		t.Error("the browser was asked to draw an address that is not public")
	})
	for _, raw := range []string{
		"http://127.0.0.1/",
		"http://10.0.0.1/",
		"http://169.254.169.254/latest/meta-data/",
		"http://[::1]/",
		"http://localhost/",
		"file:///etc/passwd",
		"gopher://x/",
	} {
		if _, err := Render(context.Background(), raw); !errors.Is(err, outbound.ErrRefused) {
			t.Errorf("Render(%q) = %v, want the refusal", raw, err)
		}
	}
}

// A public page whose redirect names the metadata endpoint is refused by the
// guarded GET that runs first, because the browser would follow the same redirect.
func TestRenderDoesNotDrawARefusedRedirect(t *testing.T) {
	browserAt(t, func(http.ResponseWriter, *http.Request) {
		t.Error("the browser was asked to draw a URL whose redirect was refused")
	})
	guarded(t, refusing{})

	if _, err := Render(context.Background(), site+"/redirect-to?url=http://169.254.169.254/"); !errors.Is(err, outbound.ErrRefused) {
		t.Fatalf("err = %v, want the refusal", err)
	}
}

// A page a plain GET cannot read is what the browser is for.
func TestRenderDrawsWhatAPlainGETCannotRead(t *testing.T) {
	browserAt(t, drawn(nil, screenshot()))
	guarded(t, roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection reset") }))

	got, err := Render(context.Background(), site)
	if err != nil || !bytes.Equal(got.Screenshot, png) {
		t.Fatalf("Render = %v, %v; want the browser to draw it", got, err)
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// ── the fee ─────────────────────────────────────────────────────────────────

// drawAs draws one page as org through a real request, so the payer is resolved
// the way production resolves it.
func drawAs(t *testing.T, org string) (*Rendered, error) {
	t.Helper()
	app := zip.New(zip.Config{Logger: luxlog.New("test"), DisableStartupMessage: true})
	app.Use(cloud.Bridge())
	var (
		out *Rendered
		err error
	)
	app.Raw(http.MethodPost, "/probe", func(c *zip.Ctx) error {
		out, err = Render(c.Context(), site)
		return c.JSON(http.StatusOK, map[string]string{"ok": "1"})
	})
	req := httptest.NewRequest(http.MethodPost, "/probe", nil)
	req.Header.Set("X-Org-Id", org)
	req.Header.Set("X-User-Id", "u_"+org)
	if _, e := app.Test(req, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true}); e != nil {
		t.Fatalf("probe: %v", e)
	}
	return out, err
}

func TestRenderDebitsTheCallerOnceForAPicture(t *testing.T) {
	l := planetest.Money(t, 100000)
	meterOn(t, l)
	var hits atomic.Int32
	browserAt(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		drawn(nil, screenshot())(w, r)
	})
	guarded(t, reaching{})

	if _, err := drawAs(t, "acme"); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if hits.Load() != 1 || !planetest.Wait(func() bool { return l.Count() == 1 }) {
		t.Fatalf("browser called %d times, debits = %d; want 1 and 1", hits.Load(), l.Count())
	}
	if org := l.Org(); org != "acme" {
		t.Errorf("debited org %q, want the caller", org)
	}
	if _, cents, model, _ := l.Charged(); cents != defaultFeeCents || model != kind {
		t.Errorf("debit = %dc %q, want %dc %q", cents, model, defaultFeeCents, kind)
	}
}

func TestRenderThatDrewNothingIsNotBilled(t *testing.T) {
	l := planetest.Money(t, 100000)
	meterOn(t, l)
	res := screenshot()
	delete(res, "screenshot")
	browserAt(t, drawn(nil, res))
	guarded(t, reaching{})

	if _, err := drawAs(t, "acme"); err == nil {
		t.Fatal("a render with no picture succeeded")
	}
	if n := l.Count(); n != 0 {
		t.Fatalf("debits = %d for a render that gave nothing, want 0", n)
	}
}

func TestRenderAnOrgThatCannotPayIsRefusedBeforeTheBrowser(t *testing.T) {
	l := planetest.Money(t, 0)
	meterOn(t, l)
	browserAt(t, func(http.ResponseWriter, *http.Request) {
		t.Error("the browser was asked to draw for an org that cannot pay")
	})
	guarded(t, reaching{})

	if _, err := drawAs(t, "acme"); err == nil {
		t.Fatal("an org with no balance was served a render")
	}
}

// ── the op ──────────────────────────────────────────────────────────────────

func TestRenderPageIsClosedToAnAnonymousCaller(t *testing.T) {
	app := mount(t)
	rq := httptest.NewRequest(http.MethodPost, RenderPath, strings.NewReader(`{"url":"https://example.com"}`))
	rq.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(rq, zip.TestConfig{Timeout: 30 * time.Second, FailOnTimeout: true})
	if err != nil {
		t.Fatalf("POST %s: %v", RenderPath, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("an anonymous render = %d, want 401: this op dials a caller-chosen address from inside the cluster", resp.StatusCode)
	}
}

func TestRenderPageAnswersTheDrawnPage(t *testing.T) {
	browserAt(t, drawn(nil, screenshot()))
	guarded(t, reaching{})

	code, out := post(t, mount(t), RenderPath, `{"url":"`+site+`"}`)
	if code != http.StatusOK {
		t.Fatalf("POST %s = %d %s", RenderPath, code, out)
	}
	var got struct {
		Success bool `json:"success"`
		Data    struct {
			URL        string `json:"url"`
			Title      string `json:"title"`
			Screenshot string `json:"screenshot"`
			DOM        string `json:"dom"`
			Markdown   string `json:"markdown"`
			Links      []struct{ Href, Text string }
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("answer is not JSON: %v: %s", err, out)
	}
	if !got.Success || got.Data.Screenshot != base64.StdEncoding.EncodeToString(png) {
		t.Fatalf("answer = %.200s", out)
	}
	if got.Data.URL != site+"/landed" || got.Data.Title != "A page" || got.Data.DOM == "" || got.Data.Markdown == "" || len(got.Data.Links) != 2 {
		t.Errorf("data = %+v", got.Data)
	}
}

func TestRenderPageSaysWhyAPageCouldNotBeDrawn(t *testing.T) {
	browserAt(t, func(http.ResponseWriter, *http.Request) { t.Error("the browser was asked for an internal address") })

	code, out := post(t, mount(t), RenderPath, `{"url":"http://169.254.169.254/latest/"}`)
	if code != http.StatusOK || !strings.HasPrefix(out, `{"success":false,"error":"`) {
		t.Fatalf("a refused address = %d %s, want 200 with success false and the reason", code, out)
	}
	code, out = post(t, mount(t), RenderPath, `{"url":""}`)
	if code != http.StatusBadRequest || out != `{"success":false,"error":"missing url"}` {
		t.Fatalf("no url = %d %s, want the 400 domain refusal", code, out)
	}
}
