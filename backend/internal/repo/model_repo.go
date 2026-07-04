package repo

import (
	"context"
	"encoding/json"
	"time"

	"backend/internal/model"
	"gorm.io/datatypes"
	"gorm.io/gorm"
)

type ModelRepository struct {
	db *gorm.DB
}

func NewModelRepository(db *gorm.DB) *ModelRepository {
	return &ModelRepository{db: db}
}

// IncrementGenerationCount bumps a model's persistent success counter by 1.
// Best-effort: a missing model id is a no-op (0 rows affected, no error).
func (r *ModelRepository) IncrementGenerationCount(ctx context.Context, modelID string) error {
	return r.db.WithContext(ctx).Model(&model.ModelConfig{}).
		Where("id = ?", modelID).
		UpdateColumn("generation_count", gorm.Expr("generation_count + 1")).Error
}

func (r *ModelRepository) List(ctx context.Context) ([]model.ModelConfig, error) {
	var items []model.ModelConfig
	// Higher weight floats to the top of the dropdown / admin list; ties fall
	// back to newest-first so order stays stable for equal-weight models.
	if err := r.db.WithContext(ctx).Order("weight desc, created_at desc").Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (r *ModelRepository) Get(ctx context.Context, modelID string) (*model.ModelConfig, error) {
	var item model.ModelConfig
	if err := r.db.WithContext(ctx).First(&item, "(alias <> '' AND alias = ?) OR (alias = '' AND id = ?)", modelID, modelID).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *ModelRepository) NameMap(ctx context.Context) (map[string]string, error) {
	items, err := r.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(items))
	for _, item := range items {
		out[item.ID] = item.EffectiveName()
	}
	return out, nil
}

func JSONStrings(v datatypes.JSON) []string {
	if len(v) == 0 {
		return []string{}
	}
	var out []string
	if err := json.Unmarshal([]byte(v), &out); err == nil {
		return out
	}
	return []string{}
}

func (r *ModelRepository) Create(ctx context.Context, item *model.ModelConfig) error {
	return r.db.WithContext(ctx).Create(item).Error
}

func (r *ModelRepository) Update(ctx context.Context, modelID string, patch map[string]any) (*model.ModelConfig, error) {
	patch["updated_at"] = time.Now()
	if err := r.db.WithContext(ctx).Model(&model.ModelConfig{}).Where("id = ?", modelID).Updates(patch).Error; err != nil {
		return nil, err
	}
	var item model.ModelConfig
	if err := r.db.WithContext(ctx).First(&item, "id = ?", modelID).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *ModelRepository) Delete(ctx context.Context, modelID string) (int64, error) {
	res := r.db.WithContext(ctx).Delete(&model.ModelConfig{}, "id = ?", modelID)
	return res.RowsAffected, res.Error
}
