package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	var err error
	switch cmd {
	case "inventory":
		err = runInventory(args)
	case "preflight":
		err = runPreflight(args)
	case "reseal":
		err = runReseal(args)
	case "verify":
		err = runVerify(args)
	case "runbook":
		printRunbook()
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "kmsreseal: unknown subcommand %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "kmsreseal %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `kmsreseal — CR-driven KMS re-seal migration (#79)

usage:
  kmsreseal inventory --crs <file|-> [--kubectl] [--only-host <url>]
  kmsreseal preflight --cloud <url> [--src <url>]
  kmsreseal reseal    --crs <file> --src <url> --cloud <url> [--only-host <url>] [--plan]
  kmsreseal verify    --crs <file> --src <url> --cloud <url> [--only-host <url>]
  kmsreseal runbook

Secret plaintext transits memory only — never written to disk, never logged.
`)
}

// readCRSource loads CR JSON from a file path, "-" (stdin), or the live cluster
// via kubectl when kubectlMode is set. It never fetches secret VALUES — a
// KMSSecret CR carries only coordinates (org/path/env/key names) + references.
func readCRSource(path string, kubectlMode bool) ([]cr, error) {
	if kubectlMode {
		return loadCRsFromKubectl()
	}
	if path == "" {
		return nil, fmt.Errorf("--crs <file|-> is required (or --kubectl)")
	}
	var data []byte
	var err error
	if path == "-" {
		data, err = os.ReadFile("/dev/stdin")
	} else {
		data, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fmt.Errorf("read CR source: %w", err)
	}
	return parseCRs(data)
}
