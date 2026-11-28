//go:build windows

package tray

import (
	"fmt"
	"os/exec"
	"syscall"
	"unsafe"

	"aliang.one/nursorgate/common/logger"
	"github.com/getlantern/systray"
	"golang.org/x/sys/windows"
)

// spawnPanelProcess Windows 面板 spawn shim(计划 Task 15 三件套):面板 =
// companion 自身可执行文件的 panel 子命令;纯 Go 无 cgo(x/sys/windows)。
//
// CREATE_SUSPENDED → AllowSetForegroundWindow(pid) → Job 入树 → Resume:
//   - CREATE_SUSPENDED:挂起创建,前台资格在面板第一条指令跑起来之前授予
//     (壳内 SetForegroundWindow 只认「已授予 AllowSetForegroundWindow 资格」
//     的调用,时序差会让失焦即退整条链失效——panelwin_windows.go 文件头);
//   - AllowSetForegroundWindow(pid):授予面板进程前台资格。best-effort,
//     失败落 Warn(失败 = 失焦即退链失效:面板弹出不激活、点外部不退,
//     真机归因靠此日志;companion 在托盘点击瞬间未必持前台权,资格授予
//     可能被拒;实测行为归 Task 16 实机探针);
//   - Job(KILL_ON_JOB_CLOSE):面板进程入 companion 的收割树——companion
//     退出时句柄由内核回收,Job 连带终止面板(⚠️ 刻意不 Close:关闭即收割,
//     面板会当场死掉;句柄泄漏给进程生命周期,KILL_ON_JOB_CLOSE 的语义就是
//     「进程死了才收」)。Job 创建/挂接失败降级为「无收割」(与 macOS 形态
//     一致),不阻断 spawn;
//   - Resume:放行(在 Job 入树之后——入树前面板自己 spawn 的子进程会逃出
//     收割树;挂起窗口期内 Resume 尚未发生,顺序安全)。
func spawnPanelProcess(opts SpawnOpts) (ChildProcess, error) {
	exe, err := panelExecutable()
	if err != nil {
		return nil, fmt.Errorf("os.Executable: %w", err)
	}
	cmd := exec.Command(exe, panelSpawnArgs(opts)...)
	// ⚠️ 不设 HideWindow:面板是 windowsgui 子系统进程,STARTF_USESHOWWINDOW
	// + SW_HIDE 会压制其首个窗口(面板白屏不显示)。
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	child, err := startPanelChild(cmd)
	if err != nil {
		return nil, err
	}
	pid := uint32(cmd.Process.Pid)

	allowSetForegroundWindow(pid)
	attachPanelJob(pid)
	resumePanelProcess(pid)

	logger.Debug(fmt.Sprintf("panel process spawned (pid=%d, side=%s, anchor=(%d,%d))",
		pid, opts.Side, opts.AnchorX, opts.AnchorY))
	return child, nil
}

// panelToggleSender Windows close 请求投递(2026-11-28 撤销方案 B,回归
// 「关闭即退」):FindWindowW 按面板窗口类名定位壳窗口 → PostMessageW 发
// WM_APP+1=请求关闭——壳内 wndProc 分支 PostQuitMessage(0) 正常关闭,窗口
// 随进程退出消失。找不到窗口(面板已死/尚未建窗)或投递失败 → 返回错误,
// launcher 按无子处理回退 spawn。pid 形参忽略:窗口类名是定位纽带,签名与
// darwin 统一。变量名保留历史名 toggle(语义已是「请求关闭」)。
// 已知边界:FindWindowW 按类名【全局】定位——双 companion 并存(单实例守卫
// 正常时不可达)会投递到错误面板实例,备忘(2026-11-28 架构评审)。
var panelToggleSender = func(pid int) error {
	if panelProcFindWindowW.Find() != nil || panelProcPostMessageW.Find() != nil {
		return fmt.Errorf("close 请求投递 API 不可用")
	}
	cls, err := windows.UTF16PtrFromString(panelWindowClassName)
	if err != nil {
		return fmt.Errorf("面板窗口类名构造失败: %w", err)
	}
	hwnd, _, _ := panelProcFindWindowW.Call(
		uintptr(unsafe.Pointer(cls)),
		0, // lpWindowName:按类名定位即可
	)
	if hwnd == 0 {
		return fmt.Errorf("面板窗口未找到(进程已死或窗口未建)")
	}
	if r, _, _ := panelProcPostMessageW.Call(hwnd, uintptr(wmAppPanelToggle), 0, 0); r == 0 {
		return fmt.Errorf("PostMessageW(close 请求) 失败(面板窗口已销毁)")
	}
	return nil
}

// wmAppPanelToggle = WM_APP+1(0x8001):companion → 面板壳的 close 请求消息
// (2026-11-28 撤销方案 B:语义=请求关闭,壳内 PostQuitMessage(0))。与
// internal/panelwin/panelwin_windows.go 壳侧常量(wmApp + 1)同值,跨文件
// 漂移由 TestPanelM2CloseChannelContract 锚定。
const wmAppPanelToggle = 0x8000 + 1

// —— user32 procs(复用 dpi_windows.go 的 user32DLL 变量)——

var (
	panelProcAllowSetForegroundWindow = user32DLL.NewProc("AllowSetForegroundWindow")
	panelProcGetCursorPos             = user32DLL.NewProc("GetCursorPos")
	panelProcGetSystemMetrics         = user32DLL.NewProc("GetSystemMetrics")
	panelProcFindWindowW              = user32DLL.NewProc("FindWindowW")
	panelProcPostMessageW             = user32DLL.NewProc("PostMessageW")
)

// trayPoint GetCursorPos 参数结构(POINT:LONG x,y;x/sys 不再提供,库内自备
// ——风格同 third_party/systray 的 Rect 导出理由,本结构仅包内使用)。
type trayPoint struct {
	X, Y int32
}

// SM_CXSCREEN/SM_CYSCREEN——主屏尺寸(GetSystemMetrics 索引;进程
// PerMonitorV2 感知下返回物理像素,与 TrayRect 同单位可比)。
const (
	smCxScreen = 0
	smCyScreen = 1
)

// readPanelTrayAnchor Windows 锚定采样:TrayRect(物理像素)+ 主屏尺寸 +
// GetCursorPos 光标点。TrayRect 失败/托盘未初始化时 OK=false(接线层判废后
// 走光标兜底);溢出区形态(Win10 S_FALSE+箭头矩形 / Win11 屏外坐标)经
// TrayRect 的错误路径拦截(ret != S_OK 一律返回错误 → OK=false → 短路走
// 光标兜底),panelAnchorUsable 只兜屏上的非法形态(占位矩形/超界矩形)。
func readPanelTrayAnchor() panelAnchorSample {
	s := panelAnchorSample{}
	if panelProcGetSystemMetrics.Find() == nil {
		if cx, _, _ := panelProcGetSystemMetrics.Call(smCxScreen); cx > 0 {
			s.ScreenW = int(int32(cx))
		}
		if ch, _, _ := panelProcGetSystemMetrics.Call(smCyScreen); ch > 0 {
			s.ScreenH = int(int32(ch))
		}
	}
	if r, err := systray.TrayRect(); err == nil {
		s.AX, s.AY, s.W, s.H = int(r.Left), int(r.Top), int(r.Right-r.Left), int(r.Bottom-r.Top)
		s.OK = true
	} else {
		logger.Debug(fmt.Sprintf("panel tray rect unavailable: %v", err))
	}
	var p trayPoint
	if panelProcGetCursorPos.Find() == nil {
		if r, _, _ := panelProcGetCursorPos.Call(uintptr(unsafe.Pointer(&p))); r != 0 {
			s.CursorX, s.CursorY = int(p.X), int(p.Y)
			s.HasCursor = true
		}
	}
	return s
}

// allowSetForegroundWindow 前台资格授予(AllowSetForegroundWindow(pid))。
// 失败 = 失焦即退链失效(面板弹出不激活、点外部不退),落 Warn 供真机
// 归因(Task 16 探针清单挂账项)。
func allowSetForegroundWindow(pid uint32) {
	if panelProcAllowSetForegroundWindow.Find() != nil {
		return
	}
	if r, _, _ := panelProcAllowSetForegroundWindow.Call(uintptr(pid)); r == 0 {
		logger.Warn(fmt.Sprintf("AllowSetForegroundWindow(%d) rejected (best-effort)", pid))
	}
}

// attachPanelJob 把面板进程挂进 KILL_ON_JOB_CLOSE 的收割 Job(句柄刻意不
// 关,语义见 spawnPanelProcess 注释)。失败降级为无收割,不阻断 spawn。
func attachPanelJob(pid uint32) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil || job == windows.InvalidHandle {
		logger.Warn("panel job object create failed, no harvest on exit")
		return
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		_ = windows.CloseHandle(job)
		logger.Warn("panel job object setup failed, no harvest on exit")
		return
	}
	ph, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		_ = windows.CloseHandle(job)
		logger.Warn(fmt.Sprintf("panel process open for job assign failed: %v", err))
		return
	}
	defer windows.CloseHandle(ph) // 关自有句柄;进程与 Job 的关联不受影响
	if err := windows.AssignProcessToJobObject(job, ph); err != nil {
		_ = windows.CloseHandle(job)
		logger.Warn(fmt.Sprintf("panel job assign failed: %v", err))
		return
	}
	// 成功路径刻意不 Close(job):KILL_ON_JOB_CLOSE 在 companion 进程死亡、
	// 内核回收句柄时终止面板进程,收割语义即由「句柄存活到进程死」构成。
}

// resumePanelProcess 放行挂起面板:CreateToolhelp32Snapshot 枚举线程,
// 属本进程的线程逐个 ResumeThread(挂起创建的进程恰好一条主线程,全量
// Resume 等价于放行;尽力而为,不阻断 spawn 返回)。⚠️ 放行失败的后果
// 不是 launcher 崩溃计数——挂起进程永不退出,Wait 不返回,既无崩溃计数
// 也无自动重拉,面板冻在挂起态;launcher 侧点图标发的 close 请求
// (FindWindowW)找不到未建窗的挂起进程会失败回退 spawn(又一只冻住进程,
// 极窄窗口接受),attach 成功时冻住的面板随 companion 死亡被 Job 收割,
// attach 也失败则孤儿至注销/重启。
func resumePanelProcess(pid uint32) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		logger.Warn(fmt.Sprintf("panel resume snapshot failed: %v", err))
		return
	}
	defer windows.CloseHandle(snap)

	var te windows.ThreadEntry32
	te.Size = uint32(unsafe.Sizeof(te))
	if err := windows.Thread32First(snap, &te); err != nil {
		logger.Warn(fmt.Sprintf("panel resume Thread32First failed: %v", err))
		return
	}
	for {
		if te.OwnerProcessID == pid {
			th, err := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, te.ThreadID)
			if err == nil {
				_, _ = windows.ResumeThread(th) // 尽力而为
				_ = windows.CloseHandle(th)
			}
		}
		if err := windows.Thread32Next(snap, &te); err != nil {
			break
		}
	}
}
