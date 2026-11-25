//go:build windows

package services

import (
	"golang.org/x/sys/windows/registry"
)

// augmentEnvWithWindowsUserPath 在 agent 进程的快照环境上补注册表里的用户与
// 机器级环境变量。agent 的 env 是启动那一刻的快照：之后安装/修改的任何
// 用户级变量（claude npm 的 %APPDATA%\npm、Python/Git/Java/Rust 的 all-users
// 安装写 HKLM、cargo 写 %USERPROFILE%\.cargo\bin 并登记 HKCU 等）都进不了
// 已运行进程。消费方：terminal.create（手机/网页新建终端即见最新环境）、
// AI CLI 子进程 env（agentChildProcessEnv）与 peer exec 子进程 env
// （agentPeerExecEnv）——三条通道共用本函数，杜绝再分叉。
//
// 合并算法见 mergeWindowsRegistryEnv（agent_terminal_env_registry.go）：
// %VAR% 展开 + PATH 拼接 + 用户覆盖机器。每次调用读两次注册表，微秒级
// 开销；任何读取失败的元素跳过，环境保持快照原样（fail-open，绝不因环境
// 探测把会话弄挂）。
func augmentEnvWithWindowsUserPath(env []string) []string {
	machine, mok := readWindowsRegistryEnv(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Control\Session Manager\Environment`)
	user, uok := readWindowsRegistryEnv(registry.CURRENT_USER, `Environment`)
	if !mok && !uok {
		return env
	}
	return mergeWindowsRegistryEnv(machine, user, env)
}

// readWindowsRegistryEnv 读一个注册表 Environment 键的全量变量（小写名→原值）。
// 值类型不做区分：REG_SZ 与 REG_EXPAND_SZ 都原样返回，%VAR% 由调用方统一
// 展开——对 REG_SZ 里恰含 % 的字面量，行为与旧版一致（容忍）。
func readWindowsRegistryEnv(root registry.Key, path string) (map[string]string, bool) {
	key, err := registry.OpenKey(root, path, registry.QUERY_VALUE)
	if err != nil {
		return nil, false
	}
	defer key.Close()
	names, err := key.ReadValueNames(-1)
	if err != nil {
		return nil, false
	}
	out := make(map[string]string, len(names))
	for _, name := range names {
		val, _, err := key.GetStringValue(name)
		if err != nil {
			continue
		}
		out[strings.ToLower(name)] = val
	}
	return out, true
}