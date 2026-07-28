//go:build !linux

package credz

import "net"

// Off Linux there is no SO_PEERCRED-plus-/proc pair, so there is no way to name
// the peer — and the answer to "I cannot identify the caller" is to grant
// nothing, not to fall back to something weaker. A developer on another platform
// runs the Dev posture, which needs no broker at all; production is Linux.
func peerPID(*net.UnixConn) (int, error) { return 0, errNoPeerCreds }
func peerArgv(int) ([]string, error)     { return nil, errNoPeerCreds }
