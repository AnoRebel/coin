# Security Policy

## Reporting

Report vulnerabilities privately to security@anorebel.example (replace with the
project's monitored address before launch). Do not open a public issue.

Include: affected component and version, reproduction steps, impact, and any
suggested fix. We aim to acknowledge within 72 hours and to publish an advisory
after a fix ships.

## Scope

In scope: consensus/state divergence, signature or replay bypasses, fund loss,
double spend, supply conservation breaks, API auth or injection issues, key or
mnemonic leakage, and denial of service with a concrete attack path.

Out of scope: the devnet faucet being open, missing features listed as planned
in `README.md`, and issues that require a compromised host.

## Handling

- Never test against a network holding real value.
- Do not access other people's data or keys.
- Give us reasonable time to fix before disclosure.

See `docs/security.md` for the threat model and secure-deployment checklist.
