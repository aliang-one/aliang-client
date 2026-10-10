//go:build windows

package services

import (
	"golang.org/x/sys/windows/registry"
)

// augmentEnvWithWindowsUserPath 在 agent 进程的快照环境上补当前用户 PATH。
// agent 的 PATH 是启动那一刻的快照：用户装完 claude(npm 写 %APPDATA%\npm、原生
// 安装器写 %USERPROFILE%\.local\bin，均只登记在 HKCU\Environment)后，新条目进
// 不了已运行进程。消费方两处：terminal.create(手机新建终端即见最新用户 PATH)
// 与 AI CLI 子进程 env(agentChildProcessEnv，否则 .cmd shim 解析不到 node)。
// 每次调用读一次本地注册表，微秒级开销。
func augmentEnvWithWindowsUserPath(env []string) []string {
	userPath, ok := readWindowsUserRegistryPath()
	if !ok {
		return env
	}
	return mergeUserPathIntoEnv(env, expandWindowsPercentVars(userPath, windowsEnvVarLookup()))
}

// readWindowsUserRegistryPath 读当前用户 HKCU\Environment\Path。值通常是
// REG_EXPAND_SZ，原样返回、由调用方展开 %VAR%。读取失败(键或值不存在)返回
// false，终端环境保持快照原样。
func readWindowsUserRegistryPath() (string, bool) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE)
	if err != nil {
		return "", false
	}
	defer key.Close()
	val, _, err := key.GetStringValue("Path")
	if err != nil {
		return "", false
	}
	return val, true
}
