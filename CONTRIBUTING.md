# Contributing

Thanks for helping build ARCE/ARNE. Keep changes small, tested, and honest about
what is implemented.

## Setup

```sh
go version        # 1.27+
uv sync --extra dev
pre-commit install
```

## Before you push

```sh
make lint
make test
```

`make test` runs Go race tests and the Python suite. Protocol or ledger changes
must also pass the cross-language vectors:

```sh
make generate     # only when you intentionally change the protocol
make conformance
```

Never hand-edit `spec/vectors/*.json`; regenerate them with `make generate` and
explain the change in the commit.

## Rules

- The Go node is canonical. Python must reproduce the same state roots and
  receipts; a parity break is a release blocker.
- Consensus-critical formats (CBOR, addresses, state keys, block hashing) need a
  vector and a test in both implementations.
- Transactions must stay deterministic: no wall-clock, randomness, or map-order
  dependence in state transitions.
- Do not commit secrets, mnemonics, or devnet key material.
- New user-visible behaviour needs a doc update in `docs/`.

## Commits

Use conventional-style subjects: `feat:`, `fix:`, `docs:`, `test:`, `chore:`.
Reference the OpenSpec task or change when one applies.

## License

Contributions are licensed under Apache-2.0. By opening a pull request you
confirm you have the right to submit the work under that license.
