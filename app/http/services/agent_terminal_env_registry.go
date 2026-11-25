package services

import "strings"

// 本文件是 Windows 注册表环境合并的跨平台纯逻辑（读注册表部分见
// agent_terminal_env_windows.go 的 augmentEnvWithWindowsUserPath）。
// 拆出无 build tag 是为了 darwin 单测可直接钉「后装 Java/Rust 任意变量」
// 场景（mergeWindowsRegistryEnv 由 windows 文件调用）。

// mergeWindowsRegistryEnv 合并机器级/用户级注册表变量进进程快照 env。
// machine/user 为小写键名→原值（REG_SZ/REG_EXPAND_SZ 不区分，%VAR% 由本
// 函数统一展开）。
//
// 合并语义（与 explorer.exe 启动新进程同构）：
//   - %VAR% 引用目标优先取注册表合并视图，回退进程环境（覆盖 JAVA_HOME
//     这类机器级变量被用户级 %JAVA_HOME% 引用的形态）；
//   - PATH 特殊：机器 Path + 用户 Path 拼接去重（Windows 语义，不是覆盖）；
//   - 其余键：用户级覆盖机器级；快照里没有的键追加，已有的键原位替换值。
func mergeWindowsRegistryEnv(machine, user map[string]string, env []string) []string {
	// combined：非 PATH 键的合并视图，用户覆盖机器（Windows 语义）。
	combined := make(map[string]string, len(machine)+len(user))
	for k, v := range machine {
		combined[k] = v
	}
	for k, v := range user {
		combined[k] = v
	}

	lookup := func(name string) (string, bool) {
		if v, ok := combined[strings.ToLower(name)]; ok {
			return v, true
		}
		return windowsEnvVarLookup()(name)
	}
	for k := range combined {
		combined[k] = expandWindowsPercentVars(combined[k], lookup)
	}

	// PATH：机器 + 用户拼接去重（Windows 语义），不是覆盖。
	machinePath := ""
	if v, ok := machine["path"]; ok {
		machinePath = expandWindowsPercentVars(v, lookup)
	}
	userPath := ""
	if v, ok := user["path"]; ok {
		userPath = expandWindowsPercentVars(v, lookup)
	}
	registryPath := mergeWindowsUserPath(machinePath, userPath)

	out := make([]string, 0, len(env)+len(combined)+1)
	touched := make(map[string]struct{}, len(combined))
	for _, kv := range env {
		name, _, ok := strings.Cut(kv, "=")
		if !ok {
			out = append(out, kv) // 畸形条目原样保留（含 Windows 空键隐藏项）
			continue
		}
		lname := strings.ToLower(name)
		if lname == "path" {
			// PATH 原位替换为注册表最新拼接结果（去重），避免 agent 快照
			// 里陈旧目录继续占位；同时保留 agent 快照里注册表没有的条目
			// （mergeWindowsUserPath 追加缺失）。
			out = append(out, name+"="+mergeWindowsUserPath(kvValue(kv), registryPath))
			touched["path"] = struct{}{}
			continue
		}
		if v, hit := combined[lname]; hit {
			out = append(out, name+"="+v) // 原键名保留，值取注册表最新
			touched[lname] = struct{}{}
			continue
		}
		out = append(out, kv)
	}
	// 快照里没有的键（后装工具的 JAVA_HOME 等）追加。
	for k, v := range combined {
		if _, seen := touched[k]; seen {
			continue
		}
		if k == "path" {
			out = append(out, "Path="+registryPath)
			continue
		}
		out = append(out, k+"="+v)
	}
	// PATH 在快照里完全缺失时追加。
	if _, seen := touched["path"]; !seen && registryPath != "" {
		out = append(out, "Path="+registryPath)
	}
	return out
}

func kvValue(kv string) string {
	if idx := strings.IndexByte(kv, '='); idx >= 0 {
		return kv[idx+1:]
	}
	return ""
}