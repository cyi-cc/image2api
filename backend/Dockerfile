# syntax=docker/dockerfile:1

# --- Stage 1: build the Go binary from source ---
FROM golang:1.26-alpine AS build
WORKDIR /src
RUN apk add --no-cache git
# Cache deps first for faster rebuilds.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# --- Stage 2: minimal runtime image ---
FROM alpine:3.20
# ca-certificates: outbound HTTPS to the AI providers. tzdata: POSTGRES_DSN sets
# TimeZone=Asia/Shanghai. wget: container healthcheck.
RUN apk add --no-cache ca-certificates tzdata wget
WORKDIR /app
COPY --from=build /out/api /app/api
# Runtime working directory; generated media is stored in Cloudflare R2.
RUN mkdir -p /app/data/generated && chmod +x /app/api
ENV HTTP_ADDR=0.0.0.0:6666
EXPOSE 6666
ENTRYPOINT ["/app/api"]
