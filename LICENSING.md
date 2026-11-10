# Licensing

Hireling is **fair source**: the code is public, you can read it, run it, change it and self-host it. One component carries a non-compete clause that expires on its own.

## What is under which license

| Path | License | In one line |
| --- | --- | --- |
| `runtime/` (the `hireling` runtime and `ledger-writer`) | [Apache-2.0](LICENSE) | Do anything; keep the notices; patent grant included |
| `services/egress/` | [Apache-2.0](LICENSE) | Same |
| `services/tavern-mcp/` (the Tavern MCP server) | [Apache-2.0](LICENSE) | Same |
| `services/gbrain/` (our image and entrypoint around GBrain) | [Apache-2.0](LICENSE) | GBrain itself keeps its upstream license |
| `services/ledger-writer/` | [Apache-2.0](LICENSE) | Same |
| `agents/` (profiles and prompts) | [Apache-2.0](LICENSE) | Copy them into your own tavern freely |
| `ledger/` (templates and seed brain) | [Apache-2.0](LICENSE) | Same |
| `docs/`, `scripts/`, `bin/`, `compose.yaml` | [Apache-2.0](LICENSE) | Same |
| `docs/assets/` (banner, screenshots, animation of the official game) | All rights reserved | Brand art shown in the README; not licensed for reuse — see [TRADEMARKS.md](TRADEMARKS.md) |
| `services/tavern-core/` | [FSL-1.1-ALv2](services/tavern-core/LICENSE.md) | Any use except a competing commercial service; becomes Apache-2.0 two years after each release |
| `hireling-client` image | [LicenseRef-Hireling-Client](LICENSE-CLIENT.md) | Run the official image with your own tavern-core. Sound stays inside the product. The game's music tracks are not included. Holder: [PENDENTE TITULAR] |

A file's directory decides its license. A file that says otherwise in its header wins.

## The FSL in plain words

The Functional Source License lets you use tavern-core for any **permitted purpose**, which is everything except a **competing use**: offering it to others as a commercial product or service that substitutes for Hireling (for example, selling hosted Hireling taverns). Internal use at your company, research, education, and professional services for people who run Hireling themselves are all permitted.

Every release turns into Apache-2.0 on its second anniversary. Nothing to sign, nothing to request.

## FAQ

**Can my company run Hireling internally, for free, forever?** Yes.

**Can I run a tavern for my clients as an agency?** Yes, if the clients are your clients for your own services (you run agents doing work for them). Selling *hosted Hireling* as a product is the competing use.

**Can I fork it?** Yes. Your fork of tavern-core stays under the FSL until each version converts.

**Can I contribute?** Yes — contributions are under the license of the directory they land in, signed off with the DCO ([CONTRIBUTING.md](CONTRIBUTING.md)). No CLA.

**Why not plain open source for everything?** So that the people building Hireling can keep a hosted business that funds the work. The parts that make it easy to build on — the runtime, the egress proxy, the profiles — are Apache-2.0 today.

## What is not in this repository

The official pixel-art game client, the art, music and sound, the logo, the multi-tenant cloud, the admin panel and billing are not open source. The name "Hireling" and the logo are trademarks: see [TRADEMARKS.md](TRADEMARKS.md).

Third-party software keeps its own license: Hermes Agent (Nous Research), GBrain, Redis, PostgreSQL and the Go modules listed in each `go.mod`.
