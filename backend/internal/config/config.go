package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv            string
	HTTPAddr          string
	AppTitle          string
	PostgresDSN       string
	RedisAddr         string
	RedisPassword     string
	RedisDB           int
	SessionCookieName string
	CookieSecure      bool
	SessionTTL        time.Duration
	SessionSlideAfter time.Duration
	CORSOrigins       []string
	FrontendRoot      string
	GeneratedRoot     string
}

func Load() (*Config, error) {
	loadDotEnv()

	wd, err := os.Getwd()
	if err != nil {
		return nil, err
	}

	cfg := &Config{
		AppEnv:            envString("APP_ENV", "development"),
		HTTPAddr:          envString("HTTP_ADDR", ":6061"),
		AppTitle:          envString("APP_TITLE", "Vivid AI"),
		PostgresDSN:       envString("POSTGRES_DSN", "host=127.0.0.1 user=postgres password=postgres dbname=vivid_ai port=5432 sslmode=disable TimeZone=Asia/Shanghai"),
		RedisAddr:         envString("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:     envString("REDIS_PASSWORD", ""),
		RedisDB:           envInt("REDIS_DB", 0),
		SessionCookieName: envString("SESSION_COOKIE_NAME", "vivid_session"),
		CookieSecure:      envBool("COOKIE_SECURE", false),
		SessionTTL:        time.Duration(envInt("SESSION_TTL_HOURS", 24)) * time.Hour,
		SessionSlideAfter: time.Duration(envInt("SESSION_SLIDE_AFTER_HOURS", 22)) * time.Hour,
		CORSOrigins:       envList("CORS_ORIGINS", []string{"http://localhost:5173", "http://127.0.0.1:5173"}),
		FrontendRoot:      filepath.Clean(envString("FRONTEND_ROOT", "")),
		GeneratedRoot: filepath.Clean(envString(
			"GENERATED_ROOT",
			// vivid-ai's own data dir (backend/data/generated) — NOT the Python
			// original's tree. Generated outputs and user-uploaded reference
			// compatibility helpers still use this path in development. Production
			// generated outputs and references are persisted in R2.
			filepath.Join(wd, "data", "generated"),
		)),
	}
	return cfg, nil
}

// loadDotEnv loads a .env file (KEY=VALUE per line) into the process environment
// before config is read. Real environment variables always win — .env only fills
// keys that aren't already set. Searches ENV_FILE, then walks up from the working
// directory so it works whether the binary runs from backend/ or the repo root.
func loadDotEnv() {
	for _, path := range dotEnvCandidates() {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		applyDotEnv(string(data))
		return
	}
}

func dotEnvCandidates() []string {
	var out []string
	if v := strings.TrimSpace(os.Getenv("ENV_FILE")); v != "" {
		out = append(out, v)
	}
	wd, err := os.Getwd()
	if err != nil {
		return out
	}
	dir := wd
	for i := 0; i < 4; i++ {
		out = append(out, filepath.Join(dir, ".env"))
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return out
}

func applyDotEnv(content string) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if key == "" {
			continue
		}
		// Real env wins: only set keys that aren't already present.
		if _, ok := os.LookupEnv(key); !ok {
			_ = os.Setenv(key, val)
		}
	}
}

func envString(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func envInt(key string, fallback int) int {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return fallback
}

func envList(key string, fallback []string) []string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		parts := strings.Split(v, ",")
		out := make([]string, 0, len(parts))
		for _, part := range parts {
			s := strings.TrimSpace(part)
			if s != "" {
				out = append(out, s)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return fallback
}
