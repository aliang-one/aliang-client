package tray

import (
	"fmt"
	"sync"
	"time"
)

// 面板 launcher 生命周期状态机(M1 计划 Task 14,纯逻辑)。
//
// 职责:托盘图标点击 → spawn 面板原生壳(cmd/panel,进程即面板),并按
// 拍板 1「只兜崩溃」做失败判定。2026-11-28 撤销方案 B「首次后常驻」,回归
// 「点击才拉起+关闭即退」:无子 → spawn;有子 → 发 close 请求(SIGUSR1/
// WM_APP+1,子进程 exit(0) 正常关闭,窗口随进程退出消失——双击保护语义
// 变为「有子在册=点图标即请求关闭」);点面板外/失焦 → 子进程 exit(0)。
// 退出码契约见 internal/panelwin.go 与 cmd/panel.go——0=正常关闭(点外/
// 失焦/close 请求/孤儿自检)、60=quit、其余=崩溃:
//
//   - 崩溃 = code != 0 && code != 60 且非 killedByUs(外部 kill -9 的
//     信号死亡 exit −1 亦落入;spawn 本身 fork/exec 失败同罪)→ 记失败
//     1 次 + 1s 后重拉;
//   - code == 0(点外/失焦/close 请求/孤儿自检的正常关闭,分派
//     onFocusFallback)与 killedByUs(兼容通道,主流程不再打标)= 正常
//     关闭,零惩罚零重拉;
//   - 10 分钟滑窗内 3 次崩溃 → onFallback()(菜单接回,Task 15)+ 停拉
//     (进兜底态,Click 无效);
//   - 存活 >60s 的成功会话清零失败滑窗;
//   - code == 60 → onQuitRequested(分派退出意图,不重拉;Task 15 对到
//     既有 quit 闸);
//   - 兜底态仅 RetryFromFallback 可退出(「打开面板」菜单项)并立即
//     spawn 一次。
//
// close 请求语义:Click 对「有子」不发 Kill,只经 requestClose 注入口投递
// 关闭请求(darwin=SIGUSR1 / windows=FindWindow+PostMessage WM_APP+1,
// 平台 shim 见 panel_spawn_darwin.go / panel_spawn_windows.go)。子进程收到
// 请求即 exit(0)——退出码是 0(正常关闭),故 launcher 无需为 close 打
// killedByUs 标:killedByUs 不打标也不会把正常关闭误计为崩溃(准绳)。请求
// 发送失败(子进程已死,kill 返回 ESRCH)→ 按无子处理:清跟踪状态 + 立即
// spawn;迟到的旧子退出报告经 childExitedIfCurrent 的身份闸让位(不得清掉
// 新子、不得误计)。
//
// 【三条裁定】(计划未明说,本实现按此拍板并经测试锚定):
//
//  1. RetryFromFallback 的 spawn 失败走既有崩溃计数 + 1s 重拉路径(同一
//     滑窗),连败再满 3 次则重新 onFallback;成功则回正常态。滑窗在 Retry
//     进入正常态时即清零——「连败再满 3 次」从 Retry 起独立计数,不受旧
//     滑窗残留影响。
//  2. 重拉 1s 延迟窗内视为「未开」:Click 允许立即 spawn,并作废挂起的
//     重拉。作废=置 pendingRetry=nil 是真机制——调度重拉时锁内真登记
//     (pendingRetry=retryNow),被作废的迟到到期动作见 nil 即让位;仅此
//     不够,retryNow 另保留二次校验(兜底态 / 已有面板即让位),不二次
//     spawn。
//  3. 兜底态进入即作废滑窗与挂起重拉:兜底是「停拉」终态,一切自动重拉
//     停摆,恢复只认 RetryFromFallback。
//
// 可测性契约(测试零真实定时器、零真实进程):
//   - 重拉 1s 延迟经 scheduleRetry 注入钩子(缺省 time.AfterFunc),测试
//     注入同步/截获版手工触发到期动作;
//   - 滑窗与存活起点经注入 now(缺省 time.Now);now 注入禁 panic(真实
//     形态为缺省 time.Now/测试 fakeClock,不会 panic;自定义实现须遵守);
//   - requestClose 经注入桩(缺省 nil=装配缺陷守卫让位,真实装配必装);
//   - 子进程退出由接线层(Task 15)转投 childExitedIfCurrent(child,
//     code, alive, killedByUs),alive 用 time.Since(spawn 起点),
//     killedByUs 兼容通道恒 false(close 请求的退出码是 0,无需打标;防
//     close 请求到达前子进程已自行退出场景的兜底说明见 childExited);
//   - 并发面:Click(托盘点击 goroutine)/ childExited(退出转投)/
//     RetryFromFallback(菜单点击)互并发,mu 保护全部状态。spawn/opts
//     在锁内调用、有意设计(消灭 Click/retryNow 双 spawn 间隙):装配层
//     注入的实现禁同步回调本状态机任何方法(否则自锁——Go 非重入锁,含
//     经日志等间接回呼状态机方法);本状态机对二者 panic 兜底转崩溃计数
//     (见 spawnAttempt),不外漏。on* 回调与 scheduleRetry/requestClose
//     钩子一律在锁外触发。
//
// 纯逻辑、无构建标签、GOOS 无关:不 import internal/panelwin(其无 Linux
// 形态,app/tray 引用会破 Linux 依赖闭包闸,见 cmd/panel.go 闭包纪律),
// Side 用字面量常量 + 测试锚定漂移;Windows 专属逻辑在 Task 15 接线层。

const (
	// panelCrashRetryDelay 崩溃后的重拉延迟(计划原文「1s 后重拉」)。
	panelCrashRetryDelay = time.Second

	// panelCrashWindow 崩溃计数滑窗宽度(计划原文「10 分钟滑窗」)。
	panelCrashWindow = 10 * time.Minute

	// panelCrashFallbackLimit 滑窗内崩溃兜底阈值(计划原文「3 次崩溃」)。
	panelCrashFallbackLimit = 3

	// panelLongSessionClear 清零滑窗的成功会话最短存活(计划原文「存活
	// >60s 的成功会话清零失败滑窗」)。
	panelLongSessionClear = time.Minute

	// panelExitCodeQuit 面板 quit 退出码(panelwin 契约:60=quit)。
	panelExitCodeQuit = 60
)

// Side 字面量:与 internal/panelwin.Side 对齐(不 import,理由见文件头)。
const (
	panelSideTop    = "top"    // panelwin.SideTop
	panelSideBottom = "bottom" // panelwin.SideBottom
)

// ChildProcess 面板子进程最小接口:Wait 返回退出码通道(关闭/送达即进程
// 退出),Kill 杀进程,Pid 取 close 请求投递地址。实现契约:三方法不得
// 同步回调 PanelLauncher。Kill 保留兼容但 launcher 主流程不再调用(关闭只
// 走 close 请求,零 Kill);外部 kill -9 的信号死亡仍按崩溃分诊(分类逻辑
// 不变)。
type ChildProcess interface {
	Wait() <-chan int
	Kill()
	// Pid 子进程 pid:spawn 返回后即稳定可读。darwin close 请求走
	// SIGUSR1(pid 投递);windows close 请求走 FindWindow+PostMessage(类名
	// 定位,忽略 pid——签名统一为 func(pid int) error)。
	Pid() int
}

// SpawnOpts 面板启动参数,字段名与 cmd/panel.go 的 flags 一一对应(Task 15
// 装配:launcher 每次 spawn 现取现用,锚定坐标禁止启动早期缓存)。
type SpawnOpts struct {
	URL     string // --url 面板页地址(回环 origin)
	AnchorX int    // --ax 托盘图标矩形 X(全局左上原点;单位随平台:macOS=点、Windows=物理像素,同 TrayRect 语义)
	AnchorY int    // --ay 托盘图标矩形 Y(全局左上原点;单位随平台:macOS=点、Windows=物理像素,同 TrayRect 语义)
	IconW   int    // --w 托盘图标矩形宽
	IconH   int    // --h 托盘图标矩形高
	PanelW  int    // --pw 面板逻辑宽
	PanelH  int    // --ph 面板逻辑高
	Side    string // --side 面板相对图标方位:top|bottom(panelSideTop/Bottom)
	// --open-dashboard / --open-peer 打开类深链仪表盘与设备互访根页。
	OpenDashboard string
	OpenPeer      string
}

// panelLaunchPhase 状态机相:正常(自动重拉在役)/ 兜底(停拉,仅 Retry 可出)。
type panelLaunchPhase uint8

const (
	phaseNormal panelLaunchPhase = iota
	phaseFallback
)

// PanelLauncher 托盘面板 launcher 状态机;零值不可用,由 Task 15 装配后
// 使用。必装清单:spawn/onFallback 必装(onFallback 缺失会困死兜底态零
// 可见);onFocusFallback/onQuitRequested 可空(缺省零动作);opts/now/
// scheduleRetry 有缺省形态。字段语义见文件头。
type PanelLauncher struct {
	mu sync.Mutex

	// spawn 面板进程注入口(Task 15 装配 exec;测试注入替身)。
	spawn func(SpawnOpts) (ChildProcess, error)
	// requestClose 关闭请求投递注入口:对在册子进程发 close 请求(SIGUSR1/
	// WM_APP+1,子进程 exit(0) 正常关闭)。缺省 nil = 装配缺陷守卫让位
	// (真实装配必装 panelToggleSender);返回非 nil 错误 = 投递失败(子进程
	// 已死),Click 按无子处理回退 spawn。
	requestClose func(pid int) error
	// opts 启动参数现取口:每次 spawn 前调用(锚定坐标现取现用)。
	opts func() SpawnOpts
	// now 时钟注入口,缺省 time.Now(测试注入假钟)。
	now func() time.Time
	// scheduleRetry 重拉调度注入口,缺省 time.AfterFunc(测试注入同步版,
	// 零真实定时器)。约定:d 后另行触发 fn,不得在调用栈内同步执行。
	scheduleRetry func(d time.Duration, fn func())

	// onFallback 滑窗满 3 崩回调:菜单接回 + 「打开面板」恢复项(Task 15)。
	onFallback func()
	// onFocusFallback code=0 正常关闭回调(点面板外/失焦/close 请求/孤儿
	// 自检,「关闭即退」下退出码 0 的主产出口):刷新「面板已关」可见面。
	// 零惩罚,与崩溃兜底无关。
	onFocusFallback func()
	// onQuitRequested 面板 quit 退出意图回调(Task 15 对到既有 quit 闸)。
	onQuitRequested func()

	// —— 状态(mu 保护)——
	phase        panelLaunchPhase // 当前相
	child        ChildProcess     // 在册面板(nil=未开/重拉窗/兜底)。「有子」判断与 close 请求失败回退是唯一状态源(子进程在册即可能收到下一次 close 请求)
	spawnedAt    time.Time        // 存活起点(接线层算 alive 用)
	killedByUs   bool             // 有意收起标记(兼容通道:主流程不再打标,恒 false——close 请求的退出码是 0,子进程已死时投递失败走无子回退,两条路都不需要打标;分类逻辑保留防外部信号死亡误判归零惩罚)
	pendingRetry func()           // 挂起的重拉到期动作真登记(调度重拉时置 retryNow);nil=无挂起,迟到的旧 fire 见 nil 即让位(作废=Click/兜底/Retry 置 nil)
	crashTimes   []time.Time      // 崩溃滑窗(入窗即 prune)
}

// Click 托盘图标点击入口(2026-11-28 撤销方案 B,回归「点击才拉起+关闭
// 即退」):无子 → spawn;有子 → 发 close 请求(子进程 exit(0) 正常关闭,
// 窗口随进程退出消失;双击保护语义=「有子在册=点图标即请求关闭」,close
// 请求期间重复点击幂等重发);兜底态无效(不发 spawn 不报错,仅
// RetryFromFallback 可恢复);重拉 1s 延迟窗内视为「未开」→ 立即 spawn
// 并作废挂起重拉(裁定 2);「崩溃已死未转投」窗口内视为「未开」→ 消费
// 退出码代行收尸(原样分诊,崩溃照计)后立即 spawn;close 请求发送失败
// (子进程已死)→ 按无子处理:清跟踪状态 + 立即 spawn。
func (l *PanelLauncher) Click() {
	l.mu.Lock()
	if l.phase == phaseFallback {
		// 兜底态点击无效:不发 spawn 不报错(裁定 3)
		l.mu.Unlock()
		return
	}
	if l.child != nil {
		child := l.child
		// 「崩溃已死未转投」窗口的非阻塞探测:面板已死但转投 goroutine
		// 尚未跑到分诊的间隙里 child 仍非 nil——此窗内直接发 close 请求
		// 会让迟到的崩溃码等在通道里无人消费(崩溃漏计 + 请求发给死进程),
		// 须先探测。
		select {
		case code := <-child.Wait():
			// 退出码已送达 = 面板已死,本次点击是「开」不是「请求关闭」。
			// 退出码已被本点击抢先消费,转投 goroutine 的 <-child.Wait()
			// 按「送达一次」契约永不再送达,其收尸(exitReport+分诊)永
			// 不执行——收尸由本点击代行:先 exitReport(取即清零契约照旧,
			// 须先于 childExited)再 childExited 原样分诊(killedByUs 如
			// 实传递——兼容通道恒 false,崩溃照计;满 3 兜底由其自处),
			// 随后按「开」语义作废挂起重拉立即 spawn(与重拉延迟窗的
			// Click 让位同型)。代价:转投 goroutine 永久阻塞泄漏一枚
			// (窗口=崩溃落地到转投执行之间,罕见且每窗至多一枚,接受)。
			l.mu.Unlock()
			alive, killedByUs := l.exitReport()
			l.childExited(code, alive, killedByUs)
			l.mu.Lock()
			l.pendingRetry = nil
			l.mu.Unlock()
			l.spawnAttempt()
			return
		default:
			// 存活 → 发 close 请求:requestClose=nil=装配缺陷守卫让位
			// (不发请求不 spawn 不崩,与 spawnAttemptLocked 的 nil-spawn
			// 守卫同风格,真实装配必装)。
			requestClose, pid := l.requestClose, child.Pid()
			l.mu.Unlock()
			if requestClose == nil {
				return
			}
			if err := requestClose(pid); err != nil {
				// 发送失败 = 子进程已死(探测后死亡的窗口):按无子处理
				// ——清跟踪状态 + 立即 spawn。迟到的旧子退出报告由
				// childExitedIfCurrent 的身份闸让位(新子已在册时不清
				// 新子引用、不误计;spawn 也失败时旧报告照常分诊补排重拉)。
				l.mu.Lock()
				l.child = nil
				l.spawnedAt = time.Time{}
				l.pendingRetry = nil
				l.mu.Unlock()
				l.spawnAttempt()
				return
			}
			// close 请求成功:子进程收到即 exit(0)(正常关闭),退出事件
			// 由转投链按 code=0 正常关闭收尾(child 清理归 childExited);
			// 请求送达前的窗口里 child 保持在册 = 期间重复点击幂等重发
			// (不重复 spawn 不 Kill,双击保护语义)。
			return
		}
	}
	// 未开(含重拉延迟窗):立即 spawn,并作废挂起的重拉(裁定 2;
	// retryNow 的二次校验兜住迟到的旧到期动作)
	l.pendingRetry = nil
	l.mu.Unlock()
	l.spawnAttempt()
}

// childExited 子进程退出转投口(接线层把 child.Wait() 的退出码转投至此,
// 签名即计划原文契约)。分诊:兼容通道 killedByUs 与 code=0(点面板外/
// 失焦/close 请求/孤儿自检的正常关闭)= 正常关闭(零惩罚;存活 >60s 顺带
// 清零滑窗);code=60 = quit 意图分派;其余 = 崩溃(计 1 次 + 1s 重拉,
// 滑窗满 3 → 兜底停拉)。分类逻辑不变(防外部 SIGKILL 误伤)。
func (l *PanelLauncher) childExited(code int, alive time.Duration, killedByUs bool) {
	var (
		retryDelay  time.Duration
		retry       bool
		fallbackNow bool
		focusClose  bool
		quit        bool
	)
	l.mu.Lock()
	l.child = nil
	l.spawnedAt = time.Time{}
	if l.phase != phaseFallback { // 兜底态:一切退出报告让位(停拉终态)
		switch {
		case killedByUs || code == 0:
			// 正常关闭:点面板外/失焦/close 请求/孤儿自检(「关闭即退」下
			// 退出码 0 的主产出口)——零惩罚零重拉;存活 >60s 的成功会话
			// 清零失败滑窗(≤60s 的正常关闭不动滑窗,拍板 1 只按崩溃计数,
			// 普通关闭不奖不罚)
			if alive > panelLongSessionClear {
				l.crashTimes = nil
			}
			focusClose = !killedByUs && code == 0
		case code == panelExitCodeQuit:
			// quit 退出意图分派:不计数不重拉(退出闸归 Task 15);quit
			// 结束的长会话不清零(清零仅挂正常关闭分支,拍板 1 口径)
			quit = true
		default:
			// 崩溃(含 kill -9 信号死亡 exit −1):记失败 + 重拉/兜底
			retryDelay, retry = l.recordCrashLocked()
			if retry {
				l.pendingRetry = l.retryNow // 挂起重拉真登记(作废=Click 置 nil,裁定 2)
			}
			fallbackNow = !retry // 滑窗满 3:本次转投内已切兜底态
		}
	}
	l.mu.Unlock()

	switch {
	case retry:
		l.scheduleRetryFn(retryDelay, l.retryNow)
	case focusClose:
		firePanelCallback(l.onFocusFallback)
	case quit:
		firePanelCallback(l.onQuitRequested)
	case fallbackNow:
		firePanelCallback(l.onFallback)
	}
}

// childExitedIfCurrent 接线层的身份闸转投口:仅当 child 仍是
// 在册面板(或已无在册面板)时才分诊——close 请求发送失败回退 spawn 后,旧子
// 的崩溃码可能此刻才送达接线层;若放行,旧报告会无条件 child=nil 清掉新子
// 引用(下次点击误判「未开」→ 重复 spawn 双面板),quit 码还会误触发退出
// 闸。l.child==nil(回退后 spawn 也失败)时放行:旧报告是彼时唯一在册
// 面板的退出,照常分诊(崩溃补排重拉)。普通路径(无回退竞态)child 恒
// 匹配,行为与 childExited 全等。
func (l *PanelLauncher) childExitedIfCurrent(child ChildProcess, code int, alive time.Duration, killedByUs bool) {
	l.mu.Lock()
	stale := l.child != nil && l.child != child
	l.mu.Unlock()
	if stale {
		return // 迟到的旧子报告:新子在册,让位
	}
	l.childExited(code, alive, killedByUs)
}

// RetryFromFallback 兜底态恢复入口(「打开面板」菜单项):退出兜底态并
// 立即 spawn 一次。spawn 失败走既有崩溃计数 + 1s 重拉路径,连败再满 3 次
// 重新 onFallback(裁定 1);正常态重复调用幂等无效。
func (l *PanelLauncher) RetryFromFallback() {
	l.mu.Lock()
	if l.phase != phaseFallback {
		// 非兜底态:幂等无效(正常态的打开归 Click)
		l.mu.Unlock()
		return
	}
	l.phase = phaseNormal
	l.crashTimes = nil // 滑窗清零:「连败再满 3 次」自 Retry 独立计数(裁定 1)
	l.pendingRetry = nil
	l.mu.Unlock()
	l.spawnAttempt()
}

// exitReport 供接线层在 child.Wait() 返回后取转投 childExited 所需的存活
// 时长与有意收起标记(兼容通道,恒 false;标记取即清零,防旧标记污染
// 下一轮)。须先于 childExited 调用——childExited 会清 spawnedAt,错序则
// alive 恒 0 静默失效。与 spawnAttempt 写入的 spawnedAt 同锁读取。
func (l *PanelLauncher) exitReport() (alive time.Duration, killedByUs bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.spawnedAt.IsZero() {
		alive = l.nowFn().Sub(l.spawnedAt)
	}
	killedByUs = l.killedByUs
	l.killedByUs = false
	return alive, killedByUs
}

// spawnAttempt 执行一次 spawn:成功记 child 与存活起点;失败(fork/exec 错,
// 含 opts/spawn panic 兜底转崩溃)= 崩溃 1 次,同一滑窗计数 + 1s 重拉,滑窗
// 满 3 直接兜底。已有面板(手动 Click 抢先)或已进兜底态时让位。spawn/opts
// 锁内调用、有意设计(契约见文件头),本状态机对二者 panic 兜底转崩溃计数;
// 其余 on* 回调 panic 契约:状态机不防御,接线层 goroutine(forwardPanelExit)
// 自带 recover,面板回调沿用 panel_bridge.go 的 recover 惯例。
func (l *PanelLauncher) spawnAttempt() {
	retryDelay, retry, spawned := l.spawnAttemptLocked()
	if spawned {
		return // 成功开面板或让位:无锁外动作
	}
	if retry {
		l.scheduleRetryFn(retryDelay, l.retryNow)
	} else {
		firePanelCallback(l.onFallback)
	}
}

// spawnAttemptLocked spawnAttempt 的持锁段:defer Unlock 形态——opts/spawn
// panic 至少释放锁,半更新态 = spawn 未成功语义等价。返回值:spawned=成功
// 开面板或让位(无锁外动作);retry=true 携延迟待锁外排重拉(挂起重拉已
// 真登记,作废=Click 置 nil),false 即兜底待锁外分派 onFallback。
func (l *PanelLauncher) spawnAttemptLocked() (retryDelay time.Duration, retry, spawned bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.phase == phaseFallback || l.child != nil {
		spawned = true // 让位
		return
	}
	if l.spawn == nil {
		// 装配缺陷守卫:未装配 spawn 不崩状态机也不计失败(Task 15 必装)
		spawned = true
		return
	}
	var opts SpawnOpts
	var spawnErr error
	if l.opts != nil {
		opts, spawnErr = func() (o SpawnOpts, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panel launcher opts panic: %v", r)
				}
			}()
			return l.opts(), nil
		}()
	}
	if spawnErr == nil {
		child, err := func() (c ChildProcess, err error) {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panel launcher spawn panic: %v", r)
				}
			}()
			return l.spawn(opts)
		}()
		if err == nil {
			l.child = child
			l.spawnedAt = l.nowFn()
			l.killedByUs = false
			spawned = true
			return
		}
		spawnErr = err
	}
	// 失败(fork/exec 错 / opts·spawn panic 兜底)= 崩溃 1 次,同罪同窗
	retryDelay, retry = l.recordCrashLocked()
	if retry {
		l.pendingRetry = l.retryNow // 挂起重拉真登记(作废=Click 置 nil,裁定 2)
	}
	return
}

// retryNow 挂起重拉的到期动作:先验真登记——被 Click 作废(置 nil)的迟到
// fire 即便此时 phase=normal 且 child==nil 也无人请求,必须让位(闭合「幽灵
// 重拉」窗口);仍在册再看兜底态 / 已有面板二次校验(裁定 2),随后交
// spawnAttempt 三重校验。
func (l *PanelLauncher) retryNow() {
	l.mu.Lock()
	if l.pendingRetry == nil {
		// 已作废(作废=置 nil 是真机制,非死字段):让位
		l.mu.Unlock()
		return
	}
	if l.phase == phaseFallback || l.child != nil {
		l.mu.Unlock()
		return
	}
	l.pendingRetry = nil
	l.mu.Unlock()
	l.spawnAttempt()
}

// recordCrashLocked 记一次崩溃并做滑窗判定(调用方持锁):入窗 → prune 过期
// (10 分钟滑窗)→ 满 3 次切兜底态(作废滑窗与挂起重拉,停拉)返回
// (0,false);否则返回 (1s,true) 待锁外排重拉。
func (l *PanelLauncher) recordCrashLocked() (time.Duration, bool) {
	now := l.nowFn()
	l.crashTimes = append(l.crashTimes, now)
	kept := l.crashTimes[:0]
	for _, t := range l.crashTimes {
		if now.Sub(t) <= panelCrashWindow {
			kept = append(kept, t)
		}
	}
	l.crashTimes = kept
	if len(l.crashTimes) >= panelCrashFallbackLimit {
		l.phase = phaseFallback
		l.crashTimes = nil   // 兜底态滑窗作废,RetryFromFallback 重新起算(裁定 3)
		l.pendingRetry = nil // 停拉:作废一切挂起重拉
		return 0, false
	}
	return panelCrashRetryDelay, true
}

// nowFn 时钟取值:nil 缺省 time.Now。
func (l *PanelLauncher) nowFn() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// scheduleRetryFn 重拉调度:nil 缺省 time.AfterFunc(真实形态);测试注入
// 截获版保证零真实定时器。
func (l *PanelLauncher) scheduleRetryFn(d time.Duration, fn func()) {
	if l.scheduleRetry != nil {
		l.scheduleRetry(d, fn)
		return
	}
	time.AfterFunc(d, fn)
}

// firePanelCallback nil 安全回调触发。
func firePanelCallback(fn func()) {
	if fn != nil {
		fn()
	}
}
