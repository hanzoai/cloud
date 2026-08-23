// Package edge holds the transport ceilings of a PUBLIC HTTP edge.
//
// It exists because TWO processes are one edge and each was configured
// separately. cmd/cloud is the host every request reaches first; the program
// behind it is built by cloud.App(). App() read these off Config and the host
// read nothing, so the host enforced the framework defaults — 4 KiB headers
// and 4 MiB bodies — and no setting downstream could be reached past it.
// GATEWAY_BODY_LIMIT was set to 100 MiB in the pod's environment and 4,194,305
// bytes still answered 400: the value was declared, and the process that decides
// had never read it.
//
// That also means the 16 MiB default had never taken effect. It exists so a
// 1M-token prompt (~4.3 MB of JSON) can reach the 1M-context models; at the
// framework's 4 MiB those models were unreachable, and the wire error is the
// opaque 400 "Error when parsing request", which reads like a malformed payload
// rather than a size cap.
//
// This package is a LEAF on purpose. The host links zip, manifest and webui and
// nothing else — importing the cloud root to reach a number would re-fuse the
// monolith the host was built to replace. Anything that terminates public HTTP
// calls these; there is no second place to write the number down, because a
// second place is how this broke.
package edge

import (
	"os"
	"strconv"
	"strings"
)

// ReadBufferSize is the header ceiling. Above fiber's 4 KiB default so a
// multi-domain SSO session (Domain=.hanzo.ai cookies on every subdomain) does
// not 431 at the public edge. Env GATEWAY_READ_BUFFER_SIZE.
func ReadBufferSize() int { return envInt("GATEWAY_READ_BUFFER_SIZE", 32768) }

// BodyLimit is the maximum request body a public edge accepts, in bytes.
// Env GATEWAY_BODY_LIMIT.
func BodyLimit() int { return envInt("GATEWAY_BODY_LIMIT", 16<<20) }

func envInt(key string, dflt int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return dflt
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return dflt
	}
	return n
}
