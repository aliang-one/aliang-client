//go:build !windows

package services

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// loginShellPathSnapshot 跑一次「登录+交互」shell 取 $PATH。-l 吃
// .zprofile/path_helper，-i 吃 .zshrc——用户的终端正是两者叠加，少一个都
// 不是「一样的环境」。marker 定位保证 .zshrc 回显噪声不污染解析。
//
// fish 的 $PATH 是空格连接的 list，printf "$PATH" 出来无冒号会解析失败
// （fail-open，无害）；fish 用户走 `string join` 分支拿到冒号形 PATH。
func loginShellPathSnapshot(timeout time.Duration) (string, bool) {
	shell := strings.TrimSpace(os.Getenv("SHELL"))
	if shell == "" || !isRegularFilePath(shell) {
		for _, candidate := range []string{"/bin/zsh", "/bin/bash", "/bin/sh"} {
			if isRegularFilePath(candidate) {
				shell = candidate
				break
			}
		}
	}
	if shell == "" {
		return "", false
	}
	probe := `printf '` + loginShellPathMarker + `%s' "$PATH"`
	if strings.EqualFold(filepath.Base(shell), "fish") {
		probe = `printf '` + loginShellPathMarker + `%s' (string join : $PATH)`
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, shell, "-l", "-i", "-c", probe)
	output, err := cmd.Output()
	if err != nil {
		// 交互 shell 可能因 job control 警告等返回非零，但 stdout 仍有效；
		// 只有拿不出标记才算失败。
		if parsed, ok := parseLoginShellPathOutput(string(output)); ok {
			return parsed, true
		}
		return "", false
	}
	return parseLoginShellPathOutput(string(output))
}

// isRegularFilePath 判定候选 shell 存在且是常规文件。
func isRegularFilePath(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}
