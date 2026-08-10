package repo

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"backend/internal/model"
	"gorm.io/datatypes"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type TokenRepository struct {
	db *gorm.DB
}

func NewTokenRepository(db *gorm.DB) *TokenRepository {
	return &TokenRepository{db: db}
}

func (r *TokenRepository) List(ctx context.Context) ([]model.TokenAccount, error) {
	var items []model.TokenAccount
	if err := r.db.WithContext(ctx).
		Order("pool asc, created_at desc").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (r *TokenRepository) ListByPool(ctx context.Context, pool string) ([]model.TokenAccount, error) {
	var items []model.TokenAccount
	if err := r.db.WithContext(ctx).
		Where("pool = ?", pool).
		Order("created_at desc").
		Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

func (r *TokenRepository) Get(ctx context.Context, pool, id string) (*model.TokenAccount, error) {
	var item model.TokenAccount
	if err := r.db.WithContext(ctx).
		First(&item, "pool = ? AND id = ?", pool, id).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

// GetByPoolEmail finds an account in a pool by its account_email (the logical
// identity for import dedup). Returns (nil, nil) when none / email is blank.
func (r *TokenRepository) GetByPoolEmail(ctx context.Context, pool, email string) (*model.TokenAccount, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, nil
	}
	var item model.TokenAccount
	err := r.db.WithContext(ctx).
		Where("pool = ? AND account_email = ?", pool, email).
		First(&item).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *TokenRepository) Create(ctx context.Context, item *model.TokenAccount) error {
	return r.db.WithContext(ctx).Create(item).Error
}

func (r *TokenRepository) Update(ctx context.Context, pool, id string, patch map[string]any) (*model.TokenAccount, error) {
	patch["updated_at"] = time.Now()
	if err := r.db.WithContext(ctx).
		Model(&model.TokenAccount{}).
		Where("pool = ? AND id = ?", pool, id).
		Updates(patch).Error; err != nil {
		return nil, err
	}
	return r.Get(ctx, pool, id)
}

// SwapValue replaces an account's credential only while the stored one is still
// the value the caller started from. A rotating cookie is minted from whatever
// was in the row, so a goroutine that has been holding an older copy (a long
// render, a slow quota probe) must NOT be allowed to write it back over a newer
// rotation — that older copy no longer authenticates, and the account then looks
// dead. Reports whether the row was updated.
func (r *TokenRepository) SwapValue(ctx context.Context, pool, id, from, to string) (bool, error) {
	res := r.db.WithContext(ctx).
		Model(&model.TokenAccount{}).
		Where("pool = ? AND id = ? AND value = ?", pool, id, from).
		Updates(map[string]any{"value": to, "updated_at": time.Now()})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// ReserveQuota atomically pre-deducts `amount` from an account's cached image
// token balance under a row lock, so concurrent picks of the same near-empty
// account can never over-commit it. Returns:
//   - allowed=true, deducted=true: balance was known and ≥ amount → decremented.
//   - allowed=true, deducted=false: balance unknown → allowed without a hold
//     (benefit of the doubt; a post-render reconcile writes the real value).
//   - allowed=false: balance known and < amount → caller should fail over.
// RefundQuota releases a hold made with deducted=true when the render fails.
func (r *TokenRepository) ReserveQuota(ctx context.Context, pool, id string, amount int) (allowed, deducted bool, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item model.TokenAccount
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&item, "pool = ? AND id = ?", pool, id).Error; e != nil {
			return e
		}
		rem, known := metaInt(item.Meta, "cached_quota_remaining")
		if !known {
			allowed, deducted = true, false
			return nil
		}
		if rem < amount {
			allowed, deducted = false, false
			return nil
		}
		meta := cloneMeta(item.Meta)
		meta["cached_quota_remaining"] = rem - amount
		if e := tx.Model(&model.TokenAccount{}).
			Where("pool = ? AND id = ?", pool, id).
			Updates(map[string]any{"meta": meta, "updated_at": time.Now()}).Error; e != nil {
			return e
		}
		allowed, deducted = true, true
		return nil
	})
	return allowed, deducted, err
}

// Grok accounts carry a forced local quota instead of an upstream balance:
// Console 没有额度接口，所以导入时写死 图 5 / 视频 2，下单预扣、失败退回，两个都归零直接判死。
const (
	GrokImageQuotaKey = "grok_image_remaining"
	GrokVideoQuotaKey = "grok_video_remaining"
	GrokImageQuota    = 5
	GrokVideoQuota    = 2
)

// grokQuotas reads an account's local per-kind counters, falling back to the
// forced defaults for accounts imported before the counters existed.
func grokQuotas(meta datatypes.JSONMap) (images, videos int) {
	images, known := metaInt(meta, GrokImageQuotaKey)
	if !known {
		images = GrokImageQuota
	}
	videos, known = metaInt(meta, GrokVideoQuotaKey)
	if !known {
		videos = GrokVideoQuota
	}
	return images, videos
}

// ReserveGrokQuota pre-deducts one unit of a grok account's local per-kind quota
// under a row lock, so concurrent orders on the same near-empty account can't
// over-commit it（下单即扣，不会超扣）。allowed=false 表示这一份已经用光，调用方换号。
// 归零的那一份打上 image_limited / video_limited 让调度跳过；判死留给
// FinalizeGrokQuota（生成真的成功了才锁号），失败时用 RefundGrokQuota 退回。
func (r *TokenRepository) ReserveGrokQuota(ctx context.Context, id, kind string) (allowed bool, err error) {
	err = r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item model.TokenAccount
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&item, "pool = ? AND id = ?", "grok", id).Error; e != nil {
			return e
		}
		images, videos := grokQuotas(item.Meta)
		if kind == "video" {
			if videos <= 0 {
				return nil
			}
			videos--
		} else {
			if images <= 0 {
				return nil
			}
			images--
		}
		meta := cloneMeta(item.Meta)
		meta[GrokImageQuotaKey] = images
		meta[GrokVideoQuotaKey] = videos
		if e := tx.Model(&model.TokenAccount{}).
			Where("pool = ? AND id = ?", "grok", id).
			Updates(map[string]any{
				"meta":          meta,
				"image_limited": images <= 0,
				"video_limited": videos <= 0,
				"updated_at":    time.Now(),
			}).Error; e != nil {
			return e
		}
		allowed = true
		return nil
	})
	return allowed, err
}

// RefundGrokQuota gives back a unit reserved by ReserveGrokQuota when the render
// failed, clearing that kind's limited flag again. Capped at the forced default so
// repeated refunds can't inflate the quota.
func (r *TokenRepository) RefundGrokQuota(ctx context.Context, id, kind string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item model.TokenAccount
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&item, "pool = ? AND id = ?", "grok", id).Error; e != nil {
			return e
		}
		images, videos := grokQuotas(item.Meta)
		if kind == "video" {
			videos = min(videos+1, GrokVideoQuota)
		} else {
			images = min(images+1, GrokImageQuota)
		}
		meta := cloneMeta(item.Meta)
		meta[GrokImageQuotaKey] = images
		meta[GrokVideoQuotaKey] = videos
		return tx.Model(&model.TokenAccount{}).
			Where("pool = ? AND id = ?", "grok", id).
			Updates(map[string]any{
				"meta":          meta,
				"image_limited": images <= 0,
				"video_limited": videos <= 0,
				"updated_at":    time.Now(),
			}).Error
	})
}

// FinalizeGrokQuota locks an account down once a successful generation left both
// local counters at zero (no reset time — 用完就废).
func (r *TokenRepository) FinalizeGrokQuota(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item model.TokenAccount
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&item, "pool = ? AND id = ?", "grok", id).Error; e != nil {
			return e
		}
		images, videos := grokQuotas(item.Meta)
		if images > 0 || videos > 0 {
			return nil
		}
		return tx.Model(&model.TokenAccount{}).
			Where("pool = ? AND id = ?", "grok", id).
			Updates(map[string]any{
				"status":     "disabled",
				"dead":       true,
				"updated_at": time.Now(),
			}).Error
	})
}

// RefundQuota atomically adds `amount` back to cached_quota_remaining (releasing a
// hold from a reservation whose render then failed). No-op if the balance is
// unknown. Row-locked like ReserveQuota.
func (r *TokenRepository) RefundQuota(ctx context.Context, pool, id string, amount int) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var item model.TokenAccount
		if e := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			First(&item, "pool = ? AND id = ?", pool, id).Error; e != nil {
			return e
		}
		rem, known := metaInt(item.Meta, "cached_quota_remaining")
		if !known {
			return nil
		}
		meta := cloneMeta(item.Meta)
		meta["cached_quota_remaining"] = rem + amount
		return tx.Model(&model.TokenAccount{}).
			Where("pool = ? AND id = ?", pool, id).
			Updates(map[string]any{"meta": meta, "updated_at": time.Now()}).Error
	})
}

func cloneMeta(m datatypes.JSONMap) datatypes.JSONMap {
	out := datatypes.JSONMap{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func metaInt(m datatypes.JSONMap, key string) (int, bool) {
	if m == nil {
		return 0, false
	}
	v, ok := m[key]
	if !ok || v == nil {
		return 0, false
	}
	switch x := v.(type) {
	case int:
		return x, true
	case int64:
		return int(x), true
	case float64:
		return int(x), true
	case json.Number:
		n, e := x.Int64()
		if e != nil {
			return 0, false
		}
		return int(n), true
	case string:
		n, e := strconv.Atoi(strings.TrimSpace(x))
		if e != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}

// TouchLastUsed stamps last_used_at at the moment a token is SELECTED, so the
// accounts view reflects an accurate "last used" time. Rotation order is driven
// by the in-memory strict round-robin cursor in the service layer (see
// V1Service.rotateRoundRobin), not by this timestamp.
func (r *TokenRepository) TouchLastUsed(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).
		Model(&model.TokenAccount{}).
		Where("id = ?", id).
		Update("last_used_at", time.Now()).Error
}

// IncrementFail bumps an account's failure counters by one. Used to attribute
// an abandoned (purged) generation's failure back to the account it was using,
// since that generation never reached the normal markTokenFailure path.
func (r *TokenRepository) IncrementFail(ctx context.Context, id string) error {
	return r.db.WithContext(ctx).
		Model(&model.TokenAccount{}).
		Where("id = ?", id).
		Updates(map[string]any{
			"fail_total": gorm.Expr("fail_total + 1"),
			"fails":      gorm.Expr("fails + 1"),
			"updated_at": time.Now(),
		}).Error
}

func (r *TokenRepository) Delete(ctx context.Context, pool, id string) (int64, error) {
	res := r.db.WithContext(ctx).
		Delete(&model.TokenAccount{}, "pool = ? AND id = ?", pool, id)
	return res.RowsAffected, res.Error
}

// DeleteByIDs removes accounts by id across pools (ids are globally unique),
// for bulk delete. Returns the number of rows removed.
func (r *TokenRepository) DeleteByIDs(ctx context.Context, ids []string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := r.db.WithContext(ctx).Delete(&model.TokenAccount{}, "id IN ?", ids)
	return res.RowsAffected, res.Error
}

// leonardoDailyTokens is the free-tier daily allowance restored at each reset.
// A paid account's true balance is reconciled on its next successful render.
const leonardoDailyTokens = 150

// RecoverQuota reactivates quota-exhausted tokens whose reset time has passed.
// Reset source: cached_quota_reset_after (upstream marker) first, else the
// quota_recover_at fallback stamped when the token was marked quota-exhausted.
// Mirrors Python TokenPool.recover_quota; returns the count reactivated.
// RecoverQuota reactivates quota-exhausted tokens whose reset time has passed and
// returns the accounts it recovered, so the caller can re-sync their real balance
// (the providers only sync quota when accessed).
func (r *TokenRepository) RecoverQuota(ctx context.Context) ([]model.TokenAccount, error) {
	// Also pick up accounts that are only single-kind limited (image_limited /
	// video_limited) — those keep status "active" and would otherwise never have
	// their per-kind flag cleared. This is legacy support; Adobe no longer sets
	// per-kind limits.
	var items []model.TokenAccount
	if err := r.db.WithContext(ctx).
		Where("status = ? OR image_limited = ? OR video_limited = ?", "quota", true, true).
		Find(&items).Error; err != nil {
		return nil, err
	}
	now := time.Now()
	var recovered []model.TokenAccount
	for i := range items {
		t := &items[i]
		// Runway's reset marker is the JWT expiry, not a quota-refresh time, and
		// there's no way to refresh a bare JWT — so a runway account is never
		// "recovered"; it's expired-to-dead by ExpireByReset instead.
		if t.Pool == "runway" {
			continue
		}
		reset := parseResetMarker(t.CachedQuotaResetAfter)
		if reset == nil {
			reset = t.QuotaRecoverAt
		}
		if reset == nil || now.Before(*reset) {
			continue
		}
		patch := map[string]any{
			"fails":            0,
			"quota_recover_at": nil,
		}
		// For Adobe, also clear any legacy per-kind limit flags.
		if t.Pool == "adobe" {
			patch["image_limited"] = false
			patch["video_limited"] = false
		}
		// Only flip status back to active if it was sunk to "quota" (both kinds
		// limited); a single-kind limit left status untouched.
		if t.Status == "quota" {
			patch["status"] = "active"
		}
		// Leonardo's free tokens fully renew at each daily reset — restore the
		// balance and advance the reset marker to the next 08:00 Beijing (== next
		// UTC midnight), so the account is immediately usable instead of stuck at a
		// stale 0. A paid account's real balance is corrected on its next render.
		if t.Pool == "leonardo" || t.Pool == "krea" || t.Pool == "imagine" {
			meta := cloneMeta(t.Meta)
			if t.Pool == "leonardo" {
				meta["cached_quota_remaining"] = leonardoDailyTokens
			} else {
				// Krea/Imagine balances re-sync from upstream (billing-data / v1/credit)
				// on next probe — drop the stale value so the account isn't shown as
				// empty after reset.
				delete(meta, "cached_quota_remaining")
			}
			meta["cached_quota_at"] = int(now.Unix())
			patch["meta"] = meta
			patch["cached_quota_reset_after"] = time.Unix((now.Unix()/86400+1)*86400, 0).UTC().Format(time.RFC3339)
		}
		if _, err := r.Update(ctx, t.Pool, t.ID, patch); err != nil {
			return recovered, err
		}
		recovered = append(recovered, *t)
	}
	return recovered, nil
}

// RollResetMarkers advances a stale (past) daily-reset marker to its next future
// occurrence — same time-of-day, +N whole days — for ACTIVE accounts of the given
// daily-reset pools, so the 恢复时间 column always shows the upcoming reset rather
// than yesterday's. Only active accounts are rolled: a 限额 account must keep its
// past marker so RecoverQuota can recover it (rolling it forward early would
// prevent recovery). Returns the number advanced.
func (r *TokenRepository) RollResetMarkers(ctx context.Context, pools []string) (int, error) {
	var items []model.TokenAccount
	if err := r.db.WithContext(ctx).
		Where("pool IN ? AND dead = ? AND status = ? AND image_limited = ? AND video_limited = ? AND cached_quota_reset_after <> ''",
			pools, false, "active", false, false).
		Find(&items).Error; err != nil {
		return 0, err
	}
	now := time.Now()
	n := 0
	for i := range items {
		t := &items[i]
		reset := parseResetMarker(t.CachedQuotaResetAfter)
		if reset == nil || !reset.Before(now) {
			continue // unparseable or already in the future
		}
		next := *reset
		for !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		if _, err := r.Update(ctx, t.Pool, t.ID, map[string]any{
			"cached_quota_reset_after": next.UTC().Format(time.RFC3339),
		}); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ExpireByReset marks accounts of a pool dead once their reset marker has passed.
// For runway the marker IS the JWT expiry and there's no refresh, so an expired
// token can only 401 — we proactively flip it to disabled+dead (the same end
// state a 401 would produce) instead of leaving a doomed account "active".
func (r *TokenRepository) ExpireByReset(ctx context.Context, pool string) (int, error) {
	var items []model.TokenAccount
	if err := r.db.WithContext(ctx).
		Where("pool = ? AND dead = ?", pool, false).
		Find(&items).Error; err != nil {
		return 0, err
	}
	now := time.Now()
	expired := 0
	for i := range items {
		t := &items[i]
		reset := parseResetMarker(t.CachedQuotaResetAfter)
		if reset == nil || now.Before(*reset) {
			continue
		}
		if _, err := r.Update(ctx, t.Pool, t.ID, map[string]any{
			"status": "disabled",
			"dead":   true,
		}); err != nil {
			return expired, err
		}
		expired++
	}
	return expired, nil
}

// parseResetMarker best-effort parses a quota reset marker into a time. Accepts
// epoch seconds (numeric string) or ISO-8601 (e.g. Adobe's available_until
// "2026-06-16T23:59:59.999Z"). Returns nil if unparseable.
func parseResetMarker(v string) *time.Time {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	if f, err := strconv.ParseFloat(v, 64); err == nil && f > 946684800 {
		t := time.Unix(int64(f), 0)
		return &t
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05.999Z07:00", "2006-01-02T15:04:05Z07:00"} {
		if t, err := time.Parse(layout, v); err == nil {
			return &t
		}
	}
	return nil
}
