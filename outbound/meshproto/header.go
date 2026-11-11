// Package meshproto 是 mesh 语义常量包(SPEC §12):流量类别、跳数上限、
// dns: 特殊目标与目标拆解工具。帧协议本体(Header/Encode/Parse/AppendHop
// 等线格式符号)已随 B 侧帧协议入站路径退役(SPEC §13.1)——mesh 流量现在
// 走 SSH 入口形态(HandleIngressConn → form 矩阵 → mesh-exit),不再有
// 自定义帧头。
package meshproto

import "net"

// 流量类别(design §4)
const (
	ClassSelf   = "self"   // 自身流量:LAN + 中继
	ClassAI     = "ai"     // AI 流量:LAN + 中继
	ClassCustom = "custom" // 用户自定义:仅 LAN
)

// 跳数上限:默认 5、硬上限 8,可配置项由 config 层 clamp 到 [1,8](design §2 行7)。
const (
	DefaultHops = 5
	MaxHopsCap  = 8
)

// DNSTarget 特殊目标:DNS 代转发查询(每次查询独占一条 mesh 连接,design §6 层1)。
const DNSTarget = "dns:"

func IsDNSTarget(target string) bool { return target == DNSTarget }

// SplitTarget 拆 host:port;裸域名(无端口)由调用方按 443 兜底。
// 返回 isDomain=false 表示 host 为 IP 字面量。
func SplitTarget(target string) (host, port string, isDomain bool, err error) {
	if target == DNSTarget {
		return "", "", false, nil
	}
	h, p, splitErr := net.SplitHostPort(target)
	if splitErr != nil {
		if net.ParseIP(target) != nil {
			// 裸 IP(无端口)不是域名:返回 isDomain=false,防 Task 6 构 Metadata 时误送去解析。
			return target, "", false, nil
		}
		return target, "", true, nil // 裸域名
	}
	if net.ParseIP(h) != nil {
		return h, p, false, nil
	}
	return h, p, true, nil
}
