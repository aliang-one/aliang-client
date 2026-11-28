package panelwin

// 空闲回收与孤儿自检(2026-11-28 用户拍板方案 B「首次后常驻」)。
//
// 方案 B:面板进程首次点击 spawn 后常驻——点击 toggle 可见⇄隐藏、失焦=隐藏
// (不再退出)。隐藏满 10 分钟 → exit(0) 空闲回收(launcher 记正常关闭,
// 零惩罚零重拉);可见期不计时。本文件无构建标签,两平台壳共用:darwin 的
// 可见性事件经 ObjC → 导出回调喂入(panelwin_darwin.go),Windows 在
// wndProc 里直接喂(panelwin_windows.go)——可见性状态两平台各自单一真源
// (darwin=NSWindow isVisible、Windows=wndProc 包级 bool),控制器只管
// 「隐藏多久了」,不持可见性判断。
//
// 退出码契约:exit(ExitFocusLoss)(0)=正常关闭——与 quit(60)/崩溃(其余
// 非零)的三分口径不变,launcher 据此零惩罚回收。
//
// 孤儿自检(darwin 专属;Windows 依赖 Job KILL_ON_JOB_CLOSE 收割,注释见
// panel_spawn_windows.go,无需实现):companion 死后面板被 launchd 收养
// (ppid==1),壳内每 5s 自检 → exit(0)。

import (
	"os"
	"sync"
	"time"

	"aliang.one/nursorgate/common/logger"
)

// panelIdleTTL 空闲回收阈值(拍板原文「隐藏持续 10 分钟」):隐藏态持续
// 超过此时长 → exit(0);显示/再次隐藏重置;可见期不计时。
const panelIdleTTL = 10 * time.Minute

// panelOrphanWatchInterval 孤儿自检周期(方案 B §4:每 5s 查一次 ppid)。
const panelOrphanWatchInterval = 5 * time.Second

// panelIdle 面板空闲回收控制器(进程级单例;面板进程即单窗叶子进程)。
// expire 缺省 exitIdle(os.Exit(0)),测试各自构造实例注入替身,零真实退出。
var panelIdle = newPanelIdleController(panelIdleTTL, exitIdle)

// panelIdleController 隐藏态空闲计时控制器:SetHidden 是唯一入口——hidden
// 真值启动/重置计时(ttl 后触发 expire),假值停止并清计时。计时器仅一枚
// (重复 hidden 幂等重置,不叠加),expire 至多触发一次(触发即进程退出,
// 测试形态下靠 Stop 收尾)。
type panelIdleController struct {
	mu     sync.Mutex
	ttl    time.Duration
	timer  *time.Timer
	expire func()
}

// newPanelIdleController 构造:ttl 到期动作 expire(测试注入;生产 exitIdle)。
func newPanelIdleController(ttl time.Duration, expire func()) *panelIdleController {
	return &panelIdleController{ttl: ttl, expire: expire}
}

// SetHidden 可见性事件入口(两平台壳喂入):hidden=true 启动/重置空闲计时,
// false 停止。状态机:SetHidden(true) 在无计时器时启动、有时即 Reset(=
// 「再次隐藏重置」);SetHidden(false) 停止并清引用(后续 true 重新起算,
// 不吃旧进度)。
func (c *panelIdleController) SetHidden(hidden bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if hidden {
		if c.timer != nil {
			c.timer.Reset(c.ttl) // 再次隐藏:重置(计时器仍活着,Reset 语义确定)
			return
		}
		c.timer = time.AfterFunc(c.ttl, c.expire)
		return
	}
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}

// Stop 停止计时(测试收尾防泄漏;进程形态下退出即回收,无需调用)。
func (c *panelIdleController) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
		c.timer = nil
	}
}

// exitIdle 空闲回收退出:exit(0)=正常关闭契约(idle 回收,launcher 记
// 正常关闭零惩罚;崩溃分类/兜底语义不变)。进程是叶子进程,直接终止。
func exitIdle() {
	logger.Info("panelwin: 隐藏空闲回收,正常退出(方案 B,退出码 0)")
	os.Exit(ExitFocusLoss)
}

// panelOrphanWatchdog 孤儿自检(方案 B §4,macOS;调用方 go 起跑):每
// interval 查一次 getppid,==1 即 companion 死后面板被 launchd 收养 →
// exit(0)(正常关闭,launcher 零惩罚回收)。getppid/exit 注入(测试替身);
// stop 非 nil 时关闭即停(测试收尾;生产传 nil,goroutine 与进程同寿)。
// Windows 不跑本看门狗:Job KILL_ON_JOB_CLOSE 由内核收割(companion 进程
// 死亡 → 句柄回收 → Job 连带终止面板),见 panel_spawn_windows.go。
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
