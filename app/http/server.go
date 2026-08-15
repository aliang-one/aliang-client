package http

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app"
	"aliang.one/nursorgate/app/http/middleware"
	"aliang.one/nursorgate/app/http/routes"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

var (
	// mux is the custom request multiplexer for applying middleware
	mux *http.ServeMux

	// server is the HTTP server instance
	server *http.Server

	// serverMutex protects server state
	serverMutex sync.Mutex

	// isRunning indicates if server is currently running
	isRunning bool

	// actualPort stores the actual port the server is listening on
	actualPort string
)

// StartHttpServer 启动HTTP服务器，注册所有路由
func StartHttpServer() error {
	serverMutex.Lock()
	defer serverMutex.Unlock()

	if isRunning {
		logger.Info("HTTP server is already running")
		return nil
	}

	// 定义 HTTP 服务端口
	port := config.ManagementListenAddr()

	// Initialize custom mux
	mux = http.NewServeMux()

	// 注册所有路由
	registerAllRoutes()

	logger.Info(fmt.Sprintf("Starting HTTP server on %s...\n", port))

	// Wrap mux with middleware stack
	middlewares := middleware.GetDefaultMiddleware()
	wrappedMux := middleware.Chain(mux, middlewares...)

	// 尝试监听端口，如果被占用则尝试其他端口
	listener, err := net.Listen("tcp", port)
	if err != nil {
		if strings.Contains(err.Error(), "address already in use") {
			// 尝试自动选择可用端口，并沿用显式配置的 host。
			logger.Warn(fmt.Sprintf("Port %s is already in use, trying to find an available port...", port))
			bindHost, _, _ := net.SplitHostPort(port)
			if bindHost == "" {
				bindHost = config.DefaultServiceBindHost
			}
			listener, err = net.Listen("tcp", net.JoinHostPort(bindHost, "0")) // 0 means auto-select port
			if err != nil {
				return fmt.Errorf("http server failed: unable to find available port: %w", err)
			}
			actualAddr := listener.Addr().(*net.TCPAddr)
			actualPort = fmt.Sprintf("%d", actualAddr.Port)
			logger.Info(fmt.Sprintf("HTTP server listening on alternative port: %s", actualAddr.String()))
		} else {
			return fmt.Errorf("http server failed: %w", err)
		}
	} else {
		// Store the actual port from the default port
		_, portStr, _ := net.SplitHostPort(port)
		actualPort = portStr
	}
	if actualPort != "" {
		services.SetAgentAIApprovalHookBaseURL("http://127.0.0.1:" + actualPort)
	}

	// Create HTTP server
	server = &http.Server{
		Handler: wrappedMux,
	}
	srv := server
	isRunning = true

	// 启动 HTTP 服务（非阻塞）
	go func(listener net.Listener, srv *http.Server) {
		err := srv.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			logger.Error(fmt.Sprintf("HTTP server failed: %v", err))
		}

		serverMutex.Lock()
		defer serverMutex.Unlock()
		if server == srv {
			isRunning = false
			server = nil
			actualPort = ""
		}
	}(listener, srv)

	return nil
}

// registerAllRoutes 注册所有HTTP路由
func registerAllRoutes() {
	// Create all handlers with dependency injection
	handlers := routes.NewHandlersWithRunService(services.GetSharedRunService())

	// Register all feature-grouped routes (using custom mux)
	// registerRoutesWithMux(handlers)
	routes.RegisterRoutes(handlers, mux)

	// NOTE: Rule engine initialization has been MOVED to cmd/start.go:InitializeGlobalRuleEngine()
	// This ensures the singleton rule engine is initialized only ONCE at startup
	// Previously this was duplicated in both HTTP mode and TUN mode
	logger.Info("HTTP: Rule engine has been initialized globally (see cmd/start.go)")

	// Initialize and start stats collector
	if handlers.TrafficStats != nil {
		logger.Info("Starting traffic stats collector...")
		// Note: statsCollector is created in routes.NewHandlers()
		// We need to access it through a package-level function
		routes.StartStatsCollector(handlers)
	}

	if handlers.HTTPStats != nil {
		logger.Info("Starting HTTP stats collector...")
		routes.StartHTTPStatsCollector(handlers)
	}

	services.StartSoftwareUpdateChecker()

	// Register static file server for web dashboard
	registerStaticFiles()
}

// initializeRuleEngine has been REMOVED - replaced by cmd/start.go:InitializeGlobalRuleEngine()
// This function was causing duplicate initialization of the singleton rule engine
// See: cmd/start.go for the new centralized initialization

// registerStaticFiles 注册静态文件服务（使用 embed 嵌入的文件）
func registerStaticFiles() {
	websiteRoot, rootPath, err := resolveWebsiteRoot()
	if err != nil {
		logger.Warn(fmt.Sprintf("Failed to access embedded website files: %v", err))
		return
	}

	// 注册 assets 路径处理器
	mux.HandleFunc("/assets/", func(w http.ResponseWriter, r *http.Request) {
		setAssetsCacheHeaders(w)

		// 移除前导 /assets/
		filePath := strings.TrimPrefix(r.URL.Path, "/assets/")

		// 安全检查：确保没有目录遍历
		if strings.Contains(filePath, "..") {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}

		// 尝试打开文件
		file, err := websiteRoot.Open("assets/" + filePath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer file.Close()

		// 获取文件信息
		info, err := file.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}

		// 设置 Content-Type
		setContentType(w, filePath)

		// 使用 ServeContent（支持 Range 请求和缓存）
		if rs, ok := file.(io.ReadSeeker); ok {
			http.ServeContent(w, r, filepath.Base(filePath), info.ModTime(), rs)
		} else {
			w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
			io.Copy(w, file)
		}
	})

	// 注册根路径处理器，支持 SPA 路由
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// API路径返回404 JSON，防止返回HTML
		if strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"code":404,"msg":"API endpoint not found","data":null}`))
			return
		}

		// Security check: prevent directory traversal for static file lookup.
		requestPath := strings.TrimPrefix(r.URL.Path, "/")
		if strings.Contains(requestPath, "..") {
			http.Error(w, "Invalid path", http.StatusBadRequest)
			return
		}

		// Prefer serving real files (e.g. /icon.svg, /favicon.ico) before SPA fallback.
		if requestPath != "" {
			file, err := websiteRoot.Open(requestPath)
			if err == nil {
				defer file.Close()
				info, statErr := file.Stat()
				if statErr == nil && !info.IsDir() {
					if filepath.Ext(requestPath) == ".html" {
						setNoCacheHTMLHeaders(w)
					} else {
						setAssetsCacheHeaders(w)
					}
					setContentType(w, requestPath)

					if rs, ok := file.(io.ReadSeeker); ok {
						http.ServeContent(w, r, filepath.Base(requestPath), info.ModTime(), rs)
					} else {
						w.Header().Set("Content-Length", fmt.Sprintf("%d", info.Size()))
						io.Copy(w, file)
					}
					return
				}
			}
		}

		// SPA fallback to index.html.
		indexFile, err := websiteRoot.Open("index.html")
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer indexFile.Close()
		setNoCacheHTMLHeaders(w)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.Copy(w, indexFile)
	})

	logger.Debug(fmt.Sprintf("Static file server registered using embedded website files (root=%s)", rootPath))
}

func resolveWebsiteRoot() (fs.FS, string, error) {
	preferredRoots := []string{
		"website/dist",
		"website",
	}

	for _, root := range preferredRoots {
		sub, err := fs.Sub(app.WebsiteFS, root)
		if err != nil {
			continue
		}
		if _, openErr := sub.Open("index.html"); openErr == nil {
			return sub, root, nil
		}
	}

	return nil, "", fmt.Errorf("no valid embedded website root found (tried: %s)", strings.Join(preferredRoots, ", "))
}

func setNoCacheHTMLHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")
}

func setAssetsCacheHeaders(w http.ResponseWriter) {
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
}

// setContentType 根据文件扩展名设置 Content-Type
// StopHttpServer gracefully stops the HTTP server
func StopHttpServer() error {
	serverMutex.Lock()
	defer serverMutex.Unlock()

	if !isRunning || server == nil {
		logger.Info("HTTP server is not running")
		return nil
	}

	logger.Info("Stopping HTTP server...")

	// Create shutdown context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Gracefully shutdown
	if err := server.Shutdown(ctx); err != nil {
		logger.Error(fmt.Sprintf("HTTP server shutdown error: %v", err))
		return err
	}

	isRunning = false
	server = nil
	actualPort = ""
	logger.Info("HTTP server stopped successfully")
	return nil
}

// IsServerRunning returns whether the HTTP server is currently running
func IsServerRunning() bool {
	serverMutex.Lock()
	defer serverMutex.Unlock()
	return isRunning
}

// GetActualPort returns the actual port the HTTP server is listening on
func GetActualPort() string {
	serverMutex.Lock()
	defer serverMutex.Unlock()
	return actualPort
}

// setContentType 根据文件扩展名设置 Content-Type
func setContentType(w http.ResponseWriter, path string) {
	ext := filepath.Ext(path)
	switch ext {
	case ".html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	case ".css":
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".js":
		w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case ".json":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
	case ".svg":
		w.Header().Set("Content-Type", "image/svg+xml")
	case ".png":
		w.Header().Set("Content-Type", "image/png")
	case ".jpg", ".jpeg":
		w.Header().Set("Content-Type", "image/jpeg")
	case ".ico":
		w.Header().Set("Content-Type", "image/x-icon")
	case ".woff", ".woff2":
		w.Header().Set("Content-Type", "fonts/woff2")
	case ".ttf":
		w.Header().Set("Content-Type", "fonts/ttf")
	case ".eot":
		w.Header().Set("Content-Type", "application/vnd.ms-fontobject")
	}
}
