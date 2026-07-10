package spew

import "github.com/davecgh/go-spew/spew"

var (
	Dump  = spew.Dump
	Fdump = spew.Fdump
	Sdump = spew.Sdump
)

func init() {
	spew.Config.Indent = "  "
	spew.Config.MaxDepth = 100
	spew.Config.DisableMethods = false
	spew.Config.SortKeys = true
	spew.Config.SpewKeys = true
}
