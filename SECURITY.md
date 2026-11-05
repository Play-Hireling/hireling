# Security policy

Hireling runs autonomous agents with access to your accounts, so we take reports seriously and answer quickly.

## Reporting a vulnerability

**Do not open a public issue.** Use one of:

- GitHub: **Security → Report a vulnerability** on this repository (private advisory), or
- email **security@playhireling.com**.

Please include what you found, how to reproduce it, the version or commit, and the impact you expect. We acknowledge within **3 business days**, share a first assessment within **10 business days**, and credit you in the advisory unless you prefer otherwise.

## In scope

- Escapes from a hireling container or from the egress allowlist.
- A hireling reaching a host its profile does not allow, or a blocked host (cloud metadata, localhost, finance/KYC systems).
- Secrets leaking into logs, turn transcripts, the Front Desk, the bar or the Ledger (the redaction layer).
- Bypassing the autonomy gate: an action on the hard deny list (payment, contract, customer data, delete, production-critical) running without the owner.
- Authentication flaws in the Front Desk API.
- Prompt-injection paths that turn data from another agent or a web page into instructions the runtime acts on.

## Out of scope

- tavern-core's internal task API answering on `127.0.0.1` without auth — it is designed for the internal network; do not publish it (see [docs/security.md](docs/security.md)).
- Model output quality, or a model refusing or misbehaving inside the limits the runtime enforces.
- Denial of service against your own box.

## Supported versions

Until 1.0, only the latest release and `main` receive fixes.

Hardening guidance for operators: [docs/security.md](docs/security.md).
