# Ledger

The Tavern Ledger, the shared brain the hirelings read and write through GBrain ([architecture](https://github.com/Play-Hireling/hireling/blob/main/docs/architecture.md#memory-and-the-central-brain-gbrain)).

- [brain/](brain/README.md): the brain itself. A live tavern keeps its brain in its own git repo (for example a private `<you>/tavern-ledger`), created from this whole folder and indexed by GBrain; this copy is the seed and the validator's fixtures.
- [templates/](templates/README.md): the six page types (E4) — goal, agent, project, audience, learning, decision.

**Open in Obsidian:** *Open folder as vault* → this `ledger/` folder (brain and templates in one graph; `.obsidian/app.json` uses relative markdown links, the Templates plugin points at `templates/`, per-machine state is git-ignored). The same links render on GitHub.

`runtime/internal/ledger` parses and checks every page (frontmatter, the `---` line, the timeline grammar, no sensitive tags); `go test ./internal/ledger/` runs it over this folder.
