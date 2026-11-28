package tray

import (
	"errors"
	"testing"
	"time"
)

// 面板 launcher 状态机测试(M1 计划 Task 14):纯逻辑,零真实定时器、零真实
// 进程——1s 重拉延迟经 scheduleRetry 注入钩子截获(测试手工触发到期动作);
// 滑窗判定经注入 now 假钟推进;子进程退出由测试直呼 childExited 模拟接线层
// 转投(child.Wait() 退出码 → childExited(code, alive, killedByUs)),不起
// goroutine、不碰 AppKit/WebView2。全部替身单线程使用(零真实定时器保证
// 无并发回调),不加锁。
//
// 点击语义(2026-11-28 撤销方案 B,回归「点击才拉起+关闭即退」):无子 →
// spawn;有子 → 发 close 请求(SIGUSR1/WM_APP+1,子进程 exit(0) 正常关闭,
// 窗口随进程退出消失)。close 请求本身零退出事件,子进程清理由转投链的
// childExited(code=0 正常关闭)承担。

// fakeClock 可推进的假钟:滑窗判定与存活起点全部由它决定,测试零睡眠。
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// fakeChild ChildProcess 测试替身:Kill 只计数;Wait 返回永不触发的通道
// (真实退出由测试直呼 childExited 走状态机,不经通道);Pid 返回自增流水号
// (close 请求投递地址断言用,每次 newFakeChild 唯一)。
type fakeChild struct {
	pid   int
	kills int
	wait  chan int
}

var fakeChildSeq int

func newFakeChild() *fakeChild {
	fakeChildSeq++
	return &fakeChild{pid: 1000 + fakeChildSeq, wait: make(chan int, 1)}
}

func (c *fakeChild) Wait() <-chan int { return c.wait }

func (c *fakeChild) Kill() { c.kills++ }

func (c *fakeChild) Pid() int { return c.pid }

// closeExit 模拟「close 请求后子进程 exit(0)」的正常关闭(接线层契约:
// close 请求(SendClose)送达 → 子进程 exit(0) → child.Wait() 送达 0 →
// childExited(code=0, 非 killedByUs))。
func (h *launcherHarness) closeExit(alive time.Duration) {
	h.launcher.childExited(0, alive, false)
}

// scheduledRetry 截获的一条重拉调度(delay 到期动作)。
type scheduledRetry struct {
	delay time.Duration
	fire  func()
}

// launcherHarness 聚合全部注入口与探针;断言走字段直读。
type launcherHarness struct {
	clock    *fakeClock
	launcher *PanelLauncher

	spawnCalls  int
	spawnOpts   []SpawnOpts
	liveChild   *fakeChild // 最近一次成功 spawn 的 child
	spawnErr    error      // 非 nil = 下一次 spawn 返回该错误
	closePids   []int      // close 请求发送记录(成功与失败都记账)
	closeErr    error      // 非 nil = 下一次 close 请求返回该错误(子进程已死形态)
	fallbacks   int
	quits       int
	focusCloses int
	scheduled   []scheduledRetry
}

func newLauncherHarness() *launcherHarness {
	h := &launcherHarness{clock: &fakeClock{now: time.Unix(1790000000, 0)}}
	h.launcher = &PanelLauncher{
		spawn: func(opts SpawnOpts) (ChildProcess, error) {
			h.spawnCalls++
			h.spawnOpts = append(h.spawnOpts, opts)
			if h.spawnErr != nil {
				return nil, h.spawnErr
			}
			h.liveChild = newFakeChild()
			return h.liveChild, nil
		},
		requestClose: func(pid int) error {
			h.closePids = append(h.closePids, pid)
			if h.closeErr != nil {
				return h.closeErr
			}
			return nil
		},
		opts: func() SpawnOpts {
			return SpawnOpts{URL: "http://127.0.0.1:56431/panel.html", PanelW: 340, PanelH: 560}
		},
		now: h.clock.Now,
		scheduleRetry: func(d time.Duration, fn func()) {
			h.scheduled = append(h.scheduled, scheduledRetry{delay: d, fire: fn})
		},
		onFallback:      func() { h.fallbacks++ },
		onFocusFallback: func() { h.focusCloses++ },
		onQuitRequested: func() { h.quits++ },
	}
	return h
}

// lastChild 返回最近一次成功 spawn 的 child(spawn 失败时为 nil)。
func (h *launcherHarness) lastChild() *fakeChild { return h.liveChild }

// crashExit 模拟当前面板崩溃一次(接线层契约:child.Wait() 退出码非 0 且
// 非 60、非有意收起 → childExited)。
func (h *launcherHarness) crashExit(alive time.Duration) {
	h.launcher.childExited(1, alive, false)
}

// firePendingRetry 触发最后一条截获的重拉到期动作(同步版调度器,零真实
// 定时器),并校验延迟恰为 1s。
func (h *launcherHarness) firePendingRetry(t *testing.T) {
	t.Helper()
	if len(h.scheduled) == 0 {
		t.Fatalf("无挂起重拉可触发")
	}
	s := h.scheduled[len(h.scheduled)-1]
	if s.delay != time.Second {
		t.Fatalf("重拉延迟 = %v, want %v", s.delay, time.Second)
	}
	s.fire()
}

// TestPanelLauncherExitClassification 退出码分诊表:拍板 1「只兜崩溃」的
// 全部分支——崩溃(code≠0 且非 60,含 kill -9 的信号死亡 exit −1)计失败
// 1 次 + 1s 重拉;code=0(点面板外/失焦/close 请求/孤儿自检的正常关闭)与
// 有意收起(兼容通道)= 零惩罚零重拉;code=60 分派 onQuitRequested。
func TestPanelLauncherExitClassification(t *testing.T) {
	tests := []struct {
		name       string
		code       int
		alive      time.Duration
		killedByUs bool
		wantFocus  bool
		wantQuit   bool
		wantCrash  bool // 是否计一次失败并排 1s 重拉
	}{
		{name: "点图标有意收起(信号态死亡也零惩罚)", code: -1, alive: 300 * time.Millisecond, killedByUs: true},
		{name: "正常关闭code=0(点外/失焦/close请求/孤儿自检)", code: 0, alive: 1500 * time.Millisecond, wantFocus: true},
		{name: "quit退出意图code=60", code: 60, alive: 30 * time.Second, wantQuit: true},
		{name: "崩溃code=1", code: 1, alive: 800 * time.Millisecond, wantCrash: true},
		{name: "kill-9信号死亡exit=-1", code: -1, alive: 200 * time.Millisecond, wantCrash: true},
		{name: "code=0但有意收起(收起优先不报失焦)", code: 0, alive: 2 * time.Second, killedByUs: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newLauncherHarness()
			h.launcher.Click() // 开面板
			h.launcher.childExited(tc.code, tc.alive, tc.killedByUs)

			if tc.wantCrash {
				if len(h.scheduled) != 1 {
					t.Fatalf("崩溃未排 1s 重拉:scheduled=%d", len(h.scheduled))
				}
				if h.fallbacks != 0 {
					t.Fatalf("单次崩溃不应兜底:fallback=%d", h.fallbacks)
				}
			} else {
				if len(h.scheduled) != 0 {
					t.Fatalf("非崩溃分支排了重拉:scheduled=%d", len(h.scheduled))
				}
			}
			if got := h.focusCloses > 0; got != tc.wantFocus {
				t.Fatalf("onFocusFallback = %v, want %v", got, tc.wantFocus)
			}
			if got := h.quits > 0; got != tc.wantQuit {
				t.Fatalf("onQuitRequested = %v, want %v", got, tc.wantQuit)
			}
			if h.fallbacks != 0 {
				t.Fatalf("单次退出不应兜底:fallback=%d", h.fallbacks)
			}
		})
	}
}

// TestPanelLauncherClickOpenThenCloseRequest 点击语义(2026-11-28 撤销方案 B,
// 回归「点击才拉起+关闭即退」):未开 → spawn;有子 → 发 close 请求
// (SIGUSR1/WM_APP+1,子进程 exit(0) 正常关闭,窗口随进程退出消失)。
// close 请求成功不产生退出事件、不清子进程——清理由转投链的
// childExited(code=0) 承担;请求送达前子进程保持在册,期间重复点击幂等
// (再发一次请求,不 spawn 不 Kill)。
func TestPanelLauncherClickOpenThenCloseRequest(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	if h.spawnCalls != 1 {
		t.Fatalf("首次点击未 spawn:spawnCalls=%d", h.spawnCalls)
	}
	if got := h.spawnOpts[0]; got.URL != "http://127.0.0.1:56431/panel.html" || got.PanelW != 340 || got.PanelH != 560 {
		t.Fatalf("spawn 参数透传失真:%+v", got)
	}
	child := h.lastChild()

	h.launcher.Click() // 再点 = close 请求(有子在册=请求关闭)
	if len(h.closePids) != 1 || h.closePids[0] != child.pid {
		t.Fatalf("已开状态点击未发 close 请求:pids=%v want [%d]", h.closePids, child.pid)
	}
	if child.kills != 0 {
		t.Fatalf("已开状态点击 Kill 了子进程(关闭只走 close 请求):kills=%d", child.kills)
	}
	if len(h.scheduled) != 0 || h.fallbacks != 0 || h.quits != 0 {
		t.Fatalf("close 请求发送产生了惩罚/退出事件:scheduled=%d fallback=%d quit=%d",
			len(h.scheduled), h.fallbacks, h.quits)
	}

	// 请求已发、子进程尚未退出(close 请求期间):重复点击幂等——再发一次
	// 请求,不重复 spawn 不误清在册子进程。
	h.launcher.Click()
	if len(h.closePids) != 2 || h.closePids[1] != child.pid {
		t.Fatalf("close 请求期间重复点击未幂等重发:pids=%v", h.closePids)
	}
	if h.spawnCalls != 1 {
		t.Fatalf("close 请求期间点击误 spawn(重复面板):spawnCalls=%d", h.spawnCalls)
	}

	// 子进程收到请求 exit(0):正常关闭零惩罚,子进程回收,面板回到「无子」。
	h.closeExit(800 * time.Millisecond)
	if len(h.scheduled) != 0 || h.fallbacks != 0 || h.focusCloses != 1 {
		t.Fatalf("close 请求的 exit(0) 被误计:focus=%d scheduled=%d fallback=%d",
			h.focusCloses, len(h.scheduled), h.fallbacks)
	}

	h.launcher.Click() // 无子:重新 spawn(生命周期闭环)
	if h.spawnCalls != 2 {
		t.Fatalf("正常关闭后未重新 spawn:spawnCalls=%d", h.spawnCalls)
	}
}

// TestPanelLauncherCloseRequestFailureFallsBackToSpawn close 请求发送失败
// (子进程已死——「崩溃已死未转投」探测之后死亡的窗口)→ 按无子处理:清
// 跟踪状态 + 立即 spawn,绝不 Kill(关闭只走 close 请求,主流程零 Kill)。
// 回退后新子取得独立 pid,后续点击 close 请求对准新子。
func TestPanelLauncherCloseRequestFailureFallsBackToSpawn(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	first := h.lastChild()

	h.closeErr = errors.New("os: process already finished")
	h.launcher.Click() // close 请求发送失败:回退 spawn
	if h.spawnCalls != 2 {
		t.Fatalf("close 请求失败未回退 spawn:spawnCalls=%d", h.spawnCalls)
	}
	if first.kills != 0 {
		t.Fatalf("close 请求失败路径 Kill 了死子进程:kills=%d", first.kills)
	}
	if len(h.scheduled) != 0 || h.fallbacks != 0 {
		t.Fatalf("close 请求失败回退被惩罚:scheduled=%d fallback=%d", len(h.scheduled), h.fallbacks)
	}

	h.closeErr = nil
	h.launcher.Click() // 新子:close 请求用新 pid
	if len(h.closePids) != 2 || h.closePids[1] != h.lastChild().pid {
		t.Fatalf("回退后 close 请求未对准新子:pids=%v want 尾=%d", h.closePids, h.lastChild().pid)
	}
}

// TestPanelLauncherStaleReportAfterCloseFailureYields 迟到的旧子报告让位:
// close 请求失败回退 spawn 后新子已在册,旧子崩溃码此刻才经接线层送达——
// childExitedIfCurrent 的身份闸必须让位:不清掉新子引用(否则下次点击误判
// 「未开」重复 spawn 出双面板)、不把旧崩溃计入新会话(排重拉会被新子在册
// 二次校验挡住,但 quit 码会误触发退出闸)。
func TestPanelLauncherStaleReportAfterCloseFailureYields(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	old := h.lastChild()

	h.closeErr = errors.New("os: process already finished")
	h.launcher.Click() // 回退 spawn,新子在册
	if h.spawnCalls != 2 {
		t.Fatalf("前置回退未 spawn:spawnCalls=%d", h.spawnCalls)
	}

	old.wait <- 1 // 旧子崩溃码迟到送达(真实形态:转投 goroutine 一直阻塞在此)
	h.launcher.childExitedIfCurrent(old, 1, time.Second, false)

	if h.lastChild() == nil {
		t.Fatal("迟到的旧子报告清掉了新子引用(下次点击会重复 spawn 双面板)")
	}
	if len(h.scheduled) != 0 {
		t.Fatalf("迟到的旧子报告被计入新会话:scheduled=%d", len(h.scheduled))
	}

	h.closeErr = nil
	h.launcher.Click() // 新子仍在册:继续 close 请求,不重复 spawn
	if h.spawnCalls != 2 {
		t.Fatalf("身份闸失效导致重复 spawn:spawnCalls=%d", h.spawnCalls)
	}
}

// TestPanelLauncherCloseRequestExitZeroZeroPenalty close 请求 → 子进程
// exit(0) 零惩罚(分类关键路径):companion 发 close 后子进程 exit(0)=
// 正常关闭(launcher 记 onFocusFallback,零惩罚零重拉),子进程回收,
// 下次 Click 重新 spawn。准绳「重复点击图标不会把正常关闭误计为崩溃」的
// 直测面(变异验证:把 code=0 误计为崩溃,本用例即红)。
func TestPanelLauncherCloseRequestExitZeroZeroPenalty(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	child := h.lastChild()
	h.launcher.Click() // close 请求(有子在册)
	if len(h.closePids) != 1 || h.closePids[0] != child.pid {
		t.Fatalf("前置 close 请求未发:pids=%v", h.closePids)
	}

	h.launcher.childExited(0, 500*time.Millisecond, false) // 子进程 exit(0)
	if len(h.scheduled) != 0 || h.fallbacks != 0 {
		t.Fatalf("close 请求的 exit(0) 被计崩溃惩罚:scheduled=%d fallback=%d", len(h.scheduled), h.fallbacks)
	}
	if h.focusCloses != 1 {
		t.Fatalf("close 请求的 exit(0) 未按正常关闭分派 onFocusFallback:focus=%d", h.focusCloses)
	}
	if child.kills != 0 {
		t.Fatalf("close 请求路径 Kill 了子进程:kills=%d", child.kills)
	}

	h.launcher.Click() // 回到无子态:重新 spawn
	if h.spawnCalls != 2 {
		t.Fatalf("正常关闭后未重新 spawn:spawnCalls=%d", h.spawnCalls)
	}
}

// TestPanelLauncherRequestCloseNilYields requestClose 未装配的缺陷守卫:让位
// 不发请求不 spawn 不崩(与 spawnAttemptLocked 的 nil-spawn 守卫同风格);
// 真实装配必装(newPanelLauncher 注入 panelToggleSender)。
func TestPanelLauncherRequestCloseNilYields(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.requestClose = nil
	h.launcher.Click()
	if h.spawnCalls != 1 {
		t.Fatalf("首次点击未 spawn:spawnCalls=%d", h.spawnCalls)
	}
	h.launcher.Click() // requestClose 未装配:让位
	if h.spawnCalls != 1 || len(h.closePids) != 0 || h.lastChild().kills != 0 {
		t.Fatalf("nil requestClose 未让位:spawn=%d closes=%d kills=%d",
			h.spawnCalls, len(h.closePids), h.lastChild().kills)
	}
}

// TestPanelLauncherDoubleClickCloseRequestNotCounted 快速双击不计数:第二次
// 点击落在已开态 → close 请求成功零惩罚(close 请求期间幂等,重复请求不
// 计失败);随后两次真崩溃不触发兜底(close 与回退均不计失败,若误计即凑满
// 3 次兜底)。
func TestPanelLauncherDoubleClickCloseRequestNotCounted(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	h.launcher.Click() // 双击第二下 = close 请求
	if len(h.scheduled) != 0 {
		t.Fatalf("close 请求成功被误计:scheduled=%d", len(h.scheduled))
	}

	h.crashExit(1 * time.Second)
	h.crashExit(1 * time.Second)
	if h.fallbacks != 0 {
		t.Fatalf("close 请求被误计失败:fallback=%d", h.fallbacks)
	}
	if len(h.scheduled) != 2 {
		t.Fatalf("崩溃计数失真:scheduled=%d, want 2(仅两次真崩溃)", len(h.scheduled))
	}
}

// TestPanelLauncherFocusAutoExitNotCounted 失焦即退(code=0)不计
// 数不重拉,且分派 onFocusFallback(接线层据此刷新「面板已关」的可见面)。
func TestPanelLauncherFocusAutoExitNotCounted(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	h.launcher.childExited(0, 1500*time.Millisecond, false)
	if h.focusCloses != 1 {
		t.Fatalf("失焦即退未分派 onFocusFallback:focus=%d", h.focusCloses)
	}
	h.crashExit(1 * time.Second)
	h.crashExit(1 * time.Second)
	if h.fallbacks != 0 {
		t.Fatalf("失焦即退被误计失败:fallback=%d", h.fallbacks)
	}
}

// TestPanelLauncherThreeCrashesEnterFallback 连崩 3 次(10 分钟滑窗)→ 兜底
// 一次 + 停拉;兜底后 Click 无效(不发 spawn 不报错),迟到的到期重拉让位。
func TestPanelLauncherThreeCrashesEnterFallback(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	h.crashExit(100 * time.Millisecond) // 崩 1:排重拉
	h.crashExit(100 * time.Millisecond) // 崩 2:再排
	if h.fallbacks != 0 {
		t.Fatalf("第 2 次崩溃不应兜底:fallback=%d", h.fallbacks)
	}
	h.crashExit(100 * time.Millisecond) // 崩 3:兜底 + 停拉
	if h.fallbacks != 1 {
		t.Fatalf("连崩 3 次未兜底:fallback=%d", h.fallbacks)
	}
	if len(h.scheduled) != 2 {
		t.Fatalf("兜底后不应再排重拉:scheduled=%d, want 2(前两次崩溃)", len(h.scheduled))
	}

	h.launcher.Click() // 兜底态点击无效
	if h.spawnCalls != 1 {
		t.Fatalf("兜底态 Click 仍然 spawn:spawnCalls=%d", h.spawnCalls)
	}
	h.firePendingRetry(t) // 迟到的到期动作:兜底态让位,不 spawn
	if h.spawnCalls != 1 {
		t.Fatalf("兜底态到期重拉仍 spawn:spawnCalls=%d", h.spawnCalls)
	}
}

// TestPanelLauncherFallbackRetryRecovers 兜底后仅 RetryFromFallback 可退出
// 兜底态并立即 spawn;成功后回正常态(点击恢复 close 请求语义)。正常态重复
// Retry 幂等无效。
func TestPanelLauncherFallbackRetryRecovers(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	h.crashExit(1 * time.Second)
	h.crashExit(1 * time.Second)
	h.crashExit(1 * time.Second) // → 兜底
	if h.fallbacks != 1 {
		t.Fatalf("前置连崩未兜底:fallback=%d", h.fallbacks)
	}

	h.launcher.RetryFromFallback()
	if h.spawnCalls != 2 {
		t.Fatalf("Retry 未立即 spawn:spawnCalls=%d", h.spawnCalls)
	}
	h.launcher.RetryFromFallback() // 正常态重复调用:幂等无效
	if h.spawnCalls != 2 {
		t.Fatalf("正常态 Retry 误 spawn:spawnCalls=%d", h.spawnCalls)
	}
	h.launcher.Click() // 回正常态:已开点击 = close 请求(关闭即退)
	if len(h.closePids) == 0 || h.closePids[len(h.closePids)-1] != h.lastChild().pid {
		t.Fatalf("恢复后点击未发 close 请求:pids=%v", h.closePids)
	}
	if h.lastChild().kills != 0 {
		t.Fatalf("恢复后点击 Kill 了子进程(关闭只走 close 请求):kills=%d", h.lastChild().kills)
	}
}

// TestPanelLauncherQuitCodeDispatched code=60 → 分派 onQuitRequested,不重拉
// 不计数不兜底(Task 15 接线对到既有 quit 闸)。
func TestPanelLauncherQuitCodeDispatched(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	h.launcher.childExited(60, 42*time.Second, false)
	if h.quits != 1 {
		t.Fatalf("code=60 未分派 onQuitRequested:quit=%d", h.quits)
	}
	if len(h.scheduled) != 0 || h.fallbacks != 0 || h.focusCloses != 0 {
		t.Fatalf("quit 分支误触其他行为:scheduled=%d fallback=%d focus=%d", len(h.scheduled), h.fallbacks, h.focusCloses)
	}
}

// TestPanelLauncherLongSessionClearsWindow 存活 >60s 的成功会话清零失败滑窗:
// 2 崩 → 61s 正常会话 → 滑窗归零,再 2 崩不兜底(未清零则早已凑满 3 次),
// 第 3 崩才兜底。
func TestPanelLauncherLongSessionClearsWindow(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	h.crashExit(1 * time.Second)
	h.crashExit(1 * time.Second) // 滑窗 2/3
	h.firePendingRetry(t)        // 1s 重拉到期:重新打开
	if h.spawnCalls != 2 {
		t.Fatalf("重拉到期未重开面板:spawnCalls=%d", h.spawnCalls)
	}
	h.launcher.childExited(0, 61*time.Second, false) // 长会话正常关闭 → 清零
	if h.focusCloses != 1 {
		t.Fatalf("长会话正常关闭未分派 onFocusFallback:focus=%d", h.focusCloses)
	}

	h.crashExit(1 * time.Second)
	h.crashExit(1 * time.Second) // 清零后再 2 崩:不兜底
	if h.fallbacks != 0 {
		t.Fatalf(">60s 成功会话未清零滑窗:fallback=%d", h.fallbacks)
	}
	h.crashExit(1 * time.Second) // 清零后口径第 3 崩 → 兜底
	if h.fallbacks != 1 {
		t.Fatalf("清零后连崩 3 次未兜底:fallback=%d", h.fallbacks)
	}
}

// TestPanelLauncherSlidingWindowPrunesOldCrashes 10 分钟滑窗:窗口外崩溃
// 过期出窗,不与窗口内计数合并兜底。
func TestPanelLauncherSlidingWindowPrunesOldCrashes(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	h.crashExit(1 * time.Second)
	h.crashExit(1 * time.Second) // 滑窗 2/3(t=0)
	h.clock.Advance(11 * time.Minute)
	h.crashExit(1 * time.Second) // 旧两崩已出窗:仅 1/3 → 不兜底
	if h.fallbacks != 0 {
		t.Fatalf("过期崩溃未出窗:fallback=%d", h.fallbacks)
	}
	if len(h.scheduled) != 3 {
		t.Fatalf("每次崩溃都应排重拉:scheduled=%d, want 3", len(h.scheduled))
	}
}

// TestPanelLauncherSpawnFailureCountsAndRetries spawn 失败(fork/exec 错)=
// 崩溃 1 次 + 1s 重拉(同一滑窗);连败 3 次 → 兜底;修复后 Retry 成功回正常。
func TestPanelLauncherSpawnFailureCountsAndRetries(t *testing.T) {
	h := newLauncherHarness()
	h.spawnErr = errors.New("fork/exec: no such file or directory")
	h.launcher.Click()
	if h.spawnCalls != 1 || len(h.scheduled) != 1 {
		t.Fatalf("spawn 失败未计崩溃+排重拉:calls=%d scheduled=%d", h.spawnCalls, len(h.scheduled))
	}
	if h.lastChild() != nil {
		t.Fatalf("spawn 失败不应有 child")
	}

	h.firePendingRetry(t) // 重拉仍失败:崩 2
	h.firePendingRetry(t) // 再失败:崩 3 → 兜底
	if h.fallbacks != 1 {
		t.Fatalf("spawn 连败 3 次未兜底:fallback=%d", h.fallbacks)
	}
	if h.spawnCalls != 3 {
		t.Fatalf("spawn 次数失真:calls=%d, want 3", h.spawnCalls)
	}

	h.firePendingRetry(t) // 兜底态迟到到期动作:让位
	if h.spawnCalls != 3 {
		t.Fatalf("兜底态到期重拉仍 spawn:calls=%d", h.spawnCalls)
	}

	h.spawnErr = nil
	h.launcher.RetryFromFallback() // 修复后 Retry 成功回正常
	if h.spawnCalls != 4 {
		t.Fatalf("Retry 未 spawn:calls=%d", h.spawnCalls)
	}
	if h.fallbacks != 1 {
		t.Fatalf("Retry 成功不应再兜底:fallback=%d", h.fallbacks)
	}
}

// TestPanelLauncherRetryFromFallbackRefailRefallbacks 兜底中 Retry 再失败:
// 走既有崩溃计数 + 1s 重拉路径,连败 3 次(自 Retry 起独立计数)→ 重新兜底。
func TestPanelLauncherRetryFromFallbackRefailRefallbacks(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	h.crashExit(1 * time.Second)
	h.crashExit(1 * time.Second)
	h.crashExit(1 * time.Second) // 兜底 1

	h.spawnErr = errors.New("fork/exec: permission denied")
	h.launcher.RetryFromFallback() // Retry spawn 失败 = 崩 1(滑窗已清零重计)
	if h.spawnCalls != 2 || len(h.scheduled) != 3 {
		t.Fatalf("Retry 失败未走崩溃重拉路径:calls=%d scheduled=%d", h.spawnCalls, len(h.scheduled))
	}
	h.firePendingRetry(t) // 崩 2
	h.firePendingRetry(t) // 崩 3 → 重新兜底
	if h.fallbacks != 2 {
		t.Fatalf("Retry 连败 3 次未重新兜底:fallback=%d", h.fallbacks)
	}
}

// TestPanelLauncherClickDuringPendingRetrySupersedes 重拉延迟窗内视为「未开」:
// Click 允许立即 spawn 并作废挂起重拉;迟到的到期动作让位(不二次 spawn)。
func TestPanelLauncherClickDuringPendingRetrySupersedes(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	h.crashExit(200 * time.Millisecond) // 崩 → 排 1s 重拉
	if len(h.scheduled) != 1 {
		t.Fatalf("前置崩溃未排重拉")
	}
	h.launcher.Click() // 重拉窗内点击:立即 spawn
	if h.spawnCalls != 2 {
		t.Fatalf("重拉窗内 Click 未立即 spawn:calls=%d", h.spawnCalls)
	}
	h.scheduled[0].fire() // 迟到的到期动作:已有面板,让位
	if h.spawnCalls != 2 {
		t.Fatalf("已作废的重拉仍二次 spawn:calls=%d", h.spawnCalls)
	}
}

// TestPanelLauncherStaleRetryAfterClickOpenCloseYields 幽灵重拉窗口闭合:崩溃
// 排 1s 重拉 → Click 抢先开面板(作废挂起重拉)→ 面板 1s 内又被正常关闭 →
// 迟到的旧到期动作让位。此序列里 phase=normal 且 child==nil,既有的二次校验
// 挡不住——必须靠「作废=置 pendingRetry nil」的真登记机制,否则无人请求也
// 会幽灵 spawn。
func TestPanelLauncherStaleRetryAfterClickOpenCloseYields(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	h.crashExit(200 * time.Millisecond) // 崩 → 排 1s 重拉
	if len(h.scheduled) != 1 {
		t.Fatalf("前置崩溃未排重拉")
	}
	h.launcher.Click() // 重拉窗内点击:立即 spawn 并作废挂起重拉
	if h.spawnCalls != 2 {
		t.Fatalf("重拉窗内 Click 未立即 spawn:calls=%d", h.spawnCalls)
	}
	h.launcher.childExited(0, 300*time.Millisecond, false) // 面板 1s 内失焦即退关闭
	h.scheduled[0].fire()                                  // 迟到的旧到期动作:已作废,让位
	if h.spawnCalls != 2 {
		t.Fatalf("已作废的旧重拉到期仍幽灵 spawn:calls=%d", h.spawnCalls)
	}
	if h.focusCloses != 1 {
		t.Fatalf("前置失焦即退未分派 onFocusFallback:focus=%d", h.focusCloses)
	}
	if len(h.scheduled) != 1 {
		t.Fatalf("让位不应新增重拉调度:scheduled=%d", len(h.scheduled))
	}
}

// TestPanelLauncherSpawnPanicBecomesCrash spawn panic 兜底:转崩溃计数 + 1s
// 重拉,状态机不挂死也不外漏 panic(锁随 defer 释放);重拉到期 spawn 恢复
// 后正常开面板,后续 Click 仍可收起(证明状态机存活)。
func TestPanelLauncherSpawnPanicBecomesCrash(t *testing.T) {
	h := newLauncherHarness()
	boom := errors.New("boom")
	h.launcher.spawn = func(opts SpawnOpts) (ChildProcess, error) {
		h.spawnCalls++
		if h.spawnCalls == 1 {
			panic(boom)
		}
		h.liveChild = newFakeChild()
		return h.liveChild, nil
	}
	h.launcher.Click() // 首次 spawn panic:兜底转崩溃计数
	if len(h.scheduled) != 1 {
		t.Fatalf("spawn panic 未转崩溃排重拉:scheduled=%d", len(h.scheduled))
	}
	if h.fallbacks != 0 {
		t.Fatalf("单次 panic 不应兜底:fallback=%d", h.fallbacks)
	}
	if h.lastChild() != nil {
		t.Fatalf("panic 后不应有 child")
	}
	h.firePendingRetry(t) // 1s 重拉到期:spawn 已恢复 → 正常开面板
	if h.spawnCalls != 2 || h.lastChild() == nil {
		t.Fatalf("panic 后重拉未恢复开面板:calls=%d", h.spawnCalls)
	}
	h.launcher.Click() // 状态机未挂死:再点 = close 请求(关闭即退)
	if len(h.closePids) == 0 || h.closePids[len(h.closePids)-1] != h.lastChild().pid {
		t.Fatalf("panic 后状态机挂死(未发 close 请求):pids=%v", h.closePids)
	}
}

// TestPanelLauncherSpawnOptsFreshEachAttempt 每次 spawn 现取启动参数(Task 15
// 装配:锚定坐标现取现用,禁止启动早期缓存)。
func TestPanelLauncherSpawnOptsFreshEachAttempt(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.opts = func() SpawnOpts {
		return SpawnOpts{
			URL:     "http://127.0.0.1:56431/panel.html",
			Side:    panelSideBottom,
			AnchorX: h.spawnCalls * 10,
		}
	}
	h.launcher.Click()
	h.launcher.childExited(0, 2*time.Second, true) // 正常关闭(有意收起)
	h.launcher.Click()
	if h.spawnCalls != 2 || h.spawnOpts[0].AnchorX == h.spawnOpts[1].AnchorX {
		t.Fatalf("两次 spawn 参数未现取:%+v / %+v", h.spawnOpts[0], h.spawnOpts[1])
	}
	if h.spawnOpts[1].Side != panelSideBottom {
		t.Fatalf("Side 未透传:%+v", h.spawnOpts[1])
	}
}

// TestPanelLauncherClickOnCrashedButUnforwardedChildSpawns 崩溃已死未转投
// 窗口内点击:面板 T 时刻崩溃死亡(退出码已送达 Wait 通道),转投 goroutine
// 尚未跑到 childExited——此窗内 child 仍非 nil,Click 是「开」不是「收起」:
// 不得打 killedByUs(Kill 对死进程 no-op,迟到的崩溃码会被正常关闭分支吞掉
// = 崩溃漏计 + 点击语义反转)。退出码被 Click 抢先消费后按送达一次契约转投
// 方永不送达,收尸(exitReport+childExited)由 Click 代行:崩溃照计 + 立即
// spawn。反向锚定:若此窗点击走「收起」,spawn 不发生且崩溃零计数。
func TestPanelLauncherClickOnCrashedButUnforwardedChildSpawns(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	child := h.lastChild()

	// 崩溃已落地(退出码已进通道)、转投 goroutine 未执行——测试直发通道
	// 模拟(不起 goroutine;真实形态转投方此刻正阻塞在 <-child.Wait())。
	child.wait <- 1

	h.launcher.Click() // 崩溃未转投窗口内的点击 = 开
	if h.spawnCalls != 2 {
		t.Fatalf("崩溃窗口内点击未立即 spawn:calls=%d", h.spawnCalls)
	}
	if len(h.scheduled) != 1 {
		t.Fatalf("崩溃码被吞(killedByUs 误吞或漏计数):scheduled=%d, want 1", len(h.scheduled))
	}
	if h.fallbacks != 0 || h.focusCloses != 0 || h.quits != 0 {
		t.Fatalf("崩溃被误分诊:fallback=%d focus=%d quit=%d", h.fallbacks, h.focusCloses, h.quits)
	}
	if _, killedByUs := h.launcher.exitReport(); killedByUs {
		t.Fatalf("崩溃窗口收尸后 killedByUs 残留(会污染下一轮分诊)")
	}

	// 代行分诊排出的 1s 重拉已被本次「开」作废(立即 spawn 取代):迟到的
	// 到期动作让位,不二次 spawn。
	h.firePendingRetry(t)
	if h.spawnCalls != 2 {
		t.Fatalf("已作废的重拉到期仍二次 spawn:calls=%d", h.spawnCalls)
	}
}

// TestPanelLauncherSideLiterals Side 字面量与 panelwin 对齐。不 import
// internal/panelwin(其无 Linux 形态,app/tray 引用会破 Linux 依赖闭包闸,
// 见 cmd/panel.go 闭包纪律),故拷贝字面量并以本测试锚定漂移。
func TestPanelLauncherSideLiterals(t *testing.T) {
	if panelSideTop != "top" || panelSideBottom != "bottom" {
		t.Fatalf("Side 字面量漂移:%q / %q", panelSideTop, panelSideBottom)
	}
}
