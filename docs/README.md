# Documentation

Start where you fit:

| I want to... | Read |
| --- | --- |
| Create a wallet, get funds, send a payment | [user-guide.md](user-guide.md) |
| Understand the protocol and change code | [developer-guide.md](developer-guide.md) |
| Run or embed a node in a service | [embedding.md](embedding.md) |
| Connect a backend, exchange, or bot | [integration-guide.md](integration-guide.md) |
| Look up an endpoint, RPC method, or error code | [api-reference.md](api-reference.md) |
| Deploy, back up, monitor, upgrade | [operator-guide.md](operator-guide.md) |
| Review the threat model and compliance stance | [security.md](security.md) |
| Understand ARCE supply and fees | [tokenomics.md](tokenomics.md) |

## Honesty rule

These docs separate what ships from what is specified:

- **Implemented** — covered by tests in `go test ./...` and `uv run pytest tests`.
- **Planned** — present in the OpenSpec change
  (`openspec/changes/arce-arne-network/`) but not in the code. Never rely on a
  planned feature in production.

If a command or response here disagrees with the code, the code wins; please
open an issue.

## Versions

The protocol version is `arne/1` and the transaction version is `1`. The REST
surface is versioned in the path (`/v1`). JSON-RPC method names are stable; new
methods are additive. See `/v1/version` on a running node.
