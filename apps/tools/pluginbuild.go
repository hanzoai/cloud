package tools

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hanzoai/cloud"
	"github.com/hanzoai/cloud/apps/connectorruntime"
	"github.com/hanzoai/cloud/types"
	"github.com/zap-proto/zip"
)

// The plugin builder: POST /v1/plugins/build.
//
// A plugin is TypeScript that declares actions against one HTTP API. The build
// is the SAME pipeline the committed connectors go through — esbuild to one
// CommonJS program (connectorruntime.Bundle), then compiled in goja
// (Runtime.Compile) — so "it built" means the artifact this deployment will
// actually execute compiled, not that a model produced plausible text.
//
// Compiling is the gate. Source that does not bundle, or bundles but does not
// compile, is rejected and never stored: a plugin in the store is one the
// runtime has already loaded once.
//
// CREDENTIALS ARE NOT PART OF A PLUGIN. A plugin names the connectors provider
// it needs and reads its credential from ctx.auth at run time, where it is
// already under KMS custody. Nothing here accepts, stores, or interpolates a
// key — so generated code is safe to read and diff, and rotating a key never
// means rebuilding a plugin. A request that puts a secret in `source` is
// refused rather than silently persisted.

const (
	maxPluginSource = 512 << 10 // 512 KiB of TypeScript is a very large connector
	maxSpecBytes    = 256 << 10
	maxSkillContent = 256 << 10 // one SKILL.md; prose, not a program
	buildModel      = "claude-sonnet-4-6"
)

// pluginName is one path-safe segment: the id a caller runs the plugin by.
var pluginName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// secretish catches the shapes of a credential pasted into source. It is a
// REFUSAL, not a scrub: a caller who pasted a key needs to know it landed in
// the wrong place, and a silently-stripped key looks like it worked.
var secretish = regexp.MustCompile(`(?i)(sk-[a-z0-9]{16,}|ghp_[a-z0-9]{20,}|xox[baprs]-[a-z0-9-]{10,}|AKIA[A-Z0-9]{12,}|-----BEGIN [A-Z ]*PRIVATE KEY-----)`)

type buildRequest struct {
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
	// Source is TypeScript to build as-is. Exactly one of Source or Spec.
	Source string `json:"source,omitempty"`
	// Spec is API documentation — an OpenAPI document, or prose describing the
	// endpoints — that the generator turns into Source. The generated source is
	// returned in the response, so a caller can read what will run before it runs.
	Spec string `json:"spec,omitempty"`
}

// buildPlugin builds, validates and stores one plugin for the caller's org.
func buildPlugin(s *cloud.Service[state], c *zip.Ctx) error {
	p, ok := PrincipalFrom(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if s.State.authored == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "the plugin store is not open")
	}
	var req buildRequest
	if err := json.Unmarshal(c.Body(), &req); err != nil {
		return zip.ErrBadRequest("malformed body: " + err.Error())
	}
	req.Name = strings.TrimSpace(req.Name)
	if !pluginName.MatchString(req.Name) {
		return zip.ErrBadRequest("name must be one lowercase path segment (a-z0-9, _ or -)")
	}
	if (req.Source == "") == (req.Spec == "") {
		return zip.ErrBadRequest("provide exactly one of source or spec")
	}
	if len(req.Source) > maxPluginSource || len(req.Spec) > maxSpecBytes {
		return zip.ErrBadRequest("input too large")
	}

	source := req.Source
	generated := false
	if req.Spec != "" {
		if s.State.ai == nil {
			return zip.Errorf(http.StatusServiceUnavailable, "no AI client is configured; post source instead of spec")
		}
		out, err := generateSource(c.Context(), s.State.ai, p.Org, req.Name, req.Provider, req.Spec)
		if err != nil {
			return zip.Errorf(http.StatusBadGateway, "generate: %s", err.Error())
		}
		source, generated = out, true
	}
	if m := secretish.FindString(source); m != "" {
		return zip.ErrBadRequest(
			"source contains what looks like a credential (" + m[:min(8, len(m))] +
				"…): a plugin reads its credential from ctx.auth, so register it as a connector instead")
	}

	bundled, err := bundleSource(req.Name, source)
	if err != nil {
		return c.JSON(http.StatusUnprocessableEntity, map[string]any{
			"error":     "build failed",
			"detail":    err.Error(),
			"source":    source,
			"generated": generated,
		})
	}

	stored, err := s.State.authored.Put(c.Context(), AuthoredPlugin{
		Org: p.Org, Name: req.Name, Provider: req.Provider,
		Source: source, Bundled: string(bundled),
	})
	if err != nil {
		return fmt.Errorf("store plugin: %w", err)
	}
	audrecordAction(s, c, "plugin.build", p.Org, req.Name, "success", http.StatusCreated)
	return c.JSON(http.StatusCreated, map[string]any{
		"plugin":    stored,
		"generated": generated,
		"bytes":     len(bundled),
	})
}

// bundleSource writes the TypeScript to a temp entrypoint and runs the SAME
// bundler the committed connectors use, then compiles the result in goja. The
// temp dir is removed either way; only the bytes survive.
func bundleSource(name, source string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "pluginbuild-")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	entry := filepath.Join(dir, "index.ts")
	if err := os.WriteFile(entry, []byte(source), 0o600); err != nil {
		return nil, fmt.Errorf("write source: %w", err)
	}
	bundled, err := connectorruntime.Bundle(entry)
	if err != nil {
		return nil, fmt.Errorf("bundle: %w", err)
	}
	// Compile it in the real runtime. A bundle that esbuild accepts can still
	// fail to load — goja is not a browser — and finding that out at build time
	// is the entire point of the gate.
	rt, err := connectorruntime.NewRuntime(nil)
	if err != nil {
		return nil, fmt.Errorf("runtime: %w", err)
	}
	if _, err := rt.Compile(name, bundled); err != nil {
		return nil, fmt.Errorf("compile: %w", err)
	}
	return bundled, nil
}

// generateSource asks the model for a connector against the given API docs.
// The prompt states the contract the bundler and runtime enforce anyway, so a
// model that ignores it fails the build rather than producing a stored plugin
// that cannot run.
func generateSource(ctx context.Context, ai types.AIClient, org, name, provider, spec string) (string, error) {
	var b strings.Builder
	b.WriteString("Write an ActivePieces-style TypeScript connector. Output ONLY TypeScript, no prose, no markdown fences.\n\n")
	b.WriteString("Contract:\n")
	b.WriteString("- Import only from @activepieces/pieces-framework and @activepieces/pieces-common.\n")
	b.WriteString("- Export a piece named " + name + " with one action per useful API operation.\n")
	b.WriteString("- Read the credential from ctx.auth. NEVER embed a key, token or secret in the code.\n")
	b.WriteString("- Use ctx.propsValue for inputs. Return the parsed response body.\n")
	b.WriteString("- No Node built-ins, no filesystem, no child processes: this runs in goja.\n")
	if provider != "" {
		b.WriteString("- The credential comes from the '" + provider + "' connector.\n")
	}
	b.WriteString("\nAPI documentation:\n")
	b.WriteString(spec)

	resp, err := ai.ChatCompletion(ctx, &types.ChatRequest{
		Model: buildModel, Prompt: b.String(), Org: org,
	})
	if err != nil {
		return "", err
	}
	src := strings.TrimSpace(resp.Content)
	if src == "" {
		return "", fmt.Errorf("model returned no source")
	}
	return stripFences(src), nil
}

// stripFences removes a ```ts wrapper when the model adds one despite the
// instruction. Cheaper than failing the build over punctuation.
func stripFences(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	// Trim AFTER dropping the closing fence, not before: the newline that
	// separated the source from the fence is left behind otherwise.
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "```"))
}

// listAuthored — GET /v1/plugins/authored: the org's own built plugins,
// distinct from GET /v1/plugins, which is the deployment's mounted subsystems.
func listAuthored(s *cloud.Service[state], c *zip.Ctx) error {
	p, ok := PrincipalFrom(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if s.State.authored == nil {
		return c.JSON(http.StatusOK, map[string]any{"plugins": []AuthoredPlugin{}})
	}
	out, err := s.State.authored.List(c.Context(), p.Org)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"plugins": out})
}

// deleteAuthored — DELETE /v1/plugins/authored/:id.
func deleteAuthored(s *cloud.Service[state], c *zip.Ctx) error {
	p, ok := PrincipalFrom(c)
	if !ok {
		return zip.ErrForbidden("a validated principal is required")
	}
	if s.State.authored == nil {
		return zip.Errorf(http.StatusServiceUnavailable, "the plugin store is not open")
	}
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		return zip.ErrBadRequest("missing plugin id")
	}
	if _, err := s.State.authored.Get(c.Context(), p.Org, id); err == sql.ErrNoRows {
		return zip.ErrNotFound("no such plugin")
	}
	if err := s.State.authored.Delete(c.Context(), p.Org, id); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{"deleted": id})
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
