# egress

The tavern's only door to the internet: an HTTP/CONNECT forward proxy that allows each hireling exactly the hosts in its profile.

- Identifies the caller by **reverse DNS** of its container name on the Docker network (`<project>-agent-<id>-N`) — no identity cache, because Docker reuses IPs at once.
- Allows `profile.network.allow` + the base list (model APIs; Composio when the profile has toolkits). A subdomain matches its parent.
- Ports 80 and 443 only; IP literals refused.
- Logs one line per decision: `allow|deny|fail <agent> <host:port> [reason]` — never paths, headers or bodies.

Runs distroless with 64 MB. Configuration: `EGRESS_ADDR` (`:3128`), `EGRESS_AGENTS_DIR` (`/agents`). Security model: [docs/security.md](../../docs/security.md).
