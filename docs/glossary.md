# Glossary

| Term | Meaning |
| --- | --- |
| **Tavern** | One team of hirelings for one owner and one goal; one Compose project |
| **Owner** | The human who gives orders and makes decisions (the Executive Director in the game) |
| **Hireling** | One AI agent: a container with Hermes + the `hireling` runtime, defined by `agents/<id>/` |
| **Innkeeper** | The planner hireling: splits requests into tasks and approves handoffs |
| **Party** | The tavern's hirelings |
| **Front Desk** | The API where the owner orders and hirelings report (`/api/*`) |
| **The bar** | Redis Streams: inboxes, the shared room, events |
| **Common table** | `/tavern/shared`, files shared by all hirelings |
| **Desk drawer** | A hireling's private volume, `/opt/data` |
| **Quest** | The single shared goal; every task declares how it moves it |
| **Contribution kind** | How a task moves the quest: revenue, leads, posts, videos, support, … |
| **Lease** | Temporary ownership of a task (15 min, renewed per step) |
| **Handoff** | A hireling asking for work from another, approved by the planner |
| **Waiting on you** | Decisions only the owner can make |
| **Escalation** | A decision the runtime opens itself after repeated failures on a task |
| **Mana** | A hireling's daily token budget |
| **Autonomy** | `free` / `capped` / `owner_decides`, per action, per hireling |
| **Hard deny list** | payment, contract, customer_data, delete, production_critical — always the owner's |
| **Hands** | The owner's accounts, reached through Composio toolkits |
| **Tavern Ledger** | Shared long-term memory: markdown in git, indexed by GBrain, written only by ledger-writer |
| **Egress** | The proxy that is the tavern's only door to the internet |
| **Server flag** | Where a hireling runs (server + region), shown in the game |
| **Answer block** | A fenced block at the end of an answer (`plan`, `request`, `decision`, `ask`, `ledger`) that the runtime acts on |
