package services

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"aliang.one/nursorgate/app/agentcore"
)

// waitAgentCoreMemoryTouchForTest 有界等在途注入 touch goroutine 退出。
// 注入块每次命中都起 goroutine(同步段已捕获 dir,不再触碰 dirFn seam),
// 但其写盘晚于测试返回时会与 t.TempDir RemoveAll 相撞(2026-11-10 -race
// 实测:不等待则 dirFn 数据竞态 + RemoveAll 撞写双炸)。与 Task 4 done
// channel 同款 happens-before 纪律。
func waitAgentCoreMemoryTouchForTest(t *testing.T) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		agentCoreMemoryTouchWG.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("在途注入 touch goroutine 2s 未退出")
	}
}

// swapAgentCoreMemoryDirForTest 把包级 store 的目录指向 t.TempDir;测试结束
// 先等在途 touch goroutine 退出再还原(顺序不可换:还原后 goroutine 写盘
// 落错目录,等待则堵住 RemoveAll 撞写窗口)。
func swapAgentCoreMemoryDirForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	restore := AgentCoreMemory.SetDirForTest(dir)
	t.Cleanup(func() {
		// restore 挂 defer:Fatal 的 Goexit 会照跑 defer——否则失败路径
		// dirFn 残留指向已删 TempDir,污染同包后续测试。
		defer restore()
		waitAgentCoreMemoryTouchForTest(t)
	})
	return dir
}

// disableAgentCoreMemoryForTest 关记忆总闸(走产品开关 seam,与生产配置
// memory_enabled=false 同一条代码路径),还原由 t.Cleanup 负责。既有 run 级
// 测试断言上游精确请求次数,而记忆提取是新增副作用(成功 run 收尾后向同一
// 上游多发一次提取请求)——不测提取的测试统一显式关闭。
func disableAgentCoreMemoryForTest(t *testing.T) {
	t.Helper()
	prev := agentCoreMemoryEnabledFn
	agentCoreMemoryEnabledFn = func() bool { return false }
	t.Cleanup(func() { agentCoreMemoryEnabledFn = prev })
}

func TestAgentCoreMemorySlug(t *testing.T) {
	got := agentCoreMemoryProjectSlug("/Users/mac/My Project/x")
	if strings.ContainsAny(got, "/") {
		t.Fatalf("slug 不得含路径分隔符: %q", got)
	}
	if got != agentCoreMemoryProjectSlug("/Users/mac/My Project/x") {
		t.Fatal("slug 必须确定")
	}
}

func TestAgentCoreMemoryRoundTrip(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	e, err := agentCoreMemory.addManual("/p/a", "提交信息全中文")
	if err != nil || e.ID != 1 || e.Source != "manual" {
		t.Fatalf("首条写入: %+v err=%v", e, err)
	}
	got := agentCoreMemory.list("/p/a")
	if len(got) != 1 || got[0].Content != "提交信息全中文" || got[0].ID != 1 {
		t.Fatalf("读回不一致: %+v", got)
	}
	// 落盘文件形态:JSON 可解析、version/project 字段在
	raw, err := os.ReadFile(filepath.Join(agentCoreMemory.dirFn(), "_p_a.json"))
	if err != nil || !strings.Contains(string(raw), `"version"`) {
		t.Fatalf("落盘形态: %v %s", err, raw)
	}
}

func TestAgentCoreMemoryHandEditedBail(t *testing.T) {
	dir := swapAgentCoreMemoryDirForTest(t)
	// 手改文件:空白项/坏 id/重复 id 全丢弃,合法项保留,trim 生效
	p := filepath.Join(dir, "_p_b.json")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(p, []byte(`{"version":1,"project":"/p/b","next_id":9,"memories":[
		{"id":3,"content":" ok ","source":"extract"},
		{"id":0,"content":"bad-id","source":"extract"},
		{"id":3,"content":"dup","source":"extract"},
		{"id":5,"content":"  ","source":"extract"}]}`), 0o644)
	got := agentCoreMemory.list("/p/b")
	if len(got) != 1 || got[0].ID != 3 || got[0].Content != "ok" {
		t.Fatalf("手改兜底: %+v", got)
	}
}

func TestAgentCoreMemoryBrokenFileBak(t *testing.T) {
	dir := swapAgentCoreMemoryDirForTest(t)
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "_p_c.json"), []byte("{not-json"), 0o644)
	_, _ = agentCoreMemory.addManual("/p/c", "after-crash")
	if _, err := os.Stat(filepath.Join(dir, "_p_c.json.bak")); err != nil {
		t.Fatalf("损坏文件必须留 .bak 现场且重建成功: %v", err)
	}
	if len(agentCoreMemory.list("/p/c")) != 1 {
		t.Fatal("重建后新写入必须可读")
	}
}

func TestAgentCoreMemoryNoHomeNoop(t *testing.T) {
	prev := agentCoreMemory.dirFn
	agentCoreMemory.dirFn = func() string { return "" }
	t.Cleanup(func() { agentCoreMemory.dirFn = prev })
	if got := agentCoreMemory.list("/p/x"); len(got) != 0 {
		t.Fatalf("dir 空=list no-op: %+v", got)
	}
	if _, err := agentCoreMemory.addManual("/p/x", "y"); err == nil {
		t.Fatal("dir 空=写必须显式报错而非静默成功")
	}
}

func TestAgentCoreMemoryCapLRU(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	for i := 1; i <= agentCoreMemoryCap; i++ {
		if _, err := agentCoreMemory.addManual("/p/cap", fmt.Sprintf("条目%03d", i)); err != nil {
			t.Fatalf("第 %d 条必须放行: %v", i, err)
		}
	}
	// 注入首条制造「最旧」,再写一条触发 LRU
	agentCoreMemory.touch("/p/cap", []int{agentCoreMemoryCap}) // id=100 变最新
	_, _ = agentCoreMemory.addManual("/p/cap", "新来的")
	got := agentCoreMemory.list("/p/cap")
	if len(got) != agentCoreMemoryCap {
		t.Fatalf("满员仍应 %d 条: %d", agentCoreMemoryCap, len(got))
	}
	for _, e := range got {
		if e.ID == 1 { // touch 把 100 顶新,id=1(未命中最旧)必被淘汰
			t.Fatal("id=1 应已被 LRU 淘汰")
		}
	}
}

func TestAgentCoreMemoryConcurrentWrite(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = agentCoreMemory.addManual("/p/cc", fmt.Sprintf("并发%d", n))
		}(i)
	}
	wg.Wait()
	if got := agentCoreMemory.list("/p/cc"); len(got) != 8 {
		t.Fatalf("并发 8 写应全落: %d", len(got))
	}
}

func TestAgentCoreMemoryProjects(t *testing.T) {
	dir := swapAgentCoreMemoryDirForTest(t)
	if _, err := agentCoreMemory.addManual("/p/alpha", "甲"); err != nil {
		t.Fatalf("写甲: %v", err)
	}
	if _, err := agentCoreMemory.addManual("/p/beta", "乙"); err != nil {
		t.Fatalf("写乙: %v", err)
	}
	got := agentCoreMemory.projects()
	if len(got) != 2 || got[0].Project != "/p/alpha" || got[0].Count != 1 ||
		got[1].Project != "/p/beta" || got[1].Count != 1 {
		t.Fatalf("projects 应两项目按路径有序: %+v", got)
	}
	// 坏 JSON 文件必须被跳过而非拖垮整表
	_ = os.WriteFile(filepath.Join(dir, "zz-broken.json"), []byte("{not-json"), 0o644)
	if got := agentCoreMemory.projects(); len(got) != 2 {
		t.Fatalf("坏文件跳过后仍应 2 项目: %+v", got)
	}
}

func TestAgentCoreMemoryNextIDLift(t *testing.T) {
	dir := swapAgentCoreMemoryDirForTest(t)
	// 手改文件:memories 有 id=1、3,但 next_id=1(≤ 最大 id)——归一必须抬
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "_p_n.json"), []byte(
		`{"version":1,"project":"/p/n","next_id":1,"memories":[
		{"id":1,"content":"a","source":"extract"},
		{"id":3,"content":"b","source":"extract"}]}`), 0o644)
	_ = agentCoreMemory.list("/p/n") // 触发一次 load 归一
	e, err := agentCoreMemory.addManual("/p/n", "新条目")
	if err != nil {
		t.Fatalf("追加失败: %v", err)
	}
	if e.ID <= 3 {
		t.Fatalf("next_id 必须抬到 maxSeen+1=4,拿到 %d", e.ID)
	}
	// 再 load 一轮:新条目绝不能被去重当重复静默丢弃
	got := agentCoreMemory.list("/p/n")
	if len(got) != 3 {
		t.Fatalf("新条目不得被去重丢弃,应 3 条: %+v", got)
	}
}

// kernelEffortRejectionErr 经真内核 client 打一个回 400+effort 字样的本地上游,
// 取回内核自产的 httpStatusError——isEffortRejection 识别的两类错误面字段均未
// 导出,services 测试无法自行拼装;由内核自产保证判定形态零复制(纪律:services
// 禁止复制错误形态识别)。自证:取回的错误必须被内核判为 effort 拒绝。
func kernelEffortRejectionErr(t *testing.T) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"output_config.effort is not supported by this model"}}`))
	}))
	t.Cleanup(srv.Close)
	err := agentcore.NewClient(srv.URL).Stream(context.Background(), &agentcore.MessagesRequest{
		Model:     "m1",
		MaxTokens: 16,
		Messages:  []agentcore.Message{agentcore.TextMessage("user", "hi")},
	}, func(agentcore.StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("预期内核自产 400 错误")
	}
	if !agentcore.IsEffortRejection(err) {
		t.Fatalf("预置错误必须被内核判为 effort 拒绝: %v", err)
	}
	return err
}

// kernelPlainRejectionErr 同法取一个非 effort 拒绝错误(403 且错误体不提 effort
// ——内核 isEffortRejection 对其判 false;403 在内核 isRetryable 也判 false,
// fixture 自身只发一次请求)。
func kernelPlainRejectionErr(t *testing.T) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"type":"error","error":{"type":"permission_error","message":"forbidden"}}`))
	}))
	t.Cleanup(srv.Close)
	err := agentcore.NewClient(srv.URL).Stream(context.Background(), &agentcore.MessagesRequest{
		Model:     "m1",
		MaxTokens: 16,
		Messages:  []agentcore.Message{agentcore.TextMessage("user", "hi")},
	}, func(agentcore.StreamEvent) error { return nil })
	if err == nil {
		t.Fatal("预期内核自产 403 错误")
	}
	if agentcore.IsEffortRejection(err) {
		t.Fatalf("预置错误必须被内核判为非 effort 拒绝: %v", err)
	}
	return err
}

// memorySeamForTest 构造提取调用 seam:按 reject(effort) 决定每档成败,拒绝档
// 返回内核自产错误,成功档返回定值文本+指定 stop_reason;顺带断言 MaxTokens=4096。
// 返回 recorder 取历次尝试的 effort 档(累积,按序)。
func memorySeamForTest(t *testing.T, reject func(eff string) bool, succeedStop string) (agentCoreMemoryStreamFn, func() []string) {
	t.Helper()
	rejErr := kernelEffortRejectionErr(t)
	var mu sync.Mutex
	var calls []string
	fn := func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
		eff := ""
		if req.OutputConfig != nil {
			eff = req.OutputConfig.Effort
		}
		mu.Lock()
		calls = append(calls, eff)
		mu.Unlock()
		if reject(eff) {
			return "", "", rejErr
		}
		if req.MaxTokens != agentCoreMemoryExtractMaxTokens {
			t.Errorf("MaxTokens 必须 %d: %d", agentCoreMemoryExtractMaxTokens, req.MaxTokens)
			return "", "", fmt.Errorf("bad max tokens")
		}
		return `{"ops":[]}`, succeedStop, nil
	}
	return fn, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

// withResetLatch 测试前后重置进程级 effort 锁存(Set 配对位一并清,空串哨兵
// 不可表达「未锁存」)。
func withResetLatch(t *testing.T) {
	t.Helper()
	prevSet, prevEffort := agentCoreMemoryEffortLatchLoad()
	agentCoreMemoryEffortLatchReset()
	t.Cleanup(func() {
		if prevSet {
			agentCoreMemoryEffortLatchStore(prevEffort)
		} else {
			agentCoreMemoryEffortLatchReset()
		}
	})
}

// fake 计数器经闭包带回,seam 内禁 t.Fatalf(纪律②);提取调用同步执行,
// 闭包内 t.Errorf 合法。
func TestAgentCoreMemoryExtractEffortFallback(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	withResetLatch(t)
	m := &agentAIManager{}
	stream, calls := memorySeamForTest(t, func(eff string) bool { return eff == "low" }, agentcore.StopReasonEndTurn)
	m.coreMemoryCallForTest = stream
	eff := agentcore.NormalizeEffortLevel("ultracode") // = "max"
	text, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", eff, "sys", "usr")
	if err != nil || text != `{"ops":[]}` {
		t.Fatalf("low 被拒不降档则失败: %q %v", text, err)
	}
	got := calls()
	if len(got) != 2 || got[0] != "low" || got[1] != "max" {
		t.Fatalf("回退序应为 low→max(归一后 run.effort): %q", got)
	}
	if set, eff := agentCoreMemoryEffortLatchLoad(); !set || eff != "max" {
		t.Fatalf("成功档必须锁存: set=%v %q", set, eff)
	}
	// 第二次调用直接用锁存档,不再试 low
	_, _ = m.agentCoreMemoryExtractCall(context.Background(), "m1", eff, "sys", "usr")
	if got := calls(); len(got) != 3 || got[2] != "max" {
		t.Fatalf("锁存后不得再试 low: %q", got)
	}
}

func TestAgentCoreMemoryExtractMaxTokensGuard(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	withResetLatch(t)
	m := &agentAIManager{}
	stream, calls := memorySeamForTest(t, func(eff string) bool { return false }, agentcore.StopReasonMaxTokens)
	m.coreMemoryCallForTest = stream
	_, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", "", "sys", "usr")
	if err == nil || !strings.Contains(err.Error(), "截断") {
		t.Fatalf("max_tokens 截断必须定性报错: %v", err)
	}
	if len(calls()) != 1 {
		t.Fatalf("截断不得重试: %q", calls())
	}
	if set, _ := agentCoreMemoryEffortLatchLoad(); set {
		t.Fatal("截断丢弃不得锁存档位")
	}
}

func TestAgentCoreMemoryExtractEffortBranches(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	withResetLatch(t)

	t.Run("终档无effort成功也锁存", func(t *testing.T) {
		withResetLatch(t)
		m := &agentAIManager{}
		stream, calls := memorySeamForTest(t, func(eff string) bool { return eff == "low" }, agentcore.StopReasonEndTurn)
		m.coreMemoryCallForTest = stream
		if _, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", "", "sys", "usr"); err != nil {
			t.Fatalf("无 effort 终试应成功: %v", err)
		}
		if got := calls(); len(got) != 2 || got[0] != "low" || got[1] != "" {
			t.Fatalf("候选序应 low→无 effort: %q", got)
		}
		if set, eff := agentCoreMemoryEffortLatchLoad(); !set || eff != "" {
			// 空串哨兵不可表达「未锁存」——Set 位才是锁存标志
			t.Fatalf("空串终档也必须置 Set 位锁存: set=%v %q", set, eff)
		}
		_, _ = m.agentCoreMemoryExtractCall(context.Background(), "m1", "", "sys", "usr")
		if got := calls(); len(got) != 3 || got[2] != "" {
			t.Fatalf("锁存后不得再试 low: %q", got)
		}
	})

	t.Run("run.effort归一为空跳中间档", func(t *testing.T) {
		withResetLatch(t)
		m := &agentAIManager{}
		stream, calls := memorySeamForTest(t, func(eff string) bool { return eff == "low" }, agentcore.StopReasonEndTurn)
		m.coreMemoryCallForTest = stream
		if _, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", "bogus-effort", "sys", "usr"); err != nil {
			t.Fatalf("终档成功: %v", err)
		}
		if got := calls(); len(got) != 2 || got[0] != "low" || got[1] != "" {
			t.Fatalf("归一为空应只两档 low→无 effort: %q", got)
		}
	})

	t.Run("run.effort恰为low去重", func(t *testing.T) {
		withResetLatch(t)
		m := &agentAIManager{}
		stream, calls := memorySeamForTest(t, func(eff string) bool { return eff == "low" }, agentcore.StopReasonEndTurn)
		m.coreMemoryCallForTest = stream
		if _, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", "low", "sys", "usr"); err != nil {
			t.Fatalf("终档成功: %v", err)
		}
		if got := calls(); len(got) != 2 || got[0] != "low" || got[1] != "" {
			t.Fatalf("恰为 low 应去重不重复试: %q", got)
		}
	})

	t.Run("双拒降无effort终试成功", func(t *testing.T) {
		withResetLatch(t)
		m := &agentAIManager{}
		stream, calls := memorySeamForTest(t, func(eff string) bool { return eff == "low" || eff == "max" }, agentcore.StopReasonEndTurn)
		m.coreMemoryCallForTest = stream
		text, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", agentcore.NormalizeEffortLevel("ultracode"), "sys", "usr")
		if err != nil || text != `{"ops":[]}` {
			t.Fatalf("无 effort 终试应成功: %q %v", text, err)
		}
		if got := calls(); len(got) != 3 || got[0] != "low" || got[1] != "max" || got[2] != "" {
			t.Fatalf("三档序应 low→max→无 effort: %q", got)
		}
		if set, eff := agentCoreMemoryEffortLatchLoad(); !set || eff != "" {
			t.Fatalf("终档锁存: set=%v %q", set, eff)
		}
	})

	t.Run("三档全拒返回错误", func(t *testing.T) {
		withResetLatch(t)
		m := &agentAIManager{}
		stream, calls := memorySeamForTest(t, func(eff string) bool { return true }, agentcore.StopReasonEndTurn)
		m.coreMemoryCallForTest = stream
		if _, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", agentcore.NormalizeEffortLevel("ultracode"), "sys", "usr"); err == nil {
			t.Fatal("三档全拒必须报错(Warn 放弃路径)")
		}
		if got := calls(); len(got) != 3 {
			t.Fatalf("全拒应恰试三档: %q", got)
		}
		if set, _ := agentCoreMemoryEffortLatchLoad(); set {
			t.Fatal("全拒不得锁存")
		}
	})

	t.Run("非effort拒绝不重试", func(t *testing.T) {
		withResetLatch(t)
		m := &agentAIManager{}
		plainErr := kernelPlainRejectionErr(t)
		var mu sync.Mutex
		var got []string
		m.coreMemoryCallForTest = func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
			mu.Lock()
			got = append(got, "x")
			mu.Unlock()
			return "", "", plainErr
		}
		_, err := m.agentCoreMemoryExtractCall(context.Background(), "m1", agentcore.NormalizeEffortLevel("ultracode"), "sys", "usr")
		if err == nil || !errors.Is(err, plainErr) {
			t.Fatalf("非 effort 拒绝必须原样返回: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("非 effort 拒绝不得重试,应恰 1 次: %d", len(got))
		}
	})
}

// agentCoreMemoryOp 类型定义在主文件(Task 4 Step 2);测试直接引用同包类型。
func TestAgentCoreMemoryApplyOps(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	_, _ = agentCoreMemory.addManual("/p/d", "旧事实")
	ops := []agentCoreMemoryOp{
		{Op: "update", ID: 1, Content: "旧事实已被纠正"},
		{Op: "add", Content: "新事实"},
		{Op: "add", Content: strings.Repeat("长", 501)}, // 超限丢弃该条
		{Op: "bogus"}, // 非法 op 丢弃
	}
	agentCoreMemory.applyOps("/p/d", ops)
	got := agentCoreMemory.list("/p/d")
	if len(got) != 2 {
		t.Fatalf("应剩 2 条(超限丢弃+非法丢弃): %+v", got)
	}
	// 断言按内容查,不按序(平局 ID 降序的排序规则下新 add 可能排前)
	byContent := map[string]agentCoreMemoryEntry{}
	for _, e := range got {
		byContent[e.Content] = e
	}
	upd, ok := byContent["旧事实已被纠正"]
	if !ok || upd.Source != "manual" || upd.ID != 1 {
		t.Fatalf("update 保 source 与 id: %+v", upd)
	}
	if _, ok := byContent["新事实"]; !ok {
		t.Fatal("add 丢失")
	}
}

func TestAgentCoreMemoryExtractPipelineSkips(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	m := &agentAIManager{}
	called := make(chan struct{}, 1)
	m.coreMemoryCallForTest = func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
		called <- struct{}{}
		// 非空 ops:全空 ops 按契约不落盘,落盘断言需要真实产物
		return `{"ops":[{"op":"add","content":"管线落盘事实"}]}`, "end_turn", nil
	}
	m.agentCoreMemoryExtractAsync("/p/e", "m-x", "", "   ", "   ") // 全空白回合:跳过
	select {
	case <-called:
		t.Fatal("空白回合不得发起提取调用")
	default:
	}
	done := m.agentCoreMemoryExtractAsync("/p/e", "m-x", "", "用户消息", "助手结论")
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("正常回合应发起提取")
	}
	// 完成信号:channel close 建立 happens-before,替代脆弱 sleep/轮询;
	// 超时显式 Fatal,禁止静默通过
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("正常回合提取未完成(2s 超时)")
	}
	if _, err := os.Stat(filepath.Join(agentCoreMemory.dirFn(), "_p_e.json")); err != nil {
		t.Fatalf("正常回合提取未落盘: %v", err)
	}
}

func TestAgentCoreMemoryExtractFailuresWarnOnly(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	m := &agentAIManager{}
	// ① LLM 报错:不 panic、不落盘(Warn 放弃)
	m.coreMemoryCallForTest = func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
		return "", "", fmt.Errorf("boom")
	}
	done := m.agentCoreMemoryExtractAsync("/p/f1", "m-x", "", "u", "a")
	<-done // 完成信号建立 happens-before,替代脆弱 sleep
	if _, err := os.Stat(filepath.Join(agentCoreMemory.dirFn(), "_p_f1.json")); !os.IsNotExist(err) {
		t.Fatal("LLM 失败不得落盘")
	}
	// ② 非法 JSON:同上
	m.coreMemoryCallForTest = func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
		return "不是json", "end_turn", nil
	}
	done = m.agentCoreMemoryExtractAsync("/p/f2", "m-x", "", "u", "a")
	<-done
	if _, err := os.Stat(filepath.Join(agentCoreMemory.dirFn(), "_p_f2.json")); !os.IsNotExist(err) {
		t.Fatal("非法 JSON 不得落盘")
	}
}

func TestAgentCoreMemoryPromptBlock(t *testing.T) {
	dir := swapAgentCoreMemoryDirForTest(t)
	_, _ = agentCoreMemory.addManual("/p/f", "提交信息全中文")
	blk := agentCoreMemoryPromptBlock("/p/f")
	if !strings.Contains(blk, "<memory>") || !strings.Contains(blk, "[1] 提交信息全中文") {
		t.Fatalf("注入块缺失: %q", blk)
	}
	if agentCoreMemoryPromptBlock("/p/none") != "" {
		t.Fatal("空记忆必须零块(prompt 与无记忆逐字节一致)")
	}
	_ = dir
}

func TestAgentCoreMemoryPromptBudget(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	for i := 0; i < 30; i++ {
		_, _ = agentCoreMemory.addManual("/p/g", fmt.Sprintf("条目%03d %s", i, strings.Repeat("内", 300)))
	}
	blk := agentCoreMemoryPromptBlock("/p/g")
	if len([]rune(blk)) > agentCoreMemoryInjectBudgetChars+200 { // 头尾开销余量
		t.Fatalf("超预算: %d", len([]rune(blk)))
	}
	// 新条目(id 大=后写)应保留:块内含最后写入的 id 30,最早的 id 1 被截
	if !strings.Contains(blk, "[30]") {
		t.Fatalf("预算应保最新: %q", blk[:200])
	}
}

// 注入后 touch 生效:last_hit_at 刷新(等异步落盘)
func TestAgentCoreMemoryPromptTouchBumps(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	_, _ = agentCoreMemory.addManual("/p/h", "会命中")
	before := agentCoreMemory.list("/p/h")[0].LastHitAt
	_ = agentCoreMemoryPromptBlock("/p/h")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		got := agentCoreMemory.list("/p/h")
		if len(got) == 1 && got[0].LastHitAt != before && got[0].LastHitAt != "" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("注入后 last_hit_at 必须被异步刷新")
}

// 多条排序:recency 降序 + 平局 ID 降序(后写靠前)
func TestAgentCoreMemoryPromptOrder(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	for i := 1; i <= 3; i++ {
		_, _ = agentCoreMemory.addManual("/p/ord", fmt.Sprintf("条目%d", i))
	}
	blk := agentCoreMemoryPromptBlock("/p/ord")
	if strings.Index(blk, "[3]") > strings.Index(blk, "[1]") {
		t.Fatalf("平局应后写靠前(3 在 1 前): %q", blk)
	}
}

// touch 与 DELETE 竞态:已删条目不得被复活
func TestAgentCoreMemoryTouchAfterDelete(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	e, _ := agentCoreMemory.addManual("/p/td", "将删")
	agentCoreMemory.remove("/p/td", e.ID)
	agentCoreMemory.touch("/p/td", []int{e.ID}) // 在途 touch 命中已删 id
	got := agentCoreMemory.list("/p/td")
	if len(got) != 0 {
		t.Fatalf("已删条目被 touch 复活: %+v", got)
	}
}

// 子 agent prompt 同样带块(同一函数链)
func TestAgentCoreSystemPromptCarriesMemory(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	_, _ = agentCoreMemory.addManual("/p/i", "项目约定X")
	p := agentCoreSystemPrompt("/p/i")
	if !strings.Contains(p, "项目约定X") {
		t.Fatal("子 agent 路径(agentCoreSystemPrompt)也必须注入记忆")
	}
}

// 开关关 = 零提取零注入(seam 替换,双覆盖)
func TestAgentCoreMemoryDisabledKillsBoth(t *testing.T) {
	swapAgentCoreMemoryDirForTest(t)
	prev := agentCoreMemoryEnabledFn
	agentCoreMemoryEnabledFn = func() bool { return false }
	t.Cleanup(func() { agentCoreMemoryEnabledFn = prev })
	if agentCoreMemoryPromptBlock("/p/off") != "" {
		t.Fatal("开关关=注入零块")
	}
	m := &agentAIManager{}
	called := make(chan struct{}, 1)
	m.coreMemoryCallForTest = func(ctx context.Context, req *agentcore.MessagesRequest) (string, string, error) {
		called <- struct{}{}
		return `{"ops":[]}`, "end_turn", nil
	}
	m.agentCoreMemoryExtractAsync("/p/off", "m-x", "", "u", "a")
	select {
	case <-called:
		t.Fatal("开关关=不得发起提取调用")
	case <-time.After(300 * time.Millisecond):
	}
}
