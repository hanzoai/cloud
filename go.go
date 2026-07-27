package cloud

import luxlog "github.com/luxfi/log"

// Go runs fn on a new goroutine with its panic CONTAINED.
//
// This is the one spawn helper for background work in the shared binary, and it
// exists because of an asymmetry that is easy to miss: middleware.Recover() wraps
// only the goroutine serving the request. A panic on any goroutine that a handler
// SPAWNS is unrecovered, and an unrecovered panic does not fail that request — it
// takes the process down, and with it every tenant and every subsystem in the
// binary. A malformed page fetched during one org's search would stop everyone's
// chat.
//
// The surface is large and fed by untrusted input: web pages we fetch and parse,
// documents we decode, payloads from platforms we bridge. Those are exactly the
// paths most likely to panic and least likely to be exercised in tests.
//
// name is what appears in the log if fn panics — make it identify the work
// ("crawl.fetch", "answer.read"), not the call site, because that is what someone
// reads at 3am. fields are appended verbatim, so pass the org or job id that makes
// the line actionable; never pass a secret.
//
// It deliberately does NOT return an error, wait, or restart. A caller that needs a
// result uses a channel; a caller that needs bounding takes a limiter FIRST and
// releases it in fn (register the release defer before calling Go, so it survives
// the panic). Containment is this helper's only job.
func Go(log luxlog.Logger, name string, fields []any, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				if log != nil {
					args := append([]any{"work", name, "panic", r}, fields...)
					log.Error("background work panicked (recovered; process kept alive)", args...)
				}
			}
		}()
		fn()
	}()
}

// GoBase is Go for a caller that already holds a Base — the common case inside a
// subsystem, where the logger is s.Base.Log and repeating it at every call site is
// the kind of noise that makes people skip the helper.
func (b *Base) Go(name string, fields []any, fn func()) {
	if b == nil {
		Go(nil, name, fields, fn)
		return
	}
	Go(b.Log, name, fields, fn)
}
