package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"backend/internal/model"
	"backend/internal/provider/adobe"
	"backend/internal/repo"
	"gorm.io/datatypes"
)

type RefreshProfileService struct {
	profiles *repo.RefreshProfileRepository
	tokens   *repo.TokenRepository
	adobe    *adobe.Client
}

func NewRefreshProfileService(profiles *repo.RefreshProfileRepository, tokens *repo.TokenRepository, adobeClient *adobe.Client) *RefreshProfileService {
	return &RefreshProfileService{
		profiles: profiles,
		tokens:   tokens,
		adobe:    adobeClient,
	}
}

func (s *RefreshProfileService) SetProxy(proxy string) {
	if s.adobe != nil {
		s.adobe.SetProxy(proxy)
	}
}

func (s *RefreshProfileService) List(ctx context.Context) ([]model.RefreshProfile, error) {
	return s.profiles.List(ctx)
}

func (s *RefreshProfileService) RefreshNow(ctx context.Context, id string) error {
	if s.adobe == nil || s.tokens == nil {
		return errors.New("refresh client not configured")
	}
	profile, err := s.profiles.Get(ctx, id)
	if err != nil {
		return err
	}
	if profile.Pool != "adobe" || profile.Kind != "adobe_cookie" {
		return errors.New("unsupported refresh profile")
	}

	now := time.Now()
	_, _ = s.profiles.Update(ctx, id, map[string]any{
		"last_attempt_at": now,
	})

	result, err := s.adobe.ExchangeCookie(ctx, profile.Cookie)
	if err != nil {
		failures := profile.ConsecutiveFailures + 1
		// Exponential backoff: 60s per consecutive failure, capped at 1h.
		secs := 60 * failures
		if secs > 3600 {
			secs = 3600
		}
		msg := err.Error()
		if len(msg) > 300 {
			msg = msg[:300]
		}
		_, _ = s.profiles.Update(ctx, id, map[string]any{
			"last_error":           msg,
			"consecutive_failures": failures,
			"next_retry_at":        now.Add(time.Duration(secs) * time.Second),
		})
		// Mark the token dead when the cookie exchange keeps failing:
		//  - 5 consecutive failures → cookie is likely expired/revoked
		//  - ride_AdobeID_acct_actreq (Adobe requires account action) → permanently broken
		if failures >= 5 || strings.Contains(msg, "ride_AdobeID_acct_actreq") {
			_, _ = s.tokens.Update(ctx, "adobe", id, map[string]any{
				"status": "disabled",
				"dead":   true,
			})
		}
		return err
	}

	tokenPatch := map[string]any{
		"value":      result.AccessToken,
		"status":     "active",
		"dead":       false,
		"fails":      0,
		"updated_at": now,
	}
	email, _ := parseJWTEmailExpiry(result.AccessToken)
	if email != "" {
		tokenPatch["account_email"] = email
	}
	if profile.IntervalSeconds > 0 {
		tokenPatch["cached_quota_reset_after"] = now.Add(time.Duration(profile.IntervalSeconds) * time.Second).Format(time.RFC3339)
	} else {
		tokenPatch["cached_quota_reset_after"] = now.Add(54000 * time.Second).Format(time.RFC3339)
	}
	if profileData, profileErr := s.adobe.FetchAccountProfile(ctx, result.AccessToken); profileErr == nil {
		if email := strings.TrimSpace(stringValue(profileData["email"])); email != "" {
			tokenPatch["account_email"] = email
		}
		if displayName := strings.TrimSpace(stringValue(profileData["display_name"])); displayName != "" {
			tokenPatch["account_display_name"] = displayName
		}
	}
	planKnown := false
	if quotaData, quotaErr := s.adobe.FetchCreditsBalance(ctx, result.AccessToken); quotaErr == nil {
		meta := datatypes.JSONMap{
			"cached_quota_at": int(time.Now().Unix()),
		}
		if remaining, ok := quotaData["remaining"].(int); ok {
			meta["cached_quota_remaining"] = remaining
		}
		if used, ok := quotaData["used"].(int); ok {
			meta["cached_quota_used"] = used
		}
		if total, ok := quotaData["total"].(int); ok {
			meta["cached_quota_total"] = total
		}
		tokenPatch["meta"] = meta

		planCap := strings.ToLower(strings.TrimSpace(stringValue(quotaData["plan"])))
		meta["plan"] = planCap
		isVIP := planCap != "" && !strings.EqualFold(planCap, "free")
		planKnown = planCap != ""

		// VIP: use Adobe's reset time; set concurrency 5.
		// 母号/子号 身份固定，不随积分动态变化——只有降级普号才刷新身份。
		if isVIP {
			if resetAfter := strings.TrimSpace(stringValue(quotaData["available_until"])); resetAfter != "" {
				tokenPatch["cached_quota_reset_after"] = resetAfter
			}
			tokenPatch["concurrency"] = 5
		} else {
			// Free account: concurrency 1, limit image+video
			tokenPatch["concurrency"] = 1
			tokenPatch["image_limited"] = true
			tokenPatch["video_limited"] = true
			meta["is_sub_account"] = false
		}
	}
	// 探测不到会员身份（credits 接口失败或没返回 plan）的号既不算普号也不算会员号，
	// 留在池里只会在需要会员的请求上撞 403 —— 直接置死号。
	if !planKnown {
		tokenPatch["status"] = "disabled"
		tokenPatch["dead"] = true
	}
	if _, err := s.tokens.Update(ctx, "adobe", id, tokenPatch); err != nil {
		return err
	}

	interval := profile.IntervalSeconds
	if interval <= 0 {
		interval = 54000
	}
	_, err = s.profiles.Update(ctx, id, map[string]any{
		"last_success_at":      now,
		"next_retry_at":        now.Add(time.Duration(interval) * time.Second),
		"last_error":           "",
		"consecutive_failures": 0,
	})
	return err
}

// RefreshDue refreshes every enabled profile whose next_retry_at has passed.
// Driven by the background maintenance loop so Adobe cookies auto-renew without
// an admin clicking "refresh". Individual failures are recorded on the profile
// (backoff + dead escalation) and don't abort the sweep.
func (s *RefreshProfileService) RefreshDue(ctx context.Context) (int, error) {
	if s.adobe == nil || s.tokens == nil {
		return 0, nil
	}
	due, err := s.profiles.ListDue(ctx, time.Now())
	if err != nil {
		return 0, err
	}
	refreshed := 0
	for _, p := range due {
		if p.Pool != "adobe" || p.Kind != "adobe_cookie" {
			continue
		}
		if err := s.RefreshNow(ctx, p.ID); err != nil {
			continue
		}
		refreshed++
	}
	return refreshed, nil
}

func (s *RefreshProfileService) Update(ctx context.Context, id string, body map[string]any) (*model.RefreshProfile, error) {
	patch := map[string]any{}
	if raw, ok := body["enabled"]; ok {
		patch["enabled"] = boolValueWithDefault(raw, false)
	}
	if raw, ok := body["name"]; ok {
		patch["name"] = stringValue(raw)
	}
	if raw, ok := body["interval_seconds"]; ok {
		n := intValue(raw)
		if n <= 0 {
			return nil, errors.New("interval_seconds must be positive")
		}
		patch["interval_seconds"] = n
	}
	if len(patch) == 0 {
		return s.profiles.Get(ctx, id)
	}
	return s.profiles.Update(ctx, id, patch)
}

func (s *RefreshProfileService) Delete(ctx context.Context, id string) error {
	return s.profiles.Delete(ctx, id)
}
