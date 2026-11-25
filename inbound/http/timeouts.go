package http

import "time"

// proxyFaceRejectWriteTimeout 是来源闸拒绝应答的写期限:被拒的对端可能
// 根本不读(扫描器/半开连接),不给期限会让一条拒绝应答无限占住连接。
const proxyFaceRejectWriteTimeout = 2 * time.Second

func deadlineAfter(d time.Duration) time.Time {
	return time.Now().Add(d)
}
