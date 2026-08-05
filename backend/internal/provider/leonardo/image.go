package leonardo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"strings"
	"time"

	http "github.com/bogdanfinn/fhttp"
)

// defaultStyleID is the "Dynamic" style applied when the caller doesn't specify
// one — Leonardo's Generate mutation expects a style_ids entry.
const defaultStyleID = "111dc692-d470-4eec-b791-3475abac4c46"

const mGenerate = `mutation Generate($request: CreateGenerationRequest!) {
  generate(request: $request) {
    apiCreditCost
    generationId
    __typename
  }
}`

// qGenerationImages polls one generation's status AND its produced images in a
// single round-trip (where: id _in [genId]).
const qGenerationImages = `query GenerationImages($where: generations_bool_exp = {}) {
  generations(where: $where) {
    id
    status
    generated_images {
      id
      url
      __typename
    }
    __typename
  }
}`

const mUploadImage = `mutation UploadImage($uploadImageInput: UploadImageInput!) {
  uploadImage(arg1: $uploadImageInput) {
    uploadId
    url
    fields
    __typename
  }
}`

// uploadInitImage uploads a reference (init) image for image-to-image: it asks
// Leonardo for a presigned S3 POST, uploads the bytes, and returns the upload id
// to reference in the Generate request's image_reference guidance.
func (c *Client) uploadInitImage(ctx context.Context, accessToken string, img []byte) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"operationName": "UploadImage",
		"query":         mUploadImage,
		"variables":     map[string]any{"uploadImageInput": map[string]any{"uploadType": "INIT", "extension": "png"}},
	})
	body, status, err := c.graphqlP(ctx, accessToken, payload, false)
	if err != nil {
		return "", fmt.Errorf("%w: upload-init: %s", ErrTemporaryUpstream, err.Error())
	}
	if status == 401 || status == 403 {
		return "", ErrAuth
	}
	if status != 200 {
		return "", fmt.Errorf("%w: upload-init http %d: %s", ErrTemporaryUpstream, status, clip(body, 160))
	}
	if e := graphqlError(body); e != nil {
		return "", e
	}
	var ur struct {
		Data struct {
			UploadImage struct {
				UploadID string `json:"uploadId"`
				URL      string `json:"url"`
				Fields   string `json:"fields"`
			} `json:"uploadImage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &ur); err != nil {
		return "", fmt.Errorf("%w: upload-init non-json", ErrTemporaryUpstream)
	}
	up := ur.Data.UploadImage
	if up.UploadID == "" || up.URL == "" {
		return "", fmt.Errorf("%w: no upload url", ErrTemporaryUpstream)
	}
	var fields map[string]string
	if err := json.Unmarshal([]byte(up.Fields), &fields); err != nil {
		return "", fmt.Errorf("%w: bad upload fields", ErrTemporaryUpstream)
	}

	// Presigned S3 POST: all policy fields first, the file part LAST.
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	fw, err := w.CreateFormFile("file", "image.png")
	if err != nil {
		return "", err
	}
	if _, err := fw.Write(img); err != nil {
		return "", err
	}
	_ = w.Close()

	client, err := c.newDirectTLSClient()
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, up.URL, &buf)
	if err != nil {
		return "", err
	}
	req = req.WithContext(ctx)
	req.Header = http.Header{
		"content-type": {w.FormDataContentType()},
		"user-agent":   {userAgent},
		"origin":       {appBase},
		"referer":      {appBase + "/"},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: s3 upload: %s", ErrTemporaryUpstream, err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 && resp.StatusCode != 200 && resp.StatusCode != 201 {
		return "", fmt.Errorf("%w: s3 upload http %d", ErrTemporaryUpstream, resp.StatusCode)
	}
	return up.UploadID, nil
}

// GenerateImage runs the full Leonardo image pipeline against one account cookie:
// mint a JWT, (for image-to-image) upload each reference image, submit the
// Generate mutation, poll until COMPLETE, then download the first produced image.
// Returns the image bytes, an info map, and a classified error.
func (c *Client) GenerateImage(ctx context.Context, cookie, model, prompt string, width, height int, styleIDs []string, refImages [][]byte, downloadResult bool) ([]byte, map[string]any, error) {
	sess, err := c.GetSession(ctx, cookie)
	if err != nil {
		return nil, nil, err
	}
	if len(styleIDs) == 0 {
		styleIDs = []string{defaultStyleID}
	}
	if strings.TrimSpace(model) == "" {
		model = "seedream-4.5"
	}

	// Image-to-image: upload each reference and collect its guidance entry.
	var imageRefs []map[string]any
	for _, img := range refImages {
		if len(img) == 0 {
			continue
		}
		uploadID, upErr := c.uploadInitImage(ctx, sess.AccessToken, img)
		if upErr != nil {
			return nil, nil, upErr
		}
		imageRefs = append(imageRefs, map[string]any{
			"image":    map[string]any{"id": uploadID, "type": "UPLOADED"},
			"strength": "MID",
		})
	}

	promptEnhance := "AUTO"
	parameters := map[string]any{
		"height":         height,
		"width":          width,
		"prompt_enhance": promptEnhance,
		"quantity":       1,
		"style_ids":      styleIDs,
		"prompt":         prompt,
	}
	if len(imageRefs) > 0 {
		// Preserve the reference when image-guided (matches the web app).
		parameters["prompt_enhance"] = "OFF"
		parameters["guidances"] = map[string]any{"image_reference": imageRefs}
	}

	// 1. submit
	genReq := map[string]any{
		"operationName": "Generate",
		"query":         mGenerate,
		"variables": map[string]any{
			"request": map[string]any{
				"model":      model,
				"public":     true,
				"parameters": parameters,
			},
		},
	}
	payload, _ := json.Marshal(genReq)
	body, status, err := c.graphql(ctx, sess.AccessToken, payload)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %s", ErrTemporaryUpstream, err.Error())
	}
	if status == 401 || status == 403 {
		return nil, nil, ErrAuth
	}
	if status != 200 {
		return nil, nil, fmt.Errorf("%w: generate http %d: %s", ErrTemporaryUpstream, status, clip(body, 200))
	}
	if e := graphqlError(body); e != nil {
		return nil, nil, e
	}
	var genResp struct {
		Data struct {
			Generate struct {
				GenerationID string `json:"generationId"`
			} `json:"generate"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &genResp); err != nil {
		return nil, nil, fmt.Errorf("%w: generate non-json", ErrTemporaryUpstream)
	}
	genID := strings.TrimSpace(genResp.Data.Generate.GenerationID)
	if genID == "" {
		return nil, nil, fmt.Errorf("%w: no generationId: %s", ErrTemporaryUpstream, clip(body, 200))
	}

	// 2. poll until COMPLETE, then read the image url.
	imageURL, err := c.pollImage(ctx, sess.AccessToken, genID)
	if err != nil {
		return nil, nil, err
	}

	info := map[string]any{
		"generation_id": genID,
		"image_url":     imageURL,
		"user_id":       sess.UserID,
	}
	if !downloadResult {
		return nil, info, nil
	}
	// 3. download bytes
	data, err := c.downloadImage(ctx, imageURL)
	if err != nil {
		return nil, nil, err
	}
	return data, info, nil
}

// pollImage polls one generation until it reports COMPLETE (returning the first
// image url) or FAILED (error). Honors ctx cancellation / deadline.
func (c *Client) pollImage(ctx context.Context, accessToken, genID string) (string, error) {
	payload, _ := json.Marshal(map[string]any{
		"operationName": "GenerationImages",
		"query":         qGenerationImages,
		"variables": map[string]any{
			"where": map[string]any{"id": map[string]any{"_in": []string{genID}}},
		},
	})

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	// Poll for the full generation budget (caller's genCtx), leaving headroom for
	// the download, instead of a shorter hardcoded cap that killed slow jobs early.
	// ctx already bounds the wait, so a stuck job still can't hang indefinitely.
	deadline := time.Now().Add(5 * time.Minute)
	if dl, ok := ctx.Deadline(); ok {
		deadline = dl.Add(-60 * time.Second)
	}

	for {
		body, status, err := c.graphqlP(ctx, accessToken, payload, false)
		if err != nil {
			return "", fmt.Errorf("%w: poll: %s", ErrTemporaryUpstream, err.Error())
		}
		if status == 401 || status == 403 {
			return "", ErrAuth
		}
		if status == 200 {
			var pr struct {
				Data struct {
					Generations []struct {
						Status          string `json:"status"`
						GeneratedImages []struct {
							URL string `json:"url"`
						} `json:"generated_images"`
					} `json:"generations"`
				} `json:"data"`
			}
			if err := json.Unmarshal(body, &pr); err == nil && len(pr.Data.Generations) > 0 {
				g := pr.Data.Generations[0]
				switch strings.ToUpper(g.Status) {
				case "COMPLETE":
					for _, img := range g.GeneratedImages {
						if u := strings.TrimSpace(img.URL); u != "" {
							return u, nil
						}
					}
					return "", fmt.Errorf("%w: complete but no image url", ErrTemporaryUpstream)
				case "FAILED":
					return "", fmt.Errorf("%w: generation failed", ErrTemporaryUpstream)
				}
			}
		}

		if time.Now().After(deadline) {
			return "", fmt.Errorf("%w: generation timed out", ErrTemporaryUpstream)
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}

// graphqlError inspects a GraphQL response body for an "errors" array and maps the
// first message to a classified sentinel (auth / quota / temporary). Returns nil
// when there are no errors.
func graphqlError(body []byte) error {
	var env struct {
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &env); err != nil || len(env.Errors) == 0 {
		return nil
	}
	msg := strings.TrimSpace(env.Errors[0].Message)
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "unauthor") || strings.Contains(low, "jwt") || strings.Contains(low, "token is") || strings.Contains(low, "forbidden"):
		return ErrAuth
	case strings.Contains(low, "token") || strings.Contains(low, "credit") || strings.Contains(low, "quota") || strings.Contains(low, "insufficient") || strings.Contains(low, "not enough"):
		return ErrQuotaExhausted
	default:
		return fmt.Errorf("leonardo: %s", clip([]byte(msg), 200))
	}
}
