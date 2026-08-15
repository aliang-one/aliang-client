package repositories

// LoggerRepository defines the interface for logger data access
type LoggerRepository interface {
	// GetLogs retrieves logs with filtering
	// GetLogs(params models.LogsQueryParams) []models.LogEntryResponse

	// ClearLogs clears the log buffer
	// ClearLogs() error

	// UpdateLogLevel updates the log level
	// UpdateLogLevel(levelStr string) error

	// SubscribeLogStream subscribes to real-time log stream
	// SubscribeLogStream() (<-chan *logger.LogEntry, func())

	// GetConfig retrieves the current logger configuration
	// GetConfig() map[string]interface{}

	// UpdateConfig updates logger configuration
	// UpdateConfig(req models.LogConfigRequest) error
}

// ProxyRepository defines the interface for proxy data access
type ProxyRepository interface {
	// GetCurrentProxy gets the current default proxy
	// GetCurrentProxy() (map[string]interface{}, error)

	// SetCurrentProxy sets the current default proxy
	// SetCurrentProxy(name string) (map[string]interface{}, error)

	// ListProxies lists all proxies
	// ListProxies() (map[string]interface{}, error)

	// GetProxy gets a specific proxy
	// GetProxy(name string) (interface{}, error)

	// RegisterProxy registers a new proxy
	// RegisterProxy(name string, config interface{}) error

	// UnregisterProxy unregisters a proxy
	// UnregisterProxy(name string) error

	// SetDefaultProxy sets the default proxy
	// SetDefaultProxy(name string) error

	// SwitchProxy switches to a proxy
	// SwitchProxy(name string) error
}

// ConfigRepository defines the interface for configuration data access
type ConfigRepository interface {
	// GetConfig retrieves stored configuration by name
	// GetConfig(name string) (interface{}, error)

	// ListConfigs lists all stored configurations
	// ListConfigs() interface{}
}

// TokenRepository defines the interface for token data access
type TokenRepository interface {
	// SetToken sets the outbound token
	// SetToken(token string) string

	// GetToken gets the current outbound token
	// GetToken() string
}

// RunRepository defines the interface for run mode state access
type RunRepository interface {
	// GetCurrentMode gets the current operating mode
	// GetCurrentMode() string

	// SetCurrentMode sets the operating mode
	// SetCurrentMode(mode string)

	// IsTunRunning checks if TUN service is running
	// IsTunRunning() bool

	// SetTunRunning sets the TUN running state
	// SetTunRunning(running bool)
}
