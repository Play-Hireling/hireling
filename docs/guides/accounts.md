# Give hirelings accounts (Composio)

Hirelings act on your accounts — GitHub, Google Drive, Calendar, Instagram, … — through **[Composio](https://composio.dev)**. You connect each account **once per tavern** on Composio's own page; hirelings never see a password or an OAuth token.

## Turn it on

1. Create a Composio project and an API key.
2. In `.env`: `COMPOSIO_API_KEY=…` (and optionally `COMPOSIO_USER_ID`, default `hireling-tavern`).
3. In each hireling's profile, list the toolkits it may use:

```yaml
hands:
  toolkits: [github]            # dev
# hands:
#   toolkits: [googledrive, googledocs]   # scriptwriter
```

4. `docker compose up -d` and check: `hireling check` prints `hands composio …, toolkits github`.

## What the runtime sets up

- One Composio **Tool Router** session per hireling, limited to its toolkits, kept on its volume (`composio-session.json`).
- **Destructive tools off**, **remote workbench off**, **connection removal disabled**.
- The session is added to Hermes as the MCP server `composio`, with the key passed as `${COMPOSIO_API_KEY}` — Hermes expands it at connect time; it never hits the disk.
- `backend.composio.dev` is added to that hireling's egress allowlist.
- Finance, KYC, signature and tax toolkits are refused at profile load.

## Connecting an account

When a hireling needs an account that is not connected yet, it does not ask for credentials. It opens a decision with Composio's **Connect Link** (valid 10 minutes):

```bash
./bin/tavern decisions
# "Connect GitHub so I can open the PR? Link: https://… (10 min)"  options: ["Connected", "Send a new link", "Not now"]
```

Open the link, authorize on Composio's page, then `./bin/tavern choose <id> Connected`. The link expired? Choose **Send a new link**.

## Autonomy still applies

Reading and drafting in an account are free; posting, sending, inviting or changing settings follow the hireling's `capped` and `owner_decides` lists; payments, contracts and customer data are always yours.
