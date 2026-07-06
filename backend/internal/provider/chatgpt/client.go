package chatgpt

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	stdhttp "net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

var (
	ErrAuth              = errors.New("chatgpt auth failed")
	ErrQuotaExhausted    = errors.New("chatgpt quota exhausted")
	ErrTemporaryUpstream = errors.New("chatgpt upstream temporary error")
	// ErrContentPolicy marks a prompt rejected by ChatGPT's content audit. It is
	// terminal and NOT retryable: the same prompt fails on every account, so the
	// caller must fail fast rather than poll or fail over.
	ErrContentPolicy = errors.New("chatgpt content policy rejection")
)

type Client struct {
	proxy     string
	deviceID  string
	sessionID string
}

type fileEntry struct {
	FileID    string
	UploadURL string
}

type uploadedReference struct {
	FileID        string
	LibraryFileID string
	FileName      string
	MimeType      string
	SizeBytes     int
	Width         int
	Height        int
}

func NewClient(proxy string) *Client {
	return &Client{
		proxy:     strings.TrimSpace(proxy),
		deviceID:  newUUID(),
		sessionID: newUUID(),
	}
}

func (c *Client) SetProxy(proxy string) {
	c.proxy = strings.TrimSpace(proxy)
}

func (c *Client) GenerateImage(ctx context.Context, accessToken, prompt, model, aspectRatio, resolution string, refs [][]byte) ([]byte, map[string]any, error) {
	session, err := c.newSession(accessToken)
	if err != nil {
		return nil, nil, err
	}

	// Fail over immediately when the account's image_gen allowance is spent:
	// submitting anyway just burns the whole poll budget and surfaces as
	// "image poll timeout". Unknown quota (init failed) proceeds as before.
	if quota, qErr := c.fetchImageQuota(ctx, session, accessToken); qErr == nil && quota["unknown"] == false {
		if remaining, ok := quota["remaining"].(int); ok && remaining <= 0 {
			return nil, nil, fmt.Errorf("%w: image_gen remaining 0 (resets %s)", ErrQuotaExhausted, stringValue(quota["reset_after"]))
		}
	}

	scriptSources, dataBuild, err := c.bootstrap(ctx, session)
	if err != nil {
		return nil, nil, err
	}
	reqs, err := c.getChatRequirements(ctx, session, accessToken, scriptSources, dataBuild)
	if err != nil {
		return nil, nil, err
	}
	effectivePrompt := injectSizeHint(prompt, aspectRatio, resolution)
	uploadedRefs, err := c.uploadReferenceImages(ctx, session, accessToken, refs)
	if err != nil {
		return nil, nil, err
	}
	conduitToken, err := c.prepareImageConversation(ctx, session, accessToken, effectivePrompt, reqs, model, uploadedRefs)
	if err != nil {
		return nil, nil, err
	}
	conversationID, fileIDs, sedimentIDs, err := c.startImageGeneration(ctx, session, accessToken, effectivePrompt, reqs, conduitToken, model, uploadedRefs)
	if err != nil {
		return nil, nil, err
	}
	session, err = c.newSession(accessToken)
	if err != nil {
		return nil, nil, err
	}
	fileIDs, sedimentIDs, err = c.pollForImage(ctx, session, accessToken, conversationID, fileIDs, sedimentIDs, pollBudget(ctx))
	if err != nil {
		return nil, nil, err
	}
	urls, err := c.resolveImageURLs(ctx, session, accessToken, conversationID, fileIDs, sedimentIDs)
	if err != nil {
		return nil, nil, err
	}
	if len(urls) == 0 {
		return nil, nil, errors.New("no image urls resolved")
	}
	images, err := c.downloadBytes(ctx, session, accessToken, urls)
	if err != nil {
		return nil, nil, err
	}
	if len(images) == 0 {
		return nil, nil, errors.New("download produced no bytes")
	}
	return images[0], map[string]any{
		"provider":        "chatgpt",
		"model":           model,
		"conversation_id": conversationID,
	}, nil
}

func ExtractAccountInfo(token string) map[string]any {
	claims := decodeJWTPayload(token)
	profile, _ := claims["https://api.openai.com/profile"].(map[string]any)
	auth, _ := claims["https://api.openai.com/auth"].(map[string]any)
	return map[string]any{
		"email":          emptyStringNil(strings.TrimSpace(stringValue(profile["email"]))),
		"email_verified": profile["email_verified"] == true,
		"plan_type":      emptyStringNil(strings.TrimSpace(stringValue(auth["chatgpt_plan_type"]))),
		"user_id":        emptyStringNil(strings.TrimSpace(stringValue(auth["chatgpt_user_id"]))),
		"issued_at":      claims["iat"],
		"expires_at":     claims["exp"],
	}
}

func (c *Client) FetchImageQuota(ctx context.Context, accessToken string) (map[string]any, error) {
	session, err := c.newSession(accessToken)
	if err != nil {
		return nil, err
	}
	return c.fetchImageQuota(ctx, session, accessToken)
}

func (c *Client) fetchImageQuota(ctx context.Context, session tlsclient.HttpClient, accessToken string) (map[string]any, error) {
	path := "/backend-api/conversation/init"
	body, _ := json.Marshal(map[string]any{
		"gizmo_id":                nil,
		"requested_default_model": nil,
		"conversation_id":         nil,
	})
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header = c.headers(accessToken, path, map[string]string{
		"accept":       "application/json",
		"content-type": "application/json",
	})
	resp, err := session.Do(req)
	if err != nil {
		return map[string]any{"remaining": nil, "reset_after": nil, "unknown": true, "error": "network: " + err.Error()}, nil
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == 401 {
		return map[string]any{"remaining": nil, "reset_after": nil, "unknown": true, "error": "token invalid", "auth_failed": true}, nil
	}
	if resp.StatusCode != 200 {
		return map[string]any{"remaining": nil, "reset_after": nil, "unknown": true, "error": fmt.Sprintf("http %d: %s", resp.StatusCode, clip(respBody, 160))}, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(respBody, &payload); err != nil {
		return map[string]any{"remaining": nil, "reset_after": nil, "unknown": true, "error": "non-json response"}, nil
	}
	limits, _ := payload["limits_progress"].([]any)
	for _, raw := range limits {
		item, _ := raw.(map[string]any)
		if strings.TrimSpace(stringValue(item["feature_name"])) != "image_gen" {
			continue
		}
		return map[string]any{
			"remaining":   intOrNil(item["remaining"]),
			"reset_after": emptyStringNil(strings.TrimSpace(stringValue(item["reset_after"]))),
			"unknown":     false,
			"error":       nil,
		}, nil
	}
	return map[string]any{"remaining": nil, "reset_after": nil, "unknown": true, "error": nil}, nil
}

type chatRequirements struct {
	Token          string
	ProofToken     string
	TurnstileToken string
}

func (c *Client) newSession(accessToken string) (tlsclient.HttpClient, error) {
	options := []tlsclient.HttpClientOption{
		tlsclient.WithTimeoutSeconds(600),
		// Match the Python reference (curl_cffi impersonate="chrome110"): the
		// Chrome_133 JA3/JA4 was tripping Cloudflare on the bootstrap GET (403).
		tlsclient.WithClientProfile(profiles.Chrome_110),
		tlsclient.WithRandomTLSExtensionOrder(),
	}
	if c.proxy != "" {
		options = append(options, tlsclient.WithProxyUrl(c.proxy))
	}
	client, err := tlsclient.NewHttpClient(tlsclient.NewNoopLogger(), options...)
	if err != nil {
		return nil, err
	}
	client.SetCookies(&url.URL{Scheme: "https", Host: "chatgpt.com"}, nil)
	return client, nil
}

func (c *Client) baseHeaders(accessToken string) http.Header {
	return http.Header{
		"accept-language":             {"zh-CN,zh;q=0.9,en;q=0.8,en-GB;q=0.7,en-US;q=0.6"},
		"oai-client-build-number":     {defaultClientBuildNumber},
		"oai-client-version":          {defaultClientVersion},
		"oai-device-id":               {c.deviceID},
		"oai-language":                {"zh-CN"},
		"oai-session-id":              {c.sessionID},
		"origin":                      {baseURL},
		"priority":                    {"u=1, i"},
		"referer":                     {baseURL + "/"},
		"sec-ch-ua":                   {`"Microsoft Edge";v="149", "Chromium";v="149", "Not)A;Brand";v="24"`},
		"sec-ch-ua-arch":              {`"x86"`},
		"sec-ch-ua-bitness":           {`"64"`},
		"sec-ch-ua-full-version":      {`"149.0.4022.69"`},
		"sec-ch-ua-full-version-list": {`"Microsoft Edge";v="149.0.4022.69", "Chromium";v="149.0.7827.115", "Not)A;Brand";v="24.0.0.0"`},
		"sec-ch-ua-mobile":            {"?0"},
		"sec-ch-ua-model":             {`""`},
		"sec-ch-ua-platform":          {`"Windows"`},
		"sec-ch-ua-platform-version":  {`"19.0.0"`},
		"sec-fetch-dest":              {"empty"},
		"sec-fetch-mode":              {"cors"},
		"sec-fetch-site":              {"same-origin"},
		"user-agent":                  {defaultUserAgent},
		"authorization":               {"Bearer " + strings.TrimSpace(accessToken)},
	}
}

func (c *Client) headers(accessToken, path string, extra map[string]string) http.Header {
	h := http.Header{
		"x-openai-target-path":  {path},
		"x-openai-target-route": {path},
	}
	for k, values := range c.baseHeaders(accessToken) {
		h[k] = append([]string{}, values...)
	}
	for k, v := range extra {
		h.Set(k, v)
	}
	return h
}

func (c *Client) bootstrap(ctx context.Context, session tlsclient.HttpClient) ([]string, string, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+"/", nil)
	if err != nil {
		return nil, "", err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"user-agent":                {defaultUserAgent},
		"accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
		"accept-language":           {"zh-CN,zh;q=0.9,en;q=0.8"},
		"sec-ch-ua":                 {`"Microsoft Edge";v="143", "Chromium";v="143", "Not A(Brand";v="24"`},
		"sec-ch-ua-mobile":          {"?0"},
		"sec-ch-ua-platform":        {`"Windows"`},
		"sec-fetch-dest":            {"document"},
		"sec-fetch-mode":            {"navigate"},
		"sec-fetch-site":            {"none"},
		"upgrade-insecure-requests": {"1"},
	}
	resp, err := session.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	if err := ensureOK(resp.StatusCode, body, "bootstrap"); err != nil {
		return nil, "", err
	}
	sources, dataBuild := parsePOWResources(string(body))
	return sources, dataBuild, nil
}

func (c *Client) getChatRequirements(ctx context.Context, session tlsclient.HttpClient, accessToken string, scriptSources []string, dataBuild string) (*chatRequirements, error) {
	pToken := buildLegacyRequirementsToken(defaultUserAgent, scriptSources, dataBuild)
	path := "/backend-api/sentinel/chat-requirements/prepare"
	reqBody, _ := json.Marshal(map[string]any{"p": pToken})
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header = c.headers(accessToken, path, map[string]string{
		"content-type": "application/json",
	})
	resp, err := session.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if err := ensureOK(resp.StatusCode, body, "chat_requirements_prepare"); err != nil {
		return nil, err
	}
	var prepare map[string]any
	if err := json.Unmarshal(body, &prepare); err != nil {
		return nil, err
	}
	if arkose, _ := prepare["arkose"].(map[string]any); arkose["required"] == true {
		return nil, errors.New("chat-requirements requires arkose token")
	}
	proofToken := ""
	if powInfo, _ := prepare["proofofwork"].(map[string]any); powInfo["required"] == true {
		proofToken, err = buildProofToken(strings.TrimSpace(stringValue(powInfo["seed"])), strings.TrimSpace(stringValue(powInfo["difficulty"])), defaultUserAgent, scriptSources, dataBuild)
		if err != nil {
			return nil, err
		}
	}
	turnstileToken := ""
	if tsInfo, _ := prepare["turnstile"].(map[string]any); tsInfo["required"] == true {
		turnstileToken = solveTurnstileToken(strings.TrimSpace(stringValue(tsInfo["dx"])), pToken)
	}

	path = "/backend-api/sentinel/chat-requirements/finalize"
	finalizeBody, _ := json.Marshal(map[string]any{
		"prepare_token":   stringValue(prepare["prepare_token"]),
		"proof_token":     proofToken,
		"turnstile_token": turnstileToken,
	})
	req, err = http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(finalizeBody))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header = c.headers(accessToken, path, map[string]string{
		"content-type": "application/json",
	})
	resp, err = session.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	body, err = io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if err := ensureOK(resp.StatusCode, body, "chat_requirements_finalize"); err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, err
	}
	token := strings.TrimSpace(stringValue(data["token"]))
	if token == "" {
		return nil, errors.New("chat-requirements missing token")
	}
	return &chatRequirements{Token: token, ProofToken: proofToken, TurnstileToken: turnstileToken}, nil
}

func (c *Client) imageHeaders(accessToken, path string, reqs *chatRequirements, conduitToken, accept string) http.Header {
	h := c.headers(accessToken, path, map[string]string{
		"content-type": "application/json",
		"accept":       accept,
		"openai-sentinel-chat-requirements-token": reqs.Token,
	})
	if reqs.ProofToken != "" {
		h.Set("openai-sentinel-proof-token", reqs.ProofToken)
	}
	// The real browser also sends the turnstile token on the conversation call
	// (HAR confirms openai-sentinel-turnstile-token). We already compute it in
	// chat-requirements/finalize but were dropping it here — send it so the
	// request matches the browser and isn't extra-challenged by sentinel.
	if reqs.TurnstileToken != "" {
		h.Set("openai-sentinel-turnstile-token", reqs.TurnstileToken)
	}
	if conduitToken != "" {
		h.Set("x-conduit-token", conduitToken)
	}
	if accept == "text/event-stream" {
		h.Set("x-oai-turn-trace-id", newUUID())
		h.Set("oai-telemetry", "[1,null]")
	}
	return h
}

func (c *Client) uploadReferenceImages(ctx context.Context, session tlsclient.HttpClient, accessToken string, refs [][]byte) ([]uploadedReference, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	out := make([]uploadedReference, 0, len(refs))
	for i, ref := range refs {
		meta, err := inspectReferenceImage(ref, i)
		if err != nil {
			return nil, err
		}
		entry, err := c.createFileEntry(ctx, session, accessToken, meta)
		if err != nil {
			return nil, err
		}
		if err := c.uploadRawFile(ctx, session, entry.UploadURL, meta.MimeType, ref); err != nil {
			return nil, err
		}
		libraryFileID, err := c.processUploadStream(ctx, session, accessToken, entry.FileID, meta.FileName)
		if err != nil {
			return nil, err
		}
		meta.FileID = entry.FileID
		meta.LibraryFileID = libraryFileID
		out = append(out, meta)
	}
	return out, nil
}

func imageModelSlug(model string) string {
	if strings.EqualFold(strings.TrimSpace(model), "gpt-image-2") {
		return "gpt-5-3"
	}
	return "auto"
}

func inspectReferenceImage(data []byte, index int) (uploadedReference, error) {
	if len(data) == 0 {
		return uploadedReference{}, errors.New("empty reference image")
	}
	mimeType := normalizeImageMime(stdhttp.DetectContentType(data))
	if mimeType == "" {
		return uploadedReference{}, errors.New("unsupported reference image type")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return uploadedReference{}, errors.New("failed to decode reference image")
	}
	ext := extensionForMime(mimeType)
	fileName := fmt.Sprintf("reference_%s_%02d%s", time.Now().UTC().Format("20060102_150405"), index+1, ext)
	return uploadedReference{
		FileName:  fileName,
		MimeType:  mimeType,
		SizeBytes: len(data),
		Width:     cfg.Width,
		Height:    cfg.Height,
	}, nil
}

func normalizeImageMime(v string) string {
	v = strings.ToLower(strings.TrimSpace(strings.Split(v, ";")[0]))
	switch v {
	case "image/jpeg", "image/jpg":
		return "image/jpeg"
	case "image/png":
		return "image/png"
	case "image/gif":
		return "image/gif"
	default:
		return ""
	}
}

func extensionForMime(mimeType string) string {
	switch strings.ToLower(strings.TrimSpace(mimeType)) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	default:
		return filepath.Ext(mimeType)
	}
}

func (c *Client) createFileEntry(ctx context.Context, session tlsclient.HttpClient, accessToken string, meta uploadedReference) (*fileEntry, error) {
	path := "/backend-api/files"
	payload := map[string]any{
		"file_name":                 meta.FileName,
		"file_size":                 meta.SizeBytes,
		"use_case":                  "multimodal",
		"timezone_offset_min":       -480,
		"reset_rate_limits":         false,
		"mime_type":                 meta.MimeType,
		"entry_surface":             "chat_composer",
		"selection_method":          "file_picker",
		"client_resolved_mime_type": meta.MimeType,
		"mime_resolution_source":    "filename_extension",
		"store_in_library":          true,
		"library_persistence_mode":  "opportunistic",
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header = c.headers(accessToken, path, map[string]string{
		"accept":       "application/json",
		"content-type": "application/json",
	})
	resp, err := session.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if err := ensureOK(resp.StatusCode, respBody, "file_create"); err != nil {
		return nil, err
	}
	var data map[string]any
	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, err
	}
	entry := &fileEntry{
		FileID:    strings.TrimSpace(stringValue(data["file_id"])),
		UploadURL: strings.TrimSpace(stringValue(data["upload_url"])),
	}
	if entry.FileID == "" || entry.UploadURL == "" {
		return nil, errors.New("file_create missing upload payload")
	}
	return entry, nil
}

func (c *Client) uploadRawFile(ctx context.Context, session tlsclient.HttpClient, uploadURL, mimeType string, data []byte) error {
	req, err := http.NewRequest(http.MethodPut, uploadURL, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"accept":             {"application/json, text/plain, */*"},
		"accept-language":    {"zh-CN,zh;q=0.9,en;q=0.8"},
		"content-type":       {mimeType},
		"origin":             {baseURL},
		"referer":            {baseURL + "/"},
		"sec-ch-ua":          {`"Microsoft Edge";v="143", "Chromium";v="143", "Not A(Brand";v="24"`},
		"sec-ch-ua-mobile":   {"?0"},
		"sec-ch-ua-platform": {`"Windows"`},
		"sec-fetch-dest":     {"empty"},
		"sec-fetch-mode":     {"cors"},
		"sec-fetch-site":     {"cross-site"},
		"user-agent":         {defaultUserAgent},
		"x-ms-blob-type":     {"BlockBlob"},
		"x-ms-version":       {"2020-04-08"},
	}
	resp, err := session.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	body, readErr := io.ReadAll(resp.Body)
	resp.Body.Close()
	if readErr != nil {
		return readErr
	}
	if err := ensureOK(resp.StatusCode, body, "file_upload"); err != nil {
		return err
	}
	return nil
}

func (c *Client) processUploadStream(ctx context.Context, session tlsclient.HttpClient, accessToken, fileID, fileName string) (string, error) {
	path := "/backend-api/files/process_upload_stream"
	payload := map[string]any{
		"file_id":                  fileID,
		"use_case":                 "multimodal",
		"index_for_retrieval":      false,
		"file_name":                fileName,
		"library_persistence_mode": "opportunistic",
		"entry_surface":            "chat_composer",
		"metadata": map[string]any{
			"store_in_library":           true,
			"is_temporary_chat":          false,
			"library_eligibility_reason": "eligible",
			"is_project_thread":          false,
		},
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req = req.WithContext(ctx)
	req.Header = c.headers(accessToken, path, map[string]string{
		"accept":       "text/event-stream",
		"content-type": "application/json",
	})
	resp, err := session.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err := ensureOK(resp.StatusCode, respBody, "file_process_upload"); err != nil {
			return "", err
		}
	}
	defer resp.Body.Close()
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1024*32), 1024*1024)
	libraryFileID := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item map[string]any
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			continue
		}
		if extra, _ := item["extra"].(map[string]any); extra != nil {
			if v := strings.TrimSpace(stringValue(extra["metadata_object_id"])); v != "" {
				libraryFileID = v
			}
		}
		if strings.TrimSpace(stringValue(item["event"])) == "file.processing.completed" {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return libraryFileID, nil
}

func attachmentMimeTypes(refs []uploadedReference) []string {
	if len(refs) == 0 {
		return nil
	}
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.MimeType != "" && !containsString(out, ref.MimeType) {
			out = append(out, ref.MimeType)
		}
	}
	return out
}

func buildAttachmentMetadata(refs []uploadedReference) []map[string]any {
	out := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		out = append(out, map[string]any{
			"id":              ref.FileID,
			"size":            ref.SizeBytes,
			"name":            ref.FileName,
			"mime_type":       ref.MimeType,
			"width":           ref.Width,
			"height":          ref.Height,
			"source":          "local",
			"library_file_id": emptyStringNil(ref.LibraryFileID),
			"is_big_paste":    false,
		})
	}
	return out
}

func buildMultimodalParts(refs []uploadedReference, prompt string) []any {
	out := make([]any, 0, len(refs)+1)
	for _, ref := range refs {
		out = append(out, map[string]any{
			"content_type":  "image_asset_pointer",
			"asset_pointer": "sediment://" + ref.FileID,
			"size_bytes":    ref.SizeBytes,
			"width":         ref.Width,
			"height":        ref.Height,
		})
	}
	out = append(out, prompt)
	return out
}

func (c *Client) prepareImageConversation(ctx context.Context, session tlsclient.HttpClient, accessToken, prompt string, reqs *chatRequirements, model string, refs []uploadedReference) (string, error) {
	path := "/backend-api/f/conversation/prepare"
	payload := map[string]any{
		"action":                 "next",
		"parent_message_id":      "client-created-root",
		"model":                  imageModelSlug(model),
		"timezone_offset_min":    -480,
		"timezone":               "Asia/Shanghai",
		"conversation_mode":      map[string]any{"kind": "primary_assistant"},
		"system_hints":           []string{"picture_v2"},
		"supports_buffering":     true,
		"supported_encodings":    []string{"v1"},
		"client_contextual_info": map[string]any{"app_name": "chatgpt.com"},
	}
	if len(refs) > 0 {
		payload["client_prepare_state"] = "none"
		payload["attachment_mime_types"] = attachmentMimeTypes(refs)
	} else {
		payload["client_prepare_state"] = "success"
		payload["partial_query"] = map[string]any{
			"id":      newUUID(),
			"author":  map[string]any{"role": "user"},
			"content": map[string]any{"content_type": "text", "parts": []string{prompt}},
		}
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req = req.WithContext(ctx)
	req.Header = c.imageHeaders(accessToken, path, reqs, "no-token", "application/json")
	resp, err := session.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return "", err
	}
	if err := ensureOK(resp.StatusCode, respBody, "image_prepare"); err != nil {
		return "", err
	}
	var data map[string]any
	if err := json.Unmarshal(respBody, &data); err != nil {
		return "", err
	}
	return strings.TrimSpace(stringValue(data["conduit_token"])), nil
}

func (c *Client) startImageGeneration(ctx context.Context, session tlsclient.HttpClient, accessToken, prompt string, reqs *chatRequirements, conduitToken, model string, refs []uploadedReference) (string, []string, []string, error) {
	path := "/backend-api/f/conversation"
	content := map[string]any{"content_type": "text", "parts": []string{prompt}}
	metadata := map[string]any{
		"selected_github_repos":     []any{},
		"selected_all_github_repos": false,
		"system_hints":              []string{"picture_v2"},
		"serialization_metadata":    map[string]any{"custom_symbol_offsets": []any{}},
	}
	if len(refs) > 0 {
		content = map[string]any{
			"content_type": "multimodal_text",
			"parts":        buildMultimodalParts(refs, prompt),
		}
		metadata["attachments"] = buildAttachmentMetadata(refs)
	}
	payload := map[string]any{
		"action": "next",
		"messages": []map[string]any{{
			"id":          newUUID(),
			"author":      map[string]any{"role": "user"},
			"create_time": float64(time.Now().Unix()),
			"content":     content,
			"metadata":    metadata,
		}},
		"parent_message_id":                    "client-created-root",
		"model":                                imageModelSlug(model),
		"client_prepare_state":                 "success",
		"timezone_offset_min":                  -480,
		"timezone":                             "Asia/Shanghai",
		"conversation_mode":                    map[string]any{"kind": "primary_assistant"},
		"enable_message_followups":             true,
		"system_hints":                         []string{"picture_v2"},
		"supports_buffering":                   true,
		"supported_encodings":                  []string{"v1"},
		"client_contextual_info":               map[string]any{"is_dark_mode": false, "time_since_loaded": 1200, "page_height": 1072, "page_width": 1724, "pixel_ratio": 1.2, "screen_height": 1440, "screen_width": 2560, "app_name": "chatgpt.com"},
		"paragen_cot_summary_display_override": "allow",
		"force_parallel_switch":                "auto",
	}
	body, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, baseURL+path, bytes.NewReader(body))
	if err != nil {
		return "", nil, nil, err
	}
	req = req.WithContext(ctx)
	req.Header = c.imageHeaders(accessToken, path, reqs, conduitToken, "text/event-stream")
	resp, err := session.Do(req)
	if err != nil {
		return "", nil, nil, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err := ensureOK(resp.StatusCode, respBody, "image_start"); err != nil {
			return "", nil, nil, err
		}
	}
	defer resp.Body.Close()

	conversationID := ""
	asyncStarted := false
	var fileIDs, sedimentIDs []string
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 1024*1024), 8*1024*1024)
	var chunks []string
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[5:])
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			break
		}
		chunks = append(chunks, payload)
		if conversationID == "" {
			if match := conversationIDRE.FindStringSubmatch(payload); len(match) >= 2 {
				conversationID = match[1]
			}
		}
		newFiles, newSeds := scanForIDs(payload)
		fileIDs = mergeStrings(fileIDs, newFiles)
		sedimentIDs = mergeStrings(sedimentIDs, newSeds)
		if !asyncStarted && containsAsyncMarker(payload) {
			asyncStarted = true
		}
		// Async pipeline: ChatGPT no longer streams the image inline — it returns
		// a placeholder tool turn (image_gen_async / image_gen_task_id) and
		// delivers the asset later via conversation polling. Once we have the
		// conversation id there is nothing more to read here, so stop instead of
		// holding the SSE open until [DONE] (a stalled stream would otherwise burn
		// the whole generation budget and surface as "context deadline exceeded").
		if asyncStarted && conversationID != "" {
			break
		}
	}
	if conversationID == "" {
		joined := strings.Join(chunks, "\n")
		if match := conversationIDRE.FindStringSubmatch(joined); len(match) >= 2 {
			conversationID = match[1]
		}
	}
	if conversationID == "" {
		return "", nil, nil, errors.New("chatgpt SSE closed without conversation_id")
	}
	return conversationID, fileIDs, sedimentIDs, nil
}

func (c *Client) getConversation(ctx context.Context, session tlsclient.HttpClient, accessToken, conversationID string) (map[string]any, error) {
	path := "/backend-api/conversation/" + conversationID
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req = req.WithContext(ctx)
	req.Header = c.headers(accessToken, path, map[string]string{"accept": "application/json"})
	resp, err := session.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}
	if err := ensureOK(resp.StatusCode, body, "conversation_get"); err != nil {
		return nil, err
	}
	if dir := os.Getenv("CHATGPT_DEBUG_DUMP"); dir != "" {
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("conv-%s-%d.json", conversationID, time.Now().UnixMilli())), body, 0o644)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, err
	}
	return payload, nil
}

// pollBudget derives how long to poll for the async image from the caller's
// remaining context budget, leaving headroom to resolve+download the asset
// before the outer deadline (genCtx, 8min) fires. Async image generation under
// load routinely exceeds the old hard-coded 180s, which surfaced as
// "image poll timeout"; tying the budget to the deadline lets slow gens finish
// while the context still backstops a truly stuck request.
func pollBudget(ctx context.Context) time.Duration {
	const (
		maxBudget = 6 * time.Minute
		headroom  = 25 * time.Second
	)
	deadline, ok := ctx.Deadline()
	if !ok {
		return 3 * time.Minute
	}
	budget := time.Until(deadline) - headroom
	if budget < 0 {
		budget = 0
	}
	if budget > maxBudget {
		budget = maxBudget
	}
	return budget
}

func (c *Client) pollForImage(ctx context.Context, session tlsclient.HttpClient, accessToken, conversationID string, initialFileIDs, initialSedimentIDs []string, timeout time.Duration) ([]string, []string, error) {
	start := time.Now()
	fileIDs := append([]string{}, initialFileIDs...)
	sedimentIDs := append([]string{}, initialSedimentIDs...)
	if len(fileIDs) == 0 {
		time.Sleep(8 * time.Second)
	} else {
		time.Sleep(2 * time.Second)
	}
	attempt := 0
	for time.Since(start) < timeout {
		// Bail out immediately if the caller's context is already done — without
		// this, a cancelled request spins here re-issuing doomed upstream calls
		// (each fails instantly with "operation was canceled") until `timeout`.
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		attempt++
		conv, err := c.getConversation(ctx, session, accessToken, conversationID)
		if err != nil {
			if errors.Is(err, ErrTemporaryUpstream) {
				time.Sleep(time.Duration(minInt(1<<minInt(attempt, 4), 8)) * time.Second)
				continue
			}
			return nil, nil, err
		}
		newFiles, newSeds := extractImageIDs(conv)
		fileIDs = mergeStrings(fileIDs, newFiles)
		sedimentIDs = mergeStrings(sedimentIDs, newSeds)
		// Fail fast on a content-audit refusal: the assistant turn carries the
		// rejection text and no image will ever land, so polling to timeout only
		// wastes the whole budget. Only bail while we have no asset yet.
		if len(fileIDs) == 0 && len(sedimentIDs) == 0 && (conversationRejected(conv) || conversationEndedWithoutImage(conv)) {
			return nil, nil, ErrContentPolicy
		}
		if len(fileIDs) > 0 || len(sedimentIDs) > 0 {
			time.Sleep(2 * time.Second)
			conv, err = c.getConversation(ctx, session, accessToken, conversationID)
			if err == nil {
				finalFiles, finalSeds := extractImageIDs(conv)
				fileIDs = mergeStrings(fileIDs, finalFiles)
				sedimentIDs = mergeStrings(sedimentIDs, finalSeds)
			}
			return fileIDs, sedimentIDs, nil
		}
		time.Sleep(5 * time.Second)
	}
	return nil, nil, errors.New("image poll timeout")
}

func (c *Client) getFileDownloadURL(ctx context.Context, session tlsclient.HttpClient, accessToken, conversationID, fileID string, inline bool) (string, error) {
	// Current web client form: GET /backend-api/files/download/{id}
	// ?conversation_id=...&inline=false → {"status":"success","download_url":...}.
	// Falls back to the legacy /files/{id}/download form if the new one fails.
	paths := []string{
		"/backend-api/files/download/" + fileID + "?conversation_id=" + conversationID + "&inline=" + strconv.FormatBool(inline),
		"/backend-api/files/" + fileID + "/download",
	}
	var lastErr error
	for _, path := range paths {
		rawURL, err := c.fetchDownloadURL(ctx, session, accessToken, path)
		if err == nil && rawURL != "" {
			return rawURL, nil
		}
		if err != nil {
			lastErr = err
		}
	}
	return "", lastErr
}

func (c *Client) fetchDownloadURL(ctx context.Context, session tlsclient.HttpClient, accessToken, path string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
	if err != nil {
		return "", err
	}
	req = req.WithContext(ctx)
	req.Header = c.headers(accessToken, path, map[string]string{"accept": "application/json"})
	resp, err := session.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return "", err
	}
	if err := ensureOK(resp.StatusCode, body, "file_download_url"); err != nil {
		return "", err
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", err
	}
	rawURL := strings.TrimSpace(stringValue(payload["download_url"]))
	if rawURL == "" {
		rawURL = strings.TrimSpace(stringValue(payload["url"]))
	}
	return rawURL, nil
}

func (c *Client) resolveImageURLs(ctx context.Context, session tlsclient.HttpClient, accessToken, conversationID string, fileIDs, sedimentIDs []string) ([]string, error) {
	var urls []string
	for _, fileID := range fileIDs {
		if fileID == "file_upload" {
			continue
		}
		rawURL, err := c.getFileDownloadURL(ctx, session, accessToken, conversationID, fileID, false)
		if err != nil {
			continue
		}
		if rawURL != "" && !containsString(urls, rawURL) {
			urls = append(urls, rawURL)
		}
	}
	for _, sedimentID := range sedimentIDs {
		path := "/backend-api/conversation/" + conversationID + "/attachment/" + sedimentID + "/download"
		req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
		if err != nil {
			continue
		}
		req = req.WithContext(ctx)
		req.Header = c.headers(accessToken, path, map[string]string{"accept": "application/json"})
		resp, err := session.Do(req)
		if err != nil {
			continue
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil || resp.StatusCode < 200 || resp.StatusCode >= 300 {
			continue
		}
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			continue
		}
		rawURL := strings.TrimSpace(stringValue(payload["download_url"]))
		if rawURL == "" {
			rawURL = strings.TrimSpace(stringValue(payload["url"]))
		}
		if rawURL != "" && !containsString(urls, rawURL) {
			urls = append(urls, rawURL)
		}
	}
	return urls, nil
}

func (c *Client) downloadBytes(ctx context.Context, session tlsclient.HttpClient, accessToken string, urls []string) ([][]byte, error) {
	out := make([][]byte, 0, len(urls))
	for _, rawURL := range urls {
		req, err := http.NewRequest(http.MethodGet, rawURL, nil)
		if err != nil {
			return nil, err
		}
		req = req.WithContext(ctx)
		// Mirror Python's session.get(url): the resolved download_url is a
		// backend-api stream that requires the same default headers as every
		// other call — crucially Authorization. Without it the fetch 403s with
		// {"detail":"File stream access denied."}.
		req.Header = c.baseHeaders(accessToken)
		req.Header.Set("accept", "*/*")
		resp, err := session.Do(req)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTemporaryUpstream, err)
		}
		body, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if err := ensureOK(resp.StatusCode, body, "image_download"); err != nil {
			return nil, err
		}
		if len(body) > 0 {
			out = append(out, body)
		}
	}
	return out, nil
}

func ensureOK(statusCode int, body []byte, context string) error {
	if statusCode >= 200 && statusCode < 300 {
		return nil
	}
	switch statusCode {
	case 401, 403:
		return fmt.Errorf("%w: %s %d %s", ErrAuth, context, statusCode, clip(body, 400))
	case 429:
		return fmt.Errorf("%w: %s 429 %s", ErrQuotaExhausted, context, clip(body, 400))
	case 500, 502, 503, 504:
		return fmt.Errorf("%w: %s %d %s", ErrTemporaryUpstream, context, statusCode, clip(body, 400))
	default:
		return fmt.Errorf("%s: %d %s", context, statusCode, clip(body, 400))
	}
}

func injectSizeHint(prompt, aspectRatio, resolution string) string {
	_ = resolution
	ratio := strings.TrimSpace(aspectRatio)
	if ratio == "" || strings.EqualFold(ratio, "auto") {
		return strings.TrimSpace(prompt)
	}
	return strings.TrimSpace(prompt) + "\n\n将宽高比设为 " + ratio
}

func scanForIDs(text string) ([]string, []string) {
	var fileIDs []string
	for _, id := range fileServiceIDPattern.FindAllStringSubmatch(text, -1) {
		if len(id) >= 2 && id[1] != "file_upload" && !containsString(fileIDs, id[1]) {
			fileIDs = append(fileIDs, id[1])
		}
	}
	for _, id := range realImageIDPattern.FindAllString(text, -1) {
		if !containsString(fileIDs, id) {
			fileIDs = append(fileIDs, id)
		}
	}
	var sedimentIDs []string
	for _, id := range sedimentIDPattern.FindAllStringSubmatch(text, -1) {
		if len(id) >= 2 && !containsString(sedimentIDs, id[1]) {
			sedimentIDs = append(sedimentIDs, id[1])
		}
	}
	return fileIDs, sedimentIDs
}

func extractImageIDs(conversation map[string]any) ([]string, []string) {
	var fileIDs, sedimentIDs []string
	mapping, _ := conversation["mapping"].(map[string]any)
	for _, rawNode := range mapping {
		node, _ := rawNode.(map[string]any)
		message, _ := node["message"].(map[string]any)
		author, _ := message["author"].(map[string]any)
		role := strings.ToLower(strings.TrimSpace(stringValue(author["role"])))
		if role != "tool" && role != "assistant" {
			continue
		}
		walkForIDs(message["content"], &fileIDs, &sedimentIDs)
		walkForIDs(message["metadata"], &fileIDs, &sedimentIDs)
	}
	return fileIDs, sedimentIDs
}

func walkForIDs(value any, fileIDs, sedimentIDs *[]string) {
	switch x := value.(type) {
	case string:
		newFiles, newSeds := scanForIDs(x)
		*fileIDs = mergeStrings(*fileIDs, newFiles)
		*sedimentIDs = mergeStrings(*sedimentIDs, newSeds)
	case map[string]any:
		for _, item := range x {
			walkForIDs(item, fileIDs, sedimentIDs)
		}
	case []any:
		for _, item := range x {
			walkForIDs(item, fileIDs, sedimentIDs)
		}
	}
}

func mergeStrings(dst, src []string) []string {
	for _, item := range src {
		if !containsString(dst, item) {
			dst = append(dst, item)
		}
	}
	return dst
}

func containsString(items []string, value string) bool {
	for _, item := range items {
		if item == value {
			return true
		}
	}
	return false
}

func intOrNil(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case float32:
		return int(x)
	case json.Number:
		n, err := x.Int64()
		if err != nil {
			return nil
		}
		return int(n)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(x))
		if err != nil {
			return nil
		}
		return n
	default:
		return nil
	}
}

func emptyStringNil(v string) any {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return v
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
