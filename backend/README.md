# Vivid AI Backend

Go backend for `vivid-ai`, using:

- Gin
- GORM
- PostgreSQL
- Redis

## Current scope

This is an in-progress rewrite. The current skeleton already includes:

- app bootstrap
- PostgreSQL and Redis initialization
- GORM auto-migrations
- session storage in Redis
- Cloudflare R2 storage with public custom-domain reads
- public site endpoint: `/admin/api/site`
- public showcase endpoint: `/admin/api/showcase`
- session-based auth endpoint: `/admin/api/auth/me`

## Environment

Set these before running:

```powershell
$env:POSTGRES_DSN="host=127.0.0.1 user=postgres password=postgres dbname=vivid_ai port=5432 sslmode=disable TimeZone=Asia/Shanghai"
$env:REDIS_ADDR="127.0.0.1:6379"
$env:HTTP_ADDR=":6061"
```

Optional:

```powershell
$env:APP_ENV="development"
$env:APP_TITLE="Vivid AI"
$env:SESSION_COOKIE_NAME="vivid_session"
$env:CORS_ORIGINS="http://localhost:5173,http://127.0.0.1:5173"
```

## Run

```powershell
go run ./cmd/api
```

## Notes

- Generated media is uploaded to R2; the local generated root remains only for development compatibility.
- R2 is configured by an administrator in the web Settings page and persisted in PostgreSQL.
- R2 credentials are not read from environment variables.
