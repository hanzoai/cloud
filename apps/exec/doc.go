// Package exec is the code interpreter: run a snippet in a sandbox, and move files
// in and out of the session that sandbox IS.
//
// # A session is a sandbox
//
// That is the whole design, and it is why this subsystem now holds no store, no
// session table and no lifetime of its own. The upstream contract hands a
// `session_id` back on every reply and takes it back on later calls; a sandbox has
// an id, a lease and files. They are the same object, so `session_id` IS the
// sandbox id and everything else falls out:
//
//	POST /v1/exec            lease the session's sandbox → write the program →
//	                         run it → report what it printed and what it wrote
//	POST /v1/exec/upload     write into the session's sandbox
//	GET  /v1/exec/download/{sid}/{id}  read one file back out
//	GET  /v1/exec/files/{sid}     list what the session holds
//
// Nothing here ends a lease. apps/sandbox's reaper does, on the two clocks it
// already keeps — the ttl the lease was taken for, and an hour of nobody touching
// it. Deleting the sandbox at the end of a run was the obvious other design and it
// is wrong: the reply carries a session id, and the contract's next three calls
// address it. A sandbox torn down at the end of POST /v1/exec would answer 404 to
// every download of the plot that run had just made.
//
// # What was here before
//
// A reverse proxy to code-exec.hanzo.svc.cluster.local:8000, and that Service has
// had ZERO endpoints for 33 days: /v1/exec has been answering 503 in production the
// whole time. The proxy was not failing to reach the executor — there was no
// executor. Pointing it somewhere better was never the fix, because the thing being
// pointed at did not exist; the fix is that cloud already runs the one compute
// primitive, and this subsystem composes over it.
//
// # The wire is NOT ours
//
// hanzo.chat drives this through @hanzochat/agents' CodeExecutor and its own
// Files/Code client, so the shapes below are MEASURED from those two callers rather
// than designed here — including the details that are easy to get subtly wrong:
// download is addressed by TWO segments (`{session_id}/{fileId}`, crud.js), the
// file listing answers a BARE JSON ARRAY of {name, lastModified} whose `name` is
// that same two-segment identifier (process.js getSessionInfo), and upload answers
// {message:"success", session_id, files:[{fileId, filename}]} and is checked for
// `message === 'success'` before anything else is read.
//
// # Auth
//
// IAM, and nothing else. A caller reaches this surface with a validated principal
// and the sandboxes are that principal's org's; a caller without one is refused.
//
// It used to also accept an opaque service key on X-API-Key, enforced here against
// CODE_EXEC_API_KEY, and a request bearing it acted for the deployment's own brand
// org. That was this subsystem authenticating callers on its own — a second way in,
// with its own credential to distribute, rotate and leak, deciding tenancy for a
// caller IAM had never seen. The chat server forwards the end user's IAM bearer
// (crud.js codeAuthHeaders); that is now the only door, so a caller it cannot
// resolve an identity for is one that cannot run code.
package exec
