package repo

import (
	"testing"
	"time"
)

func TestDecorateAccountStatePatchRecordsAndClearsAbnormalState(t *testing.T) {
	now := time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)
	fatal := map[string]any{
		"status":     "disabled",
		"dead":       true,
		"last_error": "Leonardo 定时保活失败：get-session 返回 HTTP 401",
	}
	decorateAccountStatePatch(fatal, now)
	if fatal["last_error_at"] == nil {
		t.Fatal("automatic disable did not receive last_error_at")
	}
	if got := fatal["last_error"]; got != "Leonardo 定时保活失败：get-session 返回 HTTP 401" {
		t.Fatalf("last_error = %v", got)
	}

	active := map[string]any{"status": "active", "dead": false}
	decorateAccountStatePatch(active, now)
	if got := active["last_error"]; got != "" {
		t.Fatalf("reactivation last_error = %v, want empty", got)
	}
	if got := active["last_error_at"]; got != nil {
		t.Fatalf("reactivation last_error_at = %v, want nil", got)
	}
}

func TestDecorateAccountStatePatchAddsFallbackReason(t *testing.T) {
	patch := map[string]any{"status": "disabled", "dead": true}
	decorateAccountStatePatch(patch, time.Now())
	if got := valueString(patch["last_error"]); got == "" {
		t.Fatal("automatic disable did not receive a fallback reason")
	}
}
