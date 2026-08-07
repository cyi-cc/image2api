# syntax=docker/dockerfile:1

# onnxruntime 版本要与 go.mod 里 yalue/onnxruntime_go 的 API 版本匹配
ARG ORT_VERSION=1.28.0

# --- Stage 1: build the Go binary from source ---
# YuNet 人脸检测走 onnxruntime，需要 CGO(gcc) + libonnxruntime 动态库，
# 因此构建/运行镜像都用 glibc 的 debian（onnxruntime 官方包不支持 musl/alpine）。
FROM golang:1.26-bookworm AS build
ARG ORT_VERSION
WORKDIR /src
RUN wget -qO /tmp/ort.tgz https://github.com/microsoft/onnxruntime/releases/download/v${ORT_VERSION}/onnxruntime-linux-x64-${ORT_VERSION}.tgz \
    && tar -xzf /tmp/ort.tgz -C /opt && rm /tmp/ort.tgz
# Cache deps first for faster rebuilds.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/api ./cmd/api

# --- Stage 2: minimal runtime image ---
FROM debian:bookworm-slim
ARG ORT_VERSION
# ca-certificates: outbound HTTPS to the AI providers. tzdata: POSTGRES_DSN sets
# TimeZone=Asia/Shanghai. wget: container healthcheck.
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates tzdata wget \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /opt/onnxruntime-linux-x64-${ORT_VERSION}/lib/libonnxruntime.so* /usr/local/lib/
WORKDIR /app
COPY --from=build /out/api /app/api
# Local fallback for generated media / reference uploads (RustFS/S3 is primary).
RUN mkdir -p /app/data/generated && chmod +x /app/api
ENV HTTP_ADDR=0.0.0.0:6666
EXPOSE 6666
ENTRYPOINT ["/app/api"]
