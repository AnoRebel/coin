# Security, privacy, and compliance

## Status

This is an unaudited reference implementation. The ledger, signatures, and
encoding have tests and cross-language vectors, but no external audit. Do not
use it to custody real value.

## Assets and trust boundaries

| Asset | Protection |
| --- | --- |
| User funds | Ed25519 signatures; exact nonce; network/chain binding; expiry |
| Mnemonics/keys | Never sent to the node; held by the client |
| API writes | Optional bearer keys with `write`/`admin` scopes |
| Node data | SHA-256 checksummed records; atomic block+state commit |
| Devnet faucet | Devnet-only; holds a key by design |
| Consensus | **Not implemented**; the devnet has one producer |

Trust boundaries: the client signs; the node executes deterministically; storage
is trusted to be intact (checksums catch corruption, not an attacker with write
access). A host compromise owns the node's key and data.

## Cryptography

- Signatures: Ed25519 (`ed25519-v1`). BLS12-381 aggregation and hybrid ML-DSA are
  specified for validator/account migration, not implemented.
- Hashes: SHA-256 for addresses, transaction hashes, state tree, and storage
  checksums. BLAKE3 is not used yet.
- Encoding: canonical deterministic CBOR with strict decoding (no floats, tags,
  indefinite lengths, duplicate keys, or non-shortest forms).
- Domain separation: signatures cover `dom`, network, chain, suite, purpose, and
  expiry, so a signature cannot be replayed across networks or purposes.
- Randomness: keys use the OS CSPRNG. The ledger itself uses no randomness.

Constant-time comparison and zeroization are not comprehensively audited; Go and
`cryptography` primitives are the vetted implementations used.

## Threats addressed

- Replay across networks/chains: `WRONG_NETWORK`, chain-bound signatures.
- Replay of a transaction: exact nonce.
- Tampered envelopes: signature verification before execution.
- Integer/float confusion: integer-only ledger amounts and canonical CBOR.
- Non-atomic transfers: journaled rollback; batches are all-or-nothing.
- Refund fraud/over-refund: refunds are signed by the original recipient,
  return the original asset to the original payer, and are capped by the
  cumulative on-chain `refunded` total; refund-of-refund is rejected.
- Supply inflation: conservation check after every transaction and block; a
  violation halts the node.
- Storage corruption: checksum on every record.

## Threats not addressed (yet)

- Eclipse/partition and long-range attacks: no P2P consensus or fork choice.
- Double-sign/slashing: no validator set.
- MEV/ordering manipulation: single producer; deterministic ordering exists but
  no fair ordering or encrypted mempool.
- DoS: rate limits and a 2 MiB body cap exist; there is no peer scoring, proof of
  work, or fee-on-failure. Failed transactions are free.
- Key compromise: no social recovery, passkeys, or hardware signer integration.
- Metadata privacy: transfers and amounts are public. Plaintext memos are
  public; opt-in `memo_enc` hides memo text only. Shielded transfers are
  specified, not implemented.
- Side channels in the Python implementation: not audited for constant time.

## Privacy

- On-chain data is public and permanent: addresses, amounts, asset IDs, memos,
  escrow parties, and timestamps.
- Plaintext memos are public and permanent. Opt-in `memo_enc` encrypts memo
  text to a recipient memo key, but sender, recipient, asset, amount, and time
  stay visible. Sealed memos use X25519 + HKDF-SHA-256 + ChaCha20-Poly1305 and
  are validated for shape and size by the ledger, which cannot decrypt them.
  There is no MLS messaging runtime yet.
- Payment URIs are untrusted input. They are parsed strictly, displayed before
  signing, and memos are rendered as inert text with a warning on markup. A URI
  never overrides an explicit amount, recipient, or network; conflicts are
  refused.
- Intent links are resolved against the API's declared network before signing;
  recipient, asset, and amount must match the stored intent. The ledger repeats
  those checks and atomically marks a request paid, independently of the HTTP
  retry cache. Intent references, memos, and payment metadata are public.
- Transaction-submit idempotency uses a credential/network/signer/key scope
  and hashes the exact signed bytes. Authorization precedes lookup, rejected
  submissions do not reserve keys, and cache capacity is bounded. The cache is
  process-local with 24-hour retention: retain signed bytes and receipts in
  your integration, and reconcile after restart or expiry before re-signing.
- Webhook deliveries are signed with Ed25519 over the timestamp and body.
  Receivers must verify `X-Arne-Signature`, reject timestamps outside five
  minutes, and deduplicate `X-Arne-Delivery`; otherwise a captured delivery can
  be replayed. The signing seed is a secret: anyone holding it can forge events.
- The node does not require KYC and stores no PII. If you build a compliance
  layer, keep PII off-chain and store only commitments or verifiable credentials.
- Telemetry: none is collected by the node. Reverse proxies and hosting providers
  may log IPs; configure retention accordingly.

## Compliance

The design targets FATF Travel Rule/IVMS101, EU MiCA, and ISO 20022 profiles, and
the API has stable references (transaction hash, asset ID, network ID) that map
cleanly to those flows. **None of the compliance features are implemented**:

- no jurisdiction policy engine, sanctions screening, or Travel Rule exchange;
- no verifiable-credential/KYB verification;
- no auditor access grants, retention/legal hold, or monitoring cases;
- no ISO 20022/ILP/EVM connector runtime.

Nothing here is legal advice. Operators are responsible for licensing, KYC/AML,
sanctions, tax, and consumer-protection obligations in their jurisdiction.

## Secure deployment checklist

- [ ] Node bound to localhost or behind a TLS proxy.
- [ ] `--api-key` set; keys are long, random, and rotated.
- [ ] `--allow-origin` restricted; no wildcard credentialed CORS.
- [ ] Data directory permissions restricted to the node user.
- [ ] Backups encrypted; restore tested.
- [ ] Logs do not contain mnemonics, API keys, or raw transactions with memos.
- [ ] Devnet faucet disabled on any non-devnet network.
- [ ] Monitoring alerts on halt, stalled height, and mempool growth.

## Disclosure

See [SECURITY.md](../SECURITY.md). Report privately; do not open a public issue.
