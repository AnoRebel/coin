use arne_crypto::*;
use serde_json::Value;
use std::path::PathBuf;

fn vector(name: &str) -> Value {
    let path = PathBuf::from(env!("CARGO_MANIFEST_DIR"))
        .join("..")
        .join("..")
        .join("spec")
        .join("vectors")
        .join(name);
    let raw = std::fs::read_to_string(&path).unwrap_or_else(|e| panic!("{}: {e}", path.display()));
    serde_json::from_str(&raw).expect("valid json")
}

fn hex(value: &str) -> Vec<u8> {
    (0..value.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&value[i..i + 2], 16).expect("hex"))
        .collect()
}

fn to_hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}

#[test]
fn hashes_match_known_vectors() {
    let doc = vector("core.json");
    for case in doc["sha256"].as_array().unwrap() {
        let input = hex(case["input_hex"].as_str().unwrap());
        assert_eq!(to_hex(&sha256(&input)), case["digest"].as_str().unwrap());
    }
    for case in doc["blake3"].as_array().unwrap() {
        let input = hex(case["input_hex"].as_str().unwrap());
        assert_eq!(to_hex(&blake3(&input)), case["digest"].as_str().unwrap());
    }
}

#[test]
fn ed25519_matches_rfc8032() {
    let doc = vector("core.json")["ed25519"].clone();
    let seed: [u8; 32] = hex(doc["seed"].as_str().unwrap()).try_into().unwrap();
    let public = ed25519_public_key(&seed);
    assert_eq!(to_hex(&public), doc["public_key"].as_str().unwrap());
    let message = hex(doc["message_hex"].as_str().unwrap());
    let signature = ed25519_sign(&seed, &message);
    assert_eq!(to_hex(&signature), doc["signature"].as_str().unwrap());
    assert!(ed25519_verify(&public, &message, &signature));
    assert!(!ed25519_verify(&public, b"other", &signature));
}

#[test]
fn bls_matches_shared_vectors() {
    let doc = vector("bls.json");
    let message = doc["message"].as_str().unwrap().as_bytes();
    let mut signatures = Vec::new();
    let mut public_keys = Vec::new();
    for validator in doc["validators"].as_array().unwrap() {
        let secret = bls_secret_from_seed(validator["seed"].as_str().unwrap().as_bytes()).unwrap();
        assert_eq!(to_hex(&secret), validator["secret"].as_str().unwrap());
        let public = bls_public_key(&secret).unwrap();
        assert_eq!(to_hex(&public), validator["public_key"].as_str().unwrap());
        let signature = bls_sign(&secret, message).unwrap();
        assert!(bls_verify(&public, message, &signature).unwrap());
        signatures.push(signature);
        public_keys.push(public);
    }

    let ids = doc["aggregate"]["validators"].as_array().unwrap();
    let mut selected = Vec::new();
    let mut selected_keys = Vec::new();
    for id in ids {
        let index = doc["validators"]
            .as_array()
            .unwrap()
            .iter()
            .position(|v| v["id"] == *id)
            .unwrap();
        selected.push(signatures[index]);
        selected_keys.push(public_keys[index]);
    }
    let refs: Vec<&[u8]> = selected.iter().map(|s| s.as_slice()).collect();
    let aggregate = bls_aggregate(&refs).unwrap();
    assert_eq!(
        to_hex(&aggregate),
        doc["aggregate"]["signature"].as_str().unwrap()
    );
    let key_refs: Vec<&[u8]> = selected_keys.iter().map(|k| k.as_slice()).collect();
    assert!(bls_aggregate_verify(&key_refs, message, &aggregate).unwrap());
    assert!(!bls_aggregate_verify(&key_refs, b"other", &aggregate).unwrap());
}

#[test]
fn ml_dsa_matches_hybrid_vector() {
    let doc = vector("hybrid.json");
    let seed: [u8; 32] = hex(doc["seed"].as_str().unwrap()).try_into().unwrap();
    let public = ml_dsa_public_key(&seed);
    assert_eq!(to_hex(&public), doc["key"]["ml_dsa_65"].as_str().unwrap());
    let message = doc["message"].as_str().unwrap().as_bytes();
    let signature = ml_dsa_sign(&seed, message, b"arne-hybrid-v1").unwrap();
    assert_eq!(
        to_hex(&signature),
        doc["signature"]["ml_dsa_65"].as_str().unwrap()
    );
    assert!(ml_dsa_verify(
        &public,
        message,
        b"arne-hybrid-v1",
        &signature
    ));
    assert!(!ml_dsa_verify(&public, message, b"other", &signature));
    assert!(!ml_dsa_verify(
        &public,
        b"other",
        b"arne-hybrid-v1",
        &signature
    ));
}
