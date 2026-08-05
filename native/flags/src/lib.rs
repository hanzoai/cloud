//! hanzo-flags — the stateless flag evaluator cloud embeds over FFI.
//!
//! Definitions live in cloud's per-org SQLite (Base pattern); evaluation is a
//! pure in-memory function of (definitions JSON, context JSON) with PostHog-
//! compatible semantics (matching the Hanzo Insights evaluator bit-for-bit on
//! the rollout hash and property operators). No KV, no network, no state.
//!
//! FFI contract (C strings are UTF-8 JSON):
//!   hanzo_flags_evaluate(defs_json, ctx_json) -> malloc'd result JSON; the
//!     result is either the EvalResponse shape or {"error": "..."}. Never null
//!     except on catastrophic allocation failure.
//!   hanzo_flags_free(ptr) frees a string returned by hanzo_flags_evaluate.

mod eval;
mod hash;
mod model;
mod model_props;
mod props;
mod relative_date;

pub use eval::evaluate;
pub use model::{EvalContext, EvalResponse, FlagDef};

use std::ffi::{c_char, CStr, CString};
use std::panic::{catch_unwind, AssertUnwindSafe};

fn evaluate_json(defs_json: &str, ctx_json: &str) -> String {
    let defs: Vec<FlagDef> = match serde_json::from_str(defs_json) {
        Ok(d) => d,
        Err(e) => return format!(r#"{{"error":"bad definitions: {}"}}"#, escape(&e.to_string())),
    };
    let ctx: EvalContext = match serde_json::from_str(ctx_json) {
        Ok(c) => c,
        Err(e) => return format!(r#"{{"error":"bad context: {}"}}"#, escape(&e.to_string())),
    };
    let resp = evaluate(&defs, &ctx);
    serde_json::to_string(&resp)
        .unwrap_or_else(|e| format!(r#"{{"error":"serialize: {}"}}"#, escape(&e.to_string())))
}

fn escape(s: &str) -> String {
    s.replace('\\', "\\\\").replace('"', "\\\"")
}

/// # Safety
/// `defs_json` and `ctx_json` must be valid NUL-terminated UTF-8 C strings.
/// The returned pointer must be released with `hanzo_flags_free`.
#[no_mangle]
pub unsafe extern "C" fn hanzo_flags_evaluate(
    defs_json: *const c_char,
    ctx_json: *const c_char,
) -> *mut c_char {
    let out = catch_unwind(AssertUnwindSafe(|| {
        if defs_json.is_null() || ctx_json.is_null() {
            return r#"{"error":"null input"}"#.to_string();
        }
        let defs = match CStr::from_ptr(defs_json).to_str() {
            Ok(s) => s,
            Err(_) => return r#"{"error":"definitions not utf-8"}"#.to_string(),
        };
        let ctx = match CStr::from_ptr(ctx_json).to_str() {
            Ok(s) => s,
            Err(_) => return r#"{"error":"context not utf-8"}"#.to_string(),
        };
        evaluate_json(defs, ctx)
    }))
    .unwrap_or_else(|_| r#"{"error":"panic in evaluator"}"#.to_string());

    // Interior NULs cannot occur in serde_json output; fall back defensively.
    CString::new(out)
        .unwrap_or_else(|_| CString::new(r#"{"error":"interior nul"}"#).unwrap())
        .into_raw()
}

/// # Safety
/// `s` must be a pointer previously returned by `hanzo_flags_evaluate` (or null).
#[no_mangle]
pub unsafe extern "C" fn hanzo_flags_free(s: *mut c_char) {
    if !s.is_null() {
        drop(CString::from_raw(s));
    }
}

#[cfg(test)]
mod ffi_tests {
    use super::*;

    #[test]
    fn json_roundtrip() {
        let defs = r#"[{"key":"k","active":true}]"#;
        let ctx = r#"{"distinct_id":"u"}"#;
        let out = evaluate_json(defs, ctx);
        let v: serde_json::Value = serde_json::from_str(&out).unwrap();
        assert_eq!(v["featureFlags"]["k"], serde_json::json!(true));
    }

    #[test]
    fn bad_input_yields_error_json_not_panic() {
        let out = evaluate_json("not json", "{}");
        assert!(out.contains("\"error\""));
    }
}
