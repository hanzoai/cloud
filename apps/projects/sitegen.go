package projects

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/principal"
	"github.com/hanzoai/cloud/apps/sites"
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

// projectsFile is one file in a site manifest: a relative path and its full contents.
// It is the shape of BOTH the model's manifest entries AND the raw deploy_site
// JSON body, so one validator (siteFromFiles) serves both paths.
type projectsFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// genManifest is the JSON object the model must emit for POST /v1/sites.
type genManifest struct {
	Name  string         `json:"name"`
	Files []projectsFile `json:"files"`
}

// maxBriefBytes caps the natural-language brief accepted by POST /v1/sites.
const maxBriefBytes = 8 << 10 // 8 KiB

// generateSite turns a natural-language brief into a validated, responsive
// *site via one chat completion. It composes the strict guidance with the brief,
// tolerantly parses the model's JSON manifest, and runs it through the SAME
// validation + responsiveness guarantee as the raw deploy_site path (siteFromFiles).
// A nil ai is an honest error (the caller answers 503); a parse/validation failure
// is returned so the caller answers 400.
//
// org and payer are REQUIRED, and they are the whole reason this signature has them.
// cloud's inference decorator gates and debits on the request's billing org, and its
// one exempt path is the empty string: `if org == "" { return nil }` in the gate and a
// log line instead of a debit in the record. This call named neither, so POST /v1/sites
// charged its flat hosting fee and gave the model tokens away — on the SAME request
// that had already resolved the payer for that fee. The tokens are the expensive half.
func generateSite(ctx context.Context, ai cloud.AIClient, model, brief, org, payer string) (name string, st *site, err error) {
	if ai == nil {
		return "", nil, errors.New("inference is not configured")
	}
	// Refuse to spend on behalf of nobody, BEFORE the call. billedOrg falls back to Org
	// when BillingOrg is empty, so one of the two is enough; neither is the exempt case,
	// and serving it means giving the tokens away. buildSite already 403s without an org,
	// so this cannot fire for the live route — it is here so the exemption stays
	// unreachable through this function for whoever calls it next.
	if strings.TrimSpace(org) == "" && strings.TrimSpace(payer) == "" {
		return "", nil, errors.New("site generation needs a billing org: an unattributed completion is exempt from the meter")
	}
	resp, err := ai.ChatCompletion(ctx, &cloud.ChatRequest{
		Model:  model,
		Prompt: responsiveGuidance + "\n\nBrief: " + brief,
		// Org is the EFFECTIVE org (data scope: BYO keys, RAG); BillingOrg is the HOME
		// org that PAYS — the same address gateHosting reserved the hosting fee against,
		// so one request cannot bill two different ledgers.
		Org:        org,
		BillingOrg: payer,
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
func siteFromFiles(files []projectsFile) (*site, error) {
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

type projectsBuildSite struct {
	Brief string `json:"brief"`
	Slug  string `json:"slug"`
	Name  string `json:"name"`
	Model string `json:"model"`
}

type projectsDeploySite struct {
	Slug  string         `json:"slug"`
	Name  string         `json:"name"`
	Files []projectsFile `json:"files"`
}

// projectsSite is one live site in the org's list: the pretty URL it serves at,
// and the project state behind it.
type projectsSite struct {
	Slug      string `json:"slug"`
	URL       string `json:"url"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	UpdatedAt int64  `json:"updatedAt"`
}

// projectsSites is the org's live sites. A defined slice type, so the list has a
// NAME in the document and a generated SDK returns a list of the same Site.
type projectsSites []projectsSite

// projectsSiteDeploy is what a published site answers with: where it is serving,
// which project and deployment it became, and exactly which files went up.
//
// FIELD ORDER IS ALPHABETICAL BY JSON TAG, once and deliberately: this answer
// used to be a map[string]any, which encoding/json serialises in sorted key
// order, so declaring the fields in reading order would have reordered the
// object. Object member order carries no meaning, but it costs nothing to keep
// and it makes "the typed op answers what the handler answered" checkable byte
// for byte rather than merely equal as JSON.
type projectsSiteDeploy struct {
	// DeploymentID is the deployment this publish recorded, for the history.
	DeploymentID string `json:"deploymentId"`
	// Files are the site-relative paths that were uploaded, sorted.
	Files []string `json:"files"`
	// Name is the project's display name.
	Name string `json:"name"`
	// Slug is the project the site was published into, created on the fly when
	// the slug was free.
	Slug string `json:"slug"`
	// Status is the deployment status, "live" on success.
	Status string `json:"status"`
	// URL is the canonical live URL, https://<slug>.<apex> — empty when the
	// subdomain belongs to another tenant and this site has none.
	URL string `json:"url"`
}

// siteResponse is the published-site answer shared by BuildSite and DeploySite,
// so the generated and the hand-supplied path describe a publish identically.
func siteResponse(p Project, d Deployment, st *site) *projectsSiteDeploy {
	// siteFromFiles/walkTarGz both require index.html at the root, so the map is
	// never empty and the JSON array is never null.
	paths := slices.Sorted(maps.Keys(st.files))
	return &projectsSiteDeploy{
		URL: d.LiveURL, Slug: p.Slug, Name: p.Name,
		DeploymentID: d.ID, Files: paths, Status: d.Status,
	}
}

// ---- handlers ----

// BuildSite generates a self-contained, mobile-responsive static site from a
// natural-language brief and deploys it live in one call.
//
// One inference call turns `brief` (capped at 8 KiB) into a file manifest, which
// then runs through the SAME validation, guards and viewport guarantee as a
// hand-supplied manifest: index.html required at the root, absolute and
// traversal paths rejected, per-file and total size capped, and a mobile
// viewport meta tag injected into every HTML document that lacks one. The
// generated site is fully inline — no CDNs, no remote fonts or images — so it is
// CSP-safe. `slug` and `name` are optional: the model's own title is preferred,
// and a slug is derived or minted when none is given.
//
// It writes into the SAME org-scoped store as /v1/projects — it ensures a
// project (framework `static`) for the resolved slug and records a deployment —
// so this is a second door onto one publish pipeline, not a second copy of
// project state. Ordering is the billing contract: the hosting gate runs BEFORE
// any inference or upload, so a denied gate generates and uploads NOTHING, and
// the debit lands once, only after the site is actually live. The tokens are
// billed to the same ledger the hosting fee was reserved against.
//
// Answers 503 when object storage or inference is unconfigured, and 400 when the
// model's manifest cannot be parsed or fails the guards.
//
// Scope: a validated principal is required (403 without one) and the site is
// published into THAT principal's org.
func (o ops) buildSite(ctx context.Context, in *projectsBuildSite) (*projectsSiteDeploy, error) {
	c, org, err := o.callerOf(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	if !s.State.blob.configured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage not configured (set S3_ADMIN_*)")
	}
	if s.State.ai == nil {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "inference is not configured on this deployment")
	}
	if err := requireBody(c); err != nil {
		return nil, err
	}
	brief := strings.TrimSpace(in.Brief)
	if brief == "" {
		return nil, zip.ErrBadRequest("brief is required")
	}
	if len(brief) > maxBriefBytes {
		return nil, zip.ErrBadRequest("brief too large")
	}

	fee, gErr := gateHosting(s, c)
	if gErr != nil {
		return nil, cloud.Denied(gErr)
	}

	// principal.Ledger(c) is the payer gateHosting just reserved the fee against — the
	// tokens must land on the same ledger, resolved the same way, or the request bills
	// its two halves to two different accounts.
	name, st, err := generateSite(ctx, s.State.ai, strings.TrimSpace(in.Model), brief, org, principal.Ledger(c))
	if err != nil {
		return nil, zip.ErrBadRequest("site generation failed: " + err.Error())
	}
	if name == "" {
		name = strings.TrimSpace(in.Name)
	}
	if name == "" {
		name = "Site"
	}

	slug, err := resolveSlug(in.Slug, name)
	if err != nil {
		return nil, err
	}
	p, err := ensureProject(s, ctx, org, slug, name)
	if err != nil {
		return nil, err
	}
	d, err := publishSite(s, ctx, org, p, st, "generated")
	if err != nil {
		if d.Status == "error" {
			return nil, zip.Errorf(http.StatusBadGateway, "%v", err)
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	meterDeploy(s, c, fee)
	notifyDeploy(ctx, org, p.Slug, d)
	return siteResponse(p, d, st), nil
}

// DeploySite deploys a caller-supplied file manifest — the deploy_site
// capability an agent calls — and answers with where it went live.
//
// `files` is a list of {path, content} pairs, the same shape the brief build
// emits, and it runs through the SAME guards: index.html required at the root,
// absolute and traversal paths rejected, per-file and total size capped, and a
// mobile viewport meta tag injected into every HTML document that lacks one — so
// a hand-built site is exactly as safe and as responsive as a generated one.
// `slug` and `name` are optional; a slug is derived from the name or minted.
//
// It writes into the SAME org-scoped store as /v1/projects, ensuring a project
// (framework `static`) for the resolved slug and recording a deployment. The
// hosting gate runs before the upload and the debit lands once, after the site
// is live — a failed upload is never billed. Answers 503 when object storage is
// unconfigured.
//
// Scope: a validated principal is required (403 without one) and the site is
// published into THAT principal's org.
func (o ops) deploySite(ctx context.Context, in *projectsDeploySite) (*projectsSiteDeploy, error) {
	c, org, err := o.callerOf(ctx)
	if err != nil {
		return nil, err
	}
	s := o.s
	if !s.State.blob.configured() {
		return nil, zip.Errorf(http.StatusServiceUnavailable, "object storage not configured (set S3_ADMIN_*)")
	}
	if err := requireBody(c); err != nil {
		return nil, err
	}
	if len(in.Files) == 0 {
		return nil, zip.ErrBadRequest("files is required")
	}
	if len(in.Files) > maxFiles {
		return nil, zip.ErrBadRequest("too many files")
	}
	st, err := siteFromFiles(in.Files)
	if err != nil {
		return nil, zip.ErrBadRequest("invalid site: " + err.Error())
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = "Site"
	}

	fee, gErr := gateHosting(s, c)
	if gErr != nil {
		return nil, cloud.Denied(gErr)
	}

	slug, err := resolveSlug(in.Slug, name)
	if err != nil {
		return nil, err
	}
	p, err := ensureProject(s, ctx, org, slug, name)
	if err != nil {
		return nil, err
	}
	d, err := publishSite(s, ctx, org, p, st, "deploy")
	if err != nil {
		if d.Status == "error" {
			return nil, zip.Errorf(http.StatusBadGateway, "%v", err)
		}
		return nil, zip.Errorf(http.StatusInternalServerError, "%v", err)
	}
	meterDeploy(s, c, fee)
	notifyDeploy(ctx, org, p.Slug, d)
	return siteResponse(p, d, st), nil
}

// ListSites returns the org's deployed sites at the pretty URLs they serve at.
//
// It reads the SAME org-scoped store as /v1/projects and keeps only the projects
// that are actually `live`, so a draft or a failed build is not advertised as a
// site.
//
// Scope: a validated principal is required (403 without one) and the list is
// keyed by that principal's org.
func (o ops) listSites(ctx context.Context, _ *void) (*projectsSites, error) {
	_, org, err := o.callerOf(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := o.s.State.store.ListProjects(ctx, org)
	if err != nil {
		return nil, zip.Errorf(http.StatusInternalServerError, "list: %v", err)
	}
	out := make(projectsSites, 0, len(rows))
	for _, p := range rows {
		if p.Status != "live" {
			continue
		}
		out = append(out, projectsSite{
			Slug: p.Slug, URL: siteURL(o.s, org, p.Slug), Name: p.Name,
			Status: p.Status, UpdatedAt: p.UpdatedAt,
		})
	}
	return &out, nil
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
	// Same wired-by-default settings as POST /v1/projects — analytics ON, the Base
	// data-space namespace, and the publishable ingest key — so the /v1/sites create
	// path is not a second place defaults are decided. A generated site has no
	// opt-out knob (nil ⇒ ON).
	if err := setProjectDefaults(&np, nil); err != nil {
		return Project{}, zip.Errorf(http.StatusInternalServerError, "rng: %v", err)
	}
	if err := s.State.store.CreateProject(ctx, np); err != nil {
		if errors.Is(err, errConflict) {
			return s.State.store.GetProject(ctx, org, slug)
		}
		return Project{}, zip.Errorf(http.StatusInternalServerError, "persist: %v", err)
	}
	provisionSpace(s, ctx, &np)
	return np, nil
}
