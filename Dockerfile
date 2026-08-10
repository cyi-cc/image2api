# syntax=docker/dockerfile:1

# Build the Vue frontend first so it can be embedded into the Go binary.
FROM node:22-alpine AS frontend-build
WORKDIR /src
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

# Build one self-contained MusesAPI binary (API + embedded Vue SPA).
FROM golang:1.26-alpine AS backend-build
WORKDIR /src
RUN apk add --no-cache git
COPY backend/go.mod backend/go.sum ./
# proxy.golang.org may reset connections in some networks. A `|`-separated
# GOPROXY list falls through on transport errors (unlike `,`, which only falls
# through on 404/410). Keep it overridable for private or corporate mirrors.
ARG GOPROXY="https://proxy.golang.org|https://goproxy.cn|direct"
ENV GOPROXY=${GOPROXY}
RUN for attempt in 1 2 3; do \
      go mod download && exit 0; \
      echo "go mod download failed (attempt ${attempt}/3); retrying..." >&2; \
      sleep "${attempt}"; \
    done; \
    exit 1
COPY backend/ ./
RUN rm -rf internal/frontend/dist
COPY --from=frontend-build /src/dist ./internal/frontend/dist
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build \
    -trimpath -ldflags="-s -w" \
    -o /out/musesapi ./cmd/api

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget

WORKDIR /app
COPY --from=backend-build /out/musesapi /app/musesapi
RUN mkdir -p /app/data/generated && chmod +x /app/musesapi

ENV APP_ENV=production \
    HTTP_ADDR=0.0.0.0:2000 \
    GENERATED_ROOT=/app/data/generated

EXPOSE 2000

HEALTHCHECK --interval=10s --timeout=5s --retries=10 \
    CMD wget -qO- http://127.0.0.1:2000/health || exit 1

ENTRYPOINT ["/app/musesapi"]
