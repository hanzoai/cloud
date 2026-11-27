// Package basetest serves a real base app on the plane for a test of another app
// that writes an org's Base over it.
//
// It is the engine production runs, not a double: the base app mounted with the
// embed on, every org's Base opened on first use under the test's own data
// directory, and its plane ops (base_import, base_upsert, base_get, base_list)
// answering the "base" peer by name. So a record a caller writes is validated by
// the same fields, the same modules and the same rules it meets in production.
package basetest
