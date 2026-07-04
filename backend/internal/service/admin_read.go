package service

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"backend/internal/config"
	"backend/internal/model"
	"backend/internal/repo"
	"backend/internal/storage"
)

type AdminReadService struct {
	cfg      *config.Config
	users    *repo.UserRepository
	models   *repo.ModelRepository
	events   *repo.EventRepository
	settings *repo.SiteSettingRepository
	tokens   *repo.TokenRepository
	cdks     *repo.CDKRepository
	store    *storage.Client
	showcase *repo.ShowcaseRepository
}

func NewAdminReadService(cfg *config.Config, users *repo.UserRepository, models *repo.ModelRepository, events *repo.EventRepository, settings *repo.SiteSettingRepository, tokens *repo.TokenRepository, cdks *repo.CDKRepository, store *storage.Client, showcase *repo.ShowcaseRepository) *AdminReadService {
	return &AdminReadService{
		cfg:      cfg,
		users:    users,
		models:   models,
		events:   events,
		settings: settings,
		tokens:   tokens,
		cdks:     cdks,
		store:    store,
		showcase: showcase,
	}
}

// showcaseFileList returns the homepage showcase image keys (no leading slash).
// User-facing galleries and the admin image manager hide these files — they
// belong to the public landing page, not to anyone's personal works.
func (s *AdminReadService) showcaseFileList(ctx context.Context) []string {
	if s.showcase == nil {
		return nil
	}
	set, err := s.showcase.PublicFileSet(ctx)
	if err != nil || len(set) == 0 {
		return nil
	}
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

func (s *AdminReadService) Users(ctx context.Context) ([]model.User, map[string]any, error) {
	users, err := s.users.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	stats, err := s.users.Stats(ctx)
	if err != nil {
		return nil, nil, err
	}
	// Per-user generation count now comes from the persistent users.generation_count
	// column (set in the handler from each user object), not a log COUNT.
	return users, stats, nil
}

func (s *AdminReadService) Models(ctx context.Context) ([]model.ModelConfig, error) {
	return s.models.List(ctx)
}

func (s *AdminReadService) ModelNameMap(ctx context.Context) (map[string]string, error) {
	return s.models.NameMap(ctx)
}

func (s *AdminReadService) ModelsView(ctx context.Context) ([]map[string]any, error) {
	items, err := s.models.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, map[string]any{
			"id":                    item.ID,
			"alias":                 item.Alias,
			"type":                  item.Type,
			"name":                  item.Name,
			"provider":              item.Provider,
			"enabled":               item.Enabled,
			"ratios":                repo.JSONStrings(item.Ratios),
			"prices":                map[string]any(item.Prices),
			"resolutions":           repo.JSONStrings(item.Resolutions),
			"image_to_image":        item.ImageToImage,
			"duration_prices":       map[string]any(item.DurationPrices),
			"prices_agent":          map[string]any(item.PricesAgent),
			"duration_prices_agent": map[string]any(item.DurationPricesAgent),
			"durations":             repo.JSONStrings(item.Durations),
			"max_reference_images":  item.MaxReferenceImages,
			"reference_mode":        item.ReferenceMode,
			"weight":                item.Weight,
			"generation_count":      item.GenerationCount,
			"created_at":            item.CreatedAt,
			"updated_at":            item.UpdatedAt,
		})
	}
	return out, nil
}

func (s *AdminReadService) Logs(ctx context.Context, limit, offset int, kind, status string, statuses []string, since *time.Time, userID, excludeSource, source string, hasFile, excludeShowcase, mediaOnly bool) ([]model.EventLog, int64, *repo.EventStats, error) {
	var excludeFiles []string
	if excludeShowcase {
		excludeFiles = s.showcaseFileList(ctx)
	}
	items, total, err := s.events.List(ctx, repo.EventListFilter{
		Limit:         limit,
		Offset:        offset,
		Kind:          kind,
		Status:        status,
		Statuses:      statuses,
		Since:         since,
		UserID:        userID,
		ExcludeSource: excludeSource,
		Source:        source,
		HasFile:       hasFile,
		ExcludeFiles:  excludeFiles,
		MediaOnly:     mediaOnly,
	})
	if err != nil {
		return nil, 0, nil, err
	}
	// 用户自己的日志(userID 非空)→ 按本人统计;管理员全站视图 → 全站统计。
	var stats *repo.EventStats
	if userID != "" {
		stats, err = s.events.StatsByUser(ctx, userID)
	} else {
		stats, err = s.events.Stats(ctx)
	}
	if err != nil {
		return nil, 0, nil, err
	}
	return items, total, stats, nil
}

// UserNameMap builds an id -> display name lookup (name, else email, else id)
// used to annotate admin log rows with user_name (mirrors admin.py:584-596).
func (s *AdminReadService) UserNameMap(ctx context.Context) (map[string]string, error) {
	users, err := s.users.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(users))
	for _, u := range users {
		name := strings.TrimSpace(u.Name)
		if name == "" {
			name = strings.TrimSpace(u.Email)
		}
		if name == "" {
			name = u.ID
		}
		out[u.ID] = name
	}
	return out, nil
}

// AccountNameMap builds a token-account id -> display label lookup (account
// email, else display name, else id) used to annotate log rows with which
// provider account fulfilled each generation (event_logs.account_id).
func (s *AdminReadService) AccountNameMap(ctx context.Context) (map[string]string, error) {
	accounts, err := s.tokens.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(accounts))
	for _, a := range accounts {
		label := strings.TrimSpace(a.AccountEmail)
		if label == "" {
			label = strings.TrimSpace(a.AccountDisplayName)
		}
		if label == "" {
			label = a.ID
		}
		out[a.ID] = label
	}
	return out, nil
}

func (s *AdminReadService) Stats(ctx context.Context) (map[string]any, error) {
	stats, err := s.events.Stats(ctx)
	if err != nil {
		return nil, err
	}
	recentFiles, _ := s.RecentImages(ctx, 24)
	files, fileStats, _ := s.scanGeneratedFiles(ctx)
	var size int64
	if v, ok := fileStats["size_bytes"].(int64); ok {
		size = v
	}
	return map[string]any{
		"generated_count":      len(files),
		"generated_size_bytes": size,
		"recent":               recentFiles,
		"avg_elapsed_ms":       stats.AvgElapsedMS,
		"avg_elapsed_ms_24h":   stats.AvgElapsedMS24,
	}, nil
}

// Dashboard assembles the admin overview's analytics entirely server-side
// (event windows, hourly trend, top models/failures/spenders) plus CDK / invite
// / checkin summaries. This replaces the old client-side math over the last 200
// logs, which silently undercounted week/DAU/trend once volume grew.
func (s *AdminReadService) Dashboard(ctx context.Context) (map[string]any, error) {
	now := time.Now()
	dayCut := now.Add(-24 * time.Hour)
	weekCut := now.Add(-3 * 24 * time.Hour) // "week" key = last 3 days (per admin request)

	day, err := s.events.WindowStats(ctx, dayCut)
	if err != nil {
		return nil, err
	}
	week, err := s.events.WindowStats(ctx, weekCut)
	if err != nil {
		return nil, err
	}
	prevDay, err := s.events.CountBetween(ctx, now.Add(-48*time.Hour), dayCut)
	if err != nil {
		return nil, err
	}
	dau, err := s.events.DistinctUsersSince(ctx, dayCut)
	if err != nil {
		return nil, err
	}
	wau, err := s.events.DistinctUsersSince(ctx, weekCut)
	if err != nil {
		return nil, err
	}
	hourly, err := s.events.HourlyBuckets(ctx)
	if err != nil {
		return nil, err
	}
	// All-time persistent counters (total/success/failed/image/video/api) — these
	// survive log retention/clearing, unlike the windowed day/week stats.
	lifetime, err := s.events.Counters(ctx)
	if err != nil {
		return nil, err
	}

	// Per-window top-N analytics so the frontend can toggle 24h / 7d without a
	// re-fetch (the lists are small — top 6 / top 5).
	nameByID, err := s.UserNameMap(ctx)
	if err != nil {
		return nil, err
	}
	analytics := func(since time.Time) (map[string]any, error) {
		models, err := s.events.ModelUsageSince(ctx, since, 6)
		if err != nil {
			return nil, err
		}
		failures, err := s.events.TopFailures(ctx, since, 5)
		if err != nil {
			return nil, err
		}
		users, err := s.events.TopUserSpend(ctx, since, 6)
		if err != nil {
			return nil, err
		}
		for i := range users {
			if users[i].UserID == "" {
				users[i].Name = "匿名"
			} else if name, ok := nameByID[users[i].UserID]; ok {
				users[i].Name = name
			} else {
				users[i].Name = users[i].UserID
			}
		}
		return map[string]any{"models": models, "failures": failures, "top_users": users}, nil
	}
	dayAnalytics, err := analytics(dayCut)
	if err != nil {
		return nil, err
	}
	weekAnalytics, err := analytics(weekCut)
	if err != nil {
		return nil, err
	}

	cdkStats, err := s.cdks.Stats(ctx)
	if err != nil {
		return nil, err
	}

	inviteReward := parseIntSetting(s.mustSetting(ctx, "credits.invite_reward"), 3)
	inviteSummary, err := s.users.InviteSummary(ctx)
	if err != nil {
		return nil, err
	}

	checkinReward := parseIntSetting(s.mustSetting(ctx, "credits.checkin_reward"), 3)
	checkin, err := s.users.CheckinStats(ctx)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"day":            day,
		"week":           week,
		"prev_day_total": prevDay,
		"dau":            dau,
		"wau":            wau,
		"hourly":         hourly,
		"lifetime":       lifetime,
		"analytics": map[string]any{
			"day":  dayAnalytics,
			"week": weekAnalytics,
		},
		"cdk": cdkStats,
		"invites": map[string]any{
			"total":       inviteSummary.Total,
			"completed":   inviteSummary.Completed,
			"reward":      inviteReward,
			"reward_paid": inviteSummary.Completed * int64(inviteReward),
		},
		"checkin": map[string]any{
			"today":         checkin.TodayCount,
			"max_streak":    checkin.MaxStreak,
			"reward":        checkinReward,
			"awarded_today": checkin.TodayCount * int64(checkinReward),
		},
	}, nil
}

// mustSetting reads a site setting value, returning "" on error so the caller's
// parseIntSetting default kicks in (a missing reward setting shouldn't 500 the
// whole dashboard).
func (s *AdminReadService) mustSetting(ctx context.Context, key string) string {
	v, err := s.settings.GetValue(ctx, key)
	if err != nil {
		return ""
	}
	return v
}

func (s *AdminReadService) Invites(ctx context.Context) ([]repo.InviteRecord, *repo.InviteLogStats, error) {
	rewardRaw, err := s.settings.GetValue(ctx, "credits.invite_reward")
	if err != nil {
		return nil, nil, err
	}
	reward := parseIntSetting(rewardRaw, 3)
	return s.users.AllInvites(ctx, reward)
}

func (s *AdminReadService) Providers(ctx context.Context) ([]map[string]any, error) {
	models, err := s.models.List(ctx)
	if err != nil {
		return nil, err
	}
	tokens, err := s.tokens.List(ctx)
	if err != nil {
		return nil, err
	}
	modelCounts := map[string]int{}
	for _, item := range models {
		modelCounts[item.Provider]++
	}
	type aggregate struct {
		active   int
		disabled int
		quota    int
	}
	tokenCounts := map[string]*aggregate{}
	for _, item := range tokens {
		if _, ok := tokenCounts[item.Pool]; !ok {
			tokenCounts[item.Pool] = &aggregate{}
		}
		switch item.Status {
		case "active":
			tokenCounts[item.Pool].active++
		case "quota":
			tokenCounts[item.Pool].quota++
		default:
			tokenCounts[item.Pool].disabled++
		}
	}
	providers := []struct {
		Name string
		Pool string
		Type string
	}{
		{Name: "chatgpt", Pool: "chatgpt", Type: "openai"},
		{Name: "adobe", Pool: "adobe", Type: "adobe"},
		{Name: "runway", Pool: "runway", Type: "runway"},
		{Name: "leonardo", Pool: "leonardo", Type: "leonardo"},
		{Name: "krea", Pool: "krea", Type: "krea"},
		{Name: "imagine", Pool: "imagine", Type: "imagine"},
		{Name: "grok", Pool: "grok", Type: "grok"},
	}
	out := make([]map[string]any, 0, len(providers))
	for _, item := range providers {
		count := tokenCounts[item.Pool]
		if count == nil {
			count = &aggregate{}
		}
		out = append(out, map[string]any{
			"name":            item.Name,
			"token_pool":      item.Pool,
			"type":            item.Type,
			"model_count":     modelCounts[item.Name],
			"tokens_total":    count.active + count.disabled + count.quota,
			"tokens_active":   count.active,
			"tokens_disabled": count.disabled,
			"tokens_quota":    count.quota,
		})
	}
	return out, nil
}

func (s *AdminReadService) Images(ctx context.Context, limit, offset int, kind string) ([]map[string]any, int, map[string]any, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	allFiles, stats, err := s.scanGeneratedFiles(ctx)
	if err != nil {
		return nil, 0, nil, err
	}
	// Homepage showcase media never shows in the image manager — it's public
	// landing-page content, managed on the 首页内容 page instead.
	pinned := map[string]struct{}{}
	if s.showcase != nil {
		if set, perr := s.showcase.PublicFileSet(ctx); perr == nil {
			pinned = set
		}
	}
	filtered := make([]generatedFile, 0, len(allFiles))
	for _, item := range allFiles {
		if _, ok := pinned[strings.TrimLeft(item.Name, "/")]; ok {
			continue
		}
		if kind == "" || item.Kind == kind {
			filtered = append(filtered, item)
		}
	}
	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].MTime > filtered[j].MTime
	})
	total := len(filtered)
	if offset > total {
		offset = total
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := filtered[offset:end]
	index, err := s.eventIndexByFile(ctx)
	if err != nil {
		return nil, 0, nil, err
	}
	nameMap, err := s.models.NameMap(ctx)
	if err != nil {
		return nil, 0, nil, err
	}
	out := make([]map[string]any, 0, len(page))
	for _, item := range page {
		row := map[string]any{
			"name":       item.Name,
			"size":       item.Size,
			"mtime":      item.MTime,
			"kind":       item.Kind,
			"prompt":     "",
			"model":      "",
			"resolution": "",
			"ratio":      "",
			"duration":   "",
		}
		if event, ok := index[item.Name]; ok {
			row["prompt"] = event.Prompt
			if eff, ok := nameMap[event.Model]; ok && eff != "" {
				row["model"] = eff
			} else {
				row["model"] = event.Model
			}
			row["resolution"] = event.Resolution
			row["ratio"] = event.Ratio
			row["duration"] = event.Duration
		}
		out = append(out, row)
	}
	return out, total, stats, nil
}

func (s *AdminReadService) RecentImages(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 24
	}
	allFiles, _, err := s.scanGeneratedFiles(ctx)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(allFiles, func(i, j int) bool {
		return allFiles[i].MTime > allFiles[j].MTime
	})
	if len(allFiles) > limit {
		allFiles = allFiles[:limit]
	}
	out := make([]map[string]any, 0, len(allFiles))
	for _, item := range allFiles {
		out = append(out, map[string]any{
			"name":  item.Name,
			"size":  item.Size,
			"mtime": item.MTime,
			"kind":  item.Kind,
		})
	}
	return out, nil
}

// RecentImagesOwned lists the most-recent generated images under a single owner
// directory (used by the showcase picker so an admin sees only their OWN images).
func (s *AdminReadService) RecentImagesOwned(ctx context.Context, owner string, limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 24
	}
	owner = strings.TrimSpace(owner)
	if owner == "" || s.store == nil || !s.store.Configured() {
		return []map[string]any{}, nil
	}
	objs, err := s.store.List(ctx, owner+"/")
	if err != nil {
		return nil, err
	}
	files := make([]generatedFile, 0, len(objs))
	for _, o := range objs {
		if isReferenceFile(o.Key) {
			continue
		}
		kind := mediaKind(o.Key)
		if kind == "" {
			continue
		}
		files = append(files, generatedFile{Name: o.Key, Size: o.Size, MTime: o.LastModified.Unix(), Kind: kind})
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].MTime > files[j].MTime })
	if len(files) > limit {
		files = files[:limit]
	}
	out := make([]map[string]any, 0, len(files))
	for _, f := range files {
		out = append(out, map[string]any{"name": f.Name, "size": f.Size, "mtime": f.MTime, "kind": f.Kind})
	}
	return out, nil
}

// DeleteOwnedFile removes a generated media object (and its thumbnail) that
// lives under the given owner directory, then blanks the file reference on the
// matching log rows so galleries and the 画图台 grid stop showing it. The owner
// prefix check keeps a user from deleting anyone else's files.
func (s *AdminReadService) DeleteOwnedFile(ctx context.Context, owner, rel string) error {
	owner = strings.TrimSpace(owner)
	rel = strings.TrimLeft(strings.TrimSpace(rel), "/")
	if owner == "" {
		return errors.New("invalid file")
	}
	if !strings.HasPrefix(rel, owner+"/") {
		return errors.New("file not owned by caller")
	}
	return s.DeleteFile(ctx, rel)
}

// DeleteFile removes any generated media object (and its derived stills) and
// blanks the log rows referencing it. Admin 图片管理 delete — no owner check.
func (s *AdminReadService) DeleteFile(ctx context.Context, rel string) error {
	rel = strings.TrimLeft(strings.TrimSpace(rel), "/")
	if rel == "" || strings.Contains(rel, "..") {
		return errors.New("invalid file")
	}
	if s.store == nil || !s.store.Configured() {
		return errors.New("storage not configured")
	}
	if err := s.store.Delete(ctx, rel); err != nil {
		return err
	}
	// Best-effort derived stills; old files may not have them.
	_ = s.store.Delete(ctx, ThumbKey(rel))
	_ = s.store.Delete(ctx, LastFrameKey(rel))
	if _, err := s.events.ClearFiles(ctx, []string{rel}); err != nil {
		return err
	}
	return nil
}

func (s *AdminReadService) eventIndexByFile(ctx context.Context) (map[string]model.EventLog, error) {
	items, err := s.events.RecentByFile(ctx, 10000)
	if err != nil {
		return nil, err
	}
	out := make(map[string]model.EventLog, len(items))
	for _, item := range items {
		if item.File == "" {
			continue
		}
		if _, ok := out[item.File]; ok {
			continue
		}
		out[item.File] = item
	}
	return out, nil
}

type generatedFile struct {
	Name  string
	Size  int64
	MTime int64
	Kind  string
}

// mediaKind classifies an object key by extension (image / video / "" = skip).
func mediaKind(name string) string {
	i := strings.LastIndex(name, ".")
	if i < 0 {
		return ""
	}
	switch strings.ToLower(name[i+1:]) {
	case "png", "jpg", "jpeg", "webp", "gif":
		return "image"
	case "mp4", "webm", "mov":
		return "video"
	default:
		return ""
	}
}

// isReferenceFile reports whether a key is an uploaded reference image (named
// "...-ref-..."), so the gallery / picker can skip them — only generated outputs
// are listed.
func isReferenceFile(name string) bool {
	return strings.Contains(name, "-ref-")
}

// scanGeneratedFiles lists media objects from RustFS (replacing the old local
// directory walk). Keys ARE the relative paths the rest of the app expects.
func (s *AdminReadService) scanGeneratedFiles(ctx context.Context) ([]generatedFile, map[string]any, error) {
	stats := map[string]any{"total": 0, "image": 0, "video": 0, "size_bytes": int64(0)}
	if s.store == nil || !s.store.Configured() {
		return nil, stats, nil
	}
	objs, err := s.store.List(ctx, "")
	if err != nil {
		return nil, nil, err
	}
	out := make([]generatedFile, 0, len(objs))
	for _, o := range objs {
		if isReferenceFile(o.Key) {
			continue // reference uploads are not generated outputs — hide from gallery
		}
		if IsThumbKey(o.Key) || IsLastFrameKey(o.Key) {
			continue // thumbnails / last-frame stills are derived — only originals are listed
		}
		kind := mediaKind(o.Key)
		if kind == "" {
			continue
		}
		stats[kind] = stats[kind].(int) + 1
		stats["total"] = stats["total"].(int) + 1
		stats["size_bytes"] = stats["size_bytes"].(int64) + o.Size
		out = append(out, generatedFile{
			Name:  o.Key,
			Size:  o.Size,
			MTime: o.LastModified.Unix(),
			Kind:  kind,
		})
	}
	return out, stats, nil
}
