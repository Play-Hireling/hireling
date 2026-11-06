# Run your tavern with Claude Code

Hireling is built to be driven from a terminal, so any coding agent that can run shell commands — [Claude Code](https://docs.claude.com/en/docs/claude-code/overview) included — can manage a tavern on `localhost`: give orders, watch progress, answer routine decisions you delegate, read the results on the common table, and add hirelings.

> Hireling is an independent project. "Works with Claude Code" means it runs well from it, nothing more.

## Set up

From the repository root, after the [quickstart](../getting-started/quickstart.md):

```bash
claude
```

Give it the context once (or put it in a `CLAUDE.md` in your working copy):

```markdown
# My tavern
- Control the tavern only with ./bin/tavern (status, order, decisions, choose, quest, tasks).
- The owner token is in .env — never print it.
- Read results with: docker compose exec agent-<id> cat /tavern/shared/<path>
- Never choose an option on a decision about payments, contracts, customer data, deleting or production. Show them to me instead.
- Profiles live in agents/<id>/; validate with (cd runtime && go test ./profile/...).
```

## Things to ask

- *"Order the Innkeeper to plan next week's content around our CSV export launch, then report back when the tasks are created."*
- *"Every few minutes, check `./bin/tavern status` and tell me when the Scriptwriter finishes T-2. Show me the draft."*
- *"List open decisions and summarize each in one line. Don't choose anything."*
- *"Add a Researcher hireling following docs/guides/add-a-hireling.md, validate the profile and restart the stack."*
- *"Why did the Dev's last turn fail? Look at its latest transcript in /opt/data/turns."*

## Why this works well

- **Plain HTTP + a POSIX shell CLI.** No SDK to install; JSON out, easy to parse.
- **Decisions are explicit objects** with ids and options, so an agent can present them to you instead of guessing.
- **The tavern's own limits still apply.** Whatever Claude Code does through `bin/tavern`, a hireling still can't reach a host outside its profile or skip the hard deny list.

## Prefer MCP?

The **Tavern MCP server** gives Claude Code (and any MCP client) the same console as tools and resources: orders, status, decisions, tasks and the quest, without the shell. Set it up with one `claude mcp add`: [Run your tavern from Claude Code over MCP](claude-code-mcp.md).
