package client

// graph_base.go composes the caller's org Base into the surface graph as one
// field, `base`. The Base types its own records (hanzoai/base/graph, served by
// apps/base at /v1/base/graphql): its schema is the org's own, so it is fetched
// with the caller's request, and a `base { … }` subtree is one hop there, its
// selection printed back into a document. Every field below is answered by the
// org's Base under its own rules, as /v1/base/graphql answers it.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/hanzoai/cloud/openapi"
	"github.com/zap-proto/zip"
)

const basePath = "/v1/base/graphql"

func baseAt(method string) openapi.Field {
	return openapi.Field{App: "base", Method: method, Path: basePath}
}

// withBase is the surface's schema with the caller's org Base under `base`, or
// the surface's alone when the caller has no Base to read.
func withBase(ctx context.Context, at At, fleet string) string {
	code, raw, err := exchange(at, baseAt(http.MethodGet), basePath, nil, relayed(ctx))
	if err != nil || code != http.StatusOK {
		return fleet
	}
	sdl := string(raw)
	// the Base's schema block names its roots; here they are the field `base`
	if strings.HasPrefix(sdl, "schema ") {
		_, sdl, _ = strings.Cut(sdl, "\n")
	}
	out := rootField(fleet, "Query", "base: BaseQuery")
	if strings.Contains(sdl, "type BaseMutation {") {
		out = rootField(out, "Mutation", "base: BaseMutation")
	}
	return out + "\n" + sdl
}

func rootField(sdl, typ, field string) string {
	head := "type " + typ + " {\n"
	line := `  """The caller's org Base: its collections, typed (` + basePath + ` serves this alone)."""` + "\n  " + field + "\n"
	if i := strings.Index(sdl, head); i >= 0 {
		return sdl[:i+len(head)] + line + sdl[i+len(head):]
	}
	return sdl + head + line + "}\n"
}

// delegate answers `base { … }` as one request of the org's graph.
func delegate(ctx context.Context, at At, kind string, sel zip.Selection) (any, error) {
	if len(sel.Args) > 0 {
		return nil, zip.ErrBadRequest("base takes no arguments")
	}
	var b strings.Builder
	b.WriteString(kind + " ")
	if err := printSelection(&b, sel.Fields); err != nil {
		return nil, zip.ErrBadRequest(err.Error())
	}
	body, err := json.Marshal(zip.GraphRequest{Query: b.String()})
	if err != nil {
		return nil, err
	}
	code, raw, err := exchange(at, baseAt(http.MethodPost), basePath, body, relayed(ctx))
	if err != nil {
		return nil, err
	}
	var res struct {
		Data   map[string]any `json:"data"`
		Errors []struct {
			Message string `json:"message"`
			Path    []any  `json:"path"`
		} `json:"errors"`
		Message string `json:"message"`
		Error   string `json:"error"`
	}
	_ = json.Unmarshal(raw, &res)
	if code >= 500 {
		return nil, fmt.Errorf("base answered %d: %s", code, raw)
	}

	errs := map[string]string{}
	var whole []string
	for _, e := range res.Errors {
		if len(e.Path) == 0 {
			whole = append(whole, e.Message)
			continue
		}
		errs[pathKey(e.Path)] = e.Message
	}
	if res.Data == nil {
		// refused whole: by the graph's bounds, or by the gate in front of it
		msg := strings.Join(append(whole, res.Message, res.Error), " ")
		if code < 400 {
			code = http.StatusBadRequest
		}
		return nil, &zip.HTTPError{Status: code, Msg: strings.TrimSpace(msg)}
	}
	root := "BaseQuery"
	if kind == "mutation" {
		root = "BaseMutation"
	}
	return &answered{typ: root, m: res.Data, errs: errs}, nil
}

// answered is a subtree another graph answered: its data, and each error it
// reported, told at its own path.
type answered struct {
	typ  string
	m    map[string]any
	at   string
	errs map[string]string
}

func (a *answered) Type() string {
	if a.typ != "" {
		return a.typ
	}
	t, _ := a.m["__typename"].(string)
	return t
}

func (a *answered) Field(_ context.Context, s zip.Selection) (any, error) {
	key := a.at + "/" + s.Key()
	if msg, ok := a.errs[key]; ok {
		return nil, zip.ErrBadRequest(msg)
	}
	return wrap(a.m[s.Key()], key, s.Fields, a.errs), nil
}

// wrap answers an object with a selection as an answered, so its type name and
// errors reach the caller; a leaf, or a JSON value nobody selected into, as is.
func wrap(v any, at string, sel []zip.Selection, errs map[string]string) any {
	if len(sel) == 0 {
		return v
	}
	switch t := v.(type) {
	case map[string]any:
		return &answered{m: t, at: at, errs: errs}
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = wrap(x, at+"/"+strconv.Itoa(i), sel, errs)
		}
		return out
	}
	return v
}

func pathKey(path []any) string {
	var b strings.Builder
	for _, p := range path {
		b.WriteString("/")
		if f, ok := p.(float64); ok {
			b.WriteString(strconv.Itoa(int(f)))
		} else {
			b.WriteString(fmt.Sprint(p))
		}
	}
	return b.String()
}

var gqlName = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`)

// printSelection writes a selection as GraphQL. Its arguments are values by
// now, variables included, and a JSON scalar is a GraphQL value too.
func printSelection(b *strings.Builder, sel []zip.Selection) error {
	b.WriteString("{")
	for _, s := range sel {
		b.WriteString(" ")
		if s.Alias != "" {
			b.WriteString(s.Alias + ": ")
		}
		b.WriteString(s.Name)
		if len(s.Args) > 0 {
			b.WriteString("(")
			for i, k := range sortedKeys(s.Args) {
				if i > 0 {
					b.WriteString(", ")
				}
				b.WriteString(k + ": ")
				if err := printValue(b, s.Args[k]); err != nil {
					return err
				}
			}
			b.WriteString(")")
		}
		if len(s.Fields) > 0 {
			if err := printSelection(b, s.Fields); err != nil {
				return err
			}
		}
	}
	b.WriteString(" }")
	return nil
}

func printValue(b *strings.Builder, v any) error {
	switch t := v.(type) {
	case []any:
		b.WriteString("[")
		for i, x := range t {
			if i > 0 {
				b.WriteString(", ")
			}
			if err := printValue(b, x); err != nil {
				return err
			}
		}
		b.WriteString("]")
	case map[string]any:
		b.WriteString("{")
		for i, k := range sortedKeys(t) {
			if !gqlName.MatchString(k) {
				return fmt.Errorf("%q is not a field name", k)
			}
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(k + ": ")
			if err := printValue(b, t[k]); err != nil {
				return err
			}
		}
		b.WriteString("}")
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return err
		}
		b.Write(raw)
	}
	return nil
}

func sortedKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
