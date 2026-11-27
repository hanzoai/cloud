package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	gogitstorage "github.com/go-git/go-git/v5/storage"
	"github.com/hanzoai/cloud/apps/git/objects"
)

// serve.go — git's smart-HTTP protocol, version 1 over stateless RPC, served from a
// repository kept in the database (objects) and not from a directory.
//
// The streaming git CLI reads a directory, and a replica with no volume has none, so the
// three exchanges a clone, a fetch and a push make are answered here:
//
//   - the ref advertisement (advertisement);
//   - the fetch: the negotiation of what the client has (serveFetch), the objects it is
//     owed (walk.go), and the pack, whose entries are the bytes the database holds
//     (objects.Pack);
//   - the push: the pack parsed into a transaction, the objects checked to be whole, and
//     each ref moved by compare and set (receive).
//
// What it does not speak: protocol version 2 (a client that asks for it is answered in
// version 1, which every client accepts), deltas in the packs it sends, and filters
// other than blob:none and blob:limit.
const (
	agentName = "hanzo-git"

	uploadCaps = "multi_ack multi_ack_detailed no-done side-band side-band-64k thin-pack ofs-delta shallow no-progress " +
		"include-tag allow-tip-sha1-in-want allow-reachable-sha1-in-want filter object-format=sha1 agent=" + agentName
	receiveCaps = "report-status delete-refs side-band-64k quiet atomic ofs-delta object-format=sha1 agent=" + agentName
)

// advertisement is the body of a ref advertisement for service, without the
// "# service=" header the HTTP framing adds.
func advertisement(repo *objects.Repo, service string) ([]byte, error) {
	refs, err := repo.References()
	if err != nil {
		return nil, err
	}
	upload := service == svcUploadPack
	caps := receiveCaps
	type line struct {
		name string
		hash plumbing.Hash
	}
	var lines []line
	if upload {
		caps = uploadCaps
		for _, r := range refs {
			if r.Name() != plumbing.HEAD || r.Type() != plumbing.SymbolicReference {
				continue
			}
			for _, t := range refs {
				if t.Name() == r.Target() && t.Type() == plumbing.HashReference {
					lines = append(lines, line{"HEAD", t.Hash()})
					caps += " symref=HEAD:" + r.Target().String()
				}
			}
		}
	}
	for _, r := range refs {
		if r.Type() == plumbing.HashReference && strings.HasPrefix(r.Name().String(), "refs/") {
			lines = append(lines, line{r.Name().String(), r.Hash()})
		}
	}
	var out bytes.Buffer
	if len(lines) == 0 {
		out.Write(packetWrite(zeroOID + " capabilities^{}\x00" + caps + "\n"))
	}
	for i, l := range lines {
		text := l.hash.String() + " " + l.name
		if i == 0 {
			text += "\x00" + caps
		}
		out.Write(packetWrite(text + "\n"))
		if upload && strings.HasPrefix(l.name, "refs/tags/") {
			if target, ok := peel(repo, l.hash); ok {
				out.Write(packetWrite(target.String() + " " + l.name + "^{}\n"))
			}
		}
	}
	out.WriteString("0000")
	return out.Bytes(), nil
}

// peel is what an annotated tag finally points at, and whether h is one.
func peel(repo *objects.Repo, h plumbing.Hash) (plumbing.Hash, bool) {
	o, err := repo.EncodedObject(plumbing.AnyObject, h)
	if err != nil || o.Type() != plumbing.TagObject {
		return h, false
	}
	for range 16 {
		t, err := object.DecodeTag(repo, o)
		if err != nil {
			return h, false
		}
		next, err := repo.EncodedObject(plumbing.AnyObject, t.Target)
		if err != nil || next.Type() != plumbing.TagObject {
			return t.Target, true
		}
		o = next
	}
	return h, false
}

// pktReader walks the pkt-lines of a request body.
type pktReader struct{ b []byte }

// next is the next packet's payload without its newline; flush is a 0000 packet.
func (p *pktReader) next() (line string, flush bool, err error) {
	if len(p.b) == 0 {
		return "", false, io.EOF
	}
	if len(p.b) < 4 {
		return "", false, io.ErrUnexpectedEOF
	}
	n, err := pktLen(p.b[:4])
	if err != nil {
		return "", false, err
	}
	if n == 0 {
		p.b = p.b[4:]
		return "", true, nil
	}
	if n < 4 || n > len(p.b) {
		return "", false, fmt.Errorf("pkt-line length %d overruns the request", n)
	}
	line = strings.TrimSuffix(string(p.b[4:n]), "\n")
	p.b = p.b[n:]
	return line, false, nil
}

// request is what a client asked for in one POST to git-upload-pack.
type request struct {
	wants, haves, shallows []plumbing.Hash
	caps                   string
	depth                  int
	filter                 string
	tags                   bool
	done                   bool
	// flushed is whether the haves ended in a flush packet, which is the client asking
	// for the round's answer; a request that stops after the wants (the exchange that
	// only asks for the shallow list) is answered with the shallow list and no more.
	flushed bool
}

func oidArg(s string) (plumbing.Hash, error) {
	if len(s) != 40 || !validOID(s) {
		return plumbing.ZeroHash, fmt.Errorf("%q is not an object id", s)
	}
	return plumbing.NewHash(s), nil
}

func parseFetch(body []byte) (*request, error) {
	p := &pktReader{b: body}
	req := &request{}
	for {
		line, flush, err := p.next()
		if err != nil {
			return nil, fmt.Errorf("fetch request: %w", err)
		}
		if flush {
			break
		}
		verb, rest, _ := strings.Cut(line, " ")
		switch verb {
		case "want":
			oid, caps, _ := strings.Cut(rest, " ")
			h, err := oidArg(oid)
			if err != nil {
				return nil, err
			}
			req.wants = append(req.wants, h)
			if req.caps == "" {
				req.caps = caps
			}
		case "shallow":
			h, err := oidArg(rest)
			if err != nil {
				return nil, err
			}
			req.shallows = append(req.shallows, h)
		case "deepen":
			n, err := strconv.Atoi(rest)
			if err != nil || n < 1 {
				return nil, fmt.Errorf("deepen %q is not a depth", rest)
			}
			req.depth = n
		case "filter":
			req.filter = rest
		default:
			return nil, fmt.Errorf("unsupported fetch argument %q", verb)
		}
	}
	for {
		line, flush, err := p.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if flush {
			req.flushed = true
			break
		}
		if err != nil {
			return nil, fmt.Errorf("fetch request: %w", err)
		}
		if line == "done" {
			req.done = true
			break
		}
		oid, ok := strings.CutPrefix(line, "have ")
		if !ok {
			return nil, fmt.Errorf("fetch request: expected a have, got %q", line)
		}
		h, err := oidArg(oid)
		if err != nil {
			return nil, err
		}
		req.haves = append(req.haves, h)
	}
	req.tags = hasCap(req.caps, "include-tag")
	return req, nil
}

// errMalformed is a request that is not the protocol: the client's fault, 400.
var errMalformed = errors.New("malformed fetch request")

// errPacket is a response of one fatal line, which is how git says a fetch cannot be served.
func errPacket(msg string) io.ReadCloser {
	return io.NopCloser(bytes.NewReader(packetWrite("ERR upload-pack: " + msg + "\n")))
}

// serveFetch answers one POST to git-upload-pack. The negotiation is git's own
// (upload-pack.c, get_common_commits): each have the repository holds is acknowledged;
// once every want has a common ancestor the server says it is ready, and the pack is
// sent when the client says done or, with no-done, as soon as the server is ready.
func serveFetch(ctx context.Context, objs *objects.Store, id string, body []byte) (io.ReadCloser, error) {
	// The repository is read after this returns, by the goroutine that writes the pack,
	// so its context is its own and the stream's Close ends it.
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	streaming := false
	defer func() {
		if !streaming {
			cancel()
		}
	}()
	repo := objs.Repo(ctx, id)
	req, err := parseFetch(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errMalformed, err)
	}
	if len(req.wants) == 0 {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	if err := checkFilter(req.filter); err != nil {
		return errPacket(err.Error()), nil
	}
	if missing, err := repo.Absent(req.wants); err != nil {
		return nil, err
	} else if len(missing) > 0 {
		return errPacket("not our ref " + missing[0].String()), nil
	}
	all := req.haves
	lacking, err := repo.Absent(all)
	if err != nil {
		return nil, err
	}
	absent := make(map[plumbing.Hash]struct{}, len(lacking))
	for _, h := range lacking {
		absent[h] = struct{}{}
	}
	req.haves = slices.DeleteFunc(slices.Clone(all), func(h plumbing.Hash) bool { _, no := absent[h]; return no })

	var pre bytes.Buffer
	var pl *plan
	if req.depth > 0 {
		if pl, err = req.plan(repo); err != nil {
			return errPacket(err.Error()), nil
		}
		for _, h := range pl.shallow {
			pre.Write(packetWrite("shallow " + h.String() + "\n"))
		}
		for _, h := range pl.unshallow {
			pre.Write(packetWrite("unshallow " + h.String() + "\n"))
		}
		pre.WriteString("0000")
	}

	multi := hasCap(req.caps, "multi_ack_detailed")
	simple := !multi && hasCap(req.caps, "multi_ack")
	var common []plumbing.Hash
	var last plumbing.Hash
	ready, gotOther := false, false
	cached, enough := -1, false
	giveUp := func() bool {
		if cached != len(common) {
			cached, enough = len(common), canGiveUp(repo, req.wants, common)
		}
		return enough
	}
	for _, h := range all {
		if _, no := absent[h]; !no {
			common = append(common, h)
			last = h
			switch {
			case multi:
				pre.Write(packetWrite("ACK " + h.String() + " common\n"))
			case simple:
				pre.Write(packetWrite("ACK " + h.String() + " continue\n"))
			case len(common) == 1:
				pre.Write(packetWrite("ACK " + h.String() + "\n"))
			}
			continue
		}
		gotOther = true
		if (multi || simple) && giveUp() {
			if multi {
				ready = true
				pre.Write(packetWrite("ACK " + h.String() + " ready\n"))
			} else {
				pre.Write(packetWrite("ACK " + h.String() + " continue\n"))
			}
		}
	}

	send := false
	if req.done {
		switch {
		case len(common) == 0:
			pre.Write(packetWrite("NAK\n"))
		case multi || simple:
			pre.Write(packetWrite("ACK " + last.String() + "\n"))
		}
		send = true
	} else if req.flushed {
		if multi && len(common) > 0 && !gotOther && giveUp() {
			ready = true
			pre.Write(packetWrite("ACK " + last.String() + " ready\n"))
		}
		if len(common) == 0 || multi || simple {
			pre.Write(packetWrite("NAK\n"))
		}
		if hasCap(req.caps, "no-done") && ready {
			pre.Write(packetWrite("ACK " + last.String() + "\n"))
			send = true
		}
	}
	if !send {
		return io.NopCloser(&pre), nil
	}
	if pl == nil {
		if pl, err = req.plan(repo); err != nil {
			return errPacket(err.Error()), nil
		}
	}

	band := 0
	switch {
	case hasCap(req.caps, "side-band-64k"):
		band = 65515
	case hasCap(req.caps, "side-band"):
		band = 995
	}
	// The pack is written by a goroutine into a pipe the HTTP response reads, so a
	// clone is never held in memory; closing the stream ends the goroutine, whether
	// the client finished or went away.
	streaming = true
	pr, pw := io.Pipe()
	go func() {
		defer cancel()
		_, err := pw.Write(pre.Bytes())
		if err == nil {
			var w io.Writer = pw
			var bands *bandWriter
			if band > 0 {
				bands = &bandWriter{w: pw, max: band}
				w = bands
			}
			if err = repo.Pack(ctx, w, pl.oids); err == nil && bands != nil {
				err = bands.Close()
			}
			if err != nil && bands != nil {
				bands.fatal(err)
			}
		}
		_ = pw.CloseWithError(err)
	}()
	return &stream{pr: pr, cancel: cancel}, nil
}

// stream is the pack on its way to the client.
type stream struct {
	pr     *io.PipeReader
	cancel context.CancelFunc
	onEnd  func()
}

func (s *stream) Read(p []byte) (int, error) { return s.pr.Read(p) }

func (s *stream) Close() error {
	s.cancel()
	err := s.pr.Close()
	if s.onEnd != nil {
		s.onEnd()
	}
	return err
}

// bandWriter frames what is written to it as side-band packets on band 1, max bytes of
// payload each, which is how a client that asked for side-band reads a pack.
type bandWriter struct {
	w   io.Writer
	max int
	buf []byte
}

func (b *bandWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		k := min(len(p), b.max-len(b.buf))
		b.buf = append(b.buf, p[:k]...)
		p = p[k:]
		if len(b.buf) == b.max {
			if err := b.emit(1, b.buf); err != nil {
				return 0, err
			}
			b.buf = b.buf[:0]
		}
	}
	return n, nil
}

func (b *bandWriter) emit(band byte, payload []byte) error {
	out := make([]byte, 0, len(payload)+5)
	out = append(out, fmt.Sprintf("%04x", len(payload)+5)...)
	out = append(out, band)
	out = append(out, payload...)
	_, err := b.w.Write(out)
	return err
}

// Close sends what is held and the flush that ends the stream.
func (b *bandWriter) Close() error {
	if len(b.buf) > 0 {
		if err := b.emit(1, b.buf); err != nil {
			return err
		}
		b.buf = nil
	}
	_, err := io.WriteString(b.w, "0000")
	return err
}

// fatal tells the client, on band 3, why the stream stops. It is best effort: the
// client may be gone.
func (b *bandWriter) fatal(err error) {
	_ = b.emit(3, []byte("upload-pack: "+err.Error()+"\n"))
}

// canGiveUp is git's ok_to_give_up: every wanted commit has a commit the client has
// somewhere in its history, so the pack the server would send is the difference and not
// the history. Commits older than the oldest the client has are not looked into, and
// neither are more than a bounded number.
func canGiveUp(repo *objects.Repo, wants, common []plumbing.Hash) bool {
	if len(common) == 0 {
		return false
	}
	have := map[plumbing.Hash]struct{}{}
	oldest := time.Time{}
	for _, h := range common {
		have[h] = struct{}{}
		if c, err := object.GetCommit(repo, h); err == nil && (oldest.IsZero() || c.Committer.When.Before(oldest)) {
			oldest = c.Committer.When
		}
	}
	const budget = 5000
	for _, w := range wants {
		o, err := repo.EncodedObject(plumbing.AnyObject, w)
		if err != nil {
			return false
		}
		if o.Type() == plumbing.TagObject {
			target, _ := peel(repo, w)
			if o, err = repo.EncodedObject(plumbing.AnyObject, target); err != nil {
				return false
			}
			w = target
		}
		if o.Type() != plumbing.CommitObject {
			continue
		}
		found := false
		seen := map[plumbing.Hash]struct{}{w: {}}
		for queue := []plumbing.Hash{w}; len(queue) > 0 && len(seen) < budget && !found; {
			h := queue[0]
			queue = queue[1:]
			if _, ok := have[h]; ok {
				found = true
				break
			}
			c, err := object.GetCommit(repo, h)
			if err != nil || c.Committer.When.Before(oldest) {
				continue
			}
			for _, p := range c.ParentHashes {
				if _, ok := seen[p]; !ok {
					seen[p] = struct{}{}
					queue = append(queue, p)
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// receive applies one push to repository id in a single transaction: the pack is parsed
// into it, the objects are checked to be whole, and each ref moves by compare and set,
// in name order so two pushes that share refs lock them in the same order. The
// transaction commits if any ref moved (all of them, with the atomic capability) and the
// rest is rolled back with it. A ref that moved since the client read it is refused as
// non-fast-forward, which is what the client would have been told had it asked a moment
// later; two replicas pushing one ref cannot both win.
//
// The report is git's report-status. An error is the server's fault, the database; a
// push the client got wrong (a broken pack, a hole in its history, a ref name git does
// not allow) is a report that says why.
func receive(ctx context.Context, objs *objects.Store, id string, raw []byte, cmds []refCommand, n int, caps string) (report []byte, landed []refCommand, err error) {
	reasons := make([]string, len(cmds))
	refuseAll := func(unpack, reason string) ([]byte, []refCommand, error) {
		for i := range reasons {
			reasons[i] = reason
		}
		return pushReport(unpack, cmds, reasons, caps), nil, nil
	}
	tips := map[plumbing.ReferenceName]plumbing.Hash{}
	for i, c := range cmds {
		name := plumbing.ReferenceName(c.Ref)
		if err := name.Validate(); err != nil || c.Creates() && c.Deletes() {
			reasons[i] = "funny refname"
			continue
		}
		if !c.Deletes() {
			tips[name] = plumbing.NewHash(c.New)
		}
	}
	var incoming *objects.Incoming
	if len(tips) > 0 {
		pack := raw[n:]
		if limit, err := parseSize(receiveMaxInputSize()); err == nil && int64(len(pack)) > limit {
			return refuseAll("pack exceeds the maximum allowed size", "unpacker error")
		}
		if len(pack) == 0 {
			return refuseAll("eof before pack header", "unpacker error")
		}
		// The pack is read and uploaded before the repository is locked: a large upload
		// holds neither a connection nor the repository, and a pack that is refused or
		// loses the race is garbage the collector removes, never visible.
		incoming, err = objs.Ingest(ctx, id, bytes.NewReader(pack))
		if err != nil {
			if ctx.Err() != nil {
				return nil, nil, ctx.Err()
			}
			if errors.Is(err, objects.ErrBadPack) || errors.Is(err, objects.ErrTooLarge) {
				return refuseAll(oneLine(err), "unpacker error")
			}
			return nil, nil, err
		}
	}
	tx, err := objs.Begin(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if incoming != nil {
		if err := tx.Attach(incoming); err != nil {
			return nil, nil, err
		}
		if err := tx.Connected(tips); err != nil {
			if errors.Is(err, objects.ErrMissing) || errors.Is(err, objects.ErrNotCommit) {
				return refuseAll("ok", "missing necessary objects")
			}
			return nil, nil, err
		}
	}

	order := make([]int, len(cmds))
	for i := range order {
		order[i] = i
	}
	slices.SortFunc(order, func(a, b int) int { return strings.Compare(cmds[a].Ref, cmds[b].Ref) })
	moved := 0
	for _, i := range order {
		if reasons[i] != "" {
			continue
		}
		c := cmds[i]
		name := plumbing.ReferenceName(c.Ref)
		var err error
		switch {
		case c.Creates():
			err = tx.CreateReference(plumbing.NewHashReference(name, plumbing.NewHash(c.New)))
		case c.Deletes():
			err = tx.DeleteReference(plumbing.NewHashReference(name, plumbing.NewHash(c.Old)))
		default:
			err = tx.CheckAndSetReference(plumbing.NewHashReference(name, plumbing.NewHash(c.New)),
				plumbing.NewHashReference(name, plumbing.NewHash(c.Old)))
		}
		switch {
		case err == nil:
			moved++
		case errors.Is(err, gogitstorage.ErrReferenceHasChanged):
			// The ref is not where the client read it: another push moved it between.
			switch {
			case c.Deletes():
				reasons[i] = "failed to delete"
			case c.Creates():
				reasons[i] = "failed to update ref"
			default:
				reasons[i] = "non-fast-forward"
			}
		default:
			return nil, nil, err
		}
	}
	if hasCap(caps, "atomic") && moved < len(cmds) {
		for i := range reasons {
			if reasons[i] == "" {
				reasons[i] = "atomic push failed"
			}
		}
		moved = 0
	}
	if moved == 0 {
		return pushReport("ok", cmds, reasons, caps), nil, nil
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, err
	}
	for i, c := range cmds {
		if reasons[i] == "" {
			landed = append(landed, c)
		}
	}
	return pushReport("ok", cmds, reasons, caps), landed, nil
}

// oneLine is err as a line of a report: report-status has no room for a second.
func oneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}
