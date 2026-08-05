//! Flag definition wire shapes — the PostHog-compatible `filters` JSON that the
//! console UI, SDKs and the Insights evaluator all speak. Definitions live in
//! cloud's per-org SQLite (Go side); this crate only evaluates them.

use serde::{Deserialize, Serialize};
use serde_json::Value;
use std::collections::HashMap;

use crate::model_props::PropertyFilter;

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct FlagDef {
    pub key: String,
    #[serde(default = "default_true")]
    pub active: bool,
    #[serde(default)]
    pub deleted: bool,
    #[serde(default)]
    pub filters: FlagFilters,
    /// When set, the flag aggregates by a group type (its index) instead of the person.
    #[serde(default)]
    pub aggregation_group_type_index: Option<i32>,
}

fn default_true() -> bool {
    true
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
pub struct FlagFilters {
    #[serde(default)]
    pub groups: Vec<ConditionGroup>,
    #[serde(default)]
    pub multivariate: Option<Multivariate>,
    /// "true" -> payload for boolean matches; "<variant key>" -> payload per variant.
    #[serde(default)]
    pub payloads: HashMap<String, Value>,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
pub struct ConditionGroup {
    #[serde(default)]
    pub properties: Vec<PropertyFilter>,
    /// None means 100%.
    #[serde(default)]
    pub rollout_percentage: Option<f64>,
    /// A matching group may pin a specific variant (must exist in multivariate).
    #[serde(default)]
    pub variant: Option<String>,
}

#[derive(Debug, Clone, Default, Deserialize, Serialize)]
pub struct Multivariate {
    #[serde(default)]
    pub variants: Vec<Variant>,
}

#[derive(Debug, Clone, Deserialize, Serialize)]
pub struct Variant {
    pub key: String,
    #[serde(default)]
    pub rollout_percentage: f64,
}

/// One group's identity + properties in the evaluation context.
#[derive(Debug, Clone, Default, Deserialize, Serialize)]
pub struct GroupCtx {
    #[serde(default)]
    pub key: String,
    #[serde(default)]
    pub properties: HashMap<String, Value>,
}

/// The evaluation request: who is asking, with which properties.
#[derive(Debug, Clone, Default, Deserialize, Serialize)]
pub struct EvalContext {
    pub distinct_id: String,
    #[serde(default)]
    pub person_properties: HashMap<String, Value>,
    /// group_type_index (as string, JSON object keys) -> group identity/properties.
    #[serde(default)]
    pub groups: HashMap<String, GroupCtx>,
}

/// The evaluation response — the PostHog `/flags` shape consumers already parse.
#[derive(Debug, Clone, Default, Serialize, Deserialize)]
pub struct EvalResponse {
    #[serde(rename = "featureFlags")]
    pub feature_flags: HashMap<String, Value>, // bool | variant string
    #[serde(rename = "featureFlagPayloads")]
    pub feature_flag_payloads: HashMap<String, Value>,
    #[serde(rename = "errorsWhileComputingFlags")]
    pub errors_while_computing_flags: bool,
}
