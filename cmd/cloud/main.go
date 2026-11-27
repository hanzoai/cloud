// Command cloud is the Hanzo Cloud light-host router (HIP-0106).
//
// All router, lifecycle, wake, wire, peer, console, and OpenAPI composition
// logic lives in github.com/hanzoai/cloud/cmd/cloud/host so private or custom
// distributions can register additional ZAP plugins onto manifest and invoke
// host.Main() without duplicating the host.
package main

import "github.com/hanzoai/cloud/cmd/cloud/host"

func main() {
	host.Main()
}
