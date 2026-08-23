package projects

import (
	"context"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/sites"
	"github.com/hanzoai/cloud/apps/template"
	"github.com/zap-proto/zip"
)

// projectsFork is the body of POST /v1/projects/fork: which parent to fork and,
// optionally, the target project name/slug. Both target fields default from the
// parent (name = its title, slug = the parent slug) when omitted.
type projectsFork struct {
	Slug string `json:"slug"` // parent slug to fork — catalog template or published project (required)
	Name string `json:"name"` // target project name (optional; defaults to the parent's title)
	// Variant picks a template's format/page/theme (optional; defaults to the
	// template's first shape). This is the axis the catalog used to spend
	// sibling slugs on, so it is expressed here, where the user's preference is.
	Variant string `json:"variant"`
	// Target overrides the derived project slug (optional; defaults to the
	// parent slug). Kept distinct from Slug so callers can rename on fork.
	Target string `json:"target"`
}

// ForkProject creates a project seeded from a PUBLISHED EXAMPLE — either a
// starter-kit template from the ONE embedded gallery catalog, or any live
// project on the platform (an example a seeded creator published, or another
// org's app serving at <slug>.hanzo.app). Answers 201 with the new project.
//
// `slug` names the PARENT to fork and is required. Templates resolve first, and
// the caller org's own private templates ahead of the public gallery, so a
// curated template slug keeps meaning the same thing even if someone later
// publishes a live project under it; `variant` picks that template's
// format/page/theme. If no template matches, the slug resolves to the UNIQUE
// live project that owns it across all orgs — the same resolution the site edge
// uses to serve <slug>.hanzo.app, so what you can browse is what you can fork.
//
// `name` and `target` override the derived project name and slug; everything
// else is inherited from the parent. A live parent contributes its REPO, so the
// child builds from the same source — the parent's deployed bytes are never
// copied, because releases are per-tenant by design and the fork publishes its
// own. The parent it actually resolved is stamped on the child as `forkedFrom`,
// so attribution is a fact recorded at fork time rather than a claim
// reconstructed later.
//
// It funnels through the SAME create path POST /v1/projects uses, so slug
// validation, org scoping, ID minting and the 409 on a slug the caller's own org
// already uses are identical.
//
// Scope: a validated principal is required (403 without one) and the child is
// created in THAT principal's org.
func (o ops) fork(ctx context.Context, in *projectsFork) (*projectsProject, error) {
	c, org, err := o.callerOf(ctx)
	if err != nil {
		return nil, err
	}
	if err := requireBody(c); err != nil {
		return nil, err
	}
	slug := strings.TrimSpace(in.Slug)
	if slug == "" {
		return nil, zip.ErrBadRequest("slug is required")
	}
	req, err := seedFrom(o.s, c, org, slug, strings.TrimSpace(in.Variant))
	if err != nil {
		return nil, err
	}
	// Caller overrides land on top of the parent's defaults; lineage is not one of
	// them (projectsCreate.ForkedFrom is json:"-", set by seedFrom).
	if n := strings.TrimSpace(in.Name); n != "" {
		req.Name = n
	}
	if t := strings.TrimSpace(in.Target); t != "" {
		req.Slug = t
	}
	return createProject(o.s, c, org, req)
}

// seedFrom resolves the fork parent and returns the projectsCreate it seeds. Templates
// FIRST, through the ONE catalog entry point (template.Lookup), which resolves the
// CALLER ORG's own private templates ahead of the public gallery: a curated
// template slug is a stable public name and must keep meaning the same thing even
// if some org later publishes a live project under it, and an org's private
// template is forkable only by that org (Lookup binds org, so another org's row is
// not reachable from here at all). Falling back to the unique live owner of the
// slug is the SAME resolution the sites edge uses to serve <slug>.hanzo.app, so
// "what you can browse is what you can fork". A live parent contributes its repo,
// so the child builds from the same source; the parent's deployed BYTES are never
// copied — releases are per-tenant by design, so the fork publishes its own.
func seedFrom(s *cloud.Service[state], c *zip.Ctx, org, slug, variant string) (projectsCreate, error) {
	if t, found := template.Lookup(c.Context(), org, slug); found {
		// One template, one slug: the format/page/theme it ships in is chosen
		// here, from the catalog's own options.
		v, ok := t.Variant(variant)
		if !ok {
			return projectsCreate{}, zip.ErrNotFound("template variant not found")
		}
		// Lineage is owner-qualified for a private template (the same shape a live
		// project parent gets) and a bare slug for the public catalog, whose slug IS
		// the global name.
		from := t.Slug
		if t.Org != "" {
			from = t.Org + "/" + t.Slug
		}
		req := projectsCreate{
			Name: t.Title, Slug: t.Slug, Description: t.Description,
			Framework: mapFramework(v.Framework), ForkedFrom: from,
		}
		// A non-default shape carries its id into the derived slug, so two
		// shapes of one template can live side by side in the same org.
		if len(t.Variants) > 0 && v.ID != t.Variants[0].ID {
			req.Slug += "-" + v.ID
		}
		// The derived slug is a DEFAULT, not the caller's choice, so a template
		// whose name is a reserved subdomain (metrics) must still fork in one
		// click. `-template` is the suffix its live demo already carries
		// (metrics-template.hanzo.app), so the derived name matches the demo.
		if sites.IsReserved(req.Slug) {
			req.Slug += "-template"
		}
		req.Repo.URL = v.Source
		return req, nil
	}
	if variant != "" {
		return projectsCreate{}, zip.ErrNotFound("template variant not found")
	}
	p, err := s.State.store.ResolveUniqueLiveSlug(c.Context(), slug)
	if err != nil {
		return projectsCreate{}, zip.ErrNotFound("no template or published project with that slug")
	}
	req := projectsCreate{
		Name: p.Name, Slug: p.Slug, Description: p.Description,
		Framework: p.Framework, ForkedFrom: p.Org + "/" + p.Slug,
	}
	req.Repo.URL = p.RepoURL
	req.Repo.Branch = p.RepoBranch
	return req, nil
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
