# Profile schema — `agents/<id>/profile.yaml`

One file per hireling, next to its `prompt.md`. Loaded by the runtime, tavern-core (policy gate, Front Desk), egress (allowlist) and ledger-writer (write areas). A profile that fails validation stops the container at start with a clear error.

```yaml
id: scriptwriter                     # required · lowercase slug · stable forever
display_name_key: agent.scriptwriter # UI string key (the game client translates it)
desk_id: scriptwriter                # optional · id on the Front Desk · default = id
model: deepseek-v4-flash             # required · deepseek-v4-flash | deepseek-v4-pro
home_server: local                   # where it is meant to run (server flag)
region: ""                           # ISO country code for the flag, optional
sprite: scriptwriter                 # sprite set in the game client
planner: false                       # true for the one hireling that plans (the Innkeeper)

budget:
  tokens_per_day: 150000             # mana · per UTC day; over it only P0/P1 run
  web_calls_per_day: 200

autonomy:
  free: [draft, research, write_shared_file, message_agent]
  capped:
    publish_draft_to_cms: { per_day: 3 }
  owner_decides: [payment, contract, customer_data, delete, production_critical]

tools: [files]                       # informational today (shown in the game)

secrets: [DEEPSEEK_API_KEY, OPENROUTER_API_KEY]   # names only · values are masked everywhere

network:
  allow: [youtube.com, googleapis.com]

hands:
  toolkits: [googledocs, googledrive]

ledger:
  read: [goal, audiences/*, learnings/*, projects/*]
  write: [learnings/scriptwriter/*, projects/*/scripts/*]

fallback:
  model: deepseek/deepseek-v4-flash  # OpenRouter slug · default = same model
  disabled: false

runtime:
  poll_seconds: 20
  turn_ttl_minutes: 15
  escalate_after: 3
  chat_reasoning: low
  screen: code
```

## Fields

### `id`, `desk_id`
Lowercase slug (`a-z`, `0-9`, `-`). The id names the container (`agent-<id>`), the inbox (`inbox:<id>`), the volume (`tavern-<id>`) and the token variable (`FRONTDESK_TOKEN_<ID>`). `desk_id` exists for taverns whose Front Desk already knows the agent under another name; leave it out otherwise.

### `model`
`deepseek-v4-flash` or `deepseek-v4-pro`. Anything else is rejected. See [Models and providers](../guides/models.md).

### `planner`
Marks the hireling whose answers may carry a `plan` block and who approves handoffs. Exactly one per tavern is the norm.

### `budget`
`tokens_per_day` is the hireling's daily mana: every turn's usage is recorded against it and reported by `GET /agents/<id>/budget`. Once it is reached, the runtime runs only P0/P1 work (D7): your commands and answers, bar messages about P0/P1 tasks, and alerts. Any other bar message is held, unacked, and runs after 00:00 UTC. Set it to `0` for no budget.

### `autonomy`
Action names are free-form slugs that prompts and the policy gate use. Rules enforced at load:
- an action whose name falls in a hard-deny category (payment, contract, customer data, delete, production-critical — matched by name, so `send_payment` or `delete_post` count) **cannot** be `free` or `capped`;
- the five hard-deny actions are always added to `owner_decides`.

`capped` limits take `per_day` or `per_week`.

### `network`
<a id="network"></a>
Hosts this hireling may reach through egress, on top of the base list (`api.deepseek.com`, `openrouter.ai`; `backend.composio.dev` when `hands.toolkits` is set). Plain host names only — no scheme, port, path, wildcard or IP. A subdomain matches its parent (`github.com` allows `api.github.com`). Never allowed: `localhost`, `127.0.0.1`, `0.0.0.0`, `host.docker.internal`, cloud metadata hosts, and hosts that look like finance, KYC or legal systems.

### `hands`
Composio toolkit slugs (lowercase). Finance, KYC, signature and tax toolkits are refused. See [Accounts](../guides/accounts.md).

### `ledger`
Areas of the Ledger, as paths relative to `brain/` with `*` for one level. `read` decides whether the hireling gets a read token and what its prompt says to search; `write` is checked by ledger-writer on every page.

### `secrets`
Names of environment variables whose **values** the redactor masks in everything that leaves the container. List every secret the hireling's environment carries.

### `runtime`

| Key | Default | Meaning |
| --- | --- | --- |
| `poll_seconds` | 20 | Polling interval when the Front Desk has no long poll |
| `turn_ttl_minutes` | 15 | A silent turn expires on the Front Desk after this; keepalive steps every third |
| `escalate_after` | 3 | Failed turns in a row on one task before it escalates to you |
| `chat_reasoning` | `low` | Reasoning effort for conversation turns; `default` keeps the model's |
| `screen` | `code` | Which screen the game client shows at the hireling's desk |

## `prompt.md`

Plain markdown: the role, the job, the **hard rules** (identical for every hireling — copy them), how it works. The runtime appends the quest bar, the plan / request / decision / ask / ledger instructions, the hands and the Ledger instructions as they apply. Keep the prompt about the *job*; the runtime handles the protocol.
