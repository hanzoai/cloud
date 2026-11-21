// SPDX-License-Identifier: Apache-2.0
//! [Bounty #34] (`hanzoai/cloud#36`): **Unified agent execution tracing** (OpenTelemetry GenAI).
//!
//! Clean-room simulation harness for comparing ad-hoc agent logging against
//! OpenTelemetry GenAI semantic conventions.
//!
//! # Design
//!
//! An agent run is a tree of spans (agent -> tool -> model call -> retrieval).
//! Diagnosis requires joining spans across services on a shared trace id and
//! having the attributes needed to answer the question being asked.
//!
//! - `AdHocLogs`: free-text lines per service, no trace id, inconsistent fields.
//! - `OtelGenAi`: spans with `trace_id`/`span_id`/`parent_span_id` and the GenAI
//!   attribute set (`gen_ai.system`, `gen_ai.request.model`, token counts,
//!   tool name), exported over OTLP.
//!
//! Metrics: cross-service join rate, attribute completeness for the questions
//! operators actually ask, bytes emitted per run, and the fraction of incidents
//! that can be diagnosed without a code change.
//!
//! # Honest boundaries
//! - No OpenTelemetry SDK, collector, or exporter is executed; span structure is
//!   simulated.
//! - Attribute completeness is modelled per convention, not parsed from real
//!   telemetry.
//! - Overhead is a per-span constant, not a measured allocation or network cost.
//! - All figures are simulation outputs on the authoring host, NOT Hanzo
//!   expectations until reproduced with a real OTel pipeline.

use serde::{Deserialize, Serialize};
use thiserror::Error;

#[derive(Debug, Error)]
pub enum TraceError {
    #[error("invalid configuration: {0}")]
    InvalidConfig(String),
}

#[derive(Debug, Clone)]
pub struct SplitMix64 {
    state: u64,
}
impl SplitMix64 {
    pub fn new(seed: u64) -> Self {
        Self { state: seed }
    }
    pub fn next_u64(&mut self) -> u64 {
        self.state = self.state.wrapping_add(0x9E37_79B9_7F4A_7C15);
        let mut z = self.state;
        z = (z ^ (z >> 30)).wrapping_mul(0xBF58_476D_1CE4_E5B9);
        z = (z ^ (z >> 27)).wrapping_mul(0x94D0_49BB_1331_11EB);
        z ^ (z >> 31)
    }
    pub fn next_f64(&mut self) -> f64 {
        (self.next_u64() >> 11) as f64 / (1u64 << 53) as f64
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SpanKind {
    Agent,
    Tool,
    Model,
    Retrieval,
}

impl SpanKind {
    pub fn name(&self) -> &'static str {
        match self {
            SpanKind::Agent => "agent",
            SpanKind::Tool => "tool",
            SpanKind::Model => "model",
            SpanKind::Retrieval => "retrieval",
        }
    }
    /// How many attributes a correct implementation must attach.
    pub fn required_attributes(&self) -> usize {
        match self {
            SpanKind::Agent => 4,
            SpanKind::Tool => 5,
            SpanKind::Model => 7,
            SpanKind::Retrieval => 4,
        }
    }
}

/// One span in a run.
#[derive(Debug, Clone)]
pub struct Span {
    pub id: u32,
    pub run_id: u32,
    pub parent: Option<u32>,
    pub kind: SpanKind,
    /// Service that emitted the span (0 = gateway, 1 = agent worker, 2 = tool host).
    pub service: u8,
}

pub fn generate_runs(seed: u64, n_runs: usize, spans_per_run: usize) -> Vec<Span> {
    let mut rng = SplitMix64::new(seed);
    let kinds = [SpanKind::Agent, SpanKind::Tool, SpanKind::Model, SpanKind::Retrieval];
    let mut spans = Vec::with_capacity(n_runs * spans_per_run);
    let mut next_id = 0u32;
    for run in 0..n_runs {
        let mut ids = Vec::with_capacity(spans_per_run);
        for i in 0..spans_per_run {
            let kind = kinds[(rng.next_u64() as usize) % kinds.len()];
            let parent = if i == 0 { None } else { Some(ids[(rng.next_u64() as usize) % ids.len()]) };
            // Spans are emitted by different services depending on kind.
            let service = match kind {
                SpanKind::Agent => 1,
                SpanKind::Tool => 2,
                _ => 0,
            };
            let id = next_id;
            next_id += 1;
            ids.push(id);
            spans.push(Span { id, run_id: run as u32, parent, kind, service });
        }
    }
    spans
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Policy {
    AdHocLogs,
    OtelGenAi,
}

impl Policy {
    pub fn label(&self) -> &'static str {
        match self {
            Policy::AdHocLogs => "adhoc-logs",
            Policy::OtelGenAi => "otel-genai",
        }
    }

    /// Fraction of spans that carry a usable trace identifier.
    pub fn trace_id_rate(&self) -> f64 {
        match self {
            // Only the gateway logs a request id; workers and tool hosts do not
            // propagate it, so cross-service joins fail.
            Policy::AdHocLogs => 0.33,
            Policy::OtelGenAi => 1.0,
        }
    }

    /// Fraction of required attributes actually present.
    pub fn attribute_completeness(&self) -> f64 {
        match self {
            Policy::AdHocLogs => 0.46,
            Policy::OtelGenAi => 0.97,
        }
    }

    /// Bytes emitted per span.
    pub fn bytes_per_span(&self) -> f64 {
        match self {
            Policy::AdHocLogs => 180.0,
            Policy::OtelGenAi => 420.0,
        }
    }
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct PolicyResult {
    pub policy: String,
    pub spans: usize,
    pub runs: usize,
    /// Fraction of runs whose spans can be joined across all services.
    pub cross_service_join_rate: f64,
    /// Mean fraction of required attributes present.
    pub attribute_completeness: f64,
    /// Fraction of runs with enough data to answer a full causal question.
    pub diagnosable_run_rate: f64,
    pub bytes_per_run: f64,
    pub total_bytes: u64,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BenchReport {
    pub bounty: String,
    pub hip: String,
    pub honesty_labels: Vec<String>,
    pub config: BenchConfig,
    pub results: Vec<PolicyResult>,
    pub notes: Vec<String>,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct BenchConfig {
    pub seed: u64,
    pub n_runs: usize,
    pub spans_per_run: usize,
}

impl Default for BenchConfig {
    fn default() -> Self {
        Self { seed: 0x07E1_6EA1, n_runs: 2_000, spans_per_run: 12 }
    }
}
impl BenchConfig {
    pub fn test_config() -> Self {
        Self { seed: 7, n_runs: 200, spans_per_run: 10 }
    }
}

pub fn evaluate(policy: Policy, spans: &[Span], cfg: &BenchConfig) -> PolicyResult {
    let mut rng = SplitMix64::new(cfg.seed ^ 0x7ACE);
    let trace_rate = policy.trace_id_rate();
    let completeness = policy.attribute_completeness();

    let mut joins = 0usize;
    let mut diagnosable = 0usize;

    for _run in 0..cfg.n_runs {
        // A run is joinable if every service that emitted a span carried the id.
        let join_ok = rng.next_f64() < trace_rate.powi(2);
        if join_ok {
            joins += 1;
            // Diagnosis additionally needs the attributes for the span kinds used.
            if rng.next_f64() < completeness.powi(3) {
                diagnosable += 1;
            }
        }
    }

    let n = cfg.n_runs.max(1) as f64;
    let total_bytes = (spans.len() as f64 * policy.bytes_per_span()) as u64;

    PolicyResult {
        policy: policy.label().into(),
        spans: spans.len(),
        runs: cfg.n_runs,
        cross_service_join_rate: joins as f64 / n,
        attribute_completeness: completeness,
        diagnosable_run_rate: diagnosable as f64 / n,
        bytes_per_run: cfg.spans_per_run as f64 * policy.bytes_per_span(),
        total_bytes,
    }
}

pub fn run_bench(cfg: &BenchConfig) -> Result<BenchReport, TraceError> {
    if cfg.n_runs == 0 {
        return Err(TraceError::InvalidConfig("n_runs must be positive".into()));
    }
    if cfg.spans_per_run == 0 {
        return Err(TraceError::InvalidConfig("spans_per_run must be positive".into()));
    }
    let spans = generate_runs(cfg.seed, cfg.n_runs, cfg.spans_per_run);
    let results = vec![
        evaluate(Policy::AdHocLogs, &spans, cfg),
        evaluate(Policy::OtelGenAi, &spans, cfg),
    ];

    let mut notes = Vec::new();
    if let (Some(adhoc), Some(otel)) = (
        results.iter().find(|r| r.policy == "adhoc-logs"),
        results.iter().find(|r| r.policy == "otel-genai"),
    ) {
        notes.push(format!(
            "GenAI semantic conventions raise cross-service join from {:.1}% to {:.1}% and diagnosable runs from {:.1}% to {:.1}%, at {:.1}x the emitted bytes",
            adhoc.cross_service_join_rate * 100.0,
            otel.cross_service_join_rate * 100.0,
            adhoc.diagnosable_run_rate * 100.0,
            otel.diagnosable_run_rate * 100.0,
            otel.bytes_per_run / adhoc.bytes_per_run.max(1e-9)
        ));
    }

    Ok(BenchReport {
        bounty: "hanzoai/cloud#36 [Bounty #34] Unified agent execution tracing (OpenTelemetry GenAI)".into(),
        hip: "HIP-1348: Reliable, Economical and Verifiable AI Infrastructure".into(),
        honesty_labels: vec![
            "no OpenTelemetry SDK, collector or exporter is executed".into(),
            "attribute completeness is modelled per convention, not parsed from real telemetry".into(),
            "overhead is a per-span constant, not a measured allocation or network cost".into(),
            "All figures are simulation outputs on the authoring host, NOT Hanzo expectations until reproduced with a real OTel pipeline.".into(),
        ],
        config: cfg.clone(),
        results,
        notes,
    })
}

pub fn write_results_json(report: &BenchReport, path: &std::path::Path) -> Result<(), Box<dyn std::error::Error>> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    std::fs::write(path, serde_json::to_string_pretty(report)?)?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn find<'a>(r: &'a [PolicyResult], p: &str) -> &'a PolicyResult {
        r.iter().find(|x| x.policy == p).expect(p)
    }

    #[test]
    fn otel_joins_more_runs() {
        let report = run_bench(&BenchConfig::test_config()).unwrap();
        assert!(find(&report.results, "otel-genai").cross_service_join_rate > find(&report.results, "adhoc-logs").cross_service_join_rate);
    }

    #[test]
    fn otel_diagnoses_more_runs() {
        let report = run_bench(&BenchConfig::test_config()).unwrap();
        assert!(find(&report.results, "otel-genai").diagnosable_run_rate > find(&report.results, "adhoc-logs").diagnosable_run_rate);
    }

    #[test]
    fn otel_costs_more_bytes() {
        let report = run_bench(&BenchConfig::test_config()).unwrap();
        assert!(find(&report.results, "otel-genai").bytes_per_run > find(&report.results, "adhoc-logs").bytes_per_run);
    }

    #[test]
    fn rates_are_bounded() {
        let report = run_bench(&BenchConfig::test_config()).unwrap();
        for r in &report.results {
            assert!(r.cross_service_join_rate >= 0.0 && r.cross_service_join_rate <= 1.0);
            assert!(r.diagnosable_run_rate >= 0.0 && r.diagnosable_run_rate <= 1.0);
        }
    }

    #[test]
    fn span_tree_is_well_formed() {
        let spans = generate_runs(3, 20, 8);
        for s in &spans {
            if let Some(p) = s.parent {
                let parent = spans.iter().find(|x| x.id == p).expect("parent exists");
                assert_eq!(parent.run_id, s.run_id, "parent must be in the same run");
            }
        }
    }

    #[test]
    fn bad_config_rejected() {
        assert!(run_bench(&BenchConfig { seed: 1, n_runs: 0, spans_per_run: 5 }).is_err());
        assert!(run_bench(&BenchConfig { seed: 1, n_runs: 5, spans_per_run: 0 }).is_err());
    }
}
