package routes

import (
	"fmt"
	"net/http"

	"aliang.one/nursorgate/app/http/handlers"
	"aliang.one/nursorgate/app/http/repositories"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/statistic"
)

var isDev = true

// Handlers holds all HTTP handler instances
type Handlers struct {
	Logger         *handlers.LogHandler
	ProxyRegistry  *handlers.ProxyRegistryHandler
	SoftwareCfg    *handlers.SoftwareConfigHandler
	Token          *handlers.TokenHandler
	Run            *handlers.RunHandler
	SystemService  *handlers.SystemServiceHandler
	LogStream      *handlers.LogStreamHandler
	SoftwareUpdate *handlers.SoftwareUpdateHandler
	Rules          *handlers.RulesHandler
	DNSCache       *handlers.DNSCacheHandler
	Cert           *handlers.CertHandler
	Auth           *handlers.AuthHandler
	Startup        *handlers.StartupHandler
	Config         *handlers.ConfigHandler
	TrafficStats   *handlers.TrafficStatsHandler
	HTTPStats      *handlers.HTTPStatsHandler
	Status         *handlers.StatusHandler
	Chat           *handlers.ChatHandler
	UserCenter     *handlers.UserCenterHandler
	Dashboard      *handlers.DashboardHandler
	QuickSetup     *handlers.QuickSetupHandler
	Tutorial       *handlers.TutorialHandler

	statsCollector     *statistic.StatsCollector
	httpStatsCollector *statistic.HTTPStatsCollector
}

// NewHandlers creates and initializes all handlers with their dependencies
func NewHandlers() *Handlers {
	return newHandlers(services.NewRunService())
}

// NewHandlersWithRunService creates handlers using a caller-provided run service.
func NewHandlersWithRunService(runService *services.RunService) *Handlers {
	if runService == nil {
		runService = services.NewRunService()
	}
	return newHandlers(runService)
}

func newHandlers(runService *services.RunService) *Handlers {
	logService := services.NewLogService()
	logConfigService := services.NewLogConfigService()
	tokenService := services.NewTokenService()
	softwareCfgService := services.NewSoftwareConfigService()
	certService := services.NewCertService()
	softwareUpdateService := services.GetSharedSoftwareUpdateService()
	proxyRepository := repositories.NewProxyRepository()
	statsCollector := statistic.NewStatsCollector()
	httpStatsCollector := statistic.GetDefaultHTTPStatsCollector()
	aiActivityTracker := statistic.GetDefaultAIActivityTracker()

	return &Handlers{
		Logger:             handlers.NewLogHandler(logService, logConfigService),
		ProxyRegistry:      handlers.NewProxyRegistryHandler(proxyRepository),
		SoftwareCfg:        handlers.NewSoftwareConfigHandler(softwareCfgService),
		Token:              handlers.NewTokenHandler(tokenService),
		Run:                handlers.NewRunHandler(runService),
		SystemService:      handlers.NewSystemServiceHandler(services.NewSystemServiceService()),
		LogStream:          handlers.NewLogStreamHandler(),
		SoftwareUpdate:     handlers.NewSoftwareUpdateHandler(softwareUpdateService),
		Rules:              handlers.NewRulesHandler(),
		DNSCache:           handlers.NewDNSCacheHandler(),
		Cert:               handlers.NewCertHandler(certService),
		Auth:               handlers.NewAuthHandler(),
		Startup:            handlers.NewStartupHandler(),
		Config:             handlers.NewConfigHandler(),
		TrafficStats:       handlers.NewTrafficStatsHandler(statsCollector),
		HTTPStats:          handlers.NewHTTPStatsHandler(httpStatsCollector),
		Status:             handlers.NewStatusHandler(statsCollector, httpStatsCollector, aiActivityTracker),
		Chat:               handlers.NewChatHandler(),
		UserCenter:         handlers.NewUserCenterHandler(),
		Dashboard:          handlers.NewDashboardHandler(),
		QuickSetup:         handlers.NewQuickSetupHandler(),
		Tutorial:           handlers.NewTutorialHandler(),
		statsCollector:     statsCollector,
		httpStatsCollector: httpStatsCollector,
	}
}

// RegisterRoutes registers all feature-grouped HTTP routes
func RegisterRoutes(h *Handlers, mux *http.ServeMux) {
	catalog := newRouteCatalog()
	register := func(path string, handler http.HandlerFunc, methods ...string) {
		mux.HandleFunc(path, handler)
		catalog.add(path, methods...)
	}

	// Logger routes (/api/logs/*)
	register("/api/logs", h.Logger.HandleGetLogs, http.MethodGet)
	register("/api/logs/clear", h.Logger.HandleClearLogs, http.MethodPost)
	register("/api/logs/level", h.Logger.HandleSetLogLevel, http.MethodPost)
	register("/api/logs/config", h.Logger.HandleLogConfig, http.MethodGet, http.MethodPost)
	register("/api/logs/stream", h.LogStream.HandleLogStream, http.MethodGet)

	// Proxy registry routes (/api/proxy/registry/*)
	register("/api/proxy/list", h.ProxyRegistry.HandleProxyRegistryList, http.MethodGet)
	register("/api/proxy/get", h.ProxyRegistry.HandleProxyRegistryGet, http.MethodGet)

	register("/api/software-config/save", h.SoftwareCfg.HandleSave, http.MethodPost)
	register("/api/software-config/activate", h.SoftwareCfg.HandleActivate, http.MethodPost)
	register("/api/software-config/delete", h.SoftwareCfg.HandleDelete, http.MethodPost)
	register("/api/software-config/list", h.SoftwareCfg.HandleList, http.MethodGet)
	register("/api/software-config/select", h.SoftwareCfg.HandleSelect, http.MethodPost)
	register("/api/software-config/compare", h.SoftwareCfg.HandleCompareWithCloud, http.MethodPost)
	register("/api/software-config/log", h.SoftwareCfg.HandleLogOperation, http.MethodPost)
	register("/api/software-config/cloud/push", h.SoftwareCfg.HandlePushToCloud, http.MethodPost)
	register("/api/software-config/cloud/push-selected", h.SoftwareCfg.HandlePushSelectedToCloud, http.MethodPost)
	register("/api/software-config/cloud/pull", h.SoftwareCfg.HandlePullFromCloud, http.MethodPost)

	// Token routes (/api/token/*)
	register("/api/token/get", h.Token.HandleTokenGet, http.MethodGet)
	register("/api/token/set", h.Token.HandleTokenSet, http.MethodPost)

	// Authentication routes (/api/auth/*)
	register("/api/auth/login", h.Auth.HandleLogin, http.MethodPost)
	register("/api/auth/session", h.Auth.HandleRestoreSession, http.MethodGet)
	register("/api/auth/refresh", h.Auth.HandleRefreshSession, http.MethodPost)
	register("/api/auth/me", h.Auth.HandleMe, http.MethodGet)
	register("/api/auth/logout", h.Auth.HandleLogout, http.MethodPost)

	// Run mode routes (/api/run/*)
	register("/api/run/start", h.Run.HandleRunStart, http.MethodPost)
	register("/api/run/stop", h.Run.HandleRunStop, http.MethodPost)
	register("/api/run/status", h.Run.HandleRunStatus, http.MethodGet)
	register("/api/run/aliang/status", h.Run.HandleAliangLinkStatus, http.MethodGet)
	register("/api/run/wintun/install", h.Run.HandleRunWintunInstall, http.MethodPost)
	register("/api/run/wintun/status", h.Run.HandleRunWintunStatus, http.MethodGet)
	register("/api/run/tun/status", h.Run.HandleRunTUNStatus, http.MethodGet)
	register("/api/run/tun/conflicts", h.Run.HandleRunTUNConflictStatus, http.MethodGet)
	register("/api/run/swift", h.Run.HandleRunSwift, http.MethodPost)
	register("/api/software-update/status", h.SoftwareUpdate.HandleStatus, http.MethodGet)
	register("/api/software-update/dismiss", h.SoftwareUpdate.HandleDismiss, http.MethodPost)

	// Core service lifecycle routes

	// System service routes (/api/system/service/*)
	register("/api/system/service/status", h.SystemService.HandleStatus, http.MethodGet)
	register("/api/system/service/install", h.SystemService.HandleInstall, http.MethodPost)
	register("/api/system/service/uninstall", h.SystemService.HandleUninstall, http.MethodPost)

	// Routing Rules API (/api/rules/*)
	register("/api/rules/geoip/status", h.Rules.HandleGetGeoIPStatus, http.MethodGet)
	register("/api/rules/geoip/lookup", h.Rules.HandleGeoIPLookup, http.MethodPost)
	register("/api/rules/cache/stats", h.Rules.HandleGetCacheStats, http.MethodGet)
	register("/api/rules/cache/clear", h.Rules.HandleClearCache, http.MethodPost)
	register("/api/rules/engine/status", h.Rules.HandleGetRuleEngineStatus, http.MethodGet)
	register("/api/rules/engine/enable", h.Rules.HandleEnableRuleEngine, http.MethodPost)
	register("/api/rules/engine/disable", h.Rules.HandleDisableRuleEngine, http.MethodPost)

	// Certificate Management API (/api/cert/*)
	register("/api/cert/status", h.Cert.HandleGetStatus, http.MethodGet, http.MethodPost)
	register("/api/cert/export", h.Cert.HandleExport, http.MethodPost)
	register("/api/cert/download", h.Cert.HandleDownload, http.MethodGet)
	register("/api/cert/install", h.Cert.HandleInstall, http.MethodPost)
	register("/api/cert/remove", h.Cert.HandleRemove, http.MethodPost)
	register("/api/cert/generate", h.Cert.HandleGenerateCert, http.MethodPost)
	register("/api/cert/info", h.Cert.HandleGetInfo, http.MethodGet)

	// DNS Cache API (/api/dns/*)
	// 注意：更具体的路由必须放在更通用的路由之前，避免路径冲突
	register("/api/dns/cache/query", h.DNSCache.QueryDomain, http.MethodGet)
	register("/api/dns/cache/reverse", h.DNSCache.ReverseQuery, http.MethodGet)
	register("/api/dns/cache/clear", h.DNSCache.ClearAll, http.MethodDelete)
	register("/api/dns/cache/delete/{domain}", h.DNSCache.DeleteEntry, http.MethodDelete)
	register("/api/dns/cache", h.DNSCache.GetCacheEntries, http.MethodGet)
	register("/api/dns/stats", h.DNSCache.GetStatistics, http.MethodGet)
	register("/api/dns/hotspots", h.DNSCache.GetHotspots, http.MethodGet)

	// Startup Status API (/api/startup/*)
	register("/api/startup/status", h.Startup.HandleStartupStatus, http.MethodGet)
	register("/api/startup/detail", h.Startup.HandleStartupDetail, http.MethodGet)

	// Routing Config compatibility API (/api/config/routing/*)
	register("/api/config/routing", h.Config.HandleRoutingConfig, http.MethodGet, http.MethodPost)
	register("/api/config/routing/rules/", h.Config.HandleToggleRuleStatus, http.MethodPut)
	register("/api/config/routing/auto-update", h.Config.HandleAutoUpdateStatus, http.MethodGet, http.MethodPut)
	register("/api/config/customer", h.Config.HandleCustomerConfig, http.MethodGet, http.MethodPost, http.MethodPut)
	register("/api/config/customer/providers", h.Config.HandlePresetAIRuleProviders, http.MethodGet)
	if isDev {
		register("/api/config/core", h.Config.HandleCoreConfig, http.MethodGet, http.MethodPost, http.MethodPut)
	}

	// Traffic Statistics API (/api/stats/traffic/*)
	register("/api/stats/traffic/", h.TrafficStats.HandleGetStats, http.MethodGet)
	register("/api/stats/traffic/current", h.TrafficStats.HandleGetCurrentStats, http.MethodGet)
	register("/api/stats/traffic/cache/info", h.TrafficStats.HandleGetCacheInfo, http.MethodGet)
	register("/api/stats/traffic/cache/clear", h.TrafficStats.HandleClearCache, http.MethodPost)

	// HTTP Statistics API (/api/stats/http/*)
	register("/api/stats/http/requests", h.HTTPStats.HandleGetRequests, http.MethodGet)
	register("/api/stats/http/domains", h.HTTPStats.HandleGetDomainStats, http.MethodGet)
	register("/api/stats/http/chart", h.HTTPStats.HandleGetChartData, http.MethodGet)
	register("/api/stats/http/info", h.HTTPStats.HandleGetStats, http.MethodGet)
	register("/api/stats/http/clear", h.HTTPStats.HandleClear, http.MethodPost, http.MethodDelete)
	register("/api/stats/http/preset-domains", h.HTTPStats.HandleGetPresetDomains, http.MethodGet)
	register("/api/status/summary", h.Status.HandleGetSummary, http.MethodGet)

	register("/api/chat/completions", h.Chat.HandleCompletions, http.MethodPost)

	register("/api/user-center/profile", h.UserCenter.HandleProfile, http.MethodGet, http.MethodPut)
	register("/api/user-center/usage/summary", h.UserCenter.HandleGetUsageSummary, http.MethodGet)
	register("/api/user-center/usage/progress", h.UserCenter.HandleGetUsageProgress, http.MethodGet)
	register("/api/user-center/api-keys", h.UserCenter.HandleGetAPIKeys, http.MethodGet)
	register("/api/user-center/redeem", h.UserCenter.HandleRedeemCode, http.MethodPost)
	register("/api/dashboard/stats", h.Dashboard.HandleGetStats, http.MethodGet)
	register("/api/dashboard/trend", h.Dashboard.HandleGetTrend, http.MethodGet)
	register("/api/dashboard/models", h.Dashboard.HandleGetModels, http.MethodGet)
	register("/api/dashboard/usage", h.Dashboard.HandleGetUsageRecords, http.MethodGet)
	register("/api/health", h.Dashboard.HandleGetHealth, http.MethodGet)
	register("/api/quick-setup/catalog", h.QuickSetup.HandleCatalog, http.MethodGet)
	register("/api/quick-setup/render", h.QuickSetup.HandleRender, http.MethodPost)
	register("/api/quick-setup/apply", h.QuickSetup.HandleApply, http.MethodPost)
	register("/api/docs/tutorials", h.Tutorial.HandleGetTutorial, http.MethodGet)

	registerDocsRoutes(mux, catalog)
}

// StartStatsCollector starts the traffic statistics collector background task
func StartStatsCollector(h *Handlers) error {
	if h.statsCollector == nil {
		return fmt.Errorf("stats collector not initialized")
	}

	if err := h.statsCollector.Start(); err != nil {
		logger.Error(fmt.Sprintf("Failed to start stats collector: %v", err))
		return err
	}

	logger.Info("Traffic stats collector started successfully")
	return nil
}

// StopStatsCollector stops the traffic statistics collector
func StopStatsCollector(h *Handlers) {
	if h.statsCollector != nil {
		h.statsCollector.Stop()
		logger.Info("Traffic stats collector stopped")
	}
}

// StartHTTPStatsCollector starts the HTTP statistics collector background task
func StartHTTPStatsCollector(h *Handlers) error {
	if h.httpStatsCollector == nil {
		return fmt.Errorf("http stats collector not initialized")
	}

	if err := h.httpStatsCollector.Start(); err != nil {
		logger.Error(fmt.Sprintf("Failed to start http stats collector: %v", err))
		return err
	}

	logger.Info("HTTP stats collector started successfully")
	return nil
}

// StopHTTPStatsCollector stops the HTTP statistics collector
func StopHTTPStatsCollector(h *Handlers) {
	if h.httpStatsCollector != nil {
		h.httpStatsCollector.Stop()
		logger.Info("HTTP stats collector stopped")
	}
}
