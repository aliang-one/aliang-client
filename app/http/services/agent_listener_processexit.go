package services

// agent_listener_processexit.go process_exit 事件源 watcher(specs/005-event-
// listener §3/§1.5,plan 2026-11-21 1b Task A1):本机命令/进程退出监听,命中
// 即经 listenerEventUplink 缝上行 listener.event(§1.5 契约原样)。
//
// 两种模式(spec §3 params):
//   - 代启(params.command 非空):exec.CommandContext 执行命令并等待退出,
//     退出码比对 expect_code(缺省=任意退出都触发;不匹配不发帧);命令串按
//     空白切 argv(strings.Fields),不走 shell——引号/管道/shell 语法不支持,
//     需要 shell 语义时由注册方自行包 `sh -c`(注入面也安全:不经 shell 拼接)。
//   - 挂 pid(params.pid>0):每 2s 轮询 os.FindProcess + signal 0,直到进程
//     消失。跨平台固有限界:非子进程拿不到退出码(waitpid 只对子进程可用),
//     故 pid 模式只能探测「进程消失」,帧 detail.exit_code 恒 null,expect_code
//     无码可比被忽略。另注意 unix 僵尸态:未收尸的进程 signal 0 仍报存活。
//
// 生命周期(与 file_tail 同一钩子族,接线仍在 filetail.go 的 init()):
//   - 上行/outbox/镜像注记全部复用 filetail.go 既有设施(listenerEventUplink /
//     enqueueListenerOutbox / mutateListenerMirrorLocal 平局注记技巧);
//   - 注册表独立(listenerProcExitWatchers):watcher 语义不同(有子进程要杀、
//     单发自停),但 id 语义同族——同 id 至多一个 watcher(跨两注册表)。为此
//     两侧 sync 路径互通停止(stopAnyListenerWatcher:双注册表幂等全停),
//     覆盖 source_type 漂移(file_tail↔process_exit→http_poll)的收敛;
//   - agent 重启不重建(spec §3 显式定案,防重启风暴):boot 路径
//     (StartEventListenerWatchers)发现的 active 本机行不 spawn,本地置
//     error(agent_restarted);行经显式复活(listener_update status=active →
//     server 下行 upsert → 钩子)后才会重新 spawn。live upsert 钩子照常 spawn
//     ——注册时刻进程/命令尚未起跑,「重启打断」语义不适用。
//
// 单发语义:代启命令退出 / 挂 pid 进程消失 → 发帧(或不发)→ watcher 自停。
// agent 不重跑命令(recurring 的重发裁决在 server 侧 status/cooldown;重跑须
// 重新注册——与重启不重建同理,agent 不得成为进程风暴源)。
//
// 失败分档:
//   - 启动失败(command 不存在/权限):本地置 error(exec_failed),不上行
//     (同 file_tail source_gone 的本地终态姿态);
//   - 上行失败:入 outbox 断联暂存(filetail.go M2-B 同一队列)。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"aliang.one/nursorgate/app/http/models"
	"aliang.one/nursorgate/common/logger"
)

// ---------------------------------------------------------------------------
// 可调参数(测试 swap/shrink;atomicDuration:goroutine 在跑时改值须无竞态)
// ---------------------------------------------------------------------------

var (
	// listenerProcExitExecTimeout 代启命令硬顶:超过视为挂死,超时杀掉,被杀
	// 结果(exit_code=-1)即事件结果(task 定案)。
	listenerProcExitExecTimeout = newDurationAtom(30 * time.Second)
	// listenerProcExitPollInterval pid 模式轮询周期(signal 0 探活)。
	listenerProcExitPollInterval = newDurationAtom(2 * time.Second)
	// listenerProcExitStopGrace 停止等待上限(同 file_tail 的 stop grace)。
	listenerProcExitStopGrace = newDurationAtom(5 * time.Second)
)

// ---------------------------------------------------------------------------
// 参数解析
// ---------------------------------------------------------------------------

// processExitSpec process_exit 行的启动参数(镜像行 diff 用;构造后不变)。
type processExitSpec struct {
	command    string // 代启模式:非空(原始串,run 时按空白切 argv)
	pid        int    // pid 模式:>0(command 优先,两者并存按代启)
	expectCode int    // 期望退出码
	hasExpect  bool   // expect_code 是否显式携带(区分缺省与 0)
}

// parseProcessExitParams 校验并抽取 process_exit 行的启动参数。有效 = id/
// updated_at 非空 + params 内 command 非空或 pid>0(其余键容忍,前向兼容)。
// expect_code 缺省(含 null)= 任意退出都触发。
func parseProcessExitParams(m ListenerMirror) (processExitSpec, bool) {
	if strings.TrimSpace(m.ID) == "" || strings.TrimSpace(m.UpdatedAt) == "" {
		return processExitSpec{}, false
	}
	if len(m.SourceParams) == 0 {
		return processExitSpec{}, false
	}
	var p struct {
		Command    string `json:"command"`
		PID        int64  `json:"pid"`
		ExpectCode *int   `json:"expect_code"`
	}
	if err := json.Unmarshal(m.SourceParams, &p); err != nil {
		return processExitSpec{}, false
	}
	spec := processExitSpec{command: strings.TrimSpace(p.Command), pid: int(p.PID)}
	if p.ExpectCode != nil {
		spec.expectCode, spec.hasExpect = *p.ExpectCode, true
	}
	if spec.command != "" || spec.pid > 0 {
		return spec, true
	}
	return processExitSpec{}, false
}

// ---------------------------------------------------------------------------
// 管理器:注册表 + 生命周期
// ---------------------------------------------------------------------------

var (
	// listenerProcExitMu 守护 listenerProcExitWatchers 注册表(增删查全走它)。
	listenerProcExitMu sync.Mutex
	// listenerProcExitWatchers listener_id → 在跑 process_exit watcher(本机
	// 唯一属主:同 id 至多一个,跨 file_tail/process_exit 两注册表)。
	listenerProcExitWatchers = map[string]*processExitWatcher{}
)

// syncProcessExitWatcherForMirror process_exit 行 → watcher 生命周期(upsert
// 钩子路由入口,语义同 filetail.go 的 syncListenerWatcherForMirror):
// 非 process_exit/他机行/无效行/非 active → 停(幂等);active → 未在跑起、
// 在跑且参数一致 no-op、参数变更停+重启。停止一律走 stopAnyListenerWatcher
// (双注册表)收敛 source_type 漂移残留。
func syncProcessExitWatcherForMirror(m ListenerMirror) {
	if m.SourceType != listenerSourceTypeProcessExit {
		stopAnyListenerWatcher(m.ID)
		return
	}
	if localID := listenerLocalDeviceID(); m.SourceDeviceID != localID {
		logger.Debug(fmt.Sprintf("[AGENT-LISTENER] process_exit 行属设备 %q(本机 %q),不入本地 watch id=%s",
			m.SourceDeviceID, localID, m.ID))
		stopAnyListenerWatcher(m.ID)
		return
	}
	spec, ok := parseProcessExitParams(m)
	if !ok {
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] process_exit 行无效,跳过 watcher(id=%s 需非空 id/updated_at + command 或 pid)", m.ID))
		stopAnyListenerWatcher(m.ID)
		return
	}
	if m.Status != "active" {
		stopAnyListenerWatcher(m.ID)
		return
	}
	listenerProcExitMu.Lock()
	cur, running := listenerProcExitWatchers[m.ID]
	sameParams := running && cur.spec == spec
	listenerProcExitMu.Unlock()
	if sameParams {
		return
	}
	if running {
		stopProcessExitWatcher(m.ID)
	}
	// 对侧注册表残留收敛(类型自 file_tail 漂移而来的常态 no-op)。
	stopListenerWatcher(m.ID)
	startProcessExitWatcher(m.ID, spec)
}

// startProcessExitWatcher 启动单 watcher(已在跑同 id → no-op,幂等入口)。
func startProcessExitWatcher(id string, spec processExitSpec) {
	listenerProcExitMu.Lock()
	if _, exists := listenerProcExitWatchers[id]; exists {
		listenerProcExitMu.Unlock()
		return
	}
	w := &processExitWatcher{id: id, spec: spec, stopCh: make(chan struct{}), doneCh: make(chan struct{})}
	listenerProcExitWatchers[id] = w
	listenerProcExitMu.Unlock()
	safeGo("listener_process_exit_"+id, w.run)
	logger.Info(fmt.Sprintf("[AGENT-LISTENER] process_exit watcher started id=%s command=%q pid=%d", id, spec.command, spec.pid))
}

// stopProcessExitWatcher 停止并摘除 id 对应 process_exit watcher(幂等)。
// 代启模式停止即杀子进程(命令是 agent 替监听跑的,监听没了没理由留活口)。
func stopProcessExitWatcher(id string) {
	listenerProcExitMu.Lock()
	w, ok := listenerProcExitWatchers[id]
	if ok {
		delete(listenerProcExitWatchers, id)
	}
	listenerProcExitMu.Unlock()
	if !ok {
		return
	}
	w.requestStop()
	select {
	case <-w.doneCh:
	case <-time.After(listenerProcExitStopGrace.Load()):
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] process_exit watcher stop_grace_expired id=%s", id))
	}
	logger.Info(fmt.Sprintf("[AGENT-LISTENER] process_exit watcher stopped id=%s", id))
}

// stopAnyListenerWatcher 双注册表统一停止(listener.remove 钩子 / reject 帧 /
// source_type 漂移收敛的共用入口):file_tail 与 process_exit 各查一次,不在册
// 即 no-op——id 全局唯一,两表同 id 并存是非法态,此函数即为收敛手段。
func stopAnyListenerWatcher(id string) {
	stopListenerWatcher(id)
	stopProcessExitWatcher(id)
}

// markListenerProcessExitRestartedAtBoot boot 发现的 process_exit 行处置
// (StartEventListenerWatchers 专用):active 本机行按「重启前在跑」处理——
// 不重建 watcher、本地置 error(agent_restarted,spec §3 重启风暴防线);
// paused/fired/expired/error 行与他机行不注记(非重启打断形态/不归本机管)。
// 注记走 mutateListenerMirrorLocal 平局技巧(同 file_tail source_gone):本地
// 派生注记,不上行;AI/用户显式复活(update status=active → server 下行新
// upsert)后照常重新 spawn。
func markListenerProcessExitRestartedAtBoot(m ListenerMirror) {
	stopProcessExitWatcher(m.ID) // 防御残留(Start 前应已全停)
	if m.Status != "active" {
		return
	}
	if localID := listenerLocalDeviceID(); m.SourceDeviceID != localID {
		return
	}
	logger.Warn(fmt.Sprintf("[AGENT-LISTENER] process_exit 行重启后不重建,本地置 error(agent_restarted) id=%s", m.ID))
	mutateListenerMirrorLocal(m.ID, func(cur *ListenerMirror) {
		cur.Status = "error"
		cur.ErrorNote = "agent_restarted"
	})
}

// ---------------------------------------------------------------------------
// 单 watcher
// ---------------------------------------------------------------------------

// processExitWatcher 一个 process_exit 监听行对应一个实例。除注册表交互
// (stopCh/doneCh/cancelMu)外的字段(spec)构造后不变,无须加锁。单发:
// 命令退出/进程消失处理完毕即自停(selfStop:摘注册表 + 关 stopCh)。
type processExitWatcher struct {
	id   string
	spec processExitSpec

	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}

	// cancelMu 守护 cancel:run goroutine 写、requestStop(外部停)读。
	cancelMu sync.Mutex
	cancel   context.CancelFunc // 代启模式:外部停时提前杀子进程
}

// requestStop 幂等关停(close-once):外部 stop 与自停共用;先关 stopCh 再
// cancel 子进程——run 侧以 stopCh 判「非监听语义退出,不发帧」。
func (w *processExitWatcher) requestStop() {
	w.stopOnce.Do(func() {
		close(w.stopCh)
		w.cancelMu.Lock()
		if w.cancel != nil {
			w.cancel()
		}
		w.cancelMu.Unlock()
	})
}

// selfStop 自停:摘注册表(仅当在册的是自己)+ requestStop。与 file_tail 的
// markSourceGone 同款收敛;镜像注记由调用方按需补。
func (w *processExitWatcher) selfStop() {
	listenerProcExitMu.Lock()
	if cur, ok := listenerProcExitWatchers[w.id]; ok && cur == w {
		delete(listenerProcExitWatchers, w.id)
	}
	listenerProcExitMu.Unlock()
	w.requestStop()
}

// run watcher 主入口:按模式分发;退出即自停(单发语义,见文件头注)。
// 起跑先看停止位:注册与 run 调度之间到达的外部停(删除/暂停)不得再起
// 子进程/再发帧(否则产生最长一个 exec 超时窗的孤儿命令)。
func (w *processExitWatcher) run() {
	defer close(w.doneCh)
	defer w.selfStop()
	select {
	case <-w.stopCh:
		return
	default:
	}
	start := time.Now()
	if w.spec.command != "" {
		w.runCommand(start)
		return
	}
	w.runPid(start)
}

// runCommand 代启模式:执行命令等退出;输出不接管(直落 /dev/null,防管道
// 写满卡死)。退出码裁决:
//   - 正常退出 / 非零退出:cmd.Run 的 ExitError.ExitCode();
//   - 超时杀(listenerProcExitExecTimeout):被杀结果即事件结果,exit_code=-1;
//   - 外部停(stopCh 已关,listener 删除/暂停):不发帧直接退;
//   - 启动失败(命令不存在/权限):本地置 error(exec_failed),不上行。
func (w *processExitWatcher) runCommand(start time.Time) {
	fields := strings.Fields(w.spec.command)
	if len(fields) == 0 {
		return // parse 已拦,防御
	}
	ctx, cancel := context.WithTimeout(context.Background(), listenerProcExitExecTimeout.Load())
	w.cancelMu.Lock()
	w.cancel = cancel
	w.cancelMu.Unlock()
	defer cancel()

	cmd := exec.CommandContext(ctx, fields[0], fields[1:]...)
	err := cmd.Run()

	// 外部停导致的退出不是监听语义的事件(子进程随 cancel 被杀)。
	select {
	case <-w.stopCh:
		return
	default:
	}

	timedOut := ctx.Err() != nil
	if err == nil {
		w.finishCommand(start, 0)
		return
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode() // unix 信号死 = -1
		if timedOut {
			code = -1 // windows TerminateProcess 可能回非 -1,统一钉 -1
		}
		w.finishCommand(start, code)
		return
	}
	// 启动失败族(*exec.Error / PathError):本地终态,不上行。
	logger.Warn(fmt.Sprintf("[AGENT-LISTENER] process_exit 代启失败置 error id=%s command=%q err=%v", w.id, w.spec.command, err))
	mutateListenerMirrorLocal(w.id, func(m *ListenerMirror) {
		m.Status = "error"
		m.ErrorNote = "exec_failed"
	})
}

// runPid pid 模式:起跑先探一次(注册时进程已死 = 立即命中),此后每
// listenerProcExitPollInterval 探活,消失即发帧。exit_code 跨平台拿不到
// (见文件头注),expect_code 无码可比被忽略。
func (w *processExitWatcher) runPid(start time.Time) {
	ticker := time.NewTicker(listenerProcExitPollInterval.Load())
	defer ticker.Stop()
	for {
		if listenerPidGone(w.spec.pid) {
			w.emitPidGone(start)
			return
		}
		select {
		case <-w.stopCh:
			return
		case <-ticker.C:
		}
	}
}

// finishCommand 代启退出裁决:expect_code 显式携带且不匹配 → 不发帧(事件
// 记 exit_code 是 server 侧注记,agent 不发不触发帧);否则构造帧上行。
// fingerprint = sha256(exit_code + duration_ms)(1b task 定案;spec §1.5 的
// 「退出码+时间桶」落为此式——duration 入指纹,同码不同次运行不互相去重)。
func (w *processExitWatcher) finishCommand(start time.Time, exitCode int) {
	if w.spec.hasExpect && exitCode != w.spec.expectCode {
		logger.Info(fmt.Sprintf("[AGENT-LISTENER] process_exit 退出码 %d ≠ expect_code %d,不触发 id=%s", exitCode, w.spec.expectCode, w.id))
		return
	}
	durationMs := time.Since(start).Milliseconds()
	sum := sha256.Sum256([]byte(fmt.Sprintf("code=%d;duration_ms=%d", exitCode, durationMs)))
	w.emitEvent(map[string]interface{}{
		"type":        models.AgentEventListenerEvent,
		"listener_id": w.id,
		"fingerprint": hex.EncodeToString(sum[:]),
		"fired_at":    time.Now().UTC().Format(time.RFC3339),
		"summary":     fmt.Sprintf("process exited (code %d)", exitCode),
		"source_type": listenerSourceTypeProcessExit,
		"detail": map[string]interface{}{
			"command":     truncateListenerRunes(stripListenerControlChars(w.spec.command), 200),
			"exit_code":   exitCode,
			"duration_ms": durationMs,
		},
	})
}

// emitPidGone pid 模式命中帧:exit_code 恒 null(固有限界),duration_ms =
// 自 watcher 起跑到探测消失(轮询粒度 ±2s)。
func (w *processExitWatcher) emitPidGone(start time.Time) {
	durationMs := time.Since(start).Milliseconds()
	sum := sha256.Sum256([]byte(fmt.Sprintf("pid=%d;duration_ms=%d", w.spec.pid, durationMs)))
	w.emitEvent(map[string]interface{}{
		"type":        models.AgentEventListenerEvent,
		"listener_id": w.id,
		"fingerprint": hex.EncodeToString(sum[:]),
		"fired_at":    time.Now().UTC().Format(time.RFC3339),
		"summary":     fmt.Sprintf("process gone (pid %d)", w.spec.pid),
		"source_type": listenerSourceTypeProcessExit,
		"detail": map[string]interface{}{
			"pid":         w.spec.pid,
			"exit_code":   nil,
			"duration_ms": durationMs,
		},
	})
}

// emitEvent 发送 listener.event 帧(与 file_tail 同一上行缝):失败入 outbox
// 断联暂存;成功且有积压顺手补发(通道已证明可用)。
func (w *processExitWatcher) emitEvent(frame map[string]interface{}) {
	if err := listenerEventUplink(frame); err != nil {
		logger.Warn(fmt.Sprintf("[AGENT-LISTENER] process_exit 上行失败入暂存 id=%s err=%v", w.id, err))
		enqueueListenerOutbox(frame)
		return
	}
	if listenerOutboxHasFrames() {
		safeGo("listener_outbox_drain_after_send", drainListenerOutbox)
	}
}

// ---------------------------------------------------------------------------
// pid 探活
// ---------------------------------------------------------------------------

// listenerPidGone 探测 pid 是否已消失(尽力而为,跨平台):
//   - unix:os.FindProcess 恒成功,signal 0 探活——nil/EPERM=活(EPERM=存在
//     但无权限),ESRCH 族=已消失;
//   - windows:FindProcess 即探测(OpenProcess 失败=已消失);Signal(0) 在
//     windows 恒报 not implemented(无 errno 可分),不作为消失证据。已知
//     限界:对「已终止但 OpenProcess 仍成功」的 pid,本探测恒报活——windows
//     侧 pid 模式的可靠消失探测需要等柄/注册表等另起的机制,1b 不做
//     (task 定案:pid 模式跨平台限界文档化即可,代启模式才是主路径);
//   - 僵尸限界:unix 下未收尸的进程 signal 0 仍报存活,消失探测依赖父进程
//     reap(自身代启模式无此问题——run/wait 同进程)。
func listenerPidGone(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return true // windows:OpenProcess 失败=已消失
	}
	sigErr := proc.Signal(syscall.Signal(0))
	switch {
	case sigErr == nil:
		return false
	case errors.Is(sigErr, syscall.EPERM):
		return false // unix:存在但无权限
	case errors.Is(sigErr, os.ErrProcessDone):
		return true
	case runtime.GOOS == "windows":
		return false // Signal(0) 恒 not implemented,不足为消失证据
	default:
		return true // unix ESRCH 族=已消失
	}
}
