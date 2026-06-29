package repo

import (
	"context"
	"errors"
	"strings"
	"time"

	"backend/internal/model"
	"gorm.io/gorm"
)

type EventRepository struct {
	db *gorm.DB
}

type EventListFilter struct {
	Limit         int
	Offset        int
	Kind          string
	Status        string   // single status (status = ?)
	Statuses      []string // multiple statuses (status IN (?)) — used by the 画图台 grid
	Since         *time.Time
	UserID        string
	ExcludeSource string // when set, omit rows with this source (e.g. hide API-key "v1" usage from the customer logs page)
	Source        string // when set, keep ONLY rows with this source (admin 来源 filter): "v1" (API key) / "user" (前台) / "admin" (测试模型)
	HasFile       bool   // when true, keep ONLY rows with a non-empty file (the 创作记录 gallery — paginates over real media)
}

type EventStats struct {
	Total          int64 `json:"total"`
	Success        int64 `json:"success"`
	Failed         int64 `json:"failed"`
	Pending        int64 `json:"pending"`
	AvgElapsedMS   *int  `json:"avg_elapsed_ms"`
	AvgElapsedMS24 *int  `json:"avg_elapsed_ms_24h"`
}

func NewEventRepository(db *gorm.DB) *EventRepository {
	return &EventRepository{db: db}
}

func (r *EventRepository) List(ctx context.Context, filter EventListFilter) ([]model.EventLog, int64, error) {
	q := r.db.WithContext(ctx).Model(&model.EventLog{})
	if filter.Kind != "" {
		q = q.Where("kind = ?", filter.Kind)
	}
	if filter.Status != "" {
		q = q.Where("status = ?", filter.Status)
	}
	if len(filter.Statuses) > 0 {
		q = q.Where("status IN ?", filter.Statuses)
	}
	if filter.Since != nil {
		q = q.Where("ts > ?", *filter.Since)
	}
	if filter.UserID != "" {
		q = q.Where("user_id = ?", filter.UserID)
	}
	if filter.ExcludeSource != "" {
		q = q.Where("(source IS NULL OR source <> ?)", filter.ExcludeSource)
	}
	if filter.Source != "" {
		q = q.Where("source = ?", filter.Source)
	}
	if filter.HasFile {
		q = q.Where("file <> ''")
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	var items []model.EventLog
	if err := q.Order("ts desc").
		Limit(filter.Limit).
		Offset(filter.Offset).
		Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

func (r *EventRepository) Stats(ctx context.Context) (*EventStats, error) {
	stats := &EventStats{}
	if err := r.db.WithContext(ctx).Model(&model.EventLog{}).Count(&stats.Total).Error; err != nil {
		return nil, err
	}
	if err := r.db.WithContext(ctx).Model(&model.EventLog{}).Where("status = ?", "success").Count(&stats.Success).Error; err != nil {
		return nil, err
	}
	if err := r.db.WithContext(ctx).Model(&model.EventLog{}).Where("status = ?", "failed").Count(&stats.Failed).Error; err != nil {
		return nil, err
	}
	if err := r.db.WithContext(ctx).Model(&model.EventLog{}).Where("status = ?", "pending").Count(&stats.Pending).Error; err != nil {
		return nil, err
	}

	type avgRow struct {
		Avg *float64 `gorm:"column:avg"`
	}
	var all avgRow
	if err := r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Select("AVG(elapsed_ms) AS avg").
		Where("status = ? AND elapsed_ms > 0", "success").
		Scan(&all).Error; err != nil {
		return nil, err
	}
	if all.Avg != nil {
		v := int(*all.Avg + 0.5)
		stats.AvgElapsedMS = &v
	}

	var recent avgRow
	cutoff := time.Now().Add(-24 * time.Hour)
	if err := r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Select("AVG(elapsed_ms) AS avg").
		Where("status = ? AND elapsed_ms > 0 AND ts >= ?", "success", cutoff).
		Scan(&recent).Error; err != nil {
		return nil, err
	}
	if recent.Avg != nil {
		v := int(*recent.Avg + 0.5)
		stats.AvgElapsedMS24 = &v
	}

	return stats, nil
}

// StatsByUser returns total / success / failed / pending counts scoped to a
// single user — for the customer-facing 生成日志 (/mylogs) KPI strip, so it
// reflects the caller's own history, not the whole site.
func (r *EventRepository) StatsByUser(ctx context.Context, userID string) (*EventStats, error) {
	stats := &EventStats{}
	q := func() *gorm.DB {
		return r.db.WithContext(ctx).Model(&model.EventLog{}).Where("user_id = ?", userID)
	}
	if err := q().Count(&stats.Total).Error; err != nil {
		return nil, err
	}
	if err := q().Where("status = ?", "success").Count(&stats.Success).Error; err != nil {
		return nil, err
	}
	if err := q().Where("status = ?", "failed").Count(&stats.Failed).Error; err != nil {
		return nil, err
	}
	if err := q().Where("status = ?", "pending").Count(&stats.Pending).Error; err != nil {
		return nil, err
	}
	return stats, nil
}

// ---------------------------------------------------------------------------
// Dashboard aggregates — server-side GROUP BY / FILTER so the admin overview
// no longer derives 7-day / DAU / trend / top-N numbers client-side from the
// last 200 logs (which silently undercounts once volume passes that window).
// ---------------------------------------------------------------------------

// DashboardWindow is a single time-window aggregate (e.g. last 24h / 7d).
type DashboardWindow struct {
	Total   int64   `json:"total"`
	Success int64   `json:"success"`
	Failed  int64   `json:"failed"`
	Pending int64   `json:"pending"`
	Image   int64   `json:"image"`
	Video   int64   `json:"video"`
	API     int64   `json:"api"` // source = 'v1' (OpenAI-compatible key)
	Web     int64   `json:"web"` // everything else (web / playground)
	Spent   float64 `json:"spent"`
}

type ModelUsage struct {
	Model string `json:"model"`
	Count int64  `json:"count"`
	AvgMS *int   `json:"avg_ms"`
}

type FailureReason struct {
	Reason string `json:"reason"`
	Count  int64  `json:"count"`
}

type UserSpend struct {
	UserID string  `json:"user_id"`
	Name   string  `json:"name"` // resolved by the service from user_id
	Count  int64   `json:"count"`
	Spent  float64 `json:"spent"`
}

type HourBucket struct {
	Image int64 `json:"image"`
	Video int64 `json:"video"`
}

// WindowStats rolls up counts + spend over a single window in one query.
func (r *EventRepository) WindowStats(ctx context.Context, since time.Time) (*DashboardWindow, error) {
	type row struct {
		Total   int64   `gorm:"column:total"`
		Success int64   `gorm:"column:success"`
		Failed  int64   `gorm:"column:failed"`
		Pending int64   `gorm:"column:pending"`
		Image   int64   `gorm:"column:image"`
		Video   int64   `gorm:"column:video"`
		API     int64   `gorm:"column:api"`
		Spent   float64 `gorm:"column:spent"`
	}
	var out row
	if err := r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Select(`
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE status = 'success') AS success,
			COUNT(*) FILTER (WHERE status = 'failed') AS failed,
			COUNT(*) FILTER (WHERE status = 'pending') AS pending,
			COUNT(*) FILTER (WHERE kind = 'image') AS image,
			COUNT(*) FILTER (WHERE kind = 'video') AS video,
			COUNT(*) FILTER (WHERE source = 'v1') AS api,
			COALESCE(SUM(cost) FILTER (WHERE status = 'success'), 0) AS spent`).
		Where("ts >= ?", since).
		Scan(&out).Error; err != nil {
		return nil, err
	}
	return &DashboardWindow{
		Total: out.Total, Success: out.Success, Failed: out.Failed, Pending: out.Pending,
		Image: out.Image, Video: out.Video, API: out.API, Web: out.Total - out.API,
		Spent: out.Spent,
	}, nil
}

// CountBetween counts events in (start, end] — used for the prev-24h delta.
func (r *EventRepository) CountBetween(ctx context.Context, start, end time.Time) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&model.EventLog{}).
		Where("ts > ? AND ts <= ?", start, end).Count(&n).Error
	return n, err
}

// DistinctUsersSince counts distinct (non-empty) user_ids active since `since`.
func (r *EventRepository) DistinctUsersSince(ctx context.Context, since time.Time) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Model(&model.EventLog{}).
		Where("ts >= ? AND user_id <> ''", since).
		Distinct("user_id").Count(&n).Error
	return n, err
}

// HourlyBuckets returns 24 oldest→newest buckets (image/video split) for the
// last 24h trend chart.
func (r *EventRepository) HourlyBuckets(ctx context.Context) ([24]HourBucket, error) {
	var out [24]HourBucket
	type hourRow struct {
		HoursAgo int   `gorm:"column:hours_ago"`
		Image    int64 `gorm:"column:image"`
		Video    int64 `gorm:"column:video"`
	}
	var rows []hourRow
	if err := r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Select(`
			FLOOR(EXTRACT(EPOCH FROM (NOW() - ts)) / 3600)::int AS hours_ago,
			COUNT(*) FILTER (WHERE kind = 'video') AS video,
			COUNT(*) FILTER (WHERE kind <> 'video') AS image`).
		Where("ts >= NOW() - INTERVAL '24 hours'").
		Group("hours_ago").
		Scan(&rows).Error; err != nil {
		return out, err
	}
	for _, hr := range rows {
		if hr.HoursAgo < 0 || hr.HoursAgo >= 24 {
			continue
		}
		out[23-hr.HoursAgo] = HourBucket{Image: hr.Image, Video: hr.Video}
	}
	return out, nil
}

// ModelUsageSince returns the top models by volume since `since`, with the
// success-only average latency.
func (r *EventRepository) ModelUsageSince(ctx context.Context, since time.Time, limit int) ([]ModelUsage, error) {
	if limit <= 0 {
		limit = 6
	}
	type row struct {
		Model string   `gorm:"column:model"`
		Count int64    `gorm:"column:count"`
		Avg   *float64 `gorm:"column:avg_ms"`
	}
	var rows []row
	if err := r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Select(`
			model,
			COUNT(*) AS count,
			AVG(elapsed_ms) FILTER (WHERE status = 'success' AND elapsed_ms > 0) AS avg_ms`).
		Where("ts >= ? AND model <> ''", since).
		Group("model").
		Order("count DESC").
		Limit(limit).
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]ModelUsage, 0, len(rows))
	for _, item := range rows {
		var avg *int
		if item.Avg != nil {
			v := int(*item.Avg + 0.5)
			avg = &v
		}
		out = append(out, ModelUsage{Model: item.Model, Count: item.Count, AvgMS: avg})
	}
	return out, nil
}

// TopFailures groups failed events by (truncated) error reason since `since`.
func (r *EventRepository) TopFailures(ctx context.Context, since time.Time, limit int) ([]FailureReason, error) {
	if limit <= 0 {
		limit = 5
	}
	var out []FailureReason
	if err := r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Select(`
			LEFT(COALESCE(NULLIF(error, ''), '未知错误'), 60) AS reason,
			COUNT(*) AS count`).
		Where("ts >= ? AND status = 'failed'", since).
		Group("reason").
		Order("count DESC").
		Limit(limit).
		Scan(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

// TopUserSpend ranks users by credits spent on SUCCESSFUL generations since
// `since`. Names are resolved by the caller (UserID -> display name).
func (r *EventRepository) TopUserSpend(ctx context.Context, since time.Time, limit int) ([]UserSpend, error) {
	if limit <= 0 {
		limit = 6
	}
	var out []UserSpend
	if err := r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Select(`
			user_id,
			COUNT(*) AS count,
			COALESCE(SUM(cost), 0) AS spent`).
		Where("ts >= ? AND status = 'success'", since).
		Group("user_id").
		Order("spent DESC").
		Limit(limit).
		Scan(&out).Error; err != nil {
		return nil, err
	}
	return out, nil
}

func (r *EventRepository) PurgeOlderThan(ctx context.Context, maxAge time.Duration) (int64, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-maxAge)
	result := r.db.WithContext(ctx).Where("ts < ?", cutoff).Delete(&model.EventLog{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// ClearFiles blanks the `file` column on any event rows that point at one of the
// given relative paths. Called after media retention deletes the files on disk so
// the log views don't dangle a 404 image — an emptied `file` reads as "no preview"
// ("—" in admin logs; hidden in the customer records page).
func (r *EventRepository) ClearFiles(ctx context.Context, relPaths []string) (int64, error) {
	if len(relPaths) == 0 {
		return 0, nil
	}
	result := r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Where("file IN ?", relPaths).
		Updates(map[string]any{"file": "", "updated_at": time.Now()})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// ClearRefFiles blanks the ref_files paths on one event (called after a
// successful generation once the reference images are deleted from storage, so no
// dangling reference_urls remain). The `refs` COUNT is kept for the log record.
func (r *EventRepository) ClearRefFiles(ctx context.Context, eventID string) error {
	return r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Where("id = ?", eventID).
		Update("ref_files", nil).Error
}

// StaleEvent identifies a purged pending event so the caller can refund the
// credits debited up-front AND attribute the failure to the account the
// (now-abandoned) generation was using.
type StaleEvent struct {
	ID        string  `gorm:"column:id"`
	UserID    string  `gorm:"column:user_id"`
	AccountID string  `gorm:"column:account_id"`
	Cost      float64 `gorm:"column:cost"`
}

// PurgeStale marks long-pending entries as failed/abandoned and RETURNS them so
// the caller can refund their up-front charge. A stuck pending row otherwise
// blocks the per-user generation gate (PendingByUser) forever AND silently eats
// the user's credits (the charge happens at submit; the normal failure-refund
// path never runs for a process-restart orphan). Mirrors Python purge_stale.
func (r *EventRepository) PurgeStale(ctx context.Context, maxAge time.Duration) ([]StaleEvent, error) {
	if maxAge <= 0 {
		maxAge = 600 * time.Second
	}
	cutoff := time.Now().Add(-maxAge)
	var stale []StaleEvent
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Snapshot who/what to refund BEFORE flipping status, so a concurrent
		// sweep can't double-count (the UPDATE in the same tx removes them from
		// the pending set).
		if err := tx.Model(&model.EventLog{}).
			Where("status = ? AND ts < ?", "pending", cutoff).
			Select("id", "user_id", "account_id", "cost").
			Scan(&stale).Error; err != nil {
			return err
		}
		if len(stale) == 0 {
			return nil
		}
		return tx.Model(&model.EventLog{}).
			Where("status = ? AND ts < ?", "pending", cutoff).
			Updates(map[string]any{
				"status":     "failed",
				"error":      gorm.Expr("COALESCE(NULLIF(error, ''), ?)", "abandoned (process restarted or request interrupted)"),
				"updated_at": time.Now(),
			}).Error
	})
	if err != nil {
		return nil, err
	}
	return stale, nil
}

func (r *EventRepository) Create(ctx context.Context, item *model.EventLog) error {
	if err := r.db.WithContext(ctx).Create(item).Error; err != nil {
		return err
	}
	// Persistent cumulative counters (survive log retention/clearing): every
	// created event bumps total + its kind + (api source).
	deltas := map[string]int64{"total": 1}
	if item.Kind == "video" {
		deltas["video"] = 1
	} else if item.Kind == "image" {
		deltas["image"] = 1
	}
	if item.Source == "v1" {
		deltas["api"] = 1
	}
	r.incrCounters(ctx, deltas)
	return nil
}

// incrCounters upserts monotonic counters (stat_counters). Best-effort: a counter
// failure must never fail the generation, so errors are swallowed.
func (r *EventRepository) incrCounters(ctx context.Context, deltas map[string]int64) {
	for k, n := range deltas {
		if n == 0 {
			continue
		}
		_ = r.db.WithContext(ctx).Exec(
			`INSERT INTO stat_counters (key, value, updated_at) VALUES (?, ?, now())
			 ON CONFLICT (key) DO UPDATE SET value = stat_counters.value + EXCLUDED.value, updated_at = now()`,
			k, n).Error
	}
}

// Counters returns all persistent counters as key→value.
func (r *EventRepository) Counters(ctx context.Context) (map[string]int64, error) {
	var rows []model.StatCounter
	if err := r.db.WithContext(ctx).Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, x := range rows {
		out[x.Key] = x.Value
	}
	return out, nil
}

// GetByID fetches a single event (nil, nil when not found). Used by the async
// /v1/videos job to look up status / the stored upstream URL.
func (r *EventRepository) GetByID(ctx context.Context, id string) (*model.EventLog, error) {
	var e model.EventLog
	if err := r.db.WithContext(ctx).First(&e, "id = ?", id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &e, nil
}

// MarkVideoReady completes an async video job: status=success, file=upstream URL
// (proxied on /content — never persisted), elapsed.
func (r *EventRepository) MarkVideoReady(ctx context.Context, eventID, fileURL string, elapsedMS int) error {
	// Guard on a real transition (status <> success) so the success counter is
	// incremented exactly once even if this fires twice / concurrently.
	res := r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Where("id = ? AND status <> ?", eventID, "success").
		Updates(map[string]any{
			"status":     "success",
			"file":       fileURL,
			"error":      "",
			"elapsed_ms": elapsedMS,
			"updated_at": time.Now(),
		})
	if res.Error == nil && res.RowsAffected > 0 {
		r.incrCounters(ctx, map[string]int64{"success": 1})
	}
	return res.Error
}

func (r *EventRepository) UpdateStatus(ctx context.Context, eventID, status, errMsg string, elapsedMS int) error {
	patch := map[string]any{
		"status":     status,
		"elapsed_ms": elapsedMS,
		"updated_at": time.Now(),
	}
	if strings.TrimSpace(errMsg) != "" {
		patch["error"] = strings.TrimSpace(errMsg)
	} else if status == "success" {
		// A late-completing generation (one the maintenance sweep had already
		// stamped "abandoned") must shed that stale error, or the row reads as
		// "成功 + abandoned" at once.
		patch["error"] = ""
	}
	// Guard on a real transition so the success/failed counters increment exactly
	// once per event even under a duplicate/concurrent terminal status update.
	res := r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Where("id = ? AND status <> ?", eventID, status).
		Updates(patch)
	if res.Error == nil && res.RowsAffected > 0 {
		if status == "success" {
			r.incrCounters(ctx, map[string]int64{"success": 1})
		} else if status == "failed" {
			r.incrCounters(ctx, map[string]int64{"failed": 1})
		}
	}
	return res.Error
}

// MarkRefunded atomically claims the right to refund this event exactly once:
// it flips refunded false→true and returns true ONLY for the caller that won the
// race. Both the normal failure path and the abandoned-purge sweep call this
// before crediting, so a generation can never be refunded twice.
func (r *EventRepository) MarkRefunded(ctx context.Context, eventID string) (bool, error) {
	res := r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Where("id = ? AND refunded = ?", eventID, false).
		Updates(map[string]any{"refunded": true, "updated_at": time.Now()})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected == 1, nil
}

// SetAccount stamps which provider account is fulfilling an in-flight event.
// Called when generation commits to a token, so the accounts view can count
// pending events per account and an abandoned-event purge can attribute back.
func (r *EventRepository) SetAccount(ctx context.Context, eventID, accountID string) error {
	return r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Where("id = ?", eventID).
		Update("account_id", accountID).Error
}

// InFlightByAccount counts pending (in-flight) events grouped by account_id, for
// the accounts view's live "in-flight" column.
func (r *EventRepository) InFlightByAccount(ctx context.Context) (map[string]int64, error) {
	type row struct {
		AccountID string `gorm:"column:account_id"`
		Count     int64  `gorm:"column:count"`
	}
	var rows []row
	if err := r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Select("account_id, COUNT(*) AS count").
		Where("status = ? AND account_id <> ''", "pending").
		Group("account_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, item := range rows {
		out[item.AccountID] = item.Count
	}
	return out, nil
}

func (r *EventRepository) RecentByFile(ctx context.Context, limit int) ([]model.EventLog, error) {
	if limit <= 0 {
		limit = 1000
	}
	var items []model.EventLog
	if err := r.db.WithContext(ctx).
		Where("file <> ''").
		Order("ts desc").
		Limit(limit).
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (r *EventRepository) ModelSuccessCounts(ctx context.Context) (map[string]int64, error) {
	type row struct {
		Model string `gorm:"column:model"`
		Count int64  `gorm:"column:count"`
	}
	var rows []row
	if err := r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Select("model, COUNT(*) AS count").
		Where("status = ? AND model <> ''", "success").
		Group("model").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, item := range rows {
		out[item.Model] = item.Count
	}
	return out, nil
}

func (r *EventRepository) UserSuccessCounts(ctx context.Context) (map[string]int64, error) {
	type row struct {
		UserID string `gorm:"column:user_id"`
		Count  int64  `gorm:"column:count"`
	}
	var rows []row
	if err := r.db.WithContext(ctx).
		Model(&model.EventLog{}).
		Select("user_id, COUNT(*) AS count").
		Where("status = ? AND user_id <> ''", "success").
		Group("user_id").
		Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, item := range rows {
		out[item.UserID] = item.Count
	}
	return out, nil
}

func (r *EventRepository) DeleteAll(ctx context.Context) (int64, error) {
	result := r.db.WithContext(ctx).Where("1 = 1").Delete(&model.EventLog{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

func (r *EventRepository) DeletePending(ctx context.Context) (int64, error) {
	result := r.db.WithContext(ctx).Where("status = ?", "pending").Delete(&model.EventLog{})
	if result.Error != nil {
		return 0, result.Error
	}
	return result.RowsAffected, nil
}

// LatestByUser / PendingByUser take onlySource: when non-empty they match ONLY
// that source. The playground passes "user" so it echoes ONLY the user's own
// web generations — never admin model-tests ("admin") or API-key calls ("v1").
func (r *EventRepository) LatestByUser(ctx context.Context, userID, onlySource string) (*model.EventLog, error) {
	var item model.EventLog
	q := r.db.WithContext(ctx).Model(&model.EventLog{}).Where("user_id = ?", userID)
	if strings.TrimSpace(onlySource) != "" {
		q = q.Where("source = ?", strings.TrimSpace(onlySource))
	}
	if err := q.Order("ts desc").First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &item, nil
}

func (r *EventRepository) PendingByUser(ctx context.Context, userID, onlySource string) (*model.EventLog, error) {
	var item model.EventLog
	q := r.db.WithContext(ctx).Model(&model.EventLog{}).
		Where("user_id = ? AND status = ?", userID, "pending")
	if strings.TrimSpace(onlySource) != "" {
		q = q.Where("source = ?", strings.TrimSpace(onlySource))
	}
	if err := q.Order("ts desc").First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	return &item, nil
}
