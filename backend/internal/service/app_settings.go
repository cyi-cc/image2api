package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"backend/internal/repo"
	"backend/internal/storage"
)

type AppSettingsService struct {
	settings *repo.SiteSettingRepository
	events   *repo.EventRepository
	smtp     *SMTPService
	store    *storage.Client
}

type RegistrationSettings struct {
	Open               bool     `json:"open"`
	EmailCode          bool     `json:"email_code"`
	AllowPasswordReset bool     `json:"allow_password_reset"`
	AllowedDomains     []string `json:"allowed_email_domains"`
	CodeTTLSeconds     int      `json:"code_ttl_seconds"`
}

type SMTPSettings struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	FromAddr string `json:"from_addr"`
	UseTLS   bool   `json:"use_tls"`
}

type CreditSettings struct {
	CheckinEnabled   bool `json:"checkin_enabled"`
	CheckinReward    int  `json:"checkin_reward"`
	InviteEnabled    bool `json:"invite_enabled"`
	InviteReward     int  `json:"invite_reward"`
	CDKRedeemEnabled bool `json:"cdk_redeem_enabled"`
}

// DeAISettings is the per-tier surcharge (积分) for the 去AI特征 option on the
// 画图台 — charged on top of the model's image price when the toggle is on.
type DeAISettings struct {
	Enabled bool `json:"enabled"`
	Price1K int  `json:"price_1k"`
	Price2K int  `json:"price_2k"`
	Price4K int  `json:"price_4k"`
}

// AdobeSettings controls provider-specific scheduling behavior. Sub-account
// Seedance access is deliberately opt-in: Adobe commonly rejects these
// requests with HTTP 408, so the safe default keeps those accounts filtered.
type AdobeSettings struct {
	AllowSubAccountSeedance bool `json:"allow_sub_account_seedance"`
}

type ProxySettings struct {
	Proxy string `json:"proxy"`
}

type R2Settings struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	BucketName      string `json:"bucket_name"`
	PublicBaseURL   string `json:"public_base_url"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	Configured      bool   `json:"configured"`
}

type RetentionSettings struct {
	RetentionDays int `json:"retention_days"`
}

type MediaRetentionResult struct {
	Settings   *RetentionSettings
	Removed    int   `json:"removed"`
	FreedBytes int64 `json:"freed_bytes"`
}

func NewAppSettingsService(settings *repo.SiteSettingRepository, events *repo.EventRepository, smtp *SMTPService, store *storage.Client) *AppSettingsService {
	return &AppSettingsService{
		settings: settings,
		events:   events,
		smtp:     smtp,
		store:    store,
	}
}

// UploadLogo stores a new site logo in object storage under branding/, deletes
// the previously-uploaded one (if any), persists site.logo, and returns its URL.
func (s *AppSettingsService) UploadLogo(ctx context.Context, data []byte, contentType string) (string, error) {
	if s.store == nil || !s.store.Configured() {
		return "", errors.New("对象存储未配置")
	}
	if len(data) == 0 {
		return "", errors.New("空文件")
	}
	if len(data) > 4*1024*1024 {
		return "", errors.New("logo 不能超过 4MB")
	}
	key := "branding/logo-" + randomUpper(10) + "." + logoExt(contentType)
	if err := s.store.Put(ctx, key, data, contentType); err != nil {
		return "", err
	}
	url := s.store.PublicURL(key)
	if url == "" {
		return "", errors.New("R2 公开访问地址未配置")
	}
	// Delete the previous uploaded logo (best-effort), then point site.logo at the new one.
	if old, _ := s.settings.GetValue(ctx, "site.logo"); old != "" {
		if oldKey, ok := s.storageKey(old); ok && strings.HasPrefix(oldKey, "branding/") {
			_ = s.store.Delete(ctx, oldKey)
		}
	}
	if err := s.settings.UpsertValue(ctx, "site.logo", url); err != nil {
		return "", err
	}
	return url, nil
}

// UploadAsset stores a public image (e.g. a 首页内容 底图) under branding/ and
// returns its storage path (for form.image). Does NOT touch site settings.
func (s *AppSettingsService) UploadAsset(ctx context.Context, data []byte, contentType string) (string, error) {
	if s.store == nil || !s.store.Configured() {
		return "", errors.New("对象存储未配置")
	}
	if len(data) == 0 {
		return "", errors.New("空文件")
	}
	if len(data) > 8*1024*1024 {
		return "", errors.New("图片不能超过 8MB")
	}
	key := "branding/sc-" + randomUpper(10) + "." + logoExt(contentType)
	if err := s.store.Put(ctx, key, data, contentType); err != nil {
		return "", err
	}
	return key, nil
}

// RemoveLogo deletes the uploaded logo and resets site.logo to the built-in default (empty).
func (s *AppSettingsService) RemoveLogo(ctx context.Context) error {
	if old, _ := s.settings.GetValue(ctx, "site.logo"); old != "" && s.store != nil {
		if oldKey, ok := s.storageKey(old); ok && strings.HasPrefix(oldKey, "branding/") {
			_ = s.store.Delete(ctx, oldKey)
		}
	}
	return s.settings.UpsertValue(ctx, "site.logo", "")
}

func (s *AppSettingsService) storageKey(raw string) (string, bool) {
	if s.store != nil {
		if key, ok := s.store.KeyFromPublicURL(raw); ok {
			return key, true
		}
	}
	if strings.HasPrefix(raw, "/images/") {
		return strings.TrimPrefix(raw, "/images/"), true
	}
	return "", false
}

func logoExt(contentType string) string {
	switch strings.ToLower(strings.TrimSpace(contentType)) {
	case "image/jpeg", "image/jpg":
		return "jpg"
	case "image/webp":
		return "webp"
	case "image/svg+xml":
		return "svg"
	case "image/gif":
		return "gif"
	default:
		return "png"
	}
}

func (s *AppSettingsService) Registration(ctx context.Context) (*RegistrationSettings, error) {
	openRaw, err := s.settings.GetValue(ctx, "auth.open")
	if err != nil {
		return nil, err
	}
	emailCodeRaw, err := s.settings.GetValue(ctx, "auth.email_code")
	if err != nil {
		return nil, err
	}
	resetRaw, err := s.settings.GetValue(ctx, "auth.allow_password_reset")
	if err != nil {
		return nil, err
	}
	domainsRaw, err := s.settings.GetValue(ctx, "auth.allowed_email_domains")
	if err != nil {
		return nil, err
	}
	ttlRaw, err := s.settings.GetValue(ctx, "auth.code_ttl_seconds")
	if err != nil {
		return nil, err
	}
	ttl, _ := strconv.Atoi(strings.TrimSpace(ttlRaw))
	if ttl < 60 {
		ttl = 600
	}
	return &RegistrationSettings{
		Open:               parseBoolSetting(openRaw, true),
		EmailCode:          parseBoolSetting(emailCodeRaw, false),
		AllowPasswordReset: parseBoolSetting(resetRaw, false),
		AllowedDomains:     parseCSVSetting(domainsRaw),
		CodeTTLSeconds:     ttl,
	}, nil
}

func (s *AppSettingsService) SaveRegistration(ctx context.Context, in RegistrationSettings) (*RegistrationSettings, error) {
	// Empty list is allowed and means "no domain restriction": EmailDomainAllowed
	// returns true for everyone when the whitelist is empty.
	domains := ValidateAllowedEmailDomains(in.AllowedDomains)
	if in.CodeTTLSeconds < 60 {
		in.CodeTTLSeconds = 600
	}
	if err := s.settings.UpsertValues(ctx, map[string]string{
		"auth.open":                  strconv.FormatBool(in.Open),
		"auth.email_code":            strconv.FormatBool(in.EmailCode),
		"auth.allow_password_reset":  strconv.FormatBool(in.AllowPasswordReset),
		"auth.allowed_email_domains": strings.Join(domains, ","),
		"auth.code_ttl_seconds":      strconv.Itoa(in.CodeTTLSeconds),
	}); err != nil {
		return nil, err
	}
	return s.Registration(ctx)
}

func (s *AppSettingsService) SMTP(ctx context.Context) (*SMTPSettings, error) {
	host, err := s.settings.GetValue(ctx, "smtp.host")
	if err != nil {
		return nil, err
	}
	portRaw, err := s.settings.GetValue(ctx, "smtp.port")
	if err != nil {
		return nil, err
	}
	username, err := s.settings.GetValue(ctx, "smtp.username")
	if err != nil {
		return nil, err
	}
	password, err := s.settings.GetValue(ctx, "smtp.password")
	if err != nil {
		return nil, err
	}
	fromAddr, err := s.settings.GetValue(ctx, "smtp.from_addr")
	if err != nil {
		return nil, err
	}
	useTLSRaw, err := s.settings.GetValue(ctx, "smtp.use_tls")
	if err != nil {
		return nil, err
	}

	port, _ := strconv.Atoi(strings.TrimSpace(portRaw))
	if port <= 0 {
		port = 587
	}
	return &SMTPSettings{
		Host:     strings.TrimSpace(host),
		Port:     port,
		Username: strings.TrimSpace(username),
		Password: maskedSecret(password),
		FromAddr: strings.TrimSpace(fromAddr),
		UseTLS:   parseBoolSetting(useTLSRaw, true),
	}, nil
}

func (s *AppSettingsService) SaveSMTP(ctx context.Context, in SMTPSettings) (*SMTPSettings, error) {
	host := strings.TrimSpace(in.Host)
	username := strings.TrimSpace(in.Username)
	fromAddr := strings.TrimSpace(in.FromAddr)
	if host == "" || username == "" || fromAddr == "" {
		return nil, errors.New("请填写 主机 / 用户名 / 发件地址")
	}
	if _, err := ValidateEmail(fromAddr); err != nil {
		return nil, err
	}
	if in.Port <= 0 {
		return nil, errors.New("port 必须是正整数")
	}

	updates := map[string]string{
		"smtp.host":      host,
		"smtp.port":      strconv.Itoa(in.Port),
		"smtp.username":  username,
		"smtp.from_addr": fromAddr,
		"smtp.use_tls":   strconv.FormatBool(in.UseTLS),
	}
	if strings.TrimSpace(in.Password) != "" && strings.TrimSpace(in.Password) != "***" {
		updates["smtp.password"] = in.Password
	}
	if err := s.settings.UpsertValues(ctx, updates); err != nil {
		return nil, err
	}
	return s.SMTP(ctx)
}

func (s *AppSettingsService) TestSMTP(ctx context.Context, to string) error {
	to, err := ValidateEmail(to)
	if err != nil {
		return err
	}
	cfg, err := s.loadSMTPConfig(ctx)
	if err != nil {
		return err
	}
	return s.smtp.SendCode(ctx, cfg, to, "123456", "register")
}

func (s *AppSettingsService) Proxy(ctx context.Context) (*ProxySettings, error) {
	proxy, err := s.settings.GetValue(ctx, "proxy.url")
	if err != nil {
		return nil, err
	}
	return &ProxySettings{Proxy: strings.TrimSpace(proxy)}, nil
}

func (s *AppSettingsService) SaveProxy(ctx context.Context, proxy string) (*ProxySettings, error) {
	proxy = strings.TrimSpace(proxy)
	if err := s.settings.UpsertValue(ctx, "proxy.url", proxy); err != nil {
		return nil, err
	}
	return &ProxySettings{Proxy: proxy}, nil
}

func (s *AppSettingsService) R2(ctx context.Context) (*R2Settings, error) {
	get := func(key string) (string, error) {
		v, err := s.settings.GetValue(ctx, key)
		return strings.TrimSpace(v), err
	}
	endpoint, err := get("r2.endpoint")
	if err != nil {
		return nil, err
	}
	region, err := get("r2.region")
	if err != nil {
		return nil, err
	}
	bucket, err := get("r2.bucket_name")
	if err != nil {
		return nil, err
	}
	publicBaseURL, err := get("r2.public_base_url")
	if err != nil {
		return nil, err
	}
	accessKeyID, err := get("r2.access_key_id")
	if err != nil {
		return nil, err
	}
	secret, err := get("r2.secret_access_key")
	if err != nil {
		return nil, err
	}
	if region == "" {
		region = "auto"
	}
	return &R2Settings{
		Endpoint: endpoint, Region: region, BucketName: bucket,
		PublicBaseURL: publicBaseURL, AccessKeyID: accessKeyID,
		SecretAccessKey: maskedSecret(secret), Configured: s.store != nil && s.store.Configured(),
	}, nil
}

func (s *AppSettingsService) SaveR2(ctx context.Context, in R2Settings) (*R2Settings, error) {
	oldLogoKey := ""
	if oldLogo, _ := s.settings.GetValue(ctx, "site.logo"); oldLogo != "" && s.store != nil {
		oldLogoKey, _ = s.store.KeyFromPublicURL(oldLogo)
	}
	secret := strings.TrimSpace(in.SecretAccessKey)
	if secret == "" || secret == "***" {
		var err error
		secret, err = s.settings.GetValue(ctx, "r2.secret_access_key")
		if err != nil {
			return nil, err
		}
	}
	normalized, err := storage.NormalizeR2Config(storage.R2Config{
		Endpoint: in.Endpoint, Region: in.Region, BucketName: in.BucketName,
		PublicBaseURL: in.PublicBaseURL, AccessKeyID: in.AccessKeyID,
		SecretAccessKey: secret,
	})
	if err != nil {
		return nil, err
	}
	if err := s.settings.UpsertValues(ctx, map[string]string{
		"r2.endpoint":          normalized.Endpoint,
		"r2.region":            normalized.Region,
		"r2.bucket_name":       normalized.BucketName,
		"r2.public_base_url":   normalized.PublicBaseURL,
		"r2.access_key_id":     normalized.AccessKeyID,
		"r2.secret_access_key": normalized.SecretAccessKey,
	}); err != nil {
		return nil, err
	}
	if s.store == nil {
		return nil, errors.New("R2 客户端未初始化")
	}
	if err := s.store.Configure(normalized); err != nil {
		return nil, err
	}
	if strings.HasPrefix(oldLogoKey, "branding/") {
		_ = s.settings.UpsertValue(ctx, "site.logo", s.store.PublicURL(oldLogoKey))
	}
	return s.R2(ctx)
}

// TestR2 verifies authenticated write/delete access using a tiny temporary
// object. It intentionally leaves existing generated media untouched.
func (s *AppSettingsService) TestR2(ctx context.Context) error {
	if s.store == nil || !s.store.Configured() {
		return errors.New("请先保存 R2 配置")
	}
	key := "musesapi-healthcheck/" + randomUpper(12) + ".txt"
	if err := s.store.Put(ctx, key, []byte("MusesAPI R2 connection test"), "text/plain"); err != nil {
		return fmt.Errorf("R2 写入测试失败:%w", err)
	}
	if err := s.store.Delete(ctx, key); err != nil {
		return fmt.Errorf("R2 写入成功,但删除测试失败:%w", err)
	}
	return nil
}

// TestProxy routes a probe request through the given proxy to an IP-echo service
// and reports the egress IP + latency. Tests the value passed in (so the admin
// can verify before saving). Mirrors how generation calls go out — same HTTP
// CONNECT through the proxy — so a green result means upstream calls will route.
func (s *AppSettingsService) TestProxy(ctx context.Context, proxy string) (map[string]any, error) {
	proxy = strings.TrimSpace(proxy)
	if proxy == "" {
		return nil, errors.New("代理地址为空,请先填写")
	}
	parsed, err := url.Parse(proxy)
	if err != nil || parsed.Host == "" {
		return nil, fmt.Errorf("代理地址格式不正确(应形如 http://user:pass@host:port)")
	}

	transport := &http.Transport{Proxy: http.ProxyURL(parsed)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 12 * time.Second}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.ipify.org?format=json", nil)
	if err != nil {
		return nil, err
	}
	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("通过代理请求失败:%v", err)
	}
	defer resp.Body.Close()
	elapsed := int(time.Since(start).Milliseconds())
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("代理已连接,但探测返回 HTTP %d", resp.StatusCode)
	}
	var echo struct {
		IP string `json:"ip"`
	}
	_ = json.Unmarshal(body, &echo)
	return map[string]any{
		"exit_ip":    echo.IP,
		"elapsed_ms": elapsed,
	}, nil
}

func (s *AppSettingsService) DeAI(ctx context.Context) (*DeAISettings, error) {
	enabledRaw, err := s.settings.GetValue(ctx, "deai.enabled")
	if err != nil {
		return nil, err
	}
	p1Raw, err := s.settings.GetValue(ctx, "deai.price_1k")
	if err != nil {
		return nil, err
	}
	p2Raw, err := s.settings.GetValue(ctx, "deai.price_2k")
	if err != nil {
		return nil, err
	}
	p4Raw, err := s.settings.GetValue(ctx, "deai.price_4k")
	if err != nil {
		return nil, err
	}
	return &DeAISettings{
		Enabled: parseBoolSetting(enabledRaw, false),
		Price1K: clampNonNegative(parseIntSetting(p1Raw, 1)),
		Price2K: clampNonNegative(parseIntSetting(p2Raw, 2)),
		Price4K: clampNonNegative(parseIntSetting(p4Raw, 3)),
	}, nil
}

func (s *AppSettingsService) SaveDeAI(ctx context.Context, in DeAISettings) (*DeAISettings, error) {
	if err := s.settings.UpsertValues(ctx, map[string]string{
		"deai.enabled":  strconv.FormatBool(in.Enabled),
		"deai.price_1k": strconv.Itoa(clampNonNegative(in.Price1K)),
		"deai.price_2k": strconv.Itoa(clampNonNegative(in.Price2K)),
		"deai.price_4k": strconv.Itoa(clampNonNegative(in.Price4K)),
	}); err != nil {
		return nil, err
	}
	return s.DeAI(ctx)
}

func (s *AppSettingsService) Adobe(ctx context.Context) (*AdobeSettings, error) {
	raw, err := s.settings.GetValue(ctx, "adobe.allow_sub_account_seedance")
	if err != nil {
		return nil, err
	}
	return &AdobeSettings{
		AllowSubAccountSeedance: parseBoolSetting(raw, false),
	}, nil
}

func (s *AppSettingsService) SaveAdobe(ctx context.Context, in AdobeSettings) (*AdobeSettings, error) {
	if err := s.settings.UpsertValue(
		ctx,
		"adobe.allow_sub_account_seedance",
		strconv.FormatBool(in.AllowSubAccountSeedance),
	); err != nil {
		return nil, err
	}
	return s.Adobe(ctx)
}

func clampNonNegative(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

func (s *AppSettingsService) Credits(ctx context.Context) (*CreditSettings, error) {
	checkinEnabledRaw, err := s.settings.GetValue(ctx, "credits.checkin_enabled")
	if err != nil {
		return nil, err
	}
	checkinRewardRaw, err := s.settings.GetValue(ctx, "credits.checkin_reward")
	if err != nil {
		return nil, err
	}
	inviteEnabledRaw, err := s.settings.GetValue(ctx, "credits.invite_enabled")
	if err != nil {
		return nil, err
	}
	inviteRewardRaw, err := s.settings.GetValue(ctx, "credits.invite_reward")
	if err != nil {
		return nil, err
	}
	cdkRaw, _ := s.settings.GetValue(ctx, "credits.cdk_redeem_enabled")
	return &CreditSettings{
		CheckinEnabled:   parseBoolSetting(checkinEnabledRaw, true),
		CheckinReward:    parseIntSetting(checkinRewardRaw, 3),
		InviteEnabled:    parseBoolSetting(inviteEnabledRaw, true),
		InviteReward:     parseIntSetting(inviteRewardRaw, 3),
		CDKRedeemEnabled: parseBoolSetting(cdkRaw, true),
	}, nil
}

func (s *AppSettingsService) SaveCredits(ctx context.Context, in CreditSettings) (*CreditSettings, error) {
	if in.CheckinReward < 0 {
		in.CheckinReward = 0
	}
	if in.InviteReward < 0 {
		in.InviteReward = 0
	}
	if err := s.settings.UpsertValues(ctx, map[string]string{
		"credits.checkin_enabled":    strconv.FormatBool(in.CheckinEnabled),
		"credits.checkin_reward":     strconv.Itoa(in.CheckinReward),
		"credits.invite_enabled":     strconv.FormatBool(in.InviteEnabled),
		"credits.invite_reward":      strconv.Itoa(in.InviteReward),
		"credits.cdk_redeem_enabled": strconv.FormatBool(in.CDKRedeemEnabled),
	}); err != nil {
		return nil, err
	}
	return s.Credits(ctx)
}

func (s *AppSettingsService) Logs(ctx context.Context) (*RetentionSettings, error) {
	return s.retention(ctx, "logs.retention_days")
}

func (s *AppSettingsService) SaveLogs(ctx context.Context, days int) (*RetentionSettings, error) {
	days, err := normalizeRetentionDays(days)
	if err != nil {
		return nil, err
	}
	if err := s.settings.UpsertValue(ctx, "logs.retention_days", strconv.Itoa(days)); err != nil {
		return nil, err
	}
	if s.events != nil {
		_, _ = s.events.PurgeOlderThan(ctx, time.Duration(days)*24*time.Hour)
	}
	return s.Logs(ctx)
}

func (s *AppSettingsService) Media(ctx context.Context) (*RetentionSettings, error) {
	return s.retention(ctx, "media.retention_days")
}

func (s *AppSettingsService) SaveMedia(ctx context.Context, days int) (*MediaRetentionResult, error) {
	days, err := normalizeRetentionDays(days)
	if err != nil {
		return nil, err
	}
	if err := s.settings.UpsertValue(ctx, "media.retention_days", strconv.Itoa(days)); err != nil {
		return nil, err
	}
	removed, freed := s.pruneGeneratedFiles(ctx, time.Duration(days)*24*time.Hour)
	settings, err := s.Media(ctx)
	if err != nil {
		return nil, err
	}
	return &MediaRetentionResult{
		Settings:   settings,
		Removed:    removed,
		FreedBytes: freed,
	}, nil
}

func (s *AppSettingsService) loadSMTPConfig(ctx context.Context) (SMTPConfig, error) {
	current, err := s.SMTP(ctx)
	if err != nil {
		return SMTPConfig{}, err
	}
	password, err := s.settings.GetValue(ctx, "smtp.password")
	if err != nil {
		return SMTPConfig{}, err
	}
	// The verification-email subject uses the site title, e.g. "<title> 邮箱验证码".
	title, _ := s.settings.GetValue(ctx, "site.title")
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Vivid"
	}
	return SMTPConfig{
		Host:     current.Host,
		Port:     current.Port,
		Username: current.Username,
		Password: password,
		FromAddr: current.FromAddr,
		UseTLS:   current.UseTLS,
		Subject:  title + " 邮箱验证码",
	}, nil
}

func maskedSecret(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	return "***"
}

func parseIntSetting(v string, fallback int) int {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return fallback
	}
	return n
}

func (s *AppSettingsService) retention(ctx context.Context, key string) (*RetentionSettings, error) {
	raw, err := s.settings.GetValue(ctx, key)
	if err != nil {
		return nil, err
	}
	days := parseIntSetting(raw, 30)
	if days < 1 {
		days = 30
	}
	return &RetentionSettings{RetentionDays: days}, nil
}

func normalizeRetentionDays(days int) (int, error) {
	if days < 1 {
		return 0, errors.New("留存天数至少为 1 天")
	}
	if days > 365 {
		return 0, errors.New("留存天数最多 365 天")
	}
	return days, nil
}

// pruneGeneratedFiles deletes R2 objects older than maxAge and blanks the
// matching event_log.file refs. Returns how many were removed and bytes freed.
// (The maintenance loop does the same automatically every 60s; this gives the
// admin an immediate result when they shorten the media retention window.)
func (s *AppSettingsService) pruneGeneratedFiles(ctx context.Context, maxAge time.Duration) (int, int64) {
	if s.store == nil || !s.store.Configured() || maxAge <= 0 {
		return 0, 0
	}
	objs, err := s.store.List(ctx, "")
	if err != nil {
		return 0, 0
	}
	cutoff := time.Now().Add(-maxAge)
	removed := 0
	var freed int64
	var clearedKeys []string
	for _, o := range objs {
		if !o.LastModified.Before(cutoff) {
			continue
		}
		if err := s.store.Delete(ctx, o.Key); err == nil {
			removed++
			freed += o.Size
			clearedKeys = append(clearedKeys, o.Key)
		}
	}
	if len(clearedKeys) > 0 {
		_, _ = s.events.ClearFiles(ctx, clearedKeys)
	}
	return removed, freed
}
