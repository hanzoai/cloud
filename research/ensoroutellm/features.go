// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package ensoroutellm

import (
	"hash/fnv"
	"math"
	"strings"
	"unicode"
)

// FeatureDim is the fixed width of Enso's content-free routing feature vector:
//   - [0..7]   8 domain lexical/task signals
//   - [8..14]  7 structural & protocol complexity signals
//   - [15..22] 8 signed FNV-1a hashed token/n-gram projections
//   - [23]     1 constant intercept (1.0)
const FeatureDim = 24

// FeatureVec is a value-type fixed-width feature vector safe against aliasing
// and persisted in the content-free reward ledger (HIP-0510 §2: never prompt text).
type FeatureVec [FeatureDim]float64

// TaskBucket names the coarse cold-start task categories defined in HIP-0510 §1.
type TaskBucket string

const (
	BucketCode        TaskBucket = "code"
	BucketMath        TaskBucket = "math"
	BucketReasoning   TaskBucket = "reasoning"
	BucketToolSchema  TaskBucket = "tool_schema"
	BucketLongContext TaskBucket = "long_context"
	BucketCheapChat   TaskBucket = "cheap_chat"
	BucketDefault     TaskBucket = "default"
)

// EstimatePromptTokens computes a fast, deterministic token estimate (~4 UTF-8
// bytes/token) when caller token pre-counts are omitted.
func EstimatePromptTokens(prompt string, msgs []ConversationMessage) int {
	totalChars := len(prompt)
	for _, m := range msgs {
		totalChars += len(m.Content) + 12
	}
	if totalChars == 0 {
		return 1
	}
	toks := (totalChars + 3) / 4
	if toks < 1 {
		toks = 1
	}
	return toks
}

var (
	codeKeywords = []string{
		"func ", "def ", "class ", "interface", "goroutine", "mutex", "sql", "select ",
		"implement", "algorithm", "complexity", "refactor", "compile", "pointer", "async",
		"rust", "python", "typescript", "golang", "binary tree", "dynamic programming", "dp",
	}
	mathKeywords = []string{
		"prove", "theorem", "integral", "derivative", "eigenvalue", "matrix", "modulo",
		"prime", "polynomial", "probability", "expectation", "converge", "lemma", "induction",
		"equation", "inequality", "combinatorics", "\\frac", "\\sum", "forall",
	}
	reasoningKeywords = []string{
		"counterfactual", "syllogism", "paradox", "deduce", "necessary and sufficient",
		"formal logic", "contradiction", "invariant", "causal", "hypothesis", "equilibrium",
		"step-by-step", "verify every constraint", "bayesian", "quantum", "thermodynamic",
	}
	toolSchemaKeywords = []string{
		"json schema", "tool_choice", "strict schema", "function call", "openapi",
		"rfc3339", "idempotency_key", "payload", "serialize", "graphql", "webhook",
	}
	ragContextKeywords = []string{
		"document", "passage", "citation", "according to source", "context block",
		"synthesize", "cross-reference", "excerpt", "evidence span", "contradicts",
	}
	multilingualKeywords = []string{
		"traduce", "resumen", "español", "português", "français", "deutsch",
		"translate", "localize", "bilingual", "idiom",
	}
	creativeKeywords = []string{
		"poem", "story", "metaphor", "narrative", "screenplay", "sonnet", "haiku",
	}
	simpleChatKeywords = []string{
		"hello", "hi ", "thanks", "what is the capital", "define ", "rephrase",
		"shorten", "greeting", "one sentence", "tldr", "extract email", "classify sentiment",
	}
)

func countKeywordHits(lower string, kw []string) float64 {
	hits := 0.0
	for _, k := range kw {
		if strings.Contains(lower, k) {
			hits += 1.0
		}
	}
	return hits
}

// ExtractFeatures deterministically maps a RouteRequest into a content-free
// FeatureVec in microseconds on CPU with zero external network or GPU calls.
func ExtractFeatures(req RouteRequest) FeatureVec {
	var sb strings.Builder
	sb.Grow(len(req.Prompt) + 256)
	sb.WriteString(req.Prompt)
	for _, m := range req.Messages {
		sb.WriteByte('\n')
		sb.WriteString(m.Content)
	}
	text := sb.String()
	lower := strings.ToLower(text)

	var v FeatureVec

	// 1. Domain lexical signals (squashed via tanh so no single long document overflows).
	v[0] = math.Tanh(0.45 * countKeywordHits(lower, codeKeywords))
	v[1] = math.Tanh(0.45 * countKeywordHits(lower, mathKeywords))
	v[2] = math.Tanh(0.45 * countKeywordHits(lower, reasoningKeywords))
	v[3] = math.Tanh(0.50 * countKeywordHits(lower, toolSchemaKeywords))
	v[4] = math.Tanh(0.45 * countKeywordHits(lower, ragContextKeywords))
	v[5] = math.Tanh(0.55 * countKeywordHits(lower, multilingualKeywords))
	v[6] = math.Tanh(0.60 * countKeywordHits(lower, creativeKeywords))
	v[7] = math.Tanh(0.55 * countKeywordHits(lower, simpleChatKeywords))

	// 2. Structural & protocol complexity signals.
	toks := req.EstInputTokens
	if toks <= 0 {
		toks = EstimatePromptTokens(req.Prompt, req.Messages)
	}
	v[8] = math.Min(1.0, math.Log1p(float64(toks))/math.Log1p(8192.0))

	codeFences := strings.Count(text, "```") + strings.Count(text, "func(") + strings.Count(text, "{")
	v[9] = math.Tanh(0.25 * float64(codeFences))

	mathSyms := 0
	constraintBullets := 0
	for _, r := range text {
		switch r {
		case '=', '^', '_', '\\', '∑', '∫', '≤', '≥', '≠', '∀', '∃':
			mathSyms++
		}
	}
	constraintBullets = strings.Count(lower, "must ") + strings.Count(lower, "constraint") + strings.Count(text, "\n- ") + strings.Count(text, "\n1.")
	v[10] = math.Tanh(0.15 * float64(mathSyms))
	v[11] = math.Tanh(0.25 * float64(constraintBullets))

	toolIntensity := float64(req.ToolCount) / 8.0
	if req.ToolChoice.IsForced() {
		toolIntensity += 0.65
	}
	v[12] = math.Tanh(toolIntensity)

	turnDepth := float64(len(req.Messages)) / 10.0
	v[13] = math.Tanh(turnDepth)

	thinkingSignal := 0.0
	if req.ThinkingMode == ThinkingMandatory {
		thinkingSignal = 1.0
	} else if req.ThinkingMode == ThinkingAdaptive {
		thinkingSignal = 0.5
	}
	v[14] = thinkingSignal

	// 3. Signed FNV-1a hashed word/bigram projections into 8 dense buckets.
	words := tokenizeWords(lower)
	var hashProj [8]float64
	for i, w := range words {
		addSignedHash(&hashProj, w, 1.0)
		if i+1 < len(words) {
			addSignedHash(&hashProj, w+"_"+words[i+1], 0.85)
		}
	}
	var projNorm float64
	for i := 0; i < 8; i++ {
		projNorm += hashProj[i] * hashProj[i]
	}
	if projNorm > 1e-12 {
		inv := 1.0 / math.Sqrt(projNorm)
		for i := 0; i < 8; i++ {
			v[15+i] = hashProj[i] * inv * 0.5
		}
	}

	// 4. Normalize [0..22] gently and pin intercept at v[23] = 1.0.
	var sq float64
	for i := 0; i < FeatureDim-1; i++ {
		sq += v[i] * v[i]
	}
	if sq > 1.0 {
		scale := 1.0 / math.Sqrt(sq)
		for i := 0; i < FeatureDim-1; i++ {
			v[i] *= scale
		}
	}
	v[FeatureDim-1] = 1.0
	return v
}

func tokenizeWords(lower string) []string {
	var out []string
	start := -1
	for i, r := range lower {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if start < 0 {
				start = i
			}
		} else if start >= 0 {
			if i-start >= 2 {
				out = append(out, lower[start:i])
			}
			start = -1
		}
	}
	if start >= 0 && len(lower)-start >= 2 {
		out = append(out, lower[start:])
	}
	return out
}

func addSignedHash(buckets *[8]float64, token string, weight float64) {
	h := fnv.New64a()
	_, _ = h.Write([]byte(token))
	sum := h.Sum64()
	idx := sum & 7
	sign := 1.0
	if (sum>>8)&1 == 1 {
		sign = -1.0
	}
	buckets[idx] += sign * weight
}

// ClassifyCoarseBucket implements the deterministic cold-start heuristic bucket
// classifier described in HIP-0510 §1 for baseline comparison and cold-start fallback.
func ClassifyCoarseBucket(v FeatureVec) TaskBucket {
	switch {
	case v[0] >= 0.28 || v[9] >= 0.30:
		return BucketCode
	case v[1] >= 0.28 || v[10] >= 0.30:
		return BucketMath
	case v[2] >= 0.26 || v[14] >= 0.50:
		return BucketReasoning
	case v[3] >= 0.25 || v[12] >= 0.35:
		return BucketToolSchema
	case v[4] >= 0.28 || v[8] >= 0.45:
		return BucketLongContext
	case v[7] >= 0.22 && v[8] < 0.22 && v[11] < 0.15:
		return BucketCheapChat
	default:
		return BucketDefault
	}
}
