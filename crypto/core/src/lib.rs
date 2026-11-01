//! Shared cryptography core for ARCE/ARNE nodes.
//!
//! The Go and Python implementations link this library through the C ABI at the
//! bottom of this file. Every primitive is deterministic from its inputs so the
//! three implementations can be compared byte for byte against
//! `spec/vectors/core.json`.

use blake3 as blake3_hash;
use blst::min_pk as bls;
use ed25519_dalek::{Signer, Verifier};
use ml_dsa::{ExpandedSigningKey, MlDsa65, Seed, Signature as MlDsaSignature, VerifyingKey};
use sha2::{Digest, Sha256};

pub const SHA256_LEN: usize = 32;
pub const BLAKE3_LEN: usize = 32;
pub const ED25519_SEED_LEN: usize = 32;
pub const ED25519_PUBLIC_LEN: usize = 32;
pub const ED25519_SIGNATURE_LEN: usize = 64;
pub const BLS_SECRET_LEN: usize = 32;
pub const BLS_PUBLIC_LEN: usize = 48;
pub const BLS_SIGNATURE_LEN: usize = 96;
pub const ML_DSA_SEED_LEN: usize = 32;
pub const ML_DSA_PUBLIC_LEN: usize = 1952;
pub const ML_DSA_SIGNATURE_LEN: usize = 3309;

/// Domain separation tag for the BLS proof-of-possession ciphersuite.
pub const BLS_DST: &[u8] = b"BLS_SIG_BLS12381G2_XMD:SHA-256_SSWU_RO_POP_";

#[derive(Debug)]
pub enum Error {
    InvalidInput(&'static str),
    InvalidKey,
    InvalidSignature,
}

pub type Result<T> = std::result::Result<T, Error>;

pub fn sha256(data: &[u8]) -> [u8; SHA256_LEN] {
    Sha256::digest(data).into()
}

pub fn blake3(data: &[u8]) -> [u8; BLAKE3_LEN] {
    *blake3_hash::hash(data).as_bytes()
}

pub fn ed25519_public_key(seed: &[u8; ED25519_SEED_LEN]) -> [u8; ED25519_PUBLIC_LEN] {
    ed25519_dalek::SigningKey::from_bytes(seed)
        .verifying_key()
        .to_bytes()
}

pub fn ed25519_sign(seed: &[u8; ED25519_SEED_LEN], message: &[u8]) -> [u8; ED25519_SIGNATURE_LEN] {
    ed25519_dalek::SigningKey::from_bytes(seed)
        .sign(message)
        .to_bytes()
}

pub fn ed25519_verify(
    public_key: &[u8; ED25519_PUBLIC_LEN],
    message: &[u8],
    signature: &[u8; ED25519_SIGNATURE_LEN],
) -> bool {
    let Ok(key) = ed25519_dalek::VerifyingKey::from_bytes(public_key) else {
        return false;
    };
    key.verify(message, &ed25519_dalek::Signature::from_bytes(signature))
        .is_ok()
}

fn bls_secret(secret: &[u8]) -> Result<bls::SecretKey> {
    bls::SecretKey::from_bytes(secret).map_err(|_| Error::InvalidKey)
}

/// Derives a BLS secret key from a wallet seed, matching the Go and Python
/// implementations: hash under `arne-bls-key-v1`, then rehash until the digest is
/// a valid scalar.
pub fn bls_secret_from_seed(seed: &[u8]) -> Result<[u8; BLS_SECRET_LEN]> {
    if seed.is_empty() {
        return Err(Error::InvalidInput("seed is required"));
    }
    let mut digest = Sha256::new();
    digest.update(b"arne-bls-key-v1");
    digest.update(seed);
    let mut candidate: [u8; 32] = digest.finalize().into();
    for _ in 0..256 {
        if bls::SecretKey::from_bytes(&candidate).is_ok() {
            return Ok(candidate);
        }
        candidate = sha256(&candidate);
    }
    Err(Error::InvalidKey)
}

pub fn bls_public_key(secret: &[u8]) -> Result<[u8; BLS_PUBLIC_LEN]> {
    Ok(bls_secret(secret)?.sk_to_pk().to_bytes())
}

pub fn bls_sign(secret: &[u8], message: &[u8]) -> Result<[u8; BLS_SIGNATURE_LEN]> {
    if message.is_empty() {
        return Err(Error::InvalidInput("empty message"));
    }
    Ok(bls_secret(secret)?.sign(message, BLS_DST, &[]).to_bytes())
}

pub fn bls_verify(public_key: &[u8], message: &[u8], signature: &[u8]) -> Result<bool> {
    if message.is_empty() {
        return Ok(false);
    }
    let Ok(key) = bls::PublicKey::from_bytes(public_key) else {
        return Ok(false);
    };
    let Ok(sig) = bls::Signature::from_bytes(signature) else {
        return Ok(false);
    };
    Ok(sig.verify(true, message, BLS_DST, &[], &key, true) == blst::BLST_ERROR::BLST_SUCCESS)
}

pub fn bls_aggregate(signatures: &[&[u8]]) -> Result<[u8; BLS_SIGNATURE_LEN]> {
    if signatures.is_empty() {
        return Err(Error::InvalidInput("no signatures"));
    }
    let mut parsed = Vec::with_capacity(signatures.len());
    for raw in signatures {
        parsed.push(bls::Signature::from_bytes(raw).map_err(|_| Error::InvalidSignature)?);
    }
    let refs: Vec<&bls::Signature> = parsed.iter().collect();
    let aggregate =
        bls::AggregateSignature::aggregate(&refs, true).map_err(|_| Error::InvalidSignature)?;
    Ok(aggregate.to_signature().to_bytes())
}

pub fn bls_aggregate_verify(
    public_keys: &[&[u8]],
    message: &[u8],
    signature: &[u8],
) -> Result<bool> {
    if public_keys.is_empty() || message.is_empty() {
        return Ok(false);
    }
    let mut parsed = Vec::with_capacity(public_keys.len());
    for raw in public_keys {
        parsed.push(bls::PublicKey::from_bytes(raw).map_err(|_| Error::InvalidKey)?);
    }
    let Ok(sig) = bls::Signature::from_bytes(signature) else {
        return Ok(false);
    };
    let refs: Vec<&bls::PublicKey> = parsed.iter().collect();
    let aggregate =
        bls::AggregatePublicKey::aggregate(&refs, true).map_err(|_| Error::InvalidKey)?;
    Ok(sig.verify(
        true,
        message,
        BLS_DST,
        &[],
        &aggregate.to_public_key(),
        true,
    ) == blst::BLST_ERROR::BLST_SUCCESS)
}

/// Derives the ML-DSA-65 public key from the 32-byte FIPS 204 seed.
pub fn ml_dsa_public_key(seed: &[u8; ML_DSA_SEED_LEN]) -> [u8; ML_DSA_PUBLIC_LEN] {
    ExpandedSigningKey::<MlDsa65>::from_seed(&Seed::from(*seed))
        .verifying_key()
        .encode()
        .into()
}

pub fn ml_dsa_sign(
    seed: &[u8; ML_DSA_SEED_LEN],
    message: &[u8],
    context: &[u8],
) -> Result<[u8; ML_DSA_SIGNATURE_LEN]> {
    let key = ExpandedSigningKey::<MlDsa65>::from_seed(&Seed::from(*seed));
    let signature = key
        .sign_deterministic(message, context)
        .map_err(|_| Error::InvalidInput("context"))?;
    Ok(signature.encode().into())
}

pub fn ml_dsa_verify(public_key: &[u8], message: &[u8], context: &[u8], signature: &[u8]) -> bool {
    let Ok(encoded_key) = ml_dsa::EncodedVerifyingKey::<MlDsa65>::try_from(public_key) else {
        return false;
    };
    let key = VerifyingKey::<MlDsa65>::decode(&encoded_key);
    let Ok(signature) = MlDsaSignature::<MlDsa65>::try_from(signature) else {
        return false;
    };
    key.verify_with_context(message, context, &signature)
}

// ---------------------------------------------------------------------------
// C ABI
// ---------------------------------------------------------------------------

unsafe fn input<'a>(ptr: *const u8, len: usize) -> Result<&'a [u8]> {
    if len == 0 {
        return Ok(&[]);
    }
    if ptr.is_null() {
        return Err(Error::InvalidInput("null pointer"));
    }
    Ok(unsafe { std::slice::from_raw_parts(ptr, len) })
}

unsafe fn output<'a>(ptr: *mut u8, len: usize) -> Result<&'a mut [u8]> {
    if ptr.is_null() || len == 0 {
        return Err(Error::InvalidInput("null output"));
    }
    Ok(unsafe { std::slice::from_raw_parts_mut(ptr, len) })
}

fn status(result: Result<()>) -> i32 {
    match result {
        Ok(()) => 0,
        Err(Error::InvalidInput(_)) => 1,
        Err(Error::InvalidKey) => 2,
        Err(Error::InvalidSignature) => 3,
    }
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn arne_sha256(data: *const u8, len: usize, out: *mut u8) -> i32 {
    status((|| {
        let input = unsafe { input(data, len)? };
        let out = unsafe { output(out, SHA256_LEN)? };
        out.copy_from_slice(&sha256(input));
        Ok(())
    })())
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn arne_blake3(data: *const u8, len: usize, out: *mut u8) -> i32 {
    status((|| {
        let input = unsafe { input(data, len)? };
        let out = unsafe { output(out, BLAKE3_LEN)? };
        out.copy_from_slice(&blake3(input));
        Ok(())
    })())
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn arne_ed25519_keypair(seed: *const u8, public_out: *mut u8) -> i32 {
    status((|| {
        let seed = unsafe { input(seed, ED25519_SEED_LEN)? };
        let seed: &[u8; ED25519_SEED_LEN] =
            seed.try_into().map_err(|_| Error::InvalidInput("seed"))?;
        let out = unsafe { output(public_out, ED25519_PUBLIC_LEN)? };
        out.copy_from_slice(&ed25519_public_key(seed));
        Ok(())
    })())
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn arne_ed25519_sign(
    seed: *const u8,
    message: *const u8,
    message_len: usize,
    signature_out: *mut u8,
) -> i32 {
    status((|| {
        let seed = unsafe { input(seed, ED25519_SEED_LEN)? };
        let seed: &[u8; ED25519_SEED_LEN] =
            seed.try_into().map_err(|_| Error::InvalidInput("seed"))?;
        let message = unsafe { input(message, message_len)? };
        let out = unsafe { output(signature_out, ED25519_SIGNATURE_LEN)? };
        out.copy_from_slice(&ed25519_sign(seed, message));
        Ok(())
    })())
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn arne_ed25519_verify(
    public_key: *const u8,
    message: *const u8,
    message_len: usize,
    signature: *const u8,
) -> i32 {
    let Ok(key) = (unsafe { input(public_key, ED25519_PUBLIC_LEN) }) else {
        return 0;
    };
    let Ok(key) = <&[u8; ED25519_PUBLIC_LEN]>::try_from(key) else {
        return 0;
    };
    let Ok(message) = (unsafe { input(message, message_len) }) else {
        return 0;
    };
    let Ok(signature) = (unsafe { input(signature, ED25519_SIGNATURE_LEN) }) else {
        return 0;
    };
    let Ok(signature) = <&[u8; ED25519_SIGNATURE_LEN]>::try_from(signature) else {
        return 0;
    };
    i32::from(ed25519_verify(key, message, signature))
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn arne_bls_secret_from_seed(
    seed: *const u8,
    seed_len: usize,
    secret_out: *mut u8,
) -> i32 {
    status((|| {
        let seed = unsafe { input(seed, seed_len)? };
        let out = unsafe { output(secret_out, BLS_SECRET_LEN)? };
        out.copy_from_slice(&bls_secret_from_seed(seed)?);
        Ok(())
    })())
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn arne_bls_public_key(secret: *const u8, public_out: *mut u8) -> i32 {
    status((|| {
        let secret = unsafe { input(secret, BLS_SECRET_LEN)? };
        let out = unsafe { output(public_out, BLS_PUBLIC_LEN)? };
        out.copy_from_slice(&bls_public_key(secret)?);
        Ok(())
    })())
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn arne_bls_sign(
    secret: *const u8,
    message: *const u8,
    message_len: usize,
    signature_out: *mut u8,
) -> i32 {
    status((|| {
        let secret = unsafe { input(secret, BLS_SECRET_LEN)? };
        let message = unsafe { input(message, message_len)? };
        let out = unsafe { output(signature_out, BLS_SIGNATURE_LEN)? };
        out.copy_from_slice(&bls_sign(secret, message)?);
        Ok(())
    })())
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn arne_bls_verify(
    public_key: *const u8,
    message: *const u8,
    message_len: usize,
    signature: *const u8,
) -> i32 {
    let Ok(key) = (unsafe { input(public_key, BLS_PUBLIC_LEN) }) else {
        return 0;
    };
    let Ok(message) = (unsafe { input(message, message_len) }) else {
        return 0;
    };
    let Ok(signature) = (unsafe { input(signature, BLS_SIGNATURE_LEN) }) else {
        return 0;
    };
    i32::from(bls_verify(key, message, signature).unwrap_or(false))
}

/// Aggregates `count` signatures given as `count * 96` contiguous bytes.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn arne_bls_aggregate(
    signatures: *const u8,
    count: usize,
    signature_out: *mut u8,
) -> i32 {
    status((|| {
        if count == 0 {
            return Err(Error::InvalidInput("no signatures"));
        }
        let raw = unsafe { input(signatures, count * BLS_SIGNATURE_LEN)? };
        let mut refs = Vec::with_capacity(count);
        for chunk in raw.chunks_exact(BLS_SIGNATURE_LEN) {
            refs.push(chunk);
        }
        let out = unsafe { output(signature_out, BLS_SIGNATURE_LEN)? };
        out.copy_from_slice(&bls_aggregate(&refs)?);
        Ok(())
    })())
}

/// Verifies `count` public keys (48 bytes each, contiguous) over one message.
#[unsafe(no_mangle)]
pub unsafe extern "C" fn arne_bls_aggregate_verify(
    public_keys: *const u8,
    count: usize,
    message: *const u8,
    message_len: usize,
    signature: *const u8,
) -> i32 {
    if count == 0 {
        return 0;
    }
    let Ok(raw) = (unsafe { input(public_keys, count * BLS_PUBLIC_LEN) }) else {
        return 0;
    };
    let Ok(message) = (unsafe { input(message, message_len) }) else {
        return 0;
    };
    let Ok(signature) = (unsafe { input(signature, BLS_SIGNATURE_LEN) }) else {
        return 0;
    };
    let mut keys = Vec::with_capacity(count);
    for chunk in raw.chunks_exact(BLS_PUBLIC_LEN) {
        keys.push(chunk);
    }
    i32::from(bls_aggregate_verify(&keys, message, signature).unwrap_or(false))
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn arne_ml_dsa65_keypair(seed: *const u8, public_out: *mut u8) -> i32 {
    status((|| {
        let seed = unsafe { input(seed, ML_DSA_SEED_LEN)? };
        let seed: &[u8; ML_DSA_SEED_LEN] =
            seed.try_into().map_err(|_| Error::InvalidInput("seed"))?;
        let out = unsafe { output(public_out, ML_DSA_PUBLIC_LEN)? };
        out.copy_from_slice(&ml_dsa_public_key(seed));
        Ok(())
    })())
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn arne_ml_dsa65_sign(
    seed: *const u8,
    message: *const u8,
    message_len: usize,
    context: *const u8,
    context_len: usize,
    signature_out: *mut u8,
) -> i32 {
    status((|| {
        let seed = unsafe { input(seed, ML_DSA_SEED_LEN)? };
        let seed: &[u8; ML_DSA_SEED_LEN] =
            seed.try_into().map_err(|_| Error::InvalidInput("seed"))?;
        let message = unsafe { input(message, message_len)? };
        let context = unsafe { input(context, context_len)? };
        let out = unsafe { output(signature_out, ML_DSA_SIGNATURE_LEN)? };
        out.copy_from_slice(&ml_dsa_sign(seed, message, context)?);
        Ok(())
    })())
}

#[unsafe(no_mangle)]
pub unsafe extern "C" fn arne_ml_dsa65_verify(
    public_key: *const u8,
    message: *const u8,
    message_len: usize,
    context: *const u8,
    context_len: usize,
    signature: *const u8,
) -> i32 {
    let Ok(key) = (unsafe { input(public_key, ML_DSA_PUBLIC_LEN) }) else {
        return 0;
    };
    let Ok(message) = (unsafe { input(message, message_len) }) else {
        return 0;
    };
    let Ok(context) = (unsafe { input(context, context_len) }) else {
        return 0;
    };
    let Ok(signature) = (unsafe { input(signature, ML_DSA_SIGNATURE_LEN) }) else {
        return 0;
    };
    i32::from(ml_dsa_verify(key, message, context, signature))
}
