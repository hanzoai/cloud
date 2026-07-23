package framework

import (
	"reflect"
	"testing"
)

// TestStampFixtureCloneStaysShallow guards the always-on registry-immutability
// invariant. stampFixture (module.go) hands each org a virtual always-on DocType by
// append-cloning the fixture's Fields and Perms. That append-copy is a genuine deep
// copy ONLY while DocField and DocPerm are flat value structs (scalar fields). If
// either grows a slice, map, pointer, or nested-struct field, the clone silently
// becomes shallow: a caller mutating a RESOLVED always-on fixture would then corrupt
// the process-global registry for every OTHER org — a cross-tenant registry
// corruption. This test fails the instant that assumption breaks, forcing the clone
// in stampFixture to be deepened (or this guard updated deliberately, in tandem).
func TestStampFixtureCloneStaysShallow(t *testing.T) {
	scalar := func(k reflect.Kind) bool {
		switch k {
		case reflect.String, reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
			return true
		}
		return false
	}
	for _, typ := range []reflect.Type{reflect.TypeOf(DocField{}), reflect.TypeOf(DocPerm{})} {
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			if !scalar(f.Type.Kind()) {
				t.Fatalf("%s.%s is %s, not a scalar — stampFixture's append-clone is now SHALLOW: "+
					"a resolved always-on fixture can corrupt the process-global registry across orgs. "+
					"Deep-clone this field in stampFixture (module.go) before permitting it here.",
					typ.Name(), f.Name, f.Type.Kind())
			}
		}
	}
}
