package infer

// planestream.go — a streamed completion across the peer plane.
//
// A plane op is one request and one reply, so a completion asked for with
// "stream": true cannot ride ai_chat and still arrive a delta at a time. Its op,
// ai_chat_stream, authorises like ai_chat and answers a unix socket instead (the
// shape of sandbox_stream); the model API's answer is written to that socket as it
// is produced, and the caller reads it as the HTTP response go-openai expects.
//
// The socket carries frames, [u32 LE length][kind][data], so the price can follow
// the body without being mistaken for it:
//
//	h  {"status":N,"contentType":"…"}   once, first
//	b  bytes of the body, as written     any number
//	t  {"billed":N} or {}               once, last
//
// THE PRICE IS TOLD ONCE, AT THE END. The trailer carries what the model API told
// the call's Bill, and the reading side tells it to the caller's Bill (BillOf) when
// it reads the trailer, so the meter that made the call settles it exactly once,
// as it settles a buffered completion. A reader that stops early (its client hung
// up) still drains to the trailer on Close: the model was asked, the vendor is
// paid, and the call is charged what it cost, never nothing.

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

const (
	frameHead  = 'h'
	frameBody  = 'b'
	frameTrail = 't'

	// maxFrame bounds one frame, so a corrupt length cannot ask for gigabytes.
	maxFrame = 16 << 20
)

// drainFor is how long Close waits for the trailer of a stream its reader left.
var drainFor = 2 * time.Minute

func writeFrame(w io.Writer, kind byte, data []byte) error {
	var head [5]byte
	binary.LittleEndian.PutUint32(head[:4], uint32(len(data)+1))
	head[4] = kind
	if _, err := w.Write(head[:]); err != nil {
		return err
	}
	if len(data) == 0 {
		return nil
	}
	_, err := w.Write(data)
	return err
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var head [5]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	n := binary.LittleEndian.Uint32(head[:4])
	if n == 0 || n > maxFrame {
		return 0, nil, fmt.Errorf("infer: plane stream frame of %d bytes", n)
	}
	data := make([]byte, n-1)
	if _, err := io.ReadFull(r, data); err != nil {
		return 0, nil, err
	}
	return head[4], data, nil
}

type streamHead struct {
	Status      int    `json:"status"`
	ContentType string `json:"contentType,omitempty"`
}

type streamTrail struct {
	Billed  *int64 `json:"billed,omitempty"`
	Covered bool   `json:"covered,omitempty"`
	Cash    bool   `json:"cash,omitempty"`
}

// IsStream reports whether a completion request asks for its answer streamed.
func IsStream(body []byte) bool {
	var r struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &r) == nil && r.Stream
}

// StreamWriter is the http.ResponseWriter a model API answers a streamed plane call
// into: each write leaves as a frame at once, so Flush has nothing to do. End
// writes the trailer.
type StreamWriter struct {
	w      io.Writer
	header http.Header

	mu    sync.Mutex
	wrote bool
	err   error
}

// NewStreamWriter frames a plane stream's answer onto w.
func NewStreamWriter(w io.Writer) *StreamWriter {
	return &StreamWriter{w: w, header: http.Header{}}
}

func (s *StreamWriter) Header() http.Header { return s.header }

func (s *StreamWriter) WriteHeader(code int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.head(code)
}

// head writes the head frame once; the caller holds s.mu.
func (s *StreamWriter) head(code int) {
	if s.wrote {
		return
	}
	s.wrote = true
	b, _ := json.Marshal(streamHead{Status: code, ContentType: s.header.Get("Content-Type")})
	s.fail(writeFrame(s.w, frameHead, b))
}

func (s *StreamWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.head(http.StatusOK)
	if s.err != nil {
		return 0, s.err
	}
	if len(p) > 0 {
		s.fail(writeFrame(s.w, frameBody, p))
	}
	if s.err != nil {
		return 0, s.err
	}
	return len(p), nil
}

// Flush is a no-op: every write is already on the socket.
func (s *StreamWriter) Flush() {}

// End writes the trailer: the price the call was told, when it was told one.
func (s *StreamWriter) End(nanos int64, told bool) error {
	return s.EndBilled(nanos, told, false)
}

// EndBilled writes the trailer with the billed price and whether only prepaid
// cash may pay for it.
func (s *StreamWriter) EndBilled(nanos int64, told, cash bool) error {
	var t streamTrail
	if told {
		t.Billed = &nanos
		t.Cash = cash
	}
	return s.end(t)
}

// EndCovered writes a trailer marking the streamed call as paid by the caller's
// plan or a free cap, so the meter releases its hold without debiting the wallet.
func (s *StreamWriter) EndCovered() error {
	return s.end(streamTrail{Covered: true})
}

func (s *StreamWriter) end(t streamTrail) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.head(http.StatusOK)
	b, _ := json.Marshal(t)
	s.fail(writeFrame(s.w, frameTrail, b))
	return s.err
}

// Err is the first write that failed: the reader is gone.
func (s *StreamWriter) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

func (s *StreamWriter) fail(err error) {
	if err != nil && s.err == nil {
		s.err = err
	}
}

// StreamResponse reads a plane stream from rc as the HTTP response it carries. The
// body yields the model API's bytes as they arrive; the trailer's price is told to
// the Bill r's context carries, once.
func StreamResponse(rc io.ReadCloser, r *http.Request) (*http.Response, error) {
	kind, data, err := readFrame(rc)
	if err == nil && kind != frameHead {
		err = fmt.Errorf("infer: a plane stream began with frame %q", kind)
	}
	var h streamHead
	if err == nil {
		err = json.Unmarshal(data, &h)
	}
	if err != nil {
		_ = rc.Close()
		return nil, fmt.Errorf("infer: read the head of a plane stream: %w", err)
	}
	ct := h.ContentType
	if ct == "" {
		ct = "application/json"
	}
	return &http.Response{
		Status:        fmt.Sprintf("%d %s", h.Status, http.StatusText(h.Status)),
		StatusCode:    h.Status,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        http.Header{"Content-Type": {ct}},
		Body:          &streamBody{rc: rc, bill: BillOf(r.Context())},
		ContentLength: -1,
		Request:       r,
	}, nil
}

// streamBody is a plane stream's body frames, read as one body.
type streamBody struct {
	rc   io.ReadCloser
	bill *Bill

	buf    []byte
	ended  bool
	err    error
	closed bool
}

func (b *streamBody) Read(p []byte) (int, error) {
	for len(b.buf) == 0 {
		if b.ended {
			return 0, io.EOF
		}
		if b.err != nil {
			return 0, b.err
		}
		b.next()
	}
	n := copy(p, b.buf)
	b.buf = b.buf[n:]
	return n, nil
}

// next reads one frame: a body frame fills buf, the trailer tells the price and
// ends the body.
func (b *streamBody) next() {
	kind, data, err := readFrame(b.rc)
	switch {
	case err != nil:
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF // the answer ended before its trailer
		}
		b.err = err
	case kind == frameBody:
		b.buf = data
	case kind == frameTrail:
		b.ended = true
		var t streamTrail
		if json.Unmarshal(data, &t) == nil {
			if t.Covered {
				b.bill.Cover()
			} else if t.Billed != nil {
				b.bill.Tell(*t.Billed)
				if t.Cash {
					b.bill.SetCash()
				}
			}
		}
	default:
		b.err = fmt.Errorf("infer: unexpected plane stream frame %q", kind)
	}
}

// Close reads a stream its reader left to its trailer, so what the call cost is
// told, and closes the socket.
func (b *streamBody) Close() error {
	if b.closed {
		return nil
	}
	b.closed = true
	if d, ok := b.rc.(interface{ SetReadDeadline(time.Time) error }); ok && !b.ended {
		_ = d.SetReadDeadline(time.Now().Add(drainFor))
	}
	for !b.ended && b.err == nil {
		b.buf = nil
		b.next()
	}
	return b.rc.Close()
}
