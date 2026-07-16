//! The rollout hash — byte-identical to the Hanzo Insights evaluator
//! (`rust/feature-flags/src/flags/flag_matching_utils.rs::calculate_hash`), which is
//! PostHog-compatible: sha1("{key}.{identifier}{salt}"), first 8 bytes as big-endian
//! u64 shifted right 4 bits (= first 15 hex chars), scaled to [0, 1).

use sha1::{Digest, Sha1};

const LONG_SCALE: u64 = 0xfffffffffffffff;

/// prefix is "{flag_key}." — the trailing dot is part of the wire contract.
/// salt is "" for rollout gating and "variant" for variant selection.
pub fn calculate_hash(prefix: &str, hashed_identifier: &str, salt: &str) -> f64 {
    let hash_key = format!("{prefix}{hashed_identifier}{salt}");
    let hash_value = Sha1::digest(hash_key.as_bytes());
    let hash_val: u64 = u64::from_be_bytes(hash_value[..8].try_into().unwrap()) >> 4;
    hash_val as f64 / LONG_SCALE as f64
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn hash_is_deterministic_and_uniform_at_the_edges() {
        let a = calculate_hash("some-flag.", "user-1", "");
        let b = calculate_hash("some-flag.", "user-1", "");
        assert_eq!(a, b);
        assert!((0.0..1.0).contains(&a));
        // variant salt produces a different point
        let v = calculate_hash("some-flag.", "user-1", "variant");
        assert_ne!(a, v);
    }

    #[test]
    fn matches_posthog_reference_values() {
        // Reference values computed with the PostHog python SDK algorithm
        // (sha1 of "distinct_id.hash_key", first 15 hex / 0xfffffffffffffff).
        let h = calculate_hash("holdout-", "example_id", "");
        assert!((0.0..1.0).contains(&h));
        // stability pins: the algorithm may never drift silently
        // (values independently computed: sha1 first-8-bytes-BE >> 4 / 0xfffffffffffffff)
        let pinned = calculate_hash("beta-feature.", "distinct_id", "");
        assert!((pinned - 0.8755963479474078).abs() < 1e-12, "hash drifted: {pinned}");
        let pinned2 = calculate_hash("some-flag.", "user-1", "");
        assert!((pinned2 - 0.7256676364902103).abs() < 1e-12, "hash drifted: {pinned2}");
    }
}
