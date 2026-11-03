---
title: AnoRebelCoin documentation
description: Using, running and building on AnoRebelCoin (ARCE) and AnoRebelNetwork (ARNE).
---

AnoRebelCoin (ARCE) is the native asset of AnoRebelNetwork (ARNE), a
currency-agnostic settlement and team-communication network. Nothing here is
audited or suitable for real money yet.

## Start where you fit

| I want to... | Read |
| --- | --- |
| Create a wallet, get funds, send a payment | [User guide](/user-guide) |
| Understand the protocol and change code | [Developer guide](/developer-guide) |
| Run or embed a node in a service | [Embedding](/embedding) |
| Connect a backend, exchange, or bot | [Integration guide](/integration-guide) |
| Look up an endpoint, RPC method, or error code | [API reference](/api-reference) |
| Deploy, back up, monitor, upgrade | [Operator guide](/operator-guide) |
| Review the threat model and compliance stance | [Security](/security) |
| Understand ARCE supply and fees | [Tokenomics](/tokenomics) |

## Honesty rule

These docs separate what ships from what is specified:

- **Implemented** — covered by tests in `go test ./...` and `uv run pytest tests`.
- **Specified** — in [OpenSpec](https://github.com/AnoRebel/coin/tree/main/openspec);
  not yet written or wired.
- **Not planned** — stated so nobody assumes it.

Both the Go and Python nodes follow the same rules; where they differ, the guide
says so.
