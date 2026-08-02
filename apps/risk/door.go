package risk

// door.go is the one place a value this app cannot price is refused.
//
// WHY IT IS ONE MIDDLEWARE AND NOT A RULE PER FIELD. Every bound in bound.go is
// a count multiplied by textMax — 1,278 keys, 8,192 list entries, 1,024 memoised
// agency answers — and that multiplication is only arithmetic if NO value can be
// longer than textMax. A per-field check would have to be written again for the
// next field, and the field it was not written for is the one that reopens the
// hole: a subject id, a signal value, an agent reference, a rule term and a path
// segment all end up in the same rings, the same maps and the same rows.
//
// So the door reads the REQUEST, not the shape: every string in the body, every
// path segment, every query value, and the body's own length. Whatever an op
// adds tomorrow arrives through here.
//
// IT REFUSES, IT DOES NOT TRUNCATE. A truncated identifier is a different
// subject silently sharing a counter with the one that was asked about, which is
// the same cross-key confusion the tenant boundary exists to prevent, one layer
// down.

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"

	"github.com/zap-proto/zip"
)

// door refuses any request carrying a value longer than textMax, or a body
// larger than bodyMax, before a typed op ever sees it.
func door() zip.Handler {
	return func(c *zip.Ctx) error {
		for _, seg := range strings.Split(c.Path(), "/") {
			if len(seg) > textMax {
				return zip.ErrBadRequest(errLong("a path segment").Error())
			}
		}
		for k, v := range c.Fiber().Queries() {
			if len(k) > textMax || len(v) > textMax {
				return zip.ErrBadRequest(errLong("query parameter " + trim(k)).Error())
			}
		}
		body := c.Body()
		if len(body) > bodyMax {
			return zip.Errorf(413, "this request is %d bytes; this plane accepts at most %d", len(body), bodyMax)
		}
		if len(body) == 0 {
			return c.Next()
		}
		if err := admitJSON(body); err != nil {
			return zip.ErrBadRequest(err.Error())
		}
		return c.Next()
	}
}

// admitJSON walks the body as a token stream and refuses the first over-long
// string, whether it is a key or a value.
//
// A TOKEN WALK, NOT A DECODE. Decoding into `any` would allocate the whole
// document — including the very strings being refused — which is the cost the
// cap exists to prevent. The stream reads one token at a time and returns on the
// first offender.
//
// A body that is not JSON is not this door's business: the typed op's own bind
// answers for that, and refusing here would turn every malformed-body 400 into a
// message about a length nobody exceeded.
func admitJSON(body []byte) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return nil // not JSON, or truncated: the op's bind is the authority
		}
		if s, ok := tok.(string); ok && len(s) > textMax {
			return errLong("a value in this request (" + trim(s) + "…)")
		}
	}
}

// trim renders enough of an offending name to find it without echoing the whole
// of what was refused back onto the wire.
func trim(s string) string {
	const shown = 32
	if len(s) <= shown {
		return s
	}
	return s[:shown]
}
