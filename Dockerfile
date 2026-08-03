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
RUN go mod download
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
