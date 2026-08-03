package storage

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestR2RequestUsesAutoRegionAndBucketPath(t *testing.T) {
	var requestPath, authorization string
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.EscapedPath()
		authorization = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newTestR2(server, "musesapi")
	if err := client.Put(context.Background(), "alice/video.mp4", []byte("video"), "video/mp4"); err != nil {
		t.Fatal(err)
	}
	if requestPath != "/musesapi/alice/video.mp4" {
		t.Fatalf("path = %q", requestPath)
	}
	if !strings.Contains(authorization, "/auto/s3/aws4_request") {
		t.Fatalf("authorization does not use R2 auto region: %q", authorization)
	}
}

func TestR2ListParsesObjects(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<ListBucketResult><Contents><Key>alice/a.png</Key><LastModified>2026-08-03T00:00:00Z</LastModified><Size>42</Size></Contents><IsTruncated>false</IsTruncated></ListBucketResult>`)
	}))
	defer server.Close()

	client := newTestR2(server, "musesapi")
	objects, err := client.List(context.Background(), "alice/")
	if err != nil {
		t.Fatal(err)
	}
	if len(objects) != 1 || objects[0].Key != "alice/a.png" || objects[0].Size != 42 {
		t.Fatalf("unexpected objects: %#v", objects)
	}
}

func newTestR2(server *httptest.Server, bucket string) *Client {
	return &Client{
		endpoint: server.URL, host: strings.TrimPrefix(server.URL, "https://"),
		region: "auto", bucket: bucket, publicBaseURL: "https://pub.example.test",
		ak: "access-key", sk: "secret-key", http: server.Client(),
	}
}

func TestNormalizeR2ConfigSplitsCopiedS3API(t *testing.T) {
	got, err := NormalizeR2Config(R2Config{
		Endpoint:        "https://cbbca4b929b2a4a0d3618894ed8f15be.r2.cloudflarestorage.com/muses-r2bucket",
		Region:          "",
		PublicBaseURL:   "https://muses-r2bucket.ordoeden.com/",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.Endpoint != "https://cbbca4b929b2a4a0d3618894ed8f15be.r2.cloudflarestorage.com" || got.BucketName != "muses-r2bucket" || got.Region != "auto" {
		t.Fatalf("unexpected normalized config: %#v", got)
	}
}

func TestConfigureHotReloadsPublicURL(t *testing.T) {
	client := NewR2("", "auto", "", "", "", "")
	if client.Configured() {
		t.Fatal("empty first-run client should not be configured")
	}
	err := client.Configure(R2Config{
		Endpoint:        "https://cbbca4b929b2a4a0d3618894ed8f15be.r2.cloudflarestorage.com/muses-r2bucket",
		PublicBaseURL:   "https://muses-r2bucket.ordoeden.com",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret-key",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !client.Configured() {
		t.Fatal("client should be configured after hot reload")
	}
	if got := client.PublicURL("alice/video.mp4"); got != "https://muses-r2bucket.ordoeden.com/alice/video.mp4" {
		t.Fatalf("PublicURL = %q", got)
	}
}

func TestR2PublicURLUsesPublicBaseAndEscapesKey(t *testing.T) {
	client := NewR2("https://account.r2.cloudflarestorage.com", "auto", "musesapi", "https://media.example.com", "access-key", "secret-key")
	got := client.PublicURL("alice/my image.png")
	if got != "https://media.example.com/alice/my%20image.png" {
		t.Fatalf("PublicURL = %q", got)
	}
	key, ok := client.KeyFromPublicURL(got)
	if !ok || key != "alice/my image.png" {
		t.Fatalf("KeyFromPublicURL = %q, %v", key, ok)
	}
}
