# Ledger writer (E3): commits hirelings' Ledger pages to the brain repo as
# their author. Build context is the repository root: the code is the runtime
# module (it shares the page validator and the profiles) and agents/ gives the
# ledger.write areas.
FROM golang:1.27-alpine AS build
WORKDIR /src
COPY runtime/go.mod runtime/go.sum ./
RUN go mod download
COPY runtime/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ledger-writer ./cmd/ledger-writer

FROM alpine:3.20
RUN apk add --no-cache git ca-certificates \
 && addgroup -g 10002 writer && adduser -D -u 10002 -G writer -h /repo writer \
 && mkdir -p /repo && chown writer:writer /repo
COPY --from=build /out/ledger-writer /usr/local/bin/ledger-writer
COPY agents/ /agents/
ENV LEDGER_WRITER_ADDR=:8090 LEDGER_WRITER_DIR=/repo/hireling-ledger HIRELING_AGENTS_DIR=/agents HOME=/repo
USER writer
EXPOSE 8090
ENTRYPOINT ["/usr/local/bin/ledger-writer"]
