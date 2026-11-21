// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Command tetragonharness is the CLI for the cloud#33 research harness:
//
//	tetragonharness escape   — run the escape-attempt matrix under a mode
//	tetragonharness bench    — syscall-throughput microbenchmark pass
//	tetragonharness combine  — merge baseline/monitor/enforcement samples
//	tetragonharness payload  — internal: run an escape/control payload
//	tetragonharness list     — print the probe matrix
//
// See research/tetragonenforcement/results/PROTOCOL.md for the full
// measurement protocol and research/tetragonenforcement/README.md for the
// research note.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	te "github.com/hanzoai/cloud/research/tetragonenforcement"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: tetragonharness <escape|bench|combine|payload|list> [flags]`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "escape":
		cmdEscape(os.Args[2:])
	case "bench":
		cmdBench(os.Args[2:])
	case "combine":
		cmdCombine(os.Args[2:])
	case "payload":
		if len(os.Args) < 3 {
			usage()
		}
		os.Exit(te.RunPayload(os.Args[2]))
	case "list":
		cmdList()
	default:
		usage()
	}
}

func cmdList() {
	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "id\tkind\tpayload\tpolicy\texpect blocked\trequires linux")
	for _, p := range te.ProbeMatrix() {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\t%v\n", p.ID, p.Kind, p.Payload, p.PolicyFile, p.ExpectBlocked, p.RequiresLinux)
	}
	w.Flush()
}

func cmdEscape(args []string) {
	fs := flag.NewFlagSet("escape", flag.ExitOnError)
	out := fs.String("out", "", "write the RunResult JSON to this file")
	mode := fs.String("mode", string(te.ModeNone), "tetragon mode in effect: none|monitor|enforcement")
	probe := fs.String("probe", "", "run a single probe id instead of the whole matrix")
	timeoutMS := fs.Int("timeout", 10000, "per-probe timeout in ms")
	ver := fs.String("tetragon-version", "", "tetragon agent version in effect (recorded verbatim)")
	quiet := fs.Bool("quiet", false, "print only the summary line")
	if err := fs.Parse(args); err != nil {
		usage()
	}

	opts := te.RunOptions{
		Mode:            te.RunMode(*mode),
		TimeoutPerProbe: time.Duration(*timeoutMS) * time.Millisecond,
		TetragonVersion: *ver,
		TailLimit:       4096,
	}
	probes := te.ProbeMatrix()
	if *probe != "" {
		var found []te.Probe
		for _, p := range probes {
			if p.ID == *probe {
				found = append(found, p)
			}
		}
		if len(found) == 0 {
			fmt.Fprintf(os.Stderr, "unknown probe %q (see 'tetragonharness list')\n", *probe)
			os.Exit(2)
		}
		probes = found
	}

	run := te.RunResult{
		RunID:      te.NewRunID(opts.Mode, time.Now()),
		StartedUTC: time.Now().UTC(),
		Mode:       *mode,
	}
	for _, p := range probes {
		res := te.RunProbe(useContext(), p, opts)
		run.Probes = append(run.Probes, res)
		if !*quiet {
			line := fmt.Sprintf("%-24s %-10s exit=%-4d dur=%5dms %s",
				res.ProbeID, res.Status, res.ExitCode, res.DurationMS, res.Signal)
			fmt.Println(line)
		}
	}
	run.Summary = te.Summarize(*mode, run.Probes)
	fmt.Printf("summary: %s\n", run.Summary.String())

	if *out != "" {
		b, err := te.MarshalResult(run)
		if err != nil {
			fmt.Fprintln(os.Stderr, "marshal:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		fmt.Println("wrote", *out)
	}

	// CI gate: a sandbox configuration passes only with zero leaks, zero
	// false positives and zero probe errors (skipped probes are fine —
	// they mean the host OS cannot run that payload at all).
	if run.Summary.Leaked > 0 || run.Summary.FalsePositives > 0 || run.Summary.Errors > 0 {
		os.Exit(1)
	}
}

func cmdBench(args []string) {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	out := fs.String("out", "", "write the BenchSample JSON array to this file")
	workload := fs.String("workload", string(te.BenchOpenClose), "open-close|loopback-connect")
	mode := fs.String("mode", string(te.ModeNone), "none|monitor|enforcement")
	secs := fs.Float64("secs", 10, "benchmark duration in seconds")
	workers := fs.Int("workers", 8, "concurrent workers")
	if err := fs.Parse(args); err != nil {
		usage()
	}

	ctx, cancel := contextWithTimeout(*secs)
	defer cancel()
	s, err := te.RunBench(ctx, te.BenchConfig{
		Workload: te.BenchWorkload(*workload),
		Mode:     te.RunMode(*mode),
		Duration: time.Duration(*secs * float64(time.Second)),
		Workers:  *workers,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "bench:", err)
		os.Exit(1)
	}
	fmt.Printf("%s mode=%s: %d ops in %.1fs -> %.0f ops/sec (%.0f ns/op)\n",
		s.Workload, s.Mode, s.Ops, s.DurationS, s.OpsPerSec, s.NsPerOp)
	if *out != "" {
		b, err := json.MarshalIndent([]te.BenchSample{s}, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "marshal:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
		fmt.Println("wrote", *out)
	}
}

func cmdCombine(args []string) {
	fs := flag.NewFlagSet("combine", flag.ExitOnError)
	baselinePath := fs.String("baseline", "", "bench samples JSON from mode=none")
	monitorPath := fs.String("monitor", "", "bench samples JSON from mode=monitor")
	enforcementPath := fs.String("enforcement", "", "bench samples JSON from mode=enforcement")
	out := fs.String("out", "", "write the merged BenchReports JSON here")
	ver := fs.String("tetragon-version", "", "tetragon agent version in effect")
	if err := fs.Parse(args); err != nil {
		usage()
	}
	read := func(path string) []te.BenchSample {
		if path == "" {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			os.Exit(1)
		}
		var arr []te.BenchSample
		if err := json.Unmarshal(b, &arr); err == nil {
			return arr
		}
		var one te.BenchSample
		if err := json.Unmarshal(b, &one); err == nil {
			return []te.BenchSample{one}
		}
		fmt.Fprintln(os.Stderr, "unrecognized bench sample file:", path)
		os.Exit(1)
		return nil
	}

	reports := te.BuildBenchReports(read(*baselinePath), read(*monitorPath), read(*enforcementPath))
	result := te.BenchReports{RunID: te.NewRunID(te.ModeNone, time.Now()), StartedUTC: time.Now().UTC(), Workloads: reports}
	result.Host = te.GatherHostInfo(*ver)
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal:", err)
		os.Exit(1)
	}
	if *out == "" {
		fmt.Println(string(b))
		return
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	fmt.Println("wrote", *out)
}
