/* C ABI for the shared Rust crypto core. Keep in sync with src/lib.rs. */
#ifndef ARNE_CRYPTO_H
#define ARNE_CRYPTO_H

#include <stddef.h>

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

#endif
