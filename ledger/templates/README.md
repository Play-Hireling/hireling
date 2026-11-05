---
title: Ledger page templates
tags: [ledger, templates]
---

# Ledger page templates

Page templates for the Tavern Ledger (E4), in GBrain's "compiled truth + timeline" format. Above the `---` line is the compiled truth (rewritten when facts change); below it is the timeline (append-only, newest first, one signed line per event). A hireling copies one, fills the `<…>` fields, and writes it to the area its profile allows (`ledger.write`).

- [Goal](goal-template.md): the shared quest — what the number is, who posts it, how each contribution kind moves it.
- [Agent](agent-template.md): one hireling — role, model, limits, strengths and gaps.
- [Project](project-template.md): work bigger than one task, with owner, tasks and goal contribution.
- [Audience](audience-template.md): a segment in aggregate — never a person or a customer record.
- [Learning](learning-template.md): something a hireling learned, until it is proven three times or approved.
- [Decision](decision-template.md): what was decided, by whom, why, and what was rejected.

Rules every page follows (checked by `runtime/internal/ledger`):

- Frontmatter: `title`, `type` (one of the six above), `owner` (a hireling id, `owner` or `seed`), `tags` as a YAML list.
- One `---` line between the compiled truth and `## Timeline`.
- Timeline lines: `- **YYYY-MM-DD** | <hireling id> — <summary>`, newest first. The source is always the hireling that wrote it.
- No customer, KYC, financial or legal data, and no tag that marks a page sensitive (`sensitive`, `confidential`, `pii`, `kyc`, `finance`, `legal` …): the Ledger rejects those pages (D6).
- Links are relative markdown links (they work in Obsidian and on GitHub).

Related: [the brain](../brain/README.md), [Ledger in the architecture](https://github.com/Play-Hireling/hireling/blob/main/docs/architecture.md#memory-and-the-central-brain-gbrain).
