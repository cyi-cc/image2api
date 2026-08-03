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
)

// GenerateImage runs a Runway gemini image text/image-to-image pipeline:
// upload each reference image (DATASET + DATASET_PREVIEW → dataset) to obtain
// its {assetId, url}, create a gemini image task and poll it to completion,
// then download the rendered PNG. modelID selects the variant: "runway-nano-banana-2"
// → gemini_3_1_flash_image / gemini-3.1-flash-image-preview (with aspect_ratio),
// anything else → workflow_gemini_image / gemini-3-pro-image-preview. imageSize
// is the "1K"/"2K"/"4K" tier. teamID is the workspace id; if empty it's derived
// from the token. refs may be empty (pure text-to-image).
func (c *Client) GenerateImage(ctx context.Context, token, teamID, modelID, prompt, aspectRatio, imageSize string, refs [][]byte, downloadResult bool) ([]byte, map[string]any, error) {
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
	var refAssetIDs []string
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
		refAssetIDs = append(refAssetIDs, assetID)
	}

	// Browser order: create the real session FIRST, attach the references and its
	// own asset group, THEN submit the task into it (see createSession). The task
	// carries this session's assetGroupId, NOT the account-wide "Generations"
	// group.
	sessionID, err := c.createSession(ctx, submitClient, token, teamID)
	if err != nil {
		return nil, nil, err
	}
	for _, aid := range refAssetIDs {
		c.attachReference(ctx, submitClient, token, teamID, sessionID, aid)
	}
	assetGroupID, _ := c.sessionAssetGroup(ctx, submitClient, token, teamID, sessionID) // best-effort

	taskID, err := c.createImageTask(ctx, submitClient, token, teamID, modelID, prompt, aspectRatio, imageSize, sessionID, assetGroupID, refImages)
	if err != nil {
		return nil, nil, err
	}
	artifactURL, err := c.pollTask(ctx, directClient, token, teamID, taskID)
	if err != nil {
		return nil, nil, err
	}
	meta := map[string]any{
		"provider":  "runway",
		"task_id":   taskID,
		"team_id":   teamID,
		"image_url": artifactURL,
	}
	// downloadResult=false (API-key url-only mode): return the upstream artifact
	// URL without downloading the PNG.
	if !downloadResult {
		return nil, meta, nil
	}
	data, err := c.download(ctx, directClient, artifactURL)
	if err != nil {
		return nil, nil, err
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

// createImageTask submits a gemini image task (workflow_gemini_image for Pro,
// gemini_3_1_flash_image for Nano Banana 2) INTO the pre-created session and
// returns its id. sessionID must be a real server-side session (see
// createSession) and assetGroupID must be that session's own group.
func (c *Client) createImageTask(ctx context.Context, client tlsclient.HttpClient, token, teamID, modelID, prompt, aspectRatio, imageSize, sessionID, assetGroupID string, refImages []map[string]any) (string, error) {
	if strings.TrimSpace(aspectRatio) == "" {
		aspectRatio = "16:9"
	}
	taskType := "workflow_gemini_image"
	opts := map[string]any{
		"name":           "Nano Banana Pro - " + prompt,
		"text_prompt":    prompt,
		"aspect_ratio":   aspectRatio,
		"num_images":     1,
		"image_size":     imageSize,
		"model":          "gemini-3-pro-image-preview",
		"exploreMode":    false,
		"creationSource": "tool-mode",
	}
	if modelID == "runway-nano-banana-2" {
		taskType = "gemini_3_1_flash_image"
		opts["name"] = "Nano Banana 2 - " + prompt
		opts["model"] = "gemini-3.1-flash-image-preview"
	}
	if assetGroupID != "" {
		opts["assetGroupId"] = assetGroupID
	}
	if len(refImages) > 0 {
		opts["reference_images"] = refImages
	}
	// Pre-flight cost estimate, exactly as the web app does before every spend.
	c.estimateCost(ctx, client, token, teamID, "gemini_image", opts)

	res, err := c.submitTask(ctx, client, token, teamID, map[string]any{
		"taskType":  taskType,
		"options":   opts,
		"asTeamId":  jsonNumberOrString(teamID),
		"sessionId": sessionID,
	})
	if err != nil {
		return "", err
	}
	task, _ := res["task"].(map[string]any)
	id := strings.TrimSpace(stringValue(task["id"]))
	if id == "" {
		return "", fmt.Errorf("%w: image task missing id", ErrTemporaryUpstream)
	}
	// Post-submit: generation record + session play (session already exists).
	c.recordGeneration(ctx, client, token, teamID, id, sessionID, prompt, opts)
	return id, nil
}
