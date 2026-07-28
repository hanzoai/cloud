//go:build !linux

package credz

import "net"

// Off Linux there is no SO_PEERCRED, so there is no way to tell whether the peer
// is even this deployment's own uid — and the answer to "I cannot identify the
// caller" is to grant nothing, not to fall back to something weaker. A developer
// on another platform runs the Dev posture, which needs no broker at all;
// production is Linux.
func peerPID(*net.UnixConn) (int, error) { return 0, errNoPeerCreds }
