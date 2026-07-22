package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/clients/sites"
	"github.com/zap-proto/zip"
)

// responsiveGuidance is the strict system instruction that makes generated sites
// mobile-responsive AND self-contained (CSP-safe) BY DEFAULT. It constrains the
// model to emit ONLY a JSON manifest — no prose, no markdown fences — so the
// output parses deterministically. The responsiveness is additionally GUARANTEED
// post-generation by ensureViewport (the model can't forget the viewport tag),
// but the guidance produces genuinely fluid layouts, not just the meta tag.
const responsiveGuidance = `You are a senior front-end engineer. Produce a COMPLETE, self-contained, ` +
	`mobile-responsive static website for the brief below.

Output rules (STRICT):
- Respond with ONLY a single JSON object. No prose, no explanation, no markdown code fences.
- Shape: {"name":"<short site name>","files":[{"path":"index.html","content":"<full file contents>"}, ...]}
- Include index.html at the ROOT (path exactly "index.html"). Additional files (css/js/pages) are optional and must use relative paths (no leading "/", no "..").

Site requirements:
- Every HTML file MUST have <meta name="viewport" content="width=device-width, initial-scale=1"> inside <head>.
- Mobile-first, fluid layout: use percentage/max-width widths, flexbox and/or CSS grid, and @media breakpoints. Images use img{max-width:100%;height:auto}.
- Use a system-ui font stack (font-family: system-ui, -apple-system, Segoe UI, Roboto, sans-serif) and readable spacing/line-height.
- Fully self-contained: inline all CSS and JS. NO external network requests — no CDNs, no remote fonts, no remote images, no <script src> or <link href> to other origins. It must be CSP-safe.
- It MUST render correctly from 390px wide (mobile) through desktop.`

// genFile is one file in a site manifest: a relative path and its full contents.
// It is the shape of BOTH the model's manifest entries AND the raw deploy_site
// JSON body, so one validator (siteFromFiles) serves both paths.
type genFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// genManifest is the JSON object the model must emit for POST /v1/sites.
type genManifest struct {
	Name  string    `json:"name"`
	Files []genFile `json:"files"`
}

// maxBriefBytes caps the natural-language brief accepted by POST /v1/sites.
const maxBriefBytes = 8 << 10 // 8 KiB

// generateSite turns a natural-language brief into a validated, responsive
// *site via one chat completion. It composes the strict guidance with the brief,
// tolerantly parses the model's JSON manifest, and runs it through the SAME
// validation + responsiveness guarantee as the raw deploy_site path (siteFromFiles).
// A nil ai is an honest error (the caller answers 503); a parse/validation failure
// is returned so the caller answers 400.
func generateSite(ctx context.Context, ai cloud.AIClient, model, brief string) (name string, st *site, err error) {
	if ai == nil {
		return "", nil, errors.New("inference is not configured")
	}
	resp, err := ai.ChatCompletion(ctx, &cloud.ChatRequest{
		Model:  model,
		Prompt: responsiveGuidance + "\n\nBrief: " + brief,
	})
	if err != nil {
		return "", nil, fmt.Errorf("inference: %w", err)
	}
	if resp == nil || strings.TrimSpace(resp.Content) == "" {
		return "", nil, errors.New("model returned an empty response")
	}
	return parseManifest(resp.Content)
}

// parseManifest extracts the first balanced top-level JSON object from a model
// response (tolerating markdown fences and surrounding prose), unmarshals it, and
// builds a validated responsive *site from its files.
func parseManifest(raw string) (string, *site, error) {
	obj, err := extractJSONObject(raw)
	if err != nil {
		return "", nil, err
	}
	var m genManifest
	if err := json.Unmarshal([]byte(obj), &m); err != nil {
		return "", nil, fmt.Errorf("parse site manifest: %w", err)
	}
	st, err := siteFromFiles(m.Files)
	if err != nil {
		return "", nil, err
	}
	return strings.TrimSpace(m.Name), st, nil
}

// siteFromFiles builds a validated, responsive *site from a file list — the ONE
// place the deploy guards are applied to a file manifest (the tar path applies
// them in walkTarGz). It: rejects unsafe paths via safeRel (traversal/absolute);
// enforces maxFiles / maxFileBytes / maxTotalBytes; requires index.html at the
// root; and GUARANTEES responsiveness by passing every *.html file through
// ensureViewport so a mobile viewport meta tag is always present, even if the
// model or caller omitted it.
func siteFromFiles(files []genFile) (*site, error) {
	if len(files) == 0 {
		return nil, errors.New("site has no files")
	}
	if len(files) > maxFiles {
		return nil, fmt.Errorf("site exceeds %d files", maxFiles)
	}
	st := &site{files: make(map[string][]byte, len(files))}
	for _, f := range files {
		clean, ok := safeRel(f.Path)
		if !ok {
			return nil, fmt.Errorf("unsafe path in site: %q", f.Path)
		}
		if clean == "" {
			continue
		}
		content := f.Content
		if isHTML(clean) {
			content = ensureViewport(content) // the responsive guarantee.
		}
		data := []byte(content)
		if int64(len(data)) > maxFileBytes {
			return nil, fmt.Errorf("file %q exceeds %d bytes", clean, maxFileBytes)
		}
		st.files[clean] = data
	}
	var total int64
	for _, d := range st.files {
		total += int64(len(d))
	}
	if total > maxTotalBytes {
		return nil, fmt.Errorf("site exceeds %d bytes total", maxTotalBytes)
	}
	st.bytes = total
	if len(st.files) == 0 {
		return nil, errors.New("site has no files")
	}
	if _, ok := st.files["index.html"]; !ok {
		return nil, errors.New("site missing index.html at root")
	}
	return st, nil
}

// viewportMeta is the canonical mobile viewport tag injected when absent.
const viewportMeta = `<meta name="viewport" content="width=device-width, initial-scale=1">`

// ensureViewport is the responsive guarantee for one HTML document: if it already
// declares a name="viewport" meta it is returned unchanged; otherwise the
// canonical tag is injected immediately after the opening <head> tag (or
// prepended when there is no head, which the browser still parses into <head>).
// Detection is case-insensitive and accepts single- or double-quoted attributes.
func ensureViewport(html string) string {
	lower := strings.ToLower(html)
	if strings.Contains(lower, `name="viewport"`) || strings.Contains(lower, `name='viewport'`) {
		return html
	}
	if i := headOpenEnd(lower); i >= 0 {
		return html[:i] + "\n" + viewportMeta + html[i:]
	}
	return viewportMeta + "\n" + html
}

// headOpenEnd returns the index just past the first "<head...>" opening tag in
// lower (which the caller has already lowercased), or -1 when there is none.
func headOpenEnd(lower string) int {
	i := strings.Index(lower, "<head")
	if i < 0 {
		return -1
	}
	end := strings.IndexByte(lower[i:], '>')
	if end < 0 {
		return -1
	}
	return i + end + 1
}

// isHTML reports whether a (already-clean, relative) path is an HTML document.
func isHTML(p string) bool {
	p = strings.ToLower(p)
	return strings.HasSuffix(p, ".html") || strings.HasSuffix(p, ".htm")
}

// extractJSONObject pulls the first balanced, top-level JSON object out of a model
// response. It tolerates ```json ... ``` fences and leading/trailing prose by
// scanning for the first '{' and matching braces while respecting string literals
// and escapes — so a '}' inside a JSON string never prematurely closes the object.
func extractJSONObject(s string) (string, error) {
	s = stripFences(s)
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", errors.New("no JSON object in model response")
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", errors.New("unbalanced JSON object in model response")
}

// stripFences removes a leading ```lang fence and its trailing ``` if the whole
// response is wrapped in a markdown code block, so the extractor sees raw text.
func stripFences(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return s
	}
	if nl := strings.IndexByte(t, '\n'); nl >= 0 {
		t = t[nl+1:]
	}
	if i := strings.LastIndex(t, "```"); i >= 0 {
		t = t[:i]
	}
	return t
}

// ---- request/response shapes ----

type buildSiteReq struct {
	Brief string `json:"brief"`
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Model string `json:"model"`
}

type deploySiteReq struct {
	Slug  string    `json:"slug"`
	Name  string    `json:"name"`
	Files []genFile `json:"files"`
}

type siteView struct {
	Slug      string `json:"slug"`
	URL       string `json:"url"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updatedAt"`
}

// siteResponse is the published-site response shared by buildSite and
// deploySiteFiles: the pretty URL, the resolved slug/name, the deployment id, the
// sorted file list, and the live status.
func siteResponse(p Project, d Deployment, st *site) map[string]any {
	paths := make([]string, 0, len(st.files))
	for k := range st.files {
		paths = append(paths, k)
	}
	sort.Strings(paths)
	return map[string]any{
		"url":          d.LiveURL,
		"slug":         p.Slug,
		"name":         p.Name,
		"deploymentId": d.ID,
		"files":        paths,
		"status":       d.Status,
	}
}

// ---- handlers ----

// buildSite generates a responsive static site from a brief and deploys it live.
// Order: org gate → config checks → validate brief → HOSTING GATE (before any
// inference/upload work) → generate → resolve slug → ensure project → publish →
// meter once → notify. A denied gate returns 402/503 with NOTHING generated or
// uploaded; a generation/parse failure is 400; a failed upload is never billed.
func buildSite(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	if !s.State.blob.configured() {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage not configured (set S3_ADMIN_*)")
	}
	if s.State.ai == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "inference is not configured on this deployment")
	}
	var body buildSiteReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	brief := strings.TrimSpace(body.Brief)
	if brief == "" {
		return zip.ErrBadRequest("brief is required")
	}
	if len(brief) > maxBriefBytes {
		return zip.ErrBadRequest("brief too large")
	}

	fee, gErr := gateHosting(s, c)
	if gErr != nil {
		return cloud.DenyResource(c, gErr)
	}

	name, st, err := generateSite(c.Context(), s.State.ai, strings.TrimSpace(body.Model), brief)
	if err != nil {
		return zip.ErrBadRequest("site generation failed: " + err.Error())
	}
	if name == "" {
		name = strings.TrimSpace(body.Name)
	}
	if name == "" {
		name = "Site"
	}

	slug, err := resolveSlug(body.Slug, name)
	if err != nil {
		return err
	}
	p, err := ensureProject(s, c.Context(), org, slug, name)
	if err != nil {
		return err
	}
	d, err := publishSite(s, c.Context(), org, p, st, "generated")
	if err != nil {
		if d.Status == "error" {
			return zip.Errorf(http.StatusBadGateway, "%v", err)
		}
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	meterDeploy(s, c, fee)
	notifyDeploy(c.Context(), org, p.Slug, d)
	return c.JSON(http.StatusOK, siteResponse(p, d, st))
}

// deploySiteFiles is the raw deploy_site capability: it deploys a caller-supplied
// file manifest (the same shape the model emits). Files run through the SAME
// validation + viewport-injection + guards as generation (siteFromFiles), so a
// hand-built site is exactly as safe and responsive as a generated one.
func deploySiteFiles(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	if !s.State.blob.configured() {
		return zip.Errorf(http.StatusServiceUnavailable, "object storage not configured (set S3_ADMIN_*)")
	}
	var body deploySiteReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	if len(body.Files) == 0 {
		return zip.ErrBadRequest("files is required")
	}
	if len(body.Files) > maxFiles {
		return zip.ErrBadRequest("too many files")
	}
	st, err := siteFromFiles(body.Files)
	if err != nil {
		return zip.ErrBadRequest("invalid site: " + err.Error())
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Site"
	}

	fee, gErr := gateHosting(s, c)
	if gErr != nil {
		return cloud.DenyResource(c, gErr)
	}

	slug, err := resolveSlug(body.Slug, name)
	if err != nil {
		return err
	}
	p, err := ensureProject(s, c.Context(), org, slug, name)
	if err != nil {
		return err
	}
	d, err := publishSite(s, c.Context(), org, p, st, "deploy")
	if err != nil {
		if d.Status == "error" {
			return zip.Errorf(http.StatusBadGateway, "%v", err)
		}
		return zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	meterDeploy(s, c, fee)
	notifyDeploy(c.Context(), org, p.Slug, d)
	return c.JSON(http.StatusOK, siteResponse(p, d, st))
}

// listSites lists the org's deployed (live) sites at their pretty URLs.
func listSites(s *cloud.Service[state], c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	rows, err := s.State.store.ListProjects(c.Context(), org)
	if err != nil {
		return zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make([]siteView, 0, len(rows))
	for _, p := range rows {
		if p.Status != "live" {
			continue
		}
		out = append(out, siteView{
			Slug: p.Slug, URL: siteURL(s, org, p.Slug), Name: p.Name,
			Status: p.Status, UpdatedAt: p.UpdatedAt,
		})
	}
	return c.JSON(http.StatusOK, out)
}

// ---- slug + project helpers ----

// resolveSlug turns a caller-provided slug (or, when empty, a name) into a valid,
// non-reserved site slug. An explicit slug MUST pass slugRE and MUST NOT be a
// reserved label. When no usable slug can be derived from the name it mints
// "site-<token>", so a deploy never fails purely for lack of a good name.
func resolveSlug(raw, name string) (string, error) {
	slug := strings.ToLower(strings.TrimSpace(raw))
	if slug != "" {
		if !slugRE.MatchString(slug) {
			return "", zip.ErrBadRequest("slug must match ^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$")
		}
		if sites.IsReserved(slug) {
			return "", zip.ErrBadRequest("slug is a reserved subdomain and cannot be used")
		}
		return slug, nil
	}
	if d := slugify(name); d != "" && slugRE.MatchString(d) && !sites.IsReserved(d) {
		return d, nil
	}
	return mintSlug()
}

// mintSlug returns a fresh, always-valid, never-reserved slug of the form
// "site-<random>". genID gives "site_<22 url-safe chars>"; slugify lowercases it
// and turns '_' into '-', so the result always begins "site-" and matches slugRE.
func mintSlug() (string, error) {
	tok, err := genID("site")
	if err != nil {
		return "", zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	return slugify(tok), nil
}

// ensureProject returns the org's project for slug, creating it (framework
// "static", status "draft") when absent. A create that races another create for
// the same (org,slug) maps errConflict back to a re-Get, so concurrent deploys to
// a new slug converge on the one project. org and slug come ONLY from the
// validated tenant + resolver, never from the request body, so this can only ever
// touch the caller's own namespace.
func ensureProject(s *cloud.Service[state], ctx context.Context, org, slug, name string) (Project, error) {
	p, err := s.State.store.GetProject(ctx, org, slug)
	if err == nil {
		return p, nil
	}
	if !errors.Is(err, errNotFound) {
		return Project{}, zip.Errorf(http.StatusInternalServerError, "get project: %v", err)
	}
	now := time.Now().Unix()
	id, err := genID("proj")
	if err != nil {
		return Project{}, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	np := Project{
		ID: id, Org: org, Slug: slug, Name: name, Framework: "static",
		Status: "draft", Bucket: s.State.blob.bucket, CreatedAt: now, UpdatedAt: now,
	}
	// Same wired-by-default settings as POST /v1/projects — analytics ON and the
	// Base data-space namespace — so the /v1/sites create path is not a second
	// place defaults are decided. A generated site has no opt-out knob (nil ⇒ ON).
	setProjectDefaults(&np, nil)
	if err := s.State.store.CreateProject(ctx, np); err != nil {
		if errors.Is(err, errConflict) {
			return s.State.store.GetProject(ctx, org, slug)
		}
		return Project{}, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	provisionSpace(s, ctx, &np)
	return np, nil
}
