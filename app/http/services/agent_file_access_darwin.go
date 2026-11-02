//go:build darwin

package services

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// macOS 系统设置里与文件访问相关的面板。
const (
	// FileAccessPaneFilesAndFolders = 隐私与安全性 › 文件与文件夹
	FileAccessPaneFilesAndFolders = "files"
	// FileAccessPaneAllFiles = 隐私与安全性 › 完全磁盘访问权限
	FileAccessPaneAllFiles = "allfiles"
)

// OpenFileAccessSystemSettings 打开系统设置的对应隐私面板。
//
// 为什么必须由后端执行：浏览器/网页无法唤起 `x-apple.systempreferences:` 这个
// 自定义 scheme，只有本机进程才能 `open` 它。且必须在 **user-agent 进程**里
// 执行——打开的是当前登录用户的系统设置界面。
//
// 完全磁盘访问权限（FDA）无法程序化申请，只能这样把用户送到面板前自己勾选。
func OpenFileAccessSystemSettings(pane string) error {
	url := "x-apple.systempreferences:com.apple.preference.security?Privacy_FilesAndFolders"
	if strings.EqualFold(strings.TrimSpace(pane), FileAccessPaneAllFiles) {
		url = "x-apple.systempreferences:com.apple.preference.security?Privacy_AllFiles"
	}
	// 用 Run 而不是 Start：Start 只负责 fork，open 自己非零退出（MDM 限制、
	// pane 被策略拦掉）也会被当成成功，前端显示「已打开系统设置」而窗口没出现
	// —— 这恰好是被拒目录唯一的补救路径，不能骗用户。Run 顺带回收子进程，
	// 避免每次点击留一个 zombie 挂在长活的 agent 上。
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := exec.CommandContext(ctx, "open", url).Run(); err != nil {
		return fmt.Errorf("打开系统设置失败: %w", err)
	}
	return nil
}
