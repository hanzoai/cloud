// Package sandbox is the ONE compute primitive: a sandbox is a gVisor pod that
// runs somebody else's code, and every lifetime is the same object.
//
//	a function invoke      = a sandbox with a seconds-long lease
//	a code-exec call       = a sandbox with a session lease
//	an agentic coding run  = a sandbox with a project volume and a long lease
//
// Not three subsystems, not three schedulers. One record, one pod spec, one way
// in. What differs between them is `ttlSec` and whether a volume is attached.
//
//	POST   /v1/sandbox             {kind:"sandbox", class, project?, ttlSec?} -> Sandbox
//	GET    /v1/sandbox             ?kind=&project=&status=
//	GET    /v1/sandbox/:id
//	DELETE /v1/sandbox/:id         ?purge=1 drops the volume too
//	POST   /v1/sandbox/:id/exec    {argv|command, stdin?, timeoutSec?} -> {exitCode,stdout,stderr}
//	GET    /v1/sandbox/:id/fs      ?path=  read a file, or list a directory
//	POST   /v1/sandbox/:id/fs      ?path=  write a file
//	POST   /v1/sandbox/:id/terminal/ticket a single-use ticket for one terminal
//	GET    /v1/sandbox/:id/terminal        ?ticket=&arg=  the terminal, as a page
//	GET    /v1/sandbox/:id/terminal/ws     ?ticket=&arg=  the terminal, as a socket
//
// A RUN IS WATCHABLE AND IT IS STOPPABLE. Name a session on a run and the
// command's output is appended to that session's live log as it is produced, so
// a surface reading GET /v1/agent/sessions/stream watches the work happen
// instead of a blank pause; `stop_run` interrupts what a sandbox is running and
// leaves the sandbox leased, because a run that went wrong is one somebody still
// wants to look at. See work.go — both are the same fact, that a command in
// flight is addressable.
//
// THERE IS EXACTLY ONE WAY INTO A SANDBOX, and it is the Kubernetes exec
// subresource. fs read/list/write are not a second channel — they are `cat`,
// `ls` and `tee` over that one channel, which is also how `kubectl cp` has
// always worked. The predecessor shipped an in-pod HTTP daemon with eleven
// endpoints, a shared pool-wide API key and a pod-IP address book; all three are
// gone, because Kubernetes already had the channel and we were re-implementing
// it badly.
//
// A SANDBOX IS ADDRESSED BY POD NAME THROUGH THE APISERVER, NEVER BY IP. That is
// not a style preference, it is the fix for a real cross-tenant read: a pod that
// dies on its own — evicted, OOM-killed, drained — never runs a release path, so
// a row keeps a Host the CNI has since handed to another tenant's replacement
// pod, and a call to that address is served by a stranger with every credential
// checking out. A pod NAME is minted per sandbox and never reused, so the same
// mistake cannot be spelled. The header-stamping protocol that was invented to
// defend the IP scheme is deleted along with the scheme.
//
// NOTHING RUNS IN THIS PACKAGE. There is no os/exec here and there must never
// be. It creates Kubernetes objects and streams bytes to the apiserver; the work
// happens inside the pod, under runsc, on the far side of a runtime boundary.
//
// THERE IS NO POOL. A sandbox is created for a lease and deleted at its end.
// Claiming from a warm pool is what forced the recycle, the pod-IP address book
// and the label-patch race arbitration; it also grows, inevitably, into a
// scheduler beside Kubernetes' own. If a warm pool is ever wanted, it is a
// Deployment an operator sizes — not a manager in this process.
//
// AUTH is the ordinary one: IAM terminates identity at the edge and this package
// reads principal.Org. It never mints a credential, and no caller credential
// reaches a sandbox — the pod carries no service-account token at all.
package sandbox
