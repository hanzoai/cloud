// Copyright 2023-2026 Hanzo AI Inc. All Rights Reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// See the License for the specific language governing permissions and
// limitations under the License.

package event

// Reading a session replay back, for the operator's player.
//
// The write half is replay.go: one Kafka message per rrweb batch, which the replay
// ingester turns into snappy BLOCKS in object storage and one summary row per block
// in the insights warehouse — block_urls naming each as
// s3://<bucket>/<key>?range=bytes=<first>-<last>. A block decompresses to
// newline-delimited JSON, one [windowId, eventWithTime] pair per line, which is
// exactly what an rrweb player replays. So reading one back is: list the session's
// blocks, read each byte range, decompress, split.
//
// A block that will not decode — the ingester seals blocks when its keystore is
// not cleartext — is reported as such and skipped, never guessed at.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/hanzoai/cloud/sdk/object"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/s2"
)

// replayBucket is the bucket the replay ingester writes blocks to (insights'
// SESSION_RECORDING_V2_S3_BUCKET). The block url comes off a warehouse row, and
// the credential reading it can read any bucket, so a block anywhere else is
// refused rather than followed.
const replayBucket = "hanzo-sessions"

const (
	// maxFrameBytes bounds the frames one answer carries. A recording is read
	// whole into the answer, so this is also what one read can cost the process.
	maxFrameBytes = 32 << 20
	// maxBlockBytes bounds one compressed block. The ingester flushes a block at a
	// few MiB; a range past this is not a block.
	maxBlockBytes = 64 << 20
	// maxPlainBytes bounds what one block may decompress to. A block states its own
	// decoded length up front, so it is checked before anything is allocated.
	maxPlainBytes = 256 << 20
)

// block is one stored block as the replay summary lists it.
type block struct {
	start, end time.Time
	url        string
}

// readBlock reads one byte range of one object. A var so a test serves the bytes;
// production is the process's object store (sdk/object).
var readBlock = func(ctx context.Context, bucket, key string, first, last int64) ([]byte, error) {
	body, err := object.Default().Open(ctx, bucket, key, object.Get{Ranged: true, First: first, Last: last})
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(io.LimitReader(body, maxBlockBytes))
}

// blocksOf zips the three parallel arrays blocksSQL aggregates into blocks, oldest
// first. Arrays of unequal length cannot be paired without guessing which time
// belongs to which block, so they are refused (ok false) rather than cut.
func blocksOf(rows []map[string]any) (blocks []block, ok bool) {
	if len(rows) == 0 {
		return nil, true
	}
	starts, ends, urls := timesOf(rows[0]["starts"]), timesOf(rows[0]["ends"]), stringsOf(rows[0]["urls"])
	if len(starts) != len(urls) || len(ends) != len(urls) {
		return nil, false
	}
	out := make([]block, 0, len(urls))
	for i := range urls {
		out = append(out, block{start: starts[i], end: ends[i], url: urls[i]})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].start.Before(out[j].start) })
	return out, true
}

// replay reads every block in order and decodes its frames, stopping at
// maxFrameBytes. Each block reports whether it was read; one that was not costs the
// player that stretch of the recording, never the rest of it.
func replay(ctx context.Context, blocks []block) ([]replayBlock, []replayFrame, bool) {
	report := make([]replayBlock, 0, len(blocks))
	frames := []replayFrame{}
	size := 0
	for _, b := range blocks {
		r := replayBlock{Start: nanos(b.start), End: nanos(b.end)}
		if size >= maxFrameBytes {
			r.Error = "not read: the answer is full"
			report = append(report, r)
			continue
		}
		got, err := readFrames(ctx, b.url)
		if err != nil {
			r.Error = err.Error()
			report = append(report, r)
			continue
		}
		for _, f := range got {
			if size += len(f.Event); size > maxFrameBytes {
				break
			}
			frames = append(frames, f)
			r.Frames++
		}
		report = append(report, r)
	}
	return report, frames, size > maxFrameBytes
}

// readFrames reads and decodes one block.
func readFrames(ctx context.Context, raw string) ([]replayFrame, error) {
	bucket, key, first, last, err := blockAddress(raw)
	if err != nil {
		return nil, err
	}
	if bucket != replayBucket {
		return nil, fmt.Errorf("refused: block is outside the replay bucket")
	}
	b, err := readBlock(ctx, bucket, key, first, last)
	if err != nil {
		return nil, fmt.Errorf("unreadable: %w", err)
	}
	return decodeBlock(b)
}

// blockAddress parses s3://<bucket>/<key>?range=bytes=<first>-<last>.
func blockAddress(raw string) (bucket, key string, first, last int64, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "s3" || u.Host == "" || strings.Trim(u.Path, "/") == "" {
		return "", "", 0, 0, fmt.Errorf("unreadable: %q is not a block address", raw)
	}
	span, ok := strings.CutPrefix(u.Query().Get("range"), "bytes=")
	lo, hi, cut := strings.Cut(span, "-")
	if !ok || !cut {
		return "", "", 0, 0, fmt.Errorf("unreadable: block address names no byte range")
	}
	first, err1 := strconv.ParseInt(lo, 10, 64)
	last, err2 := strconv.ParseInt(hi, 10, 64)
	if err1 != nil || err2 != nil || first < 0 || last < first || last-first >= maxBlockBytes {
		return "", "", 0, 0, fmt.Errorf("unreadable: block byte range %q", span)
	}
	return u.Host, strings.TrimPrefix(u.Path, "/"), first, last, nil
}

// decodeBlock decompresses one block and splits it into frames. A block that is
// not snappy, or whose lines are not [window, event] pairs, is sealed or corrupt —
// either way not a recording this can play — and says so.
func decodeBlock(b []byte) ([]replayFrame, error) {
	if n, err := s2.DecodedLen(b); err != nil || n > maxPlainBytes {
		return nil, fmt.Errorf("sealed: block is not a cleartext snappy block")
	}
	plain, err := s2.Decode(nil, b)
	if err != nil {
		return nil, fmt.Errorf("sealed: block is not a cleartext snappy block")
	}
	var out []replayFrame
	for line := range bytes.SplitSeq(plain, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var pair []json.RawMessage
		if json.Unmarshal(line, &pair) != nil || len(pair) != 2 {
			return nil, fmt.Errorf("sealed: block line is not a [window, event] pair")
		}
		var window string
		_ = json.Unmarshal(pair[0], &window)
		out = append(out, replayFrame{Window: window, Event: pair[1]})
	}
	return out, nil
}

// timesOf coerces an array column of DateTime64 across the shapes a transport
// surfaces.
func timesOf(v any) []time.Time {
	switch t := v.(type) {
	case []time.Time:
		return t
	case []any:
		out := make([]time.Time, len(t))
		for i, e := range t {
			out[i] = aTime(e)
		}
		return out
	}
	return nil
}

// stringsOf coerces an array column of String.
func stringsOf(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, len(t))
		for i, e := range t {
			out[i] = asStr(e)
		}
		return out
	}
	return nil
}
