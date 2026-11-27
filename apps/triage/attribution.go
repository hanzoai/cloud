package triage

import (
	"cmp"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/hanzoai/cloud/apps/event"
)

// Site is where a fault crashed: the source file, line, function and component read
// off its stack (or its origin), and the top of the stack itself. Which repository
// owns the fault is the config's (Repo.Match), never read from here.
type Site struct {
	File      string `json:"file,omitempty"`
	Line      uint32 `json:"line,omitempty"`
	Column    uint32 `json:"column,omitempty"`
	Function  string `json:"function,omitempty"`
	Component string `json:"component,omitempty"`
	// Stack is the top frames, crash site first, `*` marking the platform's own.
	Stack string `json:"stack,omitempty"`
}

// maxFrames is how many stack frames an issue shows.
const maxFrames = 12

// vendorPaths are third-party or runtime frames, never the crash site of the code
// that owns the fault.
var vendorPaths = []string{
	"node_modules/", "webpack/bootstrap", "webpack/runtime", "<anonymous>", "native code",
	"runtime/", "net/http/", "reflect/", "chrome-extension://", "moz-extension://",
}

// attribute is the crash site of a fault: its own frame nearest the crash, else the
// nearest named frame, else the origin when that is a source location and no site.
func attribute(f event.Fault) Site {
	var s Site
	if fr, ok := crashFrame(f.Frames); ok {
		s.File, s.Line, s.Column, s.Function = normalizeFile(fr.File), fr.Line, fr.Column, strings.TrimSpace(fr.Function)
	} else if file, line, col := parseOrigin(f.Origin); isSource(file, line) {
		s.File, s.Line, s.Column = normalizeFile(file), line, col
	}
	s.Component = component(f.Attributes, s.File, s.Function)
	s.Stack = stack(f.Frames)
	return s
}

// crashFrame scans from the crash site (last) outward: the platform's own frames
// first, then any non-vendor frame with a file, then any frame that says anything.
func crashFrame(frames []event.FaultFrame) (event.FaultFrame, bool) {
	for _, own := range []bool{true, false} {
		for i := len(frames) - 1; i >= 0; i-- {
			if fr := frames[i]; (fr.Own || !own) && strings.TrimSpace(fr.File) != "" && !vendor(fr.File) {
				return fr, true
			}
		}
	}
	for i := len(frames) - 1; i >= 0; i-- {
		if fr := frames[i]; strings.TrimSpace(fr.File) != "" || strings.TrimSpace(fr.Function) != "" {
			return fr, true
		}
	}
	return event.FaultFrame{}, false
}

func vendor(file string) bool {
	file = strings.ToLower(strings.TrimSpace(file))
	if file == "" {
		return true
	}
	for _, v := range vendorPaths {
		if strings.Contains(file, v) {
			return true
		}
	}
	return false
}

var (
	bundlePrefix = regexp.MustCompile(`^(webpack-internal:///(\./)?|webpack://_N_E/(\./)?|webpack:///(\./)?|app:///(\./)?|file://)`)
	modulePrefix = regexp.MustCompile(`github\.com/[^/]+/[^/]+/`)
	buildRoot    = regexp.MustCompile(`^.*/(workspace|app|build)/`)
)

// normalizeFile is a repository-relative path where one can be had: a bundle's
// scheme, query and host, a Go module path and a build root are taken off.
func normalizeFile(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = bundlePrefix.ReplaceAllString(s, "")
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		if u, err := url.Parse(s); err == nil && u.Path != "" {
			s = strings.TrimPrefix(u.Path, "/")
		}
	}
	if loc := modulePrefix.FindStringIndex(s); loc != nil {
		s = s[loc[1]:]
	}
	s = buildRoot.ReplaceAllString(s, "")
	return strings.TrimPrefix(s, "./")
}

// parseOrigin splits "path/to/file.ts:42:10" into file, line and column, leaving a
// URL's scheme colon alone.
func parseOrigin(origin string) (file string, line, col uint32) {
	s := strings.TrimSpace(origin)
	scheme := 0
	if i := strings.Index(s, "://"); i >= 0 {
		scheme = i + 3
	}
	parts := strings.Split(s[scheme:], ":")
	number := func(i int) (uint32, bool) {
		var n uint32
		if i < 0 || parts[i] == "" || len(parts[i]) > 9 {
			return 0, false
		}
		for _, r := range parts[i] {
			if r < '0' || r > '9' {
				return 0, false
			}
			n = n*10 + uint32(r-'0')
		}
		return n, true
	}
	n := len(parts)
	if l, ok := number(n - 2); ok && n >= 3 {
		if c, ok := number(n - 1); ok {
			return s[:scheme] + strings.Join(parts[:n-2], ":"), l, c
		}
	}
	if l, ok := number(n - 1); ok && n >= 2 {
		return s[:scheme] + strings.Join(parts[:n-1], ":"), l, 0
	}
	return s, 0, 0
}

// isSource tells an origin that is a source location from one that is a site's host
// ("app.hanzo.ai"), which apps/event also puts there.
func isSource(file string, line uint32) bool {
	switch s := strings.TrimSpace(file); {
	case line > 0:
		return true
	case s == "":
		return false
	case strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://"):
		u, err := url.Parse(s)
		return err == nil && strings.Trim(u.Path, "/") != ""
	default:
		return strings.Contains(s, "/")
	}
}

// component names the UI component or backend module: the one the client said, the
// leaf of its component path, the file's stem, else the function's receiver.
func component(attrs map[string]string, file, fn string) string {
	if c := first(attrs, "component", "$component"); c != "" {
		return c
	}
	if p := first(attrs, "component_path", "$component_path"); p != "" {
		segs := strings.Split(p, ">")
		leaf := strings.TrimSpace(segs[len(segs)-1])
		if open, end := strings.Index(leaf, "["), strings.Index(leaf, "]"); open >= 0 && end > open+1 {
			return strings.TrimSpace(leaf[open+1 : end])
		}
		if leaf != "" {
			return leaf
		}
	}
	if file != "" {
		base := path.Base(file)
		if stem, _, ok := strings.Cut(base, "."); ok && stem != "" && !slices.Contains([]string{"index", "page", "route", "main"}, stem) {
			return stem
		}
		if dir := path.Base(path.Dir(file)); dir != "." && dir != "/" {
			return dir
		}
	}
	if i := strings.LastIndex(fn, "."); i > 0 {
		return strings.Trim(fn[:i], "()*")
	}
	return fn
}

func first(attrs map[string]string, keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(attrs[k]); v != "" {
			return v
		}
	}
	return ""
}

// stack is the top frames, crash site first.
func stack(frames []event.FaultFrame) string {
	var lines []string
	for i := len(frames) - 1; i >= 0 && len(lines) < maxFrames; i-- {
		fr := frames[i]
		tag := " "
		if fr.Own {
			tag = "*"
		}
		where := cmp.Or(normalizeFile(fr.File), "<unknown>")
		if fr.Line > 0 {
			where += fmt.Sprintf(":%d", fr.Line)
		}
		lines = append(lines, fmt.Sprintf("%s %s (%s)", tag, cmp.Or(strings.TrimSpace(fr.Function), "<anonymous>"), where))
	}
	return strings.Join(lines, "\n")
}

// at is the site as "file:line", "" when none is known.
func (s Site) at() string {
	if s.File == "" {
		return ""
	}
	if s.Line == 0 {
		return s.File
	}
	return fmt.Sprintf("%s:%d", s.File, s.Line)
}
