# Build context is the repository root:
#   docker build -f deploy/Dockerfile --target server -t hls-monitor-server .
#   docker build -f deploy/Dockerfile --target agent  -t hls-monitor-agent  .

FROM node:22-alpine AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# The server embeds the interface from this directory.
COPY --from=web /src/web/dist/ internal/webui/dist/
ARG VERSION=dev
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/hls-monitor-server ./cmd/hls-monitor-server \
 && go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/hls-monitor-agent ./cmd/hls-monitor-agent

FROM alpine:3.24.4 AS base
# The data directory is created and owned here so that a named volume
# mounted over it starts out writable by the unprivileged user.
RUN apk add --no-cache ca-certificates tzdata \
 && addgroup -S hlsmon && adduser -S -G hlsmon -H hlsmon \
 && mkdir -p /var/lib/hls-monitor && chown hlsmon:hlsmon /var/lib/hls-monitor
VOLUME /var/lib/hls-monitor
USER hlsmon

FROM base AS server
COPY --from=build /out/hls-monitor-server /usr/local/bin/hls-monitor-server
ENV HLSMON_LISTEN=:4000 \
    HLSMON_DB=/var/lib/hls-monitor/hls-monitor.db
EXPOSE 4000
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:4000/healthz >/dev/null || exit 1
ENTRYPOINT ["hls-monitor-server"]

FROM base AS agent
COPY --from=build /out/hls-monitor-agent /usr/local/bin/hls-monitor-agent
ENV HLSMON_KEY_FILE=/var/lib/hls-monitor/agent.key
ENTRYPOINT ["hls-monitor-agent"]
