package service

import (
	"testing"
	"time"

	"gorm.io/datatypes"
)

func TestLeonardoRefreshDue(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	tests := []struct {
		name string
		meta datatypes.JSONMap
		want bool
	}{
		{name: "never attempted", meta: nil, want: true},
		{name: "next refresh in future", meta: datatypes.JSONMap{"session_refresh_next_at": int(now.Add(time.Minute).Unix())}, want: false},
		{name: "next refresh now", meta: datatypes.JSONMap{"session_refresh_next_at": int(now.Unix())}, want: true},
		{name: "next refresh overdue", meta: datatypes.JSONMap{"session_refresh_next_at": int(now.Add(-time.Minute).Unix())}, want: true},
		{name: "legacy long schedule migrates", meta: datatypes.JSONMap{"session_refresh_next_at": int(now.Add(40 * time.Minute).Unix())}, want: true},
		{name: "recent failed attempt", meta: datatypes.JSONMap{"session_refresh_attempted_at": int(now.Add(-time.Minute).Unix())}, want: false},
		{name: "failed retry due", meta: datatypes.JSONMap{"session_refresh_attempted_at": int(now.Add(-leonardoKeepaliveInterval).Unix())}, want: true},
		{
			name: "legacy success but recent failure",
			meta: datatypes.JSONMap{
				"session_refreshed_at":         int(now.Add(-24 * time.Hour).Unix()),
				"session_refresh_attempted_at": int(now.Add(-time.Minute).Unix()),
			},
			want: false,
		},
		{
			name: "overdue but recent failure",
			meta: datatypes.JSONMap{
				"session_refresh_next_at":      int(now.Add(-time.Minute).Unix()),
				"session_refresh_attempted_at": int(now.Add(-time.Minute).Unix()),
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := leonardoRefreshDue(tt.meta, now); got != tt.want {
				t.Fatalf("leonardoRefreshDue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLeonardoNextRefreshAtRange(t *testing.T) {
	now := time.Unix(2_000_000_000, 0)
	if delay := leonardoNextRefreshAt(now).Sub(now); delay != leonardoKeepaliveInterval {
		t.Fatalf("leonardoNextRefreshAt() delay = %v, want %v", delay, leonardoKeepaliveInterval)
	}
}
