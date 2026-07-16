//! The stateless evaluator: (definitions, context) -> response. Pure, no IO, no
//! clock beyond date operators' parsing — safe behind FFI and trivially parallel.
//!
//! Semantics mirror the Hanzo Insights evaluator (`flag_matching.rs`):
//!   - a flag matches on the FIRST condition group that passes; groups with a
//!     variant override are considered first (an override must be able to win)
//!   - a group passes when ALL its property filters match AND the rollout hash
//!     ("{key}.{identifier}", salt "") clears rollout_percentage (None = 100)
//!   - person flags hash the distinct_id; group flags (aggregation_group_type_index)
//!     hash that group's key and match against that group's properties
//!   - variants: the matched group's override if valid, else cumulative selection
//!     with the "variant" salt over multivariate.variants
//!   - payloads: payloads["true"] for boolean true, payloads[variant] for variants
//!   - cohort/flag-type property filters need stores this engine deliberately does
//!     not have: the flag is skipped and errorsWhileComputingFlags is set (callers
//!     keep their env/default fallback — fail-safe, never fail-wrong)

use serde_json::Value;

use crate::hash::calculate_hash;
use crate::model::{ConditionGroup, EvalContext, EvalResponse, FlagDef};
use crate::model_props::{PropertyFilter, PropertyType};
use crate::props::match_property;

pub fn evaluate(defs: &[FlagDef], ctx: &EvalContext) -> EvalResponse {
    let mut out = EvalResponse::default();
    for def in defs {
        if def.deleted {
            continue;
        }
        if !def.active {
            out.feature_flags.insert(def.key.clone(), Value::Bool(false));
            continue;
        }
        match evaluate_flag(def, ctx) {
            Ok(FlagMatch::Off) => {
                out.feature_flags.insert(def.key.clone(), Value::Bool(false));
            }
            Ok(FlagMatch::On { variant }) => {
                let (value, payload_key) = match variant {
                    Some(v) => (Value::String(v.clone()), v),
                    None => (Value::Bool(true), "true".to_string()),
                };
                if let Some(p) = def.filters.payloads.get(&payload_key) {
                    out.feature_flag_payloads.insert(def.key.clone(), p.clone());
                }
                out.feature_flags.insert(def.key.clone(), value);
            }
            Err(_) => {
                // Inconclusive (cohort/flag filters, malformed values): report the
                // computation error and omit the flag so callers fall back safely.
                out.errors_while_computing_flags = true;
            }
        }
    }
    out
}

enum FlagMatch {
    Off,
    On { variant: Option<String> },
}

struct Inconclusive;

fn evaluate_flag(def: &FlagDef, ctx: &EvalContext) -> Result<FlagMatch, Inconclusive> {
    // The identity the rollout hashes and the property bag conditions read.
    let (identifier, props) = match def.aggregation_group_type_index {
        None => (ctx.distinct_id.as_str(), &ctx.person_properties),
        Some(idx) => match ctx.groups.get(&idx.to_string()) {
            Some(g) if !g.key.is_empty() => (g.key.as_str(), &g.properties),
            // Flag aggregates by a group the caller did not supply: no match.
            _ => return Ok(FlagMatch::Off),
        },
    };

    // Variant-override groups first — mirror the Insights evaluator's ordering.
    let mut order: Vec<&ConditionGroup> = def.filters.groups.iter().collect();
    order.sort_by_key(|g| g.variant.is_none());

    let mut saw_inconclusive = false;
    for group in &order {
        match group_matches(def, group, identifier, props) {
            Ok(true) => {
                let variant = resolve_variant(def, group, identifier);
                return Ok(FlagMatch::On { variant });
            }
            Ok(false) => {}
            Err(_) => saw_inconclusive = true,
        }
    }
    if saw_inconclusive {
        return Err(Inconclusive);
    }
    // No groups at all = simple boolean flag gated only by `active`.
    if def.filters.groups.is_empty() {
        return Ok(FlagMatch::On { variant: None });
    }
    Ok(FlagMatch::Off)
}

fn group_matches(
    def: &FlagDef,
    group: &ConditionGroup,
    identifier: &str,
    props: &std::collections::HashMap<String, Value>,
) -> Result<bool, Inconclusive> {
    for filter in &group.properties {
        if !property_filter_matches(filter, props)? {
            return Ok(false);
        }
    }
    let pct = group.rollout_percentage.unwrap_or(100.0);
    if pct < 100.0 {
        let h = calculate_hash(&format!("{}.", def.key), identifier, "");
        if h > pct / 100.0 {
            return Ok(false);
        }
    }
    Ok(true)
}

fn property_filter_matches(
    filter: &PropertyFilter,
    props: &std::collections::HashMap<String, Value>,
) -> Result<bool, Inconclusive> {
    match filter.prop_type {
        PropertyType::Person | PropertyType::Group => {
            // partial_props = true: a missing property is a non-match (never an error),
            // matching the Insights evaluator's local-properties mode.
            match match_property(filter, props, true) {
                Ok(m) => Ok(m),
                Err(_) => Ok(false),
            }
        }
        // Cohort membership and flag-dependency filters need person/flag stores;
        // this stateless engine cannot answer them — inconclusive by design.
        PropertyType::Cohort | PropertyType::Flag => Err(Inconclusive),
    }
}

fn resolve_variant(def: &FlagDef, matched: &ConditionGroup, identifier: &str) -> Option<String> {
    let multivariate = def.filters.multivariate.as_ref()?;
    if let Some(overridden) = &matched.variant {
        if multivariate.variants.iter().any(|v| &v.key == overridden) {
            return Some(overridden.clone());
        }
    }
    let h = calculate_hash(&format!("{}.", def.key), identifier, "variant");
    let mut cumulative = 0.0;
    for v in &multivariate.variants {
        cumulative += v.rollout_percentage / 100.0;
        if h < cumulative {
            return Some(v.key.clone());
        }
    }
    None
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::model::{EvalContext, FlagDef};
    use serde_json::json;

    fn ctx(distinct_id: &str, props: Value) -> EvalContext {
        serde_json::from_value(json!({
            "distinct_id": distinct_id,
            "person_properties": props,
        }))
        .unwrap()
    }

    fn def(v: Value) -> FlagDef {
        serde_json::from_value(v).unwrap()
    }

    #[test]
    fn boolean_flag_no_groups_is_on() {
        let flags = vec![def(json!({"key": "simple", "active": true}))];
        let r = evaluate(&flags, &ctx("u", json!({})));
        assert_eq!(r.feature_flags["simple"], json!(true));
    }

    #[test]
    fn inactive_and_deleted() {
        let flags = vec![
            def(json!({"key": "off", "active": false})),
            def(json!({"key": "gone", "active": true, "deleted": true})),
        ];
        let r = evaluate(&flags, &ctx("u", json!({})));
        assert_eq!(r.feature_flags["off"], json!(false));
        assert!(!r.feature_flags.contains_key("gone"));
    }

    #[test]
    fn property_condition_gates() {
        let flags = vec![def(json!({
            "key": "geo",
            "active": true,
            "filters": {"groups": [{
                "properties": [{"key": "country", "value": "DE", "operator": "exact", "type": "person"}],
                "rollout_percentage": 100
            }]}
        }))];
        let hit = evaluate(&flags, &ctx("u", json!({"country": "DE"})));
        assert_eq!(hit.feature_flags["geo"], json!(true));
        let miss = evaluate(&flags, &ctx("u", json!({"country": "US"})));
        assert_eq!(miss.feature_flags["geo"], json!(false));
        let absent = evaluate(&flags, &ctx("u", json!({})));
        assert_eq!(absent.feature_flags["geo"], json!(false));
    }

    #[test]
    fn rollout_percentage_splits_users_deterministically() {
        let flags = vec![def(json!({
            "key": "half",
            "active": true,
            "filters": {"groups": [{"properties": [], "rollout_percentage": 50}]}
        }))];
        let mut on = 0;
        for i in 0..1000 {
            let r = evaluate(&flags, &ctx(&format!("user-{i}"), json!({})));
            if r.feature_flags["half"] == json!(true) {
                on += 1;
            }
        }
        // sha1 uniformity: ~500 ± sane tolerance, and perfectly stable run-to-run
        assert!((400..=600).contains(&on), "rollout split off: {on}/1000");
    }

    #[test]
    fn variants_cumulative_and_override() {
        let flags = vec![def(json!({
            "key": "exp",
            "active": true,
            "filters": {
                "groups": [
                    {"properties": [{"key": "vip", "value": true, "operator": "exact", "type": "person"}],
                     "rollout_percentage": 100, "variant": "treatment"},
                    {"properties": [], "rollout_percentage": 100}
                ],
                "multivariate": {"variants": [
                    {"key": "control", "rollout_percentage": 50},
                    {"key": "treatment", "rollout_percentage": 50}
                ]},
                "payloads": {"treatment": {"cta": "buy"}}
            }
        }))];
        // vip user pinned to treatment via the override group (sorted first)
        let vip = evaluate(&flags, &ctx("vip-user", json!({"vip": true})));
        assert_eq!(vip.feature_flags["exp"], json!("treatment"));
        assert_eq!(vip.feature_flag_payloads["exp"], json!({"cta": "buy"}));
        // everyone else gets a stable multivariate assignment
        let a = evaluate(&flags, &ctx("plain-user", json!({})));
        let b = evaluate(&flags, &ctx("plain-user", json!({})));
        assert_eq!(a.feature_flags["exp"], b.feature_flags["exp"]);
        let v = a.feature_flags["exp"].as_str().unwrap();
        assert!(v == "control" || v == "treatment");
    }

    #[test]
    fn group_type_flags_hash_the_group_key() {
        let flags = vec![def(json!({
            "key": "org-flag",
            "active": true,
            "aggregation_group_type_index": 0,
            "filters": {"groups": [{
                "properties": [{"key": "plan", "value": "pro", "operator": "exact", "type": "group", "group_type_index": 0}],
                "rollout_percentage": 100
            }]}
        }))];
        let ctx: EvalContext = serde_json::from_value(json!({
            "distinct_id": "u",
            "groups": {"0": {"key": "acme", "properties": {"plan": "pro"}}}
        }))
        .unwrap();
        let r = evaluate(&flags, &ctx);
        assert_eq!(r.feature_flags["org-flag"], json!(true));
        // group absent from context -> off, not an error
        let r2 = evaluate(
            &flags,
            &EvalContext {
                distinct_id: "u".into(),
                ..Default::default()
            },
        );
        assert_eq!(r2.feature_flags["org-flag"], json!(false));
        assert!(!r2.errors_while_computing_flags);
    }

    #[test]
    fn cohort_filters_are_inconclusive_not_wrong() {
        let flags = vec![def(json!({
            "key": "cohorted",
            "active": true,
            "filters": {"groups": [{
                "properties": [{"key": "id", "value": 42, "type": "cohort"}],
                "rollout_percentage": 100
            }]}
        }))];
        let r = evaluate(&flags, &ctx("u", json!({})));
        assert!(!r.feature_flags.contains_key("cohorted"));
        assert!(r.errors_while_computing_flags);
    }
}
