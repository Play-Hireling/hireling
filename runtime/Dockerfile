# Hireling runtime image (A1): one image for every hireling.
# Stage 1 builds the `hireling` loop (Go, static); stage 2 drops it into the
# official Hermes Agent image, so `hermes` and all its tools come from upstream.
#
# Build context is the repository root (compose.yaml sets it), so
# the image carries agents/ — the profile is picked at run time by HIRELING_AGENT.

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY runtime/go.mod runtime/go.sum ./
RUN go mod download
COPY runtime/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/hireling ./cmd/hireling

# Pin by digest in production (G10 rollback needs reproducible images):
# docker buildx imagetools inspect nousresearch/hermes-agent:latest
FROM nousresearch/hermes-agent:latest

# Keep the upstream ENTRYPOINT (s6 /init → entrypoint-dispatch.sh): an
# executable on PATH given as the command runs directly, so `hireling run` works.
COPY --from=build /out/hireling /usr/local/bin/hireling
COPY agents/ /agents/
# The common table and the private notes dir exist in the image owned by the
# hermes user, so a fresh named volume is initialised with the right owner.
RUN mkdir -p /tavern/shared /opt/data/notes /opt/data/turns && chown -R 10000:10000 /tavern /opt/data

# A7: Hermes' file tools only write under HERMES_WRITE_SAFE_ROOT (the upstream
# image sets it to /opt/data, so the Tester could not write on the common table
# except through the terminal). The common table is a second safe root; the
# private volume stays the first.
ENV HERMES_HOME=/opt/data \
    HERMES_WRITE_SAFE_ROOT=/opt/data:/tavern/shared \
    HIRELING_AGENTS_DIR=/agents \
    HIRELING_AGENT=test \
    PUID=10000 PGID=10000

# /opt/data  = private memory (volume tavern-<id>), owned by the hermes user (UID 10000)
# /tavern/shared = the common table (volume tavern-shared)
VOLUME ["/opt/data", "/tavern/shared"]

CMD ["hireling", "run"]
