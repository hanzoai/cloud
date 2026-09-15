// The doors, timed by one client.
//
// Four ways to reach the same operation on the same running server, called
// round-robin so every door meets the same machine on every iteration.
//
// The fourth door is why this is Go. REST, MCP and the op-call plane are HTTP
// and a script can speak them; ZAP is not, and a Python client against three
// doors with a Go client against the fourth would put the client's language in
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
	op := flag.String("op", "tasks_list", "the operation every door asks for")
	n := flag.Int("n", 200, "iterations per door")
	warm := flag.Int("warm", 20, "warm-up iterations per door")
	flag.Parse()
	if *base == "" {
		fmt.Fprintln(os.Stderr, "doors: -base is required")
		os.Exit(2)
	}

	// One transport for all three HTTP doors, so connection reuse is not a
	// difference between them either.
	client := &http.Client{Timeout: 10 * time.Second}

	rest := func() error { return get(client, *base+"/v1/tasks") }

	mcpBody, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": *op, "arguments": map[string]any{}},
	})
	mcp := func() error { return post(client, *base+"/mcp", "application/json", mcpBody) }

	plane := func() error { return post(client, *base+zip.CallPath+*op, "application/json", nil) }

	doors := []struct {
		name string
		call func() error
	}{{"REST", rest}, {"MCP", mcp}, {"plane", plane}}

	if *zapAddr != "" {
		conn, err := zip.Dial(*zapAddr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "doors: ZAP dial %s: %v\n", *zapAddr, err)
			os.Exit(1)
		}
		in := empty{}
		zapCall := func() error {
			_, err := zip.Call[empty, tasks](context.Background(), conn, *op, &in)
			return err
		}
		doors = append(doors, struct {
			name string
			call func() error
		}{"ZAP", zapCall})
	}

	for i := 0; i < *warm; i++ {
		for _, d := range doors {
			if err := d.call(); err != nil {
				fmt.Fprintf(os.Stderr, "doors: %s warm-up: %v\n", d.name, err)
				os.Exit(1)
			}
		}
	}

	samples := map[string][]float64{}
	for i := 0; i < *n; i++ {
		for _, d := range doors {
			t := time.Now()
			if err := d.call(); err != nil {
				fmt.Fprintf(os.Stderr, "doors: %s: %v\n", d.name, err)
				os.Exit(1)
			}
			samples[d.name] = append(samples[d.name], float64(time.Since(t).Microseconds())/1000)
		}
	}

	fmt.Printf("%-10s %8s %8s %8s   (ms, n=%d, interleaved)\n", "door", "min", "p50", "p90", *n)
	for _, d := range doors {
		xs := samples[d.name]
		sort.Float64s(xs)
		fmt.Printf("%-10s %8.2f %8.2f %8.2f\n", d.name, xs[0], xs[len(xs)/2], xs[int(float64(len(xs))*0.9)-1])
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
// a new connection on the next call, which would show up as this door being
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
