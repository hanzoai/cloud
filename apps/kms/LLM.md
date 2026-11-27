# LLM.md — apps/kms

Guidance for apps/kms. Moved from the root LLM.md; cross-cutting rules stay in the root.

## A secret is read, never delivered: the kms socket attests its peer

No object in Kubernetes holds secret material. A process that needs a secret
asks the KMS socket for it at the moment of use, and the kernel says who asked.
The plane already gave every UDS handler `zip.PeerOf(ctx)` — SO_PEERCRED's
`{PID, UID, GID}`; zip v1.36.42 adds `Peer.PodUID()`, the pod the kubelet placed
that PID in, read off `/proc/<pid>/cgroup` under either cgroup driver. Neither
is a value the caller states.

`apps/kms/attest.go` turns that into an identity: a peer in a pod is that pod
— namespace `tenant-<org>` is the tenant, any other namespace is the
deployment's own with the brand as its org, the service account names the
program — and a peer outside any pod is a host process, admitted only as this
process's user or root, which is the rule the 0600 socket enforced when no other
user could connect. Pods are indexed by UID from an informer over this node's
pods (`NODE_NAME`), so the lookup on the request path is a map read. `admit`
is the one decision, pure and table-tested: the ref names a tenant or the
deployment, the caller was attested or was not, and the org the call STATES —
a header — may narrow the attestation and never widen it. A call with no
transport behind it (`zip.Local`, v1.36.43) is this process asking itself and
is the platform.

`cloud.Plugin.Shared` opens the process's plane socket to every user on the
host (`zip.Config.SocketMode` 0666, set by `Listen` before any op is declared).
Only an app whose every plane op attests its peer may set it, and today that is
kms. The socket stops being the boundary and the attestation is; the mode is
only the door — the same shape as a SPIFFE agent socket. `cmd/kmsfetch` is the
consumer for a container that reads files: it asks `plane.KMSGet` over
`$ZIP_RUNTIME_DIR/kms.sock` and writes each secret into a memory-backed
directory, presenting nothing, so the chart's `kms:` list replaces every
`KMSSecret → Secret → secretKeyRef` projection. It ships in the image as
`/kmsfetch`.

What this closes and what it does not. It closes the tenant boundary on a
socket shared across pods on ONE node — the cloud pod's own node, where pods
mount its run directory as a hostPath. A pod on another node needs a relay the
node holds: an agent that attests the pod the same way and forwards the op to
the one store over `luxfi/kms`'s signed Envelope with the node's key, the
credential a node holds as a host file the way k3s holds its node token. That
relay is the next increment; the KMSSecret CRD, the operator's projection
controller and the eight kinds of Secret Kubernetes itself imposes each have
one answer recorded in universe's kms design and go with it.
