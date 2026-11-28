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
// 与前端的契约(Task 7 评审固化;resize 为 2026-11-28 用户拍板的追加):
//   - 页面经 window.webkit.messageHandlers.panel.postMessage(macOS)或
//     window.chrome.webview.postMessage(Windows)发 JSON;类型白名单为
//     {"type":"quit"}、{"type":"open","kind":"dashboard"|"peer"} 与
//     {"type":"resize","height":<数值>}(高度自适应)三类型;新增类型需
//     双侧同批(前端+双平台壳),未知类型默认忽略,不得崩。
//   - 页面检测到宿主后给 <html> 打 hosted 类,底色转透明——壳必须
//     drawsBackground=false(macOS)/WebView2 透明背景(Windows)+ 系统材质层
//     铺底,否则半透明面板底色失去意义、毛玻璃验收必挂。
//
// 退出码契约(实现计划 Task 14 launcher 据此分派;2026-11-28 撤销方案 B,
// 回归「点击才拉起+关闭即退」):
//
//	0 = 正常关闭,零惩罚——点面板外/失焦/托盘图标请求关闭(close 请求)/
//	    孤儿自检(macOS ppid 看门狗;Windows=Job KILL_ON_JOB_CLOSE 收割);
//	60 = 页面 quit 消息(正常关闭);
//	其余非零 = 崩溃(launcher 记失败重拉,连崩 3 次转原生菜单兜底)。
package panelwin

import (
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// Side —— 面板相对托盘图标弹出的一侧(CLI --side 字面量)。
type Side string

const (
	SideTop    Side = "top"
	SideBottom Side = "bottom"
)

// 退出码契约(Task 14 launcher 判据;撤销方案 B 后 0=正常关闭,由点外/
// 失焦/close 请求/孤儿自检产生)。
const (
	ExitFocusLoss = 0  // 正常关闭(历史名「失焦自退」;点外/失焦/close 请求/孤儿自检共用此码)
	ExitQuit      = 60 // 页面 quit 消息
)

// 面板默认逻辑尺寸(spec §4 面板 340px 宽;高度默认 560)。高度自适应
// (2026-11-28 用户拍板,推翻此前「固定 560 不做自适应」的明确不做清单):
// 页面量内容高经 resize 消息上报,壳 clamp 后跟随改窗口高度——默认值只是
// 首帧形态,稳态高度由页面内容决定。
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
	// Side —— resize 高度变化的锚定边:top=顶边固定向下生长(y 不变),
	// bottom=底边固定向上生长(y += 旧物理高-新物理高)。macOS 托盘恒菜单
	// 栏不消费本字段(恒顶边固定);Windows 壳按它重排 y(panelwin_windows.go
	// applyHeight)。未知值兜底按 bottom(与 AnchorToWindowPos 同口径)。
	Side Side
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
	// Run 阻塞展示面板,直到进程退出。返回退出码:0=正常关闭(点外/失焦/
	// close 请求/孤儿自检)、60=quit。
	// darwin 实现在 quit/点外/失焦/close 请求/孤儿自检路径上经 .m 的
	// exit() 直接终止进程,通常不返回;windows 实现经消息循环正常返回。
	Run(opts Options) int
}

// —— 孤儿自检(macOS;Windows 依赖 Job 收割,见 panel_spawn_windows.go)——
//
// companion 死后面板被 launchd 收养(ppid==1),壳内每 5s 自检 → exit(0)
// (正常关闭,launcher 零惩罚回收)。残留窗口随进程退出彻底消失——
// 「关闭即退」形态下不存在任何驻留窗口。

// panelOrphanWatchInterval 孤儿自检周期(每 5s 查一次 ppid)。
const panelOrphanWatchInterval = 5 * time.Second

// panelOrphanWatchdog 孤儿自检(macOS;调用方 go 起跑):每 interval 查一次
// getppid,==1 即 companion 死后面板被 launchd 收养 → exit(0)(正常关闭,
// launcher 零惩罚回收)。getppid/exit 注入(测试替身);stop 非 nil 时关闭
// 即停(测试收尾;生产传 nil,goroutine 与进程同寿)。Windows 不跑本看门狗:
// Job KILL_ON_JOB_CLOSE 由内核收割(companion 进程死亡 → 句柄回收 → Job
// 连带终止面板),见 panel_spawn_windows.go。
func panelOrphanWatchdog(stop <-chan struct{}, getppid func() int, exit func(int), interval time.Duration) {
	if interval <= 0 {
		interval = panelOrphanWatchInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if getppid() == 1 {
				logger.Info("panelwin: ppid==1(companion 已退出,面板被收养),孤儿自检退出")
				exit(ExitFocusLoss)
				return
			}
		}
	}
}

// —— 高度持久化(首帧尺寸修正 Part 2,2026-11-28 用户反馈「点击瞬间出现
// 大空白区域再缩到目标尺寸」)——
//
// 机制:窗口出生=缺省高 560(--ph 缺省),页面渲染后量实际高 → resize 消息
// → 窗口才缩放;560 与实际高的差值=点击瞬间的「大空白闪一下」。把上次会话
// resize 落盘的稳态逻辑高度作为启动高度读回,窗口出生即正确尺寸,差值消失
// (跨次内容高度变化仍会有的残余闪变由 Part 1「首次上屏延迟到首次 resize
// 到位」兜住,见 panelwin_darwin.m / panelwin_windows.go)。
//
// 落盘路径:两侧壳在 resize 分派时回调本文件 persistLastPanelHeight(macOS
// 经 cgo 导出 panelwinGoHeightChanged(panelwin_darwin.go),Windows 在
// MessageCallback 直调),写 ~/.aliang/panel/last-height 纯数字逻辑高度文本。

const (
	// 高度有效窗(读回判据与落盘守卫同窗):下限 240 与解算层几何兜底
	// resizeMinLogicalH(panelwin_resize.go)同口径;上限 2000 防脏值/防
	// 异常屏形态,超出常见屏高的值没有回放意义。
	panelHeightStateMin = 240
	panelHeightStateMax = 2000
)

// panelHeightStatePath 状态文件路径注入口(生产形态 ~/.aliang/panel/last-height;
// 测试覆写到临时目录,见 panelwin_height_state_test.go)。
var panelHeightStatePath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".aliang", "panel", "last-height"), nil
}

// LoadLastPanelHeight 读上次会话落盘的面板逻辑高度:存在且 240≤h≤2000 才
// 生效;缺文件/损坏/越界一律回落缺省 DefaultPanelH。调用方(cmd/panel.go)
// 以此覆盖 flag 值(launcher 恒传缺省 560,持久值不优先则机制是死代码)。
// 读写失败静默降级,不阻断面板启动。
func LoadLastPanelHeight() int {
	path, err := panelHeightStatePath()
	if err != nil {
		return DefaultPanelH
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return DefaultPanelH
	}
	h, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	if err != nil || h < panelHeightStateMin || h > panelHeightStateMax {
		return DefaultPanelH
	}
	return h
}

// persistLastPanelHeight 落盘本次 resize 的稳态逻辑高度(两侧壳在 resize
// 分派时调用;小数高度截断取整——CSS 内容高常态即整数,截断误差无回放
// 意义)。越界值不落盘(读回侧同窗拒收,写了也白写);目录不存在 MkdirAll
// 兜建;写失败静默降级不阻断。运行在壳的 UI 线程(macOS 主线程/Windows
// 消息循环线程),单文件小写入,不构成卡顿面。
func persistLastPanelHeight(height float64) {
	h := int(height)
	if h < panelHeightStateMin || h > panelHeightStateMax {
		return
	}
	path, err := panelHeightStatePath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(path, []byte(strconv.Itoa(h)), 0o644)
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

// ParseHostMessage —— WebMessage 协议解析(类型白名单:quit/open/resize,
// 新增需双侧同批)。返回 (type, kind, height, known);known=false 的形态
// (未知 type、垃圾 JSON、非 JSON 主体)由调用方直接忽略,不得崩。kind 仅
// 对 open 有意义;height 仅对 resize 有意义——height 缺失或非数值(字符串、
// null、对象等)时返回 0 但 known 仍 true:类型合法、值非法,由调用方自行
// 忽略(Windows 壳以最低阈值闸过滤)。
func ParseHostMessage(payload string) (typ string, kind string, height float64, known bool) {
	// Height 用 RawMessage 拆值:直接 float64 会在 height 为非数值时让整体
	// Unmarshal 报 UnmarshalTypeError、known 被打成 false——类型白名单口径
	// 是「类型认、值拒」,故拆出来单独解,解不动落 0(known 仍 true)。
	var m struct {
		Type   string          `json:"type"`
		Kind   string          `json:"kind"`
		Height json.RawMessage `json:"height"`
	}
	trimmed := strings.TrimSpace(payload)
	if trimmed == "" {
		return "", "", 0, false
	}
	if err := json.Unmarshal([]byte(trimmed), &m); err != nil {
		return "", "", 0, false
	}
	switch m.Type {
	case "quit":
		return "quit", "", 0, true
	case "open":
		return "open", m.Kind, 0, true
	case "resize":
		var h float64
		if len(m.Height) > 0 && json.Unmarshal(m.Height, &h) == nil {
			return "resize", "", h, true
		}
		return "resize", "", 0, true
	default:
		// 未知类型默认忽略(类型白名单;前端已删除协议外 panel_ready):
		// 返回空 type,调用方拿到 known=false 直接丢弃,零分派零崩溃。
		return "", "", 0, false
	}
}
