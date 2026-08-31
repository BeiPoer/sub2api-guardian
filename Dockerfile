# syntax=docker/dockerfile:1.7

FROM node:22-alpine AS frontend-builder
WORKDIR /src/frontend
RUN corepack enable && corepack prepare pnpm@9.15.9 --activate
COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY frontend/ ./
RUN pnpm run build

FROM golang:1.24.1-alpine AS backend-builder
WORKDIR /src/backend
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
COPY --from=frontend-builder /src/backend/internal/web/dist ./internal/web/dist
RUN CGO_ENABLED=0 GOOS=linux go build \
    -buildvcs=false -trimpath -ldflags="-s -w" \
    -o /out/guardian ./cmd/guardian

FROM alpine:3.22
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -S guardian \
    && adduser -S -D -H -G guardian guardian \
    && mkdir -p /data \
    && chown guardian:guardian /data \
    && chmod 0700 /data
COPY --from=backend-builder /out/guardian /usr/local/bin/guardian

ENV TZ=Asia/Shanghai \
    GUARDIAN_ADDR=0.0.0.0:8787 \
    GUARDIAN_DATA_DIR=/data
EXPOSE 8787
VOLUME ["/data"]
USER guardian
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:8787/healthz || exit 1
ENTRYPOINT ["/usr/local/bin/guardian"]
