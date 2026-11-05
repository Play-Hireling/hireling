# egress image (A8): the tavern's forward proxy. Build context is the repository
# root (it needs runtime/ for the shared profile package and agents/ for the
# allowlists).
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY runtime/go.mod runtime/go.sum ./runtime/
COPY services/egress/go.mod services/egress/go.sum ./services/egress/
RUN cd services/egress && go mod download
COPY runtime/ ./runtime/
COPY services/egress/ ./services/egress/
RUN cd services/egress && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/egress ./cmd/egress

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/egress /egress
COPY agents/ /agents/
ENV EGRESS_ADDR=:3128 EGRESS_AGENTS_DIR=/agents
EXPOSE 3128
USER nonroot
ENTRYPOINT ["/egress"]
