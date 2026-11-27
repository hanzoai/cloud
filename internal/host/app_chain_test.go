package host

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	cloud "github.com/hanzoai/cloud/sdk"
)

// TestAppMiddlewareChain pins the order and the members of the middleware chain App
// installs. The golden is the list as it stood before the framework left this package,
// with the package's own name folded out so a move cannot show as a change.
func TestAppMiddlewareChain(t *testing.T) {
	t.Setenv("CLOUD_ENV", "test")
	app := cloud.App("chain", &cloud.Config{Brand: "hanzo", Domain: "hanzo.app"}, cloud.Deps{}, nil)
	entries := reflect.ValueOf(app).Elem().FieldByName("entries")
	entries = reflect.NewAt(entries.Type(), unsafe.Pointer(entries.UnsafeAddr())).Elem()
	var got []string
	for i := 0; i < entries.Len(); i++ {
		n := entries.Index(i).FieldByName("n")
		n = reflect.NewAt(n.Type(), unsafe.Pointer(n.UnsafeAddr())).Elem().Elem()
		name := n.Type().String()
		if n.Kind() == reflect.Func {
			name = runtime.FuncForPC(n.Pointer()).Name()
		}
		name = name[strings.LastIndex(name, "/")+1:]
		for _, p := range []string{"cloud.", "host.", "sdk."} {
			name = strings.Replace(name, p, "~.", 1)
		}
		got = append(got, name)
	}
	_, here, _, _ := runtime.Caller(0)
	golden := filepath.Join(filepath.Dir(here), "testdata", "app_chain.txt")
	want := strings.Join(got, "\n") + "\n"
	if os.Getenv("UPDATE") != "" {
		_ = os.MkdirAll(filepath.Dir(golden), 0o755)
		if err := os.WriteFile(golden, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	b, err := os.ReadFile(golden)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != want {
		t.Fatalf("middleware chain changed\n--- want\n%s--- got\n%s", b, want)
	}
}
