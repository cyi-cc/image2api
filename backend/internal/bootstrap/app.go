package bootstrap

import (
	"context"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"backend/internal/config"
	"backend/internal/http/handler"
	"backend/internal/http/router"
	"backend/internal/model"
	"backend/internal/provider/adobe"
	"backend/internal/provider/chatgpt"
	"backend/internal/provider/custom"
	"backend/internal/provider/grok"
	"backend/internal/provider/imagine"
	"backend/internal/provider/krea"
	"backend/internal/provider/leonardo"
	"backend/internal/provider/runway"
	"backend/internal/repo"
	"backend/internal/service"
	"backend/internal/storage"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type App struct {
	Config            *config.Config
	DB                *gorm.DB
	Redis             *redis.Client
	Engine            *gin.Engine
	maintenanceCancel context.CancelFunc
}

func NewApp(ctx context.Context) (*App, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}

	// Ensure the media root (generated outputs + uploaded reference images)
	// exists from the first request — don't rely on lazy per-file MkdirAll.
	if err := os.MkdirAll(cfg.GeneratedRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create generated root %s: %w", cfg.GeneratedRoot, err)
	}

	// TranslateError: 把驱动层错误(如 Postgres 23505 唯一冲突)翻译成 gorm.ErrDuplicatedKey,
	// 否则各 import-*（krea/adobe/leonardo/runway）里的 errors.Is(err, gorm.ErrDuplicatedKey)
	// 兜底命不中,重复导入会直接抛原始错误 → 400,而不是按预期 Update 已有行。
	db, err := gorm.Open(postgres.Open(cfg.PostgresDSN), &gorm.Config{TranslateError: true})
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("sql db: %w", err)
	}
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)

	if err := db.WithContext(ctx).AutoMigrate(model.AutoMigrateModels()...); err != nil {
		return nil, fmt.Errorf("auto migrate: %w", err)
	}
	// Hard backstop for "one marketing code per user per batch": a partial unique
	// index. Concurrent double-redeems that slip past the in-tx count check still
	// fail here. AutoMigrate can't express partial indexes, so do it raw.
	if err := db.WithContext(ctx).Exec(`CREATE UNIQUE INDEX IF NOT EXISTS uniq_cdk_marketing_batch_user ` +
		`ON cdk_codes (batch_id, redeemed_by) WHERE type = 'marketing' AND redeemed_by IS NOT NULL`).Error; err != nil {
		return nil, fmt.Errorf("cdk marketing index: %w", err)
	}
	if err := seedDefaults(ctx, db); err != nil {
		return nil, fmt.Errorf("seed defaults: %w", err)
	}

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: cfg.RedisPassword,
		DB:       cfg.RedisDB,
	})
	if err := rdb.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}

	userRepo := repo.NewUserRepository(db)
	showcaseRepo := repo.NewShowcaseRepository(db)
	siteRepo := repo.NewSiteSettingRepository(db, rdb)
	modelRepo := repo.NewModelRepository(db)
	eventRepo := repo.NewEventRepository(db)
	cdkRepo := repo.NewCDKRepository(db)
	apiKeyRepo := repo.NewAPIKeyRepository(db)
	tokenRepo := repo.NewTokenRepository(db)
	refreshRepo := repo.NewRefreshProfileRepository(db)
	cgroupRepo := repo.NewConcurrencyGroupRepository(db)
	// Seed the "默认并发" group (cap 10) and bind any ungrouped users to it.
	if err := cgroupRepo.EnsureDefault(ctx); err != nil {
		log.Printf("ensure default concurrency group: %v", err)
	}
	concSvc := service.NewConcurrencyService(rdb)
	cgroupSvc := service.NewConcurrencyGroupService(cgroupRepo, concSvc)
	announcementSvc := service.NewAnnouncementService(siteRepo, userRepo)
	orderRepo := repo.NewOrderRepository(db)
	paymentSvc := service.NewPaymentService(orderRepo, userRepo, siteRepo)
	sessionSvc := service.NewSessionService(rdb, cfg.SessionTTL, cfg.SessionSlideAfter)
	emailCodeSvc := service.NewEmailCodeService(rdb)
	smtpSvc := service.NewSMTPService()
	rateLimitSvc := service.NewRateLimitService(rdb)
	r2Client := storage.NewR2("", "auto", "", "", "", "")
	if err := loadRuntimeR2(ctx, siteRepo, r2Client); err != nil {
		log.Printf("load R2 settings: %v (configure it in the admin settings page)", err)
	}
	authSvc := service.NewAuthService(userRepo, siteRepo, sessionSvc, emailCodeSvc, smtpSvc, cgroupRepo)
	appSettingsSvc := service.NewAppSettingsService(siteRepo, eventRepo, smtpSvc, r2Client)
	imageAccessSvc := service.NewImageAccessService(cfg.GeneratedRoot, showcaseRepo, authSvc)
	adobeClient := adobe.NewClient("projectx_webapp", "")
	chatGPTClient := chatgpt.NewClient("")
	runwayClient := runway.NewClient("")
	leonardoClient := leonardo.NewClient("")
	kreaClient := krea.NewClient("")
	imagineClient := imagine.NewClient("")
	grokClient := grok.NewClient("")
	// Keep grok's x-statsig-id anti-bot recipe current by reading grok's own
	// headless-browser signer output. Event-driven: seed from the persisted
	// recipe, capture once at startup, then re-capture only on an anti-bot 403
	// (a reship made the recipe stale). No polling.
	startGrokStatsigRefresh(siteRepo)
	customClient := custom.NewClient()
	v1Svc := service.NewV1Service(cfg, modelRepo, userRepo, eventRepo, tokenRepo, siteRepo, cgroupRepo, concSvc, adobeClient, chatGPTClient, runwayClient, leonardoClient, kreaClient, imagineClient, grokClient, customClient, r2Client)
	siteSvc := service.NewSiteService(siteRepo, cfg.AppTitle, r2Client)
	showcaseSvc := service.NewShowcaseService(showcaseRepo)
	adminReadSvc := service.NewAdminReadService(cfg, userRepo, modelRepo, eventRepo, siteRepo, tokenRepo, cdkRepo, r2Client, showcaseRepo)
	adminWriteSvc := service.NewAdminWriteService(userRepo, showcaseRepo, modelRepo, eventRepo, apiKeyRepo, tokenRepo, orderRepo)
	cdkSvc := service.NewCDKService(cdkRepo, userRepo, siteRepo, orderRepo)
	apiKeySvc := service.NewAPIKeyService(apiKeyRepo)
	tokenSvc := service.NewTokenService(tokenRepo, refreshRepo, eventRepo, siteRepo, adobeClient, chatGPTClient, runwayClient, leonardoClient, kreaClient, imagineClient, grokClient)
	refreshSvc := service.NewRefreshProfileService(refreshRepo, tokenRepo, adobeClient)
	// Enable refresh-then-retry on a mid-request Adobe 401 (re-mint access token
	// from the cookie). Wired post-construction to avoid a ctor init cycle.
	v1Svc.SetRefresh(refreshSvc)
	bannedWordRepo := repo.NewBannedWordRepository(db)
	v1Svc.SetBannedWords(bannedWordRepo)
	userGenSvc := service.NewUserGenerationService(v1Svc, eventRepo, userRepo, modelRepo)

	engine := router.New(cfg, authSvc, router.Handlers{
		Health:        handler.NewHealthHandler(),
		Images:        handler.NewImageHandler(cfg, imageAccessSvc, r2Client),
		V1:            handler.NewV1Handler(v1Svc),
		Site:          handler.NewSiteHandler(siteSvc),
		Showcase:      handler.NewShowcaseHandler(showcaseSvc),
		Auth:          handler.NewAuthHandler(cfg, authSvc, rateLimitSvc),
		SiteSettings:  handler.NewSiteSettingsHandler(siteSvc),
		AppSettings:   handler.NewAppSettingsHandler(appSettingsSvc),
		AdminRead:     handler.NewAdminReadHandler(adminReadSvc),
		AdminWrite:    handler.NewAdminWriteHandler(adminWriteSvc),
		CDK:           handler.NewCDKHandler(cdkSvc),
		UserTools:     handler.NewUserToolsHandler(apiKeySvc, cdkSvc),
		UserGen:       handler.NewUserGenerationHandler(userGenSvc, adminReadSvc),
		ProviderAdmin: handler.NewProviderAdminHandler(tokenSvc, refreshSvc),
		ConcGroups:    handler.NewConcurrencyGroupHandler(cgroupSvc),
		Announcement:  handler.NewAnnouncementHandler(announcementSvc),
		Payment:       handler.NewPaymentHandler(paymentSvc),
		BannedWords:   handler.NewBannedWordsHandler(bannedWordRepo),
	})

	// Background self-healing sweep (quota recovery, cookie refresh, stale-pending
	// cleanup, log retention) — the Go equivalent of the Python daemon thread.
	maintenanceSvc := service.NewMaintenanceService(tokenRepo, tokenSvc, eventRepo, userRepo, refreshSvc, siteRepo, r2Client, v1Svc.Inflight(), showcaseRepo, orderRepo)
	loopCtx, loopCancel := context.WithCancel(context.Background())
	go maintenanceSvc.Run(loopCtx)

	return &App{
		Config:            cfg,
		DB:                db,
		Redis:             rdb,
		Engine:            engine,
		maintenanceCancel: loopCancel,
	}, nil
}

// loadRuntimeR2 hydrates the shared client from PostgreSQL. Database settings
// are the single source of truth; no R2 secret is read from the environment or
// Compose file.
func loadRuntimeR2(ctx context.Context, settings *repo.SiteSettingRepository, client *storage.Client) error {
	get := func(key string) string {
		v, _ := settings.GetValue(ctx, key)
		return strings.TrimSpace(v)
	}
	in := storage.R2Config{
		Endpoint:        get("r2.endpoint"),
		Region:          get("r2.region"),
		BucketName:      get("r2.bucket_name"),
		PublicBaseURL:   get("r2.public_base_url"),
		AccessKeyID:     get("r2.access_key_id"),
		SecretAccessKey: get("r2.secret_access_key"),
	}
	if strings.TrimSpace(in.Endpoint) == "" {
		return nil
	}
	return client.Configure(in)
}

// startGrokStatsigRefresh wires grok's headless x-statsig-id refresher to the
// site-settings store (persisted across restarts) with an app-lifetime context.
func startGrokStatsigRefresh(siteRepo *repo.SiteSettingRepository) {
	const kHeader, kSuffix, kTrailer = "grok.statsig.header", "grok.statsig.suffix", "grok.statsig.trailer"
	grok.StartStatsigAutoRefresh(context.Background(), 0,
		func(ctx context.Context) (string, string, int, bool) {
			h, _ := siteRepo.GetValue(ctx, kHeader)
			s, _ := siteRepo.GetValue(ctx, kSuffix)
			tv, _ := siteRepo.GetValue(ctx, kTrailer)
			if h == "" || s == "" {
				return "", "", 0, false
			}
			t, _ := strconv.Atoi(tv)
			return h, s, t, true
		},
		func(ctx context.Context, h, s string, t int) {
			if err := siteRepo.UpsertValues(ctx, map[string]string{
				kHeader:  h,
				kSuffix:  s,
				kTrailer: strconv.Itoa(t),
			}); err != nil {
				log.Printf("grok statsig: persist recipe failed: %v", err)
			}
		},
	)
}

func (a *App) Close() error {
	if a.maintenanceCancel != nil {
		a.maintenanceCancel()
	}
	if a.Redis != nil {
		if err := a.Redis.Close(); err != nil {
			return err
		}
	}
	if a.DB != nil {
		sqlDB, err := a.DB.DB()
		if err != nil {
			return err
		}
		return sqlDB.Close()
	}
	return nil
}
