// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Command preblelocalsim runs the scenario matrix from
// research/preblelocality (hanzoai/cloud#35, HIP-1345 §2.3) and prints a
// summary table, optionally writing the raw JSON results to a file.
package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	pl "github.com/hanzoai/cloud/research/preblelocality"
)

func main() {
	out := flag.String("out", "", "write raw JSON results to this file")
	flag.Parse()

	results := pl.RunMatrix()

	if *out != "" {
		b, err := pl.MatrixJSON(results)
		if err != nil {
			fmt.Fprintln(os.Stderr, "marshal:", err)
			os.Exit(1)
		}
		if err := os.WriteFile(*out, b, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, "write:", err)
			os.Exit(1)
		}
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "scenario\tpolicy\tseed\tp50 TTFT(s)\tp99 TTFT(s)\treuse\tgpu util\tgoodput(tok/s)\treqs")
	for _, m := range results {
		fmt.Fprintf(w, "%s\t%s\t%d\t%.2f\t%.2f\t%.3f\t%.3f\t%.0f\t%d\n",
			m.Scenario, m.Policy, m.Seed,
			m.TTFTP50, m.TTFTP99, m.ReuseRatio, m.GPUUtil, m.GoodputTPS, m.RequestsArrived)
	}
	if err := w.Flush(); err != nil {
		fmt.Fprintln(os.Stderr, "flush:", err)
		os.Exit(1)
	}
}
