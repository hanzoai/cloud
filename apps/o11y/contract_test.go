package o11y

import "github.com/hanzoai/cloud"

// o11y mounts like every other subsystem, and the compiler is what keeps it that
// way. cloud.MountFunc is `func(cloud.Router, cloud.Deps) error` — the shape
// cloud.Plugin takes, and therefore the shape a generated plugin main can call.
//
// This app used to be the one exception: Mount demanded a concrete *zip.App,
// which the Router interface does not promise. Nothing was wrong with the code it
// ran; the cost was that the deviation CASCADED — plugin/o11y/main.go could not be
// a generated stub like its ~120 siblings, because it had to build an *App by hand
// just to have something of the right type to pass.
//
// One line, and the exception cannot come back silently: widen the parameter again
// and this stops compiling, here, instead of surfacing as a plugin main that has to
// be special.
var _ cloud.MountFunc = Mount
