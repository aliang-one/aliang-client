// Package panelwin — 托盘面板纯原生窗口壳(实现计划 M2 Task 13)。
//
// 分工:本文件持平台中立的纯逻辑(锚定、协议解析、白名单判定)与 Host
// 接口;平台壳实现在 panelwin_darwin.go(+.m,cgo/ObjC)与
// panelwin_windows.go(纯 Go,x/sys/windows + jchv/go-webview2 的
// pkg/edge.Chromium)。Linux 无实现,也不得进入 ./cmd/aliang 的依赖闭包
// (cmd/linux_closure_test.go TestLinuxDepsExcludeGUI 把守)。
//
// 为什么不用顶层 WebView 包装(jchv/go-webview2 的 webview.WebView):其
// Bind/JSON-RPC 会拦截 postMessage 且无 WebMessageReceived 钩子,quit/open
// 消息链会静默断裂——只用 pkg/edge.Chromium(Embed 自建 HWND + MessageCallback
// + 自建消息循环)。依赖在 go.mod 钉死 pseudo-version,见 go.mod 注释。
//
// 与前端的契约(Task 7 评审固化):
//   - 页面经 window.webkit.messageHandlers.panel.postMessage(macOS)或
//     window.chrome.webview.postMessage(Windows)发 JSON;协议冻结为
//     {"type":"quit"} 与 {"type":"open","kind":"dashboard"|"peer"} 两类型,
//     未知类型默认忽略,不得崩。
//   - 页面检测到宿主后给 <html> 打 hosted 类,底色转透明——壳必须
//     drawsBackground=false(macOS)/WebView2 透明背景(Windows)+ 系统材质层
//     铺底,否则半透明面板底色失去意义、毛玻璃验收必挂。
//
// 退出码契约(实现计划 Task 14 launcher 据此分派):
//
//	0 = 失焦自退(正常关闭,零惩罚);60 = 页面 quit 消息(正常关闭);
//	其余非零 = 崩溃(launcher 记失败重拉,连崩 3 次转原生菜单兜底)。
package panelwin

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
)

// Side —— 面板相对托盘图标弹出的一侧(CLI --side 字面量)。
type Side string

const (
	SideTop    Side = "top"
	SideBottom Side = "bottom"
)

// 退出码契约(Task 14 launcher 判据)。
const (
	ExitFocusLoss = 0  // 失焦自退
	ExitQuit      = 60 // 页面 quit 消息
)

// 面板默认逻辑尺寸(spec §4 面板 340px 宽;高度固定 560,不做自适应——
// 「明确不做」清单)。
const (
	DefaultPanelW = 340
	DefaultPanelH = 560
)

// Options —— 壳启动参数(cmd/panel.go 解析 CLI 后装配)。
type Options struct {
	// URL —— 面板页地址;其 origin 即导航白名单唯一放行源。
	URL string
	// X/Y —— 锚定后的窗口位置(全局坐标、左上原点;单位随平台:
	// macOS=点,Windows=物理像素)。由 AnchorToWindowPos 从托盘图标矩形算出。
	X, Y int
	// PanelW/PanelH —— 面板逻辑尺寸(340×560)。Windows 壳内按 DPI ×scale
	// 落物理尺寸,CSS 视口维持逻辑宽;macOS 点坐标直通。
	PanelW, PanelH int
	// OpenDashboard/OpenPeer —— open 消息按 kind 分派的两个目标 URL
	// (v1 两 kind 均传仪表盘根 URL,见实现计划)。
	OpenDashboard string
	OpenPeer      string
}

// OpenTarget —— open 消息按 kind 取目标 URL;未知 kind 返回空串=忽略分派
// (协议内 kind 只有 dashboard/peer 两种)。
func (o Options) OpenTarget(kind string) string {
	switch kind {
	case "dashboard":
		return o.OpenDashboard
	case "peer":
		return o.OpenPeer
	default:
		return ""
	}
}

// Host —— 平台壳的统一入口(Linux 无实现)。
type Host interface {
	// Run 阻塞展示面板,直到窗口关闭。返回退出码:0=失焦自退、60=quit。
	// darwin 实现在失焦/quit 路径上经 .m 的 exit() 直接终止进程,通常
	// 不返回;windows 实现经消息循环正常返回。
	Run(opts Options) int
}

// AnchorToWindowPos —— 锚定纯函数(实现计划 Task 13 指定形态):
//
//	side=bottom → y = ay - panelH - 8   (面板底边距图标顶边 8)
//	side=top    → y = ay + h + 4        (面板顶边距图标底边 4)
//	x           → 图标水平居中,并钳左边距 8
//
// y 不钳边(多屏全局坐标可为负是合法形态);右边距钳制需要屏幕宽度,
// 本函数签名(评审定稿 7 参)不含屏幕参数,右钳归接线层(Task 15 判废/
// 兜底时拿到屏幕矩形再做)。未知 side 兜底按 bottom(接线层已先校验)。
//
// 单位:入参是托盘图标矩形,单位随平台(macOS=点、Windows=物理像素,
// 两侧 TrayRect 语义对应);面板宽高按调用方原值参与运算,DPI 换算归壳。
func AnchorToWindowPos(ax, ay, w, h int, side Side, panelW, panelH int) (int, int) {
	// 钳左边距与上下间隙分开成常量:变异验证时要能只动其中一处。
	const (
		clampMargin  = 8 // x 的左钳边距
		gapBelowIcon = 8 // bottom:面板底边与图标顶边的间隙
		gapAboveIcon = 4 // top:面板顶边与图标底边的间隙
	)

	x := ax + w/2 - panelW/2
	if x < clampMargin {
		x = clampMargin
	}

	var y int
	switch side {
	case SideTop:
		y = ay + h + gapAboveIcon
	default: // SideBottom 与未知值兜底
		y = ay - panelH - gapBelowIcon
	}
	return x, y
}

// LoopbackOrigin —— 从面板页 URL 提取导航白名单锚点 origin,并强制回环
// (调研 §6.3 不变量:面板等于一个无鉴权控制台,不得加载任何第三方内容)。
// 非回环/解析失败一律报错,调用方(CLI)直接拒绝启动。
func LoopbackOrigin(rawURL string) (string, error) {
	if strings.TrimSpace(rawURL) == "" {
		return "", fmt.Errorf("面板页 URL 为空")
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("面板页 URL 解析失败: %w", err)
	}
	host := u.Hostname()
	if !isLoopbackHost(host) {
		return "", fmt.Errorf("面板页 URL 必须指向回环地址,得到 %q", host)
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return "", fmt.Errorf("面板页 URL 协议必须为 http(s),得到 %q", u.Scheme)
	}
	// origin = scheme://host[:port];默认端口省略(与两端壳的 origin 比较
	// 口径一致:两侧都从实际 URL 重新提取 origin 再比较)。
	origin := u.Scheme + "://" + originHost(host)
	if p := u.Port(); p != "" {
		origin += ":" + p
	}
	return origin, nil
}

// originHost —— origin 序列化形态的 host:IPv6 字面量要包回方括号
// (url.Hostname() 会剥掉;origin 权威形态是 http://[::1]:56431)。
func originHost(host string) string {
	host = strings.ToLower(host)
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]"
	}
	return host
}

// isLoopbackHost —— 回环判定:127.0.0.0/8、localhost、::1。
func isLoopbackHost(host string) bool {
	if host == "" {
		return false
	}
	if host == "localhost" || strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// AllowDocument —— 主文档导航闸:只有 origin 恰等于白名单 origin 的文档
// 放行。Windows 壳经 pkg/edge 的 WebResourceRequested(DOCUMENT 过滤器)
// 落地,macOS 壳经 decidePolicyFor 落地;两端共用本判定。
func AllowDocument(uri, allowOrigin string) bool {
	if allowOrigin == "" {
		return false
	}
	u, err := url.Parse(uri)
	if err != nil || u.Host == "" {
		// about:blank、javascript:、file: 等无主机形态一律拒绝。
		return false
	}
	switch u.Scheme {
	case "http", "https":
	default:
		return false
	}
	origin := u.Scheme + "://" + originHost(u.Hostname())
	if p := u.Port(); p != "" {
		origin += ":" + p
	}
	return strings.EqualFold(origin, allowOrigin)
}

// ParseHostMessage —— WebMessage 协议解析(协议冻结:quit/open 两类型)。
// 返回 (type, kind, known);known=false 的形态(未知 type、垃圾 JSON、
// 非 JSON 主体)由调用方直接忽略,不得崩。kind 仅对 open 有意义。
func ParseHostMessage(payload string) (typ string, kind string, known bool) {
	var m struct {
		Type string `json:"type"`
		Kind string `json:"kind"`
	}
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return "", "", false
	}
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return "", "", false
	}
	switch m.Type {
	case "quit":
		return "quit", "", true
	case "open":
		return "open", m.Kind, true
	default:
		// 未知类型默认忽略(协议冻结;前端已删除协议外 panel_ready):
		// 返回空 type,调用方拿到 known=false 直接丢弃,零分派零崩溃。
		return "", "", false
	}
}
