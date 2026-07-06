package model

import (
	"strings"
	"time"

	"gorm.io/datatypes"
)

type User struct {
	ID                 string  `gorm:"primaryKey;size:32"`
	Email              string  `gorm:"size:255;uniqueIndex;not null"`
	Name               string  `gorm:"size:255"`
	PasswordHash       string  `gorm:"size:255"`
	Role               string  `gorm:"size:32;index;not null"`
	Status             string  `gorm:"size:32;index;not null"`
	Credits            float64 `gorm:"not null;default:0"`
	Notes              string  `gorm:"type:text"`
	ConcurrencyGroupID string  `gorm:"size:32;index"`
	AnnouncementSeen   string  `gorm:"size:32"`            // version hash of the last announcement this user dismissed
	RechargeTotal      float64 `gorm:"not null;default:0"` // 累计充值金额(元)
	InviteCode         string  `gorm:"size:32;uniqueIndex"`
	InvitedBy          *string `gorm:"size:32;index"`
	InviteRewardDone   bool    `gorm:"not null;default:false"`
	InviteRewardAt     *time.Time
	CheckinLast        string `gorm:"size:32"`
	CheckinStreak      int    `gorm:"not null;default:0"`
	GenerationCount    int64  `gorm:"not null;default:0"`
	BannedWordHits     int64  `gorm:"not null;default:0"` // 提示词命中违禁词被拦截的累计次数
	LastLoginAt        *time.Time
	LastLoginIP        string `gorm:"size:128"`
	CreatedAt          time.Time
	UpdatedAt          time.Time
	APIKeys            []APIKey `gorm:"foreignKey:UserID"`
}

// BannedWord is an admin-managed prompt blocklist entry. Generation requests
// whose prompt contains Word (case-insensitive substring) are rejected before
// reaching any provider; Hits counts how many requests each word blocked.
type BannedWord struct {
	ID        string `gorm:"primaryKey;size:32"`
	Word      string `gorm:"size:255;uniqueIndex;not null"`
	Hits      int64  `gorm:"not null;default:0"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

// BannedWordHit records one blocked request: which word matched, who sent it,
// and when. Feeds the admin 违禁词触发列表.
type BannedWordHit struct {
	ID        string    `gorm:"primaryKey;size:32"`
	WordID    string    `gorm:"size:32;index"`
	Word      string    `gorm:"size:255;index;not null"`
	UserID    string    `gorm:"size:32;index"`
	UserName  string    `gorm:"size:255"` // snapshot of name/email at hit time
	Prompt    string    `gorm:"type:text"`
	CreatedAt time.Time `gorm:"index"`
}

type APIKey struct {
	ID         string `gorm:"primaryKey;size:32"`
	UserID     string `gorm:"size:32;index;not null"`
	Name       string `gorm:"size:100;not null"`
	KeyPreview string `gorm:"size:32;not null"`
	KeyHash    string `gorm:"size:255;uniqueIndex;not null"`
	CreatedAt  time.Time
	LastUsedAt *time.Time
}

type ShowcaseItem struct {
	ID        string `gorm:"primaryKey;size:32"`
	Kind      string `gorm:"size:32;index;not null"`
	Title     string `gorm:"size:255"`
	Subtitle  string `gorm:"size:255"`
	Prompt    string `gorm:"type:text"`
	Gradient  string `gorm:"type:text"`
	Span      string `gorm:"size:100"`
	Image     string `gorm:"size:500;index"`
	Weight    int    `gorm:"not null;default:0"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type EventLog struct {
	ID         string         `gorm:"primaryKey;size:32"`
	TS         time.Time      `gorm:"index;not null"`
	Kind       string         `gorm:"size:32;index;not null"`
	Status     string         `gorm:"size:32;index;not null"`
	Model      string         `gorm:"size:255;index"`
	Provider   string         `gorm:"size:100;index"`
	Prompt     string         `gorm:"type:text"`
	Ratio      string         `gorm:"size:32"`
	Resolution string         `gorm:"size:32"`
	Duration   string         `gorm:"size:32"`
	Refs       int            `gorm:"not null;default:0"`
	RefFiles   datatypes.JSON `gorm:"type:jsonb"` // relative paths of saved reference images, for回显 on reload
	Source     string         `gorm:"size:32;index"`
	// AccountID is the provider token/account chosen to fulfil this generation,
	// stamped when the upstream call begins. Drives the accounts view's live
	// in-flight count (pending events per account) and lets an abandoned-event
	// purge attribute the failure back to the account it was using.
	AccountID string `gorm:"size:64;index"`
	// AccountEmail denormalizes the account's email at stamp time, so log rows
	// keep showing which mailbox fulfilled the generation even after the account
	// is deleted or re-imported under a different ID.
	AccountEmail string  `gorm:"size:255"`
	UserID       string  `gorm:"size:32;index"`
	Cost         float64 `gorm:"not null;default:0"`
	// Refunded marks that this event's up-front charge has already been credited
	// back, so the normal failure path and the abandoned-purge sweep can never
	// double-refund the same generation.
	Refunded  bool   `gorm:"not null;default:false"`
	ElapsedMS int    `gorm:"not null;default:0"`
	File      string `gorm:"size:500;index"`
	Error     string `gorm:"type:text"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

type ModelConfig struct {
	ID             string            `gorm:"primaryKey;size:255"`
	Type           string            `gorm:"size:32;index;not null"`
	Name           string            `gorm:"size:255;not null"`
	Alias          string            `gorm:"column:alias;size:255;not null;default:''"`
	Provider       string            `gorm:"size:100;index;not null"`
	Enabled        bool              `gorm:"not null;default:true"`
	Ratios         datatypes.JSON    `gorm:"type:jsonb"`
	Prices         datatypes.JSONMap `gorm:"type:jsonb"`
	Resolutions    datatypes.JSON    `gorm:"type:jsonb"`
	ImageToImage   bool              `gorm:"not null;default:false"`
	DurationPrices datatypes.JSONMap `gorm:"type:jsonb"`
	// Agent (代理) pricing — optional overlay over Prices/DurationPrices. A tier
	// left unset here means agent users pay the normal price for that tier; the
	// set of *supported* tiers is always driven by Prices, not these.
	PricesAgent         datatypes.JSONMap `gorm:"type:jsonb;column:prices_agent"`
	DurationPricesAgent datatypes.JSONMap `gorm:"type:jsonb;column:duration_prices_agent"`
	Durations           datatypes.JSON    `gorm:"type:jsonb"`
	MaxReferenceImages  int               `gorm:"not null;default:0"`
	ReferenceMode       string            `gorm:"size:32;not null;default:'none'"`
	// Custom-upstream models (provider="custom"): UpstreamModel is the model name
	// sent to the upstream OpenAI-compatible API; the base_url + key live on the
	// matching custom account (pool="custom", meta.base_url). Empty for built-ins.
	UpstreamModel string `gorm:"size:255;not null;default:''"`
	// Weight controls display order in the model dropdown / admin list: higher
	// weight floats to the top (matches ShowcaseItem.Weight semantics). Ties fall
	// back to created_at desc. Default 0.
	Weight int `gorm:"not null;default:0;index"`
	// GenerationCount is a persistent success counter, incremented once per
	// successful generation. Independent of the event_log (which is subject to
	// retention / manual clearing), so the admin "次数" is a true running total.
	GenerationCount int64 `gorm:"not null;default:0"`
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

func (m ModelConfig) EffectiveName() string {
	if strings.TrimSpace(m.Alias) != "" {
		return strings.TrimSpace(m.Alias)
	}
	return m.ID
}

type CDKCode struct {
	Code       string  `gorm:"primaryKey;size:32"`
	Amount     int     `gorm:"not null"`
	Status     string  `gorm:"size:32;index;not null"`
	Type       string  `gorm:"size:16;not null;default:normal;index"` // normal | marketing
	BatchID    string  `gorm:"size:32;index"`                         // groups one generate call
	Note       string  `gorm:"type:text"`
	RedeemedBy *string `gorm:"size:32;index"`
	RedeemedAt *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type TokenAccount struct {
	ID                    string            `gorm:"primaryKey;size:64"`
	Pool                  string            `gorm:"size:64;index;not null"`
	Value                 string            `gorm:"type:text"`
	Status                string            `gorm:"size:32;index;not null"`
	Fails                 int               `gorm:"not null;default:0"`
	FailTotal             int               `gorm:"not null;default:0"`
	SuccessTotal          int               `gorm:"not null;default:0"`
	Dead                  bool              `gorm:"not null;default:false"`
	Meta                  datatypes.JSONMap `gorm:"type:jsonb"`
	AddedAt               *time.Time
	LastUsedAt            *time.Time
	CachedQuotaResetAfter string `gorm:"size:128"`
	QuotaRecoverAt        *time.Time
	// Adobe quota is tracked separately for image vs video. An account only
	// enters the shared "quota" waiting status when BOTH are limited; a single
	// limit leaves the account usable for the other kind. Recovery time is shared
	// (QuotaRecoverAt / CachedQuotaResetAfter) since Adobe resets both at once.
	ImageLimited       bool   `gorm:"not null;default:false"`
	VideoLimited       bool   `gorm:"not null;default:false"`
	AccountEmail       string `gorm:"size:255"`
	AccountDisplayName string `gorm:"size:255"`
	// Weight biases scheduling order for ANY account — higher weight is picked
	// first within its pool (ties fall back to round-robin). Default 0.
	Weight int `gorm:"not null;default:0"`
	// Concurrency is the max simultaneous jobs for THIS account. Only custom
	// (upstream) accounts honor it; built-in pools use their system default
	// (1 per account, grok 10). 0 = use the system default.
	Concurrency int `gorm:"not null;default:0"`
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type RefreshProfile struct {
	ID                  string `gorm:"primaryKey;size:64"`
	Name                string `gorm:"size:255;not null"`
	Pool                string `gorm:"size:64;index;not null"`
	Kind                string `gorm:"size:64;index;not null"`
	Cookie              string `gorm:"type:text"`
	Enabled             bool   `gorm:"not null;default:true"`
	IntervalSeconds     int    `gorm:"not null;default:54000"`
	ImportedAt          *time.Time
	LastAttemptAt       *time.Time
	LastSuccessAt       *time.Time
	LastError           string `gorm:"type:text"`
	NextRetryAt         *time.Time
	ConsecutiveFailures int `gorm:"not null;default:0"`
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

type SiteSetting struct {
	Key       string `gorm:"primaryKey;size:100"`
	Value     string `gorm:"type:text"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func AutoMigrateModels() []any {
	return []any{
		&User{},
		&BannedWord{},
		&BannedWordHit{},
		&APIKey{},
		&ShowcaseItem{},
		&EventLog{},
		&ModelConfig{},
		&CDKCode{},
		&TokenAccount{},
		&RefreshProfile{},
		&SiteSetting{},
		&StatCounter{},
		&ConcurrencyGroup{},
		&Order{},
	}
}

// Order is a points-recharge order paid via 易支付 (epay). ID is our merchant
// order number (out_trade_no). Status: pending | paid | cancelled. Unpaid orders
// auto-cancel 30 min after creation (ExpiresAt).
type Order struct {
	ID          string    `gorm:"primaryKey;size:40"`
	UserID      string    `gorm:"size:32;index;not null"`
	Amount      float64   `gorm:"not null"`               // 充值金额(元)
	Points      int       `gorm:"not null"`               // 到账积分
	PayType     string    `gorm:"size:16"`                // wxpay | alipay
	Status      string    `gorm:"size:16;index;not null"` // pending | paid | cancelled
	TradeNo     string    `gorm:"size:64;index"`          // 易支付平台订单号
	PayInfo     string    `gorm:"type:text"`              // 二维码 url / 跳转 url
	PayInfoType string    `gorm:"size:16"`                // qrcode | jump | html | ...
	ExpiresAt   time.Time `gorm:"index"`
	PaidAt      *time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ConcurrencyGroup caps how many generations a member user may run AT ONCE
// (across their API key + 画图台). MaxConcurrency 0 = unlimited. Exactly one
// group is IsDefault — new users are bound to it and it can't be deleted.
type ConcurrencyGroup struct {
	ID             string `gorm:"primaryKey;size:32"`
	Name           string `gorm:"size:100;not null"`
	MaxConcurrency int    `gorm:"not null;default:10"` // 0 = 不限制
	IsDefault      bool   `gorm:"not null;default:false;index"`
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// StatCounter is a persistent monotonic counter (key → value), independent of the
// event_log (which is retention-pruned / clearable). Used for the dashboard
// cumulative cards (total/success/failed/image/video/api) so they never reset.
type StatCounter struct {
	Key       string `gorm:"primaryKey;size:64"`
	Value     int64  `gorm:"not null;default:0"`
	UpdatedAt time.Time
}
