package script

import (
	"context"
	"fmt"
	"sort"
	"sync"
)

// The process-wide registry: one runtime, and the programs a composition root
// installed. The programs are data (piece/catalog) that only a process serving
// connectors links, so this package holds none of its own — a process that
// never installs a source runs nothing, and Has answers false.
var pkg = newRegistry()

// source is one program, compiled the first time it runs. There are hundreds
// and a process runs a handful, so compiling every one up front would spend
// seconds and memory on programs nobody calls.
type source struct {
	once sync.Once
	prog *Program
	err  error
}

type registry struct {
	rt   *Runtime
	mu   sync.RWMutex
	src  func(id string) ([]byte, bool)
	ids  []string
	byID map[string]*source
}

func newRegistry() *registry {
	rt, err := NewRuntime(nil)
	if err != nil {
		panic("script: build runtime: " + err.Error())
	}
	return &registry{rt: rt, byID: map[string]*source{}}
}

// Install sets where programs come from: src returns a program's bundled
// source by id, and ids lists every id it has. The composition root that links
// the catalog calls it once, from init.
func Install(src func(id string) ([]byte, bool), ids []string) {
	pkg.mu.Lock()
	defer pkg.mu.Unlock()
	pkg.src = src
	pkg.ids = append([]string(nil), ids...)
	sort.Strings(pkg.ids)
	pkg.byID = map[string]*source{}
}

// program compiles id's program once and returns it.
func program(id string) (*Program, error) {
	pkg.mu.Lock()
	src := pkg.src
	s, ok := pkg.byID[id]
	if !ok {
		s = &source{}
		pkg.byID[id] = s
	}
	pkg.mu.Unlock()
	if src == nil {
		return nil, fmt.Errorf("script: unknown provider %q", id)
	}
	s.once.Do(func() {
		b, ok := src(id)
		if !ok {
			s.err = fmt.Errorf("script: unknown provider %q", id)
			return
		}
		s.prog, s.err = pkg.rt.Compile(id, b)
	})
	return s.prog, s.err
}

// Run executes one provider action natively in-process. org is the caller's
// already-validated tenant — recorded for attribution; the runtime resolves no
// credential itself, it runs with the auth the caller passes. Returns the
// action result or an error (unknown provider/action, or the action's own
// failure).
func Run(ctx context.Context, org, provider, action string, auth any, props map[string]any) (any, error) {
	c, err := program(provider)
	if err != nil {
		return nil, err
	}
	return pkg.rt.Run(ctx, c, RunInput{Action: action, Auth: auth, Props: props})
}

// Validate runs a piece's own credential check (Runtime.Validate) through the
// registry. ErrNoCheck when the piece has no program here or its auth
// declares no check.
func Validate(ctx context.Context, provider, kind string, auth any) error {
	if !Has(provider) {
		return ErrNoCheck
	}
	c, err := program(provider)
	if err != nil {
		return err
	}
	return pkg.rt.Validate(ctx, c, kind, auth)
}

// Has reports whether a provider has a program here (used to fail closed
// before a doomed call).
func Has(provider string) bool {
	pkg.mu.RLock()
	defer pkg.mu.RUnlock()
	if pkg.src == nil {
		return false
	}
	i := sort.SearchStrings(pkg.ids, provider)
	return i < len(pkg.ids) && pkg.ids[i] == provider
}

// Programs lists the ids with a program here, sorted.
func Programs() []string {
	pkg.mu.RLock()
	defer pkg.mu.RUnlock()
	return append([]string(nil), pkg.ids...)
}
