package reflect

import "reflect"

// Alias a few reflect types
type Type reflect.Type

// Alias a few consts
const (
	Ptr = reflect.Ptr
)

// Aliase reflect Funcs
var (
	Append      = reflect.Append
	AppendSlice = reflect.AppendSlice
	// ArrayOf     = reflect.ArrayOf
	ChanOf    = reflect.ChanOf
	CopySlice = reflect.Copy
	DeepEqual = reflect.DeepEqual
	// FuncOf      = reflect.FuncOf
	Indirect  = reflect.Indirect
	MakeChan  = reflect.MakeChan
	MakeFunc  = reflect.MakeFunc
	MakeMap   = reflect.MakeMap
	MakeSlice = reflect.MakeSlice
	MapOf     = reflect.MapOf
	New       = reflect.New
	NewAt     = reflect.NewAt
	PtrTo     = reflect.PtrTo
	Select    = reflect.Select
	SliceOf   = reflect.SliceOf
	// StructOf    = reflect.StructOf
	TypeOf  = reflect.TypeOf
	ValueOf = reflect.ValueOf
	Zero    = reflect.Zero
)
