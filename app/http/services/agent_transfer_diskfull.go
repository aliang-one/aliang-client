package services

// 磁盘满判定的跨平台总入口：errno 优先（精确），文案兜底（包装层/翻译文案）。
// 纯文案匹配在 Windows 上失灵（ENOSPC 文案不含 "no space left"），会把
// disk_full 误分类成 download_failed 并白烧重试（P2 复核项）。

import "strings"

// isAgentTransferDiskFull 报告 err 链里是否含磁盘满。
func isAgentTransferDiskFull(err error) bool {
	if err == nil {
		return false
	}
	if isAgentTransferDiskFullErrno(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no space left") || strings.Contains(msg, "disk full")
}
