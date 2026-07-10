package projects

import (
	"strings"

	"github.com/hanzoai/cloud/clients/templates"
	"github.com/zap-proto/zip"
)

// forkReq is the body of POST /v1/projects/fork: which template to fork and,
// optionally, the target project name/slug. Both target fields default from the
// template (name = template title, slug = slugify(title)) when omitted.
type forkReq struct {
	Slug string `json:"slug"` // template slug to fork (required)
	Name string `json:"name"` // target project name (optional; defaults to template title)
	// Target overrides the derived project slug (optional; defaults to the
	// template slug). Kept distinct from Slug so callers can rename on fork.
	Target string `json:"target"`
}

// fork creates a real Project seeded from a starter-kit template. It reads the
// ONE embedded gallery catalog (templates.Get — no catalog copy here), maps the
// template's freeform framework label to the projects build-hint enum, and then
// funnels through the SAME createProject path POST /v1/projects uses — so slug
// validation, org scoping, ID minting, and conflict handling are not duplicated.
// Returns 201 with the created project.
func (s *svc) fork(c *zip.Ctx) error {
	org, ok := org(c)
	if !ok {
		return zip.ErrForbidden("X-Org-Id required")
	}
	var body forkReq
	if err := c.Bind(&body); err != nil {
		return err
	}
	slug := strings.TrimSpace(body.Slug)
	if slug == "" {
		return zip.ErrBadRequest("template slug is required")
	}
	t, found := templates.Get(slug)
	if !found {
		return zip.ErrNotFound("template not found")
	}

	// Seed the CreateProject from the template: name = title (or override),
	// slug = target override or the template slug, framework mapped from the
	// template's label, repo = the gallery source (a git fork URL).
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = t.Title
	}
	req := createReq{
		Name:        name,
		Slug:        strings.TrimSpace(body.Target),
		Description: t.Description,
		Framework:   mapFramework(t.Framework),
	}
	if req.Slug == "" {
		req.Slug = t.Slug
	}
	req.Repo.URL = t.Source

	return s.createProject(c, org, req)
}

// mapFramework maps a template's freeform framework label (e.g. "Next.js 14.2 +
// TS", "React 18 + Vite", "HTML/Gulp") to the closed projects build-hint enum
// (see `frameworks`). The label is a human display string from the gallery, so
// the match is by recognizable token, most-specific first:
//
//	Vite present            -> "vite"  (a build step, even under React)
//	Next.js                 -> "next"
//	React                   -> "react"
//	one of the enum names    -> that name (astro/svelte/vue/remix/nuxt/static)
//	anything else (HTML/…)   -> "static" (already-built, no build step)
//
// The result is always a valid `frameworks` key, so createProject never rejects
// a forked project on framework.
func mapFramework(label string) string {
	l := strings.ToLower(strings.TrimSpace(label))
	switch {
	case l == "":
		return "static"
	case strings.Contains(l, "vite"):
		return "vite"
	case strings.Contains(l, "next"):
		return "next"
	case strings.Contains(l, "nuxt"):
		return "nuxt"
	case strings.Contains(l, "remix"):
		return "remix"
	case strings.Contains(l, "astro"):
		return "astro"
	case strings.Contains(l, "svelte"):
		return "svelte"
	case strings.Contains(l, "react"):
		return "react"
	case strings.Contains(l, "vue"):
		return "vue"
	default:
		return "static"
	}
}
