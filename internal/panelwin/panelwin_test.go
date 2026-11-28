package panelwin

import (
	"strings"
	"testing"
	"time"
)

// 锚定纯函数表驱动(实现计划 Task 13):side=bottom → y=ay-panelH-8;side=top →
// y=ay+h+4;x 以图标水平居中并钳左边距 8。含钳边/负坐标边界。
//
// 单位口径:入参 (ax,ay,w,h) 是托盘图标矩形,坐标随平台单位(macOS=点、
// Windows=物理像素,两侧 TrayRect 语义对应);面板宽高即调用方传入的原值,
// 本函数只做纯算术,不做 DPI 换算(Windows 侧尺寸×scale 在壳内做)。
func TestAnchorToWindowPos(t *testing.T) {
	const iconW, iconH = 22, 22
	const panelW, panelH = 340, 560

	cases := []struct {
		name           string
		ax, ay, w, h   int
		side           Side
		panelW, panelH int
		wantX, wantY   int
	}{
		{
			name: "bottom 基本形态:图标下方留 8",
			ax:   100, ay: 600, w: iconW, h: iconH,
			side: SideBottom, panelW: panelW, panelH: panelH,
			// x = 100 + 22/2 - 340/2 = -59 → 钳到左边距 8
			wantX: 8, wantY: 600 - panelH - 8,
		},
		{
			name: "top 基本形态:图标上方留 4",
			ax:   100, ay: 600, w: iconW, h: iconH,
			side: SideTop, panelW: panelW, panelH: panelH,
			wantX: 8, wantY: 600 + iconH + 4,
		},
		{
			name: "x 居中且不触边",
			ax:   800, ay: 40, w: iconW, h: iconH,
			side: SideBottom, panelW: panelW, panelH: panelH,
			wantX: 800 + iconW/2 - panelW/2, wantY: 40 - panelH - 8,
		},
		{
			name: "x 恰好落在边距 8 上(等值不钳)",
			ax:   167, ay: 40, w: iconW, h: iconH, // 167+11-170=8
			side: SideTop, panelW: panelW, panelH: panelH,
			wantX: 8, wantY: 40 + iconH + 4,
		},
		{
			name: "钳边:ax=0 时左钳 8",
			ax:   0, ay: 40, w: iconW, h: iconH,
			side: SideBottom, panelW: panelW, panelH: panelH,
			wantX: 8, wantY: 40 - panelH - 8,
		},
		{
			name: "负坐标:x 照钳、y 直通不钳",
			ax:   -50, ay: -100, w: iconW, h: iconH,
			side: SideTop, panelW: panelW, panelH: panelH,
			wantX: 8, wantY: -100 + iconH + 4,
		},
		{
			name: "负坐标 bottom:y 可为负(多屏全局坐标合法)",
			ax:   20, ay: -50, w: iconW, h: iconH,
			side: SideBottom, panelW: panelW, panelH: panelH,
			wantX: 8, wantY: -50 - panelH - 8,
		},
		{
			name: "窄屏参数(图标矩形极小):w/2 向下取整",
			ax:   200, ay: 30, w: 1, h: 1,
			side: SideBottom, panelW: panelW, panelH: panelH,
			wantX: 200 - panelW/2, wantY: 30 - panelH - 8,
		},
		{
			// panelwin.go 的 default 分支:SideBottom 与未知值同款兜底
			// (接线层已先校验 --side,壳内不再二次防御)。
			name: "未知 side 兜底按 bottom(default 分支)",
			ax:   800, ay: 40, w: iconW, h: iconH,
			side: Side("left"), panelW: panelW, panelH: panelH,
			wantX: 800 + iconW/2 - panelW/2, wantY: 40 - panelH - 8,
		},
		{
			// 零/负面板尺寸不做防御直通参与运算:合法性归接线层判废兜底。
			// panelW=0 只影响 x 居中项,y 仍按 panelH 算(bottom 分支)。
			name: "panelW=0:x 居中项归零、y 照用 panelH",
			ax:   200, ay: 40, w: iconW, h: iconH,
			side: SideBottom, panelW: 0, panelH: panelH,
			wantX: 200 + iconW/2, wantY: 40 - panelH - 8,
		},
		{
			name: "panelW 负值:直通运算(x 反向偏移)",
			ax:   200, ay: 40, w: iconW, h: iconH,
			side: SideBottom, panelW: -340, panelH: panelH,
			wantX: 200 + iconW/2 + 340/2, wantY: 40 - panelH - 8,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotX, gotY := AnchorToWindowPos(tc.ax, tc.ay, tc.w, tc.h, tc.side, tc.panelW, tc.panelH)
			if gotX != tc.wantX || gotY != tc.wantY {
				t.Fatalf("AnchorToWindowPos(%d,%d,%d,%d,%s,%d,%d) = (%d,%d), want (%d,%d)",
					tc.ax, tc.ay, tc.w, tc.h, tc.side, tc.panelW, tc.panelH,
					gotX, gotY, tc.wantX, tc.wantY)
			}
		})
	}
}

// 退出码契约(Task 14 launcher 据此分派:0=正常关闭(点外/失焦/close 请求/
// 孤儿自检)、60=quit,其余=崩溃)。
func TestExitCodeContract(t *testing.T) {
	if ExitFocusLoss != 0 || ExitQuit != 60 {
		t.Fatalf("退出码契约漂移:ExitFocusLoss=%d ExitQuit=%d(launcher 判据 0/60)", ExitFocusLoss, ExitQuit)
	}
}

// LoopbackOrigin:导航白名单的锚点——只放行 --url 的回环 origin(调研 §6.3
// 不变量:面板访问的是 core 回环源,免 cookie 即完整控制权,不得加载任何
// 第三方内容)。
func TestLoopbackOrigin(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "127 回环带端口", raw: "http://127.0.0.1:56431/panel.html", want: "http://127.0.0.1:56431"},
		{name: "localhost", raw: "http://localhost:56431/panel.html?x=1", want: "http://localhost:56431"},
		{name: "IPv6 回环", raw: "http://[::1]:56431/panel.html", want: "http://[::1]:56431"},
		{name: "大小写归一", raw: "http://LOCALHOST:56431/A/B", want: "http://localhost:56431"},
		{name: "缺端口", raw: "http://127.0.0.1/panel.html", want: "http://127.0.0.1"},
		{name: "非回环拒绝", raw: "http://192.168.1.5:56431/panel.html", wantErr: true},
		{name: "公网域名拒绝", raw: "https://example.com/panel.html", wantErr: true},
		{name: "file 协议拒绝", raw: "file:///tmp/panel.html", wantErr: true},
		{name: "空串拒绝", raw: "", wantErr: true},
		{name: "非 URL 拒绝", raw: "not a url", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := LoopbackOrigin(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("LoopbackOrigin(%q) 期望报错,得到 origin=%q", tc.raw, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("LoopbackOrigin(%q) 意外报错: %v", tc.raw, err)
			}
			if got != tc.want {
				t.Fatalf("LoopbackOrigin(%q) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}

// AllowDocument:主文档导航闸——只有 origin 恰等于白名单 origin 的文档放行
// (Windows 侧经 WebResourceRequested DOCUMENT 过滤器落地,macOS 侧经
// decidePolicyFor 落地)。
func TestAllowDocument(t *testing.T) {
	const origin = "http://127.0.0.1:56431"

	cases := []struct {
		name string
		uri  string
		want bool
	}{
		{name: "同 origin 文档放行", uri: "http://127.0.0.1:56431/panel.html", want: true},
		{name: "同 origin 根路径放行", uri: "http://127.0.0.1:56431/", want: true},
		{name: "同 origin 带查询串放行", uri: "http://127.0.0.1:56431/panel.html?v=2", want: true},
		{name: "同主机异端口拒绝", uri: "http://127.0.0.1:56432/panel.html", want: false},
		{name: "第三方 origin 拒绝", uri: "https://evil.example/x.html", want: false},
		{name: "file 协议拒绝", uri: "file:///etc/passwd", want: false},
		{name: "about:blank 拒绝", uri: "about:blank", want: false},
		{name: "解析失败拒绝", uri: "::not-a-url::", want: false},
		{name: "大小写变体拒绝校验仍按 origin 判", uri: "http://127.0.0.1:56431/%E4%B8%AD%E6%96%87", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := AllowDocument(tc.uri, origin); got != tc.want {
				t.Fatalf("AllowDocument(%q, %q) = %v, want %v", tc.uri, origin, got, tc.want)
			}
		})
	}
}

// WebMessage 类型白名单(Task 7 评审固化;resize 为 2026-11-28 用户拍板的
// 追加):quit/open/resize 三类型;未知类型必须被识别为「忽略」而非崩溃。
// resize 的 height 三态:合法数值随 typ 返回;缺失/非数值时 height=0 且
// known 仍 true(类型认、值拒,调用方自行忽略)。
func TestParseHostMessage(t *testing.T) {
	cases := []struct {
		name       string
		payload    string
		wantType   string
		wantKind   string
		wantHeight float64
		wantKnown  bool
	}{
		{
			name: "quit", payload: `{"type":"quit"}`,
			wantType: "quit", wantKnown: true,
		},
		{
			name: "open dashboard", payload: `{"type":"open","kind":"dashboard"}`,
			wantType: "open", wantKind: "dashboard", wantKnown: true,
		},
		{
			name: "open peer", payload: `{"type":"open","kind":"peer"}`,
			wantType: "open", wantKind: "peer", wantKnown: true,
		},
		{
			name: "open 无 kind(拒绝分派,kind 空)", payload: `{"type":"open"}`,
			wantType: "open", wantKnown: true,
		},
		{
			name: "resize 合法整数 height", payload: `{"type":"resize","height":420}`,
			wantType: "resize", wantHeight: 420, wantKnown: true,
		},
		{
			name: "resize 合法小数 height", payload: `{"type":"resize","height":420.7}`,
			wantType: "resize", wantHeight: 420.7, wantKnown: true,
		},
		{
			name: "resize 缺 height→height=0 但 known=true", payload: `{"type":"resize"}`,
			wantType: "resize", wantKnown: true,
		},
		{
			name: "resize height 非数值→height=0 但 known=true", payload: `{"type":"resize","height":"abc"}`,
			wantType: "resize", wantKnown: true,
		},
		{
			name: "resize height null→height=0 但 known=true", payload: `{"type":"resize","height":null}`,
			wantType: "resize", wantKnown: true,
		},
		{
			name: "未知类型→忽略不崩", payload: `{"type":"panel_ready"}`, wantKnown: false,
		},
		{
			name: "垃圾 JSON→忽略不崩", payload: `not json`, wantKnown: false,
		},
		{
			name: "空串→忽略不崩", payload: ``, wantKnown: false,
		},
		{
			name: "JSON 数组→忽略不崩", payload: `["quit"]`, wantKnown: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			typ, kind, height, known := ParseHostMessage(tc.payload)
			if typ != tc.wantType || kind != tc.wantKind || height != tc.wantHeight || known != tc.wantKnown {
				t.Fatalf("ParseHostMessage(%q) = (%q,%q,%v,%v), want (%q,%q,%v,%v)",
					tc.payload, typ, kind, height, known, tc.wantType, tc.wantKind, tc.wantHeight, tc.wantKnown)
			}
		})
	}
}

// OpenTarget:open 消息按 kind 取对应 URL;未知 kind 返回空串=忽略分派。
func TestOpenTarget(t *testing.T) {
	opts := Options{OpenDashboard: "http://127.0.0.1:56431/", OpenPeer: "http://127.0.0.1:56431/peers"}
	if got := opts.OpenTarget("dashboard"); got != opts.OpenDashboard {
		t.Fatalf("OpenTarget(dashboard) = %q, want %q", got, opts.OpenDashboard)
	}
	if got := opts.OpenTarget("peer"); got != opts.OpenPeer {
		t.Fatalf("OpenTarget(peer) = %q, want %q", got, opts.OpenPeer)
	}
	if got := opts.OpenTarget("bogus"); got != "" {
		t.Fatalf("OpenTarget(bogus) = %q, want 空串(未知 kind 忽略)", got)
	}
}

// Side 常量与 CLI --side 值对齐(接线层校验只认这两个字面量)。
func TestSideConstants(t *testing.T) {
	if SideTop != "top" || SideBottom != "bottom" {
		t.Fatalf("Side 字面量漂移: %q / %q", SideTop, SideBottom)
	}
}

// Options 文档位——保持 Options.OpenDashboard/OpenPeer 字段名稳定(前端 open
// kind 与字段一一对应),防止重构改名甩断宿主分派。
func TestOptionsFieldDocAnchors(t *testing.T) {
	// 仅编译期锚定:字段存在性 + 类型。
	var o Options
	o.URL = "http://127.0.0.1:56431/panel.html"
	o.X, o.Y = 8, 32
	o.PanelW, o.PanelH = 340, 560
	o.OpenDashboard = "http://127.0.0.1:56431/"
	o.OpenPeer = "http://127.0.0.1:56431/"
	if !strings.HasPrefix(o.URL, "http://127.0.0.1") {
		t.Fatal("unreachable")
	}
}

// —— 孤儿自检(「关闭即退」形态的兜底防护)——

// TestPanelOrphanWatchdog 孤儿自检(macOS):ppid==1(companion
// 死后被 launchd 收养)→ exit(0);ppid 正常 → 不退出。stop 关闭即停(测试
// 收尾;生产不传,goroutine 与进程同寿)。
func TestPanelOrphanWatchdog(t *testing.T) {
	t.Run("ppid==1 触发退出码0", func(t *testing.T) {
		exitCodes := make(chan int, 1)
		stop := make(chan struct{})
		go panelOrphanWatchdog(stop, func() int { return 1 }, func(code int) {
			select {
			case exitCodes <- code:
			default:
			}
		}, 10*time.Millisecond)
		select {
		case code := <-exitCodes:
			if code != ExitFocusLoss {
				t.Fatalf("孤儿退出码 = %d, want %d(正常关闭,launcher 零惩罚)", code, ExitFocusLoss)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("ppid==1 未触发孤儿退出")
		}
		close(stop)
	})

	t.Run("ppid 正常不退出", func(t *testing.T) {
		exitCodes := make(chan int, 1)
		stop := make(chan struct{})
		go panelOrphanWatchdog(stop, func() int { return 4242 }, func(code int) {
			select {
			case exitCodes <- code:
			default:
			}
		}, 10*time.Millisecond)
		select {
		case code := <-exitCodes:
			t.Fatalf("companion 存活(ppid=%d)却触发孤儿退出:code=%d", 4242, code)
		case <-time.After(100 * time.Millisecond): // ≥8 个检查周期
		}
		close(stop)
	})
}
