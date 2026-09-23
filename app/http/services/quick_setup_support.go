package services

import (
	"aliang.one/nursorgate/processor/config"
)

// 本文件是快速配置 v3 的共享支撑函数落点：Task 8 迁移 quick_setup_render.go 的
// 幸存者（quickSetupBaseURL/resolveQuickSetupInferenceBaseURL 等）时归位于此，
// 先容纳组合服务的 base_url 预设（spec §6）。

// quickSetupBaseURLPresets 返回该 software 的 base_url 预设（local/public）。
// codex/opencode 的 base_url 变量值带 /v1（模板裸 {{base_url}}，/v1 属变量值）；
// claude-code/pi 不带（Claude Code/pi 自行追加 /v1/messages）。
func quickSetupBaseURLPresets(softwareCode string) (local, public string, err error) {
	root, err := quickSetupBaseURL()
	if err != nil {
		return "", "", err
	}
	local = "http://" + config.DefaultHTTPProxyAddr
	public = resolveQuickSetupInferenceBaseURL(root)
	switch softwareCode {
	case "codex", "opencode":
		local += "/v1"
		public += "/v1"
	}
	return local, public, nil
}
