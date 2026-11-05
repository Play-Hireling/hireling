# GBrain (E1): the Tavern Ledger's index and MCP server.
# Build context is the repository root, like the other services.
#
# GBrain is a Bun CLI installed from its GitHub tag (it is not on npm: the npm
# package named "gbrain" is unrelated). The tag is pinned so a deploy never
# picks up a new GBrain by surprise; bump GBRAIN_REF in a PR.
FROM oven/bun:1.4.4-debian

ARG GBRAIN_REF=stable-v0.60.474.0

# git: GBrain shells out to it for sync; our loop pulls the brain repo with it.
# curl: healthcheck. tini: PID 1, reaps the sync loop next to `gbrain serve`.
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates curl tini \
 && rm -rf /var/lib/apt/lists/*

ENV BUN_INSTALL=/opt/bun \
    PATH=/opt/bun/bin:$PATH \
    GBRAIN_NO_AUTOPILOT_INSTALL=1
RUN bun install -g "github:garrytan/gbrain#${GBRAIN_REF}" \
 && gbrain --version

RUN groupadd -g 10001 gbrain \
 && useradd -u 10001 -g gbrain -d /data -M -s /usr/sbin/nologin gbrain \
 && mkdir -p /data /ledger /tokens && chown gbrain:gbrain /data /ledger /tokens

COPY services/gbrain/entrypoint.sh /usr/local/bin/gbrain-entrypoint
RUN chmod 0755 /usr/local/bin/gbrain-entrypoint

ENV GBRAIN_HOME=/data \
    GBRAIN_NON_INTERACTIVE=1 \
    GBRAIN_SERVE_FAIL_FAST=1 \
    LEDGER_DIR=/ledger/brain-repo \
    LEDGER_SOURCE=ledger \
    LEDGER_SYNC_SECONDS=120
USER gbrain
WORKDIR /data
EXPOSE 3131
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/gbrain-entrypoint"]
