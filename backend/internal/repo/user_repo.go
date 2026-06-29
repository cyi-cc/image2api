package repo

import (
	"context"
	"errors"
	"strings"
	"time"

	"backend/internal/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type UserRepository struct {
	db *gorm.DB
}

var ErrAlreadyCheckedInToday = errors.New("already checked in today")

type InviteStats struct {
	InviteCount  int64 `json:"invite_count"`
	InviteEarned int   `json:"invite_earned"`
}

type InviteRecord struct {
	Name         string     `json:"name,omitempty"`
	Inviter      string     `json:"inviter,omitempty"`
	Invitee      string     `json:"invitee,omitempty"`
	Reward       int        `json:"reward"`
	RegisteredAt time.Time  `json:"registered_at"`
	CompletedAt  *time.Time `json:"completed_at"`
	Status       string     `json:"status"`
}

type InviteLogStats struct {
	Total      int64 `json:"total"`
	Completed  int64 `json:"completed"`
	Pending    int64 `json:"pending"`
	RewardPaid int64 `json:"reward_paid"`
}

type CheckinResult struct {
	Already bool    `json:"already"`
	Awarded int     `json:"awarded"`
	Streak  int     `json:"streak"`
	Credits float64 `json:"credits"`
}

func NewUserRepository(db *gorm.DB) *UserRepository {
	return &UserRepository{db: db}
}

func (r *UserRepository) GetByID(ctx context.Context, userID string) (*model.User, error) {
	var user model.User
	if err := r.db.WithContext(ctx).Preload("APIKeys").First(&user, "id = ?", userID).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) GetByIdentifier(ctx context.Context, identifier string) (*model.User, error) {
	ident := strings.TrimSpace(identifier)
	if ident == "" {
		return nil, gorm.ErrRecordNotFound
	}

	var user model.User
	q := r.db.WithContext(ctx).Preload("APIKeys")
	if strings.Contains(ident, "@") {
		if err := q.First(&user, "email = ?", strings.ToLower(ident)).Error; err != nil {
			return nil, err
		}
		return &user, nil
	}

	if err := q.First(&user, "LOWER(name) = ?", strings.ToLower(ident)).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) GetByInviteCode(ctx context.Context, code string) (*model.User, error) {
	var user model.User
	if err := r.db.WithContext(ctx).First(&user, "invite_code = ?", strings.ToUpper(strings.TrimSpace(code))).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// IncrementGenerationCount bumps a user's persistent success counter by 1.
func (r *UserRepository) IncrementGenerationCount(ctx context.Context, userID string) error {
	if userID == "" {
		return nil
	}
	return r.db.WithContext(ctx).Model(&model.User{}).
		Where("id = ?", userID).
		UpdateColumn("generation_count", gorm.Expr("generation_count + 1")).Error
}

func (r *UserRepository) List(ctx context.Context) ([]model.User, error) {
	var users []model.User
	if err := r.db.WithContext(ctx).Preload("APIKeys").Order("created_at desc").Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

func (r *UserRepository) ExistsEmail(ctx context.Context, email, excludeUserID string) (bool, error) {
	var count int64
	q := r.db.WithContext(ctx).Model(&model.User{}).Where("email = ?", strings.ToLower(strings.TrimSpace(email)))
	if strings.TrimSpace(excludeUserID) != "" {
		q = q.Where("id <> ?", strings.TrimSpace(excludeUserID))
	}
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *UserRepository) ExistsName(ctx context.Context, name, excludeUserID string) (bool, error) {
	var count int64
	q := r.db.WithContext(ctx).Model(&model.User{}).Where("LOWER(name) = ?", strings.ToLower(strings.TrimSpace(name)))
	if strings.TrimSpace(excludeUserID) != "" {
		q = q.Where("id <> ?", strings.TrimSpace(excludeUserID))
	}
	if err := q.Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *UserRepository) GetByAPIKeyHash(ctx context.Context, keyHash string) (*model.User, error) {
	var apiKey model.APIKey
	if err := r.db.WithContext(ctx).First(&apiKey, "key_hash = ?", keyHash).Error; err != nil {
		return nil, err
	}

	var user model.User
	if err := r.db.WithContext(ctx).Preload("APIKeys").First(&user, "id = ?", apiKey.UserID).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) TouchLogin(ctx context.Context, userID, ip string) error {
	now := time.Now()
	return r.db.WithContext(ctx).
		Model(&model.User{}).
		Where("id = ?", userID).
		Updates(map[string]any{
			"last_login_at": now,
			"last_login_ip": ip,
		}).Error
}

func (r *UserRepository) HasAdmin(ctx context.Context) (bool, error) {
	var count int64
	if err := r.db.WithContext(ctx).
		Model(&model.User{}).
		Where("role = ?", "admin").
		Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

func (r *UserRepository) Stats(ctx context.Context) (map[string]any, error) {
	var total, active, disabled, admins int64
	if err := r.db.WithContext(ctx).Model(&model.User{}).Count(&total).Error; err != nil {
		return nil, err
	}
	if err := r.db.WithContext(ctx).Model(&model.User{}).Where("status = ?", "active").Count(&active).Error; err != nil {
		return nil, err
	}
	if err := r.db.WithContext(ctx).Model(&model.User{}).Where("status = ?", "disabled").Count(&disabled).Error; err != nil {
		return nil, err
	}
	if err := r.db.WithContext(ctx).Model(&model.User{}).Where("role = ?", "admin").Count(&admins).Error; err != nil {
		return nil, err
	}

	type sumRow struct {
		Total *float64 `gorm:"column:total"`
	}
	var credits sumRow
	if err := r.db.WithContext(ctx).
		Model(&model.User{}).
		Select("SUM(credits) AS total").
		Scan(&credits).Error; err != nil {
		return nil, err
	}

	now := time.Now()
	dayCut := now.Add(-24 * time.Hour)
	weekCut := now.Add(-7 * 24 * time.Hour)
	var new24h, new7d, active24h int64
	if err := r.db.WithContext(ctx).Model(&model.User{}).Where("created_at >= ?", dayCut).Count(&new24h).Error; err != nil {
		return nil, err
	}
	if err := r.db.WithContext(ctx).Model(&model.User{}).Where("created_at >= ?", weekCut).Count(&new7d).Error; err != nil {
		return nil, err
	}
	if err := r.db.WithContext(ctx).Model(&model.User{}).Where("last_login_at >= ?", dayCut).Count(&active24h).Error; err != nil {
		return nil, err
	}

	creditsTotal := 0.0
	if credits.Total != nil {
		creditsTotal = *credits.Total
	}

	return map[string]any{
		"total":         total,
		"active":        active,
		"disabled":      disabled,
		"admins":        admins,
		"credits_total": creditsTotal,
		"new_24h":       new24h,
		"new_7d":        new7d,
		"active_24h":    active24h,
	}, nil
}

type CheckinStats struct {
	TodayCount int64 `json:"today_count"`
	MaxStreak  int64 `json:"max_streak"`
}

// CheckinStats counts users who checked in today and the longest active streak —
// a single-query summary for the admin dashboard's 签到 card.
func (r *UserRepository) CheckinStats(ctx context.Context) (*CheckinStats, error) {
	today := time.Now().Format("2006-01-02")
	type row struct {
		TodayCount int64 `gorm:"column:today_count"`
		MaxStreak  int64 `gorm:"column:max_streak"`
	}
	var out row
	if err := r.db.WithContext(ctx).
		Model(&model.User{}).
		Select("COUNT(*) FILTER (WHERE checkin_last = ?) AS today_count, COALESCE(MAX(checkin_streak), 0) AS max_streak", today).
		Scan(&out).Error; err != nil {
		return nil, err
	}
	return &CheckinStats{TodayCount: out.TodayCount, MaxStreak: out.MaxStreak}, nil
}

type InviteSummary struct {
	Total     int64 `json:"total"`
	Completed int64 `json:"completed"`
}

// InviteSummary is a lightweight count of invited users (and how many have had
// their reward granted). Cheaper than AllInvites — no JOIN, no record list —
// for the dashboard which polls frequently.
func (r *UserRepository) InviteSummary(ctx context.Context) (*InviteSummary, error) {
	type row struct {
		Total     int64 `gorm:"column:total"`
		Completed int64 `gorm:"column:completed"`
	}
	var out row
	if err := r.db.WithContext(ctx).
		Model(&model.User{}).
		Select("COUNT(*) AS total, COUNT(*) FILTER (WHERE invite_reward_done) AS completed").
		Where("invited_by IS NOT NULL AND invited_by <> ''").
		Scan(&out).Error; err != nil {
		return nil, err
	}
	return &InviteSummary{Total: out.Total, Completed: out.Completed}, nil
}

func (r *UserRepository) Create(ctx context.Context, user *model.User) error {
	return r.db.WithContext(ctx).Create(user).Error
}

func (r *UserRepository) Update(ctx context.Context, userID string, patch map[string]any) (*model.User, error) {
	patch["updated_at"] = time.Now()
	if err := r.db.WithContext(ctx).Model(&model.User{}).Where("id = ?", userID).Updates(patch).Error; err != nil {
		return nil, err
	}
	return r.GetByID(ctx, userID)
}

func (r *UserRepository) Delete(ctx context.Context, userID string) (int64, error) {
	res := r.db.WithContext(ctx).Delete(&model.User{}, "id = ?", userID)
	return res.RowsAffected, res.Error
}

func (r *UserRepository) DeleteByIDs(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := r.db.WithContext(ctx).Delete(&model.User{}, "id IN ?", ids)
	return res.RowsAffected, res.Error
}

func (r *UserRepository) SetPasswordByEmail(ctx context.Context, email, passwordHash string) (*model.User, error) {
	if err := r.db.WithContext(ctx).
		Model(&model.User{}).
		Where("email = ?", strings.ToLower(strings.TrimSpace(email))).
		Updates(map[string]any{
			"password_hash": passwordHash,
			"updated_at":    time.Now(),
		}).Error; err != nil {
		return nil, err
	}
	var user model.User
	if err := r.db.WithContext(ctx).Preload("APIKeys").First(&user, "email = ?", strings.ToLower(strings.TrimSpace(email))).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) TouchAPIKeyUsage(ctx context.Context, keyHash string) error {
	now := time.Now()
	return r.db.WithContext(ctx).
		Model(&model.APIKey{}).
		Where("key_hash = ?", keyHash).
		Update("last_used_at", now).Error
}

func (r *UserRepository) InviteStats(ctx context.Context, userID string, reward int) (*InviteStats, error) {
	var inviteCount int64
	if err := r.db.WithContext(ctx).
		Model(&model.User{}).
		Where("invited_by = ?", userID).
		Count(&inviteCount).Error; err != nil {
		return nil, err
	}

	var rewardedCount int64
	if err := r.db.WithContext(ctx).
		Model(&model.User{}).
		Where("invited_by = ? AND invite_reward_done = ?", userID, true).
		Count(&rewardedCount).Error; err != nil {
		return nil, err
	}

	return &InviteStats{
		InviteCount:  inviteCount,
		InviteEarned: int(rewardedCount) * reward,
	}, nil
}

func (r *UserRepository) InviteList(ctx context.Context, userID string, reward int) ([]InviteRecord, error) {
	type row struct {
		Name             string
		CreatedAt        time.Time
		InviteRewardDone bool
		InviteRewardAt   *time.Time
	}

	var rows []row
	if err := r.db.WithContext(ctx).
		Model(&model.User{}).
		Select("name, created_at, invite_reward_done, invite_reward_at").
		Where("invited_by = ?", userID).
		Order("created_at desc").
		Find(&rows).Error; err != nil {
		return nil, err
	}

	out := make([]InviteRecord, 0, len(rows))
	for _, item := range rows {
		status := "pending"
		rewardValue := 0
		if item.InviteRewardDone {
			status = "completed"
			rewardValue = reward
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = "—"
		}
		out = append(out, InviteRecord{
			Name:         name,
			Reward:       rewardValue,
			RegisteredAt: item.CreatedAt,
			CompletedAt:  item.InviteRewardAt,
			Status:       status,
		})
	}
	return out, nil
}

func (r *UserRepository) AllInvites(ctx context.Context, reward int) ([]InviteRecord, *InviteLogStats, error) {
	type row struct {
		InviterName      string     `gorm:"column:inviter_name"`
		InviterEmail     string     `gorm:"column:inviter_email"`
		InviteeName      string     `gorm:"column:invitee_name"`
		InviteeEmail     string     `gorm:"column:invitee_email"`
		CreatedAt        time.Time  `gorm:"column:created_at"`
		InviteRewardDone bool       `gorm:"column:invite_reward_done"`
		InviteRewardAt   *time.Time `gorm:"column:invite_reward_at"`
	}

	var rows []row
	if err := r.db.WithContext(ctx).
		Table("users AS invitee").
		Select(`
			inviter.name AS inviter_name,
			inviter.email AS inviter_email,
			invitee.name AS invitee_name,
			invitee.email AS invitee_email,
			invitee.created_at,
			invitee.invite_reward_done,
			invitee.invite_reward_at
		`).
		Joins("JOIN users AS inviter ON inviter.id = invitee.invited_by").
		Order("invitee.created_at desc").
		Scan(&rows).Error; err != nil {
		return nil, nil, err
	}

	out := make([]InviteRecord, 0, len(rows))
	stats := &InviteLogStats{}
	for _, item := range rows {
		stats.Total++
		status := "pending"
		rewardValue := 0
		if item.InviteRewardDone {
			status = "completed"
			rewardValue = reward
			stats.Completed++
			stats.RewardPaid += int64(reward)
		} else {
			stats.Pending++
		}

		inviter := strings.TrimSpace(item.InviterName)
		if inviter == "" {
			inviter = strings.TrimSpace(item.InviterEmail)
		}
		invitee := strings.TrimSpace(item.InviteeName)
		if invitee == "" {
			invitee = strings.TrimSpace(item.InviteeEmail)
		}

		out = append(out, InviteRecord{
			Inviter:      inviter,
			Invitee:      invitee,
			Reward:       rewardValue,
			RegisteredAt: item.CreatedAt,
			CompletedAt:  item.InviteRewardAt,
			Status:       status,
		})
	}
	return out, stats, nil
}

func (r *UserRepository) DailyCheckin(ctx context.Context, userID string, reward int) (*CheckinResult, error) {
	today := time.Now().Format("2006-01-02")
	yesterday := time.Now().Add(-24 * time.Hour).Format("2006-01-02")

	var result *CheckinResult
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, "id = ?", userID).Error; err != nil {
			return err
		}
		if user.CheckinLast == today {
			result = &CheckinResult{
				Already: true,
				Awarded: 0,
				Streak:  user.CheckinStreak,
				Credits: user.Credits,
			}
			return ErrAlreadyCheckedInToday
		}

		streak := 1
		if user.CheckinLast == yesterday {
			streak = user.CheckinStreak + 1
		}
		credits := user.Credits + float64(reward)

		if err := tx.Model(&model.User{}).
			Where("id = ?", userID).
			Updates(map[string]any{
				"credits":        credits,
				"checkin_last":   today,
				"checkin_streak": streak,
				"updated_at":     time.Now(),
			}).Error; err != nil {
			return err
		}

		result = &CheckinResult{
			Already: false,
			Awarded: reward,
			Streak:  streak,
			Credits: credits,
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrAlreadyCheckedInToday) {
			return result, nil
		}
		return nil, err
	}
	return result, nil
}

func (r *UserRepository) AdjustCredits(ctx context.Context, userID string, delta float64) (*model.User, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, "id = ?", userID).Error; err != nil {
			return err
		}
		nextCredits := user.Credits + delta
		if nextCredits < 0 {
			nextCredits = 0
		}
		return tx.Model(&model.User{}).
			Where("id = ?", userID).
			Updates(map[string]any{
				"credits":    nextCredits,
				"updated_at": time.Now(),
			}).Error
	})
	if err != nil {
		return nil, err
	}
	return r.GetByID(ctx, userID)
}

// SetCredits sets a user's credit balance to an absolute (non-negative) value.
// The row is locked for the duration of the transaction so it stays consistent
// with concurrent AdjustCredits/TryDebitCredits operations.
func (r *UserRepository) SetCredits(ctx context.Context, userID string, value float64) (*model.User, error) {
	if value < 0 {
		value = 0
	}
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&user, "id = ?", userID).Error; err != nil {
			return err
		}
		return tx.Model(&model.User{}).
			Where("id = ?", userID).
			Updates(map[string]any{
				"credits":    value,
				"updated_at": time.Now(),
			}).Error
	})
	if err != nil {
		return nil, err
	}
	return r.GetByID(ctx, userID)
}

func (r *UserRepository) TryDebitCredits(ctx context.Context, userID string, amount float64) (*model.User, bool, error) {
	if amount <= 0 {
		user, err := r.GetByID(ctx, userID)
		return user, user != nil, err
	}

	var result *model.User
	debited := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var user model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Preload("APIKeys").First(&user, "id = ?", userID).Error; err != nil {
			return err
		}
		if user.Credits < amount {
			result = &user
			return nil
		}
		nextCredits := user.Credits - amount
		if err := tx.Model(&model.User{}).
			Where("id = ?", userID).
			Updates(map[string]any{
				"credits":    nextCredits,
				"updated_at": time.Now(),
			}).Error; err != nil {
			return err
		}
		user.Credits = nextCredits
		user.UpdatedAt = time.Now()
		result = &user
		debited = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return result, debited, nil
}

func (r *UserRepository) GrantInviteReward(ctx context.Context, inviteeUserID string, reward int) (bool, error) {
	if reward <= 0 {
		return false, nil
	}

	granted := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var invitee model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&invitee, "id = ?", inviteeUserID).Error; err != nil {
			return err
		}
		if invitee.InvitedBy == nil || *invitee.InvitedBy == "" || invitee.InviteRewardDone {
			return nil
		}

		var inviter model.User
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&inviter, "id = ?", *invitee.InvitedBy).Error; err != nil {
			return err
		}

		now := time.Now()
		if err := tx.Model(&model.User{}).
			Where("id = ?", invitee.ID).
			Updates(map[string]any{
				"invite_reward_done": true,
				"invite_reward_at":   now,
				"updated_at":         now,
			}).Error; err != nil {
			return err
		}

		if err := tx.Model(&model.User{}).
			Where("id = ?", inviter.ID).
			Updates(map[string]any{
				"credits":    inviter.Credits + float64(reward),
				"updated_at": now,
			}).Error; err != nil {
			return err
		}

		granted = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return granted, nil
}
