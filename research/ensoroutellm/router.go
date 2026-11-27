// Copyright 2026 Hanzo AI, Inc.
// SPDX-License-Identifier: Apache-2.0 OR MIT

package ensoroutellm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// LatentRank is the bilinear latent dimension K in Enso's x^T (U V_m) matrix
// factorization predictor (HIP-0510 §1 & RouteLLM ICLR '25).
const LatentRank = 8

// RouterStrategy selects the routing policy evaluated on an identical request stream.
type RouterStrategy string

const (
	StrategyAlwaysStrong          RouterStrategy = "always_strong"
	StrategyAlwaysWeak            RouterStrategy = "always_weak"
	StrategyRandom                RouterStrategy = "random_router"
	StrategyStaticRule            RouterStrategy = "static_rule_prefer"
	StrategyUnconstrainedRouteLLM RouterStrategy = "unconstrained_routellm"
	StrategyLogisticConstrained   RouterStrategy = "logistic_only_constrained"
	StrategyRouteLLMMFConstrained RouterStrategy = "routellm_mf_constrained"
	StrategyEnsoHybridConstrained RouterStrategy = "enso_hybrid_constrained"
)

// IsValid reports whether s is a recognized RouterStrategy.
func (s RouterStrategy) IsValid() bool {
	switch s {
	case StrategyAlwaysStrong,
		StrategyAlwaysWeak,
		StrategyRandom,
		StrategyStaticRule,
		StrategyUnconstrainedRouteLLM,
		StrategyLogisticConstrained,
		StrategyRouteLLMMFConstrained,
		StrategyEnsoHybridConstrained:
		return true
	default:
		return false
	}
}

// PredictorKind selects which calibrated scoring head is queried on TrainedWeights.
type PredictorKind int

const (
	PredictorHybrid PredictorKind = iota
	PredictorMFOnly
	PredictorLROnly
)

// TrainingSample is one content-free labeled observation used to train and
// calibrate Enso's cost-quality predictors (HIP-0510 §2–§3).
type TrainingSample struct {
	QueryID  string     `json:"query_id"`
	Features FeatureVec `json:"features"`
	ModelID  string     `json:"model_id"`
	Passed   float64    `json:"passed"` // [0.0, 1.0]
}

// PlattParams holds per-arm out-of-fold Platt scaling parameters:
// p_hat = sigmoid(Alpha * raw_logit + Beta).
type PlattParams struct {
	Alpha float64 `json:"alpha"`
	Beta  float64 `json:"beta"`
}

// TrainedWeights holds an immutable snapshot of fitted MF, Logistic, and Platt
// parameters plus deterministic revision digests (HIP-1332 §2, §19).
type TrainedWeights struct {
	ArmIndex            map[string]int
	ArmSampleCounts     []int
	U                   [FeatureDim][LatentRank]float64
	V                   [][LatentRank]float64
	MFBias              []float64
	LogisticW           [][FeatureDim]float64
	PlattMF             []PlattParams
	PlattLR             []PlattParams
	PlattHybrid         []PlattParams
	SampleCount         int
	RouterModelRevision string
	CalibrationRevision string
}

func hybridMFWeight(armSampleCount int) float64 {
	if armSampleCount >= 35 {
		return 0.55
	}
	return 0.90
}

func clampLogit(z float64) float64 {
	if math.IsNaN(z) {
		return 0.0
	}
	if z > 30.0 {
		return 30.0
	}
	if z < -30.0 {
		return -30.0
	}
	return z
}

func sigmoid(z float64) float64 {
	z = clampLogit(z)
	return 1.0 / (1.0 + math.Exp(-z))
}

func isFiniteSample(s TrainingSample) bool {
	if !isFinite(s.Passed) || s.Passed < 0.0 || s.Passed > 1.0 {
		return false
	}
	for d := 0; d < FeatureDim; d++ {
		if !isFinite(s.Features[d]) || math.Abs(s.Features[d]) > 1e6 {
			return false
		}
	}
	return true
}

// PredictQuality returns the calibrated pass probability in (0, 1) for armID
// on feature vector x using either pure MF (mfOnly=true) or the Enso hybrid
// MF + Newton-IRLS Logistic ensemble (mfOnly=false).
func (tw *TrainedWeights) PredictQuality(x FeatureVec, armID string, mfOnly bool) (float64, bool) {
	if mfOnly {
		return tw.PredictQualityByKind(x, armID, PredictorMFOnly)
	}
	return tw.PredictQualityByKind(x, armID, PredictorHybrid)
}

// PredictQualityByKind evaluates calibrated quality under Hybrid, MF-only, or
// Logistic-only predictors, each using its own out-of-fold Platt parameters.
func (tw *TrainedWeights) PredictQualityByKind(x FeatureVec, armID string, kind PredictorKind) (float64, bool) {
	if tw == nil {
		return 0.5, false
	}
	mIdx, ok := tw.ArmIndex[armID]
	if !ok || mIdx < 0 || mIdx >= len(tw.V) {
		return 0.5, false
	}

	if kind == PredictorLROnly {
		var lrLogit float64
		w := tw.LogisticW[mIdx]
		for d := 0; d < FeatureDim; d++ {
			lrLogit += w[d] * x[d]
		}
		pl := tw.PlattLR[mIdx]
		return sigmoid(pl.Alpha*lrLogit + pl.Beta), true
	}

	// 1. Bilinear Matrix Factorization logit: b_m + (U^T x) . V_m
	var qLatent [LatentRank]float64
	for k := 0; k < LatentRank; k++ {
		var sum float64
		for d := 0; d < FeatureDim; d++ {
			sum += tw.U[d][k] * x[d]
		}
		qLatent[k] = sum
	}
	mfLogit := tw.MFBias[mIdx]
	for k := 0; k < LatentRank; k++ {
		mfLogit += qLatent[k] * tw.V[mIdx][k]
	}

	if kind == PredictorMFOnly {
		pl := tw.PlattMF[mIdx]
		return sigmoid(pl.Alpha*mfLogit + pl.Beta), true
	}

	// 2. Per-arm L2-regularized Logistic logit: w_m . x
	var lrLogit float64
	w := tw.LogisticW[mIdx]
	for d := 0; d < FeatureDim; d++ {
		lrLogit += w[d] * x[d]
	}

	armCnt := 0
	if mIdx < len(tw.ArmSampleCounts) {
		armCnt = tw.ArmSampleCounts[mIdx]
	}
	mfW := hybridMFWeight(armCnt)
	raw := mfW*mfLogit + (1.0-mfW)*lrLogit
	pl := tw.PlattHybrid[mIdx]
	return sigmoid(pl.Alpha*raw + pl.Beta), true
}

// FitTrainedWeights trains both the bilinear Matrix Factorization model and
// per-arm L2-regularized Newton-IRLS logistic models, with 3-fold query-grouped
// out-of-fold Platt probability calibration so predicted probabilities are
// calibrated without in-sample overfitting.
func FitTrainedWeights(samples []TrainingSample, catalog []ModelArm, seed uint64) *TrainedWeights {
	numArms := len(catalog)
	armIdx := make(map[string]int, numArms)
	for i, a := range catalog {
		armIdx[a.ID] = i
	}

	cleanSamples := make([]TrainingSample, 0, len(samples))
	for _, s := range samples {
		if _, ok := armIdx[s.ModelID]; ok && isFiniteSample(s) {
			cleanSamples = append(cleanSamples, s)
		}
	}

	// Group samples by QueryID into 3 deterministic out-of-fold calibration splits.
	const calFolds = 3
	oofMFLogits := make([][]float64, numArms)
	oofLRLogits := make([][]float64, numArms)
	oofHybLogits := make([][]float64, numArms)
	oofLabels := make([][]float64, numArms)

	for fold := 0; fold < calFolds; fold++ {
		var subTrain, subVal []TrainingSample
		for _, s := range cleanSamples {
			h := fnv.New32a()
			_, _ = h.Write([]byte(s.QueryID))
			if int(h.Sum32()%calFolds) == fold {
				subVal = append(subVal, s)
			} else {
				subTrain = append(subTrain, s)
			}
		}
		if len(subTrain) == 0 || len(subVal) == 0 {
			continue
		}
		uSub, vSub, bSub := fitMatrixFactorization(subTrain, armIdx, numArms, seed^uint64(fold+1)*0x9e3779b9)
		wSub := fitPerArmLogisticIRLS(subTrain, armIdx, numArms)
		subCnts := make([]int, numArms)
		for _, s := range subTrain {
			subCnts[armIdx[s.ModelID]]++
		}

		for _, s := range subVal {
			m := armIdx[s.ModelID]
			mfL := rawMFLogit(&uSub, vSub, bSub, s.Features, m)
			lrL := rawLRLogit(wSub, s.Features, m)
			mfW := hybridMFWeight(subCnts[m])
			oofMFLogits[m] = append(oofMFLogits[m], mfL)
			oofLRLogits[m] = append(oofLRLogits[m], lrL)
			oofHybLogits[m] = append(oofHybLogits[m], mfW*mfL+(1.0-mfW)*lrL)
			oofLabels[m] = append(oofLabels[m], s.Passed)
		}
	}

	// Fit final predictors on 100% of provided clean training samples.
	uFull, vFull, bFull := fitMatrixFactorization(cleanSamples, armIdx, numArms, seed)
	wFull := fitPerArmLogisticIRLS(cleanSamples, armIdx, numArms)
	fullCnts := make([]int, numArms)
	for _, s := range cleanSamples {
		fullCnts[armIdx[s.ModelID]]++
	}

	plattMF := make([]PlattParams, numArms)
	plattLR := make([]PlattParams, numArms)
	plattHyb := make([]PlattParams, numArms)
	for m := 0; m < numArms; m++ {
		plattMF[m] = fitPlatt1D(oofMFLogits[m], oofLabels[m])
		plattLR[m] = fitPlatt1D(oofLRLogits[m], oofLabels[m])
		plattHyb[m] = fitPlatt1D(oofHybLogits[m], oofLabels[m])
	}

	tw := &TrainedWeights{
		ArmIndex:        armIdx,
		ArmSampleCounts: fullCnts,
		U:               uFull,
		V:               vFull,
		MFBias:          bFull,
		LogisticW:       wFull,
		PlattMF:         plattMF,
		PlattLR:         plattLR,
		PlattHybrid:     plattHyb,
		SampleCount:     len(cleanSamples),
	}
	sanitizeTrainedWeights(tw)
	tw.RouterModelRevision, tw.CalibrationRevision = computeWeightDigests(tw)
	return tw
}

func sanitizeTrainedWeights(tw *TrainedWeights) {
	clampFinite := func(x, lo, hi float64) float64 {
		if !isFinite(x) {
			return 0
		}
		if x < lo {
			return lo
		}
		if x > hi {
			return hi
		}
		return x
	}
	for d := 0; d < FeatureDim; d++ {
		for k := 0; k < LatentRank; k++ {
			tw.U[d][k] = clampFinite(tw.U[d][k], -25, 25)
		}
	}
	for m := range tw.V {
		for k := 0; k < LatentRank; k++ {
			tw.V[m][k] = clampFinite(tw.V[m][k], -25, 25)
		}
	}
	for m := range tw.MFBias {
		tw.MFBias[m] = clampFinite(tw.MFBias[m], -25, 25)
	}
	for m := range tw.LogisticW {
		for d := 0; d < FeatureDim; d++ {
			tw.LogisticW[m][d] = clampFinite(tw.LogisticW[m][d], -50, 50)
		}
	}
	fixPlatt := func(ps []PlattParams) {
		for i := range ps {
			if !isFinite(ps[i].Alpha) {
				ps[i].Alpha = 1.0
			}
			if !isFinite(ps[i].Beta) {
				ps[i].Beta = 0.0
			}
		}
	}
	fixPlatt(tw.PlattMF)
	fixPlatt(tw.PlattLR)
	fixPlatt(tw.PlattHybrid)
}

func computeWeightDigests(tw *TrainedWeights) (string, string) {
	modelPayload, err := json.Marshal(struct {
		U      [FeatureDim][LatentRank]float64
		V      [][LatentRank]float64
		MFBias []float64
		W      [][FeatureDim]float64
	}{tw.U, tw.V, tw.MFBias, tw.LogisticW})
	if err != nil {
		panic(fmt.Sprintf("ensoroutellm: marshal weights: %v", err))
	}
	calPayload, err := json.Marshal(struct {
		MF  []PlattParams
		LR  []PlattParams
		Hyb []PlattParams
	}{tw.PlattMF, tw.PlattLR, tw.PlattHybrid})
	if err != nil {
		panic(fmt.Sprintf("ensoroutellm: marshal calibration: %v", err))
	}
	mSum := sha256.Sum256(modelPayload)
	cSum := sha256.Sum256(calPayload)
	return "enso-w-" + hex.EncodeToString(mSum[:8]), "cal_" + hex.EncodeToString(cSum[:8])
}

func clampWeight(x, lim float64) float64 {
	if !isFinite(x) {
		return 0
	}
	if x > lim {
		return lim
	}
	if x < -lim {
		return -lim
	}
	return x
}

func rawMFLogit(u *[FeatureDim][LatentRank]float64, v [][LatentRank]float64, b []float64, x FeatureVec, m int) float64 {
	var q [LatentRank]float64
	for k := 0; k < LatentRank; k++ {
		var s float64
		for d := 0; d < FeatureDim; d++ {
			s += u[d][k] * x[d]
		}
		q[k] = s
	}
	out := b[m]
	for k := 0; k < LatentRank; k++ {
		out += q[k] * v[m][k]
	}
	return out
}

func rawLRLogit(w [][FeatureDim]float64, x FeatureVec, m int) float64 {
	var out float64
	wm := w[m]
	for d := 0; d < FeatureDim; d++ {
		out += wm[d] * x[d]
	}
	return out
}

func fitMatrixFactorization(samples []TrainingSample, armIdx map[string]int, numArms int, seed uint64) ([FeatureDim][LatentRank]float64, [][LatentRank]float64, []float64) {
	rng := NewRNG(seed)
	var u [FeatureDim][LatentRank]float64
	v := make([][LatentRank]float64, numArms)
	b := make([]float64, numArms)

	for d := 0; d < FeatureDim; d++ {
		for k := 0; k < LatentRank; k++ {
			u[d][k] = 0.08 * rng.NormFloat64()
		}
	}
	for m := 0; m < numArms; m++ {
		for k := 0; k < LatentRank; k++ {
			v[m][k] = 0.08 * rng.NormFloat64()
		}
	}

	// Initialize arm biases to empirical log-odds with Laplace smoothing.
	pos := make([]float64, numArms)
	cnt := make([]float64, numArms)
	for _, s := range samples {
		if m, ok := armIdx[s.ModelID]; ok {
			pos[m] += s.Passed
			cnt[m] += 1.0
		}
	}
	for m := 0; m < numArms; m++ {
		p := (pos[m] + 1.0) / (cnt[m] + 2.0)
		b[m] = math.Log(p / (1.0 - p))
	}

	if len(samples) == 0 {
		return u, v, b
	}

	order := make([]int, len(samples))
	for i := range order {
		order[i] = i
	}

	const epochs = 90
	const baseLR = 0.045
	const l2U = 0.0015
	const l2V = 0.0015

	for ep := 0; ep < epochs; ep++ {
		rng.Shuffle(len(order), func(i, j int) { order[i], order[j] = order[j], order[i] })
		lr := baseLR * (0.5 * (1.0 + math.Cos(math.Pi*float64(ep)/float64(epochs))))
		if lr < 0.003 {
			lr = 0.003
		}

		for _, idx := range order {
			s := samples[idx]
			m, ok := armIdx[s.ModelID]
			if !ok {
				continue
			}

			var q [LatentRank]float64
			for k := 0; k < LatentRank; k++ {
				var sum float64
				for d := 0; d < FeatureDim; d++ {
					sum += u[d][k] * s.Features[d]
				}
				q[k] = sum
			}
			logit := b[m]
			for k := 0; k < LatentRank; k++ {
				logit += q[k] * v[m][k]
			}
			pred := sigmoid(logit)
			err := pred - s.Passed

			b[m] = clampWeight(b[m]-lr*(err+0.002*b[m]), 25.0)
			for k := 0; k < LatentRank; k++ {
				vmk := v[m][k]
				vStep := clampWeight(err*q[k]+l2V*vmk, 10.0)
				v[m][k] = clampWeight(vmk-lr*vStep, 25.0)
				gradQ := err * vmk
				for d := 0; d < FeatureDim; d++ {
					if s.Features[d] != 0 {
						uStep := clampWeight(gradQ*s.Features[d]+l2U*u[d][k], 10.0)
						u[d][k] = clampWeight(u[d][k]-lr*uStep, 25.0)
					}
				}
			}
		}
	}

	// Exact damped Newton-IRLS refinement of each arm's (b_m, v_m) in the shared
	// K-dimensional query subspace q(x) = U^T x, regularized toward the cross-arm
	// mean latent skill vector so sparsely observed arms borrow shared structure.
	byArm := make([][]TrainingSample, numArms)
	for _, s := range samples {
		if m, ok := armIdx[s.ModelID]; ok {
			byArm[m] = append(byArm[m], s)
		}
	}
	var vMean [LatentRank]float64
	var activeArms float64
	for m := 0; m < numArms; m++ {
		if len(byArm[m]) > 0 {
			for k := 0; k < LatentRank; k++ {
				vMean[k] += v[m][k]
			}
			activeArms++
		}
	}
	if activeArms > 0 {
		for k := 0; k < LatentRank; k++ {
			vMean[k] /= activeArms
		}
	}

	const latentDim = LatentRank + 1
	for m := 0; m < numArms; m++ {
		armS := byArm[m]
		if len(armS) == 0 {
			v[m] = vMean
			continue
		}
		if len(armS) >= 60 {
			continue
		}
		for iter := 0; iter < 5; iter++ {
			var grad [latentDim]float64
			var hess [latentDim][latentDim]float64
			for k := 0; k < LatentRank; k++ {
				grad[k] = 0.85 * (v[m][k] - vMean[k])
				hess[k][k] = 0.85
			}
			grad[LatentRank] = 0.10 * b[m]
			hess[LatentRank][LatentRank] = 0.10

			for _, s := range armS {
				var q [latentDim]float64
				logit := b[m]
				for k := 0; k < LatentRank; k++ {
					var sum float64
					for d := 0; d < FeatureDim; d++ {
						sum += u[d][k] * s.Features[d]
					}
					q[k] = sum
					logit += sum * v[m][k]
				}
				q[LatentRank] = 1.0
				p := sigmoid(logit)
				diff := p - s.Passed
				wt := p * (1.0 - p)
				if wt < 1e-6 {
					wt = 1e-6
				}
				for i := 0; i < latentDim; i++ {
					grad[i] += diff * q[i]
					for j := i; j < latentDim; j++ {
						hess[i][j] += wt * q[i] * q[j]
					}
				}
			}
			for i := 0; i < latentDim; i++ {
				for j := 0; j < i; j++ {
					hess[i][j] = hess[j][i]
				}
			}
			step, ok := solveLinear9(hess, grad)
			if !ok {
				break
			}
			var maxDelta float64
			for k := 0; k < LatentRank; k++ {
				v[m][k] -= step[k]
				if math.Abs(step[k]) > maxDelta {
					maxDelta = math.Abs(step[k])
				}
			}
			b[m] -= step[LatentRank]
			if math.Abs(step[LatentRank]) > maxDelta {
				maxDelta = math.Abs(step[LatentRank])
			}
			if maxDelta < 1e-6 {
				break
			}
		}
	}

	return u, v, b
}

func solveLinear9(a [LatentRank + 1][LatentRank + 1]float64, b [LatentRank + 1]float64) ([LatentRank + 1]float64, bool) {
	const n = LatentRank + 1
	var aug [n][n + 1]float64
	for i := 0; i < n; i++ {
		for j := 0; j < n; j++ {
			aug[i][j] = a[i][j]
		}
		aug[i][n] = b[i]
	}
	for col := 0; col < n; col++ {
		pivot := col
		maxVal := math.Abs(aug[col][col])
		for row := col + 1; row < n; row++ {
			if v := math.Abs(aug[row][col]); v > maxVal {
				maxVal = v
				pivot = row
			}
		}
		if maxVal < 1e-12 {
			return [n]float64{}, false
		}
		if pivot != col {
			aug[col], aug[pivot] = aug[pivot], aug[col]
		}
		div := aug[col][col]
		for j := col; j <= n; j++ {
			aug[col][j] /= div
		}
		for row := 0; row < n; row++ {
			if row == col {
				continue
			}
			f := aug[row][col]
			if f == 0 {
				continue
			}
			for j := col; j <= n; j++ {
				aug[row][j] -= f * aug[col][j]
			}
		}
	}
	var x [n]float64
	for i := 0; i < n; i++ {
		if !isFinite(aug[i][n]) {
			return [n]float64{}, false
		}
		x[i] = aug[i][n]
	}
	return x, true
}

// fitPerArmLogisticIRLS solves L2-regularized binary logistic regression per
// arm via damped Newton-Raphson (IRLS) with Gauss-Jordan elimination on the
// 24x24 regularized Hessian.
func fitPerArmLogisticIRLS(samples []TrainingSample, armIdx map[string]int, numArms int) [][FeatureDim]float64 {
	byArm := make([][]TrainingSample, numArms)
	for _, s := range samples {
		if m, ok := armIdx[s.ModelID]; ok {
			byArm[m] = append(byArm[m], s)
		}
	}

	weights := make([][FeatureDim]float64, numArms)
	const ridge = 1.25
	const newtonSteps = 8

	for m := 0; m < numArms; m++ {
		armSamples := byArm[m]
		var w [FeatureDim]float64
		if len(armSamples) == 0 {
			weights[m] = w
			continue
		}

		for iter := 0; iter < newtonSteps; iter++ {
			var grad [FeatureDim]float64
			var hess [FeatureDim][FeatureDim]float64

			for d := 0; d < FeatureDim; d++ {
				reg := ridge
				if d == FeatureDim-1 {
					reg = 0.15 // lighter regularization on intercept
				}
				grad[d] = reg * w[d]
				hess[d][d] = reg
			}

			for _, s := range armSamples {
				var z float64
				for d := 0; d < FeatureDim; d++ {
					z += w[d] * s.Features[d]
				}
				p := sigmoid(z)
				diff := p - s.Passed
				wt := p * (1.0 - p)
				if wt < 1e-6 {
					wt = 1e-6
				}
				for i := 0; i < FeatureDim; i++ {
					xi := s.Features[i]
					if xi == 0 {
						continue
					}
					grad[i] += diff * xi
					for j := i; j < FeatureDim; j++ {
						hess[i][j] += wt * xi * s.Features[j]
					}
				}
			}
			for i := 0; i < FeatureDim; i++ {
				for j := 0; j < i; j++ {
					hess[i][j] = hess[j][i]
				}
			}

			step, ok := solveLinear(hess, grad)
			if !ok {
				break
			}
			var maxDelta float64
			for d := 0; d < FeatureDim; d++ {
				w[d] -= step[d]
				if math.Abs(step[d]) > maxDelta {
					maxDelta = math.Abs(step[d])
				}
			}
			if maxDelta < 1e-6 {
				break
			}
		}
		weights[m] = w
	}

	return weights
}

func solveLinear(a [FeatureDim][FeatureDim]float64, b [FeatureDim]float64) ([FeatureDim]float64, bool) {
	var aug [FeatureDim][FeatureDim + 1]float64
	for i := 0; i < FeatureDim; i++ {
		for j := 0; j < FeatureDim; j++ {
			aug[i][j] = a[i][j]
		}
		aug[i][FeatureDim] = b[i]
	}

	for col := 0; col < FeatureDim; col++ {
		pivot := col
		maxVal := math.Abs(aug[col][col])
		for row := col + 1; row < FeatureDim; row++ {
			if v := math.Abs(aug[row][col]); v > maxVal {
				maxVal = v
				pivot = row
			}
		}
		if maxVal < 1e-12 {
			return [FeatureDim]float64{}, false
		}
		if pivot != col {
			aug[col], aug[pivot] = aug[pivot], aug[col]
		}
		div := aug[col][col]
		for j := col; j <= FeatureDim; j++ {
			aug[col][j] /= div
		}
		for row := 0; row < FeatureDim; row++ {
			if row == col {
				continue
			}
			factor := aug[row][col]
			if factor == 0 {
				continue
			}
			for j := col; j <= FeatureDim; j++ {
				aug[row][j] -= factor * aug[col][j]
			}
		}
	}

	var x [FeatureDim]float64
	for i := 0; i < FeatureDim; i++ {
		if !isFinite(aug[i][FeatureDim]) {
			return [FeatureDim]float64{}, false
		}
		x[i] = aug[i][FeatureDim]
	}
	return x, true
}

// fitPlatt1D fits 2-parameter Platt calibration (alpha, beta) on out-of-fold
// logits with mild L2 shrinkage toward identity (alpha=1, beta=0) for stability
// on sparse arms.
func fitPlatt1D(logits, labels []float64) PlattParams {
	if len(logits) < 8 {
		return PlattParams{Alpha: 1.0, Beta: 0.0}
	}
	alpha := 1.0
	beta := 0.0
	const reg = 0.5

	for iter := 0; iter < 12; iter++ {
		gA := reg * (alpha - 1.0)
		gB := reg * beta
		hAA := reg
		hAB := 0.0
		hBB := reg

		for i, z := range logits {
			p := sigmoid(alpha*z + beta)
			diff := p - labels[i]
			w := p * (1.0 - p)
			if w < 1e-6 {
				w = 1e-6
			}
			gA += diff * z
			gB += diff
			hAA += w * z * z
			hAB += w * z
			hBB += w
		}
		det := hAA*hBB - hAB*hAB
		if math.Abs(det) < 1e-12 {
			break
		}
		dA := (hBB*gA - hAB*gB) / det
		dB := (-hAB*gA + hAA*gB) / det
		alpha -= dA
		beta -= dB
		if alpha < 0.15 {
			alpha = 0.15
		}
		if alpha > 4.0 {
			alpha = 4.0
		}
		if math.Abs(dA)+math.Abs(dB) < 1e-6 {
			break
		}
	}
	return PlattParams{Alpha: alpha, Beta: beta}
}

// RouteDecision is Enso's non-generative, auditable routing package (HIP-0510 §1,
// HIP-1332 §19, HIP-1345 §1.1 & §1.3).
type RouteDecision struct {
	RequestID              string                `json:"request_id"`
	Org                    string                `json:"org"`
	RoutedModel            string                `json:"routed_model"`
	XRoutedModelHeader     string                `json:"x_routed_model_header"`
	Provider               string                `json:"provider"`
	Strategy               RouterStrategy        `json:"strategy"`
	PredictedQuality       float64               `json:"predicted_quality"`
	MaxEligibleQuality     float64               `json:"max_eligible_quality"`
	EffectiveQualityFloor  float64               `json:"effective_quality_floor"`
	EstimatedCostMicros    int64                 `json:"estimated_cost_micros"`
	EstimatedLatencyMs     int                   `json:"estimated_latency_ms"`
	EligibleArmCount       int                   `json:"eligible_arm_count"`
	FallbackUsed           string                `json:"fallback_used,omitempty"`
	StrippedThinkingBlocks int                   `json:"stripped_thinking_blocks"`
	SanitizedMessages      []ConversationMessage `json:"sanitized_messages,omitempty"`

	// HIP-1345 §1.1 & §1.3 + HIP-1332 §19 provenance fields
	CacheSalt           string `json:"cache_salt"`
	RuntimeDigest       string `json:"runtime_digest"`
	CacheConnector      string `json:"cache_connector"`
	RouterModelRevision string `json:"router_model_revision"`
	CalibrationRevision string `json:"calibration_revision"`
	DecisionLatencyNs   int64  `json:"decision_latency_ns"`
}

// ContentFreeLedgerRecord stores routed features and observed reward WITHOUT
// retaining any prompt or completion text (HIP-0510 §2 & Security Considerations).
type ContentFreeLedgerRecord struct {
	RequestID           string     `json:"request_id"`
	Org                 string     `json:"org"`
	RoutedModel         string     `json:"routed_model"`
	Features            FeatureVec `json:"features"`
	EstimatedCostMicros int64      `json:"estimated_cost_micros"`
	Reward              *float64   `json:"reward,omitempty"`
}

// RetrainVerdict summarizes one HIP-0510 §3.2 promote-or-keep retrain cycle.
type RetrainVerdict struct {
	Scope               string  `json:"scope"`
	SampleCount         int     `json:"sample_count"`
	IncumbentUtility    float64 `json:"incumbent_utility"`
	CandidateUtility    float64 `json:"candidate_utility"`
	MinRequiredGain     float64 `json:"min_required_gain"`
	Promoted            bool    `json:"promoted"`
	ActiveModelRevision string  `json:"active_model_revision"`
}

type orgProjectKey struct {
	org     string
	project string
}

type orgRequestKey struct {
	org       string
	requestID string
}

// EnsoRouter is the concurrent, multi-tenant Enso routing engine.
// Lookups run under RLock in microseconds on CPU; policy updates, feedback
// attachment, and gated weight hot-swaps acquire Lock cleanly.
type EnsoRouter struct {
	mu          sync.RWMutex
	catalog     []ModelArm
	catalogByID map[string]ModelArm

	platformPolicy *ScopePolicy
	orgPolicies    map[string]ScopePolicy
	projPolicies   map[orgProjectKey]ScopePolicy

	baseWeights *TrainedWeights
	orgWeights  map[string]*TrainedWeights

	ledger map[orgRequestKey]ContentFreeLedgerRecord
}

// NewEnsoRouter constructs a thread-safe EnsoRouter over catalog and optional
// initial trained baseWeights (when nil, cold-start static rules serve safely).
func NewEnsoRouter(catalog []ModelArm, baseWeights *TrainedWeights) *EnsoRouter {
	cp := make([]ModelArm, len(catalog))
	byID := make(map[string]ModelArm, len(catalog))
	for i, a := range catalog {
		cp[i] = a
		byID[a.ID] = a
	}
	return &EnsoRouter{
		catalog:      cp,
		catalogByID:  byID,
		orgPolicies:  make(map[string]ScopePolicy),
		projPolicies: make(map[orgProjectKey]ScopePolicy),
		baseWeights:  baseWeights,
		orgWeights:   make(map[string]*TrainedWeights),
		ledger:       make(map[orgRequestKey]ContentFreeLedgerRecord),
	}
}

// SetPlatformPolicy updates the global "*" policy defaults.
func (r *EnsoRouter) SetPlatformPolicy(p ScopePolicy) {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := p.Clone()
	r.platformPolicy = &cp
}

// SetOrgPolicy updates one tenant org's routing policy row.
func (r *EnsoRouter) SetOrgPolicy(org string, p ScopePolicy) {
	if !ValidateScopeIdentifier(org, false) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.orgPolicies[org] = p.Clone()
}

// SetProjectPolicy updates an org/project sub-scope policy row (narrow-only).
func (r *EnsoRouter) SetProjectPolicy(org, project string, p ScopePolicy) {
	if !ValidateScopeIdentifier(org, false) || !ValidateScopeIdentifier(project, false) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.projPolicies[orgProjectKey{org: org, project: project}] = p.Clone()
}

// EffectivePolicyFor resolves platform -> org -> project narrow-only policy.
func (r *EnsoRouter) EffectivePolicyFor(org, project string) EffectivePolicy {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.effectivePolicyLocked(org, project)
}

func (r *EnsoRouter) effectivePolicyLocked(org, project string) EffectivePolicy {
	var orgPtr, projPtr *ScopePolicy
	if op, ok := r.orgPolicies[org]; ok {
		cp := op.Clone()
		orgPtr = &cp
	}
	if project != "" {
		if pp, ok := r.projPolicies[orgProjectKey{org: org, project: project}]; ok {
			cp := pp.Clone()
			projPtr = &cp
		}
	}
	return FoldPolicies(org, project, r.platformPolicy, orgPtr, projPtr)
}

// Route selects one servable model for req under strategy, records a content-free
// ledger entry when req.RequestID != "", and returns the structured RouteDecision.
func (r *EnsoRouter) Route(req RouteRequest, strategy RouterStrategy) (RouteDecision, error) {
	start := time.Now()
	if !strategy.IsValid() {
		return RouteDecision{}, &RouteError{
			Code:       ErrCodeInvalidRequestParams,
			HTTPStatus: 400,
			Message:    fmt.Sprintf("unsupported router strategy %q", strategy),
		}
	}

	r.mu.RLock()
	err := ValidateRequestSemantics(req, r.catalogByID)
	if err != nil {
		r.mu.RUnlock()
		return RouteDecision{}, err
	}

	pol := r.effectivePolicyLocked(req.Org, req.Project)
	weights := r.orgWeights[req.Org]
	if weights == nil {
		weights = r.baseWeights
	}
	catalog := r.catalog
	r.mu.RUnlock()

	inTok, outTok := ResolveTokenEstimates(req)

	// Fast-mode fail-closed pre-execution budget check (HIP-1348 §1.1–§1.2)
	// for constrained strategies.
	if strategy != StrategyUnconstrainedRouteLLM && req.FastMode {
		if !req.HasPrepaidBalance || req.PrepaidBalanceMicros <= 0 {
			return RouteDecision{}, NewBudgetExhaustedRouteError(
				"tenant_ledger",
				"fast_mode requires positive prepaid_balance_micros in tenant_ledger",
			)
		}
	}

	var eligible []ModelArm
	rejected := make(map[string]GateName)

	if strategy == StrategyUnconstrainedRouteLLM {
		// Upstream cost-quality baseline WITHOUT HIP-1345 capability/entitlement gates.
		eligible = append(eligible, catalog...)
	} else {
		for _, arm := range catalog {
			ok, gate := CheckCapabilityGates(arm, req, pol)
			if ok {
				eligible = append(eligible, arm)
			} else {
				rejected[arm.ID] = gate
			}
		}
	}

	if len(eligible) == 0 {
		if req.FastMode {
			// Check whether at least one fast-capable arm failed solely due to
			// insufficient prepaid balance -> attribute to tenant_ledger per HIP-1348 §1.2!
			for _, arm := range catalog {
				if arm.FastModeCapable && rejected[arm.ID] == GateFastModePrepaid {
					return RouteDecision{}, NewBudgetExhaustedRouteError(
						"tenant_ledger",
						fmt.Sprintf("prepaid balance %d micro-USD below fast_mode request cost", req.PrepaidBalanceMicros),
					)
				}
			}
		}
		return RouteDecision{}, &RouteError{
			Code:         ErrCodeNoEligibleModel,
			HTTPStatus:   422,
			RejectedArms: rejected,
			Message:      "zero candidate models satisfied deterministic capability and entitlement gates",
		}
	}

	var feats FeatureVec
	if req.PreExtractedFeatures != nil {
		feats = *req.PreExtractedFeatures
	} else {
		feats = ExtractFeatures(req)
	}
	chosen, predQ, maxQ, effFloor, fallback, routeErr := selectModelArm(eligible, feats, req, pol, weights, strategy, inTok, outTok)
	if routeErr != nil {
		return RouteDecision{}, routeErr
	}

	var sanitizedMsgs []ConversationMessage
	stripped := 0
	if strategy == StrategyUnconstrainedRouteLLM {
		// Upstream unconstrained baseline performs no HIP-1345 §2.6 thinking-block sanitization.
		if len(req.Messages) > 0 {
			sanitizedMsgs = make([]ConversationMessage, len(req.Messages))
			for i, m := range req.Messages {
				sanitizedMsgs[i] = m.Clone()
			}
		}
	} else {
		sanitizedMsgs, stripped = SanitizeConversationMessages(req.Messages, chosen)
	}
	costMicros := EstimateCostMicros(chosen, inTok, outTok)
	latMs := EstimateLatencyMs(chosen, outTok)

	modelRev := "cold-start-rules/v1"
	calRev := "uncalibrated"
	if weights != nil {
		modelRev = weights.RouterModelRevision
		calRev = weights.CalibrationRevision
	}

	elapsedNs := time.Since(start).Nanoseconds()
	if elapsedNs <= 0 {
		elapsedNs = 100 // Windows timer granularity floor (~100ns)
	}

	decision := RouteDecision{
		RequestID:              req.RequestID,
		Org:                    req.Org,
		RoutedModel:            chosen.ID,
		XRoutedModelHeader:     chosen.ID,
		Provider:               chosen.Provider,
		Strategy:               strategy,
		PredictedQuality:       predQ,
		MaxEligibleQuality:     maxQ,
		EffectiveQualityFloor:  effFloor,
		EstimatedCostMicros:    costMicros,
		EstimatedLatencyMs:     latMs,
		EligibleArmCount:       len(eligible),
		FallbackUsed:           fallback,
		StrippedThinkingBlocks: stripped,
		SanitizedMessages:      sanitizedMsgs,
		CacheSalt:              "hzo-" + req.Org,
		RuntimeDigest:          chosen.RuntimeDigest,
		CacheConnector:         chosen.CacheConnector,
		RouterModelRevision:    modelRev,
		CalibrationRevision:    calRev,
		DecisionLatencyNs:      elapsedNs,
	}

	if req.RequestID != "" {
		lk := orgRequestKey{org: req.Org, requestID: req.RequestID}
		r.mu.Lock()
		prevReward := (*float64)(nil)
		if existing, ok := r.ledger[lk]; ok && existing.Reward != nil {
			val := *existing.Reward
			prevReward = &val
		}
		r.ledger[lk] = ContentFreeLedgerRecord{
			RequestID:           req.RequestID,
			Org:                 req.Org,
			RoutedModel:         chosen.ID,
			Features:            feats,
			EstimatedCostMicros: costMicros,
			Reward:              prevReward,
		}
		r.mu.Unlock()
	}

	return decision, nil
}

type scoredCandidate struct {
	arm     ModelArm
	quality float64
	cost    int64
	blended float64
	latMs   int
}

func selectModelArm(
	eligible []ModelArm,
	feats FeatureVec,
	req RouteRequest,
	pol EffectivePolicy,
	weights *TrainedWeights,
	strategy RouterStrategy,
	inTok, outTok int,
) (ModelArm, float64, float64, float64, string, error) {
	predKind := PredictorHybrid
	if strategy == StrategyRouteLLMMFConstrained {
		predKind = PredictorMFOnly
	} else if strategy == StrategyLogisticConstrained {
		predKind = PredictorLROnly
	}

	scored := make([]scoredCandidate, len(eligible))
	maxQ := -1.0

	for i, arm := range eligible {
		q := 0.5
		if weights != nil {
			if pq, ok := weights.PredictQualityByKind(feats, arm.ID, predKind); ok {
				q = pq
			}
		}
		if q > maxQ {
			maxQ = q
		}
		scored[i] = scoredCandidate{
			arm:     arm,
			quality: q,
			cost:    EstimateCostMicros(arm, inTok, outTok),
			blended: arm.BlendedUSDPerMTok(),
			latMs:   EstimateLatencyMs(arm, outTok),
		}
	}

	switch strategy {
	case StrategyAlwaysStrong:
		sort.Slice(scored, func(i, j int) bool {
			if scored[i].blended != scored[j].blended {
				return scored[i].blended > scored[j].blended
			}
			if scored[i].quality != scored[j].quality {
				return scored[i].quality > scored[j].quality
			}
			return scored[i].arm.ID < scored[j].arm.ID
		})
		win := scored[0]
		return win.arm, win.quality, maxQ, 0, "", nil

	case StrategyAlwaysWeak:
		sort.Slice(scored, func(i, j int) bool {
			if scored[i].cost != scored[j].cost {
				return scored[i].cost < scored[j].cost
			}
			if scored[i].blended != scored[j].blended {
				return scored[i].blended < scored[j].blended
			}
			if scored[i].latMs != scored[j].latMs {
				return scored[i].latMs < scored[j].latMs
			}
			return scored[i].arm.ID < scored[j].arm.ID
		})
		win := scored[0]
		return win.arm, win.quality, maxQ, 0, "", nil

	case StrategyRandom:
		h := fnv.New64a()
		_, _ = h.Write([]byte(req.Org + ":" + req.RequestID))
		if req.RequestID == "" {
			_, _ = h.Write([]byte(":" + req.Prompt))
		}
		if req.RequestID == "" && req.Prompt == "" {
			var b [8]byte
			for d := 0; d < FeatureDim; d++ {
				bits := math.Float64bits(feats[d])
				for k := 0; k < 8; k++ {
					b[k] = byte(bits >> (8 * k))
				}
				_, _ = h.Write(b[:])
			}
		}
		rng := NewRNG(h.Sum64())
		win := scored[rng.IntN(len(scored))]
		return win.arm, win.quality, maxQ, 0, "", nil

	case StrategyStaticRule:
		return selectStaticRuleArm(scored, feats, maxQ)
	}

	// Cold-start fallback to static rule router if no weights are loaded yet (HIP-0510 §1).
	if weights == nil {
		arm, q, mq, fl, _, err := selectStaticRuleArm(scored, feats, maxQ)
		return arm, q, mq, fl, "cold_start_rule", err
	}

	// Compute effective quality floor:
	// pol.MinQualityFloor acts strictly as an additive lower bound and never
	// disables the default 4% relative quality retention target when no explicit
	// per-request floor/tolerance was specified.
	hasExplicitReqConstraint := req.QualityFloor > 0 || req.QualityTolerance > 0 || req.QualityRetentionRatio > 0
	effFloor := pol.MinQualityFloor
	if req.QualityFloor > effFloor {
		effFloor = req.QualityFloor
	}
	if req.QualityTolerance > 0 || req.QualityRetentionRatio > 0 {
		ratio := req.QualityRetentionRatio
		if ratio == 0 {
			ratio = 1.0
		}
		relFloor := ratio*maxQ - req.QualityTolerance
		if relFloor > effFloor {
			effFloor = relFloor
		}
	}
	if !hasExplicitReqConstraint && maxQ-0.04 > effFloor {
		effFloor = maxQ - 0.04
	}
	if effFloor < 0 {
		effFloor = 0
	}

	// Filter to candidates meeting effFloor and pick the lowest-cost arm
	// (breaking ties by unrounded blended $/MTok, latency, quality, then ID).
	var passing []scoredCandidate
	for _, sc := range scored {
		if sc.quality+1e-9 >= effFloor {
			passing = append(passing, sc)
		}
	}

	if len(passing) > 0 {
		sort.Slice(passing, func(i, j int) bool {
			if passing[i].cost != passing[j].cost {
				return passing[i].cost < passing[j].cost
			}
			if passing[i].blended != passing[j].blended {
				return passing[i].blended < passing[j].blended
			}
			if passing[i].latMs != passing[j].latMs {
				return passing[i].latMs < passing[j].latMs
			}
			if passing[i].quality != passing[j].quality {
				return passing[i].quality > passing[j].quality
			}
			return passing[i].arm.ID < passing[j].arm.ID
		})
		win := passing[0]
		return win.arm, win.quality, maxQ, effFloor, "", nil
	}

	// No eligible arm met an aggressive absolute floor:
	if req.StrictQualityFloor {
		return ModelArm{}, 0, maxQ, effFloor, "", &RouteError{
			Code:       ErrCodeQualityFloorUnmet,
			HTTPStatus: 422,
			Message:    fmt.Sprintf("highest eligible predicted quality %.4f is below strict quality_floor %.4f", maxQ, effFloor),
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		if scored[i].quality != scored[j].quality {
			return scored[i].quality > scored[j].quality
		}
		if scored[i].cost != scored[j].cost {
			return scored[i].cost < scored[j].cost
		}
		return scored[i].arm.ID < scored[j].arm.ID
	})
	win := scored[0]
	return win.arm, win.quality, maxQ, effFloor, "best_eligible_below_floor", nil
}

func selectStaticRuleArm(scored []scoredCandidate, feats FeatureVec, maxQ float64) (ModelArm, float64, float64, float64, string, error) {
	// HIP-0510 §1 cold-start static bucket table: maps coarse keyword buckets to a
	// fixed default tier without intra-bucket complexity or calibration awareness.
	bucket := ClassifyCoarseBucket(feats)
	var preferOrder []string
	switch bucket {
	case BucketCode, BucketToolSchema:
		preferOrder = []string{"zen-4-pro", "zen-4-coder", "frontier-claude-sonnet", "zen-4-max", "zen-4-mini", "zen-4-flash"}
	case BucketMath, BucketReasoning:
		preferOrder = []string{"zen-4-pro", "zen-4-thinking", "zen-4-max", "frontier-claude-sonnet", "zen-4-mini"}
	case BucketLongContext:
		preferOrder = []string{"zen-4-pro", "zen-4-max", "frontier-claude-sonnet", "zen-4-mini", "zen-4-flash"}
	case BucketCheapChat:
		preferOrder = []string{"zen-4-flash", "zen-4-mini", "zen-4-pro"}
	default:
		preferOrder = []string{"zen-4-mini", "zen-4-pro", "zen-4-flash", "zen-4-max"}
	}

	byID := make(map[string]scoredCandidate, len(scored))
	for _, sc := range scored {
		byID[sc.arm.ID] = sc
	}
	for _, id := range preferOrder {
		if sc, ok := byID[id]; ok {
			return sc.arm, sc.quality, maxQ, 0, "", nil
		}
	}
	// Fallback to cheapest eligible if none of preferred list is in pool.
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].cost != scored[j].cost {
			return scored[i].cost < scored[j].cost
		}
		if scored[i].blended != scored[j].blended {
			return scored[i].blended < scored[j].blended
		}
		return scored[i].arm.ID < scored[j].arm.ID
	})
	return scored[0].arm, scored[0].quality, maxQ, 0, "", nil
}

// RecordFeedback attaches a [0, 1] quality reward to a previously routed
// RequestID, enforcing strict tenant isolation (HIP-0510 §2: cross-org request
// IDs are rejected) and idempotent reward recording.
func (r *EnsoRouter) RecordFeedback(callerOrg, requestID string, reward float64) error {
	if callerOrg == "" {
		return &RouteError{Code: ErrCodeUnauthorizedOrg, HTTPStatus: 403, Message: "empty caller org"}
	}
	if !ValidateScopeIdentifier(callerOrg, false) || strings.TrimSpace(requestID) == "" {
		return &RouteError{Code: ErrCodeInvalidRequestParams, HTTPStatus: 400, Message: "invalid caller org or empty request_id"}
	}
	if !isFinite(reward) || reward < 0 || reward > 1.0 {
		return &RouteError{Code: ErrCodeInvalidRequestParams, HTTPStatus: 400, Message: "reward must be a finite number in [0.0, 1.0]"}
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	lk := orgRequestKey{org: callerOrg, requestID: requestID}
	rec, exists := r.ledger[lk]
	if !exists {
		return &RouteError{
			Code:       ErrCodeCrossTenantDenied,
			HTTPStatus: 404,
			Message:    "request_id not found in caller org",
		}
	}
	val := reward
	rec.Reward = &val
	r.ledger[lk] = rec
	return nil
}

// ExportOrgLedger returns a defensive copy of content-free ledger records for
// callerOrg (HIP-0510 §7 data ownership).
func (r *EnsoRouter) ExportOrgLedger(callerOrg string) ([]ContentFreeLedgerRecord, error) {
	if callerOrg == "" {
		return nil, &RouteError{Code: ErrCodeUnauthorizedOrg, HTTPStatus: 403, Message: "empty caller org"}
	}
	if !ValidateScopeIdentifier(callerOrg, false) {
		return nil, &RouteError{Code: ErrCodeInvalidRequestParams, HTTPStatus: 400, Message: "invalid caller org identifier"}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []ContentFreeLedgerRecord
	for k, rec := range r.ledger {
		if k.org == callerOrg {
			cp := rec
			if rec.Reward != nil {
				v := *rec.Reward
				cp.Reward = &v
			}
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestID < out[j].RequestID })
	return out, nil
}

// ExportOptInGlobalTrainingSamples exports rewarded content-free training tuples
// strictly from orgs that explicitly opted into shared base training
// (TrainingOptIn == true per HIP-0510 §3.1).
func (r *EnsoRouter) ExportOptInGlobalTrainingSamples() []TrainingSample {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var out []TrainingSample
	for k, rec := range r.ledger {
		if rec.Reward == nil {
			continue
		}
		pol := r.effectivePolicyLocked(k.org, "")
		if !pol.TrainingOptIn {
			continue
		}
		out = append(out, TrainingSample{
			QueryID:  k.org + ":" + rec.RequestID,
			Features: rec.Features,
			ModelID:  rec.RoutedModel,
			Passed:   *rec.Reward,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QueryID < out[j].QueryID })
	return out
}

// EvalQueryGroundTruth holds ground-truth per-arm outcomes on one held-out query
// for promote-or-keep gating and benchmark evaluation.
type EvalQueryGroundTruth struct {
	QueryID     string
	Request     RouteRequest
	ArmOutcomes map[string]float64 // 1.0 pass / 0.0 fail
}

// RetrainOrgPromoteOrKeep implements HIP-0510 §3.2 ("fit -> gate -> deploy -> publish"):
// fits a candidate per-org model from orgTrainSamples, then under Lock evaluates
// net utility (quality - costWeight * USD_cost) on heldOut against the org's
// current live incumbent weights so concurrent retrains can never regress live state.
func (r *EnsoRouter) RetrainOrgPromoteOrKeep(
	org string,
	orgTrainSamples []TrainingSample,
	heldOut []EvalQueryGroundTruth,
	costWeight float64,
	minRequiredGain float64,
	seed uint64,
) (RetrainVerdict, error) {
	if org == "" {
		return RetrainVerdict{}, &RouteError{Code: ErrCodeUnauthorizedOrg, HTTPStatus: 403, Message: "empty org"}
	}
	if !ValidateScopeIdentifier(org, false) || !isFinite(costWeight) || costWeight < 0 || !isFinite(minRequiredGain) || minRequiredGain < 0 {
		return RetrainVerdict{}, &RouteError{Code: ErrCodeInvalidRequestParams, HTTPStatus: 400, Message: "invalid org or non-finite/negative retrain parameters"}
	}
	r.mu.RLock()
	catalog := r.catalog
	r.mu.RUnlock()

	candidate := FitTrainedWeights(orgTrainSamples, catalog, seed)

	// Snapshot incumbent and per-item effective policies under RLock so held-out
	// evaluation runs outside the exclusive write lock (avoiding stalling Route() readers).
	r.mu.RLock()
	snapIncumbent := r.orgWeights[org]
	if snapIncumbent == nil {
		snapIncumbent = r.baseWeights
	}
	itemPols := make([]EffectivePolicy, len(heldOut))
	for i, item := range heldOut {
		itemPols[i] = r.effectivePolicyLocked(org, item.Request.Project)
	}
	r.mu.RUnlock()

	incUtil, incCount := evaluateWeightsUtilityPerItem(catalog, itemPols, snapIncumbent, heldOut, costWeight)
	candUtil, candCount := evaluateWeightsUtilityPerItem(catalog, itemPols, candidate, heldOut, costWeight)

	r.mu.Lock()
	defer r.mu.Unlock()

	liveIncumbent := r.orgWeights[org]
	if liveIncumbent == nil {
		liveIncumbent = r.baseWeights
	}
	if liveIncumbent != snapIncumbent {
		// Re-evaluate against the updated live incumbent if a concurrent retrain won first.
		for i, item := range heldOut {
			itemPols[i] = r.effectivePolicyLocked(org, item.Request.Project)
		}
		incUtil, incCount = evaluateWeightsUtilityPerItem(catalog, itemPols, liveIncumbent, heldOut, costWeight)
		candUtil, candCount = evaluateWeightsUtilityPerItem(catalog, itemPols, candidate, heldOut, costWeight)
	}

	verdict := RetrainVerdict{
		Scope:            org,
		SampleCount:      candidate.SampleCount,
		IncumbentUtility: math.Round(incUtil*10_000) / 10_000,
		CandidateUtility: math.Round(candUtil*10_000) / 10_000,
		MinRequiredGain:  minRequiredGain,
	}

	if incCount > 0 && candCount > 0 && isFinite(incUtil) && isFinite(candUtil) && candUtil >= incUtil+minRequiredGain {
		r.orgWeights[org] = candidate
		verdict.Promoted = true
		verdict.ActiveModelRevision = candidate.RouterModelRevision
	} else {
		verdict.Promoted = false
		if liveIncumbent != nil {
			verdict.ActiveModelRevision = liveIncumbent.RouterModelRevision
		} else {
			verdict.ActiveModelRevision = "cold-start-rules/v1"
		}
	}
	return verdict, nil
}

func evaluateWeightsUtilityPerItem(catalog []ModelArm, pols []EffectivePolicy, w *TrainedWeights, heldOut []EvalQueryGroundTruth, costWeight float64) (float64, int) {
	if len(heldOut) == 0 {
		return 0, 0
	}
	var sum float64
	var counted int
	for idx, item := range heldOut {
		pol := pols[idx]
		req := item.Request
		var elig []ModelArm
		for _, arm := range catalog {
			if len(item.ArmOutcomes) > 0 {
				if _, hasLabel := item.ArmOutcomes[arm.ID]; !hasLabel {
					continue
				}
			}
			if ok, _ := CheckCapabilityGates(arm, req, pol); ok {
				elig = append(elig, arm)
			}
		}
		if len(elig) == 0 {
			continue
		}
		var feats FeatureVec
		if req.PreExtractedFeatures != nil {
			feats = *req.PreExtractedFeatures
		} else {
			feats = ExtractFeatures(req)
		}
		inTok, outTok := ResolveTokenEstimates(req)
		chosen, _, _, _, _, err := selectModelArm(elig, feats, req, pol, w, StrategyEnsoHybridConstrained, inTok, outTok)
		if err != nil {
			continue
		}
		reward, ok := item.ArmOutcomes[chosen.ID]
		if !ok || !isFinite(reward) {
			continue
		}
		costUSD := float64(EstimateCostMicros(chosen, inTok, outTok)) / 1_000_000.0
		sum += reward - costWeight*costUSD
		counted++
	}
	if counted == 0 {
		return 0, 0
	}
	return sum / float64(counted), counted
}
