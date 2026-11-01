"""cffi bindings to the shared Rust cryptography core.

Mirrors internal/crypto/core. Build the library first::

    cd crypto/core && cargo build --release

Set ``ARNE_CRYPTO_LIB`` to the library path if it is not in the default build
directory. Every primitive is deterministic and checked against spec/vectors.
"""

from __future__ import annotations

import os
from pathlib import Path

import cffi

SHA256_LEN = 32
BLAKE3_LEN = 32
ED25519_SEED_LEN = 32
ED25519_PUBLIC_LEN = 32
ED25519_SIGNATURE_LEN = 64
BLS_SECRET_LEN = 32
BLS_PUBLIC_LEN = 48
BLS_SIGNATURE_LEN = 96
ML_DSA_SEED_LEN = 32
ML_DSA_PUBLIC_LEN = 1952
ML_DSA_SIGNATURE_LEN = 3309


class CoreUnavailable(RuntimeError):
    pass


class CoreError(ValueError):
    pass


class InvalidInput(CoreError):
    pass


class InvalidKey(CoreError):
    pass


class InvalidSignature(CoreError):
    pass


_ffi = cffi.FFI()
_ffi.cdef(
    """
    int arne_sha256(const unsigned char *data, size_t len, unsigned char *out);
    int arne_blake3(const unsigned char *data, size_t len, unsigned char *out);
    int arne_ed25519_keypair(const unsigned char *seed, unsigned char *public_out);
    int arne_ed25519_sign(const unsigned char *seed, const unsigned char *msg,
                          size_t msg_len, unsigned char *sig_out);
    int arne_ed25519_verify(const unsigned char *pk, const unsigned char *msg,
                            size_t msg_len, const unsigned char *sig);
    int arne_bls_secret_from_seed(const unsigned char *seed, size_t seed_len,
                                  unsigned char *secret_out);
    int arne_bls_public_key(const unsigned char *secret, unsigned char *public_out);
    int arne_bls_sign(const unsigned char *secret, const unsigned char *msg,
                      size_t msg_len, unsigned char *sig_out);
    int arne_bls_verify(const unsigned char *pk, const unsigned char *msg,
                        size_t msg_len, const unsigned char *sig);
    int arne_bls_aggregate(const unsigned char *sigs, size_t count,
                           unsigned char *sig_out);
    int arne_bls_aggregate_verify(const unsigned char *pks, size_t count,
                                  const unsigned char *msg, size_t msg_len,
                                  const unsigned char *sig);
    int arne_ml_dsa65_keypair(const unsigned char *seed, unsigned char *public_out);
    int arne_ml_dsa65_sign(const unsigned char *seed, const unsigned char *msg,
                           size_t msg_len, const unsigned char *ctx,
                           size_t ctx_len, unsigned char *sig_out);
    int arne_ml_dsa65_verify(const unsigned char *pk, const unsigned char *msg,
                             size_t msg_len, const unsigned char *ctx,
                             size_t ctx_len, const unsigned char *sig);
    """
)

_lib = None


def library_path() -> Path:
    override = os.environ.get("ARNE_CRYPTO_LIB")
    if override:
        return Path(override)
    root = Path(__file__).resolve().parents[2]
    for name in ("libarne_crypto.so", "libarne_crypto.dylib", "arne_crypto.dll"):
        candidate = root / "crypto" / "core" / "target" / "release" / name
        if candidate.exists():
            return candidate
    return root / "crypto" / "core" / "target" / "release" / "libarne_crypto.so"


def _library():
    global _lib
    if _lib is None:
        path = library_path()
        if not path.exists():
            raise CoreUnavailable(f"crypto core library not found at {path}")
        try:
            _lib = _ffi.dlopen(str(path))
        except OSError as exc:
            raise CoreUnavailable(f"crypto core library could not be loaded: {exc}") from exc
    return _lib


def available() -> bool:
    try:
        _library()
        return True
    except CoreUnavailable:
        return False


def _input(data: bytes):
    if not data:
        return _ffi.NULL, 0
    return _ffi.from_buffer(data), len(data)


def _check(status: int) -> None:
    if status == 0:
        return
    if status == 1:
        raise InvalidInput("crypto core: invalid input")
    if status == 2:
        raise InvalidKey("crypto core: invalid key")
    if status == 3:
        raise InvalidSignature("crypto core: invalid signature")
    raise CoreError(f"crypto core: status {status}")


def _output(size: int):
    return _ffi.new("unsigned char[]", size)


def sha256(data: bytes) -> bytes:
    out = _output(SHA256_LEN)
    ptr, length = _input(data)
    _check(_library().arne_sha256(ptr, length, out))
    return bytes(out)


def blake3(data: bytes) -> bytes:
    out = _output(BLAKE3_LEN)
    ptr, length = _input(data)
    _check(_library().arne_blake3(ptr, length, out))
    return bytes(out)


def ed25519_public_key(seed: bytes) -> bytes:
    if len(seed) != ED25519_SEED_LEN:
        raise InvalidInput("crypto core: seed length")
    out = _output(ED25519_PUBLIC_LEN)
    _check(_library().arne_ed25519_keypair(_ffi.from_buffer(seed), out))
    return bytes(out)


def ed25519_sign(seed: bytes, message: bytes) -> bytes:
    if len(seed) != ED25519_SEED_LEN:
        raise InvalidInput("crypto core: seed length")
    out = _output(ED25519_SIGNATURE_LEN)
    msg, msg_len = _input(message)
    _check(_library().arne_ed25519_sign(_ffi.from_buffer(seed), msg, msg_len, out))
    return bytes(out)


def ed25519_verify(public_key: bytes, message: bytes, signature: bytes) -> bool:
    if len(public_key) != ED25519_PUBLIC_LEN or len(signature) != ED25519_SIGNATURE_LEN:
        return False
    msg, msg_len = _input(message)
    return (
        _library().arne_ed25519_verify(
            _ffi.from_buffer(public_key), msg, msg_len, _ffi.from_buffer(signature)
        )
        == 1
    )


def bls_secret_from_seed(seed: bytes) -> bytes:
    out = _output(BLS_SECRET_LEN)
    ptr, length = _input(seed)
    _check(_library().arne_bls_secret_from_seed(ptr, length, out))
    return bytes(out)


def bls_public_key(secret: bytes) -> bytes:
    if len(secret) != BLS_SECRET_LEN:
        raise InvalidInput("crypto core: secret length")
    out = _output(BLS_PUBLIC_LEN)
    _check(_library().arne_bls_public_key(_ffi.from_buffer(secret), out))
    return bytes(out)


def bls_sign(secret: bytes, message: bytes) -> bytes:
    if len(secret) != BLS_SECRET_LEN:
        raise InvalidInput("crypto core: secret length")
    out = _output(BLS_SIGNATURE_LEN)
    msg, msg_len = _input(message)
    _check(_library().arne_bls_sign(_ffi.from_buffer(secret), msg, msg_len, out))
    return bytes(out)


def bls_verify(public_key: bytes, message: bytes, signature: bytes) -> bool:
    if len(public_key) != BLS_PUBLIC_LEN or len(signature) != BLS_SIGNATURE_LEN:
        return False
    msg, msg_len = _input(message)
    return (
        _library().arne_bls_verify(
            _ffi.from_buffer(public_key), msg, msg_len, _ffi.from_buffer(signature)
        )
        == 1
    )


def bls_aggregate(signatures: list[bytes]) -> bytes:
    if not signatures:
        raise InvalidInput("crypto core: no signatures")
    for signature in signatures:
        if len(signature) != BLS_SIGNATURE_LEN:
            raise InvalidSignature("crypto core: invalid signature")
    joined = b"".join(signatures)
    out = _output(BLS_SIGNATURE_LEN)
    _check(_library().arne_bls_aggregate(_ffi.from_buffer(joined), len(signatures), out))
    return bytes(out)


def bls_aggregate_verify(public_keys: list[bytes], message: bytes, signature: bytes) -> bool:
    if not public_keys or len(signature) != BLS_SIGNATURE_LEN:
        return False
    for key in public_keys:
        if len(key) != BLS_PUBLIC_LEN:
            return False
    joined = b"".join(public_keys)
    msg, msg_len = _input(message)
    return (
        _library().arne_bls_aggregate_verify(
            _ffi.from_buffer(joined),
            len(public_keys),
            msg,
            msg_len,
            _ffi.from_buffer(signature),
        )
        == 1
    )


def ml_dsa_public_key(seed: bytes) -> bytes:
    if len(seed) != ML_DSA_SEED_LEN:
        raise InvalidInput("crypto core: seed length")
    out = _output(ML_DSA_PUBLIC_LEN)
    _check(_library().arne_ml_dsa65_keypair(_ffi.from_buffer(seed), out))
    return bytes(out)


def ml_dsa_sign(seed: bytes, message: bytes, context: bytes) -> bytes:
    if len(seed) != ML_DSA_SEED_LEN:
        raise InvalidInput("crypto core: seed length")
    out = _output(ML_DSA_SIGNATURE_LEN)
    msg, msg_len = _input(message)
    ctx, ctx_len = _input(context)
    _check(
        _library().arne_ml_dsa65_sign(
            _ffi.from_buffer(seed), msg, msg_len, ctx, ctx_len, out
        )
    )
    return bytes(out)


def ml_dsa_verify(public_key: bytes, message: bytes, context: bytes, signature: bytes) -> bool:
    if len(public_key) != ML_DSA_PUBLIC_LEN or len(signature) != ML_DSA_SIGNATURE_LEN:
        return False
    msg, msg_len = _input(message)
    ctx, ctx_len = _input(context)
    return (
        _library().arne_ml_dsa65_verify(
            _ffi.from_buffer(public_key),
            msg,
            msg_len,
            ctx,
            ctx_len,
            _ffi.from_buffer(signature),
        )
        == 1
    )
