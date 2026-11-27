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

// fakeClock 可推进的假钟:滑窗判定与存活起点全部由它决定,测试零睡眠。
type fakeClock struct {
	now time.Time
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

// fakeChild ChildProcess 测试替身:Kill 只计数;Wait 返回永不触发的通道
// (真实退出由测试直呼 childExited 走状态机,不经通道)。
type fakeChild struct {
	kills int
	wait  chan int
}

func newFakeChild() *fakeChild { return &fakeChild{wait: make(chan int, 1)} }

func (c *fakeChild) Wait() <-chan int { return c.wait }

func (c *fakeChild) Kill() { c.kills++ }

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
	if s.delay != panelCrashRetryDelay {
		t.Fatalf("重拉延迟 = %v, want %v", s.delay, panelCrashRetryDelay)
	}
	s.fire()
}

// TestPanelLauncherExitClassification 退出码分诊表:拍板 1「只兜崩溃」的
// 全部分支——崩溃(code!=0 且非 60 且非有意收起,含 kill -9 的信号死亡
// exit −1)计失败 1 次 + 1s 重拉;code=0(失焦自退)与有意收起 = 正常关闭,
// 零惩罚零重拉;code=60 分派 onQuitRequested。
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
		{name: "失焦自退code=0", code: 0, alive: 1500 * time.Millisecond, wantFocus: true},
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

// TestPanelLauncherNormalOpenClose 正常开合:未开→spawn;已开→Kill(标有意
// 收起);有意收起退出后可再次打开,全程零惩罚。
func TestPanelLauncherNormalOpenClose(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	if h.spawnCalls != 1 {
		t.Fatalf("首次点击未 spawn:spawnCalls=%d", h.spawnCalls)
	}
	if got := h.spawnOpts[0]; got.URL != "http://127.0.0.1:56431/panel.html" || got.PanelW != 340 || got.PanelH != 560 {
		t.Fatalf("spawn 参数透传失真:%+v", got)
	}

	h.launcher.Click() // 再点 = 收起
	if h.lastChild().kills != 1 {
		t.Fatalf("已开状态点击未 Kill:kills=%d", h.lastChild().kills)
	}
	h.launcher.childExited(0, 5*time.Second, true) // 有意收起的退出
	if len(h.scheduled) != 0 || h.fallbacks != 0 || h.focusCloses != 0 {
		t.Fatalf("有意收起被惩罚:scheduled=%d fallback=%d focus=%d", len(h.scheduled), h.fallbacks, h.focusCloses)
	}

	h.launcher.Click() // 收起后可再开
	if h.spawnCalls != 2 {
		t.Fatalf("收起后未重新 spawn:spawnCalls=%d", h.spawnCalls)
	}
}

// TestPanelLauncherDoubleClickToggleNotCounted 快速双击 toggle 不计数:第二次
// 点击落在已开态 → Kill 标有意收起,即便退出呈信号死亡形态(exit −1)也走
// 正常关闭;随后两次真崩溃不触发兜底(若 toggle 被误计,即凑满 3 次失败)。
func TestPanelLauncherDoubleClickToggleNotCounted(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	h.launcher.Click() // 双击第二下 = 收起
	h.launcher.childExited(-1, 120*time.Millisecond, true)

	h.crashExit(1 * time.Second)
	h.crashExit(1 * time.Second)
	if h.fallbacks != 0 {
		t.Fatalf("toggle 被误计失败:fallback=%d", h.fallbacks)
	}
	if len(h.scheduled) != 2 {
		t.Fatalf("崩溃计数失真:scheduled=%d, want 2(仅两次真崩溃)", len(h.scheduled))
	}
}

// TestPanelLauncherFocusAutoExitNotCounted 失焦自退(code=0, alive<2s)不计
// 数不重拉,且分派 onFocusFallback(接线层据此刷新「面板已关」的可见面)。
func TestPanelLauncherFocusAutoExitNotCounted(t *testing.T) {
	h := newLauncherHarness()
	h.launcher.Click()
	h.launcher.childExited(0, 1500*time.Millisecond, false)
	if h.focusCloses != 1 {
		t.Fatalf("失焦自退未分派 onFocusFallback:focus=%d", h.focusCloses)
	}
	h.crashExit(1 * time.Second)
	h.crashExit(1 * time.Second)
	if h.fallbacks != 0 {
		t.Fatalf("失焦自退被误计失败:fallback=%d", h.fallbacks)
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
// 兜底态并立即 spawn;成功后回正常态(点击恢复 toggle 语义)。正常态重复
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
	h.launcher.Click() // 回正常态:点击恢复 toggle 语义
	if h.lastChild().kills != 1 {
		t.Fatalf("恢复后点击未收起:kills=%d", h.lastChild().kills)
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

// TestPanelLauncherSideLiterals Side 字面量与 panelwin 对齐。不 import
// internal/panelwin(其无 Linux 形态,app/tray 引用会破 Linux 依赖闭包闸,
// 见 cmd/panel.go 闭包纪律),故拷贝字面量并以本测试锚定漂移。
func TestPanelLauncherSideLiterals(t *testing.T) {
	if panelSideTop != "top" || panelSideBottom != "bottom" {
		t.Fatalf("Side 字面量漂移:%q / %q", panelSideTop, panelSideBottom)
	}
}
