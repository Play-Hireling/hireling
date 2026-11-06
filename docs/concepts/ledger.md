# The Tavern Ledger

The Ledger is the tavern's **long-term shared memory**: what the goal is, who the agents are, the projects, the audiences, the decisions and — most valuable — what each hireling **learned**. It is optional (Compose profile `brain`).

## Shape

- **Markdown pages in a git repository** you own. Readable by people, diffable, revertable, and openable as an [Obsidian](https://obsidian.md) vault.
- **[GBrain](https://github.com/garrytan/gbrain)** indexes the repo into Postgres and serves search over **MCP** (`http://gbrain:3131/mcp`). Keyword search today; embeddings later.
- **ledger-writer** is the only way a page gets in.

```text
ledger/
  templates/          one template per page type
  brain/
    goal.md           the quest, in words
    agents/<id>.md    one page per hireling
    projects/         audiences/   decisions/
    learnings/<id>/   what each hireling learned
    RESOLVER.md       which page type a fact belongs to
```

## Reading

Each hireling with `ledger.read` gets a **read-only** GBrain token (minted by the gbrain service into a shared volume) and the GBrain MCP server in its Hermes config. Its prompt says: search the Ledger before asking someone or redoing work, and cite the page you used.

## Writing

A hireling never writes to the repo. It ends its answer with a `ledger` block (page path + content). The runtime sends it to **ledger-writer**, which:

1. identifies the caller by its container name (like egress does);
2. checks the path against the hireling's `ledger.write` areas (for example `learnings/dev/*`);
3. validates the page grammar (front matter, type, owner, sections, links);
4. commits **as that hireling** (`dev <dev@hireling.tavern>`, committer `ledger-writer`) and pushes.

GBrain picks the change up on its next sync (≈ 20–120 s). Every page is attributable; every change is a commit you can revert.

## History and revert

ledger-writer also serves the history of the Ledger and undoes a change, on the same internal address (`http://ledger-writer:8090`):

- `GET /history?path=&limit=` lists the commits that touched one page (`limit` default 20, at most 100). Without `path` it lists the caller's own `learnings/<id>/`; a planner (the Innkeeper) sees the whole `brain/`.
- `POST /revert {"commit": "…", "reason": "…"}` writes the undo as a **new commit**, authored by the asking hireling (`revert(<id>): …`), and pushes. History is never rewritten.

Pages pass through the redactor before they reach ledger-writer, and it masks any 32+ character hex string, so cite a commit by its short hash (7–12 characters): a full 40-character SHA in a page becomes `[hidden]`.

Who may revert what:

- a hireling reverts only commits whose every file is inside its `ledger.write` areas;
- a planner may revert any page of `brain/`;
- merges, commits that touch anything outside `brain/` and reverts that do not apply cleanly are refused (`409`/`422`), and the clone is left clean.

The caller is identified by its container name, never by the request body. You can always revert by hand in the repository too: it is plain git.

## The data rule

Customer, KYC, financial and legal data never enter the Ledger. `RESOLVER.md` says it plainly; prompts repeat it; and the hireling that would know such data should not exist in a tavern in the first place.

Set it up: [Turn on the Ledger](../guides/ledger.md).
