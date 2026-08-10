package leonardo

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

const mUploadInitImage = `mutation UploadInitImage($arg1: InitImageUploadInput!) {
  uploadInitImage(arg1: $arg1) {
    id
    url
    fields
    __typename
  }
}`

// initImageExtension narrows a sniffed extension to what uploadInitImage accepts
// (png / jpg / jpeg / webp); anything else is sent as png.
func initImageExtension(extension string) string {
	switch strings.TrimPrefix(strings.ToLower(strings.TrimSpace(extension)), ".") {
	case "jpg":
		return "jpg"
	case "jpeg":
		return "jpeg"
	case "webp":
		return "webp"
	default:
		return "png"
	}
}

// uploadInitImage uploads a reference image and returns the init image id to put
// in a Generate request's image_reference guidance. It has to go through
// uploadInitImage (permanent init-image bucket): the uploadImage mutation only
// hands out temporary-bucket ids, which the generation service can't resolve.
func (c *Client) uploadInitImage(ctx context.Context, cookie, extension string, img []byte) (string, error) {
	extension = initImageExtension(extension)
	payload, _ := json.Marshal(map[string]any{
		"operationName": "UploadInitImage",
		"query":         mUploadInitImage,
		"variables":     map[string]any{"arg1": map[string]any{"extension": extension}},
	})
	body, err := c.callGraphQL(ctx, cookie, payload, false, "upload-init-image")
	if err != nil {
		return "", err
	}
	var ur struct {
		Data struct {
			UploadInitImage struct {
				ID     string `json:"id"`
				URL    string `json:"url"`
				Fields string `json:"fields"`
			} `json:"uploadInitImage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &ur); err != nil {
		return "", fmt.Errorf("%w: upload-init-image non-json", ErrTemporaryUpstream)
	}
	up := ur.Data.UploadInitImage
	if up.ID == "" || up.URL == "" {
		return "", fmt.Errorf("%w: no upload url", ErrTemporaryUpstream)
	}
	if err := c.putPresigned(ctx, up.URL, up.Fields, "asset."+extension, img); err != nil {
		return "", err
	}
	return up.ID, nil
}

// putPresigned performs the presigned S3 POST: all policy fields first, the file
// part LAST.
func (c *Client) putPresigned(ctx context.Context, url, fieldsJSON, filename string, asset []byte) error {
	var fields map[string]string
	if err := json.Unmarshal([]byte(fieldsJSON), &fields); err != nil {
		return fmt.Errorf("%w: bad upload fields", ErrTemporaryUpstream)
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		_ = w.WriteField(k, v)
	}
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		return err
	}
	if _, err := fw.Write(asset); err != nil {
		return err
	}
	_ = w.Close()

	client, err := c.newDirectTLSClient()
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		return err
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
		return fmt.Errorf("%w: s3 upload: %s", ErrTemporaryUpstream, err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != 204 && resp.StatusCode != 200 && resp.StatusCode != 201 {
		return fmt.Errorf("%w: s3 upload http %d", ErrTemporaryUpstream, resp.StatusCode)
	}
	return nil
}

// uploadAsset uploads one reference asset (extension png / mp3 / mp4 …) through
// the same UploadImage presigned-S3 flow images use, and returns its upload id.
func (c *Client) uploadAsset(ctx context.Context, cookie, extension string, asset []byte) (string, error) {
	extension = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(extension)), ".")
	if extension == "" {
		extension = "png"
	}
	payload, _ := json.Marshal(map[string]any{
		"operationName": "UploadImage",
		"query":         mUploadImage,
		// originalFilename is mandatory for audio uploads and harmless otherwise.
		"variables": map[string]any{"uploadImageInput": map[string]any{
			"uploadType":       "INIT",
			"extension":        extension,
			"originalFilename": "asset." + extension,
		}},
	})
	body, err := c.callGraphQL(ctx, cookie, payload, false, "upload-init")
	if err != nil {
		return "", err
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
	if err := c.putPresigned(ctx, up.URL, up.Fields, "asset."+extension, asset); err != nil {
		return "", err
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
		uploadID, upErr := c.uploadInitImage(ctx, cookie, assetExtension(img, "png"), img)
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
	body, err := c.callGraphQL(ctx, cookie, payload, true, "generate")
	if err != nil {
		return nil, nil, err
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
	imageURL, err := c.pollImage(ctx, cookie, genID)
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
func (c *Client) pollImage(ctx context.Context, cookie, genID string) (string, error) {
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
		body, err := c.callGraphQL(ctx, cookie, payload, false, "poll")
		if errors.Is(err, ErrAuth) {
			return "", err
		}
		// 其它错误（含上游临时抖动）不中断轮询，等 deadline 再判超时。
		if err == nil {
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
			Message    string `json:"message"`
			Extensions struct {
				Code    string `json:"code"`
				Details struct {
					Message string `json:"message"`
				} `json:"details"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &env); err != nil || len(env.Errors) == 0 {
		return nil
	}
	msg := strings.TrimSpace(env.Errors[0].Message)
	// The generic "An error occurred." hides the real reason in extensions.
	if detail := strings.TrimSpace(env.Errors[0].Extensions.Details.Message); detail != "" && detail != msg {
		msg = msg + " (" + detail + ")"
	}
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
