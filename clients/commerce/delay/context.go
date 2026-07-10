package delay

import (
	stdctx "context"
	"reflect"
)

var (
	stdContextType = reflect.TypeOf((*stdctx.Context)(nil)).Elem()
)

func isContext(t reflect.Type) bool {
	return t.Implements(stdContextType)
}
