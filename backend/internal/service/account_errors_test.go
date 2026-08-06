package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"backend/internal/model"
)

func TestAccountErrorMessageFlattensAndBoundsDetail(t *testing.T) {
	msg := accountErrorMessage("Leonardo 定时保活失败", errors.New("get-session\n返回 401"))
	if msg != "Leonardo 定时保活失败：get-session 返回 401" {
		t.Fatalf("accountErrorMessage() = %q", msg)
	}
	long := accountErrorMessage("upstream", errors.New(strings.Repeat("错", 600)))
	if got := len([]rune(long)); got > maxAccountErrorRunes {
		t.Fatalf("long error has %d runes, max %d", got, maxAccountErrorRunes)
	}
}

func TestAccountRowIncludesLastErrorAndTimestamp(t *testing.T) {
	at := time.Date(2026, 8, 6, 11, 22, 33, 0, time.UTC)
	row := accountRow(model.TokenAccount{
		ID:          "leonardo-test",
		Pool:        "leonardo",
		Status:      "disabled",
		Dead:        true,
		LastError:   "Leonardo 定时保活失败：get-session 返回 HTTP 401",
		LastErrorAt: &at,
	}, 0)
	if row["last_error"] != "Leonardo 定时保活失败：get-session 返回 HTTP 401" {
		t.Fatalf("last_error = %v", row["last_error"])
	}
	if row["last_error_at"] != at.Unix() {
		t.Fatalf("last_error_at = %v, want %d", row["last_error_at"], at.Unix())
	}
}
