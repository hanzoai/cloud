// The transports, timed by one client.
//
// Four ways to reach the same operation on the same running server, called
// round-robin so every transport meets the same machine on every iteration.
//
// The fourth transport is why this is Go. REST, MCP and the op-call plane are HTTP
// and a script can speak them; ZAP is not, and a Python client against three
// transports with a Go client against the fourth would put the client's language in
// the comparison. One client, four envelopes, and the difference between the
// rows is the envelope.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/zap-proto/zip"
)

type empty struct{}

// The reply, as much of it as a timing needs. zapenc decodes into the type the
// caller declares and wants a struct, so this names the one field `tasks_list`
// answers with and nothing else: the lane measures the round trip, not the
// list.
type tasks struct {
	Tasks []struct{} `json:"tasks"`
}

func main() {
	base := flag.String("base", "", "http base, e.g. http://127.0.0.1:18086")
	zapAddr := flag.String("zap", "", "ZAP address: host:port for tcp, or a path for unix")
	zapName := flag.String("zap-name", "ZAP", "what to call the ZAP row")
	op := flag.String("op", "tasks_list", "the operation every transport asks for")
	n := flag.Int("n", 200, "iterations per transport")
	warm := flag.Int("warm", 20, "warm-up iterations per transport")
	jsonOut := flag.String("json", "", "also append the rows to this file, as JSON")
	flag.Parse()
	if *base == "" {
		fmt.Fprintln(os.Stderr, "transports: -base is required")
		os.Exit(2)
	}

	// One http.Client, shared by all three HTTP transports, so connection reuse
	// is not a difference between them either.
	client := &http.Client{Timeout: 10 * time.Second}

	rest := func() error { return get(client, *base+"/v1/tasks") }

	mcpBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": *op, "arguments": map[string]any{}},
	})
	mcp := func() error { return post(client, *base+"/mcp", "application/json", mcpBody) }

	plane := func() error { return post(client, *base+zip.CallPath+*op, "application/json", nil) }

	transports := []struct {
		name string
		call func() error
	}{{"REST", rest}, {"MCP", mcp}, {"plane", plane}}

	if *zapAddr != "" {
		conn, err := zip.Dial(*zapAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "transports: ZAP dial %s: %v\n", *zapAddr, err)
			os.Exit(1)
		}
		in := empty{}
		zapCall := func() error {
			_, err := zip.Call[empty, tasks](context.Background(), conn, *op, &in)
			return err
		}
		transports = append(transports, struct {
			name string
			call func() error
		}{*zapName, zapCall})
	}

	for i := 0; i < *warm; i++ {
		for _, d := range transports {
			if err := d.call(); err != nil {
				fmt.Fprintf(os.Stderr, "transports: %s warm-up: %v\n", d.name, err)
				os.Exit(1)
			}
		}
	}

	samples := map[string][]float64{}
	for i := 0; i < *n; i++ {
		for _, d := range transports {
			t := time.Now()
			if err := d.call(); err != nil {
				fmt.Fprintf(os.Stderr, "transports: %s: %v\n", d.name, err)
				os.Exit(1)
			}
			samples[d.name] = append(samples[d.name], float64(time.Since(t).Microseconds())/1000)
		}
	}

	fmt.Printf("%-12s %8s %8s %8s   (ms, n=%d, interleaved)\n", "transport", "min", "p50", "p90", *n)
	type row struct {
		Transport string  `json:"transport"`
		Min       float64 `json:"min_ms"`
		P50       float64 `json:"p50_ms"`
		P90       float64 `json:"p90_ms"`
		N         int     `json:"n"`
	}
	var rows []row
	for _, d := range transports {
		xs := samples[d.name]
		sort.Float64s(xs)
		min, p50, p90 := xs[0], xs[len(xs)/2], xs[int(float64(len(xs))*0.9)-1]
		fmt.Printf("%-12s %8.2f %8.2f %8.2f\n", d.name, min, p50, p90)
		rows = append(rows, row{d.name, min, p50, p90, *n})
	}

	// Appended, not written: a run measures the ZAP tcp phase and the unix one
	// separately, and both belong to the same table.
	if *jsonOut != "" {
		var all []row
		if b, err := os.ReadFile(*jsonOut); err == nil {
			_ = json.Unmarshal(b, &all)
		}
		all = append(all, rows...)
		b, err := json.MarshalIndent(all, "", " ")
		if err == nil {
			err = os.WriteFile(*jsonOut, b, 0o644)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "transports: writing %s: %v\n", *jsonOut, err)
			os.Exit(1)
		}
	}
}

func get(c *http.Client, url string) error {
	res, err := c.Get(url)
	if err != nil {
		return err
	}
	return drain(res)
}

func post(c *http.Client, url, ct string, body []byte) error {
	res, err := c.Post(url, ct, bytes.NewReader(body))
	if err != nil {
		return err
	}
	return drain(res)
}

// Read and close, so the connection returns to the pool. A body left unread is
// a new connection on the next call, which would show up as this transport being
// slower than the ones whose bodies were drained.
func drain(res *http.Response) error {
	_, err := io.Copy(io.Discard, res.Body)
	res.Body.Close()
	if err != nil {
		return err
	}
	if res.StatusCode >= 400 {
		return fmt.Errorf("status %d", res.StatusCode)
	}
	return nil
}
