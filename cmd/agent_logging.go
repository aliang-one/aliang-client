package cmd

import (
	"fmt"
	"os"
	"strings"

	"aliang.one/nursorgate/app/http/services"
	"aliang.one/nursorgate/common/logger"
	"aliang.one/nursorgate/processor/config"
)

func logAgentStartupConfig(stage string) {
	cfg := config.GetGlobalConfig()
	if cfg == nil || cfg.Core == nil {
		logger.Info(fmt.Sprintf("[AGENT-BOOT] stage=%s global_config=false local_agent_url=%s user_agent_runtime=%t", stage, services.UserAgentBaseURL(), services.IsUserAgentRuntime()))
		return
	}

	source := "default"
	if strings.TrimSpace(cfg.Core.AgentServer) != "" {
		source = "core.agent_server"
	}
	logger.Info(fmt.Sprintf(
		"[AGENT-BOOT] stage=%s global_config=true agent_server=%s agent_server_source=%s local_agent_url=%s user_agent_runtime=%t runtime_env=%q",
		stage,
		cfg.AgentBaseURL(),
		source,
		services.UserAgentBaseURL(),
		services.IsUserAgentRuntime(),
		os.Getenv(services.AgentRuntimeEnv),
	))
}
