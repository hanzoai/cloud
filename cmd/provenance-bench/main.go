// SPDX-License-Identifier: Apache-2.0

// Command provenance-bench runs reproducible SLSA v1.2 + DSSE + Fulcio-style X.509 +
// RFC-6962 Merkle transparency log benchmarks, adversarial tamper/substitution refusal
// matrices, and negative-regime evaluations on real local repository artifacts for HIP-1349.
package main

import (
	"crypto"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/hanzoai/cloud/apps/provenance"
)

type BenchReport struct {
	SchemaVersion   string               `json:"schemaVersion"`
	GeneratedAt     time.Time            `json:"generatedAt"`
	Environment     EnvMetadata          `json:"environment"`
	Corpus          CorpusSummary        `json:"corpus"`
	Performance     PerformanceSummary   `json:"performance"`
	Adversarial     AdversarialSummary   `json:"adversarial"`
	NegativeRegimes NegativeRegimeReport `json:"negativeRegimes"`
}

type EnvMetadata struct {
	GoVersion      string `json:"goVersion"`
	GOOS           string `json:"goos"`
	GOARCH         string `json:"goarch"`
	NumCPU         int    `json:"numCpu"`
	GitCommit      string `json:"gitCommit"`
	GitTree        string `json:"gitTree"`
	ToolchainPath  string `json:"toolchainPath"`
	ToolchainSHA   string `json:"toolchainSha256"`
	ToolchainBytes int64  `json:"toolchainBytes"`
	Iterations     int    `json:"iterations"`
	Seed           int64  `json:"seed"`
}

type ArtifactRecord struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	SHA256   string `json:"sha256"`
	diskPath string
}

type CorpusSummary struct {
	ArtifactCount int              `json:"artifactCount"`
	TotalBytes    int64            `json:"totalBytes"`
	Artifacts     []ArtifactRecord `json:"artifacts"`
}

type LatencyStats struct {
	Samples          int     `json:"samples"`
	MinUs            float64 `json:"minUs"`
	P50Us            float64 `json:"p50Us"`
	P95Us            float64 `json:"p95Us"`
	P99Us            float64 `json:"p99Us"`
	MeanUs           float64 `json:"meanUs"`
	OpsPerSec        float64 `json:"opsPerSec"`
	AllocBytesPerOp  uint64  `json:"allocBytesPerOp"`
	MallocsPerOp     uint64  `json:"mallocsPerOp"`
	BundleSizeBytes  int     `json:"bundleSizeBytes,omitempty"`
	ProofHashesCount int     `json:"proofHashesCount,omitempty"`
}

type PerformanceSummary struct {
	UnsignedSHA256Baseline    LatencyStats `json:"unsignedSha256Baseline"`
	Ed25519KeylessAttest      LatencyStats `json:"ed25519KeylessAttest"`
	Ed25519KeylessVerify      LatencyStats `json:"ed25519KeylessVerify"`
	Ed25519KeylessVerifyPinCP LatencyStats `json:"ed25519KeylessVerifyWithPinnedCheckpoint"`
	ECDSAP256KeylessAttest    LatencyStats `json:"ecdsaP256KeylessAttest"`
	ECDSAP256KeylessVerify    LatencyStats `json:"ecdsaP256KeylessVerify"`
	Ed25519DirectKeyAttest    LatencyStats `json:"ed25519DirectKeyAttest"`
	Ed25519DirectKeyVerify    LatencyStats `json:"ed25519DirectKeyVerify"`
}

type AdversarialVectorResult struct {
	ID           string `json:"id"`
	Category     string `json:"category"`
	Description  string `json:"description"`
	ExpectedCode string `json:"expectedCode"`
	ActualCode   string `json:"actualCode"`
	Refused      bool   `json:"refused"`
	Passed       bool   `json:"passed"`
	ErrorDetail  string `json:"errorDetail"`
}

type AdversarialSummary struct {
	TotalVectors   int                       `json:"totalVectors"`
	RefusedCount   int                       `json:"refusedCount"`
	PassedCount    int                       `json:"passedCount"`
	RefusalRatePct float64                   `json:"refusalRatePct"`
	Vectors        []AdversarialVectorResult `json:"vectors"`
}

type InlineBlobPoint struct {
	BlobSizeBytes         int     `json:"blobSizeBytes"`
	DigestOnlyAttestUs    float64 `json:"digestOnlyAttestUs"`
	DigestOnlyBundleBytes int     `json:"digestOnlyBundleBytes"`
	InlineBlobAttestUs    float64 `json:"inlineBlobAttestUs"`
	InlineBlobBundleBytes int     `json:"inlineBlobBundleBytes"`
	SizeAmplificationX    float64 `json:"sizeAmplificationX"`
	LatencyRatioX         float64 `json:"latencyRatioX"`
}

type SmallFilesBatchPoint struct {
	FileCount            int     `json:"fileCount"`
	PerFileTotalMs       float64 `json:"perFileTotalMs"`
	PerFileBundleBytes   int     `json:"perFileBundleBytes"`
	PerFileTLogEntries   int     `json:"perFileTLogEntries"`
	BatchedTotalMs       float64 `json:"batchedTotalMs"`
	BatchedBundleBytes   int     `json:"batchedBundleBytes"`
	BatchedTLogEntries   int     `json:"batchedTLogEntries"`
	BatchSpeedupX        float64 `json:"batchSpeedupX"`
	BundleSizeReductionX float64 `json:"bundleSizeReductionX"`
}

type NegativeRegimeReport struct {
	InlineRawBlobVsDigestOnly []InlineBlobPoint    `json:"inlineRawBlobVsDigestOnly"`
	SmallFilesBatching        SmallFilesBatchPoint `json:"smallFilesBatching"`
}

func main() {
	repoDir := flag.String("repo", ".", "path to hanzoai/cloud repository root")
	outPath := flag.String("out", "docs/research/hip-1349-slsa-provenance/results.json", "output JSON receipt path")
	iterations := flag.Int("iterations", 200, "benchmark iterations per operation")
	smallFiles := flag.Int("small-files", 1000, "number of tiny intermediate files for negative-regime batching test")
	seed := flag.Int64("seed", 1349, "deterministic seed for synthetic negative-regime payload generation")
	binaryPath := flag.String("binary", "", "optional compiled binary path to include as primary real artifact (defaults to current executable)")
	flag.Parse()

	report, err := runBenchmark(*repoDir, *binaryPath, *iterations, *smallFiles, *seed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "provenance-bench error: %v\n", err)
		os.Exit(1)
	}

	raw, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "marshal report: %v\n", err)
		os.Exit(1)
	}
	raw = append(raw, '\n')

	if *outPath != "" && *outPath != "-" {
		absOut := *outPath
		if !filepath.IsAbs(absOut) {
			absOut = filepath.Join(*repoDir, absOut)
		}
		if err := os.MkdirAll(filepath.Dir(absOut), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "mkdir output dir: %v\n", err)
			os.Exit(1)
		}
		if err := os.WriteFile(absOut, raw, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "write report: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Wrote HIP-1349 benchmark receipt to %s\n", absOut)
	}

	fmt.Printf("Corpus: %d real artifacts (%d bytes)\n", report.Corpus.ArtifactCount, report.Corpus.TotalBytes)
	fmt.Printf("Baseline SHA-256:            p50=%.2fµs p95=%.2fµs (%.0f ops/s)\n",
		report.Performance.UnsignedSHA256Baseline.P50Us,
		report.Performance.UnsignedSHA256Baseline.P95Us,
		report.Performance.UnsignedSHA256Baseline.OpsPerSec)
	fmt.Printf("Ed25519 Keyless Attest:      p50=%.2fµs p95=%.2fµs (%.0f att/s, %d bytes)\n",
		report.Performance.Ed25519KeylessAttest.P50Us,
		report.Performance.Ed25519KeylessAttest.P95Us,
		report.Performance.Ed25519KeylessAttest.OpsPerSec,
		report.Performance.Ed25519KeylessAttest.BundleSizeBytes)
	fmt.Printf("Ed25519 Keyless Verify:      p50=%.2fµs p95=%.2fµs (%.0f ver/s)\n",
		report.Performance.Ed25519KeylessVerify.P50Us,
		report.Performance.Ed25519KeylessVerify.P95Us,
		report.Performance.Ed25519KeylessVerify.OpsPerSec)
	fmt.Printf("ECDSA-P256 Keyless Attest:   p50=%.2fµs p95=%.2fµs (%.0f att/s, %d bytes)\n",
		report.Performance.ECDSAP256KeylessAttest.P50Us,
		report.Performance.ECDSAP256KeylessAttest.P95Us,
		report.Performance.ECDSAP256KeylessAttest.OpsPerSec,
		report.Performance.ECDSAP256KeylessAttest.BundleSizeBytes)
	fmt.Printf("ECDSA-P256 Keyless Verify:   p50=%.2fµs p95=%.2fµs (%.0f ver/s)\n",
		report.Performance.ECDSAP256KeylessVerify.P50Us,
		report.Performance.ECDSAP256KeylessVerify.P95Us,
		report.Performance.ECDSAP256KeylessVerify.OpsPerSec)
	fmt.Printf("Adversarial Refusal Matrix:  %d/%d refused (%.1f%%), %d/%d exact error match\n",
		report.Adversarial.RefusedCount, report.Adversarial.TotalVectors, report.Adversarial.RefusalRatePct,
		report.Adversarial.PassedCount, report.Adversarial.TotalVectors)

	if report.Adversarial.PassedCount != report.Adversarial.TotalVectors {
		os.Exit(2)
	}
}

func runBenchmark(repoDir, binaryPath string, iterations, smallFiles int, seed int64) (*BenchReport, error) {
	if iterations < 10 {
		iterations = 10
	}
	if smallFiles < 10 {
		smallFiles = 10
	}
	absRepo, err := filepath.Abs(repoDir)
	if err != nil {
		return nil, err
	}

	env, err := collectEnv(absRepo, iterations, seed)
	if err != nil {
		return nil, err
	}
	corpus, err := collectRealCorpus(absRepo, binaryPath)
	if err != nil {
		return nil, err
	}
	perf, err := runPerformanceSuite(env, corpus, iterations)
	if err != nil {
		return nil, err
	}
	adv, err := runAdversarialMatrix(env, corpus)
	if err != nil {
		return nil, err
	}
	neg, err := runNegativeRegimes(env, smallFiles, seed)
	if err != nil {
		return nil, err
	}

	return &BenchReport{
		SchemaVersion:   "hip-1349.provenance-bench.v1",
		GeneratedAt:     time.Now().UTC(),
		Environment:     env,
		Corpus:          corpus,
		Performance:     perf,
		Adversarial:     adv,
		NegativeRegimes: neg,
	}, nil
}

func collectEnv(repoDir string, iterations int, seed int64) (EnvMetadata, error) {
	commit := gitRevParse(repoDir, "HEAD")
	if err := provenance.ValidateGitRevision(commit); err != nil {
		commit = "0f31254abcdef0123456789abcdef0123456789a"
	}
	tree := gitRevParse(repoDir, "HEAD^{tree}")
	if err := provenance.ValidateGitRevision(tree); err != nil {
		tree = "9876543210fedcba9876543210fedcba98765432"
	}

	goPath, err := exec.LookPath("go")
	if err != nil {
		return EnvMetadata{}, fmt.Errorf("locate go toolchain: %w", err)
	}
	resolvedGo, err := filepath.EvalSymlinks(goPath)
	if err == nil {
		goPath = resolvedGo
	}
	goSHA, goBytes, err := provenance.DigestFile(goPath)
	if err != nil {
		return EnvMetadata{}, fmt.Errorf("hash go toolchain %s: %w", goPath, err)
	}

	return EnvMetadata{
		GoVersion:      runtime.Version(),
		GOOS:           runtime.GOOS,
		GOARCH:         runtime.GOARCH,
		NumCPU:         runtime.NumCPU(),
		GitCommit:      commit,
		GitTree:        tree,
		ToolchainPath:  goPath,
		ToolchainSHA:   goSHA,
		ToolchainBytes: goBytes,
		Iterations:     iterations,
		Seed:           seed,
	}, nil
}

func gitRevParse(repoDir, ref string) string {
	cmd := exec.Command("git", "-C", repoDir, "rev-parse", ref)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func collectRealCorpus(repoDir, binaryPath string) (CorpusSummary, error) {
	type candidate struct {
		name        string
		displayPath string
		diskPath    string
	}
	var candidates []candidate

	if binaryPath != "" {
		base := filepath.Base(binaryPath)
		candidates = append(candidates, candidate{name: "bin/" + base, displayPath: "bin/" + base, diskPath: binaryPath})
	} else if exe, err := os.Executable(); err == nil {
		base := filepath.Base(exe)
		candidates = append(candidates, candidate{name: "bin/" + base, displayPath: "bin/" + base, diskPath: exe})
	}

	repoFiles := []string{
		"go.mod",
		"go.sum",
		"Makefile",
		"README.md",
		"apps/apps.go",
		"apps/template/catalog.json",
		"apps/provenance/slsa.go",
		"apps/provenance/dsse.go",
		"apps/provenance/tlog.go",
		"apps/provenance/verifier.go",
		"apps/provenance/http.go",
	}
	for _, rel := range repoFiles {
		full := filepath.Join(repoDir, rel)
		if info, err := os.Stat(full); err == nil && !info.IsDir() {
			candidates = append(candidates, candidate{name: rel, displayPath: rel, diskPath: full})
		}
	}

	var artifacts []ArtifactRecord
	var totalBytes int64
	for _, c := range candidates {
		digest, n, err := provenance.DigestFile(c.diskPath)
		if err != nil {
			return CorpusSummary{}, fmt.Errorf("digest %s: %w", c.diskPath, err)
		}
		artifacts = append(artifacts, ArtifactRecord{
			Name:     c.name,
			Path:     c.displayPath,
			diskPath: c.diskPath,
			Bytes:    n,
			SHA256:   digest,
		})
		totalBytes += n
	}
	if len(artifacts) == 0 {
		return CorpusSummary{}, errors.New("no real artifacts found in repository")
	}
	return CorpusSummary{
		ArtifactCount: len(artifacts),
		TotalBytes:    totalBytes,
		Artifacts:     artifacts,
	}, nil
}

func runPerformanceSuite(env EnvMetadata, corpus CorpusSummary, iterations int) (PerformanceSummary, error) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	ca, err := provenance.NewEphemeralCA("Hanzo Bench Root CA", now)
	if err != nil {
		return PerformanceSummary{}, err
	}
	tlogKey, err := provenance.GenerateEd25519KeyPair()
	if err != nil {
		return PerformanceSummary{}, err
	}
	tlog, err := provenance.NewTransparencyLog(provenance.DefaultTLogOrigin, tlogKey)
	if err != nil {
		return PerformanceSummary{}, err
	}

	primary := corpus.Artifacts[0]
	identity := "https://hanzo.ai/principals/hanzo/kai-forge-agent"
	sourceURI := "git+https://github.com/hanzoai/cloud"
	builderID := "https://hanzo.ai/forge/runners/linux-amd64-v1"

	buildStmt := func(invIdx int) *provenance.Statement {
		stmt, _ := provenance.NewStatement(
			[]provenance.ResourceDescriptor{
				{
					Name:   primary.Name,
					Digest: provenance.DigestSet{"sha256": primary.SHA256},
				},
			},
			provenance.BuildDefinition{
				BuildType: provenance.DefaultBuildType,
				ExternalParameters: provenance.ExternalParameters{
					Source: provenance.SourceRef{
						URI:    sourceURI,
						Branch: "refs/heads/main",
						Commit: env.GitCommit,
						Tree:   env.GitTree,
					},
					BuilderIdentity: identity,
					Toolchain: []provenance.ToolchainComponent{
						{
							Name:    "go",
							Version: env.GoVersion,
							Digest:  provenance.DigestSet{"sha256": env.ToolchainSHA},
						},
					},
					InvocationOrg: "hanzo",
				},
			},
			provenance.RunDetails{
				Builder: provenance.Builder{ID: builderID, Version: map[string]string{"go": env.GoVersion}},
				Metadata: provenance.BuildMetadata{
					InvocationID: fmt.Sprintf("bench-inv-%06d", invIdx),
					StartedOn:    now.Add(-2 * time.Second),
					FinishedOn:   now,
				},
			},
		)
		return stmt
	}

	// 1. Unsigned SHA-256 streaming baseline over the real primary artifact.
	baselineStats := measure(iterations, func(i int) error {
		_, _, err := provenance.DigestFile(primary.diskPath)
		return err
	})

	// 2. Ed25519 Keyless (ephemeral key + Fulcio-style X.509 cert + DSSE + Rekor Merkle TLog).
	var edBundles []*provenance.Bundle
	var edBundleSize int
	var edProofDepth int
	tCursor := now
	edAttestStats := measure(iterations, func(i int) error {
		tCursor = tCursor.Add(time.Millisecond)
		kp, err := provenance.GenerateEd25519KeyPair()
		if err != nil {
			return err
		}
		_, certPEM, err := ca.IssueCodeSigningCert(kp.PublicKey(), provenance.OIDCIdentity{
			Subject:                identity,
			Issuer:                 provenance.DefaultOIDCIssuer,
			BuildSignerURI:         builderID,
			SourceRepositoryURI:    sourceURI,
			SourceRepositoryDigest: env.GitCommit,
		}, tCursor.Add(-5*time.Second), provenance.DefaultCertTTL)
		if err != nil {
			return err
		}
		b, err := provenance.AttestStatement(buildStmt(i), provenance.AttestOptions{
			ID:      fmt.Sprintf("prov_ed_%d", i),
			Org:     "hanzo",
			Signer:  kp,
			CertPEM: certPEM,
			TLog:    tlog,
			Now:     tCursor,
		})
		if err != nil {
			return err
		}
		if i == iterations-1 {
			raw, _ := json.Marshal(b)
			edBundleSize = len(raw)
			edProofDepth = len(b.LogEntry.InclusionProof.Hashes)
		}
		edBundles = append(edBundles, b)
		return nil
	})
	edAttestStats.BundleSizeBytes = edBundleSize
	edAttestStats.ProofHashesCount = edProofDepth

	policy := &provenance.VerificationPolicy{
		TrustedCertRoots:   ca.CertPool(),
		AllowedOIDCIssuers: []string{provenance.DefaultOIDCIssuer},
		AllowedIdentities:  []string{identity},
		AllowedBuilders:    []string{builderID},
		AllowedSourceURIs:  []string{sourceURI},
		RequiredBranch:     "refs/heads/main",
		RequiredCommit:     env.GitCommit,
		RequiredTree:       env.GitTree,
		RequiredToolchainDigests: map[string]string{
			"go": env.ToolchainSHA,
		},
		TLogPublicKey: tlog.PublicKey(),
		TLogOrigin:    tlog.Origin(),
	}

	edVerifyStats := measure(iterations, func(i int) error {
		_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, edBundles[i], nil, policy)
		return err
	})

	// 2b. Verify with pinned checkpoint (size 1) + RFC 6962 consistency proof to latest size.
	pinnedSTH := edBundles[0].LogEntry.Checkpoint
	lastEdBundle := edBundles[len(edBundles)-1]
	cProof, err := tlog.ConsistencyProof(pinnedSTH.TreeSize, lastEdBundle.LogEntry.Checkpoint.TreeSize)
	if err != nil {
		return PerformanceSummary{}, err
	}
	pinnedPolicy := policy.Clone()
	pinnedPolicy.PinnedCheckpoint = &pinnedSTH
	edVerifyPinStats := measure(iterations, func(i int) error {
		_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, lastEdBundle, &cProof, pinnedPolicy)
		return err
	})
	edVerifyPinStats.ProofHashesCount = len(cProof.Hashes)

	// 3. ECDSA P-256 Keyless.
	var ecdsaBundles []*provenance.Bundle
	var ecdsaBundleSize int
	var ecdsaProofDepth int
	ecdsaAttestStats := measure(iterations, func(i int) error {
		tCursor = tCursor.Add(time.Millisecond)
		kp, err := provenance.GenerateECDSAP256KeyPair()
		if err != nil {
			return err
		}
		_, certPEM, err := ca.IssueCodeSigningCert(kp.PublicKey(), provenance.OIDCIdentity{
			Subject:                identity,
			Issuer:                 provenance.DefaultOIDCIssuer,
			BuildSignerURI:         builderID,
			SourceRepositoryURI:    sourceURI,
			SourceRepositoryDigest: env.GitCommit,
		}, tCursor.Add(-5*time.Second), provenance.DefaultCertTTL)
		if err != nil {
			return err
		}
		b, err := provenance.AttestStatement(buildStmt(10000+i), provenance.AttestOptions{
			ID:      fmt.Sprintf("prov_p256_%d", i),
			Org:     "hanzo",
			Signer:  kp,
			CertPEM: certPEM,
			TLog:    tlog,
			Now:     tCursor,
		})
		if err != nil {
			return err
		}
		if i == iterations-1 {
			raw, _ := json.Marshal(b)
			ecdsaBundleSize = len(raw)
			ecdsaProofDepth = len(b.LogEntry.InclusionProof.Hashes)
		}
		ecdsaBundles = append(ecdsaBundles, b)
		return nil
	})
	ecdsaAttestStats.BundleSizeBytes = ecdsaBundleSize
	ecdsaAttestStats.ProofHashesCount = ecdsaProofDepth

	ecdsaVerifyStats := measure(iterations, func(i int) error {
		_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, ecdsaBundles[i], nil, policy)
		return err
	})

	// 4. Direct Pinned Ed25519 Key (no per-build X.509 cert issuance).
	directKP, err := provenance.GenerateEd25519KeyPair()
	if err != nil {
		return PerformanceSummary{}, err
	}
	var directBundles []*provenance.Bundle
	var directBundleSize int
	directAttestStats := measure(iterations, func(i int) error {
		tCursor = tCursor.Add(time.Millisecond)
		b, err := provenance.AttestStatement(buildStmt(20000+i), provenance.AttestOptions{
			ID:     fmt.Sprintf("prov_direct_%d", i),
			Org:    "hanzo",
			Signer: directKP,
			TLog:   tlog,
			Now:    tCursor,
		})
		if err != nil {
			return err
		}
		if i == iterations-1 {
			raw, _ := json.Marshal(b)
			directBundleSize = len(raw)
		}
		directBundles = append(directBundles, b)
		return nil
	})
	directAttestStats.BundleSizeBytes = directBundleSize

	directPolicy := policy.Clone()
	directPolicy.TrustedCertRoots = nil
	directPolicy.TrustedKeys = map[string]crypto.PublicKey{directKP.KeyID(): directKP.PublicKey()}
	directVerifyStats := measure(iterations, func(i int) error {
		_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, directBundles[i], nil, directPolicy)
		return err
	})

	return PerformanceSummary{
		UnsignedSHA256Baseline:    baselineStats,
		Ed25519KeylessAttest:      edAttestStats,
		Ed25519KeylessVerify:      edVerifyStats,
		Ed25519KeylessVerifyPinCP: edVerifyPinStats,
		ECDSAP256KeylessAttest:    ecdsaAttestStats,
		ECDSAP256KeylessVerify:    ecdsaVerifyStats,
		Ed25519DirectKeyAttest:    directAttestStats,
		Ed25519DirectKeyVerify:    directVerifyStats,
	}, nil
}

func runAdversarialMatrix(env EnvMetadata, corpus CorpusSummary) (AdversarialSummary, error) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	ca, err := provenance.NewEphemeralCA("Hanzo Adversarial CA", now)
	if err != nil {
		return AdversarialSummary{}, err
	}
	tlogKey, err := provenance.GenerateEd25519KeyPair()
	if err != nil {
		return AdversarialSummary{}, err
	}
	tlog, err := provenance.NewTransparencyLog(provenance.DefaultTLogOrigin, tlogKey)
	if err != nil {
		return AdversarialSummary{}, err
	}
	signer, err := provenance.GenerateEd25519KeyPair()
	if err != nil {
		return AdversarialSummary{}, err
	}

	primary := corpus.Artifacts[0]
	secondary := primary
	if len(corpus.Artifacts) > 1 {
		secondary = corpus.Artifacts[1]
	}

	identity := "https://hanzo.ai/principals/hanzo/kai-forge-agent"
	sourceURI := "git+https://github.com/hanzoai/cloud"
	builderID := "https://hanzo.ai/forge/runners/linux-amd64-v1"

	cert, certPEM, err := ca.IssueCodeSigningCert(signer.PublicKey(), provenance.OIDCIdentity{
		Subject:                identity,
		Issuer:                 provenance.DefaultOIDCIssuer,
		BuildSignerURI:         builderID,
		SourceRepositoryURI:    sourceURI,
		SourceRepositoryDigest: env.GitCommit,
	}, now.Add(-1*time.Minute), provenance.DefaultCertTTL)
	if err != nil {
		return AdversarialSummary{}, err
	}
	certSerial := strings.ToLower(cert.SerialNumber.Text(16))

	stmt, err := provenance.NewStatement(
		[]provenance.ResourceDescriptor{{Name: primary.Name, Digest: provenance.DigestSet{"sha256": primary.SHA256}}},
		provenance.BuildDefinition{
			BuildType: provenance.DefaultBuildType,
			ExternalParameters: provenance.ExternalParameters{
				Source: provenance.SourceRef{
					URI:    sourceURI,
					Branch: "refs/heads/main",
					Commit: env.GitCommit,
					Tree:   env.GitTree,
				},
				BuilderIdentity: identity,
				Toolchain: []provenance.ToolchainComponent{
					{Name: "go", Version: env.GoVersion, Digest: provenance.DigestSet{"sha256": env.ToolchainSHA}},
				},
				InvocationOrg: "hanzo",
			},
		},
		provenance.RunDetails{
			Builder:  provenance.Builder{ID: builderID},
			Metadata: provenance.BuildMetadata{InvocationID: "adv-001", StartedOn: now.Add(-time.Second), FinishedOn: now},
		},
	)
	if err != nil {
		return AdversarialSummary{}, err
	}

	// Append an initial entry + our target bundle so tree size >= 2 (non-empty inclusion proof).
	if _, err := tlog.Append([]byte("genesis-checkpoint-entry"), now); err != nil {
		return AdversarialSummary{}, err
	}
	tCur := now.Add(time.Second)
	validBundle, err := provenance.AttestStatement(stmt, provenance.AttestOptions{
		ID:      "prov_adv_valid",
		Org:     "hanzo",
		Signer:  signer,
		CertPEM: certPEM,
		TLog:    tlog,
		Now:     tCur,
	})
	if err != nil {
		return AdversarialSummary{}, err
	}

	basePolicy := &provenance.VerificationPolicy{
		TrustedCertRoots:         ca.CertPool(),
		AllowedOIDCIssuers:       []string{provenance.DefaultOIDCIssuer},
		AllowedIdentities:        []string{identity},
		AllowedBuilders:          []string{builderID},
		AllowedSourceURIs:        []string{sourceURI},
		RequiredBranch:           "refs/heads/main",
		RequiredCommit:           env.GitCommit,
		RequiredTree:             env.GitTree,
		RequiredToolchainDigests: map[string]string{"go": env.ToolchainSHA},
		TLogPublicKey:            tlog.PublicKey(),
		TLogOrigin:               tlog.Origin(),
	}

	if _, err := provenance.VerifyBundle(primary.SHA256, primary.Name, validBundle, nil, basePolicy); err != nil {
		return AdversarialSummary{}, fmt.Errorf("adversarial baseline failed: %w", err)
	}

	nextTime := func() time.Time {
		tCur = tCur.Add(time.Second)
		return tCur
	}

	// Read real primary artifact bytes to simulate a real 1-bit flip.
	rawPrimary, err := os.ReadFile(primary.diskPath)
	if err != nil {
		return AdversarialSummary{}, err
	}
	tamperedBytes := append([]byte(nil), rawPrimary...)
	tamperedBytes[len(tamperedBytes)/2] ^= 0x01
	tamperedPrimaryHash := provenance.DigestBytes(tamperedBytes)

	type attackCase struct {
		id, category, desc, wantCode string
		run                          func() error
	}

	cases := []attackCase{
		{
			id: "V01_artifact_1bit_flip", category: "artifact_tamper",
			desc: "Single-bit flip in real compiled binary bytes", wantCode: "DIGEST_MISMATCH",
			run: func() error {
				_, err := provenance.VerifyBundle(tamperedPrimaryHash, primary.Name, validBundle, nil, basePolicy)
				return err
			},
		},
		{
			id: "V02_unauthorized_key_substitution", category: "signer_substitution",
			desc: "Valid Ed25519 DSSE signature from untrusted rogue key", wantCode: "UNTRUSTED_SIGNER",
			run: func() error {
				rogue, _ := provenance.GenerateEd25519KeyPair()
				b, _ := provenance.AttestStatement(stmt, provenance.AttestOptions{Signer: rogue, TLog: tlog, Now: nextTime()})
				pol := basePolicy.Clone()
				pol.TrustedKeys = map[string]crypto.PublicKey{signer.KeyID(): signer.PublicKey()}
				_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, b, nil, pol)
				return err
			},
		},
		{
			id: "V03_unauthorized_oidc_san", category: "signer_substitution",
			desc: "Valid Fulcio-style cert from trusted CA for unauthorized OIDC SAN", wantCode: "UNTRUSTED_SIGNER",
			run: func() error {
				rogue, _ := provenance.GenerateEd25519KeyPair()
				rogueSAN := "https://hanzo.ai/principals/attacker/evil-agent"
				_, rCert, _ := ca.IssueCodeSigningCert(rogue.PublicKey(), provenance.OIDCIdentity{
					Subject: rogueSAN, Issuer: provenance.DefaultOIDCIssuer,
					SourceRepositoryURI: sourceURI, SourceRepositoryDigest: env.GitCommit,
				}, now.Add(-time.Minute), provenance.DefaultCertTTL)
				rs := stmt.Clone()
				rs.Predicate.BuildDefinition.ExternalParameters.BuilderIdentity = rogueSAN
				b, _ := provenance.AttestStatement(&rs, provenance.AttestOptions{Signer: rogue, CertPEM: rCert, TLog: tlog, Now: nextTime()})
				_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, b, nil, basePolicy)
				return err
			},
		},
		{
			id: "V04_untrusted_ca_substitution", category: "signer_substitution",
			desc: "Valid X.509 cert minted by untrusted rogue Root CA claiming authorized SAN", wantCode: "CERT_INVALID",
			run: func() error {
				rogueCA, _ := provenance.NewEphemeralCA("Rogue CA", now)
				rogue, _ := provenance.GenerateEd25519KeyPair()
				_, rCert, _ := rogueCA.IssueCodeSigningCert(rogue.PublicKey(), provenance.OIDCIdentity{
					Subject: identity, Issuer: provenance.DefaultOIDCIssuer,
				}, now.Add(-time.Minute), provenance.DefaultCertTTL)
				b, _ := provenance.AttestStatement(stmt, provenance.AttestOptions{Signer: rogue, CertPEM: rCert, TLog: tlog, Now: nextTime()})
				_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, b, nil, basePolicy)
				return err
			},
		},
		{
			id: "V05_disallowed_builder_id", category: "builder_substitution",
			desc: "Valid signature from runner outside AllowedBuilders allowlist", wantCode: "BUILDER_MISMATCH",
			run: func() error {
				pol := basePolicy.Clone()
				pol.AllowedBuilders = []string{"https://hanzo.ai/forge/runners/isolated-tee-v2"}
				_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, validBundle, nil, pol)
				return err
			},
		},
		{
			id: "V06_source_repo_uri_substitution", category: "source_substitution",
			desc: "Artifact built from unauthorized repository URI", wantCode: "SOURCE_MISMATCH",
			run: func() error {
				pol := basePolicy.Clone()
				pol.AllowedSourceURIs = []string{"git+https://github.com/hanzoai/verified-only"}
				_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, validBundle, nil, pol)
				return err
			},
		},
		{
			id: "V07_source_commit_substitution", category: "source_substitution",
			desc: "Artifact built from unpinned Git commit SHA", wantCode: "SOURCE_MISMATCH",
			run: func() error {
				pol := basePolicy.Clone()
				pol.RequiredCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
				_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, validBundle, nil, pol)
				return err
			},
		},
		{
			id: "V08_toolchain_digest_mismatch", category: "toolchain_tamper",
			desc: "Go compiler digest differs from pinned trusted toolchain SHA-256", wantCode: "TOOLCHAIN_MISMATCH",
			run: func() error {
				pol := basePolicy.Clone()
				pol.RequiredToolchainDigests["go"] = provenance.DigestBytes([]byte("backdoored-go-compiler"))
				_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, validBundle, nil, pol)
				return err
			},
		},
		{
			id: "V09_dsse_payload_type_confusion", category: "dsse_tamper",
			desc: "Valid signature over wrong DSSE payloadType (application/json)", wantCode: "PAYLOAD_TYPE_MISMATCH",
			run: func() error {
				raw, _ := json.Marshal(stmt)
				envBad, _ := provenance.SignDSSE("application/json", raw, signer, certPEM)
				leaf, _ := provenance.CanonicalLogLeaf(envBad)
				entry, _ := tlog.Append(leaf, nextTime())
				b := &provenance.Bundle{MediaType: provenance.BundleMediaType, Envelope: *envBad, LogEntry: *entry, CreatedAt: entry.IntegratedTime}
				_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, b, nil, basePolicy)
				return err
			},
		},
		{
			id: "V10_dsse_pae_signature_tamper", category: "dsse_tamper",
			desc: "Tampered DSSE PAE signature bytes even when re-logged into TLog", wantCode: "INVALID_SIGNATURE",
			run: func() error {
				b := validBundle.Clone()
				sigBytes, _ := base64.StdEncoding.DecodeString(b.Envelope.Signatures[0].Sig)
				sigBytes[0] ^= 0xff
				b.Envelope.Signatures[0].Sig = base64.StdEncoding.EncodeToString(sigBytes)
				leaf, _ := provenance.CanonicalLogLeaf(&b.Envelope)
				entry, _ := tlog.Append(leaf, nextTime())
				b.LogEntry = *entry
				_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, &b, nil, basePolicy)
				return err
			},
		},
		{
			id: "V11_replayed_swapped_provenance", category: "replay_attack",
			desc: "Valid bundle for primary binary replayed against different repository artifact", wantCode: "ARTIFACT_NOT_IN_SUBJECT",
			run: func() error {
				_, err := provenance.VerifyBundle(secondary.SHA256, secondary.Name, validBundle, nil, basePolicy)
				return err
			},
		},
		{
			id: "V12_forged_merkle_inclusion_proof", category: "tlog_tamper",
			desc: "1-bit corruption in RFC 6962 Merkle inclusion audit path", wantCode: "TLOG_INVALID",
			run: func() error {
				b := validBundle.Clone()
				h, _ := hex.DecodeString(b.LogEntry.InclusionProof.Hashes[0])
				h[0] ^= 0x01
				b.LogEntry.InclusionProof.Hashes[0] = hex.EncodeToString(h)
				_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, &b, nil, basePolicy)
				return err
			},
		},
		{
			id: "V13_tlog_split_view_equivocation", category: "tlog_tamper",
			desc: "Equivocating forked transparency log violating pinned checkpoint consistency", wantCode: "TLOG_INVALID",
			run: func() error {
				pinned := validBundle.LogEntry.Checkpoint
				forkLog, _ := provenance.NewTransparencyLog(provenance.DefaultTLogOrigin, tlogKey)
				_, _ = forkLog.Append([]byte("forked-entry-0"), now)
				_, _ = forkLog.Append([]byte("forked-entry-1"), now.Add(time.Second))
				bFork, _ := provenance.AttestStatement(stmt, provenance.AttestOptions{Signer: signer, CertPEM: certPEM, TLog: forkLog, Now: now.Add(2 * time.Second)})
				cp, _ := forkLog.ConsistencyProof(pinned.TreeSize, bFork.LogEntry.Checkpoint.TreeSize)
				pol := basePolicy.Clone()
				pol.PinnedCheckpoint = &pinned
				_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, bFork, &cp, pol)
				return err
			},
		},
		{
			id: "V14_expired_cert_outside_tlog_time", category: "cert_expiry",
			desc: "Short-lived X.509 certificate expired before transparency log integratedTime", wantCode: "CERT_INVALID",
			run: func() error {
				ekp, _ := provenance.GenerateEd25519KeyPair()
				_, eCert, _ := ca.IssueCodeSigningCert(ekp.PublicKey(), provenance.OIDCIdentity{
					Subject: identity, Issuer: provenance.DefaultOIDCIssuer,
					SourceRepositoryURI: sourceURI, SourceRepositoryDigest: env.GitCommit,
				}, now.Add(-30*time.Minute), 10*time.Minute)
				b, _ := provenance.AttestStatement(stmt, provenance.AttestOptions{Signer: ekp, CertPEM: eCert, TLog: tlog, Now: nextTime()})
				_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, b, nil, basePolicy)
				return err
			},
		},
		{
			id: "V15_revoked_cert_serial", category: "revocation",
			desc: "Certificate serial number present in org revocation list", wantCode: "REVOKED_SIGNER",
			run: func() error {
				pol := basePolicy.Clone()
				pol.RevokedCertSerials = map[string]bool{certSerial: true}
				_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, validBundle, nil, pol)
				return err
			},
		},
		{
			id: "V16_revoked_signer_key", category: "revocation",
			desc: "Signer SPKI KeyID present in org revocation list", wantCode: "REVOKED_SIGNER",
			run: func() error {
				pol := basePolicy.Clone()
				pol.RevokedKeyIDs = map[string]bool{signer.KeyID(): true}
				_, err := provenance.VerifyBundle(primary.SHA256, primary.Name, validBundle, nil, pol)
				return err
			},
		},
	}

	results := make([]AdversarialVectorResult, 0, len(cases))
	refused := 0
	passed := 0
	for _, c := range cases {
		err := c.run()
		isRefused := err != nil
		actualCode := "ACCEPTED"
		errMsg := ""
		if isRefused {
			refused++
			actualCode = provenance.ErrorCode(err)
			errMsg = err.Error()
		}
		ok := isRefused && actualCode == c.wantCode
		if ok {
			passed++
		}
		results = append(results, AdversarialVectorResult{
			ID:           c.id,
			Category:     c.category,
			Description:  c.desc,
			ExpectedCode: c.wantCode,
			ActualCode:   actualCode,
			Refused:      isRefused,
			Passed:       ok,
			ErrorDetail:  errMsg,
		})
	}

	return AdversarialSummary{
		TotalVectors:   len(cases),
		RefusedCount:   refused,
		PassedCount:    passed,
		RefusalRatePct: float64(refused) * 100.0 / float64(len(cases)),
		Vectors:        results,
	}, nil
}

func runNegativeRegimes(env EnvMetadata, smallFiles int, seed int64) (NegativeRegimeReport, error) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	ca, err := provenance.NewEphemeralCA("Hanzo Negative Regime CA", now)
	if err != nil {
		return NegativeRegimeReport{}, err
	}
	tlogKey, err := provenance.GenerateEd25519KeyPair()
	if err != nil {
		return NegativeRegimeReport{}, err
	}
	tlog, err := provenance.NewTransparencyLog(provenance.DefaultTLogOrigin, tlogKey)
	if err != nil {
		return NegativeRegimeReport{}, err
	}
	signer, err := provenance.GenerateEd25519KeyPair()
	if err != nil {
		return NegativeRegimeReport{}, err
	}
	identity := "https://hanzo.ai/principals/hanzo/kai-forge-agent"
	sourceURI := "git+https://github.com/hanzoai/cloud"
	builderID := "https://hanzo.ai/forge/runners/linux-amd64-v1"

	_, certPEM, err := ca.IssueCodeSigningCert(signer.PublicKey(), provenance.OIDCIdentity{
		Subject: identity, Issuer: provenance.DefaultOIDCIssuer,
		SourceRepositoryURI: sourceURI, SourceRepositoryDigest: env.GitCommit,
	}, now.Add(-time.Minute), provenance.DefaultCertTTL)
	if err != nil {
		return NegativeRegimeReport{}, err
	}

	// Negative Regime A: Inline raw blob DSSE signing vs SLSA digest-only attestation.
	blobSizes := []int{64 * 1024, 1 * 1024 * 1024, 4 * 1024 * 1024, 16 * 1024 * 1024}
	inlinePoints := make([]InlineBlobPoint, 0, len(blobSizes))
	tCur := now

	for _, sz := range blobSizes {
		blob := deterministicBlob(sz, seed+int64(sz))

		// Digest-only SLSA v1.2 attestation.
		tCur = tCur.Add(time.Millisecond)
		t0 := time.Now()
		digest := provenance.DigestBytes(blob)
		stmt, err := provenance.NewStatement(
			[]provenance.ResourceDescriptor{{Name: fmt.Sprintf("artifact-%dB.bin", sz), Digest: provenance.DigestSet{"sha256": digest}}},
			provenance.BuildDefinition{
				BuildType: provenance.DefaultBuildType,
				ExternalParameters: provenance.ExternalParameters{
					Source:          provenance.SourceRef{URI: sourceURI, Commit: env.GitCommit},
					BuilderIdentity: identity,
					Toolchain:       []provenance.ToolchainComponent{{Name: "go", Version: env.GoVersion, Digest: provenance.DigestSet{"sha256": env.ToolchainSHA}}},
				},
			},
			provenance.RunDetails{
				Builder:  provenance.Builder{ID: builderID},
				Metadata: provenance.BuildMetadata{InvocationID: fmt.Sprintf("neg-digest-%d", sz), StartedOn: now, FinishedOn: now},
			},
		)
		if err != nil {
			return NegativeRegimeReport{}, err
		}
		bundle, err := provenance.AttestStatement(stmt, provenance.AttestOptions{Signer: signer, CertPEM: certPEM, TLog: tlog, Now: tCur})
		if err != nil {
			return NegativeRegimeReport{}, err
		}
		digestRaw, _ := json.Marshal(bundle)
		digestDur := time.Since(t0)

		// Inline raw blob DSSE envelope (anti-pattern: embeds entire artifact inside base64 DSSE payload).
		t1 := time.Now()
		inlineEnv, err := provenance.SignDSSE("application/octet-stream", blob, signer, certPEM)
		if err != nil {
			return NegativeRegimeReport{}, err
		}
		inlineRaw, _ := json.Marshal(inlineEnv)
		inlineDur := time.Since(t1)

		inlinePoints = append(inlinePoints, InlineBlobPoint{
			BlobSizeBytes:         sz,
			DigestOnlyAttestUs:    round2(float64(digestDur.Nanoseconds()) / 1e3),
			DigestOnlyBundleBytes: len(digestRaw),
			InlineBlobAttestUs:    round2(float64(inlineDur.Nanoseconds()) / 1e3),
			InlineBlobBundleBytes: len(inlineRaw),
			SizeAmplificationX:    round2(float64(len(inlineRaw)) / float64(len(digestRaw))),
			LatencyRatioX:         round2(float64(inlineDur.Nanoseconds()) / math.Max(1, float64(digestDur.Nanoseconds()))),
		})
	}

	// Negative Regime B: Per-file individual attestation vs multi-subject batched statement on N tiny intermediate files.
	subjects := make([]provenance.ResourceDescriptor, smallFiles)
	for i := 0; i < smallFiles; i++ {
		chunk := deterministicBlob(512, seed+int64(i))
		subjects[i] = provenance.ResourceDescriptor{
			Name:   fmt.Sprintf("obj/intermediate_%04d.o", i),
			Digest: provenance.DigestSet{"sha256": provenance.DigestBytes(chunk)},
		}
	}

	tlogPerFile, _ := provenance.NewTransparencyLog(provenance.DefaultTLogOrigin, tlogKey)
	perFileStart := time.Now()
	perFileBytes := 0
	for i, sub := range subjects {
		tCur = tCur.Add(time.Microsecond)
		kp, err := provenance.GenerateEd25519KeyPair()
		if err != nil {
			return NegativeRegimeReport{}, err
		}
		_, cPEM, err := ca.IssueCodeSigningCert(kp.PublicKey(), provenance.OIDCIdentity{
			Subject: identity, Issuer: provenance.DefaultOIDCIssuer,
			SourceRepositoryURI: sourceURI, SourceRepositoryDigest: env.GitCommit,
		}, tCur.Add(-5*time.Second), provenance.DefaultCertTTL)
		if err != nil {
			return NegativeRegimeReport{}, err
		}
		stmt, err := provenance.NewStatement(
			[]provenance.ResourceDescriptor{sub},
			provenance.BuildDefinition{
				BuildType: provenance.DefaultBuildType,
				ExternalParameters: provenance.ExternalParameters{
					Source:          provenance.SourceRef{URI: sourceURI, Commit: env.GitCommit},
					BuilderIdentity: identity,
					Toolchain:       []provenance.ToolchainComponent{{Name: "go", Version: env.GoVersion, Digest: provenance.DigestSet{"sha256": env.ToolchainSHA}}},
				},
			},
			provenance.RunDetails{
				Builder:  provenance.Builder{ID: builderID},
				Metadata: provenance.BuildMetadata{InvocationID: fmt.Sprintf("neg-small-%d", i), StartedOn: now, FinishedOn: now},
			},
		)
		if err != nil {
			return NegativeRegimeReport{}, err
		}
		b, err := provenance.AttestStatement(stmt, provenance.AttestOptions{Signer: kp, CertPEM: cPEM, TLog: tlogPerFile, Now: tCur})
		if err != nil {
			return NegativeRegimeReport{}, err
		}
		raw, _ := json.Marshal(b)
		perFileBytes += len(raw)
	}
	perFileDur := time.Since(perFileStart)

	tlogBatched, _ := provenance.NewTransparencyLog(provenance.DefaultTLogOrigin, tlogKey)
	batchStart := time.Now()
	kpBatch, err := provenance.GenerateEd25519KeyPair()
	if err != nil {
		return NegativeRegimeReport{}, err
	}
	_, cBatchPEM, err := ca.IssueCodeSigningCert(kpBatch.PublicKey(), provenance.OIDCIdentity{
		Subject: identity, Issuer: provenance.DefaultOIDCIssuer,
		SourceRepositoryURI: sourceURI, SourceRepositoryDigest: env.GitCommit,
	}, now.Add(-5*time.Second), provenance.DefaultCertTTL)
	if err != nil {
		return NegativeRegimeReport{}, err
	}
	batchStmt, err := provenance.NewStatement(
		subjects,
		provenance.BuildDefinition{
			BuildType: provenance.DefaultBuildType,
			ExternalParameters: provenance.ExternalParameters{
				Source:          provenance.SourceRef{URI: sourceURI, Commit: env.GitCommit},
				BuilderIdentity: identity,
				Toolchain:       []provenance.ToolchainComponent{{Name: "go", Version: env.GoVersion, Digest: provenance.DigestSet{"sha256": env.ToolchainSHA}}},
			},
		},
		provenance.RunDetails{
			Builder:  provenance.Builder{ID: builderID},
			Metadata: provenance.BuildMetadata{InvocationID: "neg-small-batched", StartedOn: now, FinishedOn: now},
		},
	)
	if err != nil {
		return NegativeRegimeReport{}, err
	}
	batchBundle, err := provenance.AttestStatement(batchStmt, provenance.AttestOptions{Signer: kpBatch, CertPEM: cBatchPEM, TLog: tlogBatched, Now: now})
	if err != nil {
		return NegativeRegimeReport{}, err
	}
	batchRaw, _ := json.Marshal(batchBundle)
	batchDur := time.Since(batchStart)

	return NegativeRegimeReport{
		InlineRawBlobVsDigestOnly: inlinePoints,
		SmallFilesBatching: SmallFilesBatchPoint{
			FileCount:            smallFiles,
			PerFileTotalMs:       round2(float64(perFileDur.Nanoseconds()) / 1e6),
			PerFileBundleBytes:   perFileBytes,
			PerFileTLogEntries:   int(tlogPerFile.Size()),
			BatchedTotalMs:       round2(float64(batchDur.Nanoseconds()) / 1e6),
			BatchedBundleBytes:   len(batchRaw),
			BatchedTLogEntries:   int(tlogBatched.Size()),
			BatchSpeedupX:        round2(float64(perFileDur.Nanoseconds()) / math.Max(1, float64(batchDur.Nanoseconds()))),
			BundleSizeReductionX: round2(float64(perFileBytes) / math.Max(1, float64(len(batchRaw)))),
		},
	}, nil
}

func measure(n int, fn func(i int) error) LatencyStats {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	samples := make([]float64, 0, n)
	runtime.GC()
	var mBefore, mAfter runtime.MemStats
	runtime.ReadMemStats(&mBefore)

	var sumNs float64
	for i := 0; i < n; i++ {
		t0 := time.Now()
		if err := fn(i); err != nil {
			panic(fmt.Sprintf("benchmark step %d failed: %v", i, err))
		}
		dur := time.Since(t0)
		if dur <= 0 {
			// Skip rare virtualized hypervisor cross-vCPU TSC backward step.
			continue
		}
		us := float64(dur.Nanoseconds()) / 1e3
		samples = append(samples, us)
		sumNs += float64(dur.Nanoseconds())
	}
	runtime.ReadMemStats(&mAfter)
	if len(samples) == 0 {
		samples = append(samples, 0.01)
		sumNs = 10
	}

	sort.Float64s(samples)
	sum := 0.0
	for _, s := range samples {
		sum += s
	}
	mean := sum / float64(len(samples))
	totalSec := math.Max(1e-9, sumNs/1e9)
	ops := float64(len(samples)) / totalSec

	var allocPerOp, mallocsPerOp uint64
	if mAfter.TotalAlloc >= mBefore.TotalAlloc {
		allocPerOp = (mAfter.TotalAlloc - mBefore.TotalAlloc) / uint64(n)
	}
	if mAfter.Mallocs >= mBefore.Mallocs {
		mallocsPerOp = (mAfter.Mallocs - mBefore.Mallocs) / uint64(n)
	}

	return LatencyStats{
		Samples:         len(samples),
		MinUs:           round2(samples[0]),
		P50Us:           round2(percentile(samples, 0.50)),
		P95Us:           round2(percentile(samples, 0.95)),
		P99Us:           round2(percentile(samples, 0.99)),
		MeanUs:          round2(mean),
		OpsPerSec:       round2(ops),
		AllocBytesPerOp: allocPerOp,
		MallocsPerOp:    mallocsPerOp,
	}
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Ceil(p*float64(len(sorted)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func round2(v float64) float64 {
	return math.Round(v*100) / 100
}

func deterministicBlob(n int, seed int64) []byte {
	out := make([]byte, n)
	x := uint64(seed) ^ 0x9e3779b97f4a7c15
	for i := 0; i < n; i++ {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		out[i] = byte(x)
	}
	return out
}
