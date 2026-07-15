package cloud

// Version is the API contract/build version emitted as the X-Api-Version
// response header — the support-correlation build signal (the brand-neutral
// analog of the image tag). Stamped at link time by the release build:
//
//	-ldflags "-X github.com/hanzoai/cloud.Version=<image tag>"
//
// and overridable at runtime by CLOUD_VERSION, which the operator sets from the
// deployed image tag without a rebuild (the same channel as CLOUD_BRAND).
// Defaults to "dev" for an untagged go build / go test.
var Version = "dev"
