package router

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestServeFrontend(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("SPA INDEX"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "assets", "app.js"), []byte("APP JS"), 0o644); err != nil {
		t.Fatal(err)
	}

	engine := gin.New()
	serveFrontend(engine, os.DirFS(root))

	tests := []struct {
		name       string
		path       string
		status     int
		body       string
		contentSub string
	}{
		{name: "home", path: "/", status: http.StatusOK, body: "SPA INDEX", contentSub: "text/html"},
		{name: "vue route", path: "/admin/overview", status: http.StatusOK, body: "SPA INDEX", contentSub: "text/html"},
		{name: "static asset", path: "/assets/app.js", status: http.StatusOK, body: "APP JS", contentSub: "javascript"},
		{name: "missing asset", path: "/assets/missing.js", status: http.StatusNotFound},
		{name: "unknown api", path: "/v1/missing", status: http.StatusNotFound, contentSub: "application/json"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, tt.path, nil)
			engine.ServeHTTP(recorder, request)
			if recorder.Code != tt.status {
				t.Fatalf("status = %d, want %d; body=%q", recorder.Code, tt.status, recorder.Body.String())
			}
			if tt.body != "" && strings.TrimSpace(recorder.Body.String()) != tt.body {
				t.Fatalf("body = %q, want %q", recorder.Body.String(), tt.body)
			}
			if tt.contentSub != "" && !strings.Contains(recorder.Header().Get("Content-Type"), tt.contentSub) {
				t.Fatalf("Content-Type = %q, want substring %q", recorder.Header().Get("Content-Type"), tt.contentSub)
			}
		})
	}
}
