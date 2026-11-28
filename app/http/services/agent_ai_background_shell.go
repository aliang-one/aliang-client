package services

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"aliang.one/nursorgate/app/agentcore"
)

// 后台 shell 登记处：宿主侧生命周期归属（2026-11-28 后台 shell 批）。
//
// 职责边界：内核（agentcore）拥有进程纪律（组杀/WaitDelay/独立 ctx 与寿命帽，
// 见 runShellBackground），本登记处只负责「登记簿 + 输出环 + 增量读 + 终止
// 转发 + 收口」：
//   - 跨 run 存活：run 结束/被停，后台进程继续跑（本特性的存在意义）；
//   - 输出环复用 terminalRingBuffer（与终端子系统同款滚动语义，256KB）；
//   - 记录收口：AgentService.Shutdown → manager.backgroundShellsCloseAll；
//     死记录超量惰性逐出（容量风格对齐 sessionApprovals 的 64 上限）。
//   - 终止语义：内核 SetCancel 装填的闭包 armed 保持到进程树终了之后，对已
//     自然完成的任务调用同样安全（内核侧组杀有存活探测，不会误伤复用 pid）。

// agentBackgroundShellMaxRecords 是已结束记录的保留上限（防长寿命进程累积）。
const agentBackgroundShellMaxRecords = 64

// agentBackgroundShellRingBytes 是单条后台 shell 的输出环容量（256KB，dev
// server 启动横幅量级；下限由 newTerminalRingBuffer 的 64KB floor 兜底）。
const agentBackgroundShellRingBytes = 256 * 1024

const (
	// agentBackgroundShellIDPrefix 是登记 id 前缀（内核返回给模型作 shell_id）。
	agentBackgroundShellIDPrefix = "bgsh-"
	// agentBackgroundShellRollMark 是环滚动后增量读的前置标注（不落环，读侧
	// 合成）：告知模型早期字节已被滚动淘汰、当前是残存量重放。
	agentBackgroundShellRollMark = "[输出环已滚动，早期字节被丢弃]\n"
	// agentBackgroundShellCutMarkFmt 是单次读取超 MaxToolOutputBytes 的截断
	// 标注：余量留待下次 read 继续，不静默吞字节。
	agentBackgroundShellCutMarkFmt = "\n[输出超限已截断（单次读取上限 %d 字节），余量下次读取继续]\n"
)

// agentBackgroundShellRegistry 是宿主侧登记簿：manager 级单例（跨会话共享，
// 记录按 session 隔离），实现内核窄接口 agentcore.BackgroundShellRegistry。
type agentBackgroundShellRegistry struct {
	mu         sync.Mutex
	shells     map[string]*agentBackgroundShell
	order      []string // 创建序；死记录惰性逐出用
	seq        int
	capForTest int // 测试注入小容量；0=默认 64
}

// agentBackgroundShell 是单条登记：实现 agentcore.BackgroundShellHandle（内核
// 并发调用 Write/SetCancel/MarkRunning/Finished）。顶层四字段创建后不可变
// （免锁读）；运行态一律入 mu。锁序纪律：registry.mu → 本 mu，任何路径不得
// 反向（内核回调只持本 mu，天然满足）。
type agentBackgroundShell struct {
	id      string
	session string
	command string
	created time.Time

	mu          sync.Mutex
	ring        *terminalRingBuffer
	written     int    // 单调写入总量；环逐出量 = written - 快照残量（read 增量基准）
	cursor      int    // 已读逻辑偏移（对 written 全流）；落在逐出区段时回放残量全量
	cancel      func() // 内核装填的终止闭包；nil=已解除武装（树已终了）
	running     bool   // 主进程在跑；Finished 终态即转 false
	pid         int
	outcome     agentcore.ShellOutcome
	detail      string
	finished    bool
	finishCalls int // 幂等防御观测
}

// 接口契约钉子：登记簿与句柄必须始终满足内核窄接口（Task 5 的 ToolEnv 注入点）。
var (
	_ agentcore.BackgroundShellRegistry = (*agentBackgroundShellRegistry)(nil)
	_ agentcore.BackgroundShellHandle   = (*agentBackgroundShell)(nil)
	_ agentcore.BackgroundShellRegistry = (*agentBackgroundShellSessionView)(nil)
)

func newAgentBackgroundShellRegistry() *agentBackgroundShellRegistry {
	return &agentBackgroundShellRegistry{shells: make(map[string]*agentBackgroundShell)}
}

// Begin 实现内核窄接口的无会话形态：记录归属空会话，仅对 sessionID==""
// 的 read/kill/list 可见。生产路径一律经 sessionView 绑定会话后注入 ToolEnv；
// 直挂形态是测试与误用兜底，不另设闸。
func (r *agentBackgroundShellRegistry) Begin(command string) (agentcore.BackgroundShellHandle, string) {
	return r.beginSession("", command)
}

// sessionView 返回绑定 sessionID 的内核接口视图：内核 Begin 不带会话参数，
// 宿主构造 ToolEnv 时按会话闭包绑定（Task 5 runAgentCore 接线）。同一登记簿、
// 多条视图，read/kill/list 的会话校验以 Begin 时绑定为准。
func (r *agentBackgroundShellRegistry) sessionView(sessionID string) agentcore.BackgroundShellRegistry {
	return &agentBackgroundShellSessionView{reg: r, session: sessionID}
}

// agentBackgroundShellSessionView 是 run 侧的会话绑定适配器（见 sessionView）。
type agentBackgroundShellSessionView struct {
	reg     *agentBackgroundShellRegistry
	session string
}

func (v *agentBackgroundShellSessionView) Begin(command string) (agentcore.BackgroundShellHandle, string) {
	return v.reg.beginSession(v.session, command)
}

// beginSession 登记一条归属 sessionID 的后台 shell：造 id、建记录、惰性逐出
// 超量死记录。返回句柄与 id（内核回填 shell_id 给模型）。
func (r *agentBackgroundShellRegistry) beginSession(sessionID, command string) (agentcore.BackgroundShellHandle, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	rec := &agentBackgroundShell{
		id:      fmt.Sprintf("%s%d", agentBackgroundShellIDPrefix, r.seq),
		session: sessionID,
		command: command,
		created: time.Now(),
		ring:    newTerminalRingBuffer(agentBackgroundShellRingBytes),
	}
	r.shells[rec.id] = rec
	r.order = append(r.order, rec.id)
	r.evictDeadLocked()
	return rec, rec.id
}

// read 取一条记录的增量输出与状态行；会话不符或 id 不存在 → ok=false。
// 增量基准：cursor/written 是对全流（含已被环逐出的部分）的逻辑偏移，环快照
// 的物理起点 = cursor - dropped（dropped = written - 残量）。cursor 落在逐出
// 区段（含「逐出后回灌使残量≥cursor」的掩盖形态，单看 len<cursor 抓不到）时
// 未读字节已不可找回——回放残量全量并前置滚标，绝不静默跳段；单次读取以
// agentcore.MaxToolOutputBytes 封顶，余量留待下次。
func (r *agentBackgroundShellRegistry) read(sessionID, id string) (chunk, status string, ok bool) {
	// 全程持 r.mu（ lookup 到快照一气呵成，防与 evict/closeAll 撕裂）；锁序
	// r.mu → rec.mu。环快照最坏 256KB 搬运，微秒级，串行无碍。
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, found := r.shells[id]
	if !found || rec.session != sessionID {
		return "", "", false
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()

	data := rec.ring.snapshot()
	dropped := rec.written - len(data) // 环已逐出的字节量（单调计数-残量）
	if rec.cursor < dropped {
		// 游标落在已被逐出的区段：回退残量全量并标注。
		rec.cursor = dropped
		chunk = agentBackgroundShellRollMark
	}
	raw := data[rec.cursor-dropped:]
	delivered := len(raw)
	if delivered > agentcore.MaxToolOutputBytes {
		delivered = agentcore.MaxToolOutputBytes
		chunk += string(raw[:delivered])
		chunk += fmt.Sprintf(agentBackgroundShellCutMarkFmt, agentcore.MaxToolOutputBytes)
	} else {
		chunk += string(raw)
	}
	// 游标只推进实付字节（逻辑流），截断余量下次照常读出。
	rec.cursor += delivered
	return chunk, rec.statusLineLocked(), true
}

// kill 终止一条记录：调内核装填的终止闭包（组杀+bgCancel）并解除武装。
// 对已自然完成的任务同样安全——内核闭包有 kill(-pid,0) 存活探测，死树不误杀
// （Windows 按 Job 身份杀）；armed 已解除时只回报诚实状态、不二次杀。
// 闭包在锁外调用：闭包会间接触发内核等待 goroutine 的 Finished 回调（回持
// rec.mu），锁外调用避免拖长持锁窗。
func (r *agentBackgroundShellRegistry) kill(sessionID, id string) (found, wasRunning bool) {
	r.mu.Lock()
	rec, ok := r.shells[id]
	r.mu.Unlock()
	if !ok || rec.session != sessionID {
		return false, false
	}

	rec.mu.Lock()
	wasRunning = rec.running
	fn := rec.cancel
	rec.cancel = nil // 调用即解除武装，重复 kill 幂等
	rec.mu.Unlock()
	if fn != nil {
		fn()
	}
	return true, wasRunning
}

// list 返回本 session 全部记录的状态行（创建序），形如
// "bgsh-3 sleep 30 running(pid=123, started=5s 前)"——id 在行首，消费侧
// （Task 5 BashOutput/KillShell）按 id 取段。command 折叠空白（多行命令压
// 单行），防破列表行形。
func (r *agentBackgroundShellRegistry) list(sessionID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines := make([]string, 0, len(r.order))
	for _, id := range r.order {
		rec := r.shells[id]
		if rec == nil || rec.session != sessionID {
			continue
		}
		rec.mu.Lock()
		folded := strings.Join(strings.Fields(rec.command), " ")
		lines = append(lines, fmt.Sprintf("%s %s %s", rec.id, folded, rec.statusLineLocked()))
		rec.mu.Unlock()
	}
	return lines
}

// closeAll 收口：终止全部存活记录（调非 nil cancel）并清空登记簿。
// AgentService.Shutdown（进程退出真关停，RUN-03 同语义）等调用方使用；空簿
// 幂等。reason 供调用点语义对齐 closeAllWithReason，登记处不做日志。
func (r *agentBackgroundShellRegistry) closeAll(reason string) {
	r.mu.Lock()
	cancels := make([]func(), 0, len(r.shells))
	for _, rec := range r.shells {
		rec.mu.Lock()
		if rec.cancel != nil {
			cancels = append(cancels, rec.cancel)
			rec.cancel = nil // 先解除再调用，与 kill 并发也不双杀
		}
		rec.mu.Unlock()
	}
	r.shells = make(map[string]*agentBackgroundShell)
	r.order = nil
	r.mu.Unlock()
	for _, fn := range cancels {
		fn()
	}
}

// evictDeadLocked 惰性逐出超量的已结束记录（从最旧起），活记录永不逐出。
// 调用方须持 r.mu（beginSession 触发）；逐条读 finished 短暂入 rec.mu（-race
// 纪律）。
func (r *agentBackgroundShellRegistry) evictDeadLocked() {
	capRecords := r.capForTest
	if capRecords <= 0 {
		capRecords = agentBackgroundShellMaxRecords
	}
	var dead []string
	for _, id := range r.order {
		rec := r.shells[id]
		if rec == nil {
			continue
		}
		rec.mu.Lock()
		finished := rec.finished
		rec.mu.Unlock()
		if finished {
			dead = append(dead, id)
		}
	}
	for len(dead) > capRecords {
		oldest := dead[0]
		dead = dead[1:]
		delete(r.shells, oldest)
		for i, id := range r.order {
			if id == oldest {
				r.order = append(r.order[:i], r.order[i+1:]...)
				break
			}
		}
	}
}

// ---------------------------------------------------------------------------
// agentcore.BackgroundShellHandle 实现（内核并发调用面）
// ---------------------------------------------------------------------------

// Write 接收内核合并的 stdout/stderr（两根管道各一条拷贝 goroutine 并发调），
// 推进输出环——有界滚动，不无界累积。
func (s *agentBackgroundShell) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ring.push(p)
	s.written += len(p) // 单调流计数：read 的逐出量判定基准（环残量会缩水）
	return len(p), nil
}

// SetCancel 装填/解除（fn=nil）终止闭包。armed 保持到进程树终了之后的内核
// 收尾点；解除后的时序竞争由 kill/closeAll 的 nil 检查兜底。
func (s *agentBackgroundShell) SetCancel(fn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cancel = fn
}

// MarkRunning 记录内核成功启动后的 PID（展示用）。
func (s *agentBackgroundShell) MarkRunning(pid int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pid, s.running = pid, true
}

// Finished 报告终态。内核保证恰好一次（runShellBackground 用 sync.Once 收敛
// panic 兜底/终态 switch/启动失败三条路径）；本处以首次为准做幂等防御：
// finishCalls 照记（观测面），outcome/detail 不被覆盖。
func (s *agentBackgroundShell) Finished(outcome agentcore.ShellOutcome, detail string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finishCalls++
	if s.finished {
		return
	}
	s.finished = true
	s.outcome = outcome
	s.detail = detail
	s.running = false // 终态即主进程不再在跑（kill 的 wasRunning 语义以此为准）
}

// statusLineLocked 合成状态行（read/list 共用；调用方须持 s.mu）。
func (s *agentBackgroundShell) statusLineLocked() string {
	if s.finished {
		return fmt.Sprintf("%s(%s)", s.outcome, s.detail)
	}
	if s.running {
		return fmt.Sprintf("running(pid=%d, started=%ds 前)", s.pid, int(time.Since(s.created).Seconds()))
	}
	return "starting(pid 未定，等待进程启动)"
}

// ---------------------------------------------------------------------------
// agentAIManager 挂载
// ---------------------------------------------------------------------------

// ensureBackgroundShells 惰性构造（sessionApprovals 的内联判空同款风格）。
func (m *agentAIManager) ensureBackgroundShells() *agentBackgroundShellRegistry {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.backgroundShells == nil {
		m.backgroundShells = newAgentBackgroundShellRegistry()
	}
	return m.backgroundShells
}

// backgroundShellsCloseAll 供 AgentService.Shutdown 调用（进程退出真关停，
// RUN-03 同语义）。manager 为 nil 时 no-op。
func (m *agentAIManager) backgroundShellsCloseAll(reason string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	reg := m.backgroundShells
	m.mu.Unlock()
	if reg != nil {
		reg.closeAll(reason)
	}
}
