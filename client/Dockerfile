# Data-only self-host tavern client.
# Build context (scripts/pack-client.sh):
#   dist/       the playhireling self-host build
#   licenses/   NOTICE, LICENSE-CLIENT.md, THIRD-PARTY.md
# The image's only job is to copy those files onto a volume and exit.
# tavern-core serves the copy at /tavern/ from a read-only mount.
# docs/provenance is not copied. packages/assets/withheld is not copied.
FROM busybox:1.37

ARG REVISION=unknown
LABEL org.opencontainers.image.title="hireling-client" \
      org.opencontainers.image.description="Data-only self-host tavern client, served by tavern-core at /tavern/." \
      org.opencontainers.image.licenses="LicenseRef-Hireling-Client" \
      org.opencontainers.image.source="https://github.com/Play-Hireling/hireling" \
      org.opencontainers.image.url="https://github.com/Play-Hireling/hireling" \
      org.opencontainers.image.documentation="https://github.com/Play-Hireling/hireling/blob/main/LICENSE-CLIENT.md" \
      org.opencontainers.image.vendor="Play Hireling" \
      org.opencontainers.image.revision="${REVISION}"

COPY dist/ /dist/
COPY licenses/ /licenses/
VOLUME /out
# Copies the game and the public notices. Does not copy provenance.
ENTRYPOINT ["sh", "-c", "mkdir -p /out/licenses && cp -a /dist/. /out/ && cp -a /licenses/. /out/licenses/ && chmod -R a+rX /out && echo client-ready"]
