//go:build !windows

package services

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/agentcore"
)

// bgTestAllowAllPolicy 是后台 shell 测试用全放行策略（仿内核
// tools_shell_background_test.go 的 allowAllShellPolicy：/bin/bash -c 全放行，
// 不复用生产 agentCoreShellPolicy 是为了绕开项目围栏——登记处测试只关心
// 生命周期语义，不关心路径围栏）。
type bgTestAllowAllPolicy struct{}

func (bgTestAllowAllPolicy) Resolve(kind string) (string, []string, error) {
	return "/bin/bash", []string{"-c"}, nil
}

func (bgTestAllowAllPolicy) Allow(string) (bool, string) { return true, "" }

// bgTestSpawn 经内核 Bash 工具（DefaultRegistry().Get("Bash")）真实后台启动
// command，走完整内核链路（Begin→MarkRunning→Write→Finished），返回 shell_id。
func bgTestSpawn(t *testing.T, reg *agentBackgroundShellRegistry, session, command string) string {
	t.Helper()
	env := &agentcore.ToolEnv{
		ProjectPath:      t.TempDir(),
		BackgroundShells: reg.sessionView(session),
		ShellPolicy:      bgTestAllowAllPolicy{},
	}
	tool, found := agentcore.DefaultRegistry().Get("Bash")
	if !found {
		t.Fatal("DefaultRegistry 应注册 Bash 工具")
	}
	res := tool.Execute(context.Background(), map[string]interface{}{
		"command":           command,
		"run_in_background": true,
	}, env)
	if res.IsError {
		t.Fatalf("后台启动不应失败: %s", res.Content)
	}
	// 解析「后台任务已启动 shell_id=bgsh-N（PID …）」——括号全角，按 id 字符
	// 集截断而不是找 ASCII 括号。
	mark := "shell_id="
	i := strings.Index(res.Content, mark)
	if i < 0 {
		t.Fatalf("返回应携带 shell_id，得到: %s", res.Content)
	}
	rest := res.Content[i+len(mark):]
	end := len(rest)
	for j, r := range rest {
		if !(r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			end = j
			break
		}
	}
	id := strings.TrimSpace(rest[:end])
	if id == "" {
		t.Fatalf("shell_id 解析失败，得到: %s", res.Content)
	}
	return id
}

// bgTestRecord 取登记簿内记录指针（同包测试观测终态/注入防御调用用；观测
// 一律经 rec.mu 入锁，禁裸读字段）。
func bgTestRecord(t *testing.T, reg *agentBackgroundShellRegistry, id string) *agentBackgroundShell {
	t.Helper()
	reg.mu.Lock()
	defer reg.mu.Unlock()
	rec := reg.shells[id]
	if rec == nil {
		t.Fatalf("记录 %s 应在簿内", id)
	}
	return rec
}

// bgTestFinishedCancelled 是锁内快照：记录已收尾且终态为 cancelled。
func bgTestFinishedCancelled(rec *agentBackgroundShell) bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.finished && rec.outcome == agentcore.ShellCancelled
}

func TestBackgroundShellRegistryBeginReadKill(t *testing.T) {
	reg := newAgentBackgroundShellRegistry()
	id := bgTestSpawn(t, reg, "s1", "echo reg-hello")

	// 轮询增量读到 reg-hello（此刻 status 可能仍在 running）。
	var chunk, status string
	var ok bool
	waitFor(t, func() bool {
		chunk, status, ok = reg.read("s1", id)
		return ok && strings.Contains(chunk, "reg-hello")
	})
	if !strings.Contains(chunk, "reg-hello") {
		t.Fatalf("增量应含 reg-hello，得到 %q", chunk)
	}

	// 收尾后 status 转 completed；再读一次应为空 delta（增量语义：不重发）。
	waitFor(t, func() bool {
		_, status, _ = reg.read("s1", id)
		return strings.Contains(status, "completed")
	})
	if !strings.HasPrefix(status, "completed(") {
		t.Fatalf("终态行应为 completed(...)，得到 %q", status)
	}
	chunk2, status2, ok2 := reg.read("s1", id)
	if !ok2 {
		t.Fatal("completed 后 read 仍应命中记录")
	}
	if chunk2 != "" {
		t.Fatalf("二次 read 应为空 delta（增量语义），得到 %q", chunk2)
	}
	if !strings.Contains(status2, "completed") {
		t.Fatalf("二次 read 状态仍应 completed，得到 %q", status2)
	}

	// 内核接口直挂形态（不经 sessionView）的契约兜底：记录归属空会话，仅对
	// sessionID=="" 的 read 可见。生产路径（Task 5 ToolEnv）一律走 sessionView。
	h, rawID := reg.Begin("echo raw")
	if _, _, ok := reg.read("", rawID); !ok {
		t.Fatal("空会话 read 应可见直挂记录")
	}
	if _, _, ok := reg.read("s1", rawID); ok {
		t.Fatal("其他会话 read 不得看到直挂记录")
	}
	_ = h
}

func TestBackgroundShellRegistryIncremental(t *testing.T) {
	reg := newAgentBackgroundShellRegistry()
	id := bgTestSpawn(t, reg, "s2", `printf 'a\nb\nc\n'; sleep 0.3; printf 'd\ne\n'`)

	// 持续增量读直到收敛：分片时序不钉死（快机器一次拿全、慢机器多段），
	// 但拼接必须恰为全量输出——不重发、不丢失、不重复。
	var got strings.Builder
	emptyDeltas := 0
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		chunk, status, ok := reg.read("s2", id)
		if !ok {
			t.Fatalf("read 不应丢失记录，得到 ok=false")
		}
		got.WriteString(chunk)
		if chunk == "" {
			emptyDeltas++
		}
		if chunk == "" && strings.Contains(status, "completed") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if want := "a\nb\nc\nd\ne\n"; got.String() != want {
		t.Fatalf("增量拼接应恰为全量输出（不重发不丢失），得到 %q want %q", got.String(), want)
	}
	if emptyDeltas == 0 {
		t.Fatal("应至少出现一次空 delta（增量语义）")
	}
}

func TestBackgroundShellRegistryKillRunning(t *testing.T) {
	reg := newAgentBackgroundShellRegistry()
	id := bgTestSpawn(t, reg, "s3", "sleep 30")
	waitFor(t, func() bool {
		_, status, ok := reg.read("s3", id)
		return ok && strings.Contains(status, "running")
	})

	// 对存活任务 kill：found + wasRunning 诚实回报。
	found, wasRunning := reg.kill("s3", id)
	if !found || !wasRunning {
		t.Fatalf("存活任务 kill 应返回 (true, true)，得到 (%v, %v)", found, wasRunning)
	}
	waitFor(t, func() bool {
		_, status, ok := reg.read("s3", id)
		return ok && strings.Contains(status, "cancelled")
	})
	if chunk, _, _ := reg.read("s3", id); chunk != "" {
		t.Fatalf("sleep 无输出，cancelled 后增量应为空，得到 %q", chunk)
	}

	// 终态后再 kill：记录仍在（found=true）、running 已随终态转 false。
	found2, wasRunning2 := reg.kill("s3", id)
	if !found2 || wasRunning2 {
		t.Fatalf("终态后 kill 应返回 (true, false)，得到 (%v, %v)", found2, wasRunning2)
	}

	// 内核 armed 契约的宿主侧兜底：任务已自然收尾但 cancel 仍短暂 armed 时，
	// kill 照常调用（内核闭包有存活探测，死树不误杀）。用无进程记录手工模拟
	// 该窗口，闭包应恰被调一次。
	h, manualID := reg.sessionView("s3").Begin("echo manual-armed")
	armed := 0
	h.SetCancel(func() { armed++ })
	h.Finished(agentcore.ShellCompleted, "手工收尾（模拟自然完成后仍 armed）")
	found3, wasRunning3 := reg.kill("s3", manualID)
	if !found3 || wasRunning3 {
		t.Fatalf("armed 记录 kill 应返回 (true, false)，得到 (%v, %v)", found3, wasRunning3)
	}
	if armed != 1 {
		t.Fatalf("armed 闭包应被调用恰一次，实测 %d", armed)
	}
}

func TestBackgroundShellRegistryList(t *testing.T) {
	reg := newAgentBackgroundShellRegistry()
	idEcho := bgTestSpawn(t, reg, "sess-a", "echo list-done")
	idSleep := bgTestSpawn(t, reg, "sess-a", "sleep 30")
	idOther := bgTestSpawn(t, reg, "sess-b", "echo other-session")

	waitFor(t, func() bool {
		_, status, ok := reg.read("sess-a", idEcho)
		return ok && strings.Contains(status, "completed")
	})
	waitFor(t, func() bool {
		_, status, ok := reg.read("sess-a", idSleep)
		return ok && strings.Contains(status, "running")
	})

	listA := reg.list("sess-a")
	if len(listA) != 2 {
		t.Fatalf("sess-a 应恰好两条记录，得到 %d: %v", len(listA), listA)
	}
	// 创建序：先 echo 后 sleep；状态行各自正确。
	if !strings.Contains(listA[0], idEcho) || !strings.Contains(listA[0], "completed") {
		t.Fatalf("首条应为 echo 的 completed 行，得到 %q", listA[0])
	}
	if !strings.Contains(listA[1], idSleep) || !strings.Contains(listA[1], "running") {
		t.Fatalf("次条应为 sleep 的 running 行，得到 %q", listA[1])
	}
	joined := strings.Join(listA, "\n")
	if strings.Contains(joined, idOther) {
		t.Fatalf("sess-a 列表不得混入其他会话记录: %v", listA)
	}

	listB := reg.list("sess-b")
	if len(listB) != 1 || !strings.Contains(listB[0], idOther) {
		t.Fatalf("sess-b 应只见自己一条，得到 %v", listB)
	}
	if strings.Contains(listB[0], idEcho) || strings.Contains(listB[0], idSleep) {
		t.Fatalf("sess-b 列表不得混入 sess-a 记录: %v", listB)
	}
	if got := reg.list("sess-zz"); len(got) != 0 {
		t.Fatalf("无记录会话应得空列表，得到 %v", got)
	}

	// 清理：杀掉 sleep 30，不留活进程过测（指针须在 closeAll 清簿前抓取）。
	recSleep := bgTestRecord(t, reg, idSleep)
	reg.closeAll("list-test-cleanup")
	waitFor(t, func() bool { return bgTestFinishedCancelled(recSleep) })
}

func TestBackgroundShellRegistryCloseAll(t *testing.T) {
	reg := newAgentBackgroundShellRegistry()
	id1 := bgTestSpawn(t, reg, "s5", "sleep 30")
	id2 := bgTestSpawn(t, reg, "s5", "sleep 30")
	waitFor(t, func() bool {
		_, st1, ok1 := reg.read("s5", id1)
		_, st2, ok2 := reg.read("s5", id2)
		return ok1 && ok2 && strings.Contains(st1, "running") && strings.Contains(st2, "running")
	})
	// 收口前抓住记录指针：closeAll 清簿后 read 报 not found，终态只能从
	// 指针观测（内核等待 goroutine 会对孤儿记录回调 Finished）。
	rec1 := bgTestRecord(t, reg, id1)
	rec2 := bgTestRecord(t, reg, id2)

	reg.closeAll("test")
	waitFor(t, func() bool { return bgTestFinishedCancelled(rec1) })
	waitFor(t, func() bool { return bgTestFinishedCancelled(rec2) })

	if _, _, ok := reg.read("s5", id1); ok {
		t.Fatal("closeAll 后 read 应报 not found")
	}
	if _, _, ok := reg.read("s5", id2); ok {
		t.Fatal("closeAll 后 read 应报 not found")
	}
	if got := reg.list("s5"); len(got) != 0 {
		t.Fatalf("closeAll 后登记簿应清空，得到 %v", got)
	}
	// 幂等：空簿再收口不 panic。
	reg.closeAll("test-again")
}

func TestBackgroundShellRegistryEvictDead(t *testing.T) {
	reg := newAgentBackgroundShellRegistry()
	reg.capForTest = 2

	// 活锚（sleep 30）+ 三条会自然结束的 echo：死记录累计 3 > cap 2。
	idLive := bgTestSpawn(t, reg, "s6", "sleep 30")
	waitFor(t, func() bool {
		_, status, ok := reg.read("s6", idLive)
		return ok && strings.Contains(status, "running")
	})
	id1 := bgTestSpawn(t, reg, "s6", "echo evict-1")
	id2 := bgTestSpawn(t, reg, "s6", "echo evict-2")
	id3 := bgTestSpawn(t, reg, "s6", "echo evict-3")
	for _, id := range []string{id1, id2, id3} {
		waitFor(t, func() bool {
			_, status, ok := reg.read("s6", id)
			return ok && strings.Contains(status, "completed")
		})
	}

	// 第四条 Begin 触发惰性逐出：最旧的死记录（id1）让位，活记录不受影响。
	id4 := bgTestSpawn(t, reg, "s6", "echo evict-4")
	if _, _, ok := reg.read("s6", id1); ok {
		t.Fatal("超量最旧死记录应被逐出")
	}
	if _, _, ok := reg.read("s6", id2); !ok {
		t.Fatal("cap 内的死记录应保留")
	}
	if _, _, ok := reg.read("s6", idLive); !ok {
		t.Fatal("活记录不得被死记录逐出连带")
	}
	if _, _, ok := reg.read("s6", id4); !ok {
		t.Fatal("新记录应在簿内")
	}

	// 清理：杀掉 sleep 30，不留活进程过测（指针须在 closeAll 清簿前抓取）。
	recLive := bgTestRecord(t, reg, idLive)
	reg.closeAll("evict-test-cleanup")
	waitFor(t, func() bool { return bgTestFinishedCancelled(recLive) })
}

func TestBackgroundShellRegistryFinishedIdempotent(t *testing.T) {
	reg := newAgentBackgroundShellRegistry()
	id := bgTestSpawn(t, reg, "s7", "echo fin-dup")
	// 末批数据与 completed 状态可能同一次 read 返回，轮询期必须累计增量。
	var out strings.Builder
	waitFor(t, func() bool {
		chunk, status, ok := reg.read("s7", id)
		out.WriteString(chunk)
		return ok && strings.Contains(status, "completed")
	})
	if !strings.Contains(out.String(), "fin-dup") {
		t.Fatalf("completed 前后累计应读到全量输出，得到 %q", out.String())
	}
	chunk, _, ok := reg.read("s7", id)
	if !ok {
		t.Fatal("completed 后 read 仍应命中记录")
	}
	if chunk != "" {
		t.Fatalf("收尾后增量应为空，得到 %q", chunk)
	}

	// 防御注入：内核保证恰好一次，本处模拟二次回调不覆盖首终态、不 panic。
	rec := bgTestRecord(t, reg, id)
	rec.Finished(agentcore.ShellFailed, "重复终态（防御注入）")

	rec.mu.Lock()
	calls, outcome, detail := rec.finishCalls, rec.outcome, rec.detail
	rec.mu.Unlock()
	if calls != 2 {
		t.Fatalf("二次回调应记 finishCalls=2，实测 %d", calls)
	}
	if outcome != agentcore.ShellCompleted {
		t.Fatalf("首终态不得被覆盖，outcome=%q", outcome)
	}
	if !strings.Contains(detail, "退出码 0") {
		t.Fatalf("首终态 detail 应保留，得到 %q", detail)
	}
	_, statusAfter, _ := reg.read("s7", id)
	if !strings.HasPrefix(statusAfter, "completed(") || strings.Count(statusAfter, "completed") != 1 {
		t.Fatalf("状态行不得重复/串终态，得到 %q", statusAfter)
	}
}

// stripBgReadMarks 剥掉读侧合成标注（滚标/截断标注），只留净载荷字节——
// 合成环用例按「标注之外的拼接恰为残量」做断言。
func stripBgReadMarks(chunk string) string {
	chunk = strings.ReplaceAll(chunk, agentBackgroundShellRollMark, "")
	chunk = strings.ReplaceAll(chunk,
		fmt.Sprintf(agentBackgroundShellCutMarkFmt, agentcore.MaxToolOutputBytes), "")
	return chunk
}

// TestBackgroundShellRegistryBurstRollResidual 合成环用例 a：单次写入 300KB
// 突发进 256KB 环——滚标必须出现、残量恰为环容量且分次读尽不丢失。
func TestBackgroundShellRegistryBurstRollResidual(t *testing.T) {
	reg := newAgentBackgroundShellRegistry()
	h, id := reg.sessionView("sx").Begin("yes")
	h.Write(bytes.Repeat([]byte{'a'}, 300*1024))

	var payload strings.Builder
	sawRoll, sawCut := false, false
	for i := 0; i < 8; i++ {
		chunk, _, ok := reg.read("sx", id)
		if !ok {
			t.Fatal("read 不应丢失记录")
		}
		sawRoll = sawRoll || strings.Contains(chunk, agentBackgroundShellRollMark)
		sawCut = sawCut || strings.Contains(chunk, "输出超限已截断")
		payload.WriteString(stripBgReadMarks(chunk))
		if chunk == "" {
			break
		}
	}
	if !sawRoll {
		t.Fatal("突发超环应出现滚标")
	}
	if !sawCut {
		t.Fatal("单次读取超 64KB 应出现截断标注")
	}
	if got := payload.String(); got != strings.Repeat("a", 256*1024) {
		t.Fatalf("残量应恰为环容量（%d 字节）且分次读尽不丢失，实得 %d 字节",
			256*1024, len(got))
	}
}

// TestBackgroundShellRegistryReadCapContinue 合成环用例 b：两次 read 之间
// 压入 70KB（>64KB 单次上限、<256KB 环容量，不触发逐出）——首读截断标注、
// 续读恰为余量、再读空 delta。
func TestBackgroundShellRegistryReadCapContinue(t *testing.T) {
	reg := newAgentBackgroundShellRegistry()
	h, id := reg.sessionView("sb").Begin("gen-70k")
	h.Write([]byte(strings.Repeat("b", 70*1024)))

	chunk1, _, ok := reg.read("sb", id)
	if !ok {
		t.Fatal("read 不应丢失记录")
	}
	if want := strings.Repeat("b", 64*1024) +
		fmt.Sprintf(agentBackgroundShellCutMarkFmt, agentcore.MaxToolOutputBytes); chunk1 != want {
		t.Fatalf("首读应为 64KB 净载荷+截断标注，实得 %d 字节", len(chunk1))
	}
	if chunk2, _, _ := reg.read("sb", id); chunk2 != strings.Repeat("b", 6*1024) {
		t.Fatalf("续读应为余量 6KB，实得 %d 字节", len(chunk2))
	}
	if chunk3, _, _ := reg.read("sb", id); chunk3 != "" {
		t.Fatalf("读尽后应为空 delta（增量语义），得到 %q", chunk3)
	}
}

// TestBackgroundShellRegistryRollAfterBackfill 合成环用例 c（Important 1 回归
// 钉子，修复前跑必红）：先读尽 10 字节（cursor=10），再突发 300KB——逐出量
// （45066）>cursor 且回灌使残量（256KB）≥cursor。旧判定 len<cursor 漏检该
// 掩盖形态，静默跳段无滚标；修复后必须滚标+残量完整回放。
func TestBackgroundShellRegistryRollAfterBackfill(t *testing.T) {
	reg := newAgentBackgroundShellRegistry()
	h, id := reg.sessionView("sc").Begin("burst-after-read")
	h.Write([]byte("0123456789"))
	if chunk, _, _ := reg.read("sc", id); chunk != "0123456789" {
		t.Fatalf("前置：首段应读尽，得到 %q", chunk)
	}
	h.Write(bytes.Repeat([]byte{'c'}, 300*1024))

	chunk, _, ok := reg.read("sc", id)
	if !ok {
		t.Fatal("read 不应丢失记录")
	}
	if !strings.Contains(chunk, agentBackgroundShellRollMark) {
		t.Fatalf("逐出+回灌后必须打滚标（不得静默跳段），得到 %q", chunk[:64])
	}
	var payload strings.Builder
	payload.WriteString(stripBgReadMarks(chunk))
	for i := 0; i < 8; i++ {
		c, _, _ := reg.read("sc", id)
		payload.WriteString(stripBgReadMarks(c))
		if c == "" {
			break
		}
	}
	if got := payload.String(); got != strings.Repeat("c", 256*1024) {
		t.Fatalf("残量应完整回放（10 字节首段已全被逐出），实得 %d 字节", len(got))
	}
}

// TestAgentAIManagerBackgroundShellsCloseAll 钉住 manager 侧接线（Step 4.3
// 交付面）：惰性初始化唯一实例、manager 路径收口、nil manager no-op。
func TestAgentAIManagerBackgroundShellsCloseAll(t *testing.T) {
	m := &agentAIManager{}
	if m.backgroundShells != nil {
		t.Fatal("字段应惰性初始化")
	}
	reg1 := m.ensureBackgroundShells()
	reg2 := m.ensureBackgroundShells()
	if reg1 == nil || reg1 != reg2 {
		t.Fatal("惰性构造应返回同一实例")
	}
	// nil manager 不 panic（Shutdown 收口路径的防御契约）。
	var nilManager *agentAIManager
	nilManager.backgroundShellsCloseAll("nil-safe")

	id := bgTestSpawn(t, reg1, "m1", "sleep 30")
	rec := bgTestRecord(t, reg1, id)
	m.backgroundShellsCloseAll("manager-shutdown")
	waitFor(t, func() bool { return bgTestFinishedCancelled(rec) })
	if _, _, ok := reg1.read("m1", id); ok {
		t.Fatal("manager 收口后登记簿应清空")
	}
}
