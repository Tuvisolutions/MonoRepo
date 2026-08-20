package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/rajchodisetti/restaurant-platform/backend/internal/analytics"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/auth"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/campaigns"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/consultations"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/demos"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/developer"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/http/handlers"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/jobs"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/leadreview"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/media"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/outreach"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/outreachaccounts"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/platform/config"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/platform/db"
	emailprovider "github.com/rajchodisetti/restaurant-platform/backend/internal/providers/email"
	llmprovider "github.com/rajchodisetti/restaurant-platform/backend/internal/providers/llm"
	placesprovider "github.com/rajchodisetti/restaurant-platform/backend/internal/providers/places"
	storageprovider "github.com/rajchodisetti/restaurant-platform/backend/internal/providers/storage"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/reservations"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/restaurants"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/scrapejobs"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/seoreport"
	"github.com/rajchodisetti/restaurant-platform/backend/internal/store"
)

type ReadinessChecker interface {
	Ping(ctx context.Context) error
}

func NewRouter(log *slog.Logger, readiness ReadinessChecker, dataStore *store.Store, cfg config.Config) http.Handler {
	mux := http.NewServeMux()

	tokenManager := auth.NewTokenManager(cfg.Token.Secret, cfg.Token.AccessTokenTTL)
	authService := auth.NewService(dataStore.Users, tokenManager)
	accessService := restaurants.NewService(dataStore.Restaurants, dataStore.Memberships)
	demoService := demos.NewService(dataStore.Demos, accessService, cfg.Demo.TokenTTL)
	placesClient := placesprovider.NewClient(cfg.Places)
	objectStore, storageErr := storageprovider.New(context.Background(), cfg.Storage)
	if storageErr != nil {
		log.WarnContext(context.Background(), "restaurant_media_storage_unavailable", "error", storageErr)
		objectStore = storageprovider.Disabled{}
	}
	mediaService := media.NewService(
		media.NewPostgres(dataStore.Pool()),
		dataStore.Profiles,
		placesClient,
		objectStore,
		log,
	)
	demoEngagementService := analytics.NewService(demoService, analytics.NewPostgres(dataStore.Pool()))

	jobQueue := jobs.NewPostgresQueue(dataStore.Pool(), cfg.Jobs.BufferSize, cfg.Jobs.RetryDelay)
	var jobEnqueuer campaigns.SendJobEnqueuer = &jobs.CampaignEnqueuer{Queue: jobQueue}
	if dataStore.Pool() == nil {
		jobEnqueuer = &jobs.CampaignEnqueuer{Queue: jobs.NewInMemoryQueue(cfg.Jobs.BufferSize)}
	}
	campaignService := campaigns.NewService(dataStore.Campaigns, dataStore.Demos, accessService, jobEnqueuer, cfg.AppURLs, cfg.Demo.TokenTTL)
	emailProvider, err := emailprovider.NewFromConfig(cfg.Email, cfg.ZohoMail)
	if err != nil {
		log.ErrorContext(context.Background(), "consultation_email_provider_unavailable", "error", err)
		emailProvider = emailprovider.NewDisabled()
	}
	consultationService := consultations.NewService(cfg.Consultations, dataStore.Consultations, emailProvider, log)

	authHandler := handlers.NewAuthHandler(authService, cfg.App.Env, writeJSON, writeError)
	adminHandler := handlers.NewAdminHandler(dataStore.Users, writeJSON, writeError)
	userHandler := handlers.NewUserHandler(dataStore.Users, dataStore.Restaurants, dataStore.Memberships, writeJSON, writeError)
	restaurantHandler := handlers.NewRestaurantHandler(accessService, writeJSON, writeError)
	demoPublicHandler := handlers.NewDemoPublicHandler(demoService, mediaService, writeJSON, writeError)
	demoAdminHandler := handlers.NewDemoAdminHandler(demoService, writeJSON, writeError)
	demoEngagementHandler := handlers.NewDemoEngagementHandler(demoEngagementService, writeJSON, writeError)
	campaignHandler := handlers.NewCampaignHandler(campaignService, writeJSON, writeError)
	outreachRepo := outreach.NewPostgres(dataStore.Pool())
	accountStore := outreachaccounts.NewPostgres(dataStore.Pool())
	accountService := outreachaccounts.NewService(accountStore, cfg.Outreach, cfg.Outreach.CredentialEncryptionKey, log)
	emailHealthService := emailprovider.NewReloadingHealthService(cfg.Email, accountService, outreachRepo)
	outreachAccountPool := emailprovider.NewReloadingPersistentAccountPool(cfg.Email, accountService, outreachRepo)
	outreachService := outreach.NewService(
		outreachRepo,
		dataStore.Pool(),
		dataStore.Campaigns,
		campaignService,
		accessService,
		outreach.DemoTokenResolver{Campaigns: dataStore.Campaigns, Demos: dataStore.Demos},
		outreachAccountPool,
		emailProvider,
		cfg.Email,
		cfg.Outreach,
		cfg.AppURLs,
		&jobs.OutreachBulkEnqueuer{Queue: jobQueue},
		log,
	)
	outreachBulkHandler := handlers.NewOutreachBulkHandler(outreachService, writeJSON, writeError)
	emailHealthHandler := handlers.NewEmailHealthHandler(emailHealthService, cfg.Outreach, writeJSON, writeError)
	emailAccountsHandler := handlers.NewOutreachEmailAccountsHandler(accountService, writeJSON, writeError)
	scrapeJobRepo := scrapejobs.NewPostgres(dataStore.Pool())
	scrapeJobService := scrapejobs.NewService(scrapeJobRepo)
	scrapeJobHandler := handlers.NewScrapeJobHandler(scrapeJobService, writeJSON, writeError)
	leadReviewService := leadreview.NewService(dataStore.Pool())
	leadReviewHandler := handlers.NewLeadReviewHandler(leadReviewService, writeJSON, writeError)
	trackingHandler := handlers.NewTrackingHandler(dataStore.Campaigns, dataStore.Restaurants, writeError)
	restaurantPublicHandler := handlers.NewRestaurantPublicHandler(dataStore.Profiles, mediaService, writeJSON, writeError)
	restaurantImagesAdminHandler := handlers.NewRestaurantImagesAdminHandler(
		dataStore.Profiles,
		placesClient,
		mediaService,
		writeJSON,
		writeError,
	)
	restaurantSiteAdminHandler := handlers.NewRestaurantSiteAdminHandler(
		dataStore.Profiles,
		cfg.AppURLs.PublicWebURL,
		writeJSON,
		writeError,
	)
	reservationService := reservations.NewService(dataStore.Reservations)
	reservationPublicHandler := handlers.NewReservationPublicHandler(reservationService, writeJSON, writeError)
	companyConsultationHandler := handlers.NewCompanyConsultationHandler(consultationService, writeJSON)
	companyConsultationAdminHandler := handlers.NewCompanyConsultationAdminHandler(
		consultationService,
		writeJSON,
		writeError,
	)
	developerHandler := handlers.NewDeveloperHandler(
		developer.NewService(dataStore.Pool()),
		writeJSON,
		writeError,
	)
	var interestedRepo seoreport.InterestedRepository
	if pool := dataStore.Pool(); pool != nil {
		interestedRepo = seoreport.NewInterestedPostgres(pool)
	}
	// Prefer the same Google Workspace mailbox used for outreach sending.
	seoMailer := emailProvider
	if len(cfg.Outreach.GoogleWorkspaceAccounts) > 0 {
		gmailMailer, gmailErr := emailprovider.NewGmail(cfg.Email, cfg.Outreach.GoogleWorkspaceAccounts[0])
		if gmailErr != nil {
			log.WarnContext(context.Background(), "seo_gmail_mailer_unavailable", "error", gmailErr)
		} else {
			seoMailer = gmailMailer
			log.InfoContext(context.Background(), "seo_unlock_mailer", "provider", "gmail", "from", cfg.Outreach.GoogleWorkspaceAccounts[0].FromEmail)
		}
	}
	seoService := seoreport.NewServiceFull(
		cfg.Places,
		cfg.App,
		dataStore.Profiles,
		interestedRepo,
		seoMailer,
		llmprovider.NewFromConfig(cfg.LLM),
		cfg.Token.Secret,
		log,
	)
	seoPublicHandler := handlers.NewSEOPublicHandler(seoService, writeJSON, writeError)
	contactPublicHandler := handlers.NewContactPublicHandler(
		seoMailer,
		cfg.Consultations.NotifyEmail,
		writeJSON,
		writeError,
	)

	mux.HandleFunc("POST /api/v1/auth/signup", authHandler.Signup)
	mux.HandleFunc("POST /api/v1/auth/login", authHandler.Login)

	protectAuthenticated := RequireAuth(tokenManager)
	protectDeveloper := func(next http.Handler) http.Handler {
		return protectAuthenticated(RequireRole(auth.RoleDeveloper)(next))
	}
	protectInternalAdmin := func(next http.Handler) http.Handler {
		return protectAuthenticated(RequireRole(auth.RoleInternalAdmin)(next))
	}
	protectRestaurantUser := func(next http.Handler) http.Handler {
		return protectAuthenticated(RequireAnyRole(auth.RoleRestaurantOwner, auth.RoleDeveloper)(next))
	}
	protectRestaurantScoped := func(next http.Handler) http.Handler {
		return protectAuthenticated(RequireAnyRole(auth.RoleInternalAdmin, auth.RoleRestaurantOwner)(
			RequireRestaurantAccess(accessService)(next),
		))
	}
	protectRestaurantAdmin := func(next http.Handler) http.Handler {
		return protectAuthenticated(RequireRole(auth.RoleInternalAdmin)(
			RequireRestaurantAccess(accessService)(next),
		))
	}
	protectConsultationAPI := RequireStaticBearerToken(cfg.Consultations.APIToken)

	mux.Handle("GET /api/v1/auth/me", protectAuthenticated(http.HandlerFunc(authHandler.Me)))
	mux.Handle("GET /healthz", protectDeveloper(http.HandlerFunc(healthz(cfg))))
	mux.Handle("GET /readyz", protectDeveloper(http.HandlerFunc(readyz(readiness))))
	mux.Handle("GET /api/v1/admin/me", protectInternalAdmin(http.HandlerFunc(adminHandler.Me)))
	mux.Handle("GET /api/v1/admin/consultation-calendar/{month}", protectInternalAdmin(http.HandlerFunc(companyConsultationAdminHandler.GetCalendar)))
	mux.Handle("PUT /api/v1/admin/consultation-calendar/{month}", protectInternalAdmin(http.HandlerFunc(companyConsultationAdminHandler.PutCalendar)))
	mux.Handle("GET /api/v1/user/me", protectRestaurantUser(http.HandlerFunc(userHandler.Me)))

	mux.Handle("GET /api/v1/restaurants", protectAuthenticated(RequireAnyRole(auth.RoleInternalAdmin, auth.RoleRestaurantOwner)(
		http.HandlerFunc(restaurantHandler.List),
	)))
	mux.Handle("GET /api/v1/restaurants/shared-emails", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.ListSharedEmailGroups)))
	mux.Handle("POST /api/v1/restaurants", protectInternalAdmin(http.HandlerFunc(restaurantHandler.Create)))
	mux.Handle("GET /api/v1/restaurants/{id}", protectRestaurantScoped(http.HandlerFunc(restaurantHandler.Get)))
	mux.Handle("PATCH /api/v1/restaurants/{id}", protectRestaurantAdmin(http.HandlerFunc(restaurantHandler.Update)))
	mux.Handle("PATCH /api/v1/restaurants/{id}/status", protectRestaurantAdmin(http.HandlerFunc(restaurantHandler.UpdateStatus)))
	mux.Handle("DELETE /api/v1/restaurants/{id}", protectRestaurantAdmin(http.HandlerFunc(restaurantHandler.Archive)))
	mux.Handle("GET /api/v1/restaurants/{id}/members", protectRestaurantAdmin(http.HandlerFunc(restaurantHandler.ListMembers)))
	mux.Handle("POST /api/v1/restaurants/{id}/members", protectRestaurantAdmin(http.HandlerFunc(restaurantHandler.AddMember)))
	mux.Handle("POST /api/v1/restaurants/{id}/demo-sites", protectRestaurantAdmin(http.HandlerFunc(demoAdminHandler.Create)))
	mux.Handle("GET /api/v1/demo-sites/{id}/review-preview", protectInternalAdmin(http.HandlerFunc(demoAdminHandler.ReviewPreview)))
	mux.Handle("GET /api/v1/restaurants/{id}/profile/review-preview", protectRestaurantAdmin(http.HandlerFunc(leadReviewHandler.GetProfileReviewPreview)))
	mux.Handle("PATCH /api/v1/restaurants/{id}/profile/review", protectRestaurantAdmin(http.HandlerFunc(leadReviewHandler.ReviewProfile)))
	mux.Handle("PATCH /api/v1/demo-sites/{id}/status", protectInternalAdmin(http.HandlerFunc(leadReviewHandler.SetDemoStatus)))
	mux.Handle("POST /api/v1/outreach/bulk-send", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.Trigger)))
	mux.Handle("PATCH /api/v1/outreach/email-job", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.SetEmailJob)))
	mux.Handle("PATCH /api/v1/outreach/send-window", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.SetSendSchedule)))
	mux.Handle("GET /api/v1/outreach/bulk-send/status", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.Status)))
	mux.Handle("GET /api/v1/outreach/deliveries", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.ListDeliveries)))
	mux.Handle("GET /api/v1/outreach/sequences", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.ListSequences)))
	mux.Handle("POST /api/v1/outreach/sequences", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.CreateSequence)))
	mux.Handle("PUT /api/v1/outreach/sequences/{id}", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.UpdateSequence)))
	mux.Handle("POST /api/v1/outreach/sequences/{id}/approve", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.ApproveSequence)))
	mux.Handle("POST /api/v1/outreach/sequences/{id}/preview", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.PreviewSequence)))
	mux.Handle("GET /api/v1/outreach/recipients", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.ListRecipients)))
	mux.Handle("POST /api/v1/outreach/test-send", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.SendTemplateTest)))
	mux.Handle("GET /api/v1/outreach/email-accounts", protectInternalAdmin(http.HandlerFunc(emailAccountsHandler.List)))
	mux.Handle("POST /api/v1/outreach/email-accounts", protectInternalAdmin(http.HandlerFunc(emailAccountsHandler.Create)))
	mux.Handle("PATCH /api/v1/outreach/email-accounts/{id}", protectInternalAdmin(http.HandlerFunc(emailAccountsHandler.Update)))
	mux.Handle("GET /api/v1/outreach/email-accounts/health", protectInternalAdmin(http.HandlerFunc(emailHealthHandler.Status)))
	mux.Handle("GET /api/v1/outreach/inbox", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.ListInbox)))
	mux.Handle("POST /api/v1/outreach/messages/{id}/read", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.MarkMessageRead)))
	mux.Handle("POST /api/v1/outreach/messages/{id}/reply", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.ReplyToInboxMessage)))
	mux.Handle("GET /api/v1/restaurants/{id}/messages", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.ListRestaurantMessages)))
	mux.Handle("GET /api/v1/restaurants/{id}/outreach-greeting", protectInternalAdmin(http.HandlerFunc(outreachBulkHandler.PreviewRestaurantGreeting)))
	mux.Handle("GET /api/v1/developer/schema", protectInternalAdmin(http.HandlerFunc(developerHandler.Schema)))
	mux.Handle("POST /api/v1/developer/sql", protectInternalAdmin(http.HandlerFunc(developerHandler.ExecuteSQL)))
	mux.Handle("POST /api/v1/scrape-jobs", protectInternalAdmin(http.HandlerFunc(scrapeJobHandler.Trigger)))
	mux.Handle("GET /api/v1/scrape-jobs", protectInternalAdmin(http.HandlerFunc(scrapeJobHandler.List)))
	mux.Handle("GET /api/v1/scrape-jobs/{id}", protectInternalAdmin(http.HandlerFunc(scrapeJobHandler.Get)))
	mux.Handle("POST /api/v1/scrape-jobs/{id}/resume", protectInternalAdmin(http.HandlerFunc(scrapeJobHandler.Resume)))
	mux.Handle("POST /api/v1/scrape-jobs/{id}/retry", protectInternalAdmin(http.HandlerFunc(scrapeJobHandler.Retry)))

	mux.Handle("GET /api/v1/restaurants/{id}/images", protectRestaurantAdmin(http.HandlerFunc(restaurantImagesAdminHandler.List)))
	mux.Handle("GET /api/v1/restaurants/{id}/images/google", protectRestaurantAdmin(http.HandlerFunc(restaurantImagesAdminHandler.ListGoogle)))
	mux.Handle("POST /api/v1/restaurants/{id}/media", protectRestaurantAdmin(http.HandlerFunc(restaurantImagesAdminHandler.Upload)))
	mux.Handle("PATCH /api/v1/restaurants/{id}/media/{assetId}/review", protectInternalAdmin(http.HandlerFunc(restaurantImagesAdminHandler.ReviewOwned)))
	mux.Handle("DELETE /api/v1/restaurants/{id}/media/{assetId}", protectRestaurantAdmin(http.HandlerFunc(restaurantImagesAdminHandler.HideOwned)))
	mux.Handle("POST /api/v1/restaurants/{id}/media/{assetId}/restore", protectRestaurantAdmin(http.HandlerFunc(restaurantImagesAdminHandler.RestoreOwned)))
	mux.Handle("DELETE /api/v1/restaurants/{id}/images/{kind}/{imageId}", protectRestaurantAdmin(http.HandlerFunc(restaurantImagesAdminHandler.Hide)))
	mux.Handle("POST /api/v1/restaurants/{id}/images/{kind}/{imageId}/restore", protectRestaurantAdmin(http.HandlerFunc(restaurantImagesAdminHandler.Unhide)))
	mux.Handle("GET /api/v1/restaurants/{id}/demo-links", protectRestaurantAdmin(http.HandlerFunc(campaignHandler.ListDemoLinks)))
	mux.Handle("GET /api/v1/restaurants/{id}/generated-site", protectRestaurantAdmin(http.HandlerFunc(restaurantSiteAdminHandler.Get)))
	mux.Handle("GET /api/v1/restaurants/{id}/demo-engagement", protectRestaurantAdmin(http.HandlerFunc(demoEngagementHandler.ListByRestaurant)))
	mux.Handle("POST /api/v1/restaurants/{id}/demo-engagement/preview", protectRestaurantAdmin(http.HandlerFunc(demoEngagementHandler.StartAdminPreview)))
	mux.HandleFunc("GET /t/click/{token}", trackingHandler.Click)
	mux.HandleFunc("GET /t/open/{token}", trackingHandler.Open)

	mux.HandleFunc("GET /api/public/v1/demo/{slug}", demoPublicHandler.Get)
	mux.HandleFunc("POST /api/public/v1/demo/{slug}/sessions", demoEngagementHandler.Start)
	mux.HandleFunc("POST /api/public/v1/demo-sessions/{session_id}/events", demoEngagementHandler.Touch)
	mux.HandleFunc("POST /api/public/v1/demo-sessions/{session_id}/transcript", demoEngagementHandler.Transcript)
	mux.HandleFunc("GET /api/public/v1/restaurants/{id}/site-images", restaurantPublicHandler.GetSiteImagesByID)
	mux.HandleFunc("GET /api/public/v1/restaurants/by-place/{place_id}/site-images", restaurantPublicHandler.GetSiteImagesByPlaceID)
	mux.HandleFunc("GET /api/public/v1/site/restaurants", restaurantPublicHandler.ListSiteRestaurants)
	mux.HandleFunc("GET /api/public/v1/site/restaurants/by-id/{id}", restaurantPublicHandler.GetSiteContentByID)
	mux.HandleFunc("GET /api/public/v1/site/restaurants/{index}", restaurantPublicHandler.GetSiteContentByIndex)
	mux.HandleFunc("GET /api/public/v1/site/by-place/{place_id}", restaurantPublicHandler.GetSiteContentByPlaceID)
	mux.HandleFunc("GET /api/public/v1/seo/search", seoPublicHandler.Search)
	mux.HandleFunc("GET /api/public/v1/seo/report/{place_id}", seoPublicHandler.Report)
	mux.HandleFunc("GET /api/public/v1/seo/photo", seoPublicHandler.Photo)
	mux.HandleFunc("POST /api/public/v1/seo/unlock/request", seoPublicHandler.RequestUnlock)
	mux.HandleFunc("POST /api/public/v1/seo/unlock/verify", seoPublicHandler.VerifyUnlock)
	mux.HandleFunc("POST /api/public/v1/contact", contactPublicHandler.Submit)
	mux.HandleFunc("GET /api/public/v1/restaurants/{id}/table-availability", reservationPublicHandler.GetTableAvailability)
	mux.HandleFunc("PUT /api/public/v1/restaurants/{id}/reservations", reservationPublicHandler.PutReservation)

	registerCompanyConsultationRoutes(mux, "/api/v1/company/consultations", protectConsultationAPI, companyConsultationHandler)
	registerCompanyConsultationRoutes(mux, "/api/v1/consultations", protectConsultationAPI, companyConsultationHandler)

	var handler http.Handler = mux
	handler = Recovery(log)(handler)
	handler = CORS(cfg.HTTP.CORSAllowedOrigins)(handler)
	handler = AccessLog(log)(handler)
	handler = RequestID()(handler)

	return handler
}

func registerCompanyConsultationRoutes(
	mux *http.ServeMux,
	prefix string,
	protect func(http.Handler) http.Handler,
	handler *handlers.CompanyConsultationHandler,
) {
	mux.Handle("GET "+prefix+"/availability", protect(http.HandlerFunc(handler.GetAvailability)))
	mux.Handle("GET "+prefix+"/availability/check", protect(http.HandlerFunc(handler.CheckAvailability)))
	mux.Handle("POST "+prefix, protect(http.HandlerFunc(handler.Book)))
}

func RequireStaticBearerToken(token string) func(http.Handler) http.Handler {
	expected := "Bearer " + token
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != expected {
				writeJSON(w, http.StatusUnauthorized, map[string]any{
					"status":  "error",
					"message": "unauthorized",
				})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func healthz(cfg config.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":  "ok",
			"service": cfg.App.Name,
			"env":     cfg.App.Env,
			"version": cfg.App.Version,
		})
	}
}

func readyz(readiness ReadinessChecker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if readiness == nil {
			writeError(w, http.StatusServiceUnavailable, "database_not_configured", "Database readiness is not configured.")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()

		if err := readiness.Ping(ctx); err != nil {
			code := "database_unavailable"
			message := "Database is not ready."
			if errors.Is(err, db.ErrNotConfigured) {
				code = "database_not_configured"
				message = "Database URL is not configured."
			}
			writeError(w, http.StatusServiceUnavailable, code, message)
			return
		}

		writeJSON(w, http.StatusOK, map[string]any{
			"status":   "ok",
			"database": "ready",
		})
	}
}
