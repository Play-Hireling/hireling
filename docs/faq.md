# FAQ

**Is Hireling an agent framework?**
No. It is a *runtime for a team* built on an existing agent (Hermes). Hireling adds what a team needs: one manager, one goal, tasks with leases, messages, shared files, shared memory, budgets and enforced limits.

**How much does it cost to run?**
The software is free. You pay your server and the model API. DeepSeek V4-Flash is cheap enough that a light day of three hirelings usually costs cents; every turn's cost is recorded (`GET /agents/<id>/budget`).

**Can I use Claude, GPT or a local model?**
Today the profile accepts DeepSeek V4-Flash/Pro, with OpenRouter as fallback. Other OpenAI-compatible providers per hireling are on the [roadmap](roadmap.md).

**Can I use Claude Code to run it?**
Yes — see [Run your tavern with Claude Code](guides/claude-code.md).

**Does it need the game?**
No. The engine runs headless; `bin/tavern` and the API are enough. The game is how the hosted product shows the same state.

**Can hirelings spend money or publish without me?**
Payments never. Publishing, spending and similar actions are `capped` or `owner_decides` per profile; the starter Paid Traffic cap is 0. Read [what is enforced by code and what by prompt](concepts/autonomy.md#what-enforces-the-limits).

**Where do my data and files live?**
On your server: Redis, the volumes and your own git repo for the Ledger. Prompts go to the model provider you configured.

**Can I put customer data in a tavern?**
No. The project's rule is that customer, KYC, financial and legal data never enter a hireling, prompt, log or the Ledger.

**Is it open source?**
Fair source. Most of the code is Apache-2.0; tavern-core is FSL-1.1-ALv2 (no competing hosted service; becomes Apache after two years). See [LICENSING.md](../LICENSING.md).

**Windows?**
Use WSL 2 with Docker Desktop.

**How do I reset everything?**
`docker compose down -v` deletes all volumes: tasks, memory, the common table. The Ledger repo is untouched.
