package repo

import (
	"context"
	"errors"
	"strings"
	"time"

	"backend/internal/model"
	"github.com/google/uuid"
	"gorm.io/gorm"
)

type BannedWordRepository struct {
	db *gorm.DB
}

func NewBannedWordRepository(db *gorm.DB) *BannedWordRepository {
	return &BannedWordRepository{db: db}
}

func (r *BannedWordRepository) List(ctx context.Context) ([]model.BannedWord, error) {
	var items []model.BannedWord
	err := r.db.WithContext(ctx).Order("hits DESC, created_at DESC").Find(&items).Error
	return items, err
}

func (r *BannedWordRepository) Create(ctx context.Context, word string) (*model.BannedWord, error) {
	word = strings.TrimSpace(word)
	if word == "" {
		return nil, errors.New("违禁词不能为空")
	}
	item := &model.BannedWord{
		ID:        strings.ReplaceAll(uuid.NewString(), "-", "")[:32],
		Word:      word,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}
	if err := r.db.WithContext(ctx).Create(item).Error; err != nil {
		return nil, errors.New("添加失败(可能已存在)")
	}
	return item, nil
}

func (r *BannedWordRepository) Delete(ctx context.Context, id string) (int64, error) {
	res := r.db.WithContext(ctx).Delete(&model.BannedWord{}, "id = ?", id)
	return res.RowsAffected, res.Error
}

// RecordHit bumps the word's block counter and, when userID is set, the user's
// 违禁词触发次数 shown on the admin users table. Best-effort bookkeeping.
func (r *BannedWordRepository) RecordHit(ctx context.Context, wordID, userID string) {
	_ = r.db.WithContext(ctx).Model(&model.BannedWord{}).Where("id = ?", wordID).
		UpdateColumn("hits", gorm.Expr("hits + 1")).Error
	if userID != "" {
		_ = r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).
			UpdateColumn("banned_word_hits", gorm.Expr("banned_word_hits + 1")).Error
	}
}
