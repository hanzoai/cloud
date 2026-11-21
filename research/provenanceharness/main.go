// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

// Command provenanceharness is the CLI for the cloud#39 research harness:
//
//	provenanceharness sign     — sign a fixture document with a private key (PEM)
//	provenanceharness verify   — verify a signed document against a public key (PEM)
//	provenanceharness bench    — verify+digest throughput measurement
//	provenanceharness genkeys  — write a fresh fixture keypair (pub.pem committed, priv kept)
//
// See research/provenanceverification/results/PROTOCOL.md for the full
// measurement protocol and research/provenanceverification/README.md for
// the research note.
package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"time"

	pv "github.com/hanzoai/cloud/research/provenanceverification"
)

func usage() {
	fmt.Fprintln(os.Stderr, `usage: provenanceharness <sign|verify|bench|genkeys> [flags]`)
	os.Exit(2)
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "sign":
		cmdSign(os.Args[2:])
	case "verify":
		cmdVerify(os.Args[2:])
	case "bench":
		cmdBench(os.Args[2:])
	case "genkeys":
		cmdGenKeys(os.Args[2:])
	default:
		usage()
	}
}

// readPEMKey loads an ed25519 key (public or private) from a PEM file.
func readPEMKey(path string, wantPrivate bool) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(b)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM block", path)
	}
	if wantPrivate {
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%s: parse pkcs8: %w", path, err)
		}
		ed, ok := k.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%s: not an Ed25519 private key", path)
		}
		return ed, nil
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("%s: parse pkix: %w", path, err)
	}
	ed, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%s: not an Ed25519 public key", path)
	}
	return ed, nil
}

func cmdSign(args []string) {
	fs := flag.NewFlagSet("sign", flag.ExitOnError)
	keyPath := fs.String("key", "results/fixtures/priv.pem", "PEM private key (PKCS#8), NOT committed")
	out := fs.String("out", "", "write the signed document JSON here")
	if err := fs.Parse(args); err != nil {
		usage()
	}
	priv, err := readPEMKey(*keyPath, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load key:", err)
		os.Exit(1)
	}
	d := pv.FixtureDocument()
	if err := d.Sign(ed25519.PrivateKey(priv)); err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
	b, err := d.Marshal()
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal:", err)
		os.Exit(1)
	}
	if *out == "" {
		os.Stdout.Write(b)
		fmt.Println()
		return
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	fmt.Println("signed document written:", *out)
}

func cmdVerify(args []string) {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	keyPath := fs.String("key", "results/fixtures/pub.pem", "PEM public key (PKIX)")
	docPath := fs.String("doc", "", "signed document JSON to verify")
	artifact := fs.String("artifact", "", "artifact file to digest-check against the document")
	if err := fs.Parse(args); err != nil {
		usage()
	}
	pub, err := readPEMKey(*keyPath, false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load key:", err)
		os.Exit(1)
	}
	if *docPath == "" {
		fmt.Fprintln(os.Stderr, "verify: -doc is required")
		os.Exit(2)
	}
	b, err := os.ReadFile(*docPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "read doc:", err)
		os.Exit(1)
	}
	d, err := pv.UnmarshalDocument(b)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse doc:", err)
		os.Exit(1)
	}
	start := time.Now()
	if err := d.Verify(ed25519.PublicKey(pub)); err != nil {
		fmt.Fprintln(os.Stderr, "VERIFY REJECTED:", err)
		os.Exit(1)
	}
	latency := time.Since(start)
	fmt.Printf("signature: OK (%s)\n", latency)
	if *artifact != "" {
		ab, err := os.ReadFile(*artifact)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read artifact:", err)
			os.Exit(1)
		}
		if err := d.CheckArtifactDigest(ab); err != nil {
			fmt.Fprintln(os.Stderr, "ARTIFACT REJECTED:", err)
			os.Exit(1)
		}
		fmt.Println("artifact digest: OK")
	}
}

func cmdBench(args []string) {
	fs := flag.NewFlagSet("bench", flag.ExitOnError)
	iters := fs.Int("iters", 2000, "verification iterations")
	if err := fs.Parse(args); err != nil {
		usage()
	}
	d := pv.FixtureDocument()
	pub, priv := pv.NewTestKeys()
	if err := d.Sign(priv); err != nil {
		fmt.Fprintln(os.Stderr, "sign:", err)
		os.Exit(1)
	}
	artifact := []byte("fixture-artifact-bytes")
	for i := 0; i < 200; i++ { // warmup
		_ = d.Verify(pub)
		_ = d.CheckArtifactDigest(artifact)
	}
	start := time.Now()
	for i := 0; i < *iters; i++ {
		_ = d.Verify(pub)
		_ = d.CheckArtifactDigest(artifact)
	}
	elapsed := time.Since(start)
	perOp := elapsed / time.Duration(*iters)
	fmt.Printf("verify+digest: %d iters in %s -> %s/op\n", *iters, elapsed, perOp)
}

func cmdGenKeys(args []string) {
	fs := flag.NewFlagSet("genkeys", flag.ExitOnError)
	dir := fs.String("dir", "results/fixtures", "output directory")
	if err := fs.Parse(args); err != nil {
		usage()
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "keygen:", err)
		os.Exit(1)
	}
	pubPEM, err := pv.MarshalPublicKeyPEM(pub)
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal pub:", err)
		os.Exit(1)
	}
	derPriv, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal priv:", err)
		os.Exit(1)
	}
	privPEM := []byte(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: derPriv}))
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, "mkdir:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*dir+"/pub.pem", pubPEM, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write pub:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*dir+"/priv.pem", privPEM, 0o600); err != nil {
		fmt.Fprintln(os.Stderr, "write priv:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %s/pub.pem (commit this) and %s/priv.pem (NEVER commit this)\n", *dir, *dir)
}
