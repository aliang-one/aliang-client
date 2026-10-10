package services

import (
	"os"
	"strings"
)

// 本文件是终端环境变量的跨平台纯逻辑：Windows 侧(见 agent_terminal_env_windows.go)
// 在每次 terminal.create 时读取注册表用户 PATH 并经此合并进 agent 快照环境，
// 使新装的 CLI(如 claude)无需重启 user-agent 即可被新建终端识别。

// expandWindowsPercentVars 展开 REG_EXPAND_SZ 里 %VAR% 形式的引用。lookup 命中
// 则替换，未命中保留字面量(与 cmd/powershell 对未定义变量的容忍一致)。
func expandWindowsPercentVars(s string, lookup func(string) (string, bool)) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var b strings.Builder
	for {
		start := strings.IndexByte(s, '%')
		if start < 0 {
			b.WriteString(s)
			return b.String()
		}
		end := strings.IndexByte(s[start+1:], '%')
		if end < 0 {
			b.WriteString(s)
			return b.String()
		}
		name := s[start+1 : start+1+end]
		// 空名(%%)不查 lookup：Windows 环境块含 "=C:=..." 空键隐藏项，
		// 查询会误命中并把 %% 展开成垃圾路径。
		if name != "" {
			if v, ok := lookup(name); ok {
				b.WriteString(s[:start])
				b.WriteString(v)
				s = s[start+end+2:]
				continue
			}
		}
		b.WriteString(s[:start+end+2])
		s = s[start+end+2:]
	}
}

// windowsEnvVarLookup 返回按 Windows 语义大小写不敏感的进程环境变量查询。
func windowsEnvVarLookup() func(string) (string, bool) {
	return func(name string) (string, bool) {
		if v, ok := os.LookupEnv(name); ok {
			return v, true
		}
		for _, kv := range os.Environ() {
			k, v, ok := strings.Cut(kv, "=")
			if ok && strings.EqualFold(k, name) {
				return v, true
			}
		}
		return "", false
	}
}

// mergeWindowsUserPath 把用户级 PATH(注册表值展开后)合并进当前 PATH：现有条目
// 原序在前，注册表新增条目按原序追加在后。比较按 Windows 语义大小写不敏感、
// 忽略尾部一个路径分隔符差异；空白项跳过。
func mergeWindowsUserPath(currentPath, userPath string) string {
	existing := splitWindowsPathList(currentPath)
	seen := make(map[string]struct{}, len(existing))
	for _, e := range existing {
		seen[windowsPathEntryKey(e)] = struct{}{}
	}
	out := existing
	for _, e := range splitWindowsPathList(userPath) {
		key := windowsPathEntryKey(e)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, e)
	}
	return strings.Join(out, ";")
}

func splitWindowsPathList(p string) []string {
	var out []string
	for _, e := range strings.Split(p, ";") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out
}

// windowsPathEntryKey 归一化比较键：去空白、小写化、去掉尾部一个路径分隔符
// (C:\Foo\ 与 C:\Foo 视为同目录)。只去一个是为了不动 C:\ 这类盘根本身。
func windowsPathEntryKey(e string) string {
	e = strings.TrimSpace(e)
	e = strings.TrimSuffix(e, `\`)
	e = strings.TrimSuffix(e, `/`)
	return strings.ToLower(e)
}

// mergeUserPathIntoEnv 在 "KEY=VALUE" 环境切片中定位 PATH(Windows 键不区分
// 大小写，原键名保留)，把 userPath 合并进其值；没有 PATH 条目时追加 Path=。
func mergeUserPathIntoEnv(env []string, userPath string) []string {
	for i, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.EqualFold(k, "PATH") {
			continue
		}
		env[i] = k + "=" + mergeWindowsUserPath(v, userPath)
		return env
	}
	return append(env, "Path="+mergeWindowsUserPath("", userPath))
}
