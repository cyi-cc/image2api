// Package storage implements the subset of Cloudflare R2's S3-compatible API
// used by MusesAPI. It signs requests with AWS Signature V4 using only the Go
// standard library. Put / Get / Delete / List cover generated media, the
// public media URLs, the legacy /images proxy, the admin gallery, and retention
// cleanup.
//
// The surface mirrors what a thin wrapper over aws-sdk-go-v2 would expose, so it
// can be swapped for the official SDK later by reimplementing this one file.
package storage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const service = "s3"

type Client struct {
	mu            sync.RWMutex
	endpoint      string // e.g. https://<account>.r2.cloudflarestorage.com
	host          string
	region        string
	bucket        string
	publicBaseURL string
	ak, sk        string
	http          *http.Client
}

// R2Config is the runtime configuration stored in PostgreSQL. Endpoint is the
// authenticated S3 API; PublicBaseURL is the public r2.dev/custom-domain base.
type R2Config struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	BucketName      string `json:"bucket_name"`
	PublicBaseURL   string `json:"public_base_url"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// Object is one entry returned by List.
type Object struct {
	Key          string
	Size         int64
	LastModified time.Time
}

// NewR2 builds a Cloudflare R2 client. endpoint must be the account-level S3
// API endpoint (https://<ACCOUNT_ID>.r2.cloudflarestorage.com), not an r2.dev or
// custom public-domain URL. Cloudflare's signing region is normally "auto".
func NewR2(endpoint, region, bucket, publicBaseURL, accessKeyID, secretAccessKey string) *Client {
	c := &Client{http: &http.Client{Timeout: 60 * time.Second}}
	_ = c.Configure(R2Config{
		Endpoint: endpoint, Region: region, BucketName: bucket,
		PublicBaseURL: publicBaseURL, AccessKeyID: accessKeyID,
		SecretAccessKey: secretAccessKey,
	})
	return c
}

// NormalizeR2Config validates and canonicalizes settings entered in the admin
// UI. Cloudflare's copied S3 API may include /<bucket>; accept that form and
// split it automatically so signed requests never contain the bucket twice.
func NormalizeR2Config(in R2Config) (R2Config, error) {
	in.Endpoint = strings.TrimRight(strings.TrimSpace(in.Endpoint), "/")
	in.Region = strings.TrimSpace(in.Region)
	if in.Region == "" {
		in.Region = "auto"
	}
	in.BucketName = strings.Trim(strings.TrimSpace(in.BucketName), "/")
	in.PublicBaseURL = strings.TrimRight(strings.TrimSpace(in.PublicBaseURL), "/")
	in.AccessKeyID = strings.TrimSpace(in.AccessKeyID)
	in.SecretAccessKey = strings.TrimSpace(in.SecretAccessKey)

	parsed, err := url.Parse(in.Endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return R2Config{}, fmt.Errorf("S3 API 必须是完整的 HTTPS 地址")
	}
	if parsed.Scheme != "https" {
		return R2Config{}, fmt.Errorf("S3 API 必须使用 HTTPS")
	}
	if !strings.HasSuffix(strings.ToLower(parsed.Hostname()), ".r2.cloudflarestorage.com") {
		return R2Config{}, fmt.Errorf("S3 API 必须使用 Cloudflare R2 域名")
	}
	pathBucket := strings.Trim(parsed.Path, "/")
	if strings.Contains(pathBucket, "/") {
		return R2Config{}, fmt.Errorf("S3 API 路径格式不正确")
	}
	if pathBucket != "" {
		if in.BucketName == "" {
			in.BucketName = pathBucket
		} else if in.BucketName != pathBucket {
			return R2Config{}, fmt.Errorf("S3 API 中的 Bucket 与 Bucket 名称不一致")
		}
		parsed.Path = ""
		in.Endpoint = parsed.String()
	}
	if in.BucketName == "" {
		return R2Config{}, fmt.Errorf("请填写 Bucket 名称")
	}
	publicURL, err := url.Parse(in.PublicBaseURL)
	if err != nil || publicURL.Scheme == "" || publicURL.Host == "" {
		return R2Config{}, fmt.Errorf("公开访问地址必须是完整的 HTTPS 地址")
	}
	if publicURL.Scheme != "https" {
		return R2Config{}, fmt.Errorf("公开访问地址必须使用 HTTPS")
	}
	if publicURL.RawQuery != "" || publicURL.Fragment != "" {
		return R2Config{}, fmt.Errorf("公开访问地址不能包含查询参数或片段")
	}
	if in.AccessKeyID == "" || in.SecretAccessKey == "" {
		return R2Config{}, fmt.Errorf("请填写 Access Key ID 和 Secret Access Key")
	}
	return in, nil
}

// Configure atomically replaces the active storage settings. In-flight calls
// finish using the old settings; subsequent calls immediately use the new ones.
func (c *Client) Configure(in R2Config) error {
	if c == nil {
		return fmt.Errorf("R2 client is nil")
	}
	// An entirely empty config is valid during first-run setup.
	if strings.TrimSpace(in.Endpoint) == "" && strings.TrimSpace(in.BucketName) == "" &&
		strings.TrimSpace(in.PublicBaseURL) == "" && strings.TrimSpace(in.AccessKeyID) == "" &&
		strings.TrimSpace(in.SecretAccessKey) == "" {
		c.mu.Lock()
		c.endpoint, c.host, c.region, c.bucket, c.publicBaseURL, c.ak, c.sk = "", "", "auto", "", "", "", ""
		c.mu.Unlock()
		return nil
	}
	normalized, err := NormalizeR2Config(in)
	if err != nil {
		return err
	}
	parsed, _ := url.Parse(normalized.Endpoint)
	c.mu.Lock()
	c.endpoint = normalized.Endpoint
	c.host = parsed.Host
	c.region = normalized.Region
	c.bucket = normalized.BucketName
	c.publicBaseURL = normalized.PublicBaseURL
	c.ak = normalized.AccessKeyID
	c.sk = normalized.SecretAccessKey
	c.mu.Unlock()
	return nil
}

// Configured reports whether the client has the minimum config to be usable.
func (c *Client) Configured() bool {
	if c == nil {
		return false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.endpoint != "" && c.bucket != "" && c.publicBaseURL != "" && c.ak != "" && c.sk != ""
}

func (c *Client) PublicBaseURL() string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.publicBaseURL
}

// PublicURL returns the browser-readable URL for an object in the public bucket.
// The authenticated S3 endpoint is deliberately never exposed to browsers.
func (c *Client) PublicURL(key string) string {
	if c == nil {
		return ""
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.publicBaseURL == "" {
		return ""
	}
	return c.publicBaseURL + "/" + uriEncode(strings.TrimPrefix(key, "/"), true)
}

// KeyFromPublicURL converts a URL previously returned by PublicURL back into an
// object key. This is used when replacing/deleting persisted branding URLs.
func (c *Client) KeyFromPublicURL(raw string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.publicBaseURL == "" {
		return "", false
	}
	prefix := c.publicBaseURL + "/"
	if !strings.HasPrefix(strings.TrimSpace(raw), prefix) {
		return "", false
	}
	key, err := url.PathUnescape(strings.TrimPrefix(strings.TrimSpace(raw), prefix))
	if err != nil {
		return "", false
	}
	if key == "" || strings.Contains(key, "..") {
		return "", false
	}
	return key, true
}

// Put uploads body under key with the given content type.
func (c *Client) Put(ctx context.Context, key string, body []byte, contentType string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	resp, err := c.do(ctx, http.MethodPut, c.bucket+"/"+key, nil, body, contentType, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return c.statusErr("put", key, resp)
	}
	return nil
}

// Get fetches key. The caller owns resp.Body (must Close it) and streams it. A
// non-empty rangeHeader is forwarded verbatim (for video seeking). Returns the
// raw *http.Response so headers/status can be passed through by the proxy.
func (c *Client) Get(ctx context.Context, key, rangeHeader string) (*http.Response, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	extra := map[string]string{}
	if strings.TrimSpace(rangeHeader) != "" {
		extra["Range"] = rangeHeader
	}
	return c.do(ctx, http.MethodGet, c.bucket+"/"+key, nil, nil, "", extra)
}

// Delete removes key. A missing object is not an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	resp, err := c.do(ctx, http.MethodDelete, c.bucket+"/"+key, nil, nil, "", nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 && resp.StatusCode != http.StatusNotFound {
		return c.statusErr("delete", key, resp)
	}
	return nil
}

// List returns every object whose key starts with prefix (paginated internally).
func (c *Client) List(ctx context.Context, prefix string) ([]Object, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var out []Object
	token := ""
	for {
		q := map[string]string{"list-type": "2", "max-keys": "1000"}
		if prefix != "" {
			q["prefix"] = prefix
		}
		if token != "" {
			q["continuation-token"] = token
		}
		resp, err := c.do(ctx, http.MethodGet, c.bucket, q, nil, "", nil)
		if err != nil {
			return nil, err
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("r2 list: status %d: %s", resp.StatusCode, truncate(data))
		}
		var parsed struct {
			Contents []struct {
				Key          string    `xml:"Key"`
				Size         int64     `xml:"Size"`
				LastModified time.Time `xml:"LastModified"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}
		if err := xml.Unmarshal(data, &parsed); err != nil {
			return nil, fmt.Errorf("r2 list: parse: %w", err)
		}
		for _, it := range parsed.Contents {
			out = append(out, Object{Key: it.Key, Size: it.Size, LastModified: it.LastModified})
		}
		if !parsed.IsTruncated || parsed.NextContinuationToken == "" {
			break
		}
		token = parsed.NextContinuationToken
	}
	return out, nil
}

func (c *Client) statusErr(op, key string, resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return fmt.Errorf("r2 %s %q: status %d: %s", op, key, resp.StatusCode, truncate(data))
}

func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

// do builds, signs (SigV4) and sends a request. resourcePath is the path after
// the host WITHOUT a leading slash (e.g. "bucket/dir/file.png" or "bucket").
func (c *Client) do(ctx context.Context, method, resourcePath string, query map[string]string, body []byte, contentType string, extraHeaders map[string]string) (*http.Response, error) {
	if c.endpoint == "" || c.bucket == "" || c.ak == "" || c.sk == "" {
		return nil, fmt.Errorf("R2 尚未配置")
	}
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	canonicalURI := "/" + uriEncode(resourcePath, true)
	canonicalQuery := canonicalQueryString(query)

	payloadHash := hexSHA256(body)
	// Signed headers: always host + x-amz-content-sha256 + x-amz-date, plus
	// content-type on PUT. Range etc. are sent unsigned.
	signed := map[string]string{
		"host":                 c.host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           amzDate,
	}
	if strings.TrimSpace(contentType) != "" {
		signed["content-type"] = contentType
	}
	names := sortedKeys(signed)
	var canonHeaders strings.Builder
	for _, k := range names {
		canonHeaders.WriteString(k + ":" + signed[k] + "\n")
	}
	signedHeaders := strings.Join(names, ";")

	canonicalRequest := strings.Join([]string{
		method, canonicalURI, canonicalQuery, canonHeaders.String(), signedHeaders, payloadHash,
	}, "\n")

	scope := dateStamp + "/" + c.region + "/" + service + "/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hexSHA256([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(signingKey(c.sk, dateStamp, c.region), []byte(stringToSign)))
	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.ak, scope, signedHeaders, signature)

	url := c.endpoint + canonicalURI
	if canonicalQuery != "" {
		url += "?" + canonicalQuery
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr)
	if err != nil {
		return nil, err
	}
	req.Host = c.host
	req.Header.Set("Authorization", auth)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	if ct := strings.TrimSpace(contentType); ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	return c.http.Do(req)
}

// ---- SigV4 helpers ----

func hexSHA256(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func hmacSHA256(key, msg []byte) []byte {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return h.Sum(nil)
}

func signingKey(secret, dateStamp, region string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// simple insertion sort (small n)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

func canonicalQueryString(q map[string]string) string {
	if len(q) == 0 {
		return ""
	}
	keys := sortedKeys(q)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, uriEncode(k, false)+"="+uriEncode(q[k], false))
	}
	return strings.Join(parts, "&")
}

// uriEncode applies AWS's URI encoding rules. When keepSlash is true, '/' is left
// as-is (for object key paths); otherwise it's percent-encoded (for query parts).
func uriEncode(s string, keepSlash bool) string {
	var b strings.Builder
	for _, r := range []byte(s) {
		switch {
		case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'),
			r == '-', r == '_', r == '.', r == '~':
			b.WriteByte(r)
		case r == '/' && keepSlash:
			b.WriteByte('/')
		default:
			b.WriteString(fmt.Sprintf("%%%02X", r))
		}
	}
	return b.String()
}
