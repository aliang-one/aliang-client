package services

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"aliang.one/nursorgate/app/http/models"
)

// 2026-10 终端回显延迟修复 Stage B 的钉子:
//
//   - terminal.create 整体移出读循环(goroutine + safeGo),回放不再阻塞
//     后续消息;本文件钉 safeGo 的 recover 语义与 beginCreate 的同步登记
//     契约(读循环派发点必须在 go 之前登记,紧随其后的 terminal.input 才
//     有得等)。
//   - creating 注册表:手机先注册占位、键盘已活,可在 terminal.created 之前
//     打字——write 找不到会话时等待在途 create 至多 agentTerminalCreateWait
//     Timeout,而不是立刻报 not found。
//   - attach_token 回显:sendReplay 每 帧(含 final)原样带 token;空 token
//     字段必须缺席(旧 server/手机路径帧字节级不变)。

func TestSafeGoRecoversPanic(t *testing.T) {
	done := make(chan struct{})
	safeGo("test.panic", func() {
		defer close(done)
		panic("boom")
	})
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("safeGo did not run the task")
	}
	// 走到这里说明 panic 被 recover,进程没有死。
}

func TestBeginCreateRegistersSynchronously(t *testing.T) {
	m := newAgentTerminalManager()
	release := m.beginCreate("t-reg")
	// 读循环派发点同步登记:creating 表立即可见,不依赖 goroutine 调度时机。
	m.mu.Lock()
	_, registered := m.creating["t-reg"]
	m.mu.Unlock()
	if !registered {
		t.Fatal("beginCreate did not register synchronously")
	}
	// waitForCreate 只在 create 收尾(channel 关闭)时返回 true。
	if m.waitForCreate("t-reg", 30*time.Millisecond) {
		t.Fatal("waitForCreate returned true while the create was still in flight")
	}
	release()
	m.mu.Lock()
	_, stillThere := m.creating["t-reg"]
	m.mu.Unlock()
	if stillThere {
		t.Fatal("release did not clear the creating entry")
	}
	// release 之后条目已删:再查 waitForCreate 直接 false(注册表语义:
	// settle 与未登记不可区分,write 路径随后以 m.get 会话存在性裁决)。
	if m.waitForCreate("t-reg", 30*time.Millisecond) {
		t.Fatal("waitForCreate returned true with no entry at all")
	}
}

func TestWriteWaitsForInFlightCreate(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	spawner := &fakeTerminalSpawner{}
	// fake 进程必须长活：默认 wait() 立即返回，waitTerminal 会在 create 返回
	// 的瞬间把 sessions 清空，"输入竞速 create" 退化成 "对已死会话输入"——
	// 后者按语义本就该报 not-found（Windows 实测 ~25% 假失败、spawned=1/
	// sessions=0/createdEvents=1 铁证）。长活后本测试才真正守护等待机制。
	blockExit := make(chan struct{})
	spawner.blockExit = blockExit
	t.Cleanup(func() { close(blockExit) })
	m.startProcess = spawner.start
	// 预置授权目录缓存:冷进程里 collectAgentSyncSnapshot 首扫要数秒
	// (测试环境噪音,与被测语义无关)。
	cwd := t.TempDir()
	setAgentAuthorizedExecutionDirectoriesCache([]string{cwd})

	// 复刻 ws 派发点:同步 beginCreate,create 稍后在 goroutine 里跑。
	release := m.beginCreate("t-race")
	created := make(chan struct{})
	go func() {
		defer close(created)
		defer release()
		// shell 走平台默认（resolveAgentShell("")→defaultAgentShell）：
		// "/bin/zsh" 在 Windows 不存在，create 会在 shell 校验处提前失败，
		// 令本测试在 Windows 确定性假失败（实测 3/3）。
		m.create(map[string]interface{}{"session_id": "t-race", "shell": "", "cwd": cwd}, write)
	}()

	// 紧随其后的 terminal.input(读循环内联):必须等到 create 落地,
	// 而不是报 not found。
	m.write(map[string]interface{}{"session_id": "t-race", "data": "echo hi\r"}, write)

	select {
	case <-created:
	case <-time.After(2 * time.Second):
		t.Fatal("create did not finish")
	}
	termErrs := coll.ofTypes(models.AgentEventTerminalError)
	for _, e := range termErrs {
		if strings.Contains(fmt.Sprintf("%v", e["error"]), "not found") {
			m.mu.Lock()
			mk := len(m.sessions)
			ck := len(m.creating)
			m.mu.Unlock()
			var createdSeen int
			for _, p := range coll.snapshot() {
				if p["type"] == models.AgentEventTerminalCreated {
					createdSeen++
				}
			}
			t.Fatalf("input raced the create and errored: %v (create-side errors: %v; spawned=%d sessions=%d creating=%d createdEvents=%d)",
				e, termErrs, spawner.spawned(), mk, ck, createdSeen)
		}
	}
	if spawner.spawned() != 1 {
		t.Fatalf("expected exactly one spawn, got %d", spawner.spawned())
	}
}

func TestWriteErrorsWhenCreateNeverLands(t *testing.T) {
	oldTimeout := agentTerminalCreateWaitTimeout
	agentTerminalCreateWaitTimeout = 50 * time.Millisecond
	defer func() { agentTerminalCreateWaitTimeout = oldTimeout }()

	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()

	// 在途 create 永不落地(派发后 goroutine 死亡等极端情形):write 在
	// 超时后回到既有 not found 语义,不能永久阻塞读循环。
	release := m.beginCreate("t-stuck")
	_ = release
	start := time.Now()
	m.write(map[string]interface{}{"session_id": "t-stuck", "data": "x"}, write)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("write blocked %v; expected ~50ms timeout", elapsed)
	}
	errs := coll.ofTypes(models.AgentEventTerminalError)
	if len(errs) == 0 || !strings.Contains(fmt.Sprintf("%v", errs[0]["error"]), "not found") {
		t.Fatalf("expected a not-found error after the timeout, got %v", errs)
	}
}

func TestSendReplayEchoesAttachToken(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	ring := newTerminalRingBuffer(1 << 20)
	ring.push([]byte("hello"))

	m.sendReplay("t-tok", ring, terminalReplayStatusLive, nil, "tok_abc", write)

	frames := coll.ofTypes(models.AgentEventTerminalReplay)
	if len(frames) == 0 {
		t.Fatal("no replay frames emitted")
	}
	for _, f := range frames {
		if f["attach_token"] != "tok_abc" {
			t.Fatalf("frame missing attach_token echo: %v", f)
		}
	}
	// final 帧也必须带。
	last := frames[len(frames)-1]
	if last["final"] != true {
		t.Fatalf("expected a final frame, got %v", last)
	}
	if last["attach_token"] != "tok_abc" {
		t.Fatalf("final frame missing attach_token echo: %v", last)
	}
}

func TestSendReplayOmitsEmptyAttachToken(t *testing.T) {
	m := newAgentTerminalManager()
	coll, write := newPayloadCollector()
	ring := newTerminalRingBuffer(1 << 20)
	ring.push([]byte("hello"))

	m.sendReplay("t-tok", ring, terminalReplayStatusLive, nil, "", write)

	for _, f := range coll.ofTypes(models.AgentEventTerminalReplay) {
		if _, ok := f["attach_token"]; ok {
			t.Fatalf("empty token must omit the field entirely (byte-identical legacy frames): %v", f)
		}
	}
}
