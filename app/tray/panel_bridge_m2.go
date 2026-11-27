package tray

// companion M2 面板接线(2026-11-26 托盘面板 M2 Task 15):托盘图标点击 →
// launcher 状态机(Task 14)→ spawn 本机可执行文件的 panel 子命令(进程即
// 面板:失焦自退 / quit 退出 / 崩溃,退出码契约见 internal/panelwin.go)。
// M1 的浏览器形态(panel_bridge.go)被取代:面板模式点图标 = launcher.Click,
// 不再开浏览器;openPanelInBrowser 与 installPanelBridge 保留(M1 行为不变,
// 浏览器打开能力仍可被其它路径复用)。
//
// 平台差异收敛在 panel_spawn_darwin.go / panel_spawn_windows.go 两个 shim:
//   - spawn:macOS 直接 exec(**无收割机制**,companion 先退时面板成孤儿,
//     依赖失焦自退退出——计划评审次#13);Windows 三件套 CREATE_SUSPENDED →
//     AllowSetForegroundWindow → Resume + Job KILL_ON_JOB_CLOSE(companion
//     退出收割面板进程)。
//   - 锚定:两平台共用本文件的判废纯函数(Task 4 探针硬要求:状态项窗口有
//     约 800ms 占位期,占位矩形形态 (0,-33) 且 ok=true——**现取现用**(点击
//     时取,launcher 每次 spawn 前经 opts 口重新采样),禁止启动早期缓存;
//     对「x=0 且 y<0」「y≥屏高」「矩形超 1.5 倍主屏」判废)。Windows 判废
//     后走 GetCursorPos 光标兜底;macOS 直传(无光标 API,判废失败退中性
//     顶部锚)。
//
// DPI>100% 锚定收口(计划 :361-364 硬验收):接线层仍传逻辑 pw/ph(与
// cmd/panel.go 语义一致),物理重锚在 internal/panelwin/panelwin_windows.go
// 壳内拿到 GetDpiForWindow scale 后做(见 panelwin_reanchor.go)。
//
// 纯 Go、无构建标签,两平台编译;平台 shim 之外的全部逻辑在本文件(判废、
// 方位、参数组装、兜底切换、退出对接),测试经下方注入口零真实进程/托盘。

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"

	"aliang.one/nursorgate/common/logger"
	"github.com/getlantern/systray"
)

// —— 面板几何常量 ——

const (
	// panelW/panelH 面板逻辑尺寸:与 internal/panelwin 的 DefaultPanelW/H
	// 对齐(不 import,理由见 panel_launcher.go 文件头 Linux 闭包纪律;
	// 漂移由 TestPanelM2PanelSizesMatchShell 结构断言把守)。CSS 视口宽
	// 340 是验收线,漂移即前端几何失真。
	panelW = 340
	panelH = 560

	// panelMenuBarNeutralY macOS 判废失败的中性兜底锚 Y:菜单栏高度量级的
	// 固定值,配 side=top 落在菜单栏下方。判废失败在「点击时现取」的前提下
	// 实际不可达(点击本身要求状态项已摆放),仅作防御。
	panelMenuBarNeutralY = 24

	// panelCursorFallbackHalfSize 光标兜底矩形:图标矩形以光标为中心、边长
	// 2×11=22(与 cmd/panel.go 的 --w/--h flag 缺省 22 同值)。
	panelCursorFallbackHalfSize = 11
)

// —— 注入口(测试替身;缺省形态见各 var 注释)——

// panelAnchorSampler 托盘图标矩形采样口:平台 shim(readPanelTrayAnchor,
// darwin/windows 各自实现),返回图标矩形 + 主屏尺寸(拿不到为 0)+ 光标点
// (macOS 无此 API 恒 HasCursor=false)。launcher 每次 spawn 前(含自动重拉)
// 经 opts 口现取现用。
var panelAnchorSampler = readPanelTrayAnchor

// panelProcessSpawner 面板进程 spawn 口:平台 shim(spawnPanelProcess)。
// 契约同 launcher 的 spawn 注入口:返回的 ChildProcess 不得同步回调状态机。
var panelProcessSpawner = spawnPanelProcess

// panelExecutable 面板可执行文件取值口(测试替身),缺省 os.Executable——
// 面板 = companion 自身可执行文件的 panel 子命令。
var panelExecutable = os.Executable

// panelOpenItemVisor 「打开面板」菜单项显隐口(测试替身),缺省 systray
// Show/Hide 分派。真实 MenuItem 操作需要活托盘,单测必须经此替身拦截。
var panelOpenItemVisor = func(item *systray.MenuItem, show bool) {
	if show {
		item.Show()
	} else {
		item.Hide()
	}
}

// panelQuitInvoker 退出闸注入口(测试替身),缺省转投 CompanionApp.quit
// (两平台各自的确认退出闸:Windows quit 内含 stopProxyForQuit——失败弹窗
// 取消即留在托盘;macOS 直接完整退出序列)。面板内退出 = 确认后的完整退出,
// 照 M1 的 quit 消息语义(计划 Task 15)。
var panelQuitInvoker = func(a *CompanionApp) { a.quit() }

// —— 锚定采样与判废 ——

// panelAnchorSample 托盘图标矩形的一次采样(单位随平台:macOS=点、
// Windows=物理像素,与两侧 TrayRect 语义对应;面板窗口锚定同单位直通)。
type panelAnchorSample struct {
	AX, AY, W, H     int  // 图标矩形(全局左上原点)
	ScreenW, ScreenH int  // 主屏尺寸(物理;拿不到为 0 = 未知,跳过尺寸判废)
	CursorX, CursorY int  // 光标全局坐标(兜底锚用)
	HasCursor        bool // GetCursorPos 成功(Windows);macOS 恒 false
	OK               bool // 平台层 TrayRect 取数成功(未初始化/错误形态=false)
}

// panelAnchorUsable 锚定判废纯函数(Task 4 探针硬要求 1 + 计划 :372):
//   - 占位矩形形态 x=0 且 y<0(实测 (0,-33) 且 ok=true)→ 判废;
//   - y≥屏高(屏外/Win11 溢出区屏外坐标形态)→ 判废;
//   - 矩形超 1.5 倍主屏 → 判废;
//   - 零尺寸矩形 → 判废;
//   - 屏幕尺寸未知(0,macOS 直传形态)→ 跳过尺寸/屏高判废,仅做形态判废。
func panelAnchorUsable(ax, ay, w, h, screenW, screenH int) bool {
	if ax == 0 && ay < 0 {
		return false // 占位矩形形态(Task 4 探针实测 (0,-33))
	}
	if w <= 0 || h <= 0 {
		return false
	}
	if screenH > 0 {
		if ay >= screenH {
			return false // 屏外/溢出区屏外坐标形态(与 Windows S_FALSE 判废同型)
		}
		if h > screenH*3/2 {
			return false // 超 1.5 倍主屏
		}
	}
	if screenW > 0 && w > screenW*3/2 {
		return false // 超 1.5 倍主屏
	}
	return true
}

// panelSideForTray 方位判定纯函数:图标中心在屏幕下半 → bottom(面板向上弹,
// 悬于图标上方);上半 → top(面板向下弹)。屏幕尺寸未知(0,macOS 直传)
// → 恒 top(菜单栏平台,菜单栏恒在顶部)。
func panelSideForTray(ay, h, screenH int) string {
	if screenH <= 0 {
		return panelSideTop
	}
	if (ay+h/2)*2 >= screenH {
		return panelSideBottom
	}
	return panelSideTop
}

// cursorFallbackAnchor 光标兜底矩形:图标矩形以光标为中心(Windows 判废
// 后 TrayRect 不可信,点击瞬间光标就在图标上,以光标定位等价于以图标定位)。
func cursorFallbackAnchor(cx, cy int) (ax, ay, w, h int) {
	return cx - panelCursorFallbackHalfSize, cy - panelCursorFallbackHalfSize,
		panelCursorFallbackHalfSize * 2, panelCursorFallbackHalfSize * 2
}

// neutralTopAnchor 中性顶部锚(macOS 判废失败退路,见 panelMenuBarNeutralY)。
func neutralTopAnchor() (ax, ay, w, h int) {
	return 0, panelMenuBarNeutralY, 0, 0
}

// —— spawn 参数组装(现取现用)——

// panelSpawnOpts 组装 launcher 的启动参数(launcher 每次 spawn 前调用——
// 含自动重拉,锚定坐标现取现用,禁止启动早期缓存)。判废失败依次回落:
// 光标兜底(Windows)→ 中性顶部锚(macOS 无光标 API 的退路)。
func (a *CompanionApp) panelSpawnOpts() SpawnOpts {
	base := a.panelBaseURL()
	sample := panelAnchorSampler()

	ax, ay, w, h := sample.AX, sample.AY, sample.W, sample.H
	if !sample.OK || !panelAnchorUsable(ax, ay, w, h, sample.ScreenW, sample.ScreenH) {
		if sample.HasCursor {
			ax, ay, w, h = cursorFallbackAnchor(sample.CursorX, sample.CursorY)
		} else {
			ax, ay, w, h = neutralTopAnchor()
		}
	}

	return SpawnOpts{
		URL:           panelURL(base),
		AnchorX:       ax,
		AnchorY:       ay,
		IconW:         w,
		IconH:         h,
		PanelW:        panelW,
		PanelH:        panelH,
		Side:          panelSideForTray(ay, h, sample.ScreenH),
		OpenDashboard: base,
		OpenPeer:      base,
	}
}

// panelSpawnArgs panel 子命令参数组装(纯函数):与 cmd/panel.go 的 flags
// 一一对应;可执行文件本体由 exec.Command 首参携带,不在返回值内。
func panelSpawnArgs(opts SpawnOpts) []string {
	return []string{
		"panel",
		"--url", opts.URL,
		"--ax", strconv.Itoa(opts.AnchorX),
		"--ay", strconv.Itoa(opts.AnchorY),
		"--w", strconv.Itoa(opts.IconW),
		"--h", strconv.Itoa(opts.IconH),
		"--pw", strconv.Itoa(opts.PanelW),
		"--ph", strconv.Itoa(opts.PanelH),
		"--side", opts.Side,
		"--open-dashboard", opts.OpenDashboard,
		"--open-peer", opts.OpenPeer,
	}
}

// —— launcher 装配与退出转投 ——

// newPanelLauncher 构造 M2 形态的 launcher 并落到 a.panelLauncher。⚠️ 落
// 字段必须发生在任何菜单/转投 goroutine 启动之前(两平台 NewCompanionApp
// 在 onReady 之前调用,写读之间有 goroutine 创建边,天然无 race);直构
// CompanionApp 的测试形态由 installPanelLauncher 的 nil 守卫兜住(见下)。
// 回调契约:on* 锁外触发(见 panel_launcher.go 文件头),回调体内的系统
// 调用(register/mode/visor/notifier/quit)都不回调状态机,无自锁面。
func (a *CompanionApp) newPanelLauncher() *PanelLauncher {
	l := &PanelLauncher{
		spawn: func(opts SpawnOpts) (ChildProcess, error) {
			child, err := panelProcessSpawner(opts)
			if err != nil {
				return nil, err
			}
			// 退出转投 goroutine:Wait 阻塞到进程退出,不经 spawn 调用栈,
			// 无「锁内同步回呼」风险(launcher 文件头契约)。
			go a.forwardPanelExit(child)
			return child, nil
		},
		opts: a.panelSpawnOpts,
		// onFocusFallback 面板失焦自退(code=0)的可选项,最小实现:空。
		// 零惩罚语义由状态机保证,无需额外可见面(面板模式本就由点击 toggle)。
		onFocusFallback: func() {},
		onFallback:      a.enterPanelFallback,
		onQuitRequested: func() { panelQuitInvoker(a) },
	}
	a.panelLauncher = l
	return l
}

// installPanelLauncher 挂接 M2 面板接线(onReady 末尾调用,两平台;取代 M1
// 的 installPanelBridge):先注册回调(launcher.Click)再开面板模式——M1
// 时序契约延续(macOS 摘菜单前回调必须就位,Windows panelClickFn 非 nil 即
// 接管左右键)。launcher 本体生产形态在 NewCompanionApp 已装配(写读顺序
// 见 newPanelLauncher);此处 nil 守卫只服务直构 CompanionApp 的测试形态。
func (a *CompanionApp) installPanelLauncher() {
	if a.panelLauncher == nil {
		a.newPanelLauncher()
	}
	panelClickRegister(a.panelLauncher.Click)
	panelModeSetter(true)
}

// forwardPanelExit 子进程退出转投(接线层契约,panel_launcher.go 文件头):
// child.Wait() 的退出码 → exitReport(必须先于 childExited——childExited
// 清 spawnedAt,错序则 alive 恒 0 静默失效)→ childExited(code, alive,
// killedByUs)。跑在 spawn 闭包的裸 go goroutine 上(状态机不防御回调
// panic),整体包 recover 留痕——转投链异常不得炸托盘 companion。
func (a *CompanionApp) forwardPanelExit(child ChildProcess) {
	defer func() {
		if r := recover(); r != nil {
			logger.Error(fmt.Sprintf("panel exit forward panicked (recovered): %v", r))
		}
	}()
	code := <-child.Wait()
	alive, killedByUs := a.panelLauncher.exitReport()
	a.panelLauncher.childExited(code, alive, killedByUs)
}

// —— 兜底切换与恢复 ——

// enterPanelFallback 兜底态回调(滑窗满 3 崩):恢复原生菜单形态 + 「打开
// 面板」恢复项 + 桌面通知。
//   - Windows:panelClickFn 非 nil 即接管左右键(原生菜单不弹),置 nil 才能还
//     菜单给用户——否则兜底态「打开面板」不可达(点击回调本尊 launcher.Click
//     在兜底态是无害 no-op,但菜单弹不出来)。
//   - macOS:SetPanelMode(false) 摘回调接回原生菜单。
func (a *CompanionApp) enterPanelFallback() {
	panelClickRegister(nil)
	panelModeSetter(false)
	if a.mPanelOpen != nil {
		panelOpenItemVisor(a.mPanelOpen, true)
	}
	panelNotifier("Aliang", "面板连续崩溃,已接回菜单,点「打开面板」重试")
}

// restorePanelMode 面板模式恢复(「打开面板」菜单项的对称动作):重挂
// launcher.Click + 开面板模式 + 隐藏恢复项。幂等——重复调用只是把同样的
// 回调/开关再设一遍。
func (a *CompanionApp) restorePanelMode() {
	if a.panelLauncher != nil {
		panelClickRegister(a.panelLauncher.Click)
	}
	panelModeSetter(true)
	if a.mPanelOpen != nil {
		panelOpenItemVisor(a.mPanelOpen, false)
	}
}

// handlePanelOpenClick 「打开面板」菜单项点击(兜底态恢复入口):退出兜底态
// 并立即 spawn 一次(spawn 失败走状态机既有崩溃计数 + 1s 重拉路径),随后
// 恢复面板模式。非兜底态下该项不可见,点得到即兜底态;防御性幂等由
// RetryFromFallback 自带(正常态无效)。
func (a *CompanionApp) handlePanelOpenClick() {
	if a.panelLauncher == nil {
		return // 未装配(装配缺陷守卫;onReady 必装 installPanelLauncher)
	}
	a.panelLauncher.RetryFromFallback()
	a.restorePanelMode()
}

// —— ChildProcess 的 os/exec 实现(两平台共用) ——

// osExecChild ChildProcess 的 os/exec 实现:Wait 返回退出码通道(进程退出后
// 送达一次),Kill 杀进程(os.Process.Kill:darwin=SIGKILL 信号死亡 exit −1,
// windows=TerminateProcess)。
type osExecChild struct {
	cmd  *exec.Cmd
	wait chan int
}

// startPanelChild 启动命令并挂退出码 goroutine:ExitCode() 对信号死亡返回
// −1(kill -9 有意收起的信号态亦落此,由 killedByUs 标记在状态机里兜成
// 正常关闭,零惩罚);等待本身失败按崩溃码 1 处置。
func startPanelChild(cmd *exec.Cmd) (ChildProcess, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &osExecChild{cmd: cmd, wait: make(chan int, 1)}
	go func() {
		code := 1
		if err := cmd.Wait(); err == nil {
			code = 0
		} else if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		}
		c.wait <- code
	}()
	return c, nil
}

// Wait 退出码通道(契约:关闭/送达即进程退出;本实现恒送达一次)。
func (c *osExecChild) Wait() <-chan int { return c.wait }

// Kill 杀进程(点图标收起路径;退出码形态由 killedByUs 标记兜住)。
func (c *osExecChild) Kill() {
	if c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}
