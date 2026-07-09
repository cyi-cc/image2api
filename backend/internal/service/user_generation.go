package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"backend/internal/model"
	"backend/internal/repo"
)

type UserGenerationService struct {
	v1     *V1Service
	events *repo.EventRepository
	users  *repo.UserRepository
	models *repo.ModelRepository
}

func NewUserGenerationService(v1 *V1Service, events *repo.EventRepository, users *repo.UserRepository, models *repo.ModelRepository) *UserGenerationService {
	return &UserGenerationService{
		v1:     v1,
		events: events,
		users:  users,
		models: models,
	}
}

type UserGenerateRequest struct {
	Model           string
	Prompt          string
	Ratio           string
	Resolution      string
	Duration        string
	ReferenceImages []string
	// DeAI applies 去AI特征 post-processing to the generated image (image only)
	// and charges the per-tier surcharge on top of the model price.
	DeAI bool
	// AccountID pins an admin test to one specific provider account (账号生图测试).
	AccountID string
}

func (s *UserGenerationService) Generate(ctx context.Context, user *model.User, in UserGenerateRequest) (map[string]any, error) {
	if user == nil || strings.TrimSpace(user.ID) == "" {
		return nil, errors.New("未登录或会话已过期")
	}
	// No single-job lock anymore — concurrent generations are allowed, capped by
	// the user's concurrency group (enforced in prepareImageExecution/Video).

	modelItem, err := s.models.Get(ctx, strings.TrimSpace(in.Model))
	if err != nil {
		return nil, ErrUnknownModel
	}

	principal := &APIPrincipal{
		User:      user,
		TokenType: "session",
	}

	switch modelItem.Type {
	case "video":
		resp, err := s.v1.prepareSessionVideo(ctx, principal, V1VideoRequest{
			Model:           in.Model,
			Prompt:          in.Prompt,
			Duration:        in.Duration,
			AspectRatio:     in.Ratio,
			Resolution:      in.Resolution,
			ReferenceImages: in.ReferenceImages,
		})
		if err != nil {
			return nil, err
		}
		return resp, nil
	default:
		resp, err := s.v1.prepareSessionImage(ctx, principal, V1ImageRequest{
			Model:           in.Model,
			Prompt:          in.Prompt,
			AspectRatio:     in.Ratio,
			Resolution:      in.Resolution,
			ReferenceImages: in.ReferenceImages,
			DeAI:            in.DeAI,
		})
		if err != nil {
			return nil, err
		}
		return resp, nil
	}
}

func (s *UserGenerationService) AdminTest(ctx context.Context, user *model.User, in UserGenerateRequest) (map[string]any, error) {
	if user == nil || strings.TrimSpace(user.ID) == "" {
		return nil, errors.New("未登录或会话已过期")
	}
	modelItem, err := s.models.Get(ctx, strings.TrimSpace(in.Model))
	if err != nil {
		return nil, ErrUnknownModel
	}
	principal := &APIPrincipal{
		User:      user,
		TokenType: "session",
	}
	switch modelItem.Type {
	case "video":
		return s.v1.prepareAdminTestVideo(ctx, principal, V1VideoRequest{
			Model:           in.Model,
			Prompt:          in.Prompt,
			Duration:        in.Duration,
			AspectRatio:     in.Ratio,
			Resolution:      in.Resolution,
			ReferenceImages: in.ReferenceImages,
			AccountID:       in.AccountID,
		})
	default:
		return s.v1.prepareAdminTestImage(ctx, principal, V1ImageRequest{
			Model:           in.Model,
			Prompt:          in.Prompt,
			AspectRatio:     in.Ratio,
			Resolution:      in.Resolution,
			ReferenceImages: in.ReferenceImages,
			AccountID:       in.AccountID,
		})
	}
}

func (s *UserGenerationService) MyJobs(ctx context.Context, user *model.User, source string) (map[string]any, error) {
	if user == nil || strings.TrimSpace(user.ID) == "" {
		return map[string]any{"pending": nil, "latest": nil}, nil
	}
	// source scopes the lookup: "user" = 画图台(默认),"admin" = 后台测试模型。
	// Both are this caller's own events; the admin-test poll uses "admin" so a
	// gateway-timed-out (524) test can still recover its result.
	if source != "admin" {
		source = "user"
	}
	modelNames, err := s.ModelNameMap(ctx)
	if err != nil {
		return nil, err
	}
	pending, err := s.events.PendingByUser(ctx, user.ID, source)
	if err != nil {
		return nil, err
	}
	latest, err := s.events.LatestByUser(ctx, user.ID, source)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"pending": shapeJobEvent(pending, modelNames),
		"latest":  shapeJobEvent(latest, modelNames),
	}, nil
}

func (s *UserGenerationService) ModelNameMap(ctx context.Context) (map[string]string, error) {
	return s.models.NameMap(ctx)
}

func shapeJobEvent(item *model.EventLog, modelNames map[string]string) map[string]any {
	if item == nil {
		return nil
	}
	status := item.Status
	url := ""
	if strings.TrimSpace(item.File) != "" {
		url = "/images/" + strings.ReplaceAll(strings.TrimSpace(item.File), "\\", "/")
	}
	return map[string]any{
		"id":             item.ID,
		"kind":           item.Kind,
		"model":          displayModelName(modelNames, item.Model),
		"prompt":         item.Prompt,
		"ratio":          item.Ratio,
		"resolution":     item.Resolution,
		"duration":       item.Duration,
		"deai":           item.DeAI,
		"status":         status,
		"file":           emptyOrNil(item.File),
		"url":            emptyOrNil(url),
		"reference_urls": referenceURLs(item.RefFiles),
		"elapsed_ms":     item.ElapsedMS,
		"error":          emptyOrNil(item.Error),
		"charged":        item.Cost,
		"cost":           item.Cost,
		"ts":             item.TS.Unix(),
	}
}

func displayModelName(modelNames map[string]string, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if modelNames != nil {
		if name, ok := modelNames[raw]; ok && strings.TrimSpace(name) != "" {
			return name
		}
	}
	return raw
}

// referenceURLs turns the stored relative reference paths into /images URLs so
// the playground can re-display the uploaded reference image(s) after a reload.
func referenceURLs(raw []byte) []string {
	if len(raw) == 0 {
		return []string{}
	}
	var paths []string
	if err := json.Unmarshal(raw, &paths); err != nil {
		return []string{}
	}
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.ReplaceAll(strings.TrimSpace(p), "\\", "/")
		if p != "" {
			out = append(out, "/images/"+p)
		}
	}
	return out
}

func emptyOrNil(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
