# Add a hireling

A new role is two files and one Compose block. Example: a **Researcher** that reads the web and writes market notes.

## 1. Profile and prompt

```bash
mkdir agents/researcher
cp agents/scriptwriter/profile.yaml agents/researcher/
cp agents/scriptwriter/prompt.md agents/researcher/
```

`agents/researcher/profile.yaml`:

```yaml
# researcher — market and competitor notes. Schema: docs/reference/profile.md.
id: researcher                      # lowercase slug, stable forever
display_name_key: agent.researcher
model: deepseek-v4-flash
home_server: local
sprite: scriptwriter                # which sprite the game client draws

budget:
  tokens_per_day: 120000
  web_calls_per_day: 150

autonomy:
  free: [draft, research, write_shared_file, message_agent]
  capped: {}
  owner_decides: [payment, contract, customer_data, delete, production_critical]

tools: [files, web]                 # informational today; real access = network.allow + hands

network:
  allow: [news.ycombinator.com, reddit.com, wikipedia.org]   # a subdomain matches its parent

secrets: [DEEPSEEK_API_KEY, OPENROUTER_API_KEY, FRONTDESK_OWNER_TOKEN]

ledger:
  read: [goal, audiences/*, learnings/*, projects/*]
  write: [learnings/researcher/*, audiences/*]

runtime:
  poll_seconds: 20
  turn_ttl_minutes: 15
```

`agents/researcher/prompt.md` — keep the structure of the starter prompts:

```markdown
You are the **Researcher** of the Hireling tavern: market, competitor and audience notes.

## Your job
- Answer the question asked, with sources (links) for every claim.
- One note per question on the common table: `/tavern/shared/<command-id>/note.md`, a 5-line summary on top.

## Hard rules
(copy the hard rules from another starter prompt verbatim — they are the same for every hireling)

## How you work
(copy and adapt)
```

## 2. Validate

```bash
cd runtime && go test ./profile/...      # loads every profile under agents/
```

Common rejections: an action from the hard deny list under `free`/`capped`; a host with a scheme, port or wildcard; a blocked or sensitive host; an unknown model.

## 3. Compose service

Copy an `agent-*` block in `compose.yaml`:

```yaml
  agent-researcher:
    <<: *hireling
    environment:
      <<: *hireling-env
      HIRELING_AGENT: researcher
    volumes: ["tavern-researcher:/opt/data", "tavern-shared:/tavern/shared", "gbrain-tokens:/run/gbrain:ro"]
```

…and add `tavern-researcher:` under `volumes:`. With the Ledger on, also add `researcher` to `HIRELING_LEDGER_READERS` on the `gbrain` service.

## 4. Start it

Profiles are baked into the images, so rebuild:

```bash
docker compose up -d --build
docker compose exec -u hermes agent-researcher hireling check
./bin/tavern order researcher "Three competitors of our CSV export, one paragraph each, with links"
```

The Innkeeper sees the new hireling in its party on its next turn (`hireling check` on the Innkeeper prints the party).

## Tips

- **Small and sharp beats broad.** A hireling with one job and three tools plans better and costs less.
- **Start with `network.allow: []`** and add hosts as `egress deny` lines show what it really needs.
- **Never give a hireling a capability you would not give a new contractor on day one.**
- Share it: a good role is a great first pull request (`agents/<id>/` + a line in [docs/concepts/hirelings.md](../concepts/hirelings.md)).
