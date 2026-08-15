package setup

// ServiceManager 定义跨平台服务管理的统一接口
type ServiceManager interface {
	// Install 安装服务
	Install(options InstallOptions) error

	// Uninstall 卸载服务
	Uninstall() error

	// Start 启动服务
	Start() error

	// Stop 停止服务
	Stop() error

	// Restart 重启服务
	Restart() error

	// Status 获取服务状态
	Status() (*ServiceStatus, error)

	// IsInstalled 检查服务是否已安装
	IsInstalled() bool

	// GetName 获取服务名称
	GetName() string
}

// InstallOptions 服务安装选项
type InstallOptions struct {
	// 服务名称（内部标识符）
	Name string

	// 显示名称
	DisplayName string

	// 服务描述
	Description string

	// 可执行文件路径（留空则使用当前可执行文件）
	ExecutablePath string

	// 配置文件路径
	ConfigPath string

	// 是否以系统级安装（需要管理员权限）
	SystemWide bool

	// 服务启动类型
	StartType StartType

	// 额外参数
	Args []string

	// 环境变量
	Env map[string]string

	// 工作目录
	WorkingDirectory string
}

// StartType 服务启动类型
type StartType int

const (
	// StartManual 手动启动
	StartManual StartType = iota
	// StartAutomatic 自动启动
	StartAutomatic
	// StartDisabled 禁用
	StartDisabled
)

// ServiceStatus 服务状态
type ServiceStatus struct {
	IsRunning   bool   // 是否正在运行
	IsInstalled bool   // 是否已安装
	PID         int    // 进程ID（如果正在运行）
	Status      string // 状态描述："running", "stopped", "failed", etc.
}