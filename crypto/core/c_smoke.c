/* C smoke test for the shared crypto core.
 *
 * Build and run:
 *   cargo build --release
 *   cc c_smoke.c target/release/libarne_crypto.a -lpthread -ldl -lm -o target/c_smoke
 *   ./target/c_smoke
 */
#include <stdio.h>
#include <string.h>

int arne_sha256(const unsigned char *data, unsigned long len, unsigned char *out);
int arne_blake3(const unsigned char *data, unsigned long len, unsigned char *out);
int arne_ed25519_keypair(const unsigned char *seed, unsigned char *public_out);
int arne_ed25519_sign(const unsigned char *seed, const unsigned char *msg,
                      unsigned long msg_len, unsigned char *sig_out);
int arne_ed25519_verify(const unsigned char *pk, const unsigned char *msg,
                        unsigned long msg_len, const unsigned char *sig);
int arne_bls_secret_from_seed(const unsigned char *seed, unsigned long seed_len,
                              unsigned char *secret_out);
int arne_bls_public_key(const unsigned char *secret, unsigned char *public_out);
int arne_bls_sign(const unsigned char *secret, const unsigned char *msg,
                  unsigned long msg_len, unsigned char *sig_out);
int arne_bls_verify(const unsigned char *pk, const unsigned char *msg,
                    unsigned long msg_len, const unsigned char *sig);
int arne_bls_aggregate(const unsigned char *sigs, unsigned long count,
                       unsigned char *sig_out);
int arne_bls_aggregate_verify(const unsigned char *pks, unsigned long count,
                              const unsigned char *msg, unsigned long msg_len,
                              const unsigned char *sig);
int arne_ml_dsa65_keypair(const unsigned char *seed, unsigned char *public_out);
int arne_ml_dsa65_sign(const unsigned char *seed, const unsigned char *msg,
                       unsigned long msg_len, const unsigned char *ctx,
                       unsigned long ctx_len, unsigned char *sig_out);
int arne_ml_dsa65_verify(const unsigned char *pk, const unsigned char *msg,
                         unsigned long msg_len, const unsigned char *ctx,
                         unsigned long ctx_len, const unsigned char *sig);

static int failures = 0;

static void check(int condition, const char *label) {
    if (!condition) {
        fprintf(stderr, "FAIL %s\n", label);
        failures++;
    }
}

static int hex_equals(const unsigned char *bytes, unsigned long len, const char *hex) {
    static const char digits[] = "0123456789abcdef";
    for (unsigned long i = 0; i < len; i++) {
        if (digits[bytes[i] >> 4] != hex[i * 2] || digits[bytes[i] & 0xf] != hex[i * 2 + 1]) {
            return 0;
        }
    }
    return 1;
}

static void from_hex(const char *hex, unsigned char *out, unsigned long len) {
    for (unsigned long i = 0; i < len; i++) {
        unsigned int value = 0;
        sscanf(hex + i * 2, "%2x", &value);
        out[i] = (unsigned char)value;
    }
}

int main(void) {
    const unsigned char abc[] = {'a', 'b', 'c'};
    unsigned char digest[96];

    check(arne_sha256(abc, 3, digest) == 0, "sha256 status");
    check(hex_equals(digest, 32,
                     "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"),
          "sha256 vector");

    check(arne_blake3(abc, 3, digest) == 0, "blake3 status");
    check(hex_equals(digest, 32,
                     "6437b3ac38465133ffb63b75273a8db548c558465d79db03fd359c6cd5bd9d85"),
          "blake3 vector");

    unsigned char seed[32];
    from_hex("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60", seed, 32);
    unsigned char public_key[32];
    unsigned char signature[64];
    unsigned char message[] = "c smoke";
    check(arne_ed25519_keypair(seed, public_key) == 0, "ed25519 keypair");
    check(arne_ed25519_sign(seed, message, sizeof(message) - 1, signature) == 0, "ed25519 sign");
    check(arne_ed25519_verify(public_key, message, sizeof(message) - 1, signature) == 1,
          "ed25519 verify");
    check(arne_ed25519_verify(public_key, (const unsigned char *)"other", 5, signature) == 0,
          "ed25519 rejects another message");

    unsigned char bls_secret[32];
    unsigned char bls_public[48];
    unsigned char bls_signature[96];
    check(arne_bls_secret_from_seed((const unsigned char *)"validator-one", 13, bls_secret) == 0,
          "bls secret");
    check(arne_bls_public_key(bls_secret, bls_public) == 0, "bls public key");
    check(arne_bls_sign(bls_secret, message, sizeof(message) - 1, bls_signature) == 0, "bls sign");
    check(arne_bls_verify(bls_public, message, sizeof(message) - 1, bls_signature) == 1,
          "bls verify");
    check(arne_bls_verify(bls_public, (const unsigned char *)"other", 5, bls_signature) == 0,
          "bls rejects another message");
    check(arne_bls_aggregate(bls_signature, 1, digest) == 0, "bls aggregate");
    check(arne_bls_aggregate_verify(bls_public, 1, message, sizeof(message) - 1, digest) == 1,
          "bls aggregate verify");

    unsigned char ml_seed[32];
    from_hex("0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20", ml_seed, 32);
    unsigned char ml_public[1952];
    unsigned char ml_signature[3309];
    check(arne_ml_dsa65_keypair(ml_seed, ml_public) == 0, "ml-dsa keypair");
    check(arne_ml_dsa65_sign(ml_seed, message, sizeof(message) - 1,
                             (const unsigned char *)"arne-hybrid-v1", 14, ml_signature) == 0,
          "ml-dsa sign");
    check(arne_ml_dsa65_verify(ml_public, message, sizeof(message) - 1,
                               (const unsigned char *)"arne-hybrid-v1", 14, ml_signature) == 1,
          "ml-dsa verify");
    check(arne_ml_dsa65_verify(ml_public, (const unsigned char *)"other", 5,
                               (const unsigned char *)"arne-hybrid-v1", 14, ml_signature) == 0,
          "ml-dsa rejects another message");

    if (failures != 0) {
        fprintf(stderr, "%d check(s) failed\n", failures);
        return 1;
    }
    puts("crypto core C smoke test passed");
    return 0;
}
