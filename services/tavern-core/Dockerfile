# tavern-core image. Build context is the repository root (it needs runtime/
# for the shared profile package and agents/ for the profiles).
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY runtime/go.mod runtime/go.sum ./runtime/
COPY services/tavern-core/go.mod services/tavern-core/go.sum ./services/tavern-core/
RUN cd services/tavern-core && go mod download
COPY runtime/ ./runtime/
COPY services/tavern-core/ ./services/tavern-core/
RUN cd services/tavern-core && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/tavern-core ./cmd/tavern-core

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tavern-core /tavern-core
COPY agents/ /agents/
ENV TAVERN_ADDR=:8080 TAVERN_AGENTS_DIR=/agents
EXPOSE 8080
USER nonroot
ENTRYPOINT ["/tavern-core"]
