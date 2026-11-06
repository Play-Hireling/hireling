# Roadmap

What is coming to the open engine, roughly in order. Dates are intentions, not promises. Follow progress in GitHub issues and milestones; propose changes in Discussions.

## Now — make the box delightful
- **Autonomy gate** (D1) on every tool call, **capped limits counted per day or week** (D2) and the **Autonomy Log** (D4) with who, what, why and turn cost; **autonomy first** (D11): an ask about the hireling's own action goes back to it, one card per task.
- **Streaming answers** to the Front Desk (A13): first words in under a second.
- **Plain-art game client** in the box: watch the party work at `http://127.0.0.1:8080/` (the official art stays with the hosted game).
- **Release images** on GitHub Container Registry, multi-arch, signed.

## Next — limits you can trust
- **Token budget hard stop** (D7: over budget → only P0/P1).
- **Authenticated task API** in tavern-core (per-hireling tokens everywhere, not only on the Front Desk). The owner's reads are already under `/api/*` with the owner token (`/api/tasks`, `/api/quest`); until the rest moves, the internal routes (`/tasks`, `/goal`, `/actions/*`, …) have no auth and must stay on the Compose network ([security](security.md)).
- **Any OpenAI-compatible provider per hireling** (Anthropic, OpenAI, local models via vLLM/Ollama) with pricing tables.

## Later — a team that improves itself
- **The Caretaker** (F1–F6): cron hygiene, context pruning, token-waste detection, health watch, daily report.
- **Coins, scorecards and levels** (C4–C8): budgets follow measured results; promotion and probation.
- **Learning promotion** (E5) and **Tavern night** (E9): overnight review of failures into the Ledger.
- **Screens** (A11): a computer per hireling for apps without APIs.
- **Multi-server taverns** (G8): hirelings on several boxes sharing one bar and one Ledger.

## Not planned for this repository
Multi-tenant hosting, billing, the admin panel and the official art: those are **Hireling Cloud** ([playhireling.com](https://playhireling.com)), which funds this work.
