package approval

import "github.com/hanzoai/cloud/clients/commerce/models/mixin"

// Compile-time guard: every model MUST satisfy mixin.Entity. A struct field
// named like an embedded Model[T] method (Key/Id/Kind/Save/…) silently
// shadows it and breaks the interface — this assertion turns that into a
// build error instead of a runtime nil panic in Query().Get().
var _ mixin.Entity = (*Approval)(nil)
