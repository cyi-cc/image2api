# MusesAPI combined application image

The root `Dockerfile` builds the Go backend and Vue frontend into one application
image. The Vue build is embedded into the Go binary, which serves both the API
and SPA on port 2000.
There is no nginx process inside this image.

PostgreSQL and Redis remain separate services. Media is stored directly in the
public Cloudflare R2 bucket and read through its custom domain. The supplied
nginx service is separate and optional.

## Cloudflare R2

Create the bucket, enable its public custom domain, and create an Object Read &
Write API token in Cloudflare. Start MusesAPI first, then open **Admin → Settings
→ Cloudflare R2 Storage** and enter:

```text
S3 API:           https://cbbca4b929b2a4a0d3618894ed8f15be.r2.cloudflarestorage.com/muses-r2bucket
Public URL:       https://muses-r2bucket.ordoeden.com
Region:           auto
Access Key ID:    your R2 token access key
Secret Access Key: your R2 token secret
```

You may paste Cloudflare's complete S3 API. MusesAPI splits its bucket suffix
automatically. Values are saved in PostgreSQL and activated immediately; they
are not placed in Compose or `.env`, and the secret is never returned in full.

Set this in the bucket's **CORS policy**, replacing the origins with the ones you
actually use:

```json
[
  {
    "AllowedOrigins": ["https://ai.example.com", "http://YOUR_SERVER_IP:2000"],
    "AllowedMethods": ["GET", "HEAD"],
    "AllowedHeaders": ["*"],
    "ExposeHeaders": ["Content-Length", "Content-Type", "ETag"],
    "MaxAgeSeconds": 3600
  }
]
```

## Tag release workflow

Configure these GitHub Actions repository secrets before publishing the first
tag:

```text
DOCKERHUB_USERNAME
DOCKERHUB_TOKEN
```

Push a version tag to trigger both the GitHub Release and multi-architecture
Docker image workflows:

```bash
git tag v1.0.0
git push origin v1.0.0
```

The Docker workflow publishes `heself/musesapi:v1.0.0` and
`heself/musesapi:latest` for Linux AMD64 and ARM64. The release workflow uploads
self-contained Linux binaries, a Compose deployment bundle, and SHA-256 sums.

## Build locally

From the repository root:

```bash
docker build -t musesapi:1.0.0 .
```

Apple Silicon creates an ARM64 image by default. For a typical Intel/AMD Linux
server, build an AMD64 image instead:

```bash
docker buildx build \
  --platform linux/amd64 \
  -t heself/musesapi:1.0.0 \
  --push .
```

To publish both AMD64 and ARM64 under one tag:

```bash
docker buildx build \
  --platform linux/amd64,linux/arm64 \
  -t heself/musesapi:1.0.0 \
  --push .
```

## Deploy from a registry

Copy only this file to the server:

```text
compose.yml
```

Start the stack directly—no `.env` file is required:

```bash
docker compose -f compose.yml pull
docker compose -f compose.yml up -d
docker compose -f compose.yml ps
```

When using a private registry, run `docker login` on the server first.

## Direct-IP mode (no nginx)

Start without a profile. The optional nginx service will not be created:

```bash
docker compose -f compose.yml up -d
curl http://YOUR_SERVER_IP:2000/health
```

Open `http://YOUR_SERVER_IP:2000` in a browser.

## Optional separate nginx service

`nginx` is a sibling of `musesapi`, `postgres`, and `redis` in the
Compose file. It is guarded by the `proxy` profile, so it only starts when
explicitly enabled:

```bash
docker compose -f compose.yml --profile proxy up -d
```

For this mode, copy `nginx.conf` to the same server directory as `compose.yml`.
The included configuration listens on HTTP port 80 and forwards all requests to
`musesapi:2000`. For a public production site, add TLS certificates or use a
host-level Caddy/nginx reverse proxy, then set:

```dotenv
CORS_ORIGINS=https://ai.example.com
COOKIE_SECURE=true
```

## Deploy without a registry

Build the server's architecture and export it locally:

```bash
docker buildx build \
  --platform linux/amd64 \
  -t musesapi:1.0.0 \
  --load .
docker save musesapi:1.0.0 | gzip > musesapi-1.0.0-amd64.tar.gz
```

Upload the tarball and `compose.yml` to the server, then run:

```bash
gzip -dc musesapi-1.0.0-amd64.tar.gz | docker load
MUSESAPI_IMAGE=musesapi:1.0.0 docker compose -f compose.yml up -d
```

## Verify

For direct-IP mode:

```bash
curl http://YOUR_SERVER_IP:2000/health
docker compose -f compose.yml logs -f musesapi
```
