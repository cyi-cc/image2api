package router

import (
	"backend/internal/config"
	"backend/internal/http/handler"
	"backend/internal/http/middleware"
	"backend/internal/service"

	"github.com/gin-contrib/cors"
	"github.com/gin-gonic/gin"
)

type Handlers struct {
	Health        *handler.HealthHandler
	Images        *handler.ImageHandler
	V1            *handler.V1Handler
	Site          *handler.SiteHandler
	Showcase      *handler.ShowcaseHandler
	Auth          *handler.AuthHandler
	SiteSettings  *handler.SiteSettingsHandler
	AppSettings   *handler.AppSettingsHandler
	AdminRead     *handler.AdminReadHandler
	AdminWrite    *handler.AdminWriteHandler
	CDK           *handler.CDKHandler
	UserTools     *handler.UserToolsHandler
	UserGen       *handler.UserGenerationHandler
	ProviderAdmin *handler.ProviderAdminHandler
	ConcGroups    *handler.ConcurrencyGroupHandler
	Announcement  *handler.AnnouncementHandler
	Payment       *handler.PaymentHandler
	BannedWords   *handler.BannedWordsHandler
}

func New(cfg *config.Config, auth *service.AuthService, handlers Handlers) *gin.Engine {
	if cfg.AppEnv != "development" {
		gin.SetMode(gin.ReleaseMode)
	}

	engine := gin.New()
	engine.Use(gin.Recovery())
	engine.Use(middleware.RequestID())
	engine.Use(cors.New(cors.Config{
		AllowOrigins:     cfg.CORSOrigins,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Authorization", "Content-Type", "X-Request-Id"},
		AllowCredentials: true,
	}))

	engine.GET("/health", handlers.Health.Handle)
	engine.GET("/images/:user/:name", handlers.Images.Serve)
	engine.GET("/v1/models", handlers.V1.Models)
	engine.POST("/v1/images/generations", handlers.V1.ImageGenerations)
	engine.POST("/v1/images/edits", handlers.V1.ImageEdits)
	// OpenAI Sora-style async video: create job → poll → stream content.
	engine.POST("/v1/videos", handlers.V1.CreateVideo)
	engine.GET("/v1/videos/:id", handlers.V1.GetVideo)
	engine.GET("/v1/videos/:id/content", handlers.V1.GetVideoContent)

	publicAdmin := engine.Group("/admin/api")
	{
		publicAdmin.GET("/site", handlers.Site.Public)
		publicAdmin.GET("/showcase", handlers.Showcase.List)
		publicAdmin.GET("/managed-models", handlers.AdminRead.Models)
		publicAdmin.GET("/stats", handlers.AdminRead.Stats)
		publicAdmin.GET("/video-presets", handlers.UserGen.VideoPresets)
		publicAdmin.GET("/catalog", handlers.UserGen.Catalog)
		publicAdmin.GET("/models", handlers.UserGen.Models)
		// 易支付 async notify — called server-to-server by the pay platform, no auth.
		publicAdmin.GET("/pay/notify", handlers.Payment.Notify)
		publicAdmin.POST("/pay/notify", handlers.Payment.Notify)
	}

	authGroup := engine.Group("/admin/api/auth")
	{
		authGroup.GET("/config", handlers.Auth.Config)
		authGroup.POST("/send-code", handlers.Auth.SendCode)
		authGroup.POST("/register", handlers.Auth.Register)
		authGroup.POST("/login", handlers.Auth.Login)
		authGroup.POST("/logout", handlers.Auth.Logout)
		authGroup.POST("/reset-password", handlers.Auth.ResetPassword)
	}

	userAuthed := engine.Group("/admin/api")
	userAuthed.Use(middleware.RequireSession(auth, cfg))
	{
		userAuthed.GET("/logs", handlers.UserGen.Logs)
		userAuthed.POST("/generate", handlers.UserGen.Generate)
		userAuthed.POST("/test", handlers.UserGen.Test)
		userAuthed.GET("/jobs/mine", handlers.UserGen.MyJobs)
		userAuthed.GET("/my-images", handlers.UserGen.MyImages)
		userAuthed.DELETE("/my-files", handlers.UserGen.DeleteMyFile)
		userAuthed.GET("/announcement", handlers.Announcement.Get)
		userAuthed.POST("/announcement/seen", handlers.Announcement.MarkSeen)
		userAuthed.GET("/pay/config", handlers.Payment.Config)
		userAuthed.POST("/pay/recharge", handlers.Payment.Recharge)
		userAuthed.GET("/pay/orders", handlers.Payment.MyOrders)
		userAuthed.GET("/pay/orders/:id", handlers.Payment.OrderStatus)
		userAuthed.POST("/pay/orders/:id/continue", handlers.Payment.ContinueOrder)
	}

	authed := engine.Group("/admin/api")
	authed.Use(middleware.RequireAdminSession(auth, cfg))
	{
		authed.GET("/dashboard", handlers.AdminRead.Dashboard)
		authed.GET("/users", handlers.AdminRead.Users)
		authed.GET("/invites", handlers.AdminRead.Invites)
		authed.POST("/users", handlers.AdminWrite.CreateUser)
		authed.POST("/users/delete-bulk", handlers.AdminWrite.DeleteUsersBulk)
		authed.PATCH("/users/:user_id", handlers.AdminWrite.UpdateUser)
		authed.DELETE("/users/:user_id", handlers.AdminWrite.DeleteUser)
		authed.POST("/users/:user_id/credits", handlers.AdminWrite.AdjustUserCredits)
		authed.POST("/users/:user_id/api-keys", handlers.AdminWrite.CreateUserAPIKey)
		authed.DELETE("/users/:user_id/api-keys/:key_id", handlers.AdminWrite.DeleteUserAPIKey)
		authed.GET("/concurrency-groups", handlers.ConcGroups.List)
		authed.POST("/concurrency-groups", handlers.ConcGroups.Create)
		authed.PATCH("/concurrency-groups/:id", handlers.ConcGroups.Update)
		authed.POST("/concurrency-groups/:id/default", handlers.ConcGroups.SetDefault)
		authed.DELETE("/concurrency-groups/:id", handlers.ConcGroups.Delete)
		authed.GET("/pay/admin/orders", handlers.Payment.AdminOrders)
		authed.GET("/cdks", handlers.CDK.List)
		authed.POST("/cdks", handlers.CDK.Create)
		authed.POST("/cdks/delete-bulk", handlers.CDK.DeleteBulk)
		authed.DELETE("/cdks/:code", handlers.CDK.Delete)
		authed.GET("/tokens", handlers.ProviderAdmin.TokensList)
		authed.POST("/tokens", handlers.ProviderAdmin.TokensCreate)
		authed.POST("/tokens/import-chatgpt-token", handlers.ProviderAdmin.ImportChatGPTToken)
		authed.POST("/tokens/import-adobe-cookie", handlers.ProviderAdmin.ImportAdobeCookie)
		authed.POST("/tokens/import-runway-token", handlers.ProviderAdmin.ImportRunwayToken)
		authed.POST("/tokens/import-leonardo-cookie", handlers.ProviderAdmin.ImportLeonardoCookie)
		authed.POST("/tokens/import-krea-cookie", handlers.ProviderAdmin.ImportKreaCookie)
		authed.POST("/tokens/import-imagine-token", handlers.ProviderAdmin.ImportImagineToken)
		authed.POST("/tokens/import-grok-token", handlers.ProviderAdmin.ImportGrokToken)
		authed.POST("/tokens/import-custom-account", handlers.ProviderAdmin.ImportCustomAccount)
		authed.POST("/tokens/delete-bulk", handlers.ProviderAdmin.TokenDeleteBulk)
		authed.PATCH("/tokens/:pool/:id", handlers.ProviderAdmin.TokenUpdate)
		authed.DELETE("/tokens/:pool/:id", handlers.ProviderAdmin.TokenDelete)
		authed.GET("/accounts", handlers.ProviderAdmin.AccountsList)
		authed.GET("/accounts/:pool/:id/quota", handlers.ProviderAdmin.AccountQuota)
		authed.GET("/accounts/:pool/:id/email", handlers.ProviderAdmin.AccountEmail)
		authed.GET("/providers", handlers.AdminRead.Providers)
		authed.GET("/images", handlers.AdminRead.Images)
		authed.DELETE("/images", handlers.AdminRead.DeleteImage)
		authed.GET("/banned-words", handlers.BannedWords.List)
		authed.POST("/banned-words", handlers.BannedWords.Create)
		authed.POST("/banned-words/import", handlers.BannedWords.Import)
		authed.DELETE("/banned-words/:id", handlers.BannedWords.Delete)
		authed.GET("/banned-word-hits", handlers.BannedWords.Hits)
		authed.GET("/refresh/profiles", handlers.ProviderAdmin.RefreshProfiles)
		authed.POST("/refresh/profiles/:profile_id/refresh-now", handlers.ProviderAdmin.RefreshNow)
		authed.PATCH("/refresh/profiles/:profile_id", handlers.ProviderAdmin.RefreshUpdate)
		authed.DELETE("/refresh/profiles/:profile_id", handlers.ProviderAdmin.RefreshDelete)
		authed.POST("/managed-models", handlers.AdminWrite.CreateModel)
		authed.PATCH("/managed-models/:model_id", handlers.AdminWrite.UpdateModel)
		authed.DELETE("/managed-models/:model_id", handlers.AdminWrite.DeleteModel)
		authed.DELETE("/logs", handlers.AdminWrite.ClearLogs)
		authed.DELETE("/logs/pending", handlers.AdminWrite.ClearPendingLogs)
		authed.POST("/showcase", handlers.AdminWrite.CreateShowcase)
		authed.PATCH("/showcase/:entry_id", handlers.AdminWrite.UpdateShowcase)
		authed.DELETE("/showcase/:entry_id", handlers.AdminWrite.DeleteShowcase)

		settings := authed.Group("/settings")
		{
			settings.GET("/site", handlers.SiteSettings.Get)
			settings.PUT("/site", handlers.SiteSettings.Put)
			settings.POST("/logo", handlers.AppSettings.LogoUpload)
			settings.DELETE("/logo", handlers.AppSettings.LogoDelete)
			settings.POST("/asset", handlers.AppSettings.AssetUpload)
			settings.GET("/registration", handlers.AppSettings.RegistrationGet)
			settings.PUT("/registration", handlers.AppSettings.RegistrationPut)
			settings.GET("/smtp", handlers.AppSettings.SMTPGet)
			settings.PUT("/smtp", handlers.AppSettings.SMTPPut)
			settings.POST("/smtp/test", handlers.AppSettings.SMTPTest)
			settings.GET("/proxy", handlers.AppSettings.ProxyGet)
			settings.PUT("/proxy", handlers.AppSettings.ProxyPut)
			settings.POST("/proxy/test", handlers.AppSettings.ProxyTest)
			settings.GET("/credits", handlers.AppSettings.CreditsGet)
			settings.PUT("/credits", handlers.AppSettings.CreditsPut)
			settings.GET("/logs", handlers.AppSettings.LogsGet)
			settings.PUT("/logs", handlers.AppSettings.LogsPut)
			settings.GET("/media", handlers.AppSettings.MediaGet)
			settings.PUT("/media", handlers.AppSettings.MediaPut)
			settings.GET("/announcement", handlers.Announcement.AdminGet)
			settings.PUT("/announcement", handlers.Announcement.AdminPut)
			settings.GET("/pay", handlers.Payment.SettingsGet)
			settings.PUT("/pay", handlers.Payment.SettingsSave)
		}
	}

	authGroup.Use(middleware.RequireSession(auth, cfg))
	{
		authGroup.GET("/me", handlers.Auth.Me)
		authGroup.GET("/invites", handlers.Auth.Invites)
		authGroup.POST("/checkin", handlers.Auth.Checkin)
		authGroup.POST("/change-password", handlers.Auth.ChangePassword)
		authGroup.GET("/api-key", handlers.UserTools.APIKeyGet)
		authGroup.POST("/api-key", handlers.UserTools.APIKeyMint)
		authGroup.DELETE("/api-key", handlers.UserTools.APIKeyDelete)
		authGroup.POST("/redeem-cdk", handlers.UserTools.RedeemCDK)
	}

	return engine
}
