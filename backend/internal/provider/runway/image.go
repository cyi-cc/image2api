package runway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"strings"
	"time"

	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/google/uuid"
)

// GenerateImage runs the Runway "Nano Banana Pro" (workflow_gemini_image /
// gemini-3-pro-image-preview) text/image-to-image pipeline: upload each
// reference image (DATASET + DATASET_PREVIEW → dataset) to obtain its
// {assetId, url}, create a gemini image task and poll it to completion, then
// download the rendered PNG. teamID is the workspace id; if empty it's derived
// from the token. imageSize is the "1K"/"2K"/"4K" tier (Pro honors 2K/4K).
// refs may be empty (pure text-to-image).
func (c *Client) GenerateImage(ctx context.Context, token, teamID, prompt, imageSize string, refs [][]byte) ([]byte, map[string]any, error) {
	token = strings.TrimSpace(strings.TrimPrefix(token, "Bearer "))
	if token == "" {
		return nil, nil, ErrAuth
	}
	if teamID == "" {
		teamID = TeamIDFromToken(token)
	}
	if teamID == "" {
		return nil, nil, errors.New("runway: no team id")
	}
	if strings.TrimSpace(imageSize) == "" {
		imageSize = "1K"
	}

	// Only the task-create (generate submit) egresses via the proxy; reference
	// upload, polling and download run on the local IP.
	submitClient, err := c.newTLSClient()
	if err != nil {
		return nil, nil, err
	}
	directClient, err := c.newDirectTLSClient()
	if err != nil {
		return nil, nil, err
	}

	var refImages []map[string]any
	for i, raw := range refs {
		if len(raw) == 0 {
			continue
		}
		filename := fmt.Sprintf("ref_%s_%d.png", time.Now().UTC().Format("20060102_150405"), i+1)
		assetID, url, upErr := c.uploadReference(ctx, directClient, token, teamID, filename, raw)
		if upErr != nil {
			return nil, nil, upErr
		}
		refImages = append(refImages, map[string]any{
			"tag":     fmt.Sprintf("IMG_%d", i+1),
			"assetId": assetID,
			"url":     url,
		})
	}

	taskID, err := c.createImageTask(ctx, submitClient, token, teamID, prompt, imageSize, refImages)
	if err != nil {
		return nil, nil, err
	}
	artifactURL, err := c.pollTask(ctx, directClient, token, teamID, taskID)
	if err != nil {
		return nil, nil, err
	}
	data, err := c.download(ctx, directClient, artifactURL)
	if err != nil {
		return nil, nil, err
	}
	meta := map[string]any{
		"provider":  "runway",
		"task_id":   taskID,
		"team_id":   teamID,
		"image_url": artifactURL,
	}
	return data, meta, nil
}

// uploadReference uploads one reference image through the dataset pipeline
// (DATASET_PREVIEW + DATASET uploads → /v1/datasets) and returns its asset id
// (= dataset id) and the cloudfront URL the task references.
func (c *Client) uploadReference(ctx context.Context, client tlsclient.HttpClient, token, teamID, filename string, data []byte) (string, string, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", "", errors.New("runway: failed to decode reference image")
	}
	previewUploadID, _, err := c.uploadFile(ctx, client, token, teamID, filename, "DATASET_PREVIEW", data)
	if err != nil {
		return "", "", err
	}
	// The DATASET upload's completed URL is exactly what the task references.
	datasetUploadID, refURL, err := c.uploadFile(ctx, client, token, teamID, filename, "DATASET", data)
	if err != nil {
		return "", "", err
	}
	assetID, _, err := c.createDataset(ctx, client, token, teamID, filename, datasetUploadID, previewUploadID, cfg.Width, cfg.Height)
	if err != nil {
		return "", "", err
	}
	return assetID, refURL, nil
}

// createImageTask creates a workflow_gemini_image (Nano Banana Pro) task and
// returns its id.
func (c *Client) createImageTask(ctx context.Context, client tlsclient.HttpClient, token, teamID, prompt, imageSize string, refImages []map[string]any) (string, error) {
	opts := map[string]any{
		"name":           "Nano Banana Pro - " + prompt,
		"text_prompt":    prompt,
		"num_images":     1,
		"image_size":     imageSize,
		"model":          "gemini-3-pro-image-preview",
		"exploreMode":    false,
		"creationSource": "tool-mode",
	}
	if len(refImages) > 0 {
		opts["reference_images"] = refImages
	}
	res, err := c.submitTask(ctx, client, token, teamID, map[string]any{
		"taskType":  "workflow_gemini_image",
		"options":   opts,
		"asTeamId":  jsonNumberOrString(teamID),
		"sessionId": uuid.NewString(),
	})
	if err != nil {
		return "", err
	}
	task, _ := res["task"].(map[string]any)
	id := strings.TrimSpace(stringValue(task["id"]))
	if id == "" {
		return "", fmt.Errorf("%w: image task missing id", ErrTemporaryUpstream)
	}
	return id, nil
}
