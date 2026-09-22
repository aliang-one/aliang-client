package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"aliang.one/nursorgate/app/agentruntime"
	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/app/http/storage"
	"aliang.one/nursorgate/common/logger"
	auth "aliang.one/nursorgate/processor/auth"
	"aliang.one/nursorgate/processor/setup"
	"github.com/spf13/cobra"
)

var agentCmd = &cobra.Command{
	Use:    "agent",
	Short:  "Run the user-mode command agent",
	Hidden: true,
	RunE:   runAgent,
}

func init() {
	rootCmd.AddCommand(agentCmd)
}

func runAgent(cmd *cobra.Command, args []string) error {
	ensureUserAgentEnvironment()
	auth.SetSessionOwnerProcess(false)

	if err := storage.InitializeSoftwareConfigStore(); err != nil {
		return fmt.Errorf("failed to initialize software config persistence: %w", err)
	}
	if err := ApplyStartupConfigForMode(setup.RuntimeModeInteractive, configPath); err != nil {
		return fmt.Errorf("failed to initialize agent configuration: %w", err)
	}
	logAgentStartupConfig("agent_config_loaded")
	// The user-agent is deliberately not an auth-session owner. The dashboard/core
	// process restores and refreshes the session, then forwards the current access
	// token through /api/agent/sync.

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("Starting Aliang user-mode agent")
	return agentruntime.RunForeground(ctx)
}

// ensureUserAgentEnvironment 为 agent 进程补齐最小运行环境。除 runtime 标记
// 与路径隔离外，还为"凭据被拒→通知 owner"链兜底：正常形态下
// ALIANG_SESSION_OWNER_ADDR 由 manager（owner 进程）spawn 时注入
// （agentruntime.userAgentEnv）；但 Linux headless 上手动 `aliang agent`
// （无 manager spawn）时无人注入，通知链（services.NotifyOwnerAuthRejected）
// 因无地址而整体静默缺失——agent 只能自禁等 owner 自查。故仅在该 env 未设置
// 时注入默认 owner 地址（与 manager 的 ownerBaseURL 默认分支共用
// services.DefaultSessionOwnerAddr）；显式值（manager spawn 或运维注入）不覆盖。
func ensureUserAgentEnvironment() {
	_ = os.Setenv(services.AgentRuntimeEnv, "1")
	if strings.TrimSpace(os.Getenv(services.SessionOwnerAddrEnv)) == "" {
		_ = os.Setenv(services.SessionOwnerAddrEnv, services.DefaultSessionOwnerAddr())
	}
	_ = os.Unsetenv("ALIANG_DATA_DIR")
	_ = os.Unsetenv("ALIANG_CACHE_DIR")
	_ = os.Unsetenv("ALIANG_LOG_DIR")
	_ = os.Unsetenv("ALIANG_SOCKET_PATH")
}
